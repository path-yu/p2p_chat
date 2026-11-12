package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ptp_chat/internal/identity"
	"ptp_chat/internal/p2p"
)

// palette holds the colors of one appearance, beside the theme's.
type palette struct {
	bg         ui.Color
	sidebar    ui.Color
	sidebarEnd ui.Color
	panel      ui.Color
	chrome     ui.Color
	hairline   ui.Color
	hover      ui.Color
	accentSoft ui.Color
	mineA      ui.Color
	mineB      ui.Color
	green      ui.Color
}

// theme builds the window's theme: Apple's grays and blues, in the
// appearance the user chose.
func (a *app) theme(c *ui.Context) *ui.Theme {
	var t *ui.Theme
	if a.dark {
		t = ui.DarkTheme()
		t.Background = ui.Hex("#0C0C10")
		t.Surface = ui.RGBA(255, 255, 255, 0.065)
		t.SurfaceHover = ui.RGBA(255, 255, 255, 0.10)
		t.SurfacePressed = ui.RGBA(255, 255, 255, 0.14)
		t.Border = ui.RGBA(255, 255, 255, 0.09)
		t.Text = ui.Hex("#F2F2F7")
		t.TextMuted = ui.RGBA(235, 235, 245, 0.50)
		t.Accent = ui.Hex("#0A84FF")
		t.Selection = ui.RGBA(10, 132, 255, 0.36)
		t.Scrollbar = ui.RGBA(255, 255, 255, 0.10)
		t.Danger = ui.Hex("#FF453A")
		t.Success = ui.Hex("#30D158")
		a.pal = palette{
			bg:         ui.Hex("#0C0C10"),
			sidebar:    ui.Hex("#16161D"),
			sidebarEnd: ui.Hex("#0D0D12"),
			panel:      ui.Hex("#111116"),
			chrome:     ui.Hex("#16161D"),
			hairline:   ui.RGBA(255, 255, 255, 0.075),
			hover:      ui.RGBA(255, 255, 255, 0.075),
			accentSoft: ui.RGBA(10, 132, 255, 0.17),
			mineA:      ui.Hex("#3AA3FF"),
			mineB:      ui.Hex("#0A67FF"),
			green:      ui.Hex("#32D74B"),
		}
	} else {
		t = ui.LightTheme()
		t.Background = ui.Hex("#FFFFFF")
		t.Surface = ui.RGBA(0, 0, 0, 0.045)
		t.SurfaceHover = ui.RGBA(0, 0, 0, 0.075)
		t.SurfacePressed = ui.RGBA(0, 0, 0, 0.11)
		t.Border = ui.RGBA(0, 0, 0, 0.09)
		t.Text = ui.Hex("#1C1C1E")
		t.TextMuted = ui.RGBA(60, 60, 67, 0.55)
		t.Accent = ui.Hex("#007AFF")
		t.Selection = ui.RGBA(0, 122, 255, 0.22)
		t.Scrollbar = ui.RGBA(0, 0, 0, 0.12)
		t.Danger = ui.Hex("#FF3B30")
		t.Success = ui.Hex("#34C759")
		a.pal = palette{
			bg:         ui.Hex("#FFFFFF"),
			sidebar:    ui.Hex("#F7F7FA"),
			sidebarEnd: ui.Hex("#EFEFF4"),
			panel:      ui.Hex("#FAFAFC"),
			chrome:     ui.Hex("#F7F7FA"),
			hairline:   ui.RGBA(0, 0, 0, 0.075),
			hover:      ui.RGBA(0, 0, 0, 0.055),
			accentSoft: ui.RGBA(0, 122, 255, 0.11),
			mineA:      ui.Hex("#34A0FF"),
			mineB:      ui.Hex("#0071E3"),
			green:      ui.Hex("#34C759"),
		}
	}
	t.Radius = 10
	t.Spacing = 4
	t.FontSize = 13
	c.SetTheme(t)
	return t
}

func (a *app) view(c *ui.Context) {
	a.now = c.Now()
	if !a.ready {
		a.dark = c.Theme().Dark
		a.ready = true
	}
	// A text area takes the height its rows ask for a pass or two after the
	// one that asks, so the first frames ask for another.
	if a.frames < 3 {
		a.frames++
		c.Invalidate()
	}
	if a.forceDark != nil {
		a.dark = *a.forceDark
	}
	if c.Shortcut(ui.Cmd, ui.KeyI) {
		a.info = !a.info
	}
	if c.Shortcut(ui.Cmd, ui.KeyK) {
		a.copyInvite()
	}
	if c.Shortcut(ui.Cmd, ui.KeyL) {
		a.connOpen = !a.connOpen
	}
	if c.Shortcut(ui.Cmd, ui.KeyP) {
		a.openProfile()
	}
	if !a.nameDirtyAt.IsZero() && a.now.Sub(a.nameDirtyAt) > 600*time.Millisecond {
		a.saveName()
	}

	t := a.theme(c)
	a.peers = a.node.Peers()
	a.syncChrome()

	root := ui.Column(c).Fill().Background(t.Background)
	root.Children(func() {
		a.titleBar(c, t, c.TitleBar())
		ui.Row(c).Grow(1).FillWidth().Children(func() {
			a.sidebar(c, t)
			a.conversation(c, t)
			if a.info {
				a.inspector(c, t)
			}
		})
		a.profileDialog(c, t)
	})
}

// titleBar draws the window's title bar: the app's mark and name, the room
// for the appearance, and room on the right for the system's three window
// buttons, which Windows draws over the window.
func (a *app) titleBar(c *ui.Context, t *ui.Theme, tb ui.TitleBar) {
	height := max(tb.Height, 38)
	bar := ui.Column(c).Height(height).FillWidth().
		Gradient(a.pal.chrome, a.pal.chrome, 0).
		BorderWidth(0, 0, 1, 0).BorderColor(a.pal.hairline)
	bar.Children(func() {
		ui.Row(c).Fill().Padding(0, tb.Right+10, 0, tb.Left+14).Children(func() {
			drag := ui.Row(c).Grow(1).AlignItems(ui.Center).Gap(9).DragWindow()
			drag.Children(func() {
				a.logo(c, 17)
				ui.Text(c, "Lumen").FontSize(12.5).FontWeight(620).
					TextColor(t.Text).SingleLine()
				ui.Text(c, "·").FontSize(11).TextColor(t.TextMuted)
				ui.Text(c, "点对点聊天").FontSize(10.5).TextColor(t.TextMuted).SingleLine()
			})
			a.appearanceSwitch(c, t)
		})
	})
}

// appearanceSwitch is the segmented control that turns the day and the
// night: the window, the widgets and the system's window buttons follow.
func (a *app) appearanceSwitch(c *ui.Context, t *ui.Theme) {
	seg := ui.Row(c).Gap(1).Padding(2).Radius(8).
		Background(a.pal.hover).Border(1, a.pal.hairline)
	seg.Children(func() {
		a.appearanceChip(c, t, icoSun, !a.dark, func() { a.setDark(false) })
		a.appearanceChip(c, t, icoMoon, a.dark, func() { a.setDark(true) })
	})
}

