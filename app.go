package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"ptp_chat/internal/identity"
	"ptp_chat/internal/p2p"
	"ptp_chat/internal/store"
	"ptp_chat/internal/wire"
)

// message is one line of a room's conversation.
type message struct {
	ID     string
	From   string
	Name   string
	Avatar string
	// File is set when the message is a transfer rather than a line of text.
	File *p2p.FileState
	Text string
	TS   time.Time
	Mine bool
	// Pending marks a message written while nobody was connected: it is
	// kept and sent as soon as a peer shows up.
	Pending bool
}

// outMsg is one message waiting for the first peer to appear.
type outMsg struct {
	env   *wire.Envelope
	msgID string
}

// room is a channel of the mesh. Rooms are tags: a message carries the
// room it belongs to, and every node keeps the ones it receives.
type room struct {
	ID   string
	Name string
	Icon string
	// Kind is "dm" for a conversation with one peer, empty for a channel
	// that the whole mesh hears.
	Kind     string
	Peer     string
	Messages []*message
	Unread   int
}

// app is the window's state; the view reads it, and the node's events
// change it on the main thread.
type app struct {
	id   *identity.Identity
	node *p2p.Node
	st   *store.Store
	win  *mygo.Window
	dir  string

	rooms  []*room
	active int
	peers  []p2p.PeerInfo
	typing map[string]time.Time

	draft string
	// connOpen shows the field that takes a peer's address by hand, and
	// connDraft is what is typed in it.
	connOpen  bool
	connDraft string

	// profileOpen shows the dialog that edits the name and the look;
	// profName and profLook are what it is editing, savedName what the
	// identity file already holds.
	profileOpen bool
	profName    string
	profLook    string
	savedName   string
	// downloadDir is where received files land.
	downloadDir string
	// pending are the files staged in the input, sent with the next Enter.
	pending []string
	// outbox holds what was written with no peer to hear it, in order.
	outbox      []outMsg
	nameDirtyAt time.Time
	list        ui.ListState
	info        bool
	dark        bool
	ready       bool
	frames      int

	// forceDark pins the appearance when the command line says which.
	forceDark *bool

	now      time.Time
	status   string
	statusAt time.Time
	pal      palette

	// chromeDark is the appearance the window buttons were last drawn for.
	chromeDark int8

	lastTyping map[string]time.Time
}

// setDark turns the day and the night: the view, and the system's window
// buttons over it. A choice made here wins over the command line's.
func (a *app) setDark(dark bool) {
	a.dark = dark
	v := dark
	a.forceDark = &v
	if a.st != nil {
		room := ""
		if r := a.current(); r != nil {
			room = r.ID
		}
		_ = a.st.SaveSettings(store.Settings{Name: a.id.Name, Room: room, Dark: &v})
	}
}

// syncChrome draws the system's window buttons light on a dark title bar
// and dark on a light one, following the appearance.
func (a *app) syncChrome() {
	want := int8(2)
	if a.dark {
		want = 1
	}
	if a.chromeDark == want || a.win == nil {
		return
	}
	a.chromeDark = want
	if a.dark {
		a.win.SetWindowButtonsTheme(mygo.ThemeDark)
	} else {
		a.win.SetWindowButtonsTheme(mygo.ThemeLight)
	}
}

