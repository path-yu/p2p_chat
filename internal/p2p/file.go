package p2p

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ptp_chat/internal/wire"
)

// A transfer carries one file or a whole folder over the same encrypted,
// ordered stream as the messages. Each file is cut into chunks and each
// chunk is remembered in a bitmap, so a transfer that breaks can resume
// with only the holes: the receiver says which chunks it has, the sender
// skips them.

const (
	fileChunk = 32 << 10
	// fileFrame marks a frame carrying file bytes instead of JSON. A JSON
	// frame starts with '{', so one byte is enough to tell them apart.
	fileFrame = 0x02
	// fileHeaderLen is the transfer id at the head of a data frame.
	fileHeaderLen = 16
	// progressEvery throttles how often the window hears about bytes.
	progressEvery = 80 * time.Millisecond
	// bitmapFlush is how often the record of received chunks hits the disk.
	bitmapFlush = 64
	// maxEntries bounds a folder that can be offered at once.
	maxEntries = 20000
)

// FileEntry is one file of a transfer: where it goes, and how big it is.
type FileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// FileState is what a window shows about one transfer.
type FileState struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Done     int64  `json:"done"`
	Files    int    `json:"files"`
	Current  string `json:"current,omitempty"`
	Incoming bool   `json:"incoming"`
	Active   bool   `json:"active"`
	Finished bool   `json:"finished"`
	Path     string `json:"path,omitempty"`
	Peer     string `json:"peer,omitempty"`
	Err      string `json:"err,omitempty"`
	Rate     int64  `json:"rate,omitempty"`
}

// Percent is how far the transfer got, 0 to 100.
func (f FileState) Percent() int {
	if f.Size <= 0 {
		if f.Finished {
			return 100
		}
		return 0
	}
	p := int(f.Done * 100 / f.Size)
	if p > 100 {
		p = 100
	}
	return p
}

// fileMsg is the JSON side of a transfer.
type fileMsg struct {
	Action string      `json:"action"` // offer, accept, reject, done, stop
	ID     string      `json:"id"`
	Name   string      `json:"name,omitempty"`
	Files  []FileEntry `json:"files,omitempty"`
	// Have is one base64 bitmap per file: the chunks the receiver already
	// holds, so a resumed transfer sends only what is missing.
	Have []string `json:"have,omitempty"`
	// Sha is one hash per file, sent when the sender is done.
	Sha []string `json:"sha,omitempty"`
}

type outFile struct {
	abs    string
	entry  FileEntry
	chunks int
	have   []byte
}

type inFile struct {
	entry  FileEntry
	abs    string
	f      *os.File
	have   []byte
	got    int
	chunks int
	marked int
}

type fileTransfer struct {
	mu    sync.Mutex
	state FileState
	peer  *Peer
	stop  atomic.Bool

	// sender
	out  []*outFile
	base string

	// receiver
	in  []*inFile
	dir string

	lastEmit time.Time
	lastAt   time.Time
	lastDone int64
	rate     float64
}

func (ft *fileTransfer) snapshot() FileState {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.state
}

func (ft *fileTransfer) set(fn func(*FileState)) FileState {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	fn(&ft.state)
	return ft.state
}

// ---------------------------------------------------------------- bitmaps

func chunkCount(size int64) int {
	n := int(size / fileChunk)
	if size%fileChunk != 0 {
		n++
	}
	if n < 1 {
		n = 1
	}
	return n
}

func newBitmap(chunks int) []byte { return make([]byte, (chunks+7)/8) }

func bitGet(b []byte, i int) bool {
	if i < 0 || i/8 >= len(b) {
		return false
	}
	return b[i/8]&(1<<uint(i%8)) != 0
}

func bitSet(b []byte, i int) {
	if i < 0 || i/8 >= len(b) {
		return
	}
	b[i/8] |= 1 << uint(i%8)
}

func bitsSet(b []byte, n int) int {
	total := 0
	for i := 0; i < n; i++ {
		if bitGet(b, i) {
			total++
		}
	}
	return total
}