func (a *app) appearanceChip(c *ui.Context, t *ui.Theme, icon *ui.SVG, on bool, fn func()) {
	chip := ui.Box(c).Size(22, 18).Radius(6).
		AlignItems(ui.Center).Justify(ui.Center).Cursor(ui.CursorPointer).
		Tooltip(map[bool]string{true: "深色", false: "浅色"}[on])
	if on {
		chip.Background(t.Surface)
	} else if chip.Hovered() {
		chip.Background(t.SurfaceHover)
	}
	chip.Children(func() {
		col := t.TextMuted
		if on {
			col = t.Accent
		}
		ui.Icon(c, icon).FontSize(11.5).TextColor(col)
	})
	if chip.Clicked() {
		fn()
	}
}

// ---------------------------------------------------------------- sidebar

func (a *app) sidebar(c *ui.Context, t *ui.Theme) {
	col := ui.Column(c).Width(268).FillHeight().Padding(0, 10, 0, 10)
	col.Gradient(a.pal.sidebar, a.pal.sidebarEnd, 180)
	col.BorderWidth(0, 1, 0, 0).BorderColor(a.pal.hairline)
	col.Children(func() {
		ui.Row(c).Height(10)
		a.sectionLabel(c, t, "频道", "")
		for i := range a.rooms {
			if a.rooms[i].Kind == "" {
				a.roomRow(c, t, i)
			}
		}

		if dms := a.dmRooms(); len(dms) > 0 {
			ui.Row(c).Height(16)
			a.sectionLabel(c, t, "私聊", fmt.Sprint(len(dms)))
			for _, i := range dms {
				a.roomRow(c, t, i)
			}
		}

		ui.Row(c).Height(16)
		a.sectionLabel(c, t, "在线设备", fmt.Sprint(len(a.peers)))
		if len(a.peers) == 0 {
			ui.Text(c, "正在寻找局域网里的节点…").FontSize(10.5).TextColor(t.TextMuted).Padding(4, 9)
		}
		for _, p := range a.peers {
			a.peerRow(c, t, p)
		}

		ui.Spacer(c)
		a.connectRow(c, t)
		a.securityLine(c, t)
		a.meRow(c, t)
	})
}

// connectRow is the way in that never moves: the welcome card's goes away
// with the first message, and the inspector can be closed.
func (a *app) connectRow(c *ui.Context, t *ui.Theme) {
	row := ui.Row(c).Gap(6).Padding(4, 9).Radius(8).Cursor(ui.CursorPointer).
		Margin(0, 0, 6, 0)
	if row.Hovered() {
		row.Background(a.pal.hover)
	}
	row.Children(func() {
		ui.Icon(c, icoPlus).FontSize(10.5).TextColor(t.TextMuted)
		ui.Text(c, "手动连接").FontSize(11).TextColor(t.Text.Mix(t.TextMuted, 0.35)).Grow(1)
		if !a.info {
			ui.Text(c, "在右侧输入").FontSize(9).TextColor(t.TextMuted)
		}
	})
	if row.Clicked() {
		a.info = true
	}
}

func (a *app) logo(c *ui.Context, size float32) {
	ui.Box(c).Size(size, size).Radius(size*0.3).
		Gradient(ui.Hex("#3FA9FF"), ui.Hex("#0A5BE0"), 150).
		AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		ui.Icon(c, icoOrbit).Size(size*0.72, size*0.72).TextColor(ui.RGBA(255, 255, 255, 0.96))
	})
}

func (a *app) sectionLabel(c *ui.Context, t *ui.Theme, name, count string) {
	ui.Row(c).Padding(6, 5, 2, 9).Children(func() {
		ui.Text(c, name).FontSize(10).FontWeight(600).TextColor(t.TextMuted).Grow(1).SingleLine()
		if count != "" {
			ui.Text(c, count).FontSize(10).TextColor(t.TextMuted).SingleLine()
		}
	})
}

func (a *app) roomRow(c *ui.Context, t *ui.Theme, i int) {
	r := a.rooms[i]
	active := i == a.active
	row := ui.Row(c).Gap(8).Padding(6, 9).Radius(9).Cursor(ui.CursorPointer).Margin(0, 0, 1, 0)
	switch {
	case active:
		row.Background(a.pal.accentSoft)
	case row.Hovered():
		row.Background(a.pal.hover)
	}
	row.Children(func() {
		glyph := icoHash
		if r.Kind == "dm" {
			glyph = icoUsers
		}
		icon := ui.Icon(c, glyph).FontSize(13)
		label := ui.Text(c, r.Name).FontSize(12.5).Grow(1).SingleLine()
		if active {
			icon.TextColor(t.Accent)
			label.FontWeight(600).TextColor(t.Text)
		} else {
			icon.TextColor(t.TextMuted)
			label.TextColor(t.Text.Mix(t.TextMuted, 0.35))
		}
		if r.Unread > 0 {
			ui.Badge(c, strconv.Itoa(r.Unread))
		}
	})
	if row.Clicked() {
		a.openRoom(i)
	}
}

func (a *app) peerRow(c *ui.Context, t *ui.Theme, p p2p.PeerInfo) {
	open := a.current() != nil && a.current().ID == "dm:"+p.ID
	row := ui.Row(c).Gap(8).Padding(4, 9).Radius(8).
		Cursor(ui.CursorPointer).Tooltip("和 " + p.Name + " 单独聊")
	switch {
	case open:
		row.Background(a.pal.accentSoft)
	case row.Hovered():
		row.Background(a.pal.hover)
	}
	row.Children(func() {
		a.avatar(c, p.Name, p.ID, p.Avatar, 20)
		ui.Column(c).Grow(1).Children(func() {
			ui.Text(c, p.Name).FontSize(11.5).TextColor(t.Text).SingleLine()
			ui.Text(c, a.peerDetail(p)).FontSize(9).TextColor(t.TextMuted).SingleLine()
		})
		ui.Box(c).Size(6, 6).Radius(3).Background(a.pal.green)
	})
	if row.Clicked() {
		a.openDM(p)
	}
}

func (a *app) peerDetail(p p2p.PeerInfo) string {
	addr := p.Addr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		addr = addr[:i]
	}
	if p.RTT > 0 {
		if p.RTT < time.Millisecond {
			return fmt.Sprintf("%s · <1 ms", addr)
		}
		return fmt.Sprintf("%s · %d ms", addr, p.RTT.Milliseconds())
	}
	return addr
}

func (a *app) securityLine(c *ui.Context, t *ui.Theme) {
	v := punchVerdict(a.node.ReachStatus())
	icon := icoGlobe
	if v.tone != 0 {
		icon = icoLock
	}
	ui.Row(c).Gap(6).Padding(4, 9, 6, 9).Children(func() {
		ui.Icon(c, icon).FontSize(10).TextColor(a.verdictColor(t, v.tone))
		ui.Text(c, v.short+" · 端到端加密").FontSize(9).TextColor(t.TextMuted).Grow(1).SingleLine()
	})
}