func newApp(id *identity.Identity, st *store.Store, node *p2p.Node, dir string) *app {
	a := &app{
		id:         id,
		node:       node,
		st:         st,
		dir:        dir,
		typing:     map[string]time.Time{},
		lastTyping: map[string]time.Time{},
		rooms: []*room{
			{ID: "lobby", Name: "大厅", Icon: "hash"},
			{ID: "work", Name: "工作", Icon: "hash"},
			{ID: "night", Name: "深夜", Icon: "moon"},
		},
	}
	a.savedName = id.Name
	a.downloadDir = p2p.DefaultDownloadDir()

	// History fills the rooms it holds; a room with none stays empty.
	for _, m := range st.History(400) {
		r := a.room(m.Room)
		if r == nil {
			// A one-to-one room is not in the defaults; its history brings
			// it back, so a private conversation survives a restart.
			if !strings.HasPrefix(m.Room, "dm:") {
				continue
			}
			name := m.Name
			if name == "" {
				name = "对端"
			}
			r = &room{ID: m.Room, Name: name, Kind: "dm", Peer: strings.TrimPrefix(m.Room, "dm:")}
			a.rooms = append(a.rooms, r)
		}
		r.Messages = append(r.Messages, &message{
			ID: m.ID, From: m.From, Name: m.Name, Text: m.Text,
			TS: time.UnixMilli(m.TS), Mine: m.Mine,
		})
	}
	if s := st.Settings(); s.Room != "" {
		for i, r := range a.rooms {
			if r.ID == s.Room {
				a.active = i
			}
		}
	}
	if s := st.Settings(); s.DownloadDir != "" {
		a.downloadDir = s.DownloadDir
	}
	if s := st.Settings(); s.Dark != nil {
		a.dark = *s.Dark
		a.forceDark = s.Dark
	}
	return a
}

