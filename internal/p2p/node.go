// Package p2p carries the mesh: encrypted connections between peers, the
// gossip that relays a message through them, and the discovery that finds
// them on the local network.
package p2p

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"ptp_chat/internal/identity"
	"ptp_chat/internal/wire"
)

// Kind tells what happened.
type Kind int

const (
	// PeerJoined reports a new connection.
	PeerJoined Kind = iota
	// PeerLeft reports a connection that closed.
	PeerLeft
	// Message reports a chat message, from a peer or relayed by one.
	Message
	// TypingNow reports that a peer is writing.
	TypingNow
	// FileOffer reports a file a peer wants to send.
	FileOffer
	// FileProgress reports bytes moving, and FileDone or FileFailed the
	// end of it.
	FileProgress
	FileDone
	FileFailed
)

// PeerInfo describes a peer for the interface.
type PeerInfo struct {
	ID   string
	Name string
	Addr string
	RTT  time.Duration
	// UDP tells that the peer was reached by punching through its NAT.
	UDP bool
	// Avatar is the look the peer chose, "colour|glyph".
	Avatar string
}

// Event is what the node tells its app.
type Event struct {
	Kind Kind
	Peer PeerInfo
	Env  wire.Envelope
	// File is set for the file events.
	File *FileState
}

// Node is this peer in the mesh.
type Node struct {
	ID   *identity.Identity
	Port int
	Addr string

	OnEvent func(Event)
	Logf    func(string, ...any)

	ln       net.Listener
	mu       sync.Mutex
	peers    map[string]*Peer
	seen     map[string]time.Time
	dialing  map[string]bool
	pings    map[string]time.Time
	events   chan Event
	closed   chan struct{}
	once     sync.Once
	disc     func()
	host     string
	discPort int

	// reach is the rendezvous on other networks, and the port opened for it.
	reach reachState

	// hub carries the sessions of peers reached by punching through their
	// NATs, which no TCP connection could cross.
	hub      *udpHub
	punching map[string]bool
	// retries bounds the resends of one packet before a path is dropped.
	retries int

	// reLookup asks the rendezvous for fresh endpoints, so that a punch
	// that timed out can try the peer's new address at once instead of
	// waiting for the next scheduled lookup.
	reLookup   func()
	lastLookup time.Time

	// warmEvery is how often a punched peer gets a keepalive packet; a NAT
	// forgets a mapping that stays quiet, and that mapping is per peer.
	warmEvery time.Duration
	// warm counts the keepalives sent, which a test can watch.
	warm atomic.Int64

	// files are the transfers in flight, offered or under way.
	fmu   sync.Mutex
	files map[string]*fileTransfer
}

// New makes a node for an identity, listening on port (0 picks one).
func New(id *identity.Identity, port int) *Node {
	n := &Node{
		ID:       id,
		Port:     port,
		peers:    map[string]*Peer{},
		seen:     map[string]time.Time{},
		dialing:  map[string]bool{},
		pings:    map[string]time.Time{},
		punching: map[string]bool{},
		events:   make(chan Event, 256),
		closed:   make(chan struct{}),
		Logf:     func(string, ...any) {},
	}
	return n
}

// Start listens for peers and dispatches what they say.
func (n *Node) Start() error {
	ln, err := net.Listen("tcp4", ":"+strconv.Itoa(n.Port))
	if err != nil {
		return err
	}
	n.ln = ln
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		n.Port = addr.Port
	}
	n.host = BestLANIP()
	n.Addr = net.JoinHostPort(n.host, strconv.Itoa(n.Port))
	go n.acceptLoop()
	go n.dispatch()
	go n.pingLoop()
	return nil
}

// SetDiscovery stops discovery when it closes.
func (n *Node) SetDiscovery(stop func()) { n.disc = stop }

// Discovery names the port discovery listens on, empty when it is off.
func (n *Node) Discovery() string {
	if n.discPort == 0 {
		return ""
	}
	return "udp " + strconv.Itoa(n.discPort)
}

// ErrClosed is returned by a node that stopped.
var ErrClosed = errors.New("p2p: node closed")

