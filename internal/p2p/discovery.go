package p2p

import (
	"encoding/json"
	"net"
	"strings"
	"sync"
	"time"
)

// beacon is what a node broadcasts on the local network so that the peers
// beside it find it without an address.
type beacon struct {
	Magic string `json:"m"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Port  int    `json:"p"`
	Addr  string `json:"a"`
}

const discoveryMagic = "lumen-lan-1"

// StartDiscovery listens for beacons on port and broadcasts this node's
// every few seconds. A port already taken by another process only turns
// discovery off: the mesh still works with addresses given by hand.
func (n *Node) StartDiscovery(port int) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		n.logf("局域网发现不可用: %v", err)
		return
	}
	n.discPort = port
	_ = conn.SetReadBuffer(1 << 16)

	var once sync.Once
	stop := make(chan struct{})
	n.SetDiscovery(func() {
		once.Do(func() {
			close(stop)
			_ = conn.Close()
		})
	})

	go func() {
		buf := make([]byte, 2048)
		for {
			nr, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				select {
				case <-stop:
					return
				default:
					continue
				}
			}
			var b beacon
			if err := json.Unmarshal(buf[:nr], &b); err != nil || b.Magic != discoveryMagic {
				continue
			}
			if b.ID == n.ID.ID() || b.Addr == "" {
				continue
			}
			_ = from
			n.Connect(b.Addr)
		}
	}()

	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		payload, _ := json.Marshal(beacon{
			Magic: discoveryMagic,
			ID:    n.ID.ID(),
			Name:  n.ID.Name,
			Port:  n.Port,
			Addr:  n.Addr,
		})
		send := func() {
			for _, ip := range broadcastIPs() {
				_, _ = conn.WriteToUDP(payload, &net.UDPAddr{IP: ip, Port: port})
			}
		}
		send()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				send()
			}
		}
	}()
}

// BestLANIP is the address other hosts of the network reach this one at.
func BestLANIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	best := ""
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		ip := ipnet.IP.To4()
		if ip == nil || !ip.IsPrivate() {
			continue
		}
		s := ip.String()
		switch {
		case strings.HasPrefix(s, "192.168."):
			return s
		case strings.HasPrefix(s, "10."):
			best = s
		case best == "":
			best = s
		}
	}
	if best == "" {
		return "127.0.0.1"
	}
	return best
}

// broadcastIPs lists the broadcast address of every IPv4 interface, and
// the network's own.
func broadcastIPs() []net.IP {
	seen := map[string]bool{}
	var out []net.IP
	add := func(ip net.IP) {
		if ip == nil || seen[ip.String()] {
			return
		}
		seen[ip.String()] = true
		out = append(out, ip)
	}
	add(net.IPv4bcast)
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil {
				continue
			}
			mask := net.IP(ipnet.Mask).To4()
			if mask == nil {
				continue
			}
			b := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				b[i] = ip[i] | ^mask[i]
			}
			add(b)
		}
	}
	return out
}