// reachCard tells how this node is reached from other networks, which is
// what a peer on another address needs to find it.
func (a *app) reachCard(c *ui.Context, t *ui.Theme) {
	r := a.node.ReachStatus()
	a.card(c, t, func() {
		ui.Row(c).Gap(6).Children(func() {
			ui.Icon(c, icoGlobe).FontSize(11).TextColor(a.pal.green)
			a.cardTitle(c, t, "跨网穿透")
		})
		v := punchVerdict(r)
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "能否打洞").FontSize(9.5).TextColor(t.TextMuted).Width(62)
			ui.Box(c).Size(6, 6).Radius(3).Background(a.verdictColor(t, v.tone))
			ui.Text(c, v.short).FontSize(11).FontWeight(600).
				TextColor(a.verdictColor(t, v.tone)).SingleLine()
		})
		a.field(c, t, "你的网络", natName(r.NAT), "")
		public := "未打通"
		if r.Public != "" {
			public = r.Public
		}
		a.field(c, t, "公网地址", public, r.Public)
		mapping := "无"
		if r.Mapping != "" {
			mapping = r.Mapping + " 已放行"
		}
		a.field(c, t, "端口映射", mapping, "")
		a.field(c, t, "会合网络", r.Network, "")
		a.field(c, t, "会合节点", fmt.Sprintf("%d · 发现 %d", r.Nodes, r.Found), "")
		ui.Text(c, v.long).FontSize(9).LineHeight(1.55).TextColor(t.TextMuted)
	})
}

func natName(s string) string {
	switch s {
	case "cone":
		return "锥形 NAT"
	case "symmetric":
		return "对称 NAT"
	case "":
		return "检测中…"
	}
	return "未知"
}

// inviteHint says what to do with the address, which depends on whether
// this side can be reached at all: a router that forwards the port lets the
// other side connect alone, a plain NAT needs both sides to reach out.
func (a *app) inviteHint() string {
	r := a.node.ReachStatus()
	if r.NATErr != "" {
		return "暂时没拿到公网地址（正在重试）。内网地址现在就能给同一个 Wi-Fi 里的机器用；\n如果一直是这个状态，说明这台机器出不去 UDP。"
	}
	if r.Public == "" {
		return "公网地址还在问 STUN，几秒后出现在上面；内网地址现在就能用。"
	}
	if punchVerdict(r).short == "可直连" {
		return "公网地址给别的网络用，内网地址给同一个 Wi-Fi 用。\n你这边的端口已经放行，对方粘上公网地址就能连进来。"
	}
	return "公网地址给别的网络用（打洞要两边同时往外发，各粘一次最稳）；\n内网地址给同一个 Wi-Fi 用，直连最快。"
}

// publicText is the address peers on other networks dial, or what stands
// in its place while there is none: a placeholder must never be copied.
func (a *app) publicText() string {
	r := a.node.ReachStatus()
	switch {
	case r.Public != "":
		return r.Public
	case r.NATErr != "":
		return "暂时没有（重试中）"
	}
	return "获取中…"
}

// addrRow is one address with its name, copying what it shows when the
// row is clicked.
func (a *app) addrRow(c *ui.Context, t *ui.Theme, label, addr, copyText, why string) {
	row := ui.Row(c).Gap(9).Padding(6, 8).Radius(8).AlignItems(ui.Center)
	if copyText != "" {
		row.Cursor(ui.CursorPointer).Tooltip(why + "，点击复制")
	}
	if row.Hovered() && copyText != "" {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Text(c, label).FontSize(9).TextColor(t.TextMuted).Width(24)
		col := t.Text
		if copyText == "" {
			col = t.TextMuted
		}
		ui.Text(c, addr).FontSize(11.5).TextColor(col).Grow(1).SingleLine()
		if copyText != "" {
			ui.Icon(c, icoCopy).FontSize(10).TextColor(t.TextMuted)
		}
	})
	if copyText != "" && row.Clicked() {
		a.copy(copyText, "已复制 "+copyText)
	}
}

// verdict is what the network type means for a peer on another network.
// connectHint says what the field is about to do.
func (a *app) connectHint() string {
	if punchVerdict(a.node.ReachStatus()).short == "可直连" {
		return "回车即连：你这边端口已放行，对方能直接连进来。"
	}
	return "回车即连：打洞要两边同时往外发，让对方也粘一次你的地址，成功率最高。"
}

type verdict struct {
	short string // 侧栏那一行
	long  string // 卡片里的解释
	tone  int    // 0 好，1 中性，2 不行
}

// punchVerdict turns the NAT type into the answer the user is really
// after: can someone on another network reach this node, and if not, why.
func punchVerdict(r p2p.Reach) verdict {
	switch {
	case r.Public != "" && r.Mapping != "":
		return verdict{"可直连", "路由器已放行端口，别的网络可以直接连上你，不必打洞。", 0}
	case r.NAT == "cone":
		return verdict{"可打洞", "锥形 NAT：对方的包打得进来，打洞通常一次成功。", 0}
	case r.NAT == "symmetric":
		return verdict{"打洞受限", "对称 NAT：每次目的地都换一个端口，打洞多半失败，需要中继。", 2}
	case r.NATErr != "":
		return verdict{"打洞不可用", "暂时拿不到公网地址（" + r.NATErr + "）。可能只是这次超时，程序每 25 秒会重试；一直这样就说明 UDP 出不去。", 2}
	case r.Public != "":
		return verdict{"可打洞", "已拿到公网地址，可以接受打洞。", 0}
	case r.Ready:
		return verdict{"检测中", "正在问 STUN 要公网地址，稍等几秒。", 1}
	}
	return verdict{"仅局域网", "没有加入跨网会合，只能发现同一局域网里的节点。", 1}
}

// verdictColor paints a tone: green when the way is open, red when it is
// shut, muted when it is not known yet.
func (a *app) verdictColor(t *ui.Theme, tone int) ui.Color {
	switch tone {
	case 0:
		return a.pal.green
	case 2:
		return t.Danger
	}
	return t.TextMuted
}

func (a *app) meRow(c *ui.Context, t *ui.Theme) {
	row := ui.Row(c).Gap(8).Padding(6, 8).Radius(10).Background(a.pal.hover).Margin(0, 0, 6, 0)
	row.Children(func() {
		main := ui.Row(c).Gap(8).Grow(1).Cursor(ui.CursorPointer).
			Tooltip("编辑昵称与头像")
		main.Children(func() {
			a.avatar(c, a.id.Name, a.id.ID(), a.id.Avatar, 26)
			ui.Column(c).Grow(1).Children(func() {
				ui.Text(c, a.id.Name).FontSize(11.5).FontWeight(550).TextColor(t.Text).SingleLine()
				ui.Text(c, "本机 · "+shortID(a.id.ID())).FontSize(9).TextColor(t.TextMuted).SingleLine()
			})
		})
		if main.Clicked() {
			a.openProfile()
		}
		a.iconButton(c, t, icoCopy, false, a.copyInvite)
	})
}

