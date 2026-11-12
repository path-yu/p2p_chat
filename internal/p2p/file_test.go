package p2p

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pair starts two nodes on the loopback, punched together, and returns
// them once they see each other.
func pair(t *testing.T) (*Node, *Node) {
	t.Helper()
	a := startUDPNode(t, "A", 0, 0)
	b := startUDPNode(t, "B", 0, 0)
	a.Punch("127.0.0.1:" + itoa(b.UDPPort()))

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && (len(a.Peers()) == 0 || len(b.Peers()) == 0) {
		time.Sleep(50 * time.Millisecond)
	}
	if len(a.Peers()) == 0 || len(b.Peers()) == 0 {
		t.Fatal("the two nodes did not connect")
	}
	return a, b
}

// acceptAll makes b take every offer into dir, and reports each offer.
func acceptAll(t *testing.T, b *Node, dir string) chan FileState {
	t.Helper()
	offers := make(chan FileState, 8)
	b.OnEvent = func(ev Event) {
		switch ev.Kind {
		case FileOffer:
			offers <- *ev.File
			if err := b.AcceptFile(ev.File.ID, dir); err != nil {
				t.Logf("accept: %v", err)
			}
		case FileFailed:
			t.Logf("failed: %s", ev.File.Err)
		}
	}
	return offers
}

// waitFor waits until every file of want exists under root with the same
// bytes, and fails after a while.
func waitFor(t *testing.T, root string, want map[string][]byte) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		for rel, data := range want {
			got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil || len(got) != len(data) {
				ok = false
				break
			}
			if sha256.Sum256(got) != sha256.Sum256(data) {
				t.Fatalf("%s arrived with different bytes", rel)
			}
		}
		if ok {
			return
		}
		time.Sleep(80 * time.Millisecond)
	}
	for rel := range want {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s never arrived: %v", rel, err)
		}
	}
	t.Fatal("the files arrived with the wrong bytes")
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFileTransfer: the plain case, one file of a megabyte.
func TestFileTransfer(t *testing.T) {
	a, b := pair(t)

	dir := t.TempDir()
	src := filepath.Join(dir, "big.bin")
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i * 31)
	}
	write(t, src, data)

	inbox := filepath.Join(dir, "in")
	offers := acceptAll(t, b, inbox)

	state, err := a.OfferFile("lobby", src)
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
	if state.Size != int64(len(data)) || state.Files != 1 {
		t.Fatalf("offer says %d bytes in %d files", state.Size, state.Files)
	}
	select {
	case <-offers:
	case <-time.After(10 * time.Second):
		t.Fatal("the offer never arrived")
	}
	waitFor(t, inbox, map[string][]byte{"big.bin": data})
}

// TestSizesThatBendTheChunking: a file of no bytes, of one, of exactly one
// chunk, and of one byte more.
func TestSizesThatBendTheChunking(t *testing.T) {
	sizes := map[string]int{
		"empty.txt":   0,
		"one.txt":     1,
		"chunk.bin":   fileChunk,
		"chunk+1.bin": fileChunk + 1,
	}

	for name, size := range sizes {
		t.Run(name, func(t *testing.T) {
			a, b := pair(t)
			dir := t.TempDir()
			data := make([]byte, size)
			for i := range data {
				data[i] = byte(i%251 + 1)
			}
			src := filepath.Join(dir, name)
			write(t, src, data)

			inbox := filepath.Join(dir, "in")
			offers := acceptAll(t, b, inbox)
			if _, err := a.OfferFile("lobby", src); err != nil {
				t.Fatalf("offer: %v", err)
			}
			select {
			case <-offers:
			case <-time.After(10 * time.Second):
				t.Fatal("the offer never arrived")
			}
			waitFor(t, inbox, map[string][]byte{name: data})
		})
	}
}

