package main

import (
	"testing"

	"ptp_chat/internal/p2p"
)

// TestPunchVerdict: the interface has to say, in one look, whether someone
// on another network can reach this node.
func TestPunchVerdict(t *testing.T) {
	cases := []struct {
		name string
		r    p2p.Reach
		want string
		tone int
	}{
		{
			name: "a mapped port needs no punching",
			r:    p2p.Reach{NAT: "cone", Public: "1.2.3.4:47823", Mapping: "UPnP"},
			want: "可直连", tone: 0,
		},
		{
			name: "cone NAT punches",
			r:    p2p.Reach{NAT: "cone", Public: "1.2.3.4:47823"},
			want: "可打洞", tone: 0,
		},
		{
			name: "symmetric NAT mostly fails",
			r:    p2p.Reach{NAT: "symmetric", Public: "1.2.3.4:47823"},
			want: "打洞受限", tone: 2,
		},
		{
			name: "no UDP at all",
			r:    p2p.Reach{NATErr: "stun: no answer"},
			want: "打洞不可用", tone: 2,
		},
		{
			name: "still asking",
			r:    p2p.Reach{Ready: true},
			want: "检测中", tone: 1,
		},
		{
			name: "no rendezvous at all",
			r:    p2p.Reach{},
			want: "仅局域网", tone: 1,
		},
	}
	for _, c := range cases {
		got := punchVerdict(c.r)
		if got.short != c.want || got.tone != c.tone {
			t.Errorf("%s: got %q tone %d, want %q tone %d", c.name, got.short, got.tone, c.want, c.tone)
		}
		if got.long == "" {
			t.Errorf("%s: no explanation", c.name)
		}
	}
}

// TestNatName keeps the technical name of the type honest.
func TestNatName(t *testing.T) {
	if natName("cone") != "锥形 NAT" || natName("symmetric") != "对称 NAT" {
		t.Fatalf("cone=%q symmetric=%q", natName("cone"), natName("symmetric"))
	}
	if natName("") != "检测中…" {
		t.Fatalf("empty=%q", natName(""))
	}
}
