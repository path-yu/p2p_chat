package dht

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config is what a node needs to join the network.
type Config struct {
	// Bootstrap lists the routers that introduce a node to the DHT, as
	// "host:port".
	Bootstrap []string
	// Logf reports what happens, when set.
	Logf func(string, ...any)
	// Timeout bounds a query; 3 seconds when zero.
	Timeout time.Duration
}

// Stats is what the node has seen, for the interface.
type Stats struct {
	Nodes   int    `json:"nodes"`
	Peers   int    `json:"peers"`
	Queries int    `json:"queries"`
	LastErr string `json:"lastErr,omitempty"`
	Ready   bool   `json:"ready"`
}

// Node is this program in the DHT.
type Node struct {
	id   [20]byte
	conn *net.UDPConn
	cfg  Config

	mu     sync.Mutex
	known  map[string]*route // by node id, as a string
	sent   map[string]chan *reply
	tokens map[string]string // node address to the token it gave
	peers  map[string]time.Time
	// store holds what announced itself here, as a node of the network
	// does, so that the peers that pass by find each other through it.
	store   map[string]map[string]time.Time
	queries int
	lastErr string
	ready   bool

	secret [16]byte
	close  chan struct{}
	once   sync.Once
}

// route is a node of the network this one has heard from.
type route struct {
	id       string
	addr     string
	lastSeen time.Time
	fails    int
}

type reply struct {
	y string
	r map[string]any
	e any
}

// New makes a node with a random identity of its own.
func New(cfg Config) *Node {
	n := &Node{
		cfg:    cfg,
		known:  map[string]*route{},
		sent:   map[string]chan *reply{},
		tokens: map[string]string{},
		peers:  map[string]time.Time{},
		store:  map[string]map[string]time.Time{},
		close:  make(chan struct{}),
	}
	_, _ = rand.Read(n.id[:])
	_, _ = rand.Read(n.secret[:])
	if n.cfg.Timeout == 0 {
		n.cfg.Timeout = 3 * time.Second
	}
	return n
}

func (n *Node) logf(format string, args ...any) {
	if n.cfg.Logf != nil {
		n.cfg.Logf(format, args...)
	}
}

// Start opens the socket and introduces the node to the routers.
func (n *Node) Start() error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		return err
	}
	n.conn = conn
	go n.readLoop()
	go n.boot()
	return nil
}

// Close leaves the network.
func (n *Node) Close() {
	n.once.Do(func() {
		close(n.close)
		if n.conn != nil {
			_ = n.conn.Close()
		}
	})
}

// ID is the node's identifier in the network.
func (n *Node) ID() string { return string(n.id[:]) }

// Stats reports what the node has seen.
func (n *Node) Stats() Stats {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := Stats{Nodes: len(n.known), Peers: len(n.peers), Queries: n.queries, LastErr: n.lastErr, Ready: n.ready}
	return st
}

// Peers lists the endpoints other nodes announced.
func (n *Node) Peers() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, 0, len(n.peers))
	for addr, at := range n.peers {
		if time.Since(at) < 20*time.Minute {
			out = append(out, addr)
		}
	}
	sort.Strings(out)
	return out
}

func (n *Node) boot() {
	n.bootstrap()
	n.mu.Lock()
	n.ready = len(n.known) > 0
	n.mu.Unlock()
	// Ask the closest nodes for the node's own identifier, so that the
	// table holds nodes around where this one lives.
	n.lookup(n.id[:], false)
}

// bootstrap introduces the node to the routers, twice if the first round
// finds nothing.
func (n *Node) bootstrap() {
	// The routers answer at once, so one that never does costs a round, not
	// a round each.
	type res struct {
		addr string
		resp map[string]any
		err  error
	}
	ask := func(rounds int) {
		ch := make(chan res, len(n.cfg.Bootstrap))
		for _, addr := range n.cfg.Bootstrap {
			go func(addr string) {
				ua, err := net.ResolveUDPAddr("udp4", addr)
				if err != nil {
					ch <- res{addr: addr, err: err}
					return
				}
				m, err := n.query(ua, "find_node", map[string]any{
					"id":     n.ID(),
					"target": n.ID(),
				})
				ch <- res{addr: addr, resp: m, err: err}
			}(addr)
		}
		for range n.cfg.Bootstrap {
			r := <-ch
			if r.err != nil {
				continue
			}
			n.absorb(r.resp, r.addr)
		}
	}
	ask(1)
	if n.count() == 0 {
		time.Sleep(time.Second)
		ask(2)
	}
}