// fileRow draws a transfer: the name, how far it got, and what can be done
// with it, aligned like any other message.
func (a *app) fileRow(c *ui.Context, t *ui.Theme, m *message, fresh bool, top float32, last bool) {
	if m.Mine {
		ui.Row(c).Justify(ui.End).Padding(top, 0, 0, 0).Children(func() {
			a.fileCard(c, t, m.File)
		})
		return
	}
	ui.Row(c).AlignItems(ui.Start).Gap(8).Padding(top, 0, 0, 0).Children(func() {
		if fresh {
			a.avatar(c, m.Name, m.From, m.Avatar, 26)
		} else {
			ui.Box(c).Size(26, 26)
		}
		ui.Column(c).Children(func() {
			if fresh {
				ui.Text(c, m.Name).FontSize(10.5).FontWeight(600).
					TextColor(hueFor(m.From)).Margin(0, 0, 3, 2).SingleLine()
			}
			a.fileCard(c, t, m.File)
			if last {
				ui.Text(c, clock(m.TS)).FontSize(9).TextColor(t.TextMuted).Margin(3, 0, 0, 3)
			}
		})
	})
}

func (a *app) fileCard(c *ui.Context, t *ui.Theme, f *p2p.FileState) {
	const width = 292
	card := ui.Column(c).Width(width).Padding(10, 12).Radius(14).Gap(9).
		Background(t.Surface).Border(1, a.pal.hairline)
	card.Tooltip("右键：在资源管理器中打开")
	card.ContextMenu(func(m *ui.Menu) {
		if m.Item("在资源管理器中打开").Disabled(f.Path == "").Chosen() {
			if err := revealInExplorer(f.Path); err != nil {
				a.flash(err.Error())
			}
		}
		if m.Item("复制文件路径").Disabled(f.Path == "").Chosen() {
			a.copy(f.Path, "已复制路径")
		}
		if f.Incoming && !f.Active && !f.Finished {
			m.Separator()
			if m.Item("接收").Chosen() {
				if err := a.node.AcceptFile(f.ID, a.downloadDir); err != nil {
					a.flash(err.Error())
				}
			}
			if m.Item("拒绝").Chosen() {
				_ = a.node.RejectFile(f.ID)
			}
		}
	})
	card.Children(func() {

		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(34, 34).Radius(9).AlignItems(ui.Center).Justify(ui.Center).
				Background(a.pal.hover).Children(func() {
				ui.Icon(c, icoFile).FontSize(15).TextColor(t.Accent)
			})
			ui.Column(c).Grow(1).Children(func() {
				title := f.Name
				if f.Files > 1 {
					title = fmt.Sprintf("%s（%d 个文件）", f.Name, f.Files)
				}
				ui.Text(c, title).FontSize(12).FontWeight(600).TextColor(t.Text).SingleLine()
				ui.Text(c, a.fileStatus(f)).FontSize(9.5).TextColor(t.TextMuted).SingleLine()
			})
		})

		done := f.Percent()
		if f.Active || f.Finished {
			ui.Box(c).Height(3).Width(width - 24).Radius(2).
				Background(a.pal.hairline).Children(func() {
				if done > 0 {
					ui.Box(c).Height(3).Radius(2).
						Width((width - 24) * float32(done) / 100).
						Background(t.Accent)
				}
			})
		}

		if f.Incoming && !f.Active && !f.Finished {
			ui.Row(c).Gap(8).Children(func() {
				a.smallButton(c, t, "接收", true, func() {
					if err := a.node.AcceptFile(f.ID, a.downloadDir); err != nil {
						a.flash(err.Error())
					}
				})
				a.smallButton(c, t, "拒绝", false, func() {
					_ = a.node.RejectFile(f.ID)
				})
			})
		}
	})
}

// smallButton is a compact action on a card. It is a real button rather
// than a styled row, so the framework gives it the hit area, the hover and
// the keyboard activation a button is expected to have.
func (a *app) smallButton(c *ui.Context, t *ui.Theme, label string, primary bool, fn func()) {
	// A real button gives the framework the hit area and the keyboard
	// activation; the label is drawn here so it keeps the theme's type.
	btn := ui.ButtonBase(c).Padding(3, 10).Radius(7).Height(22)
	col := t.Text
	if primary {
		btn.Background(t.Accent)
		col = ui.RGBA(255, 255, 255, 0.98)
	} else {
		btn.Background(a.pal.hover)
	}
	btn.Children(func() {
		ui.Text(c, label).FontSize(10).TextColor(col)
	})
	if btn.Clicked() {
		fn()
	}
}

// fileStatus is the line under a file's name.
func (a *app) fileStatus(f *p2p.FileState) string {
	switch {
	case f.Err != "":
		return humanSize(f.Size) + " · " + f.Err
	case f.Finished:
		if f.Incoming {
			return humanSize(f.Size) + " · 已保存"
		}
		return humanSize(f.Size) + " · 已发送"
	case f.Active:
		parts := []string{fmt.Sprintf("%s / %s", humanSize(f.Done), humanSize(f.Size))}
		if f.Rate > 0 {
			parts = append(parts, humanSize(f.Rate)+"/s")
		}
		parts = append(parts, fmt.Sprintf("%d%%", f.Percent()))
		if f.Current != "" && f.Files > 1 {
			parts = append(parts, f.Current)
		}
		return strings.Join(parts, " · ")
	}
	return humanSize(f.Size) + " · 待接收"
}

// humanSize writes a byte count the way a person reads it.
func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n)
	i := -1
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// connectCard keeps the manual way in reach whatever the conversation
// holds: the one in the welcome card goes away as soon as a message
// arrives, and with it every way to reach a peer on another network.
func (a *app) connectCard(c *ui.Context, t *ui.Theme) {
	a.card(c, t, func() {
		a.cardTitle(c, t, "手动连接")
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			in := ui.TextInput(c, &a.connDraft).
				Placeholder("对方的 ip:端口").
				Background(t.Background).Border(1, a.pal.hairline).
				Padding(5, 8).Radius(8).FontSize(11).Grow(1)
			in.OnTextKey(func(k ui.TextKey) bool {
				if k.Key == ui.KeyEnter && k.Mods == 0 {
					k.SetText("")
					a.connectTo(a.connDraft)
					return true
				}
				return false
			})
			if in.Submitted() {
				a.connectTo(a.connDraft)
			}
			a.smallButton(c, t, "连接", true, func() { a.connectTo(a.connDraft) })
		})
		ui.Text(c, a.connectHint()).FontSize(9).LineHeight(1.5).
			TextColor(t.TextMuted)
	})
}

// filesCard says where received files land, and lets it be changed.
func (a *app) filesCard(c *ui.Context, t *ui.Theme) {
	a.card(c, t, func() {
		a.cardTitle(c, t, "文件")
		a.field(c, t, "保存到", a.downloadDir, a.downloadDir)
		ui.Row(c).Gap(8).Children(func() {
			a.smallButton(c, t, "更改目录", true, a.chooseDownloadDir)
			a.smallButton(c, t, "打开目录", false, func() {
				if err := revealInExplorer(a.downloadDir); err != nil {
					a.flash(err.Error())
				}
			})
		})
	})
}

// ----------------------------------------------------------- conversation

func (a *app) conversation(c *ui.Context, t *ui.Theme) {
	r := a.current()
	col := ui.Column(c).Grow(1).FillHeight().Background(t.Background)

	col.Children(func() {
		a.header(c, t, r)
		if len(r.Messages) == 0 {
			if r.Kind == "dm" {
				a.dmWelcome(c, t, r)
			} else {
				a.welcome(c, t)
			}
		} else {
			a.messages(c, t, r)
		}
		a.composer(c, t, r)
	})
}

