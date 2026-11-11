package main

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestAppIcon checks that the mark is a square PNG: clear at the corners,
// mostly the blue of the mark, with the orbits in white across it. It
// writes the icon for a look.
func TestAppIcon(t *testing.T) {
	data := appIcon()
	if len(data) < 1000 {
		t.Fatalf("icon is %d bytes", len(data))
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != b.Dy() || b.Dx() < 64 {
		t.Fatalf("icon is %dx%d", b.Dx(), b.Dy())
	}
	if _, _, _, a := img.At(b.Min.X, b.Min.Y).RGBA(); a != 0 {
		t.Errorf("a corner is not transparent: alpha %d", a)
	}

	var blue, white, total int
	for y := b.Min.Y; y < b.Max.Y; y += 2 {
		for x := b.Min.X; x < b.Max.X; x += 2 {
			r, g, bl, a := img.At(x, y).RGBA()
			if a < 0x8000 {
				continue
			}
			total++
			switch {
			case bl>>8 > 140 && r>>8 < 140:
				blue++
			case r>>8 > 225 && g>>8 > 225 && bl>>8 > 225:
				white++
			}
		}
	}
	if blue*100 < total*25 {
		t.Errorf("only %d%% of the mark is blue", blue*100/total)
	}
	if white*100 < total*8 {
		t.Errorf("only %d%% of the mark is white", white*100/total)
	}

	_ = os.MkdirAll("testdata", 0o755)
	if err := os.WriteFile(filepath.Join("testdata", "icon.png"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