// readLoop answers what comes and hands replies to their queries.
func (n *Node) readLoop() {
	buf := make([]byte, 4096)
	for {
		nr, from, err := n.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-n.close:
				return
			default:
				continue
			}
		}
		v, err := decodeAll(buf[:nr])
		if err != nil {
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		y, _ := m["y"].(string)
		switch y {
		case "r":
			if r, ok := m["r"].(map[string]any); ok {
				n.deliver(m, &reply{y: "r", r: r})
			}
		case "e":
			n.deliver(m, &reply{y: "e", e: m["e"]})
		case "q":
			n.answer(m, from)
		}
	}
}

func (n *Node) deliver(m map[string]any, r *reply) {
	t, _ := m["t"].(string)
	n.mu.Lock()
	ch := n.sent[t]
	n.mu.Unlock()
	if ch != nil {
		select {
		case ch <- r:
		default:
		}
	}
}

// query sends a query and waits for its answer.
func (n *Node) query(addr *net.UDPAddr, q string, args map[string]any) (map[string]any, error) {
	var tb [4]byte
	_, _ = rand.Read(tb[:])
	t := string(tb[:])
	ch := make(chan *reply, 1)
	n.mu.Lock()
	n.sent[t] = ch
	n.queries++
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.sent, t)
		n.mu.Unlock()
	}()

	payload, err := encode(nil, map[string]any{"t": t, "y": "q", "q": q, "a": args})
	if err != nil {
		return nil, err
	}
	if n.conn == nil {
		return nil, errors.New("dht: not started")
	}
	if _, err := n.conn.WriteToUDP(payload, addr); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.y == "e" {
			return nil, fmt.Errorf("dht: %v", r.e)
		}
		return r.r, nil
	case <-time.After(n.cfg.Timeout):
		return nil, errors.New("dht: timeout")
	case <-n.close:
		return nil, errors.New("dht: closed")
	}
}

// answer replies to a query of another node. The node keeps no peers of its
// own, so get_peers answers with the nodes it knows, and announce_peer is
// accepted and forgotten: the nodes that matter store it.
func (n *Node) answer(m map[string]any, from *net.UDPAddr) {
	t, _ := m["t"].(string)
	q, _ := m["q"].(string)
	if t == "" {
		return
	}
	resp := map[string]any{"t": t, "y": "r"}
	r := map[string]any{"id": n.ID()}
	switch q {
	case "ping":
	case "find_node":
		r["nodes"] = n.closestNodes(m["a"], 8)
	case "get_peers":
		var ih string
		if a, ok := m["a"].(map[string]any); ok {
			if s, ok := a["info_hash"].(string); ok && len(s) == 20 {
				ih = s
			}
		}
		r["token"] = string(n.token(from.IP))
		if vals := n.storedPeers(ih); len(vals) > 0 {
			r["values"] = vals
		} else {
			r["nodes"] = n.closestNodes(m["a"], 8)
		}
	case "announce_peer":
		n.keepAnnounce(m["a"], from)
	default:
		return
	}
	resp["r"] = r
	if payload, err := encode(nil, resp); err == nil {
		_, _ = n.conn.WriteToUDP(payload, from)
	}
	n.observe(m, from)
}

