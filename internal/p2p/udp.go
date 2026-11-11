package p2p

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Over UDP a message may be lost, doubled or reordered, so each pipe that
// carries a session numbers its packets, acknowledges them, and sends the
// missing ones again. What the session above sees is a net.Conn: an
// ordered stream of bytes, which is what the handshake and the frames
// expect.

const (
	pktData  = 1
	pktAck   = 2
	pktPunch = 3
	pktFin   = 4

	// maxPayload keeps a datagram under the smallest MTU a path is likely
	// to have, once the header and the encryption above are counted.
	maxPayload = 1100
	// defaultMaxRetries is how often a packet is sent again before the
	// path is called dead: at ~200 ms each that is several seconds of
	// silence, enough for a Wi-Fi handover, and -retries changes it.
	defaultMaxRetries = 20
	// receiveWindow bounds how far ahead of the reader a sender may run;
	// anything further is dropped rather than buffered forever.
	receiveWindow = 512
)

var errPipeClosed = errors.New("p2p: udp pipe closed")

// udpHub owns the socket, and hands the datagrams of each peer to its pipe.
type udpHub struct {
	conn *net.UDPConn
	mu   sync.Mutex
	pipe map[string]*udpPipe
	// onUnknown sees a datagram from an address without a pipe, which is a
	// peer punching towards this node.
	onUnknown func(addr *net.UDPAddr, b []byte)
	// inbox carries the datagrams of unknown addresses to whoever waits
	// for them, as a STUN answer does.
	inbox  chan inbound
	closed chan struct{}
	once   sync.Once
	// maxRetries bounds how often one packet is sent again before the pipe
	// is given up on.
	maxRetries int
}

type inbound struct {
	data []byte
	addr *net.UDPAddr
}

func newHub(port int) (*udpHub, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, err
	}
	_ = conn.SetReadBuffer(1 << 20)
	_ = conn.SetWriteBuffer(1 << 20)
	h := &udpHub{
		conn:       conn,
		pipe:       map[string]*udpPipe{},
		inbox:      make(chan inbound, 64),
		closed:     make(chan struct{}),
		maxRetries: defaultMaxRetries,
	}
	go h.loop()
	return h, nil
}

// port is the local port the hub listens on.
func (h *udpHub) port() int {
	if ua, ok := h.conn.LocalAddr().(*net.UDPAddr); ok {
		return ua.Port
	}
	return 0
}

func (h *udpHub) close() {
	h.once.Do(func() {
		close(h.closed)
		_ = h.conn.Close()
	})
}

func (h *udpHub) loop() {
	buf := make([]byte, 65535)
	for {
		n, from, err := h.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-h.closed:
				return
			default:
				continue
			}
		}
		key := from.String()
		h.mu.Lock()
		p := h.pipe[key]
		h.mu.Unlock()
		if p != nil {
			p.feed(buf[:n])
			continue
		}
		if n > 0 {
			select {
			case h.inbox <- inbound{data: append([]byte(nil), buf[:n]...), addr: from}:
			default:
			}
		}
		if h.onUnknown != nil && n > 0 && buf[0] == pktPunch {
			h.onUnknown(from, append([]byte(nil), buf[:n]...))
		}
	}
}

// open makes a pipe to an address, or returns the one already there.
func (h *udpHub) open(addr *net.UDPAddr) *udpPipe {
	key := addr.String()
	h.mu.Lock()
	defer h.mu.Unlock()
	if p, ok := h.pipe[key]; ok {
		return p
	}
	p := &udpPipe{
		hub:        h,
		addr:       addr,
		in:         make(chan []byte, 256),
		done:       make(chan struct{}),
		unacked:    map[uint32]*outPacket{},
		rbuf:       map[uint32][]byte{},
		cc:         newCongestion(),
		maxRetries: h.maxRetries,
	}
	h.pipe[key] = p
	go p.retransmit()
	return p
}

func (h *udpHub) forget(p *udpPipe) {
	h.mu.Lock()
	if cur, ok := h.pipe[p.addr.String()]; ok && cur == p {
		delete(h.pipe, p.addr.String())
	}
	h.mu.Unlock()
}