// sidecarPath is where the bitmap of a part file is written, so that a
// resume knows which chunks are really there rather than trusting a size.
func sidecarPath(part string) string { return part + ".lumen" }

const sidecarMagic = "LMP1"

func saveBitmap(part string, chunks int, bits []byte) error {
	buf := make([]byte, 8+len(bits))
	copy(buf, sidecarMagic)
	binary.BigEndian.PutUint32(buf[4:], uint32(chunks))
	copy(buf[8:], bits)
	return os.WriteFile(sidecarPath(part), buf, 0o644)
}

func loadBitmap(part string, chunks int) []byte {
	data, err := os.ReadFile(sidecarPath(part))
	if err != nil || len(data) < 8 || string(data[:4]) != sidecarMagic {
		return nil
	}
	if int(binary.BigEndian.Uint32(data[4:])) != chunks {
		return nil
	}
	bits := data[8:]
	if len(bits) != (chunks+7)/8 {
		return nil
	}
	return append([]byte(nil), bits...)
}

// ------------------------------------------------------------------ offers

// randomID names a transfer: eight random bytes, written as hex. Raw bytes
// would not survive JSON, which replaces what is not valid UTF-8.
func randomID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// OfferFile asks the newest peer to take one or more paths: a file, a
// folder, or what a multi-selection gave. A folder is walked, so its shape
// arrives with it.
func (n *Node) OfferFile(paths ...string) (FileState, error) {
	if len(paths) == 0 {
		return FileState{}, errors.New("没有选择要发的东西")
	}
	p := n.newestPeer()
	if p == nil {
		return FileState{}, errors.New("还没有连上的节点")
	}

	base, name, entries, out, err := collect(paths)
	if err != nil {
		return FileState{}, err
	}
	if len(entries) == 0 {
		return FileState{}, errors.New("这些路径里没有文件")
	}
	if len(entries) > maxEntries {
		return FileState{}, fmt.Errorf("一次最多 %d 个文件，这次有 %d 个", maxEntries, len(entries))
	}

	var total int64
	for _, e := range entries {
		total += e.Size
	}

	id := randomID()
	ft := &fileTransfer{
		peer: p,
		base: base,
		out:  out,
		state: FileState{
			ID: id, Name: name, Size: total, Files: len(entries),
			Active: true, Peer: p.Name, Path: paths[0],
		},
	}
	n.addFile(ft)
	first := ft.snapshot()
	n.emit(Event{Kind: FileOffer, Peer: p.info(), File: &first})

	n.sendFileMsg(p, fileMsg{Action: "offer", ID: id, Name: name, Files: entries})
	return ft.snapshot(), nil
}

// collect turns the chosen paths into a base directory, a name and the
// entries below it. Relative paths keep a folder's shape on the far side.
func collect(paths []string) (base, name string, entries []FileEntry, out []*outFile, err error) {
	var abs []string
	for _, p := range paths {
		a, err := filepath.Abs(p)
		if err != nil {
			return "", "", nil, nil, err
		}
		abs = append(abs, a)
	}

	if len(abs) == 1 {
		info, err := os.Stat(abs[0])
		if err != nil {
			return "", "", nil, nil, err
		}
		if info.IsDir() {
			base, name = abs[0], filepath.Base(abs[0])
			walkErr := filepath.Walk(base, func(path string, fi os.FileInfo, err error) error {
				if err != nil || fi.IsDir() {
					return nil
				}
				rel, relErr := filepath.Rel(base, path)
				if relErr != nil {
					return nil
				}
				entries = append(entries, FileEntry{
					Path: filepath.ToSlash(rel), Size: fi.Size(),
				})
				out = append(out, &outFile{
					abs: path, entry: entries[len(entries)-1],
					chunks: chunkCount(fi.Size()),
				})
				return nil
			})
			if walkErr != nil {
				return "", "", nil, nil, walkErr
			}
			return base, name, entries, out, nil
		}
		base, name = filepath.Dir(abs[0]), filepath.Base(abs[0])
		entries = append(entries, FileEntry{Path: name, Size: info.Size()})
		out = append(out, &outFile{abs: abs[0], entry: entries[0], chunks: chunkCount(info.Size())})
		return base, name, entries, out, nil
	}

	// Several chosen files: they keep their own names under one batch.
	base = filepath.Dir(abs[0])
	seen := map[string]bool{}
	for _, a := range abs {
		info, err := os.Stat(a)
		if err != nil || info.IsDir() {
			continue
		}
		rel := filepath.Base(a)
		for seen[rel] {
			rel = "_" + rel
		}
		seen[rel] = true
		entries = append(entries, FileEntry{Path: rel, Size: info.Size()})
		out = append(out, &outFile{abs: a, entry: entries[len(entries)-1], chunks: chunkCount(info.Size())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].entry.Path < out[j].entry.Path })
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return base, "选中的文件", entries, out, nil
}