func (a *app) header(c *ui.Context, t *ui.Theme, r *room) {
	typing := a.typingPeers()
	title := "# " + r.Name
	subtitle := fmt.Sprintf("%d 个节点在线 · 消息签名后中继", len(a.peers))
	if a.reachable() {
		subtitle = fmt.Sprintf("%d 个节点在线 · 公网可达", len(a.peers))
	}
	if r.Kind == "dm" {
		title = r.Name
		subtitle = "私聊 · 只有对方收得到"
		if !a.peerOnline(r.Peer) {
			subtitle = "私聊 · 对方不在线"
		}
	}
	if len(typing) > 0 {
		subtitle = strings.Join(typing, "、") + " 正在输入…"
	}
	if n := a.pendingCount(); n > 0 && len(typing) == 0 {
		subtitle = fmt.Sprintf("%d 条待发送 · 有人连上就补发", n)
	}
	ui.Row(c).Height(46).FillWidth().Padding(0, 14, 0, 20).Gap(10).
		BorderWidth(0, 0, 1, 0).BorderColor(a.pal.hairline).Children(func() {
		ui.Column(c).Grow(1).Children(func() {
			ui.Text(c, title).FontSize(13.5).FontWeight(620).TextColor(t.Text).SingleLine()
			ui.Text(c, subtitle).FontSize(10).TextColor(t.TextMuted).SingleLine()
		})
		a.iconButton(c, t, icoInfo, a.info, func() { a.info = !a.info })
	})
}

func (a *app) messages(c *ui.Context, t *ui.Theme, r *room) {
	a.list.Key = func(i int) any { return r.Messages[i].ID }
	a.list.FollowEnd = true
	a.list.FollowAnimation = &ui.Smooth
	list := ui.List(c, &a.list, len(r.Messages), func(i int) {
		a.messageRow(c, t, r, i)
	}).Grow(1).Padding(14, 20, 8, 20).Gap(3).Justify(ui.End)
	// The rows fade under the header rather than cut off at its edge.
	ink := ui.RGB(0, 0, 0)
	list.MaskLinearGradient(ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
		{Color: ui.Transparent, At: 0},
		{Color: ink, At: 0.035},
		{Color: ink, At: 1},
	}})
}

func (a *app) messageRow(c *ui.Context, t *ui.Theme, r *room, i int) {
	m := r.Messages[i]
	prev := (*message)(nil)
	if i > 0 {
		prev = r.Messages[i-1]
	}
	fresh := prev == nil || prev.From != m.From || m.TS.Sub(prev.TS) > 5*time.Minute
	last := i == len(r.Messages)-1 || r.Messages[i+1].From != m.From || r.Messages[i+1].TS.Sub(m.TS) > 5*time.Minute

	top := float32(0)
	if fresh && i > 0 {
		top = 12
	}

	if m.File != nil {
		a.fileRow(c, t, m, fresh, top, last)
		return
	}

	if m.Mine {
		ui.Row(c).Justify(ui.End).Padding(top, 0, 0, 0).Children(func() {
			ui.Column(c).AlignItems(ui.End).MaxWidthPercent(74).Children(func() {
				ui.Text(c, m.Text).Selectable().FontSize(13.5).LineHeight(1.45).
					TextColor(ui.RGBA(255, 255, 255, 0.97)).
					Padding(8, 12.5).Radius(17).MaxWidth(520).
					Gradient(a.pal.mineA, a.pal.mineB, 160)
				if last {
					stamp := clock(m.TS)
					if m.Pending {
						stamp = "待发送 · " + stamp
					}
					ui.Text(c, stamp).FontSize(9).TextColor(t.TextMuted).Margin(3, 3, 0, 0)
				}
			})
		})
		return
	}

	ui.Row(c).AlignItems(ui.Start).Gap(8).Padding(top, 0, 0, 0).Children(func() {
		if fresh {
			a.avatar(c, m.Name, m.From, m.Avatar, 26)
		} else {
			ui.Box(c).Size(26, 26)
		}
		ui.Column(c).MaxWidthPercent(74).Children(func() {
			if fresh {
				ui.Text(c, m.Name).FontSize(10.5).FontWeight(600).
					TextColor(hueFor(m.From)).Margin(0, 0, 3, 2).SingleLine()
			}
			ui.Text(c, m.Text).Selectable().FontSize(13.5).LineHeight(1.45).
				TextColor(t.Text).Padding(8, 12.5).Radius(17).MaxWidth(520).
				Background(t.Surface).Border(1, a.pal.hairline)
			if last {
				ui.Text(c, clock(m.TS)).FontSize(9).TextColor(t.TextMuted).Margin(3, 0, 0, 3)
			}
		})
	})
}

func (a *app) welcome(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Grow(1).AlignItems(ui.Center).Justify(ui.Center).Gap(12).
		Padding(30, 30, 20, 30).Children(func() {
		ui.Box(c).Size(76, 76).Radius(24).
			Gradient(ui.Hex("#3FA9FF"), ui.Hex("#0A4FD0"), 145).
			Shadow(0, 12, 30, 0, ui.RGBA(10, 100, 255, 0.28)).
			AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Icon(c, icoOrbit).Size(46, 46).TextColor(ui.RGBA(255, 255, 255, 0.97))
		})
		ui.Text(c, "点对点，没有服务器").FontSize(21).FontWeight(620).TextColor(t.Text)
		ui.Text(c, a.inviteHint()).
			FontSize(11.5).LineHeight(1.6).TextColor(t.TextMuted).MaxWidth(440).TextAlign(ui.Center)

		// Both addresses are useful at once: the public one reaches other
		// networks, the local one is the fastest path inside this one. The
		// public one is unknown for the first second or two, and says so
		// rather than swapping the line under the reader.
		card := ui.Column(c).Width(330).Radius(12).Padding(4).
			Margin(6, 0, 0, 0).Background(t.Surface).Border(1, a.pal.hairline)
		card.Children(func() {
			a.addrRow(c, t, "公网", a.publicText(), a.node.ReachStatus().Public,
				"别的网络连你用这个")
			ui.Row(c).Height(1).Margin(3, 10).Background(a.pal.hairline)
			a.addrRow(c, t, "内网", a.node.Addr, a.node.Addr,
				"同一个 Wi-Fi 里用这个，直连最快")
		})

		a.connectField(c, t)

		if s := a.statusText(); s != "" {
			ui.Text(c, s).FontSize(10).TextColor(t.TextMuted)
		}
	})
}