func (h *udpHub) send(addr *net.UDPAddr, b []byte) error {
	_, err := h.conn.WriteToUDP(b, addr)
	return err
}

// punch sends an empty datagram that opens the path through both NATs,
// and is itself the signal: it needs no acknowledgement.
// recv takes the next datagram of an unknown address, as a STUN answer is.
func (h *udpHub) recv(d time.Duration) ([]byte, *net.UDPAddr, error) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case in := <-h.inbox:
		return in.data, in.addr, nil
	case <-t.C:
		return nil, nil, os.ErrDeadlineExceeded
	case <-h.closed:
		return nil, nil, errPipeClosed
	}
}

func (h *udpHub) punch(addr *net.UDPAddr) {
	pkt := make([]byte, 5)
	pkt[0] = pktPunch
	_ = h.send(addr, pkt)
}

type outPacket struct {
	data  []byte
	at    time.Time
	tries int
	// retx marks a packet that was sent again, whose round trip says
	// nothing reliable about the path (Karn's rule).
	retx bool
}

// udpPipe is one peer's stream over the hub's socket.
type udpPipe struct {
	hub  *udpHub
	addr *net.UDPAddr

	in     chan []byte
	done   chan struct{}
	closed atomic.Bool

	wmu sync.Mutex
	// cc decides how much may be in flight, so that a big transfer does
	// not starve the chat sharing the same link.
	cc         *congestion
	maxRetries int
	wseq       uint32
	unacked    map[uint32]*outPacket

	rmu        sync.Mutex
	rnext      uint32
	rbuf       map[uint32][]byte
	pending    []byte
	readDline  time.Time
	writeDline time.Time

	seen atomic.Bool // a packet arrived: the punch worked
}

func (p *udpPipe) feed(b []byte) {
	if len(b) == 0 || p.closed.Load() {
		return
	}
	// A punch is five bytes: too short for a packet, and all it says is
	// that the path is open.
	if b[0] == pktPunch {
		p.seen.Store(true)
		return
	}
	if len(b) < 9 {
		return
	}
	switch b[0] {
	case pktAck:
		p.onAck(binary.BigEndian.Uint32(b[5:9]))
	case pktFin:
		p.closeLocal()
	case pktData:
		seq := binary.BigEndian.Uint32(b[1:5])
		payload := append([]byte(nil), b[9:]...)
		p.seen.Store(true)

		p.rmu.Lock()
		if seq >= p.rnext && seq < p.rnext+receiveWindow {
			if seq == p.rnext {
				p.pending = append(p.pending, payload...)
				p.rnext++
				for {
					next, ok := p.rbuf[p.rnext]
					if !ok {
						break
					}
					p.pending = append(p.pending, next...)
					delete(p.rbuf, p.rnext)
					p.rnext++
				}
			} else if _, dup := p.rbuf[seq]; !dup {
				p.rbuf[seq] = payload
			}
		}
		next := p.rnext
		p.rmu.Unlock()
		p.sendAck(next)
		select {
		case p.in <- nil: // wake a reader waiting for bytes
		default:
		}
	}
}

func (p *udpPipe) sendAck(next uint32) {
	pkt := make([]byte, 9)
	pkt[0] = pktAck
	binary.BigEndian.PutUint32(pkt[5:9], next)
	_ = p.hub.send(p.addr, pkt)
}

func (p *udpPipe) retransmit() {
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-t.C:
			p.wmu.Lock()
			var seq uint32
			var oldest *outPacket
			for s, out := range p.unacked {
				if oldest == nil || s < seq {
					oldest, seq = out, s
				}
			}
			if oldest == nil || time.Since(oldest.at) < p.cc.rto() {
				p.wmu.Unlock()
				continue
			}
			if oldest.tries >= p.maxRetries {
				p.wmu.Unlock()
				p.closeLocal()
				return
			}
			// One packet is enough: its acknowledgement moves the whole
			// window forward, and the rest follow by themselves.
			oldest.tries++
			oldest.retx = true
			oldest.at = time.Now()
			p.cc.onTimeout()
			data := oldest.data
			p.wmu.Unlock()
			_ = p.hub.send(p.addr, data)
		}
	}
}

