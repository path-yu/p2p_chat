package p2p

import (
	"net"
	"testing"
)

// TestNearbyPortsStaysOnThePeer: prediction may guess other doors, but
// never another host, and never a port that does not exist.
func TestNearbyPortsStaysOnThePeer(t *testing.T) {
	ip := net.ParseIP("203.0.113.9")
	got := nearbyPorts(&net.UDPAddr{IP: ip, Port: 40000}, 5)
	if len(got) != 5 {
		t.Fatalf("got %d candidates, want 5", len(got))
	}
	seen := map[int]bool{}
	for _, a := range got {
		if !a.IP.Equal(ip) {
			t.Fatalf("candidate %s changed the host", a)
		}
		if a.Port < 1 || a.Port > 65535 {
			t.Fatalf("candidate port %d is out of range", a.Port)
		}
		if seen[a.Port] {
			t.Fatalf("port %d listed twice", a.Port)
		}
		seen[a.Port] = true
	}
	for _, want := range []int{40001, 40002, 40003, 39999, 40004} {
		if !seen[want] {
			t.Fatalf("port %d missing from %v", want, seen)
		}
	}
}

// TestNearbyPortsAtTheEdges: a peer on port 1 or 65535 must not produce a
// port zero or 65536.
func TestNearbyPortsAtTheEdges(t *testing.T) {
	ip := net.ParseIP("198.51.100.7")
	for _, port := range []int{1, 2, 65534, 65535} {
		for _, a := range nearbyPorts(&net.UDPAddr{IP: ip, Port: port}, 5) {
			if a.Port < 1 || a.Port > 65535 {
				t.Fatalf("from %d came an impossible port %d", port, a.Port)
			}
		}
	}
}

// TestNearbyPortsRespectsTheLimit: the window is bounded, so a punch can
// never turn into a scan.
func TestNearbyPortsRespectsTheLimit(t *testing.T) {
	ip := net.ParseIP("192.0.2.5")
	for _, n := range []int{1, 2, 3} {
		if got := nearbyPorts(&net.UDPAddr{IP: ip, Port: 5000}, n); len(got) != n {
			t.Fatalf("asked for %d, got %d", n, len(got))
		}
	}
	if got := nearbyPorts(&net.UDPAddr{IP: ip, Port: 5000}, 0); len(got) != 0 {
		t.Fatalf("a limit of zero produced %d candidates", len(got))
	}
}
