package p2p

import (
	"errors"
	"net"
	"strconv"
	"time"
)

// StartUDP opens the socket peers punch towards, on port (0 picks one),
// and answers the peers that punch first.
func (n *Node) StartUDP(port int) error {
	hub, err := newHub(port)
	if err != nil {
		return err
	}
	hub.maxRetries = n.transportRetries()
	n.hub = hub
	hub.onUnknown = func(addr *net.UDPAddr, b []byte) {
		// Someone is punching towards this node: punch back, so that both
		// routers have a way through, and let the handshake follow.
		n.Punch(addr.String())
	}
	return nil
}

// SetTransport tunes the punched path: how often a packet is sent again
// before the path is called dead. Bigger numbers ride out longer outages at
// the cost of noticing a broken peer later.
func (n *Node) SetTransport(retries int) error {
	if retries < 1 {
		return errors.New("p2p: retries must be at least 1")
	}
	n.mu.Lock()
	n.retries = retries
	hub := n.hub
	n.mu.Unlock()
	if hub != nil {
		hub.mu.Lock()
		hub.maxRetries = retries
		hub.mu.Unlock()
	}
	return nil
}

// transportRetries is the configured limit, or the default.
func (n *Node) transportRetries() int {
	n.mu.Lock()
	r := n.retries
	n.mu.Unlock()
	if r < 1 {
		return defaultMaxRetries
	}
	return r
}

// UDPPort is the port peers punch towards, 0 when the socket is closed.
func (n *Node) UDPPort() int {
	if n.hub == nil {
		return 0
	}
	return n.hub.port()
}

// Punch opens a session with a peer reachable only over UDP: it sends
// datagrams until the peer's arrive, then hands the path to the handshake.
func (n *Node) Punch(addr string) {
	if n.hub == nil {
		n.logf("打洞跳过 %s：UDP 套接字未就绪", addr)
		return
	}
	ua, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil || ua.Port == 0 {
		n.logf("打洞跳过 %s：地址无效 (%v)", addr, err)
		return
	}
	n.mu.Lock()
	for _, p := range n.peers {
		if p.Addr == addr {
			n.mu.Unlock()
			return
		}
	}
	if n.punching == nil {
		n.punching = map[string]bool{}
	}
	if n.punching[addr] {
		n.mu.Unlock()
		return
	}
	n.punching[addr] = true
	n.mu.Unlock()

	hub := n.hub
	n.logf("向 %s 打洞", addr)
	go func() {
		defer func() {
			n.mu.Lock()
			delete(n.punching, addr)
			n.mu.Unlock()
		}()
		pipe := hub.open(ua)
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) && !pipe.seen.Load() {
			hub.punch(ua)
			time.Sleep(200 * time.Millisecond)
		}
		if !pipe.seen.Load() {
			n.logf("打洞超时: %s", addr)
			pipe.Close()
			return
		}
		peer, _, err := handshake(pipe, n.ID, n.Port, n.Addr)
		if err != nil {
			n.logf("打洞后握手失败 %s: %v", addr, err)
			pipe.Close()
			return
		}
		// The address of a punched peer is the one its NAT gave it, which
		// no other peer can use: it is not handed on.
		peer.Addr = addr
		peer.UDP = true
		peer.Outbound = true
		n.addPeer(peer)
	}()
}

// udpStatus tells whether the punch socket runs.
func (n *Node) udpStatus() string {
	if n.hub == nil {
		return ""
	}
	return "udp " + strconv.Itoa(n.hub.port())
}
