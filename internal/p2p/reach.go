package p2p

import (
	"net"
	"sync"
	"time"

	"ptp_chat/internal/dht"
	"ptp_chat/internal/nat"
)

// ReachConfig says how to look for peers on other networks.
type ReachConfig struct {
	// Network is the name peers meet under: the same name, the same swarm.
	// Empty means "lumen".
	Network string
	// Bootstrap lists the DHT routers that introduce a node.
	Bootstrap []string
	// STUN lists the servers that tell the node its public address; empty
	// means nat.DefaultSTUN.
	STUN []string
	// Announce is how often the node tells the network where it is, ten
	// minutes when zero; Lookup how often it asks for peers, 45 seconds.
	Announce time.Duration
	Lookup   time.Duration
	Logf     func(string, ...any)
}

// DefaultBootstrap lists the DHT routers BitTorrent clients use.
var DefaultBootstrap = []string{
	"router.bittorrent.com:6881",
	"dnsaddr.bootstrap.bitcomet.com:6881",
	"router.utorrent.com:6881",
	"dht.transmissionbt.com:6881",
}

// Reach is how this node can be reached from other networks.
type Reach struct {
	// Network is the swarm this node meets peers in.
	Network string `json:"network"`
	// Public is the address peers punch towards, empty when none was found.
	Public string `json:"public,omitempty"`
	// PublicIP is what a STUN server saw, shown even without a port.
	PublicIP string `json:"publicIp,omitempty"`
	// Mapping names what opened a port: "UPnP", or empty for none.
	Mapping string `json:"mapping,omitempty"`
	// NAT says how the router maps ports: cone, symmetric or unknown.
	NAT string `json:"nat,omitempty"`
	// NATErr is why the type is unknown, as when no STUN server answers.
	NATErr string `json:"natErr,omitempty"`
	// Nodes and Found are the DHT's table and the endpoints it gave.
	Nodes int `json:"nodes"`
	Found int `json:"found"`
	// Note explains what stands in the way, when something does.
	Note string `json:"note,omitempty"`
	// Ready tells that the rendezvous runs.
	Ready bool `json:"ready"`
}

// reachState is the rendezvous, kept under one lock.
type reachState struct {
	sync.Mutex
	r        Reach
	stop     chan struct{}
	mappings []*nat.Mapping
	table    *dht.Node
	once     sync.Once
}

// ReachStatus reports how the node can be reached.
func (n *Node) ReachStatus() Reach {
	n.reach.Lock()
	defer n.reach.Unlock()
	r := n.reach.r
	if n.reach.table != nil {
		st := n.reach.table.Stats()
		r.Nodes, r.Found = st.Nodes, st.Peers
	}
	return r
}