// profileDialog edits the name and the look in one place, over the
// window, so that picking a colour or a glyph shows on the avatar above
// before anything is kept.
func (a *app) profileDialog(c *ui.Context, t *ui.Theme) {
	ui.DialogBase(c, &a.profileOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.45))
		panel.Width(332).Padding(20).Radius(16).Gap(14).
			Background(t.Background).Border(1, a.pal.hairline).
			Shadow(0, 26, 60, 0, ui.RGBA(0, 0, 0, 0.38))
		panel.Children(func() {
			ui.Text(c, "个人资料").FontSize(13.5).FontWeight(620).TextColor(t.Text)

			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				a.avatar(c, a.profName, a.id.ID(), a.profLook, 52)
				in := ui.TextInput(c, &a.profName).Placeholder("昵称").FontSize(12.5).
					Padding(7, 9).Radius(8).Grow(1).
					Background(t.Surface).Border(1, a.pal.hairline)
				if in.Submitted() {
					a.saveProfile()
					a.profileOpen = false
				}
			})

			ui.Column(c).Gap(8).Children(func() {
				ui.Text(c, "头像颜色").FontSize(9.5).TextColor(t.TextMuted)
				idx, _ := lookParts(a.profLook)
				ui.Row(c).Gap(8).Children(func() {
					for i := range avatarPairs {
						pair := avatarPairs[i]
						pick := i == idx
						sw := ui.Box(c).Size(22, 22).Radius(11).
							Gradient(ui.Hex(pair[0]), ui.Hex(pair[1]), 145).
							Cursor(ui.CursorPointer)
						if pick {
							sw.Border(2, t.Text)
						}
						if sw.Clicked() {
							a.setLookColor(i)
						}
					}
				})
			})

			ui.Column(c).Gap(8).Children(func() {
				ui.Text(c, "头像图标").FontSize(9.5).TextColor(t.TextMuted)
				_, cur := lookParts(a.profLook)
				ui.Row(c).Gap(6).Children(func() {
					for _, g := range avatarGlyphs {
						pick := g.key == cur
						chip := ui.Box(c).Size(28, 28).Radius(8).
							AlignItems(ui.Center).Justify(ui.Center).Cursor(ui.CursorPointer).
							Background(t.Surface).Border(1, a.pal.hairline)
						if pick {
							chip.Background(t.SurfaceHover).Border(1, t.Accent)
						}
						icon := g.icon
						chip.Children(func() {
							col := t.TextMuted
							if pick {
								col = t.Accent
							}
							if icon == nil {
								ui.Text(c, "Ab").FontSize(10).TextColor(col)
								return
							}
							ui.Icon(c, icon).FontSize(13).TextColor(col)
						})
						key := g.key
						if chip.Clicked() {
							a.setLookGlyph(key)
						}
					}
				})
			})

			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				cancel := ui.Row(c).Padding(7, 14).Radius(8).Cursor(ui.CursorPointer)
				cancel.Children(func() {
					ui.Text(c, "取消").FontSize(11).TextColor(t.TextMuted)
				})
				if cancel.Clicked() {
					a.profileOpen = false
				}
				save := ui.Row(c).Padding(7, 16).Radius(8).
					Cursor(ui.CursorPointer).Background(t.Accent)
				save.Children(func() {
					ui.Text(c, "保存").FontSize(11).TextColor(ui.RGBA(255, 255, 255, 0.98))
				})
				if save.Clicked() {
					a.saveProfile()
					a.profileOpen = false
				}
			})
		})
	})
}

// setLookColor keeps the glyph and changes the colours.
func (a *app) setLookColor(i int) {
	_, g := lookParts(a.profLook)
	a.profLook = strconv.Itoa(i) + "|" + g
}

// setLookGlyph keeps the colours and changes the glyph.
func (a *app) setLookGlyph(k string) {
	i, _ := lookParts(a.profLook)
	if i < 0 {
		i = 0
	}
	a.profLook = strconv.Itoa(i) + "|" + k
}

// dmWelcome is the empty state of a one-to-one conversation: quieter than
// the lobby's, and without the address a channel hands out.
func (a *app) dmWelcome(c *ui.Context, t *ui.Theme, r *room) {
	online := a.peerOnline(r.Peer)
	ui.Column(c).Grow(1).AlignItems(ui.Center).Justify(ui.Center).Gap(10).
		Padding(30, 30, 20, 30).Children(func() {
		a.avatar(c, r.Name, r.Peer, "", 64)
		ui.Text(c, r.Name).FontSize(19).FontWeight(620).TextColor(t.Text)
		note := "消息只发给对方一个人，不经过其他节点。"
		if !online {
			note = "对方现在不在线，消息发出去也收不到。"
		}
		ui.Text(c, note).FontSize(11.5).LineHeight(1.6).TextColor(t.TextMuted).
			MaxWidth(400).TextAlign(ui.Center)
	})
}

// connectField is the way a peer's address gets into the app, which the
// pill above hands out. Without it the address was only good for a command
// line flag.
func (a *app) connectField(c *ui.Context, t *ui.Theme) {
	if !a.connOpen {
		link := ui.Row(c).Gap(5).Padding(3, 6).Radius(6).
			AlignItems(ui.Center).Cursor(ui.CursorPointer)
		if link.Hovered() {
			link.Background(t.SurfaceHover)
		}
		link.Children(func() {
			ui.Icon(c, icoPlus).FontSize(10).TextColor(t.TextMuted)
			ui.Text(c, "手动连接").FontSize(10.5).TextColor(t.TextMuted)
		})
		if link.Clicked() {
			a.connOpen = true
		}
		return
	}

	box := ui.Row(c).Gap(6).Padding(5, 6).Radius(10).Width(340).
		Background(t.Surface).Border(1, a.pal.hairline).AlignItems(ui.Center)
	box.Children(func() {
		in := ui.TextInput(c, &a.connDraft).
			Placeholder("对方的 ip:port").
			Background(ui.Color{}).Border(0, ui.Color{}).
			Padding(4, 2).FontSize(11).Grow(1).AutoFocus()
		in.OnTextKey(func(k ui.TextKey) bool {
			if k.Key == ui.KeyEnter && k.Mods == 0 {
				k.SetText("")
				a.connectTo(a.connDraft)
				return true
			}
			return false
		})
		if in.Submitted() {
			a.connectTo(a.connDraft)
		}
		btn := ui.Row(c).Padding(3, 9).Radius(6).Cursor(ui.CursorPointer).
			Background(t.Accent)
		btn.Children(func() {
			ui.Text(c, "连接").FontSize(10.5).TextColor(ui.RGBA(255, 255, 255, 0.98))
		})
		if btn.Clicked() {
			a.connectTo(a.connDraft)
		}
		close := ui.Box(c).Size(20, 20).Radius(5).
			AlignItems(ui.Center).Justify(ui.Center).Cursor(ui.CursorPointer)
		close.Children(func() {
			ui.Icon(c, icoClose).FontSize(9).TextColor(t.TextMuted)
		})
		if close.Clicked() {
			a.connOpen = false
			a.connDraft = ""
		}
	})
	ui.Text(c, a.connectHint()).FontSize(9).LineHeight(1.5).
		TextColor(t.TextMuted).MaxWidth(340)
}