// Close ends every connection.
func (n *Node) Close() {
	n.once.Do(func() {
		close(n.closed)
		n.mu.Lock()
		for _, p := range n.peers {
			p.Close()
		}
		n.peers = map[string]*Peer{}
		n.mu.Unlock()
		if n.ln != nil {
			_ = n.ln.Close()
		}
		if n.hub != nil {
			n.hub.close()
		}
		if n.disc != nil {
			n.disc()
		}
		n.StopReach()
	})
}

func (n *Node) logf(format string, args ...any) {
	if n.Logf != nil {
		n.Logf(format, args...)
	}
}

func (n *Node) dispatch() {
	for {
		select {
		case <-n.closed:
			return
		case ev := <-n.events:
			if n.OnEvent != nil {
				n.OnEvent(ev)
			}
		}
	}
}

func (n *Node) emit(ev Event) {
	select {
	case n.events <- ev:
	case <-n.closed:
	default: // the interface is behind; drop rather than stall the mesh
	}
}

func (n *Node) acceptLoop() {
	for {
		conn, err := n.ln.Accept()
		if err != nil {
			select {
			case <-n.closed:
				return
			default:
			}
			continue
		}
		go func() {
			p, _, err := handshake(conn, n.ID, n.Port, n.Addr)
			if err != nil {
				_ = conn.Close()
				n.logf("握手失败: %v", err)
				return
			}
			p.Outbound = false
			n.addPeer(p)
		}()
	}
}

// Connect dials a peer at addr, "host:port", unless it is already dialing
// it or connected to it.
func (n *Node) Connect(addr string) {
	if addr == "" || addr == n.Addr {
		return
	}
	n.mu.Lock()
	if n.dialing[addr] {
		n.mu.Unlock()
		return
	}
	for _, p := range n.peers {
		if p.Addr == addr {
			n.mu.Unlock()
			return
		}
	}
	n.dialing[addr] = true
	n.mu.Unlock()

	go func() {
		defer func() {
			n.mu.Lock()
			delete(n.dialing, addr)
			n.mu.Unlock()
		}()
		d := net.Dialer{Timeout: 6 * time.Second}
		conn, err := d.Dial("tcp4", addr)
		if err != nil {
			n.logf("%s: %v", addr, err)
			return
		}
		p, _, err := handshake(conn, n.ID, n.Port, n.Addr)
		if err != nil {
			_ = conn.Close()
			n.logf("%s: %v", addr, err)
			return
		}
		p.Outbound = true
		n.addPeer(p)
	}()
}

func (n *Node) addPeer(p *Peer) {
	n.mu.Lock()
	if old, ok := n.peers[p.ID]; ok {
		// Both ends dialed at once: keep the connection the smaller peer
		// opened, so that the two agree on which to drop.
		keepOutbound := n.ID.ID() < p.ID
		if p.Outbound != keepOutbound {
			n.mu.Unlock()
			p.Close()
			return
		}
		delete(n.peers, p.ID)
		old.Close()
	}
	n.peers[p.ID] = p
	addrs := n.addrsLocked()
	n.mu.Unlock()

	n.logf("已连接 %s (%s)", p.Name, p.Addr)
	n.emit(Event{Kind: PeerJoined, Peer: p.info()})
	go n.readLoop(p)

	// A punched peer is already on the far side of its NAT: the addresses
	// this node knows would only make it dial into nothing.
	if p.UDP {
		return
	}

	// Introduce ourselves and hand over what we know, so the mesh closes.
	env := &wire.Envelope{
		V: wire.Version, Type: wire.Hello, ID: wire.NewID(),
		From: n.ID.ID(), Name: n.ID.Name, Addr: n.Addr,
		TS: time.Now().UnixMilli(),
	}
	env.List = addrs
	_ = p.Send(env)
	n.announcePeers(addrs)
}

