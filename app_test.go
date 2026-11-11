package main

import (
	"os"
	"path/filepath"
	"testing"

	"ptp_chat/internal/identity"
	"ptp_chat/internal/p2p"
	"ptp_chat/internal/store"
)

func newTestApp(t *testing.T) (*app, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	id, err := identity.New("旧名字")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	if err := id.Save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}
	return newApp(id, st, p2p.New(id, 0), dir), dir
}

// TestProfilePersists: what the dialog keeps has to reach the identity
// file, which is what the next launch reads.
func TestProfilePersists(t *testing.T) {
	a, dir := newTestApp(t)

	a.openProfile()
	if !a.profileOpen {
		t.Fatal("the dialog did not open")
	}
	if a.profName != "旧名字" {
		t.Fatalf("the dialog opened with %q", a.profName)
	}
	if a.profLook == "" {
		t.Fatal("the dialog has no look to show")
	}

	a.profName = "  新名字  "
	a.profLook = "2|moon"
	a.saveProfile()

	if a.id.Name != "新名字" || a.id.Avatar != "2|moon" {
		t.Fatalf("memory holds %q %q", a.id.Name, a.id.Avatar)
	}
	reloaded, err := identity.Load(dir, "忽略")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Name != "新名字" || reloaded.Avatar != "2|moon" {
		t.Fatalf("identity.json holds %q %q, so the edit is lost on restart",
			reloaded.Name, reloaded.Avatar)
	}
	if got := a.st.Settings().Name; got != "新名字" {
		t.Fatalf("settings hold %q", got)
	}
}

// TestProfileKeepsNameOnEmpty: an empty field leaves the old name alone.
func TestProfileKeepsNameOnEmpty(t *testing.T) {
	a, _ := newTestApp(t)
	a.openProfile()
	a.profName = "   "
	a.saveProfile()
	if a.id.Name != "旧名字" {
		t.Fatalf("name became %q", a.id.Name)
	}
}

// TestLookSwitchKeepsTheOtherHalf: picking a colour keeps the glyph, and
// the other way round.
func TestLookSwitchKeepsTheOtherHalf(t *testing.T) {
	a, _ := newTestApp(t)
	a.openProfile()
	a.setLookColor(4)
	a.setLookGlyph("globe")
	if a.profLook != "4|globe" {
		t.Fatalf("look is %q", a.profLook)
	}
	a.setLookColor(1)
	if a.profLook != "1|globe" {
		t.Fatalf("colour switched to %q", a.profLook)
	}
	if pair, glyph := avatarLook(a.id.ID(), a.profLook); glyph == nil || pair[0] == "" {
		t.Fatalf("the look does not resolve: %v %v", pair, glyph)
	}
}

// TestAvatarFallsBack: no look means the key's colours and the initial.
func TestAvatarFallsBack(t *testing.T) {
	pair, glyph := avatarLook("abcdef0123", "")
	if pair[0] == "" {
		t.Fatal("no colours")
	}
	if glyph != nil {
		t.Fatal("a fallback look should show the initial, not a glyph")
	}
}

// TestDownloadDirIsRemembered: the folder chosen for received files must
// survive a restart.
func TestDownloadDirIsRemembered(t *testing.T) {
	a, dir := newTestApp(t)
	if a.downloadDir == "" {
		t.Fatal("no default folder")
	}
	want := filepath.Join(dir, "收件")
	a.setDownloadDir(want)

	st2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	again := newApp(a.id, st2, a.node, dir)
	if again.downloadDir != want {
		t.Fatalf("after a restart the folder is %q, want %q", again.downloadDir, want)
	}
}

// TestPendingAttachments: files staged in the input are added once, can be
// taken back, and survive a send that had nowhere to go.
func TestPendingAttachments(t *testing.T) {
	a, dir := newTestApp(t)
	src := filepath.Join(dir, "报告.pdf")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	a.addPending([]string{src, src, ""})
	if len(a.pending) != 1 {
		t.Fatalf("staged %d, want 1", len(a.pending))
	}
	a.addPending([]string{filepath.Join(dir, "第二个.txt")})
	if len(a.pending) != 2 {
		t.Fatalf("staged %d, want 2", len(a.pending))
	}
	a.dropPending(0)
	if len(a.pending) != 1 {
		t.Fatalf("after dropping one, %d remain", len(a.pending))
	}

	// No peer is connected here, so submit must keep them for a retry.
	a.submit()
	if len(a.pending) != 1 {
		t.Fatalf("a failed send threw the attachment away: %d left", len(a.pending))
	}
}