// Reach opens the ports the router allows, learns the address the internet
// sees, and joins the DHT swarm of a network, where peers announce
// themselves and punch towards each other. Every step is best effort: a
// network without UPnP, or without UDP to the internet, keeps working on
// the local network alone.
func (n *Node) Reach(cfg ReachConfig) {
	if cfg.Network == "" {
		cfg.Network = "lumen"
	}
	if cfg.Announce == 0 {
		cfg.Announce = 10 * time.Minute
	}
	if cfg.Lookup == 0 {
		cfg.Lookup = 45 * time.Second
	}
	if len(cfg.Bootstrap) == 0 {
		cfg.Bootstrap = DefaultBootstrap
	}
	servers := cfg.STUN
	if len(servers) == 0 {
		servers = nat.DefaultSTUN
	}
	// pick wraps around, so a list of one server still works everywhere.
	pick := func(i int) string { return servers[i%len(servers)] }
	logf := cfg.Logf
	if logf == nil {
		logf = n.Logf
	}

	n.reach.Lock()
	n.reach.r.Network = cfg.Network
	n.reach.stop = make(chan struct{})
	stop := n.reach.stop
	n.reach.Unlock()

	addMapping := func(m *nat.Mapping) {
		n.reach.Lock()
		n.reach.mappings = append(n.reach.mappings, m)
		if n.reach.r.Mapping == "" {
			n.reach.r.Mapping = "UPnP"
		}
		n.reach.Unlock()
	}

	started := time.Now()
	go func() {
		// The table fills while the ports are mapped and STUN is asked, so
		// the rendezvous starts without waiting on either.
		table := dht.New(dht.Config{Bootstrap: cfg.Bootstrap, Logf: logf, Timeout: 3 * time.Second})
		if err := table.Start(); err != nil {
			logf("DHT 不可用: %v", err)
		} else {
			n.reach.Lock()
			n.reach.table = table
			n.reach.r.Ready = true
			n.reach.Unlock()
			n.reach.once.Do(func() {
				go func() {
					<-stop
					table.Close()
				}()
			})
		}

		// 1. STUN first: the address is what the window shows and what a
		// peer needs to reach this node, so it must not wait on a router
		// that may not even answer.
		var public nat.Endpoint
		if n.hub != nil {
			send := func(b []byte, to *net.UDPAddr) error { return n.hub.send(to, b) }
			recv := n.hub.recv
			// One server timing out says nothing about the network, so a few
			// are asked before the address is called missing.
			var ep nat.Endpoint
			var firstErr error
			for i := 0; i < len(servers) && i < 3; i++ {
				got, err := nat.DiscoverVia(send, recv, servers[i], 2*time.Second)
				if err == nil {
					ep = got
					break
				}
				if firstErr == nil {
					firstErr = err
				}
			}
			if ep.Empty() {
				logf("STUN 没有回应: %v", firstErr)
				n.reach.Lock()
				n.reach.r.NATErr = firstErr.Error()
				n.reach.Unlock()
			} else {
				public = ep
				n.reach.Lock()
				n.reach.r.Public = ep.String()
				n.reach.r.PublicIP = ep.IP.String()
				n.reach.Unlock()
				logf("公网地址 %s（%d ms）", ep.String(), time.Since(started).Milliseconds())
			}
		}

		// 2. The NAT type takes a second question to another server; it is
		// worth a line of the log, not a second of the window.
		if !public.Empty() {
			if ep2, err := nat.DiscoverVia(
				func(b []byte, to *net.UDPAddr) error { return n.hub.send(to, b) },
				n.hub.recv, pick(1), 3*time.Second); err == nil {
				kind := "unknown"
				switch {
				case ep2.IP.Equal(public.IP) && ep2.Port == public.Port:
					kind = "cone"
				case ep2.Port != public.Port:
					kind = "symmetric"
				}
				n.reach.Lock()
				n.reach.r.NAT = kind
				n.reach.Unlock()
				logf("NAT 类型 %s", kind)
			}
		}

		// 3. Ask the router to forward the ports, when it speaks UPnP. This
		// can take seconds on a network without UPnP, so it runs beside the
		// rest rather than in front of it.
		go func() {
			if m, err := nat.Map(n.Port, "TCP", 0); err != nil {
				logf("端口映射（TCP）未成功: %v", err)
				n.reach.Lock()
				n.reach.r.Note = err.Error()
				n.reach.Unlock()
			} else {
				addMapping(m)
				logf("路由器已放行 TCP %s", m.External.String())
			}
			if n.hub != nil {
				if m, err := nat.Map(n.hub.port(), "UDP", 0); err == nil {
					addMapping(m)
					logf("路由器已放行 UDP %s", m.External.String())
				}
			}
		}()

		// 3. Announce the port peers punch, and punch towards the peers
		// found in the swarm.
		if table == nil {
			return
		}
		key := dht.NetworkKey(cfg.Network)

		announce := func() bool {
			n.reach.Lock()
			port := 0
			if !public.Empty() {
				port = public.Port
			}
			n.reach.Unlock()
			if port == 0 {
				return false
			}
			if err := table.Announce(key, port); err != nil {
				logf("向 DHT 宣告失败: %v", err)
				return false
			}
			logf("已在网络「%s」里宣告端口 %d", cfg.Network, port)
			return true
		}
		go func() {
			// The table fills for a while after the bootstrap, so the first
			// announcement waits for a node that takes it.
			for !announce() {
				select {
				case <-stop:
					return
				case <-time.After(6 * time.Second):
				}
			}
			t := time.NewTicker(cfg.Announce)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					announce()
				}
			}
		}()

		// A NAT forgets a mapping that stays quiet, so the punch socket
		// asks its own address again every half minute, which keeps the
		// port peers punch towards open.
		if n.hub != nil {
			send := func(b []byte, to *net.UDPAddr) error { return n.hub.send(to, b) }
			recv := n.hub.recv
			go func() {
				t := time.NewTicker(25 * time.Second)
				defer t.Stop()
				for {
					select {
					case <-stop:
						return
					case <-t.C:
						ep, err := nat.DiscoverVia(send, recv, pick(0), 3*time.Second)
						if err != nil {
							continue
						}
						n.reach.Lock()
						changed := n.reach.r.Public != ep.String()
						n.reach.r.Public = ep.String()
						n.reach.r.PublicIP = ep.IP.String()
						n.reach.r.NATErr = ""
						n.reach.Unlock()
						if changed {
							announce()
						}
					}
				}
			}()
		}

		lookup := func() {
			found := table.Lookup(key)
			n.reach.Lock()
			self := n.reach.r.Public
			n.reach.Unlock()
			var fresh []string
			for _, addr := range found {
				if addr == self || addr == n.Addr {
					continue
				}
				fresh = append(fresh, addr)
			}
			n.PunchAll(fresh, 3)
		}
		// A punch that times out can ask for the peer's new endpoint at
		// once, instead of waiting for the next tick.
		n.mu.Lock()
		n.reLookup = lookup
		n.mu.Unlock()

		lookup()
		t := time.NewTicker(cfg.Lookup)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				lookup()
			}
		}
	}()
}

// StopReach leaves the swarm and takes the port mappings off the router.
func (n *Node) StopReach() {
	n.reach.Lock()
	if n.reach.stop != nil {
		select {
		case <-n.reach.stop:
		default:
			close(n.reach.stop)
		}
	}
	mappings := n.reach.mappings
	n.reach.mappings = nil
	n.reach.Unlock()
	for _, m := range mappings {
		m.Close()
	}
}
