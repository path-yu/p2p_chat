// Command stunprobe asks a list of STUN servers who it is, so that the app
// can be given the ones that answer from where it runs.
//
//	go run ./cmd/stunprobe
//	go run ./cmd/stunprobe stun.example.com:3478
package main

import (
	"fmt"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	"ptp_chat/internal/nat"
)

// candidates are the public STUN servers worth trying, the ones in China
// first: they are close to a Chinese network and outlive the rest.
var candidates = []struct {
	name string
	host string
}{
	{"腾讯", "stun.qq.com:3478"},
	{"小米", "stun.miwifi.com:3478"},
	{"哔哩哔哩", "stun.chat.bilibili.com:3478"},
	{"斗鱼", "stun.douyucdn.cn:18000"},
	{"芒果TV", "stun.hitv.com:3478"},
	{"迅雷", "stun.xunlei.com:3478"},
	{"网易", "stun.163.com:3478"},
	{"V2EX", "stun.v2ex.com:3478"},
	{"Google", "stun.l.google.com:19302"},
	{"Google 备", "stun1.l.google.com:19302"},
	{"Cloudflare", "stun.cloudflare.com:3478"},
	{"Nextcloud", "stun.nextcloud.com:3478"},
	{"Twilio", "global.stun.twilio.com:3478"},
	{"Syncthing", "stun.syncthing.net:3478"},
}

type result struct {
	label string
	host  string
	ep    nat.Endpoint
	rtt   time.Duration
	err   error
}

func main() {
	if len(os.Args) > 1 {
		candidates = nil
		for _, h := range os.Args[1:] {
			candidates = append(candidates, struct {
				name string
				host string
			}{h, h})
		}
	}

	var wg sync.WaitGroup
	out := make([]result, len(candidates))
	for i, c := range candidates {
		wg.Add(1)
		go func(i int, c struct {
			name string
			host string
		}) {
			defer wg.Done()
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
			if err != nil {
				out[i] = result{label: c.name, host: c.host, err: err}
				return
			}
			defer conn.Close()
			start := time.Now()
			ep, err := nat.Discover(conn, c.host, 2*time.Second)
			out[i] = result{label: c.name, host: c.host, ep: ep, rtt: time.Since(start), err: err}
		}(i, c)
	}
	wg.Wait()

	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].err == nil) != (out[j].err == nil) {
			return out[i].err == nil
		}
		return out[i].rtt < out[j].rtt
	})

	fmt.Printf("%-10s %-32s %-24s %s\n", "结果", "服务器", "它看到的地址", "耗时")
	for _, r := range out {
		if r.err != nil {
			fmt.Printf("%-10s %-32s %-24s %s\n", "✗ 无响应", r.host, "—", r.err)
			continue
		}
		fmt.Printf("%-10s %-32s %-24s %d ms\n", "✓ "+r.label, r.host, r.ep.String(), r.rtt.Milliseconds())
	}

	var ok, ports int
	var first net.IP
	for _, r := range out {
		if r.err != nil {
			continue
		}
		if ok == 0 {
			first = r.ep.IP
		}
		ok++
		if r.ep.IP.Equal(first) {
			ports++
		}
	}
	fmt.Printf("\n%d/%d 有应答；其中 %d 个看到的公网 IP 一致\n", ok, len(out), ports)
}
