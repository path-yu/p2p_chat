// Command lumend runs a Lumen node on a server: no window, no graphics
// library, one static binary. It joins the rendezvous network, opens the
// port peers punch towards, and relays what it receives, so that a laptop
// behind a router can reach it from anywhere.
//
//	lumend -network mynet                 the node, typing into the terminal sends to the room
//	lumend -network mynet -name 机房       a name of its own
//	lumend -port 47821 -udp-port 47823     the ports to listen on
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"ptp_chat/internal/identity"
	"ptp_chat/internal/p2p"
	"ptp_chat/internal/store"
	"ptp_chat/internal/wire"
)

func main() {
	name := flag.String("name", "", "显示名称")
	port := flag.Int("port", 47821, "TCP 监听端口")
	udpPort := flag.Int("udp-port", 47823, "UDP 打洞端口")
	discPort := flag.Int("discovery-port", 47822, "局域网发现端口，0 关闭")
	network := flag.String("network", "lumen", "会合网络名：和笔记本用同一个才会互相发现")
	peer := flag.String("peer", "", "手动连接的 TCP 地址，逗号分隔")
	punch := flag.String("punch", "", "手动打洞的 UDP 端点，逗号分隔")
	noDHT := flag.Bool("no-dht", false, "不加入 DHT 会合")
	stun := flag.String("stun", "", "STUN 服务器，逗号分隔；默认国内四个 + 国外两个")
	retries := flag.Int("retries", 20, "打洞链路上一个包最多重发几次才判定断开")
	send := flag.String("send", "", "连上后把文件发给对端")
	autoAccept := flag.String("recv", "", "自动接收文件到这个目录")
	lookup := flag.Duration("lookup", 45*time.Second, "多久找一次节点")
	announce := flag.Duration("announce", 10*time.Minute, "多久宣告一次自己")
	dir := flag.String("dir", "", "数据目录，默认 ~/.lumen")
	room := flag.String("room", "lobby", "终端里打字发到哪个频道")
	flag.Parse()

	dataDir := *dir
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
		dataDir = filepath.Join(home, ".lumen")
	}
	st, err := store.Open(dataDir)
	if err != nil {
		log.Fatalf("数据目录: %v", err)
	}

	hint := st.Settings().Name
	if *name != "" {
		hint = *name
	}
	if hint == "" {
		hint = "服务器节点"
	}
	id, err := identity.Load(dataDir, hint)
	if err != nil {
		log.Fatalf("身份: %v", err)
	}
	if *name != "" {
		id.Name = *name
	} else if id.Name == "" || id.Name == "匿名节点" {
		id.Name = wire.RandomName(id.ID())
	}
	_ = id.Save(dataDir)

	// Every step the node takes goes to stdout, which is the SSH session's
	// log, and to lumen.log, which survives it.
	var mu sync.Mutex
	out := io.MultiWriter(os.Stdout)
	if f, err := os.OpenFile(filepath.Join(dataDir, "lumen.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		defer f.Close()
		out = io.MultiWriter(os.Stdout, f)
	}
	logger := log.New(out, "", log.Ltime)
	say := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Printf("· "+format+"\n", args...)
		logger.Printf(format, args...)
	}

	node := p2p.New(id, *port)
	if err := node.Start(); err != nil {
		log.Fatalf("监听 %d: %v", *port, err)
	}
	defer node.Close()
	node.Logf = say

	fmt.Println("Lumen 服务器节点")
	fmt.Printf("  名字     %s\n", id.Name)
	fmt.Printf("  本机地址 %s（TCP %d）\n", node.Addr, node.Port)
	fmt.Printf("  节点 ID  %s\n", id.ID())
	fmt.Printf("  数据目录 %s\n", dataDir)

	if *discPort > 0 {
		node.StartDiscovery(*discPort)
	}
	if err := node.StartUDP(*udpPort); err != nil {
		say("UDP 打洞端口不可用: %v", err)
	} else {
		fmt.Printf("  打洞端口 UDP %d\n", node.UDPPort())
	}
	_ = node.SetTransport(*retries)
	for _, addr := range strings.Split(*peer, ",") {
		if addr = strings.TrimSpace(addr); addr != "" {
			node.Connect(addr)
		}
	}
	for _, addr := range strings.Split(*punch, ",") {
		if addr = strings.TrimSpace(addr); addr != "" {
			node.Punch(addr)
		}
	}
	if !*noDHT {
		var stunServers []string
		if *stun != "" {
			for _, a := range strings.Split(*stun, ",") {
				if a = strings.TrimSpace(a); a != "" {
					stunServers = append(stunServers, a)
				}
			}
		}
		node.Reach(p2p.ReachConfig{
			Network: *network, STUN: stunServers,
			Lookup: *lookup, Announce: *announce, Logf: say,
		})
	}

	node.OnEvent = func(ev p2p.Event) {
		if ev.File != nil {
			switch ev.Kind {
			case p2p.FileOffer:
				if *autoAccept != "" {
					if err := node.AcceptFile(ev.File.ID, *autoAccept); err != nil {
						say("接收失败: %v", err)
					} else {
						say("接收 %s（%s）", ev.File.Name, humanSize(ev.File.Size))
					}
				} else {
					say("%s 想发给你 %s（%s），加 -recv 目录可自动接收",
						ev.File.Peer, ev.File.Name, humanSize(ev.File.Size))
				}
			case p2p.FileProgress:
				rate := ""
				if ev.File.Rate > 0 {
					rate = " · " + humanSize(ev.File.Rate) + "/s"
				}
				say("%s %d%%（%s / %s%s）", ev.File.Name, ev.File.Percent(),
					humanSize(ev.File.Done), humanSize(ev.File.Size), rate)
			case p2p.FileDone:
				if ev.File.Incoming {
					say("收到 %s → %s", ev.File.Name, ev.File.Path)
				} else {
					say("已发出 %s（%s）", ev.File.Name, humanSize(ev.File.Size))
				}
			case p2p.FileFailed:
				say("%s 失败: %s", ev.File.Name, ev.File.Err)
			}
		}
		switch ev.Kind {
		case p2p.PeerJoined:
			say("＋ %s 连上了（%s）", ev.Peer.Name, ev.Peer.Addr)
			if *send != "" {
				if st, err := node.OfferFile("lobby", *send); err != nil {
					say("发送 %s 失败: %v", *send, err)
				} else {
					say("请求发送 %s（%s）", st.Name, humanSize(st.Size))
				}
			}
		case p2p.PeerLeft:
			say("－ %s 离开了", ev.Peer.Name)
		case p2p.Message:
			say("[%s] %s: %s", ev.Env.Room, ev.Env.Name, ev.Env.Body)
			_ = st.Append(store.Msg{
				ID: ev.Env.ID, Room: ev.Env.Room, From: ev.Env.From,
				Name: ev.Env.Name, Text: ev.Env.Body, TS: ev.Env.TS,
			})
		}
	}

	fmt.Println()
	fmt.Println("在下面打一行字回车，会发到频道「" + *room + "」，笔记本上就能看到；Ctrl+C 结束。")

	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			env := &wire.Envelope{Type: wire.Chat, Room: *room, Body: line}
			node.Broadcast(env)
			say("[我 → %s] %s", *room, line)
		}
		// A daemon outlives its terminal: EOF only stops reading.
	}()

	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		last := ""
		for range t.C {
			r := node.ReachStatus()
			dash := func(s string) string {
				if s == "" {
					return "—"
				}
				return s
			}
			line := fmt.Sprintf("穿透｜网络 %s｜公网 %s｜映射 %s｜NAT %s｜DHT %d 节点·发现 %d",
				r.Network, dash(r.Public), dash(r.Mapping), dash(r.NAT), r.Nodes, r.Found)
			if line != last {
				last = line
				say("%s", line)
			}
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println()
	say("收到退出信号，收拾端口映射…")
}

// humanSize writes a byte count the way a person reads it.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