func (n *Node) dropPeer(p *Peer, why string) {
	p.Close()
	n.mu.Lock()
	if cur, ok := n.peers[p.ID]; ok && cur == p {
		delete(n.peers, p.ID)
		n.mu.Unlock()
		n.logf("断开 %s: %s", p.Name, why)
		n.emit(Event{Kind: PeerLeft, Peer: p.info()})
		// A punched path dies with its NAT mapping, and waiting for the next
		// lookup (45 seconds) to find the peer again would feel broken: try
		// the address it had, a few times, before leaving it to the swarm.
		if p.UDP {
			go n.repunch(p.ID, p.Addr)
		}
		return
	}
	n.mu.Unlock()
}

// repunch tries a lost peer's last address again, backing off.
func (n *Node) repunch(id, addr string) {
	for _, wait := range []time.Duration{1 * time.Second, 3 * time.Second, 8 * time.Second, 20 * time.Second, 45 * time.Second} {
		time.Sleep(wait)
		n.mu.Lock()
		_, alive := n.peers[id]
		n.mu.Unlock()
		if alive {
			return
		}
		n.logf("重连 %s（%s）", addr, id[:6])
		n.Punch(addr)
		// Give the punch its moment before deciding it did not take.
		time.Sleep(8 * time.Second)
		n.mu.Lock()
		_, alive = n.peers[id]
		n.mu.Unlock()
		if alive {
			return
		}
	}
}

func (p *Peer) info() PeerInfo {
	return PeerInfo{ID: p.ID, Name: p.Name, Addr: p.Addr, RTT: p.RTT(), UDP: p.UDP, Avatar: p.Avatar}
}