// AcceptFile takes an offer in, under dir, resuming the chunks already
// held for every file.
func (n *Node) AcceptFile(id, dir string) error {
	ft := n.getFile(id)
	if ft == nil {
		return errors.New("这个传输已经不存在了")
	}
	st := ft.snapshot()
	if !st.Incoming {
		return errors.New("这是发出去的传输")
	}
	if dir == "" {
		dir = DefaultDownloadDir()
	}
	ft.dir = dir

	ft.mu.Lock()
	if len(ft.in) == 0 {
		ft.mu.Unlock()
		return errors.New("这个传输没有文件清单")
	}
	ft.mu.Unlock()

	if err := n.prepare(ft); err != nil {
		n.failFile(ft, err)
		return err
	}

	ft.mu.Lock()
	var have []string
	var resumed int64
	for _, f := range ft.in {
		part := f.abs + ".part"
		if bits := loadBitmap(part, f.chunks); bits != nil {
			f.have = bits
		} else {
			f.have = newBitmap(f.chunks)
		}
		f.got = bitsSet(f.have, f.chunks)
		resumed += int64(f.got) * fileChunk
		if f.got == f.chunks {
			// The last chunk is usually short; do not count its padding.
			resumed -= int64(f.chunks*fileChunk) - f.entry.Size
		}
		have = append(have, base64.StdEncoding.EncodeToString(f.have))
	}
	ft.state.Done = resumed
	ft.state.Active = true
	ft.mu.Unlock()

	n.emitProgress(ft, true)
	n.sendFileMsg(ft.peer, fileMsg{Action: "accept", ID: id, Have: have})
	return nil
}

// RejectFile declines an offer.
func (n *Node) RejectFile(id string) error {
	ft := n.getFile(id)
	if ft == nil {
		return errors.New("这个传输已经不存在了")
	}
	n.sendFileMsg(ft.peer, fileMsg{Action: "reject", ID: id})
	ft.set(func(s *FileState) { s.Active = false; s.Finished = true; s.Err = "已拒绝" })
	n.forgetFile(id)
	return nil
}

// StopFile cancels a transfer in either direction.
func (n *Node) StopFile(id string) {
	ft := n.getFile(id)
	if ft == nil {
		return
	}
	ft.stop.Store(true)
	n.sendFileMsg(ft.peer, fileMsg{Action: "stop", ID: id})
	ft.mu.Lock()
	for _, f := range ft.in {
		if f.f != nil {
			f.f.Close()
			f.f = nil
		}
	}
	ft.mu.Unlock()
	ft.set(func(s *FileState) { s.Active = false; s.Finished = true; s.Err = "已取消" })
	n.forgetFile(id)
}

// Files lists the transfers the window should show.
func (n *Node) Files() []FileState {
	n.fmu.Lock()
	defer n.fmu.Unlock()
	out := make([]FileState, 0, len(n.files))
	for _, ft := range n.files {
		out = append(out, ft.snapshot())
	}
	return out
}

