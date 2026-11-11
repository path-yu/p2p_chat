package p2p

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFileTransfer sends a file over a punched path: offer, accept,
// chunks, and the hash check at the far end.
func TestFileTransfer(t *testing.T) {
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

	dir := t.TempDir()
	src := filepath.Join(dir, "big.bin")
	want := make([]byte, 1<<20)
	if _, err := rand.Read(want); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatal(err)
	}

	inbox := filepath.Join(dir, "in")
	offered := make(chan struct{}, 1)
	b.OnEvent = func(ev Event) {
		switch ev.Kind {
		case FileOffer:
			if err := b.AcceptFile(ev.File.ID, inbox); err != nil {
				t.Logf("accept: %v", err)
			}
			offered <- struct{}{}
		case FileFailed:
			t.Logf("b failed: %s", ev.File.Err)
		case FileDone:
			t.Logf("b done: %s (%d bytes)", ev.File.Path, ev.File.Done)
		}
	}

	state, err := a.OfferFile(src)
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
	if state.Size != int64(len(want)) {
		t.Fatalf("offer says %d bytes, want %d", state.Size, len(want))
	}

	select {
	case <-offered:
	case <-time.After(10 * time.Second):
		t.Fatal("the offer never arrived")
	}

	// The transfer is done when the file is in place with the right bytes.
	got := filepath.Join(inbox, "big.bin")
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(got); err == nil && fi.Size() == int64(len(want)) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("the file never arrived: %v", err)
	}
	if len(data) != len(want) {
		t.Fatalf("got %d bytes, want %d", len(data), len(want))
	}
	for i := range want {
		if data[i] != want[i] {
			t.Fatalf("byte %d differs", i)
		}
	}
	if _, err := os.Stat(got + ".part"); err == nil {
		t.Fatal("the part file was left behind")
	}
}

// TestFolderTransfer sends a folder: the shape arrives as well as the bytes.
func TestFolderTransfer(t *testing.T) {
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

	src := filepath.Join(t.TempDir(), "相册")
	if err := os.MkdirAll(filepath.Join(src, "2024", "春"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"readme.txt":     []byte("这一批里的说明"),
		"2024/一.txt":     []byte("第一张"),
		"2024/春/二.bin":   make([]byte, 200<<10),
		"2024/春/深/三.bin": make([]byte, 90<<10),
	}
	for rel, data := range files {
		path := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	inbox := filepath.Join(t.TempDir(), "收")
	offered := make(chan struct{}, 1)
	b.OnEvent = func(ev Event) {
		switch ev.Kind {
		case FileOffer:
			if ev.File.Files != len(files) {
				t.Logf("offer lists %d files, want %d", ev.File.Files, len(files))
			}
			if err := b.AcceptFile(ev.File.ID, inbox); err != nil {
				t.Logf("accept: %v", err)
			}
			offered <- struct{}{}
		case FileFailed:
			t.Logf("b failed: %s", ev.File.Err)
		}
	}

	if _, err := a.OfferFile(src); err != nil {
		t.Fatalf("offer: %v", err)
	}
	select {
	case <-offered:
	case <-time.After(10 * time.Second):
		t.Fatal("the offer never arrived")
	}

	root := filepath.Join(inbox, "相册")
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		for rel, want := range files {
			got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil || len(got) != len(want) {
				ok = false
				break
			}
		}
		if ok {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	for rel := range files {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s never arrived: %v", rel, err)
		}
	}
	t.Fatal("the files arrived with the wrong sizes")
}

// TestResumeFillsTheHoles: a sidecar bitmap says which chunks are already
// there, so a resumed transfer asks only for the rest — in any order.
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
		in: []*inFile{{entry: FileEntry{Path: "holes.bin", Size: size}, chunks: chunks}},
	}
	n.addFile(ft)

	// Pretend chunks 0..9 and 20..29 arrived earlier, leaving two holes.
	bits := newBitmap(chunks)
	for i := 0; i < 10; i++ {
		bitSet(bits, i)
	}
	for i := 20; i < 30; i++ {
		bitSet(bits, i)
	}
	part := filepath.Join(dir, "holes.bin.part")
	if err := os.WriteFile(part, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveBitmap(part, chunks, bits); err != nil {
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

// TestBitmapRoundTrip: the sidecar has to come back exactly as written, or
// a resume would resend the wrong chunks.
func TestBitmapRoundTrip(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "a.part")
	bits := newBitmap(100)
	for _, i := range []int{0, 3, 17, 63, 99} {
		bitSet(bits, i)
	}
	if err := saveBitmap(part, 100, bits); err != nil {
		t.Fatal(err)
	}
	got := loadBitmap(part, 100)
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
	if loadBitmap(part, 99) != nil {
		t.Fatal("a bitmap of another size was accepted")
	}
}
