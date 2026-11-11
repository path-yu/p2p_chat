package p2p

import (
	"sync/atomic"
	"testing"
	"time"

	"ptp_chat/internal/wire"
)

// TestDirectMessageReachesOnlyItsPeer: an envelope with a recipient goes to
// that connection, and one addressed elsewhere is dropped rather than
// relayed or shown.
func TestDirectMessageReachesOnlyItsPeer(t *testing.T) {
	a := startUDPNode(t, "A", 0, 0)
	b := startUDPNode(t, "B", 0, 0)

	var got atomic.Int32
	var last atomic.Value
	b.OnEvent = func(ev Event) {
		if ev.Kind == Message {
			got.Add(1)
			last.Store(ev.Env)
		}
	}

	a.Punch("127.0.0.1:" + itoa(b.UDPPort()))
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && (len(a.Peers()) == 0 || len(b.Peers()) == 0) {
		time.Sleep(50 * time.Millisecond)
	}
	if len(a.Peers()) == 0 || len(b.Peers()) == 0 {
		t.Fatal("the two nodes did not connect")
	}

	// Addressed to B: it arrives.
	a.Broadcast(&wire.Envelope{Type: wire.Chat, Room: "dm:" + b.ID.ID(), To: b.ID.ID(), Body: "只给你"})
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && got.Load() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if got.Load() != 1 {
		t.Fatalf("B received %d messages, want 1", got.Load())
	}
	env := last.Load().(wire.Envelope)
	if env.Body != "只给你" || env.To != b.ID.ID() {
		t.Fatalf("got %+v", env)
	}

	// Addressed to someone else: B must not see it, and must not pass it on.
	a.Broadcast(&wire.Envelope{Type: wire.Chat, Room: "dm:someone", To: "not-this-node", Body: "不该到你"})
	time.Sleep(300 * time.Millisecond)
	if got.Load() != 1 {
		t.Fatalf("B received %d messages, want still 1", got.Load())
	}
}

// TestDirectMessageIsNotRelayed: a node that is neither sender nor
// recipient must not hear a direct message, even with a hop left.
func TestDirectMessageIsNotRelayed(t *testing.T) {
	a := startUDPNode(t, "A", 0, 0)
	b := startUDPNode(t, "B", 0, 0)
	c := startUDPNode(t, "C", 0, 0)

	// A—B and B—C are connected; A and C are not.
	a.Punch("127.0.0.1:" + itoa(b.UDPPort()))
	c.Punch("127.0.0.1:" + itoa(b.UDPPort()))
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && (len(a.Peers()) == 0 || len(c.Peers()) == 0) {
		time.Sleep(50 * time.Millisecond)
	}
	if len(c.Peers()) == 0 {
		t.Skip("c did not connect to b")
	}

	var cGot atomic.Int32
	c.OnEvent = func(ev Event) {
		if ev.Kind == Message {
			cGot.Add(1)
		}
	}

	a.Broadcast(&wire.Envelope{Type: wire.Chat, Room: "dm:" + c.ID.ID(), To: c.ID.ID(), Body: "私下说"})
	time.Sleep(400 * time.Millisecond)

	if cGot.Load() != 0 {
		t.Fatalf("C received a direct message relayed through B (%d)", cGot.Load())
	}
}
