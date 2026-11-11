package p2p

import (
	"path/filepath"
	"testing"
	"time"

	"ptp_chat/internal/identity"
	"ptp_chat/internal/wire"
)

func startNode(t *testing.T, name string, port int) *Node {
	t.Helper()
	id, err := identity.Load(filepath.Join(t.TempDir(), name), name)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	n := New(id, port)
	if err := n.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(n.Close)
	return n
}

// TestHandshakeAndDelivery checks that two peers agree on the session key
// and that a signed message arrives intact.
func TestHandshakeAndDelivery(t *testing.T) {
	a := startNode(t, "A", 0)
	b := startNode(t, "B", 0)

	got := make(chan wire.Envelope, 4)
	b.OnEvent = func(ev Event) {
		if ev.Kind == Message {
			got <- ev.Env
		}
	}

	a.Connect(b.Addr)
	waitPeers(t, a, 1)
	waitPeers(t, b, 1)

	a.Broadcast(&wire.Envelope{Type: wire.Chat, Room: "lobby", Body: "你好，点对点世界"})

	select {
	case env := <-got:
		if env.Body != "你好，点对点世界" {
			t.Fatalf("body = %q", env.Body)
		}
		if env.Room != "lobby" {
			t.Fatalf("room = %q", env.Room)
		}
		if err := env.Verify(); err != nil {
			t.Fatalf("signature: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no message arrived")
	}
}

// TestMeshCloses checks that a peer introduced by one node connects to the
// others on its own.
func TestMeshCloses(t *testing.T) {
	a := startNode(t, "A", 0)
	b := startNode(t, "B", 0)
	c := startNode(t, "C", 0)

	b.Connect(a.Addr)
	waitPeers(t, a, 1)
	waitPeers(t, b, 1)

	c.Connect(a.Addr)
	waitPeers(t, a, 2)

	// The node that knows both hands their addresses over, so b and c meet.
	waitPeers(t, b, 2)
	waitPeers(t, c, 2)
}

func waitPeers(t *testing.T, n *Node, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(n.Peers()) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s has %d peers, want %d", n.ID.Name, len(n.Peers()), want)
}