// onAck takes an acknowledgement: it measures the path, grows the window,
// and starts a fast retransmission when three duplicates say a packet went
// missing.
func (p *udpPipe) onAck(ack uint32) {
	now := time.Now()
	p.wmu.Lock()
	defer p.wmu.Unlock()

	acked := 0
	for seq, out := range p.unacked {
		if seq >= ack {
			continue
		}
		if !out.retx {
			p.cc.sample(now.Sub(out.at))
		}
		delete(p.unacked, seq)
		acked++
	}
	p.cc.onAck(acked)

	if acked == 0 && ack == p.cc.lastAck {
		p.cc.dupAcks++
		if p.cc.dupAcks == 3 {
			if out, ok := p.unacked[ack]; ok {
				out.retx = true
				out.tries++
				out.at = now
				p.cc.onLoss()
				_ = p.hub.send(p.addr, out.data)
			}
		}
	} else if acked > 0 {
		p.cc.dupAcks = 0
	}
	p.cc.lastAck = ack
}

// Write sends the bytes as data packets.
func (p *udpPipe) Write(b []byte) (int, error) {
	if p.closed.Load() {
		return 0, errPipeClosed
	}
	written := 0
	for len(b) > 0 {
		n := len(b)
		if n > maxPayload {
			n = maxPayload
		}
		p.wmu.Lock()
		// Wait for room in the window rather than run ahead blind; the
		// window itself grows as acknowledgements come back.
		for len(p.unacked) >= p.cc.limit() {
			p.wmu.Unlock()
			select {
			case <-p.done:
				return written, errPipeClosed
			case <-time.After(20 * time.Millisecond):
			}
			p.wmu.Lock()
		}
		seq := p.wseq
		p.wseq++
		pkt := make([]byte, 9+n)
		pkt[0] = pktData
		binary.BigEndian.PutUint32(pkt[1:5], seq)
		copy(pkt[9:], b[:n])
		p.unacked[seq] = &outPacket{data: pkt, at: time.Now()}
		p.wmu.Unlock()

		if err := p.hub.send(p.addr, pkt); err != nil {
			return written, err
		}
		written += n
		b = b[n:]
	}
	return written, nil
}

// Read gives the bytes that arrived in order, waiting for them.
func (p *udpPipe) Read(b []byte) (int, error) {
	for {
		p.rmu.Lock()
		if len(p.pending) > 0 {
			n := copy(b, p.pending)
			p.pending = p.pending[n:]
			p.rmu.Unlock()
			return n, nil
		}
		deadline := p.readDline
		p.rmu.Unlock()

		var expired <-chan time.Time
		if !deadline.IsZero() {
			d := time.Until(deadline)
			if d <= 0 {
				return 0, os.ErrDeadlineExceeded
			}
			t := time.NewTimer(d)
			defer t.Stop()
			expired = t.C
		}

		select {
		case <-p.in:
		case <-expired:
			return 0, os.ErrDeadlineExceeded
		case <-p.done:
			return 0, io.EOF
		}
	}
}

// Close tells the peer and drops the pipe.
func (p *udpPipe) Close() error {
	if p.closed.Load() {
		return nil
	}
	fin := make([]byte, 9)
	fin[0] = pktFin
	_ = p.hub.send(p.addr, fin)
	p.closeLocal()
	return nil
}

func (p *udpPipe) closeLocal() {
	if p.closed.Swap(true) {
		return
	}
	close(p.done)
	p.hub.forget(p)
}

func (p *udpPipe) LocalAddr() net.Addr  { return p.hub.conn.LocalAddr() }
func (p *udpPipe) RemoteAddr() net.Addr { return p.addr }

func (p *udpPipe) SetDeadline(t time.Time) error {
	p.rmu.Lock()
	p.readDline = t
	p.rmu.Unlock()
	p.wmu.Lock()
	p.writeDline = t
	p.wmu.Unlock()
	return nil
}

func (p *udpPipe) SetReadDeadline(t time.Time) error {
	p.rmu.Lock()
	p.readDline = t
	p.rmu.Unlock()
	return nil
}

func (p *udpPipe) SetWriteDeadline(t time.Time) error {
	p.wmu.Lock()
	p.writeDline = t
	p.wmu.Unlock()
	return nil
}