// Peers lists the connections, newest last.
func (n *Node) Peers() []PeerInfo {
	n.mu.Lock()
	out := make([]PeerInfo, 0, len(n.peers))
	for _, p := range n.peers {
		out = append(out, p.info())
	}
	n.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Broadcast signs a message and sends it to every connection.
func (n *Node) Broadcast(env *wire.Envelope) {
	env.V = wire.Version
	if env.ID == "" {
		env.ID = wire.NewID()
	}
	env.From = n.ID.ID()
	env.Name = n.ID.Name
	env.Avatar = n.ID.Avatar
	if env.TS == 0 {
		env.TS = time.Now().UnixMilli()
	}
	env.TTL = wire.MaxTTL
	env.Sign(n.ID.Key(), n.ID.Public())
	n.markSeen(env.ID)

	// A message with a recipient is between the two of them: it goes to that
	// connection alone, and nobody passes it on.
	if env.To != "" {
		n.mu.Lock()
		p := n.peers[env.To]
		n.mu.Unlock()
		if p != nil {
			_ = p.Send(env)
		}
		return
	}
	n.relay(env, "")
}

// SendTo sends an envelope to one peer alone.
func (n *Node) SendTo(peerID string, env *wire.Envelope) {
	n.mu.Lock()
	p := n.peers[peerID]
	n.mu.Unlock()
	if p == nil {
		return
	}
	env.V = wire.Version
	env.From = n.ID.ID()
	env.Name = n.ID.Name
	if env.TS == 0 {
		env.TS = time.Now().UnixMilli()
	}
	_ = p.Send(env)
}

func (n *Node) relay(env *wire.Envelope, skip string) {
	n.mu.Lock()
	targets := make([]*Peer, 0, len(n.peers))
	for id, p := range n.peers {
		if id == skip {
			continue
		}
		targets = append(targets, p)
	}
	n.mu.Unlock()
	for _, p := range targets {
		_ = p.Send(env)
	}
}

// RefreshName tells the peers the name this node goes by now.
func (n *Node) RefreshName() {
	env := &wire.Envelope{
		V: wire.Version, Type: wire.Hello, ID: wire.NewID(),
		From: n.ID.ID(), Name: n.ID.Name, Avatar: n.ID.Avatar,
		TS: time.Now().UnixMilli(),
	}
	n.relay(env, "")
}

func (n *Node) announcePeers(addrs []string) {
	if len(addrs) == 0 {
		return
	}
	env := &wire.Envelope{
		V: wire.Version, Type: wire.Peers, ID: wire.NewID(),
		From: n.ID.ID(), Name: n.ID.Name, TS: time.Now().UnixMilli(),
	}
	env.List = addrs
	n.relay(env, "")
}

func (n *Node) addrsLocked() []string {
	out := make([]string, 0, len(n.peers)+1)
	if n.Addr != "" {
		out = append(out, n.Addr)
	}
	for _, p := range n.peers {
		// A punched peer's address is private to the two NATs that made it,
		// so it is never handed to a third peer.
		if p.Addr != "" && !p.UDP {
			out = append(out, p.Addr)
		}
	}
	return out
}

func (n *Node) markSeen(id string) {
	if id == "" {
		return
	}
	n.mu.Lock()
	n.seen[id] = time.Now()
	if len(n.seen) > 4096 {
		cut := time.Now().Add(-5 * time.Minute)
		for k, t := range n.seen {
			if t.Before(cut) {
				delete(n.seen, k)
			}
		}
	}
	n.mu.Unlock()
}

func (n *Node) firstSeen(id string) bool {
	if id == "" {
		return false
	}
	n.mu.Lock()
	_, ok := n.seen[id]
	if !ok {
		n.seen[id] = time.Now()
	}
	n.mu.Unlock()
	return !ok
}

func (n *Node) handle(env *wire.Envelope, from *Peer) {
	switch env.Type {
	case wire.Ping:
		_ = from.Send(&wire.Envelope{
			V: wire.Version, Type: wire.Pong, ID: env.ID,
			From: n.ID.ID(), TS: env.TS, Body: env.Body,
		})
		return
	case wire.Pong:
		n.mu.Lock()
		sent, ok := n.pings[env.Body]
		delete(n.pings, env.Body)
		n.mu.Unlock()
		if ok {
			from.rtt.Store(int64(time.Since(sent)))
		}
		return
	case wire.Hello:
		if env.Name != "" {
			from.Name = env.Name
		}
		if env.Avatar != "" {
			from.Avatar = env.Avatar
		}
		n.dialAll(env.List)
		return
	case wire.Peers:
		n.dialAll(env.List)
		return
	case wire.Bye:
		n.dropPeer(from, "对方离开")
		return
	case wire.File:
		n.onFileMsg(from, env)
		return
	case wire.Chat, wire.Typing:
		if env.To != "" && env.To != n.ID.ID() {
			return
		}
		if !n.firstSeen(env.ID) {
			return
		}
		if err := env.Verify(); err != nil {
			n.logf("丢弃了一条未通过签名的消息: %v", err)
			return
		}
		pub, err := base64.StdEncoding.DecodeString(env.PK)
		if err != nil || len(pub) != ed25519.PublicKeySize || identity.ShortID(pub) != env.From {
			n.logf("丢弃了一条身份不符的消息")
			return
		}
		if env.Type == wire.Typing {
			n.emit(Event{Kind: TypingNow, Peer: PeerInfo{ID: env.From, Name: env.Name}, Env: *env})
			return
		}
		n.emit(Event{
			Kind: Message,
			Peer: PeerInfo{ID: env.From, Name: env.Name, Addr: from.Addr},
			Env:  *env,
		})
		if env.TTL > 1 && env.To == "" {
			env.TTL--
			n.relay(env, from.ID)
		}
	}
}

func (n *Node) dialAll(addrs []string) {
	for _, addr := range addrs {
		if addr == "" || addr == n.Addr {
			continue
		}
		go n.Connect(addr)
	}
}

func (n *Node) pingLoop() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-n.closed:
			return
		case <-t.C:
			n.mu.Lock()
			targets := make([]*Peer, 0, len(n.peers))
			for _, p := range n.peers {
				targets = append(targets, p)
			}
			n.mu.Unlock()
			for _, p := range targets {
				id := wire.NewID()
				n.mu.Lock()
				n.pings[id] = time.Now()
				n.mu.Unlock()
				_ = p.Send(&wire.Envelope{
					V: wire.Version, Type: wire.Ping, ID: id,
					From: n.ID.ID(), Body: id, TS: time.Now().UnixMilli(),
				})
			}
		}
	}
}
