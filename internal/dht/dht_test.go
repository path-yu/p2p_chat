package dht

import (
	"bytes"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestBencodeRoundTrip(t *testing.T) {
	msg := map[string]any{
		"t": "aa",
		"y": "q",
		"q": "get_peers",
		"a": map[string]any{
			"id":           string(make([]byte, 20)),
			"info_hash":    "01234567890123456789",
			"port":         int64(47821),
			"implied_port": int64(0),
			"list":         []any{int64(1), "two"},
		},
	}
	data, err := encode(nil, msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := decodeAll(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	again, err := encode(nil, got)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if !bytes.Equal(again, data) {
		t.Fatalf("round trip changed the message:/n got %q\nwant %q", again, data)
	}
	// Dictionary keys are sorted, as the wire requires.
	want := "d1:ad2:id20:\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00" +
		"12:implied_porti0e9:info_hash20:012345678901234567894:listli1e3:twoe4:porti47821ee1:q9:get_peers1:t2:aa1:y1:qe"
	if string(data) != want {
		t.Fatalf("encoded:/n%q/nwant:/n%q", data, want)
	}
}

func start(t *testing.T) *Node {
	t.Helper()
	n := New(Config{Timeout: 500 * time.Millisecond, Logf: t.Logf})
	if err := n.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(n.Close)
	return n
}

// TestAnnounceAndLookup runs the rendezvous the app depends on: a peer
// announces its port to a node both sides know, and another finds it there.
func TestAnnounceAndLookup(t *testing.T) {
	meeting := start(t)
	alice := start(t)
	bob := start(t)

	ua := meeting.conn.LocalAddr().(*net.UDPAddr)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(ua.Port))
	alice.addNode(meeting.ID(), addr)
	bob.addNode(meeting.ID(), addr)

	key := NetworkKey("test-net")

	if err := alice.Announce(key, 47821); err != nil {
		t.Fatalf("announce: %v", err)
	}
	got := bob.Lookup(key)
	if len(got) == 0 {
		t.Fatal("the lookup found no peer")
	}
	want := "127.0.0.1:47821"
	for _, p := range got {
		if p == want {
			return
		}
	}
	t.Fatalf("found %v, want %s among them", got, want)
}

// TestNetworkKeySeparates: two names give two swarms, so peers of another
// network don't show up.
func TestNetworkKeySeparates(t *testing.T) {
	a, b := NetworkKey("home"), NetworkKey("office")
	if a == b {
		t.Fatal("two network names share a key")
	}
	if a != NetworkKey("home") {
		t.Fatal("a network key is not stable")
	}
}