// DefaultDownloadDir is where received files land.
func DefaultDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Downloads", "Lumen")
}

func (n *Node) addFile(ft *fileTransfer) {
	n.fmu.Lock()
	if n.files == nil {
		n.files = map[string]*fileTransfer{}
	}
	n.files[ft.state.ID] = ft
	n.fmu.Unlock()
}

func (n *Node) getFile(id string) *fileTransfer {
	n.fmu.Lock()
	defer n.fmu.Unlock()
	return n.files[id]
}

func (n *Node) forgetFile(id string) {
	n.fmu.Lock()
	delete(n.files, id)
	n.fmu.Unlock()
}

func (n *Node) newestPeer() *Peer {
	n.mu.Lock()
	defer n.mu.Unlock()
	var best *Peer
	for _, p := range n.peers {
		if best == nil || p.Since.After(best.Since) {
			best = p
		}
	}
	return best
}

// ------------------------------------------------------------------ receive

func (n *Node) onFileMsg(p *Peer, env *wire.Envelope) {
	var m fileMsg
	if err := json.Unmarshal([]byte(env.Body), &m); err != nil {
		return
	}
	switch m.Action {
	case "offer":
		ft := &fileTransfer{
			peer: p,
			dir:  DefaultDownloadDir(),
			state: FileState{
				ID: m.ID, Name: m.Name, Files: len(m.Files),
				Incoming: true, Peer: env.Name,
			},
		}
		var total int64
		for _, e := range m.Files {
			total += e.Size
			ft.in = append(ft.in, &inFile{entry: e, chunks: chunkCount(e.Size)})
		}
		ft.state.Size = total
		n.addFile(ft)
		first := ft.snapshot()
		n.emit(Event{Kind: FileOffer, Peer: p.info(), Env: *env, File: &first})

	case "accept":
		ft := n.getFile(m.ID)
		if ft == nil || len(ft.out) == 0 {
			return
		}
		go n.streamFiles(ft, m.Have)

	case "reject":
		if ft := n.getFile(m.ID); ft != nil {
			ft.set(func(s *FileState) { s.Active = false; s.Finished = true; s.Err = "对方拒绝接收" })
			n.emitProgress(ft, true)
			n.forgetFile(m.ID)
		}

	case "done":
		n.finishIncoming(m.ID, m.Sha)

	case "stop":
		if ft := n.getFile(m.ID); ft != nil {
			ft.stop.Store(true)
			ft.mu.Lock()
			for _, f := range ft.in {
				if f.f != nil {
					f.f.Close()
					f.f = nil
				}
			}
			ft.mu.Unlock()
			ft.set(func(s *FileState) { s.Active = false; s.Finished = true; s.Err = "对方取消" })
			n.emitProgress(ft, true)
			n.forgetFile(m.ID)
		}
	}
}

// prepare makes the folders and the part files a transfer needs, and
// records the path it will land in.
func (n *Node) prepare(ft *fileTransfer) error {
	st := ft.snapshot()
	root := filepath.Join(ft.dir, safeName(st.Name))
	if st.Files == 1 && !strings.Contains(ft.in[0].entry.Path, "/") {
		// A single file lands as itself, not inside a folder named after it.
		root = filepath.Join(ft.dir, safeName(ft.in[0].entry.Path))
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return err
	}

	ft.mu.Lock()
	defer ft.mu.Unlock()
	for _, f := range ft.in {
		target := root
		if st.Files > 1 || strings.Contains(f.entry.Path, "/") {
			target = filepath.Join(root, safePath(f.entry.Path))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
		}
		f.abs = target
		fh, err := os.OpenFile(target+".part", os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return err
		}
		f.f = fh
	}
	ft.state.Path = root
	return nil
}