// observe remembers a node that spoke, so that lookups have somewhere to go.
func (n *Node) observe(m map[string]any, from *net.UDPAddr) {
	var id string
	if a, ok := m["a"].(map[string]any); ok {
		if s, ok := a["id"].(string); ok {
			id = s
		}
	}
	if id == "" {
		if r, ok := m["r"].(map[string]any); ok {
			if s, ok := r["id"].(string); ok {
				id = s
			}
		}
	}
	if len(id) != 20 || id == n.ID() {
		return
	}
	n.addNode(id, from.String())
}

func (n *Node) addNode(id, addr string) {
	if len(id) != 20 || id == "" || addr == "" || id == n.ID() {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if r, ok := n.known[id]; ok {
		r.lastSeen, r.addr, r.fails = time.Now(), addr, 0
		return
	}
	n.known[id] = &route{id: id, addr: addr, lastSeen: time.Now()}
	// Keep the table small: the oldest, least reliable nodes go first.
	if len(n.known) > 400 {
		var worst *route
		for _, r := range n.known {
			if r.id == id {
				continue
			}
			if worst == nil || r.lastSeen.Before(worst.lastSeen) {
				worst = r
			}
		}
		if worst != nil {
			delete(n.known, worst.id)
		}
	}
}

// closestNodes packs the nodes nearest an identifier, as the wire wants
// them: 20 bytes of id, 4 of address, 2 of port.
func (n *Node) closestNodes(a any, want int) string {
	args, _ := a.(map[string]any)
	var target []byte
	if args != nil {
		if s, ok := args["info_hash"].(string); ok && len(s) == 20 {
			target = []byte(s)
		} else if s, ok := args["target"].(string); ok && len(s) == 20 {
			target = []byte(s)
		}
	}
	if target == nil {
		target = n.id[:]
	}

	n.mu.Lock()
	list := make([]*route, 0, len(n.known))
	for _, r := range n.known {
		list = append(list, r)
	}
	n.mu.Unlock()

	sort.Slice(list, func(i, j int) bool {
		return closer(list[i].id, list[j].id, target)
	})
	if len(list) > want {
		list = list[:want]
	}
	out := make([]byte, 0, len(list)*26)
	for _, r := range list {
		ua, err := net.ResolveUDPAddr("udp4", r.addr)
		if err != nil || len(ua.IP.To4()) != 4 {
			continue
		}
		out = append(out, r.id...)
		out = append(out, ua.IP.To4()...)
		var p [2]byte
		binary.BigEndian.PutUint16(p[:], uint16(ua.Port))
		out = append(out, p[:]...)
	}
	return string(out)
}

// absorb reads the nodes of a response into the table, and remembers the
// node that answered.
func (n *Node) absorb(r map[string]any, addr string) {
	if nodes, ok := r["nodes"].(string); ok {
		n.parseNodes([]byte(nodes))
	}
	if vals, ok := r["values"].([]any); ok {
		for _, v := range vals {
			if s, ok := v.(string); ok {
				n.addPeer(peerAddr(s))
			}
		}
	}
	if id, ok := r["id"].(string); ok {
		n.addNode(id, addr)
	}
}

func (n *Node) parseNodes(b []byte) {
	for i := 0; i+26 <= len(b); i += 26 {
		id := string(b[i : i+20])
		ip := net.IP(b[i+20 : i+24])
		port := binary.BigEndian.Uint16(b[i+24 : i+26])
		if port == 0 || ip.IsUnspecified() {
			continue
		}
		n.addNode(id, net.JoinHostPort(ip.String(), fmt.Sprint(port)))
	}
}

func (n *Node) addPeer(addr string) {
	ua, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil || ua.Port == 0 {
		return
	}
	n.mu.Lock()
	n.peers[ua.String()] = time.Now()
	n.mu.Unlock()
}

// lookup walks towards the nodes nearest an identifier, collecting the
// peers others announced there.
func (n *Node) lookup(target []byte, collect bool) []string {
	queried := map[string]bool{}
	found := map[string]bool{}
	var peers []string

	// The nodes of a lookup are asked at once: one that never answers costs
	// its timeout, not the whole round.
	type answer struct {
		r    *route
		addr string
		resp map[string]any
		err  error
	}
	timeout := n.cfg.Timeout
	if timeout > 1500*time.Millisecond {
		timeout = 1500 * time.Millisecond
	}

	for round := 0; round < 5; round++ {
		n.mu.Lock()
		list := make([]*route, 0, len(n.known))
		for _, r := range n.known {
			if !queried[r.id] {
				list = append(list, r)
			}
		}
		n.mu.Unlock()
		if len(list) == 0 {
			break
		}
		sort.Slice(list, func(i, j int) bool {
			return closer(list[i].id, list[j].id, target)
		})
		if len(list) > 12 {
			list = list[:12]
		}

		answers := make(chan answer, len(list))
		for _, r := range list {
			queried[r.id] = true
			go func(r *route) {
				ua, err := net.ResolveUDPAddr("udp4", r.addr)
				if err != nil {
					answers <- answer{r: r, err: err}
					return
				}
				resp, err := n.queryTimeout(ua, "get_peers", map[string]any{
					"id":        n.ID(),
					"info_hash": string(target),
				}, timeout)
				answers <- answer{r: r, addr: ua.String(), resp: resp, err: err}
			}(r)
		}

		before := n.count()
		for range list {
			a := <-answers
			if a.err != nil {
				n.mu.Lock()
				if rt, ok := n.known[a.r.id]; ok {
					rt.fails++
					if rt.fails > 2 {
						delete(n.known, a.r.id)
					}
				}
				n.mu.Unlock()
				continue
			}
			n.absorb(a.resp, a.addr)
			if tok, ok := a.resp["token"].(string); ok && tok != "" {
				n.mu.Lock()
				n.tokens[a.addr] = tok
				n.mu.Unlock()
			}
			if vals, ok := a.resp["values"].([]any); ok {
				for _, v := range vals {
					s, ok := v.(string)
					if !ok {
						continue
					}
					addr := peerAddr(s)
					if addr != "" && !found[addr] {
						found[addr] = true
						n.addPeer(addr)
						peers = append(peers, addr)
					}
				}
			}
		}
		if n.count() == before {
			break
		}
	}
	if collect && len(peers) > 0 {
		n.logf("DHT 找到 %d 个候选端点", len(peers))
	}
	return peers
}

// queryTimeout is query with a timeout of its own, for the lookups that
// must not wait on a slow node.
func (n *Node) queryTimeout(addr *net.UDPAddr, q string, args map[string]any, d time.Duration) (map[string]any, error) {
	var tb [4]byte
	_, _ = rand.Read(tb[:])
	t := string(tb[:])
	ch := make(chan *reply, 1)
	n.mu.Lock()
	n.sent[t] = ch
	n.queries++
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.sent, t)
		n.mu.Unlock()
	}()
	payload, err := encode(nil, map[string]any{"t": t, "y": "q", "q": q, "a": args})
	if err != nil {
		return nil, err
	}
	if n.conn == nil {
		return nil, errors.New("dht: not started")
	}
	if _, err := n.conn.WriteToUDP(payload, addr); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.y == "e" {
			return nil, fmt.Errorf("dht: %v", r.e)
		}
		return r.r, nil
	case <-time.After(d):
		return nil, errors.New("dht: timeout")
	case <-n.close:
		return nil, errors.New("dht: closed")
	}
}

