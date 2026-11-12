package p2p

import (
	"errors"
	"net"
	"strconv"
	"strings"
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
		// A NAT that rewrites the source port usually allocates it in
		// order, so the door the lookup announced can be one or two away
		// from the real one. After a few seconds of silence, the same host
		// is asked on its neighbours too — never anyone else, and only a
		// handful of ports.
		var near []*net.UDPAddr
		nextPredict := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && !pipe.seen.Load() {
			hub.punch(ua)
			if time.Now().After(nextPredict) {
				if near == nil {
					near = nearbyPorts(ua, 5)
					if len(near) > 0 {
						n.logf("打洞无回应，改试邻近端口 %s", portsOf(near))
					}
				}
				for _, alt := range near {
					hub.punch(alt)
				}
				nextPredict = time.Now().Add(400 * time.Millisecond)
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !pipe.seen.Load() {
			// A peer that answers on a neighbour port reaches this node as
			// an unknown address and is connected by its own punch, so the
			// "已连接" line is the honest place to see it, not here.
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

// nearbyPorts lists the ports to try when the announced one stays silent:
// a few above it, a couple below, since allocation mostly runs upward.
// The host is never changed, so this only ever talks to the peer.
func nearbyPorts(addr *net.UDPAddr, n int) []*net.UDPAddr {
	offsets := []int{1, 2, 3, -1, 4, 5, -2}
	out := make([]*net.UDPAddr, 0, n)
	for _, off := range offsets {
		if len(out) >= n {
			break
		}
		port := addr.Port + off
		if port < 1 || port > 65535 {
			continue
		}
		out = append(out, &net.UDPAddr{IP: addr.IP, Port: port})
	}
	return out
}

// portsOf writes a list of addresses short, for a log line.
func portsOf(addrs []*net.UDPAddr) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		parts = append(parts, strconv.Itoa(a.Port))
	}
	return strings.Join(parts, ",")
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
