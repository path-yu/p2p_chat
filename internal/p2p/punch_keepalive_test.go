package p2p

import (
	"path/filepath"
	"testing"
	"time"

	"ptp_chat/internal/identity"
)

// startWarmNode is startUDPNode with the keepalive interval turned down so
// a test does not have to wait a quarter of a minute for it.
func startWarmNode(t *testing.T, name string, every time.Duration) *Node {
	t.Helper()
	id, err := identity.Load(filepath.Join(t.TempDir(), name), name)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	n := New(id, 0)
	n.Logf = t.Logf
	n.warmEvery = every
	if err := n.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := n.StartUDP(0); err != nil {
		t.Fatalf("udp: %v", err)
	}
	t.Cleanup(n.Close)
	return n
}

// TestKeepaliveHoldsThePathOpen: a quiet conversation keeps sending to its
// peer, because a NAT drops the mapping otherwise.
func TestKeepaliveHoldsThePathOpen(t *testing.T) {
	a := startWarmNode(t, "A", 150*time.Millisecond)
	b := startWarmNode(t, "B", 150*time.Millisecond)

	a.Punch("127.0.0.1:" + itoa(b.UDPPort()))
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && (len(a.Peers()) == 0 || len(b.Peers()) == 0) {
		time.Sleep(50 * time.Millisecond)
	}
	if len(a.Peers()) == 0 || len(b.Peers()) == 0 {
		t.Fatal("the two nodes did not connect")
	}

	before := a.warm.Load()
	time.Sleep(700 * time.Millisecond)
	sent := a.warm.Load() - before
	if sent < 2 {
		t.Fatalf("only %d keepalives in 700 ms at a 150 ms interval", sent)
	}
	if len(a.Peers()) != 1 || len(b.Peers()) != 1 {
		t.Fatalf("the path dropped while idle: %d and %d peers", len(a.Peers()), len(b.Peers()))
	}
}

// TestPunchAllIsLimited: a stale table must not turn into a flood.
func TestPunchAllIsLimited(t *testing.T) {
	a := startWarmNode(t, "A", 15*time.Second)
	a.PunchAll([]string{
		"127.0.0.1:9", "127.0.0.1:10", "127.0.0.1:11",
		"127.0.0.1:12", "127.0.0.1:13",
	}, 2)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		n := len(a.punching)
		a.mu.Unlock()
		if n > 2 {
			t.Fatalf("%d punches at once, limit was 2", n)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