func (n *Node) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.known)
}

// Lookup finds the peers that announced themselves under an identifier.
func (n *Node) Lookup(infoHash [20]byte) []string {
	if n.conn == nil {
		return nil
	}
	return n.lookup(infoHash[:], true)
}

// Announce tells the closest nodes that this peer listens at port.
func (n *Node) Announce(infoHash [20]byte, port int) error {
	if n.conn == nil {
		return errors.New("dht: not started")
	}
	n.lookup(infoHash[:], false)

	n.mu.Lock()
	type cand struct {
		addr  string
		token string
		id    string
	}
	list := make([]cand, 0, 8)
	for addr, tok := range n.tokens {
		list = append(list, cand{addr: addr, token: tok})
	}
	n.mu.Unlock()
	if len(list) == 0 {
		return fmt.Errorf("dht: no node answered with a token（表内 %d 个节点）", n.count())
	}
	var announced int
	for _, c := range list {
		ua, err := net.ResolveUDPAddr("udp4", c.addr)
		if err != nil {
			continue
		}
		_, err = n.query(ua, "announce_peer", map[string]any{
			"id":           n.ID(),
			"info_hash":    string(infoHash[:]),
			"port":         int64(port),
			"token":        c.token,
			"implied_port": int64(0),
		})
		if err == nil {
			announced++
		}
	}
	if announced == 0 {
		return errors.New("dht: no node took the announcement")
	}
	return nil
}