func (a *app) room(id string) *room {
	for _, r := range a.rooms {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (a *app) current() *room { return a.rooms[a.active] }

// setWindow hands the window over once it exists, so that the node's
// goroutines can reach it.
func (a *app) setWindow(w *mygo.Window) { a.win = w }

// onEvent receives what the mesh says, off the main thread.
func (a *app) onEvent(ev p2p.Event) {
	w := a.win
	if w == nil {
		return
	}
	switch ev.Kind {
	case p2p.PeerJoined:
		w.Update(func() {
			a.flash(ev.Peer.Name + " 加入了网状网")
			a.flushOutbox()
		})
	case p2p.PeerLeft:
		w.Update(func() { a.flash(ev.Peer.Name + " 离开了网状网") })
	case p2p.Message:
		w.Update(func() { a.receive(ev.Env) })
	case p2p.FileOffer, p2p.FileProgress, p2p.FileDone, p2p.FileFailed:
		w.Update(func() { a.applyFile(ev) })
	case p2p.TypingNow:
		w.Update(func() {
			a.typing[ev.Env.From] = a.now
			if a.win != nil {
				time.AfterFunc(3500*time.Millisecond, a.win.Invalidate)
			}
		})
	}
}

func (a *app) flash(s string) {
	a.status, a.statusAt = s, time.Now()
}

// receive adds a message that came over the mesh.
func (a *app) receive(env wire.Envelope) {
	r := a.room(env.Room)
	if r == nil {
		// A direct message from someone new makes its own room, named after
		// them, rather than landing in the lobby.
		if env.To != "" && strings.HasPrefix(env.Room, "dm:") {
			name := env.Name
			if name == "" {
				name = "对端"
			}
			r = &room{ID: env.Room, Name: name, Kind: "dm", Peer: env.From}
			a.rooms = append(a.rooms, r)
		} else {
			r = a.room("lobby")
			if r == nil {
				return
			}
		}
	}
	for _, m := range r.Messages {
		if m.ID == env.ID {
			return
		}
	}
	ts := time.UnixMilli(env.TS)
	if env.TS == 0 {
		ts = time.Now()
	}
	m := &message{ID: env.ID, From: env.From, Name: env.Name, Avatar: env.Avatar, Text: env.Body, TS: ts}
	r.Messages = append(r.Messages, m)
	delete(a.typing, env.From)

	active := a.current()
	if active == nil || active.ID != r.ID {
		r.Unread++
	}
	_ = a.st.Append(store.Msg{
		ID: m.ID, Room: r.ID, From: m.From, Name: m.Name, Text: m.Text,
		TS: ts.UnixMilli(), Mine: false,
	})
}

// send broadcasts what was typed to the mesh.
func (a *app) send(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	r := a.current()
	if r == nil {
		return
	}
	// The id is made here, not by Broadcast, so the outbox and the bubble
	// can both refer to the same message.
	env := &wire.Envelope{Type: wire.Chat, Room: r.ID, Body: text, ID: wire.NewID()}
	if r.Kind == "dm" {
		env.To = r.Peer
	}
	alone := len(a.node.Peers()) == 0
	if alone {
		a.outbox = append(a.outbox, outMsg{env: env, msgID: env.ID})
		if len(a.outbox) > 50 {
			a.outbox = a.outbox[1:]
		}
		a.node.Logf("没有节点在线，%d 条消息先存着，有人连上就补发", len(a.outbox))
	}
	a.node.Broadcast(env)
	m := &message{
		ID: env.ID, From: a.id.ID(), Name: a.id.Name, Text: text,
		TS: time.Now(), Mine: true, Pending: alone,
	}
	r.Messages = append(r.Messages, m)
	_ = a.st.Append(store.Msg{
		ID: m.ID, Room: r.ID, From: m.From, Name: m.Name, Text: m.Text,
		TS: m.TS.UnixMilli(), Mine: true,
	})
	a.draft = ""
	a.list.ScrollToEnd()
}

// typing tells the mesh that something is being written, at most once a
// second and a bit.
func (a *app) typingNow() {
	r := a.current()
	if r == nil {
		return
	}
	key := "typing:" + r.ID
	if last, ok := a.lastTyping[key]; ok && a.now.Sub(last) < 1200*time.Millisecond {
		return
	}
	a.lastTyping[key] = a.now
	a.node.Broadcast(&wire.Envelope{Type: wire.Typing, Room: r.ID})
}

// openRoom switches rooms.
func (a *app) openRoom(i int) {
	if i < 0 || i >= len(a.rooms) || i == a.active {
		return
	}
	a.active = i
	a.rooms[i].Unread = 0
	a.list.ScrollToEnd()
	dark := a.dark
	_ = a.st.SaveSettings(store.Settings{Name: a.id.Name, Room: a.rooms[i].ID, Dark: &dark})
}

// typingPeers lists, by name, the peers writing in the room shown.
func (a *app) typingPeers() []string {
	var out []string
	for id, at := range a.typing {
		if a.now.Sub(at) < 3500*time.Millisecond {
			out = append(out, a.peerName(id))
		}
	}
	sort.Strings(out)
	return out
}

func (a *app) peerName(id string) string {
	for _, p := range a.peers {
		if p.ID == id {
			return p.Name
		}
	}
	return "有人"
}

// online reports whether a peer is connected.
func (a *app) online(id string) bool {
	for _, p := range a.peers {
		if p.ID == id {
			return true
		}
	}
	return false
}

// invite is the address to hand a friend: the public one when the router
// let peers in, the one of the local network otherwise.
func (a *app) invite() string {
	if r := a.node.ReachStatus(); r.Public != "" {
		return r.Public
	}
	return a.node.Addr
}

// copy puts text on the clipboard and says so in the window.
func (a *app) copy(text, note string) {
	mygo.Clipboard.WriteText(text)
	a.flash(note)
}

// reachable reports whether peers on other networks can dial this node.
func (a *app) reachable() bool { return a.node.ReachStatus().Public != "" }

// connectTo takes a peer's address by hand. It may be the port the peer
// listens on for TCP, or the one peers punch towards over UDP, so both are
// tried and whichever answers first wins.
func (a *app) connectTo(addr string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		a.connOpen = false
		return
	}
	a.node.Connect(addr)
	a.node.Punch(addr)
	a.flash("正在连接 " + addr)
	a.connDraft = ""
	a.connOpen = false
}

// openProfile starts editing the name and the look, in a dialog.
func (a *app) openProfile() {
	a.profName = a.id.Name
	a.profLook = a.id.Avatar
	if a.profLook == "" {
		a.profLook = defaultLook(a.id.ID())
	}
	a.profileOpen = true
}

// saveProfile keeps what the dialog holds: in memory, in the identity
// file (which the next launch reads), and on the wire.
func (a *app) saveProfile() {
	name := strings.TrimSpace(a.profName)
	if name == "" {
		name = a.savedName
	}
	a.id.Name = name
	a.id.Avatar = a.profLook
	a.savedName = name
	_ = a.id.Save(a.dir)
	room := ""
	if r := a.current(); r != nil {
		room = r.ID
	}
	dark := a.dark
	_ = a.st.SaveSettings(store.Settings{Name: name, Room: room, Dark: &dark})
	a.node.RefreshName()
	a.flash("资料已更新")
	a.nameDirtyAt = time.Time{}
}

// saveName writes a new name to the identity file and to the settings, and
// tells the peers, so that a rename shows up on the other side too.
func (a *app) saveName() {
	name := strings.TrimSpace(a.id.Name)
	if name == "" {
		a.id.Name = a.savedName
		return
	}
	if name == a.savedName {
		return
	}
	a.id.Name = name
	a.savedName = name
	_ = a.id.Save(a.dir)
	room := ""
	if r := a.current(); r != nil {
		room = r.ID
	}
	dark := a.dark
	_ = a.st.SaveSettings(store.Settings{Name: name, Room: room, Dark: &dark})
	a.node.RefreshName()
	a.flash("昵称改为 " + name)
	a.nameDirtyAt = time.Time{}
}

// nameTyped marks the name as waiting to be written, so that typing it
// does not write the identity file on every keystroke.
func (a *app) nameTyped() { a.nameDirtyAt = a.now }

// copyInvite puts this node's address on the clipboard.
func (a *app) copyInvite() {
	addr := a.invite()
	a.copy(addr, "已复制邀请地址 "+addr)
}

// applyFile keeps one message per transfer, updated as the bytes move.
func (a *app) applyFile(ev p2p.Event) {
	if ev.File == nil {
		return
	}
	st := *ev.File
	for _, r := range a.rooms {
		for _, m := range r.Messages {
			if m.File != nil && m.File.ID == st.ID {
				m.File = &st
				return
			}
		}
	}
	r := a.room(st.Room)
	if r == nil {
		r = a.current()
	}
	if r == nil {
		return
	}
	m := &message{
		ID:   "file-" + st.ID,
		From: ev.Peer.ID,
		Name: ev.Peer.Name,
		Text: st.Name,
		TS:   time.Now(),
		Mine: !st.Incoming,
		File: &st,
	}
	if m.Name == "" {
		m.Name = "对端"
	}
	r.Messages = append(r.Messages, m)
	a.list.ScrollToEnd()
}

// sendFile offers one or more files to the peer this window talks to.
func (a *app) sendFile() {
	paths, err := PickFiles()
	if err != nil {
		return
	}
	a.offer(paths)
}

// sendFolder offers a whole folder, shape and all.
func (a *app) sendFolder() {
	dir, err := PickFolder("选择要发送的文件夹")
	if err != nil {
		return
	}
	a.offer([]string{dir})
}

func (a *app) offer(paths []string) {
	room := ""
	if r := a.current(); r != nil {
		room = r.ID
	}
	st, err := a.node.OfferFile(room, paths...)
	if err != nil {
		a.flash(err.Error())
		return
	}
	if st.Files > 1 {
		a.flash(fmt.Sprintf("请求发送 %s（%d 个文件）", st.Name, st.Files))
		return
	}
	a.flash("请求发送 " + st.Name)
}

// chooseDownloadDir asks for the folder received files should go to.
func (a *app) chooseDownloadDir() {
	dir, err := PickFolder("选择接收文件的保存位置")
	if err != nil {
		return
	}
	a.setDownloadDir(dir)
	a.flash("收到文件保存到 " + dir)
}

// setDownloadDir remembers where received files go.
func (a *app) setDownloadDir(dir string) {
	if dir == "" {
		return
	}
	a.downloadDir = dir
	dark := a.dark
	room := ""
	if r := a.current(); r != nil {
		room = r.ID
	}
	_ = a.st.SaveSettings(store.Settings{
		Name: a.id.Name, Room: room, Dark: &dark, DownloadDir: dir,
	})
}

// revealInExplorer opens the folder holding path with the file selected.
func revealInExplorer(path string) error {
	if path == "" {
		return errors.New("还没有文件路径")
	}
	if _, err := os.Stat(path); err != nil {
		return errors.New("文件已经不在了")
	}
	// The folder must come up in front of this window, not behind it.
	allowForeground()
	err := exec.Command("explorer", "/select,"+path).Start()
	bringFolderToFront(filepath.Base(path))
	return err
}

// openDM opens the conversation with one peer, making the room the first
// time it is asked for.
func (a *app) openDM(p p2p.PeerInfo) {
	id := "dm:" + p.ID
	for i, r := range a.rooms {
		if r.ID == id {
			r.Name = p.Name
			a.openRoom(i)
			return
		}
	}
	a.rooms = append(a.rooms, &room{
		ID: id, Name: p.Name, Kind: "dm", Peer: p.ID,
	})
	a.openRoom(len(a.rooms) - 1)
}

// dmRooms lists the one-to-one conversations, for the sidebar.
func (a *app) dmRooms() []int {
	var out []int
	for i, r := range a.rooms {
		if r.Kind == "dm" {
			out = append(out, i)
		}
	}
	return out
}

// peerOnline reports whether the peer a direct conversation is for is
// still connected.
func (a *app) peerOnline(id string) bool {
	for _, p := range a.peers {
		if p.ID == id {
			return true
		}
	}
	return false
}

// addPending stages dropped or picked files for the next send.
func (a *app) addPending(paths []string) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		dup := false
		for _, q := range a.pending {
			if q == p {
				dup = true
				break
			}
		}
		if !dup {
			a.pending = append(a.pending, p)
		}
	}
	if n := len(a.pending); n > 0 {
		a.flash(fmt.Sprintf("已附上 %d 个，回车发送", n))
	}
}