func (a *app) composer(c *ui.Context, t *ui.Theme, r *room) {
	col := ui.Column(c).Padding(0, 20, 14, 20).Gap(6)

	ready := strings.TrimSpace(a.draft) != "" || len(a.pending) > 0

	col.Children(func() {
		if len(a.pending) > 0 {
			ui.Row(c).Gap(6).Children(func() {
				for i, path := range a.pending {
					a.pendingChip(c, t, i, path)
				}
			})
		}

		ui.Row(c).AlignItems(ui.End).Gap(8).Children(func() {
			a.iconButton(c, t, icoClip, false, func() {
				if paths, err := PickFiles(); err == nil {
					a.addPending(paths)
				}
			})
			a.iconButton(c, t, icoFolder, false, func() {
				if dir, err := PickFolder("选择要发送的文件夹"); err == nil {
					a.addPending([]string{dir})
				}
			})

			field := ui.Row(c).Grow(1).AlignItems(ui.End).Gap(6).
				Padding(4, 6, 4, 10).Radius(20).
				Background(t.Surface).Border(1, a.pal.hairline)

			// The input itself is the zone: it has a background, so it is
			// hit, and a drop lands where the eye expects it.
			if files := field.DroppedFiles(); len(files) > 0 {
				a.addPending(files)
			}
			dropping := field.FileDragOver()
			if dropping {
				field.Border(2, t.Accent)
			}
			field.Children(func() {
				hint := "发消息到 #" + r.Name
				if r.Kind == "dm" {
					hint = "发消息给 " + r.Name
				}
				if dropping {
					hint = "松手添加附件"
				}
				in := ui.TextInput(c, &a.draft).
					Placeholder(hint).
					Background(ui.Color{}).Border(0, ui.Color{}).
					Padding(5, 2).Grow(1).AutoFocus()
				in.OnTextKey(func(k ui.TextKey) bool {
					if k.Key == ui.KeyEnter && k.Mods == 0 {
						a.submit()
						k.SetText("")
						return true
					}
					return false
				})
				if in.Changed() {
					a.typingNow()
				}

				btn := ui.Box(c).Size(30, 30).Radius(15).
					AlignItems(ui.Center).Justify(ui.Center).Cursor(ui.CursorPointer)
				if ready {
					btn.Gradient(a.pal.mineA, a.pal.mineB, 160)
				} else {
					btn.Background(t.SurfaceHover)
				}
				btn.Children(func() {
					col := ui.RGBA(255, 255, 255, 0.96)
					if !ready {
						col = t.TextMuted
					}
					ui.Icon(c, icoSend).FontSize(14).TextColor(col)
				})
				if btn.Clicked() && ready {
					a.submit()
				}
				// Enter sends, unless an input method composes: that keeps
				// its Enter, and Submitted reports the one that comes after.
				if in.Submitted() && ready {
					a.submit()
				}
			})
		})

		ui.Row(c).Justify(ui.Center).Children(func() {
			ui.Text(c, "Enter 发送 · 也可以把文件拖进这里 · 消息签名后加密中继").
				FontSize(9).TextColor(t.TextMuted)
		})
	})
}

// pendingChip is one file staged in the input, with a way to take it back.
func (a *app) pendingChip(c *ui.Context, t *ui.Theme, i int, path string) {
	chip := ui.Row(c).Gap(6).Padding(4, 8).Radius(8).
		Background(t.Surface).Border(1, a.pal.hairline).AlignItems(ui.Center)
	chip.Tooltip(path)
	chip.Children(func() {
		ui.Icon(c, icoFile).FontSize(11).TextColor(t.Accent)
		ui.Text(c, filepath.Base(path)).FontSize(10.5).TextColor(t.Text).MaxWidth(150).SingleLine()
		x := ui.Box(c).Size(16, 16).Radius(8).
			AlignItems(ui.Center).Justify(ui.Center).Cursor(ui.CursorPointer)
		x.Children(func() {
			ui.Icon(c, icoClose).FontSize(8).TextColor(t.TextMuted)
		})
		if x.Clicked() {
			a.dropPending(i)
		}
	})
}

// -------------------------------------------------------------- inspector

func (a *app) inspector(c *ui.Context, t *ui.Theme) {
	col := ui.Column(c).Width(302).FillHeight().Background(a.pal.panel)
	col.BorderWidth(0, 0, 0, 1).BorderColor(a.pal.hairline)
	col.Children(func() {
		ui.Row(c).Height(44).Padding(0, 10, 0, 18).Gap(8).Children(func() {
			ui.Text(c, "节点信息").FontSize(12.5).FontWeight(600).TextColor(t.Text).Grow(1)
			a.iconButton(c, t, icoClose, false, func() { a.info = false })
		})
		// The cards scroll: with the window short, the last of them would
		// otherwise be cut off by its edge.
		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Padding(4, 14, 14, 14).Gap(10).Children(func() {
				a.identityCard(c, t)
				a.reachCard(c, t)
				a.networkCard(c, t)
				a.connectCard(c, t)
				a.filesCard(c, t)
				a.securityCard(c, t)
				a.peersCard(c, t)
				a.appearanceCard(c, t)
			})
		})
	})
}

func (a *app) card(c *ui.Context, t *ui.Theme, fn func()) {
	box := ui.Column(c).Padding(11, 12).Radius(12).Gap(9).
		Background(t.Surface).Border(1, a.pal.hairline)
	box.Children(fn)
}

func (a *app) cardTitle(c *ui.Context, t *ui.Theme, text string) {
	ui.Text(c, text).FontSize(10).FontWeight(600).TextColor(t.TextMuted)
}

func (a *app) identityCard(c *ui.Context, t *ui.Theme) {
	a.card(c, t, func() {
		a.cardTitle(c, t, "身份")
		ui.Row(c).Gap(10).Children(func() {
			a.avatar(c, a.id.Name, a.id.ID(), a.id.Avatar, 38)
			ui.Column(c).Grow(1).Justify(ui.Center).Children(func() {
				ui.Text(c, "本机节点").FontSize(12).FontWeight(600).TextColor(t.Text)
				ui.Text(c, identity.Fingerprint(a.id.ID())).FontSize(9).TextColor(t.TextMuted).SingleLine()
			})
		})
		if in := ui.TextInput(c, &a.id.Name).Placeholder("你的名字").FontSize(11.5); in.Changed() {
			a.nameTyped()
		}
	})
}

func (a *app) networkCard(c *ui.Context, t *ui.Theme) {
	a.card(c, t, func() {
		a.cardTitle(c, t, "连接")
		a.field(c, t, "邀请地址", a.invite(), a.invite())
		a.field(c, t, "监听端口", strconv.Itoa(a.node.Port), "")
		a.field(c, t, "在线节点", fmt.Sprintf("%d 个", len(a.peers)), "")
		disc := "广播中"
		if a.node.Discovery() == "" {
			disc = "已关闭"
		}
		a.field(c, t, "局域网发现", disc+" · "+a.node.Discovery(), "")
	})
}

func (a *app) securityCard(c *ui.Context, t *ui.Theme) {
	a.card(c, t, func() {
		ui.Row(c).Gap(6).Children(func() {
			ui.Icon(c, icoShield).FontSize(11).TextColor(a.pal.green)
			a.cardTitle(c, t, "安全")
		})
		a.field(c, t, "密钥交换", "X25519 临时密钥", "")
		a.field(c, t, "传输加密", "AES-256-GCM", "")
		a.field(c, t, "消息签名", "Ed25519", "")
		ui.Text(c, "中继节点只能转发明文信封，无法冒充发送者。").
			FontSize(9).LineHeight(1.5).TextColor(t.TextMuted)
	})
}