// NetworkKey is the identifier a network's peers meet under: the first
// twenty bytes of the SHA-1 of its name, as an infohash.
func NetworkKey(name string) [20]byte {
	sum := sha1.Sum([]byte("lumen/v1/" + name))
	var out [20]byte
	copy(out[:], sum[:20])
	return out
}

// keepAnnounce stores a peer that announced itself, for the peers that
// look for it here later.
func (n *Node) keepAnnounce(a any, from *net.UDPAddr) {
	args, _ := a.(map[string]any)
	if args == nil {
		return
	}
	ih, _ := args["info_hash"].(string)
	port, _ := args["port"].(int64)
	if implied, _ := args["implied_port"].(int64); implied != 0 {
		port = int64(from.Port)
	}
	if len(ih) != 20 || port <= 0 || port > 65535 {
		return
	}
	addr := net.JoinHostPort(from.IP.String(), fmt.Sprint(port))
	n.mu.Lock()
	defer n.mu.Unlock()
	set := n.store[ih]
	if set == nil {
		if len(n.store) > 256 {
			for k := range n.store {
				delete(n.store, k)
				break
			}
		}
		set = map[string]time.Time{}
		n.store[ih] = set
	}
	if len(set) > 200 {
		for k := range set {
			delete(set, k)
			break
		}
	}
	set[addr] = time.Now()
}

// storedPeers lists the peers kept for an identifier.
func (n *Node) storedPeers(ih string) []any {
	if len(ih) != 20 {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	set := n.store[ih]
	if set == nil {
		return nil
	}
	out := make([]any, 0, len(set))
	for addr, at := range set {
		if time.Since(at) > 30*time.Minute {
			delete(set, addr)
			continue
		}
		if packed := packAddr(addr); packed != "" {
			out = append(out, packed)
		}
	}
	if len(out) == 0 {
		delete(n.store, ih)
	}
	return out
}

// peerAddr reads a peer of the wire: six bytes of address and port, or the
// textual form this node's own tests use.
func peerAddr(v string) string {
	if len(v) == 6 {
		ip := net.IP(v[:4])
		port := binary.BigEndian.Uint16([]byte(v[4:]))
		if port == 0 {
			return ""
		}
		return net.JoinHostPort(ip.String(), fmt.Sprint(port))
	}
	if strings.Contains(v, ":") {
		return v
	}
	return ""
}

// packAddr writes a peer as six bytes, as a get_peers answer carries them.
func packAddr(addr string) string {
	ua, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return ""
	}
	ip := ua.IP.To4()
	if ip == nil || ua.Port == 0 {
		return ""
	}
	b := make([]byte, 6)
	copy(b, ip)
	binary.BigEndian.PutUint16(b[4:], uint16(ua.Port))
	return string(b)
}

// token is what a node hands a peer to announce with later.
func (n *Node) token(ip net.IP) []byte {
	mac := hmac.New(sha1.New, n.secret[:])
	mac.Write(ip.To4())
	return mac.Sum(nil)[:8]
}

// closer reports whether a is nearer target than b in the network's XOR
// metric.
func closer(a, b string, target []byte) bool {
	for i := 0; i < 20 && i < len(a) && i < len(b) && i < len(target); i++ {
		da := a[i] ^ target[i]
		db := b[i] ^ target[i]
		if da != db {
			return da < db
		}
	}
	return false
}