// onFileFrame writes one chunk where it belongs: the bitmap decides, so
// chunks may arrive in any order across a resume.
func (n *Node) onFileFrame(p *Peer, plain []byte) {
	if len(plain) < 1+fileHeaderLen+2+4 {
		return
	}
	id := strings.TrimRight(string(plain[1:1+fileHeaderLen]), "\x00")
	idx := int(binary.BigEndian.Uint16(plain[1+fileHeaderLen : 3+fileHeaderLen]))
	seq := int(binary.BigEndian.Uint32(plain[3+fileHeaderLen : 7+fileHeaderLen]))
	data := plain[7+fileHeaderLen:]

	ft := n.getFile(id)
	if ft == nil || ft.stop.Load() {
		return
	}
	ft.mu.Lock()
	if idx < 0 || idx >= len(ft.in) {
		ft.mu.Unlock()
		return
	}
	f := ft.in[idx]
	if f.f == nil || bitGet(f.have, seq) {
		ft.mu.Unlock()
		return
	}
	if _, err := f.f.WriteAt(data, int64(seq)*fileChunk); err != nil {
		ft.mu.Unlock()
		n.failFile(ft, fmt.Errorf("写盘失败: %w", err))
		return
	}
	bitSet(f.have, seq)
	f.got++
	ft.state.Current = f.entry.Path
	ft.state.Done += int64(len(data))
	if f.got-f.marked >= bitmapFlush || f.got == f.chunks {
		f.marked = f.got
		_ = saveBitmap(f.abs+".part", f.chunks, f.have)
	}
	ft.mu.Unlock()
	n.emitProgress(ft, false)
}

// streamFiles sends what the receiver is missing, file by file.
func (n *Node) streamFiles(ft *fileTransfer, have []string) {
	shas := make([]string, len(ft.out))
	for i, f := range ft.out {
		if i < len(have) && have[i] != "" {
			if bits, err := base64.StdEncoding.DecodeString(have[i]); err == nil && len(bits) == (f.chunks+7)/8 {
				f.have = bits
			}
		}
		if f.have == nil {
			f.have = newBitmap(f.chunks)
		}
	}

	for i, f := range ft.out {
		if ft.stop.Load() {
			return
		}
		shas[i] = n.sendOneFile(ft, i, f)
	}
	if ft.stop.Load() {
		return
	}

	n.sendFileMsg(ft.peer, fileMsg{Action: "done", ID: ft.state.ID, Sha: shas})
	ft.set(func(s *FileState) { s.Active = false; s.Finished = true; s.Current = "" })
	n.emitProgress(ft, true)
	n.forgetFile(ft.state.ID)
}

