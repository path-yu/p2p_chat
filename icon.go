package main

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"sync"
)

// The app's mark: a blue squircle with two orbits crossing it, the same
// drawing the title bar and the welcome show, made a PNG for the icon of
// the window, which Windows puts in the title bar and the taskbar.
var (
	iconOnce sync.Once
	iconData []byte
)

// appIcon returns the icon's PNG, drawing it once.
func appIcon() []byte {
	iconOnce.Do(func() { iconData = drawIcon(256) })
	return iconData
}

// drawIcon paints the mark at size, three times larger for its edges, and
// averages the samples down.
func drawIcon(size int) []byte {
	const ss = 3
	n := size * ss
	f := float64(n)

	type sample struct{ r, g, b, a float64 }
	px := make([]sample, n*n)

	// The blue of the mark, lighter at its top.
	top := [3]float64{0.36, 0.72, 1.0}
	bottom := [3]float64{0.03, 0.26, 0.82}
	corner := f * 0.235
	half := f / 2

	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			sx, sy := float64(x)+0.5, float64(y)+0.5
			if roundedBox(sx, sy, half, corner) > 0 {
				continue
			}
			t := sy / f
			r := mixf(top[0], bottom[0], t)
			g := mixf(top[1], bottom[1], t)
			b := mixf(top[2], bottom[2], t)

			// A light from the top left, as a pane of glass catches one.
			d := math.Hypot(sx-half*0.75, sy-half*0.45) / (f * 0.85)
			if light := math.Max(0, 1-d) * 0.16; light > 0 {
				r, g, b = r+light, g+light, b+light
			}

			// The orbits: two rings and a core, in white.
			ink := math.Max(ring(sx, sy, half, half, f*0.352, f*0.150, 30, f*0.0165),
				ring(sx, sy, half, half, f*0.352, f*0.150, -30, f*0.0165))
			core := math.Hypot(sx-half, sy-half) - f*0.043
			ink = math.Max(ink, clamp01(0.5-core))
			if ink > 0 {
				ink *= 0.97
				r = mixf(r, 1, ink)
				g = mixf(g, 1, ink)
				b = mixf(b, 1, ink)
			}
			px[y*n+x] = sample{r, g, b, 1}
		}
	}

	// Downsample, weighing the colors by their coverage.
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for dy := 0; dy < ss; dy++ {
				for dx := 0; dx < ss; dx++ {
					s := px[(y*ss+dy)*n+x*ss+dx]
					r += s.r * s.a
					g += s.g * s.a
					b += s.b * s.a
					a += s.a
				}
			}
			if a == 0 {
				continue
			}
			i := (y*size + x) * 4
			out.Pix[i] = byte(clamp01(r/a)*255 + 0.5)
			out.Pix[i+1] = byte(clamp01(g/a)*255 + 0.5)
			out.Pix[i+2] = byte(clamp01(b/a)*255 + 0.5)
			out.Pix[i+3] = byte(clamp01(a/(ss*ss))*255 + 0.5)
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, out)
	return buf.Bytes()
}

// roundedBox is the signed distance from a point to the box that fills a
// square of that half side, its corners rounded by r; negative inside.
func roundedBox(x, y, half, r float64) float64 {
	qx := math.Abs(x-half) - (half - r)
	qy := math.Abs(y-half) - (half - r)
	out := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	in := math.Min(math.Max(qx, qy), 0)
	return out + in - r
}

func mixf(a, b, t float64) float64 { return a + (b-a)*t }

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// ring is how much of a pixel the ring of an ellipse covers, from 0 to 1.
func ring(x, y, cx, cy, a, b, angle, halfWidth float64) float64 {
	rad := angle * math.Pi / 180
	dx, dy := x-cx, y-cy
	u := dx*math.Cos(rad) + dy*math.Sin(rad)
	v := -dx*math.Sin(rad) + dy*math.Cos(rad)
	f := (u/a)*(u/a) + (v/b)*(v/b)
	if f <= 0 {
		return 0
	}
	// The distance to the ellipse, near enough for an icon.
	dist := math.Abs(f-1) * b / (2 * math.Sqrt(f))
	return clamp01(halfWidth - dist + 0.5)
}