func (a *app) peersCard(c *ui.Context, t *ui.Theme) {
	a.card(c, t, func() {
		ui.Row(c).Gap(6).Children(func() {
			ui.Icon(c, icoUsers).FontSize(11).TextColor(t.TextMuted)
			a.cardTitle(c, t, fmt.Sprintf("节点 · %d", len(a.peers)))
		})
		if len(a.peers) == 0 {
			ui.Text(c, "还没有连上任何节点。").FontSize(10).TextColor(t.TextMuted)
		}
		for _, p := range a.peers {
			ui.Row(c).Gap(8).Children(func() {
				a.avatar(c, p.Name, p.ID, p.Avatar, 20)
				ui.Column(c).Grow(1).Children(func() {
					ui.Text(c, p.Name).FontSize(10.5).TextColor(t.Text).SingleLine()
					ui.Text(c, p.Addr).FontSize(8.5).TextColor(t.TextMuted).SingleLine()
				})
				if p.RTT > 0 {
					ui.Text(c, fmt.Sprintf("%dms", p.RTT.Milliseconds())).FontSize(9).TextColor(t.TextMuted)
				}
			})
		}
	})
}

func (a *app) appearanceCard(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Gap(6).Children(func() {
		a.modeButton(c, t, "浅色", !a.dark, icoSun)
		a.modeButton(c, t, "深色", a.dark, icoMoon)
	})
}

func (a *app) modeButton(c *ui.Context, t *ui.Theme, label string, on bool, icon *ui.SVG) {
	btn := ui.Row(c).Grow(1).Gap(5).Justify(ui.Center).Padding(6, 8).Radius(8).
		Background(t.Surface).Border(1, a.pal.hairline).Cursor(ui.CursorPointer)
	if on {
		btn.Background(a.pal.accentSoft).Border(1, ui.RGBA(10, 132, 255, 0.35))
	} else if btn.Hovered() {
		btn.Background(t.SurfaceHover)
	}
	btn.Children(func() {
		col := t.TextMuted
		if on {
			col = t.Accent
		}
		ui.Icon(c, icon).FontSize(11).TextColor(col)
		ui.Text(c, label).FontSize(10.5).TextColor(col)
	})
	if btn.Clicked() {
		a.setDark(label == "深色")
	}
}

func (a *app) field(c *ui.Context, t *ui.Theme, label, value, copyText string) {
	row := ui.Row(c).Gap(8).Children(func() {
		ui.Text(c, label).FontSize(9.5).TextColor(t.TextMuted).Width(62)
		ui.Text(c, value).FontSize(10).TextColor(t.Text).Grow(1).SingleLine()
		if copyText != "" {
			ui.Icon(c, icoCopy).FontSize(10).TextColor(t.TextMuted)
		}
	})
	if copyText != "" {
		row.Cursor(ui.CursorPointer)
		if row.Clicked() {
			a.copy(copyText, "已复制 "+copyText)
		}
	}
}

// ------------------------------------------------------------------ parts

func (a *app) iconButton(c *ui.Context, t *ui.Theme, icon *ui.SVG, on bool, fn func()) {
	btn := ui.Box(c).Size(26, 26).Radius(8).AlignItems(ui.Center).Justify(ui.Center).
		Cursor(ui.CursorPointer)
	switch {
	case on:
		btn.Background(a.pal.accentSoft)
	case btn.Hovered():
		btn.Background(a.pal.hover)
	}
	btn.Children(func() {
		col := t.TextMuted
		if on {
			col = t.Accent
		}
		ui.Icon(c, icon).FontSize(13).TextColor(col)
	})
	if btn.Clicked() && fn != nil {
		fn()
	}
}

var avatarPairs = [][2]string{
	{"#5AB0FF", "#0A63E0"},
	{"#4ADE80", "#0E9F6E"},
	{"#C084FC", "#7C3AED"},
	{"#FDBA74", "#F97316"},
	{"#67E8F9", "#0891B2"},
	{"#FDA4AF", "#E11D48"},
}

func hueFor(id string) ui.Color {
	n := 0
	for i := 0; i < len(id); i++ {
		n += int(id[i])
	}
	if n < 0 {
		n = -n
	}
	return ui.Hex(avatarPairs[n%len(avatarPairs)][1])
}

// avatar draws a node's mark: the colours and the glyph it chose, or the
// ones its key gives it and the first letter of its name.
func (a *app) avatar(c *ui.Context, name, id, look string, size float32) {
	pair, glyph := avatarLook(id, look)
	box := ui.Box(c).Size(size, size).Radius(size/2).
		Gradient(ui.Hex(pair[0]), ui.Hex(pair[1]), 145).
		AlignItems(ui.Center).Justify(ui.Center)
	box.Children(func() {
		if glyph != nil {
			ui.Icon(c, glyph).Size(size*0.5, size*0.5).
				TextColor(ui.RGBA(255, 255, 255, 0.97))
			return
		}
		ui.Text(c, initial(name)).FontSize(size * 0.42).FontWeight(600).
			TextColor(ui.RGBA(255, 255, 255, 0.97))
	})
}

// avatarGlyphs lists what an avatar may show in its circle.
var avatarGlyphs = []struct {
	key  string
	icon *ui.SVG
}{
	{"", nil},
	{"orbit", icoOrbit},
	{"hash", icoHash},
	{"moon", icoMoon},
	{"sun", icoSun},
	{"globe", icoGlobe},
	{"shield", icoShield},
	{"users", icoUsers},
}

// avatarLook turns "3|orbit" into colours and a glyph, falling back to the
// key's own colours and the name's first letter.
func avatarLook(id, look string) ([2]string, *ui.SVG) {
	idx, glyph := lookParts(look)
	if idx < 0 || idx >= len(avatarPairs) {
		sum := 0
		for i := 0; i < len(id); i++ {
			sum += int(id[i])
		}
		idx = sum % len(avatarPairs)
	}
	for _, g := range avatarGlyphs {
		if g.key == glyph {
			return avatarPairs[idx], g.icon
		}
	}
	return avatarPairs[idx], nil
}

// lookParts reads a look, giving -1 for the colour when it names none.
func lookParts(look string) (int, string) {
	if i := strings.IndexByte(look, '|'); i >= 0 {
		n, err := strconv.Atoi(look[:i])
		if err != nil {
			return -1, look[i+1:]
		}
		return n, look[i+1:]
	}
	return -1, look
}

// defaultLook is the look a node gets before it chooses one.
func defaultLook(id string) string {
	sum := 0
	for i := 0; i < len(id); i++ {
		sum += int(id[i])
	}
	return strconv.Itoa(sum%len(avatarPairs)) + "|"
}

func initial(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "?"
	}
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return "?"
}

func clock(t time.Time) string { return t.Format("15:04") }

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:4] + "…" + id[len(id)-4:]
}

// statusText is the line under the welcome, or the mesh's last word.
func (a *app) statusText() string {
	if a.status != "" && a.now.Sub(a.statusAt) < 6*time.Second {
		return a.status
	}
	if len(a.peers) == 0 {
		return "等待节点… · " + punchVerdict(a.node.ReachStatus()).short
	}
	return fmt.Sprintf("%d 个节点已连上你", len(a.peers))
}
