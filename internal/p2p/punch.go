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
	n.mu.Lock()
	every := n.warmEvery
	n.mu.Unlock()
	go n.keepPeersWarm(every)
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
		// A burst first: the two sides have to be sending while the other
		// one's mapping exists, and a tight opening window makes that far
		// likelier than one packet every 200 ms.
		for i := 0; i < 12 && !pipe.seen.Load(); i++ {
			hub.punch(ua)
			time.Sleep(30 * time.Millisecond)
		}
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) && !pipe.seen.Load() {
			hub.punch(ua)
			time.Sleep(200 * time.Millisecond)
		}
		if !pipe.seen.Load() {
			n.logf("打洞超时: %s", addr)
			pipe.Close()
			n.lookupAgain()
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

// PunchAll tries several addresses at once: a lookup often knows more than
// one, and the first that answers is the one worth keeping. Attempts are
// capped so a stale table cannot become a flood.
func (n *Node) PunchAll(addrs []string, limit int) {
	if limit <= 0 {
		limit = 3
	}
	tried := 0
	for _, addr := range addrs {
		if tried >= limit {
			return
		}
		n.mu.Lock()
		busy := false
		for _, p := range n.peers {
			if p.Addr == addr {
				busy = true
				break
			}
		}
		if n.punching[addr] {
			busy = true
		}
		n.mu.Unlock()
		if busy {
			continue
		}
		n.Punch(addr)
		tried++
	}
}

// keepPeersWarm sends a few bytes to every punched peer now and then. A
// NAT drops a mapping that stays quiet, and that mapping is per peer: the
// pings to the STUN server do not keep the peer's door open. Without this
// an idle conversation loses its path every half minute and has to punch
// again, which is what made calls drop.
func (n *Node) keepPeersWarm(every time.Duration) {
	if n.hub == nil {
		return
	}
	if every <= 0 {
		every = 15 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for range t.C {
		n.mu.Lock()
		addrs := make([]string, 0, len(n.peers))
		for _, p := range n.peers {
			if p.UDP && p.Addr != "" {
				addrs = append(addrs, p.Addr)
			}
		}
		n.mu.Unlock()
		for _, addr := range addrs {
			if ua, err := net.ResolveUDPAddr("udp4", addr); err == nil {
				n.hub.punch(ua)
				n.warm.Add(1)
			}
		}
	}
}

// lookupAgain asks for the peer's current endpoints, at most once every
// ten seconds: a punch that failed usually means the address moved, and
// the rendezvous knows the new one before the next scheduled round.
func (n *Node) lookupAgain() {
	n.mu.Lock()
	ask := n.reLookup
	recent := time.Since(n.lastLookup) < 10*time.Second
	if !recent {
		n.lastLookup = time.Now()
	}
	n.mu.Unlock()
	if ask != nil && !recent {
		go ask()
	}
}

// udpStatus tells whether the punch socket runs.
func (n *Node) udpStatus() string {
	if n.hub == nil {
		return ""
	}
	return "udp " + strconv.Itoa(n.hub.port())
}
