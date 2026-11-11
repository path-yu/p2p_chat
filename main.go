// Lumen is a peer-to-peer chat: no server, no account. Every node keeps its
// own Ed25519 identity, opens an X25519 session with the peers it meets,
// signs what it writes and relays what the others wrote, so the mesh
// carries a message even where no two peers can see each other directly.
//
// Lumen is the window, and it is built as a Windows program rather than a
// console one (see tools/build.ps1), so opening it shows the window alone.
// A node in a terminal, or on a server, is cmd/lumend:
//
//	Lumen                       the window
//	Lumen -peer 10.0.0.7:47821  a peer given by address
//	lumend -network mynet       the same node, in a terminal
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"ptp_chat/internal/identity"
	"ptp_chat/internal/p2p"
	"ptp_chat/internal/store"
	"ptp_chat/internal/wire"
)

// logDir is where a failure is written, once the data directory is known.
// A window program has no console, so this is the only place left.
var logDir string

func main() {
	defer func() {
		if r := recover(); r != nil {
			crash(r)
		}
	}()

	name := flag.String("name", "", "显示名称，默认沿用上次的")
	port := flag.Int("port", 47821, "监听端口，0 为随机")
	disc := flag.Int("discovery-port", 47822, "局域网发现端口")
	peers := flag.String("peer", "", "手动连接的节点，逗号分隔的 host:port")
	udpPort := flag.Int("udp-port", 47823, "UDP 打洞端口，0 为随机")
	punch := flag.String("punch", "", "手动打洞的 UDP 端点，逗号分隔的 host:port")
	network := flag.String("network", "lumen", "会合网络名：同名才会在公网上互相发现")
	noDHT := flag.Bool("no-dht", false, "不加入 DHT 会合，只连局域网和手动地址")
	bootstrap := flag.String("bootstrap", "", "DHT 引导节点，逗号分隔，默认用公开路由器")
	stun := flag.String("stun", "", "STUN 服务器，逗号分隔；默认国内四个 + 国外两个")
	retries := flag.Int("retries", 20, "打洞链路上一个包最多重发几次才判定断开")
	lookup := flag.Duration("lookup", 45*time.Second, "多久在会合网络里找一次节点")
	announce := flag.Duration("announce", 10*time.Minute, "多久向会合网络宣告一次自己")
	appearance := flag.String("appearance", "auto", "外观：auto、light 或 dark")
	dir := flag.String("dir", "", "数据目录，默认 ~/.lumen")
	diag := flag.Bool("diag", false, "显示环境自检：打不开时先跑这个")
	flag.Parse()

	dataDir := *dir
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fail("", "找不到用户目录: %v", err)
		}
		dataDir = filepath.Join(home, ".lumen")
	}

	logDir = dataDir
	if *diag {
		showDiag(dataDir, *port)
		return
	}

	st, err := store.Open(dataDir)
	if err != nil {
		fail(dataDir, "数据目录 %s: %v", dataDir, err)
	}

	hint := st.Settings().Name
	if *name != "" {
		hint = *name
	}
	if hint == "" {
		hint = "匿名节点"
	}
	id, err := identity.Load(dataDir, hint)
	if err != nil {
		fail(dataDir, "身份: %v", err)
	}
	if *name != "" {
		id.Name = *name
	} else if id.Name == "" || id.Name == "匿名节点" {
		id.Name = wire.RandomName(id.ID())
	}
	_ = id.Save(dataDir)

	node := p2p.New(id, *port)
	if err := node.Start(); err != nil {
		fail(dataDir, "监听 %d 失败（端口被占用？）: %v", *port, err)
	}
	defer node.Close()

	// The window has no room for what the network does — the port mapping,
	// STUN, the DHT's rendezvous — so it goes to a file beside the identity.
	f, err := os.OpenFile(filepath.Join(dataDir, "lumen.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		defer f.Close()
		logger := log.New(f, "", log.Ltime)
		node.Logf = func(format string, args ...any) { logger.Printf(format, args...) }
	}

	node.StartDiscovery(*disc)
	if err := node.StartUDP(*udpPort); err != nil {
		node.Logf("UDP 打洞端口不可用: %v", err)
	}
	_ = node.SetTransport(*retries)
	for _, addr := range strings.Split(*punch, ",") {
		if addr = strings.TrimSpace(addr); addr != "" {
			node.Punch(addr)
		}
	}
	if !*noDHT {
		bs := p2p.DefaultBootstrap
		if *bootstrap != "" {
			bs = strings.Split(*bootstrap, ",")
		}
		var stunServers []string
		if *stun != "" {
			for _, a := range strings.Split(*stun, ",") {
				if a = strings.TrimSpace(a); a != "" {
					stunServers = append(stunServers, a)
				}
			}
		}
		node.Reach(p2p.ReachConfig{
			Network: *network, Bootstrap: bs, STUN: stunServers,
			Lookup: *lookup, Announce: *announce,
		})
	}
	for _, addr := range strings.Split(*peers, ",") {
		if addr = strings.TrimSpace(addr); addr != "" {
			node.Connect(addr)
		}
	}

	a := newApp(id, st, node, dataDir)
	node.OnEvent = a.onEvent
	switch *appearance {
	case "dark":
		on := true
		a.forceDark = &on
	case "light":
		off := false
		a.forceDark = &off
	}
	if a.forceDark != nil {
		node.Logf("外观：%v（命令行 -appearance %s）", *a.forceDark, *appearance)
	} else {
		node.Logf("外观：跟随系统")
	}

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:         "Lumen",
			Width:         1140,
			Height:        760,
			MinWidth:      920,
			MinHeight:     580,
			TitleBarStyle: mygo.TitleBarHidden,
			Content:       ui.View(a.view),
		})
		a.setWindow(win)
		// A drop that lands anywhere else must still do something rather
		// than vanish: it joins the attachments waiting in the input.
		win.OnFileDrop(func(ev *mygo.FileDropEvent) {
			if len(ev.Paths) == 0 {
				return
			}
			win.Update(func() { a.addPending(ev.Paths) })
		})
		if err := win.SetIcon(appIcon()); err != nil {
			node.Logf("图标: %v", err)
		}
	})
	if err := mygo.App.Run(); err != nil {
		fail(dataDir, "窗口: %v", err)
	}
}

