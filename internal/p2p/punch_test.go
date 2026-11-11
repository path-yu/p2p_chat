package p2p

import (
	"path/filepath"
	"testing"
	"time"

	"ptp_chat/internal/identity"
	"ptp_chat/internal/wire"
)

func startUDPNode(t *testing.T, name string, tcpPort, udpPort int) *Node {
	t.Helper()
	id, err := identity.Load(filepath.Join(t.TempDir(), name), name)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	n := New(id, tcpPort)
	n.Logf = t.Logf
	if err := n.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := n.StartUDP(udpPort); err != nil {
		t.Fatalf("udp: %v", err)
	}
	t.Cleanup(n.Close)
	return n
}

// TestPunchDelivers proves the path a NAT would give two peers: one
// punches, the other punches back, and a signed message crosses.
func TestPunchDelivers(t *testing.T) {
	a := startUDPNode(t, "A", 0, 0)
	b := startUDPNode(t, "B", 0, 0)

	got := make(chan wire.Envelope, 4)
	a.OnEvent = func(ev Event) {
		if ev.Kind == Message {
			got <- ev.Env
		}
	}

	a.Punch("127.0.0.1:" + itoa(b.UDPPort()))

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(a.Peers()) > 0 && len(b.Peers()) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(a.Peers()) != 1 || len(b.Peers()) != 1 {
		t.Fatalf("peers after punching: a=%v b=%v", a.Peers(), b.Peers())
	}
	if !a.Peers()[0].UDP || !b.Peers()[0].UDP {
		t.Fatalf("the session is not the punched one: %v %v", a.Peers(), b.Peers())
	}

	b.Broadcast(&wire.Envelope{Type: wire.Chat, Room: "lobby", Body: "打洞通了"})
	select {
	case env := <-got:
		if env.Body != "打洞通了" {
			t.Fatalf("body %q", env.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message over the punched path")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