// TestFolderTransfer: the shape of a folder arrives with the bytes.
func TestFolderTransfer(t *testing.T) {
	a, b := pair(t)

	src := filepath.Join(t.TempDir(), "相册")
	files := map[string][]byte{
		"readme.txt":     []byte("这一批里的说明"),
		"2024/一.txt":     []byte("第一张"),
		"2024/春/二.bin":   make([]byte, 200<<10),
		"2024/春/深/三.bin": make([]byte, 90<<10),
	}
	for rel, data := range files {
		write(t, filepath.Join(src, filepath.FromSlash(rel)), data)
	}

	inbox := filepath.Join(t.TempDir(), "收")
	offers := acceptAll(t, b, inbox)
	if _, err := a.OfferFile("lobby", src); err != nil {
		t.Fatalf("offer: %v", err)
	}
	select {
	case st := <-offers:
		if st.Files != len(files) {
			t.Fatalf("offer lists %d files, want %d", st.Files, len(files))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the offer never arrived")
	}
	waitFor(t, filepath.Join(inbox, "相册"), files)
}

// TestAwkwardNames: spaces, brackets, emoji and other names a real folder
// is full of must survive the trip.
func TestAwkwardNames(t *testing.T) {
	a, b := pair(t)

	src := filepath.Join(t.TempDir(), "这次 有个 空格")
	files := map[string][]byte{
		"报告 (最终版).pdf":       []byte("pdf"),
		"截图 2024-01-01.png":  []byte("png"),
		"emoji 🦊 .txt":       []byte("fox"),
		"#1 [v2] {final}.md": []byte("md"),
	}
	for rel, data := range files {
		write(t, filepath.Join(src, filepath.FromSlash(rel)), data)
	}

	inbox := filepath.Join(t.TempDir(), "收")
	offers := acceptAll(t, b, inbox)
	if _, err := a.OfferFile("lobby", src); err != nil {
		t.Fatalf("offer: %v", err)
	}
	select {
	case <-offers:
	case <-time.After(10 * time.Second):
		t.Fatal("the offer never arrived")
	}
	waitFor(t, filepath.Join(inbox, "这次 有个 空格"), files)
}

// TestManySmallFiles: a folder of a hundred files, as a photo album or a
// project folder is.
func TestManySmallFiles(t *testing.T) {
	a, b := pair(t)

	src := filepath.Join(t.TempDir(), "many")
	files := map[string][]byte{}
	for i := 0; i < 100; i++ {
		rel := fmt.Sprintf("d%02d/f%03d.txt", i%7, i)
		data := []byte(strings.Repeat("x", i+1))
		files[rel] = data
		write(t, filepath.Join(src, filepath.FromSlash(rel)), data)
	}

	inbox := filepath.Join(t.TempDir(), "收")
	offers := acceptAll(t, b, inbox)
	if _, err := a.OfferFile("lobby", src); err != nil {
		t.Fatalf("offer: %v", err)
	}
	select {
	case st := <-offers:
		if st.Files != 100 {
			t.Fatalf("offer lists %d files", st.Files)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the offer never arrived")
	}
	waitFor(t, filepath.Join(inbox, "many"), files)
}

// TestSeveralFilesAtOnce: a multi-selection keeps its names.
func TestSeveralFilesAtOnce(t *testing.T) {
	a, b := pair(t)

	dir := t.TempDir()
	names := []string{"一.txt", "二.bin", "三.md"}
	files := map[string][]byte{}
	var paths []string
	for i, name := range names {
		data := []byte(strings.Repeat(name, 50+i))
		files[name] = data
		p := filepath.Join(dir, name)
		write(t, p, data)
		paths = append(paths, p)
	}

	inbox := filepath.Join(t.TempDir(), "收")
	offers := acceptAll(t, b, inbox)
	if _, err := a.OfferFile("lobby", paths...); err != nil {
		t.Fatalf("offer: %v", err)
	}
	select {
	case <-offers:
	case <-time.After(10 * time.Second):
		t.Fatal("the offer never arrived")
	}
	waitFor(t, filepath.Join(inbox, "选中的文件"), files)
}

// TestResumeFillsTheHoles: the sidecar says which chunks are there, so a
// resumed transfer asks only for the rest, holes and all.
func TestResumeFillsTheHoles(t *testing.T) {
	dir := t.TempDir()
	n := &Node{}
	const chunks = 40
	size := int64(chunks * fileChunk)
	ft := &fileTransfer{
		peer: &Peer{Name: "对方"},
		dir:  dir,
		state: FileState{
			ID: "1234567890abcdef", Name: "holes.bin", Size: size,
			Files: 1, Incoming: true, Peer: "对方",
		},
		in: []*inFile{{
			entry:  FileEntry{Path: "holes.bin", Size: size, Tag: "aaaa"},
			chunks: chunks,
		}},
	}
	n.addFile(ft)

	bits := newBitmap(chunks)
	for _, i := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29} {
		bitSet(bits, i)
	}
	part := filepath.Join(dir, "holes.bin.part")
	if err := os.WriteFile(part, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveBitmap(part, chunks, "aaaa", bits); err != nil {
		t.Fatal(err)
	}

	if err := n.AcceptFile(ft.state.ID, dir); err != nil {
		t.Fatalf("accept: %v", err)
	}
	st := n.getFile(ft.state.ID).snapshot()
	want := int64(20) * fileChunk
	if st.Done != want {
		t.Fatalf("resumed at %d bytes, want %d", st.Done, want)
	}
	n.StopFile(ft.state.ID)
}

// TestBitmapRefusesAnotherFile: a part file of the same size but another
// content must not be resumed, or the gaps would be filled with the wrong
// bytes.
func TestBitmapRefusesAnotherFile(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "same.bin.part")
	bits := newBitmap(10)
	bitSet(bits, 3)
	if err := saveBitmap(part, 10, "file-one", bits); err != nil {
		t.Fatal(err)
	}
	if loadBitmap(part, 10, "file-two") != nil {
		t.Fatal("a bitmap of another file was accepted")
	}
	if loadBitmap(part, 10, "file-one") == nil {
		t.Fatal("its own bitmap was refused")
	}
	if loadBitmap(part, 11, "file-one") != nil {
		t.Fatal("a bitmap of another size was accepted")
	}
}

// TestBitmapRoundTrip: what was written comes back bit for bit.
func TestBitmapRoundTrip(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "a.part")
	bits := newBitmap(100)
	for _, i := range []int{0, 3, 17, 63, 99} {
		bitSet(bits, i)
	}
	if err := saveBitmap(part, 100, "tag", bits); err != nil {
		t.Fatal(err)
	}
	got := loadBitmap(part, 100, "tag")
	if got == nil {
		t.Fatal("no bitmap came back")
	}
	for i := 0; i < 100; i++ {
		if bitGet(got, i) != bitGet(bits, i) {
			t.Fatalf("bit %d differs", i)
		}
	}
	if bitsSet(got, 100) != 5 {
		t.Fatalf("counted %d bits, want 5", bitsSet(got, 100))
	}
}