// fail reports a startup problem where someone can still read it: a window
// program has no console, so the message goes to lumen.log, and to stderr
// when it is run from a terminal.
func fail(dataDir, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, "Lumen: "+msg)
	if dataDir != "" {
		path := filepath.Join(dataDir, "lumen.log")
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "%s 启动失败: %s\n", time.Now().Format("15:04:05"), msg)
			f.Close()
			msg += "\n\n详见 " + path
		}
	}
	messageBox("Lumen 启动失败", msg, true)
	os.Exit(1)
}

// crash writes what panicked, then says so: a window program that closes
// without a word is impossible to report.
func crash(r any) {
	stack := make([]byte, 1<<16)
	n := runtime.Stack(stack, true)
	msg := fmt.Sprintf("%v\n\n%s", r, stack[:n])

	dir := logDir
	if dir == "" {
		dir = os.TempDir()
	}
	path := filepath.Join(dir, "crash.log")
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintf(f, "=== %s ===\n%s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
		f.Close()
	}
	short := fmt.Sprintf("%v", r)
	if len(short) > 400 {
		short = short[:400] + "…"
	}
	messageBox("Lumen 崩溃了",
		short+"\n\n完整堆栈已写入：\n"+path, true)
	os.Exit(2)
}

// showDiag tells what this machine offers a window program, which is the
// first thing to look at when nothing appears on screen.
func showDiag(dataDir string, port int) {
	line := func(ok bool, format string, args ...any) string {
		mark := "✓"
		if !ok {
			mark = "✗"
		}
		return mark + " " + fmt.Sprintf(format, args...)
	}

	writable := false
	if err := os.MkdirAll(dataDir, 0o700); err == nil {
		if f, err := os.CreateTemp(dataDir, "diag*"); err == nil {
			f.Close()
			os.Remove(f.Name())
			writable = true
		}
	}
	portFree := false
	if ln, err := net.Listen("tcp4", fmt.Sprintf(":%d", port)); err == nil {
		portFree = true
		ln.Close()
	}

	text := strings.Join([]string{
		"Lumen 环境自检",
		"",
		line(true, "程序 %s/%s，Go %s", runtime.GOOS, runtime.GOARCH, runtime.Version()),
		line(writable, "数据目录 %s", dataDir),
		line(canLoad("d3d11.dll"), "Direct3D 11（画窗口用）"),
		line(canLoad("dcomp.dll"), "DirectComposition（窗口合成用）"),
		line(canLoad("dwmapi.dll"), "DWM（阴影/圆角用）"),
		line(portFree, "端口 %d", port),
		"",
		"任何一行是 ✗，就是它挡住了启动；把这段发回给开发者。",
	}, "\n")
	messageBox("Lumen", text, false)
}