// dropPending takes one attachment back out of the input.
func (a *app) dropPending(i int) {
	if i < 0 || i >= len(a.pending) {
		return
	}
	a.pending = append(a.pending[:i], a.pending[i+1:]...)
}

// submit sends what the input holds: the text, the files, or both.
func (a *app) submit() {
	text := strings.TrimSpace(a.draft)
	if len(a.pending) == 0 {
		if text != "" {
			a.send(text)
		}
		return
	}
	if text != "" {
		a.send(text)
	} else {
		a.draft = ""
	}
	paths := a.pending
	a.pending = nil
	room := ""
	if cur := a.current(); cur != nil {
		room = cur.ID
	}
	if _, err := a.node.OfferFile(room, paths...); err != nil {
		// Nothing was sent: keep them staged so the user can retry.
		a.pending = paths
		a.flash(err.Error())
		return
	}
}

// flushOutbox sends what was written while nobody was listening. The ids
// do not change, so a peer that already had them drops the second copy.
func (a *app) flushOutbox() {
	if len(a.outbox) == 0 || len(a.node.Peers()) == 0 {
		return
	}
	queued := a.outbox
	a.outbox = nil
	for _, q := range queued {
		a.node.Broadcast(q.env)
		for _, r := range a.rooms {
			for _, m := range r.Messages {
				if m.ID == q.msgID {
					m.Pending = false
				}
			}
		}
	}
	if n := len(queued); n > 0 {
		a.flash(fmt.Sprintf("补发了 %d 条离线消息", n))
	}
}

// pendingCount is how many messages are still waiting for a peer.
func (a *app) pendingCount() int {
	return len(a.outbox)
}