// TestSafeNames: a name from another system that Windows would refuse is
// made acceptable instead of failing the transfer.
func TestSafeNames(t *testing.T) {
	cases := map[string]string{
		"a:b.txt":   "a_b.txt",
		`q"q.txt`:   "q_q.txt",
		"star*.txt": "star_.txt",
		"pipe|.txt": "pipe_.txt",
		"dots...":   "dots",
		"trailing ": "trailing",
		"正常 名字.txt": "正常 名字.txt",
	}
	for in, want := range cases {
		if got := safeNamePart(in); got != want {
			t.Errorf("safeNamePart(%q) = %q, want %q", in, got, want)
		}
	}
	if got := safePath("../../etc/passwd"); got != filepath.Join("etc", "passwd") {
		t.Errorf("safePath escaped: %q", got)
	}
	if got := safePath("a/b/../../../c"); strings.Contains(got, "..") {
		t.Errorf("safePath kept a parent: %q", got)
	}
}

// TestDeepFolderReachesTheEnd: a folder nested far enough to pass MAX_PATH
// still arrives.
func TestDeepFolderReachesTheEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("deep paths are slow")
	}
	a, b := pair(t)

	src := filepath.Join(t.TempDir(), "deep")
	rel := strings.Repeat("a-fairly-long-folder-name/", 8) + "file.txt"
	data := []byte("深处的文件")
	write(t, filepath.Join(src, filepath.FromSlash(rel)), data)

	inbox := filepath.Join(t.TempDir(), "收")
	offers := acceptAll(t, b, inbox)
	if _, err := a.OfferFile("lobby", src); err != nil {
		t.Fatalf("offer: %v", err)
	}
	select {
	case <-offers:
	case <-time.After(10 * time.Second):
		t.Fatal("the offer never arrived")
	}
	waitFor(t, filepath.Join(inbox, "deep"), map[string][]byte{rel: data})
}

// TestRejectSendsNothing: a refused offer writes no file.
func TestRejectSendsNothing(t *testing.T) {
	a, b := pair(t)
	dir := t.TempDir()

	src := filepath.Join(dir, "nope.bin")
	write(t, src, make([]byte, 64<<10))

	inbox := filepath.Join(dir, "in")
	rejected := make(chan struct{}, 1)
	b.OnEvent = func(ev Event) {
		if ev.Kind == FileOffer {
			_ = b.RejectFile(ev.File.ID)
			rejected <- struct{}{}
		}
	}
	if _, err := a.OfferFile("lobby", src); err != nil {
		t.Fatalf("offer: %v", err)
	}
	select {
	case <-rejected:
	case <-time.After(10 * time.Second):
		t.Fatal("the offer never arrived")
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(inbox, "nope.bin")); err == nil {
		t.Fatal("a rejected file was written")
	}
}
