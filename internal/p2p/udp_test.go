package p2p

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// openPair wires two hubs together over the loopback, as a punched path
// would be, and returns a pipe on each side.
func openPair(t *testing.T) (*udpHub, *udpHub, *udpPipe, *udpPipe) {
	t.Helper()
	h1, err := newHub(0)
	if err != nil {
		t.Fatalf("hub 1: %v", err)
	}
	h2, err := newHub(0)
	if err != nil {
		h1.close()
		t.Fatalf("hub 2: %v", err)
	}
	t.Cleanup(func() { h1.close(); h2.close() })

	loop := func(h *udpHub) *net.UDPAddr {
		port := h.conn.LocalAddr().(*net.UDPAddr).Port
		return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	}
	// Each side needs a pipe towards the other's address before either
	// writes, or the datagrams arrive as unknown and are dropped.
	p1 := h1.open(loop(h2))
	p2 := h2.open(loop(h1))
	return h1, h2, p1, p2
}

// TestPipeCarriesMegabytes pushes a payload far bigger than the window, so
// that the congestion window has to open up, and checks every byte survives
// in order.
func TestPipeCarriesMegabytes(t *testing.T) {
	_, _, from, to := openPair(t)

	const size = 4 << 20
	src := make([]byte, size)
	for i := range src {
		src[i] = byte(i*7 + i/251)
	}
	want := sha256.Sum256(src)

	start := time.Now()
	go func() {
		if _, err := from.Write(src); err != nil {
			t.Logf("write: %v", err)
		}
	}()
	got := make([]byte, size)
	if _, err := io.ReadFull(to, got); err != nil {
		t.Fatalf("read: %v (got %d of %d bytes)", err, len(got), size)
	}
	took := time.Since(start)

	if sha256.Sum256(got) != want {
		t.Fatal("the payload came out changed")
	}
	rate := float64(size) / took.Seconds() / (1 << 20)
	t.Logf("4 MiB over the pipe in %v (%.1f MiB/s)", took.Round(time.Millisecond), rate)
	if rate < 1 {
		t.Fatalf("throughput %.2f MiB/s is too slow for a local link", rate)
	}
}

// TestWindowGrowsOnAcks: the window must open while packets are acked, and
// shrink when one is lost.
func TestWindowGrowsOnAcks(t *testing.T) {
	cc := newCongestion()
	start := cc.limit()
	for i := 0; i < 20; i++ {
		cc.onAck(4)
	}
	if cc.limit() <= start {
		t.Fatalf("window did not grow: %d -> %d", start, cc.limit())
	}
	grown := cc.limit()
	cc.onLoss()
	if cc.limit() >= grown {
		t.Fatalf("window did not shrink on loss: %d -> %d", grown, cc.limit())
	}
	cc.onTimeout()
	if cc.limit() != 1 {
		t.Fatalf("timeout left the window at %d, want 1", cc.limit())
	}
}

// TestRTOFollowsSamples: a fast path gets a short timeout, a slow one a
// longer timeout, within the bounds.
func TestRTOFollowsSamples(t *testing.T) {
	cc := newCongestion()
	if cc.rto() != initialRTO {
		t.Fatalf("before any sample the rto is %v", cc.rto())
	}
	for i := 0; i < 10; i++ {
		cc.sample(20 * time.Millisecond)
	}
	if cc.rto() > 400*time.Millisecond {
		t.Fatalf("a 20 ms path got an rto of %v", cc.rto())
	}
	for i := 0; i < 10; i++ {
		cc.sample(900 * time.Millisecond)
	}
	if cc.rto() < 900*time.Millisecond || cc.rto() > maxRTO {
		t.Fatalf("a 900 ms path got an rto of %v", cc.rto())
	}
}

// TestRetriesAreConfigurable: the limit comes from the node, not a constant.
func TestRetriesAreConfigurable(t *testing.T) {
	n := Node{}
	_ = n.SetTransport(7)
	if n.transportRetries() != 7 {
		t.Fatalf("retries read back as %d", n.transportRetries())
	}
}

// TestManySmallMessages: the chat case, where a stream of short writes
// must arrive in order and whole.
func TestManySmallMessages(t *testing.T) {
	_, _, from, to := openPair(t)

	const n = 300
	msg := func(i int) string { return fmt.Sprintf("m%04d|", i) }
	go func() {
		for i := 0; i < n; i++ {
			if _, err := from.Write([]byte(msg(i))); err != nil {
				return
			}
		}
	}()

	buf := make([]byte, len(msg(0)))
	var all []byte
	for i := 0; i < n; i++ {
		if _, err := io.ReadFull(to, buf); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		all = append(all, buf...)
	}
	for i := 0; i < n; i++ {
		size := len(msg(i))
		got := string(all[i*size : (i+1)*size])
		if got != msg(i) {
			t.Fatalf("message %d is %q, want %q", i, got, msg(i))
		}
	}
}