// sendOneFile moves one file, skipping the chunks the far side has.
func (n *Node) sendOneFile(ft *fileTransfer, idx int, f *outFile) string {
	fh, err := os.Open(f.abs)
	if err != nil {
		n.failFile(ft, err)
		return ""
	}
	defer fh.Close()

	h := sha256.New()
	buf := make([]byte, fileChunk)
	skipped := false
	for c := 0; c < f.chunks; c++ {
		if ft.stop.Load() {
			return ""
		}
		nr, readErr := fh.ReadAt(buf, int64(c)*fileChunk)
		if nr > 0 {
			h.Write(buf[:nr])
			if bitGet(f.have, c) {
				skipped = true
			} else {
				if err := ft.peer.sendFileFrame(ft.state.ID, idx, c, buf[:nr]); err != nil {
					n.failFile(ft, err)
					return ""
				}
				ft.mu.Lock()
				ft.state.Current = f.entry.Path
				ft.state.Done += int64(nr)
				ft.mu.Unlock()
				n.emitProgress(ft, false)
			}
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			n.failFile(ft, readErr)
			return ""
		}
	}
	if skipped {
		// The chunks the far side already had were read into the hash here
		// too, so it still covers the whole file.
		return hex.EncodeToString(h.Sum(nil))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// finishIncoming checks every file it wrote and puts them in place.
func (n *Node) finishIncoming(id string, shas []string) {
	ft := n.getFile(id)
	if ft == nil {
		return
	}
	ft.mu.Lock()
	files := append([]*inFile(nil), ft.in...)
	ft.mu.Unlock()

	for i, f := range files {
		ft.mu.Lock()
		if f.f != nil {
			_ = f.f.Sync()
			f.f.Close()
			f.f = nil
		}
		ft.mu.Unlock()

		part := f.abs + ".part"
		if f.got != f.chunks {
			n.failFile(ft, fmt.Errorf("%s 还差 %d 块", f.entry.Path, f.chunks-f.got))
			return
		}
		if i < len(shas) && shas[i] != "" {
			sum, err := hashFile(part)
			if err != nil {
				n.failFile(ft, err)
				return
			}
			if sum != shas[i] {
				n.failFile(ft, fmt.Errorf("%s 校验不一致", f.entry.Path))
				return
			}
		}
		if err := os.Remove(sidecarPath(part)); err != nil && !os.IsNotExist(err) {
			n.logf("清理位图失败: %v", err)
		}
		if err := os.Rename(part, f.abs); err != nil {
			n.failFile(ft, err)
			return
		}
	}

	ft.set(func(s *FileState) {
		s.Active = false
		s.Finished = true
		s.Done = s.Size
		s.Current = ""
	})
	n.emitProgress(ft, true)
	n.forgetFile(id)
}

func (n *Node) failFile(ft *fileTransfer, err error) {
	msg := err.Error()
	ft.mu.Lock()
	for _, f := range ft.in {
		if f.f != nil {
			_ = f.f.Sync()
			f.f.Close()
			f.f = nil
		}
	}
	ft.state.Active = false
	ft.state.Finished = true
	ft.state.Err = msg
	ft.mu.Unlock()
	last := ft.snapshot()
	n.emit(Event{Kind: FileFailed, Peer: ft.peer.info(), File: &last})
	n.forgetFile(ft.state.ID)
}

// ---------------------------------------------------------------- emitting

func (n *Node) emitProgress(ft *fileTransfer, force bool) {
	now := time.Now()
	ft.mu.Lock()
	if !force && now.Sub(ft.lastEmit) < progressEvery {
		ft.mu.Unlock()
		return
	}
	if dt := now.Sub(ft.lastAt).Seconds(); !ft.lastAt.IsZero() && dt > 0 {
		inst := float64(ft.state.Done-ft.lastDone) / dt
		if ft.rate == 0 {
			ft.rate = inst
		} else {
			ft.rate = 0.7*ft.rate + 0.3*inst
		}
		ft.state.Rate = int64(ft.rate)
	}
	ft.lastAt, ft.lastDone = now, ft.state.Done
	ft.lastEmit = now

	st := ft.state
	peer := ft.peer
	ft.mu.Unlock()

	kind := FileProgress
	switch {
	case st.Finished && st.Err != "":
		kind = FileFailed
	case st.Finished:
		kind = FileDone
	}
	done := st
	n.emit(Event{Kind: kind, Peer: peer.info(), File: &done})
}

func (n *Node) sendFileMsg(p *Peer, m fileMsg) {
	body, err := json.Marshal(m)
	if err != nil {
		return
	}
	env := &wire.Envelope{Type: wire.File, Body: string(body)}
	if err := p.Send(env); err != nil && n.Logf != nil {
		n.Logf("文件信令发送失败: %v", err)
	}
}

// hashFile is the whole-file check that catches a broken transfer.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fileHeader is the transfer id at the head of a data frame.
func fileHeader(id string) []byte {
	b := make([]byte, fileHeaderLen)
	copy(b, id)
	return b
}

// safeName keeps a folder or file name from escaping the folder it is
// written into.
func safeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." {
		return "文件"
	}
	return name
}

// safePath does the same for the inside of a folder, keeping the shape.
func safePath(p string) string {
	parts := strings.Split(filepath.ToSlash(p), "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".", "..":
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return "文件"
	}
	return filepath.Join(out...)
}
