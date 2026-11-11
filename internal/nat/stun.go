// Package nat finds this host's place on the public internet: the address
// a NAT gives it, how it maps ports, and whether it will let a peer in.
package nat

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

// Endpoint is an address on the public internet.
type Endpoint struct {
	IP   net.IP
	Port int
}

// String writes the endpoint as host:port.
func (e Endpoint) String() string {
	if e.IP == nil || e.Port == 0 {
		return ""
	}
	return net.JoinHostPort(e.IP.String(), fmt.Sprint(e.Port))
}

// Empty reports whether the endpoint holds an address.
func (e Endpoint) Empty() bool { return e.IP == nil || e.Port == 0 }

const stunMagic = 0x2112A442

// DefaultSTUN lists the servers a probe asks, on different networks so
// that their answers tell a cone from a symmetric mapping.
// The first four answer from a Chinese network in 50~70 ms, the rest are
// the fallback for when those are unreachable (cmd/stunprobe measures them).
var DefaultSTUN = []string{
	"stun.hitv.com:3478",
	"stun.douyucdn.cn:18000",
	"stun.chat.bilibili.com:3478",
	"stun.miwifi.com:3478",
	"stun.l.google.com:19302",
	"stun.cloudflare.com:3478",
}

// Sender sends a datagram, and Reader takes the next one that arrives.
type Sender func(b []byte, to *net.UDPAddr) error

// Reader waits for a datagram, giving up after d.
type Reader func(d time.Duration) (b []byte, from *net.UDPAddr, err error)

// Discover asks one STUN server which address it sees this socket come
// from (RFC 5389's binding request).
func Discover(conn *net.UDPConn, server string, timeout time.Duration) (Endpoint, error) {
	return DiscoverVia(
		func(b []byte, to *net.UDPAddr) error {
			_, err := conn.WriteToUDP(b, to)
			return err
		},
		func(d time.Duration) ([]byte, *net.UDPAddr, error) {
			_ = conn.SetReadDeadline(time.Now().Add(d))
			buf := make([]byte, 1500)
			n, from, err := conn.ReadFromUDP(buf)
			return buf[:n], from, err
		},
		server, timeout)
}

// DiscoverVia does the same through a socket the caller owns, so that a
// session keeps the port a STUN server saw, the one peers punch towards.
func DiscoverVia(send Sender, recv Reader, server string, timeout time.Duration) (Endpoint, error) {
	ua, err := net.ResolveUDPAddr("udp4", server)
	if err != nil {
		return Endpoint{}, err
	}

	var txid [12]byte
	if _, err := rand.Read(txid[:]); err != nil {
		return Endpoint{}, err
	}
	req := make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:], 0x0001) // binding request
	binary.BigEndian.PutUint16(req[2:], 0)      // no attributes
	binary.BigEndian.PutUint32(req[4:], stunMagic)
	copy(req[8:], txid[:])

	if err := send(req, ua); err != nil {
		return Endpoint{}, err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		b, from, err := recv(left)
		if err != nil {
			return Endpoint{}, err
		}
		if from == nil || !from.IP.Equal(ua.IP) {
			continue
		}
		ep, gotTx, err := parseBinding(b)
		if err != nil || gotTx != txid {
			continue
		}
		return ep, nil
	}
	return Endpoint{}, errors.New("stun: no answer")
}

// parseBinding reads a binding success response, giving the mapped address
// and the transaction it answers.
func parseBinding(b []byte) (Endpoint, [12]byte, error) {
	var txid [12]byte
	if len(b) < 20 {
		return Endpoint{}, txid, errors.New("stun: short message")
	}
	typ := binary.BigEndian.Uint16(b[0:])
	if typ != 0x0101 {
		return Endpoint{}, txid, fmt.Errorf("stun: not a success response (%#x)", typ)
	}
	if binary.BigEndian.Uint32(b[4:]) != stunMagic {
		return Endpoint{}, txid, errors.New("stun: bad magic cookie")
	}
	copy(txid[:], b[8:20])

	length := int(binary.BigEndian.Uint16(b[2:]))
	end := 20 + length
	if end > len(b) {
		end = len(b)
	}
	var plain, xor *Endpoint
	for i := 20; i+4 <= end; {
		attr := binary.BigEndian.Uint16(b[i:])
		size := int(binary.BigEndian.Uint16(b[i+2:]))
		val := b[i+4:]
		if i+4+size > end {
			break
		}
		val = val[:size]
		if len(val) >= 8 {
			family := val[1]
			port := binary.BigEndian.Uint16(val[2:4])
			ip := net.IP(append([]byte(nil), val[4:8]...))
			switch attr {
			case 0x0020: // XOR-MAPPED-ADDRESS
				m := uint32(stunMagic)
				e := Endpoint{
					IP: net.IPv4(
						ip[0]^byte(m>>24), ip[1]^byte(m>>16),
						ip[2]^byte(m>>8), ip[3]^byte(m),
					),
					Port: int(port ^ uint16(m>>16)),
				}
				if family == 1 {
					xor = &e
				}
			case 0x0001: // MAPPED-ADDRESS, older servers
				e := Endpoint{IP: ip, Port: int(port)}
				if family == 1 {
					plain = &e
				}
			}
		}
		i += 4 + size + (4-size%4)%4 // attributes are padded to four bytes
	}
	switch {
	case xor != nil:
		return *xor, txid, nil
	case plain != nil:
		return *plain, txid, nil
	}
	return Endpoint{}, txid, errors.New("stun: no mapped address")
}

// Report is what the network looks like from here.
type Report struct {
	// Public is the address a STUN server saw, empty when none answered.
	Public Endpoint
	// Mapping says how the router maps ports: "cone" when it keeps one
	// mapping for the socket, "symmetric" when it makes a new one for each
	// destination, "unknown" when the servers disagreed or failed.
	Mapping string
	// Server names the server that answered.
	Server string
	// Err is why no report could be made.
	Err error
}

// Probe asks two servers on different networks. The same mapped port from
// both means a cone NAT, which a peer can reach once a port is opened; a
// different port each time means a symmetric one, which only a relay helps.
func Probe(conn *net.UDPConn, servers []string, timeout time.Duration) Report {
	if len(servers) == 0 {
		servers = DefaultSTUN
	}
	var first Endpoint
	var firstServer string
	var lastErr error
	seen := map[string]bool{}
	for _, s := range servers {
		if seen[s] {
			continue
		}
		seen[s] = true
		ep, err := Discover(conn, s, timeout)
		if err != nil {
			lastErr = err
			continue
		}
		if first.Empty() {
			first, firstServer = ep, s
			continue
		}
		rep := Report{Public: first, Server: firstServer}
		switch {
		case ep.IP.Equal(first.IP) && ep.Port == first.Port:
			rep.Mapping = "cone"
		case ep.Port != first.Port:
			rep.Mapping = "symmetric"
		default:
			rep.Mapping = "unknown"
		}
		return rep
	}
	if first.Empty() {
		if lastErr == nil {
			lastErr = errors.New("stun: no server answered")
		}
		return Report{Err: lastErr}
	}
	return Report{Public: first, Server: firstServer, Mapping: "unknown"}
}
