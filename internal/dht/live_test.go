package dht

import (
	"net"
	"os"
	"testing"
	"time"
)

// TestLiveProbe talks to the real network, for a look at what it answers.
func TestLiveProbe(t *testing.T) {
	if os.Getenv("LUMEN_LIVE") == "" {
		t.Skip("set LUMEN_LIVE=1 to talk to the network")
	}
	n := New(Config{
		Bootstrap: []string{
			"router.bittorrent.com:6881",
			"dht.transmissionbt.com:6881",
			"router.utorrent.com:6881",
		},
		Timeout: 4 * time.Second,
		Logf:    t.Logf,
	})
	if err := n.Start(); err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) && n.count() < 12 {
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("nodes in table: %d", n.count())

	n.mu.Lock()
	var addrs []string
	for _, r := range n.known {
		addrs = append(addrs, r.addr)
		if len(addrs) >= 5 {
			break
		}
	}
	n.mu.Unlock()

	key := NetworkKey("live-probe")
	for _, addr := range addrs {
		ua, err := net.ResolveUDPAddr("udp4", addr)
		if err != nil {
			t.Logf("%s: %v", addr, err)
			continue
		}
		resp, err := n.query(ua, "get_peers", map[string]any{
			"id":        n.ID(),
			"info_hash": string(key[:]),
		})
		if err != nil {
			t.Logf("%s: %v", addr, err)
			continue
		}
		keys := make([]string, 0, len(resp))
		for k := range resp {
			keys = append(keys, k)
		}
		t.Logf("%s answered with %v (token %d bytes)", addr, keys, len(stringOf(resp["token"])))
	}

	t.Logf("announce: %v", n.Announce(key, 47823))
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}
