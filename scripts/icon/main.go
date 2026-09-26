//go:build ignore

// Draws SuperSync's app icon: an orange record on a dark rounded tile, with
// a sync arc. Writes assets/icon-1024.png, assets/SuperSync.ico and an
// iconset for macOS's iconutil. Run: go run scripts/icon/main.go
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const S = 1024

func main() {
	img := render(S, 3)
	os.MkdirAll("assets/SuperSync.iconset", 0o755)
	save("assets/icon-1024.png", img)
	for _, n := range []int{16, 32, 128, 256, 512} {
		save(filepath.Join("assets/SuperSync.iconset", pngName(n, 1)), resize(img, n))
		save(filepath.Join("assets/SuperSync.iconset", pngName(n, 2)), resize(img, 2*n))
	}
	writeICO("assets/SuperSync.ico", img, []int{16, 24, 32, 48, 64, 128, 256})
}

func pngName(n, scale int) string {
	if scale == 2 {
		return "icon_" + itoa(n) + "x" + itoa(n) + "@2x.png"
	}
	return "icon_" + itoa(n) + "x" + itoa(n) + ".png"
}

func itoa(n int) string { return string(appendInt(nil, n)) }
func appendInt(b []byte, n int) []byte {
	if n >= 10 {
		b = appendInt(b, n/10)
	}
	return append(b, byte('0'+n%10))
}

// render draws at size×ss and averages down (supersampling for smooth edges).
func render(size, ss int) *image.NRGBA {
	W := size * ss
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	f := float64(W)
	c := f / 2
	// macOS icon grid: the tile is ~80% of the canvas.
	tile := f * 0.805
	x0 := c - tile/2
	radius := tile * 0.225
	for oy := 0; oy < size; oy++ {
		for ox := 0; ox < size; ox++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(ox*ss+sx) + 0.5
					py := float64(oy*ss+sy) + 0.5
					pr, pg, pb, pa := shade(px, py, c, x0, tile, radius, f)
					r += pr * pa
					g += pg * pa
					b += pb * pa
					a += pa
				}
			}
			n := float64(ss * ss)
			if a > 0 {
				out.SetNRGBA(ox, oy, color.NRGBA{u8(r / a), u8(g / a), u8(b / a), u8(a / n)})
			}
		}
	}
	return out
}

func shade(px, py, c, x0, tile, radius, f float64) (r, g, b, a float64) {
	// Rounded tile (a soft shadow below it).
	d := roundRect(px, py, x0, x0, tile, tile, radius)
	if d > 0 {
		sd := roundRect(px, py-f*0.012, x0, x0, tile, tile, radius)
		if sd < f*0.03 {
			al := 0.22 * (1 - sd/(f*0.03))
			return 0, 0, 0, al
		}
		return 0, 0, 0, 0
	}
	// Tile: dark, slightly lighter at the top.
	t := (py - x0) / tile
	r, g, b = lerp(0.105, 0.052, t), lerp(0.105, 0.052, t), lerp(0.112, 0.058, t)
	// Faint warm glow from the upper left.
	glow := math.Max(0, 1-math.Hypot(px-(x0+tile*0.25), py-(x0+tile*0.2))/(tile*0.9))
	r += 0.10 * glow * glow
	g += 0.04 * glow * glow

	dx, dy := px-c, py-c
	dist := math.Hypot(dx, dy)
	R := tile * 0.36
	if dist < R {
		// The record: orange gradient, darker grooves, dark label hole.
		k := (dx + dy) / (2 * R) // -1 top-left .. 1 bottom-right
		rr, gg, bb := lerp(1.0, 0.95, k*0.5+0.5), lerp(0.62, 0.36, k*0.5+0.5), lerp(0.33, 0.10, k*0.5+0.5)
		groove := 0.5 + 0.5*math.Sin(dist/R*95)
		if dist > R*0.42 && dist < R*0.95 {
			m := 1 - 0.07*groove
			rr, gg, bb = rr*m, gg*m, bb*m
		}
		// Sheen.
		ang := math.Atan2(dy, dx)
		sheen := math.Pow(math.Max(0, math.Cos(2*(ang+0.8))), 6) * 0.10 * (dist / R)
		rr, gg, bb = rr+sheen, gg+sheen, bb+sheen
		if dist < R*0.3 {
			rr, gg, bb = 0.08, 0.08, 0.085 // label
			if dist < R*0.075 {
				rr, gg, bb = 1.0, 0.45, 0.14 // spindle
			}
		}
		// Edge anti-alias against the tile.
		e := clamp((R - dist) / (f * 0.0015))
		r, g, b = mix(r, rr, e), mix(g, gg, e), mix(b, bb, e)
	}
	// Sync arc around the record, with an arrowhead.
	ar := tile * 0.43
	w := tile * 0.022
	ang := math.Atan2(dy, dx)
	if math.Abs(dist-ar) < w {
		// From -150° to 60° (clockwise in screen space).
		a0, a1 := -150*math.Pi/180, 60*math.Pi/180
		if ang >= a0 && ang <= a1 {
			e := clamp((w - math.Abs(dist-ar)) / (f * 0.0015))
			r, g, b = mix(r, 0.96, e), mix(g, 0.95, e), mix(b, 0.93, e)
		}
	}
	// Arrowhead at the arc's end (60°), pointing along the arc.
	end := 60 * math.Pi / 180
	hx, hy := c+ar*math.Cos(end), c+ar*math.Sin(end)
	tx, ty := -math.Sin(end), math.Cos(end) // direction of travel
	nx, ny := math.Cos(end), math.Sin(end)
	L := tile * 0.085
	tip := [2]float64{hx + tx*L, hy + ty*L}
	b1 := [2]float64{hx + nx*L*0.75, hy + ny*L*0.75}
	b2 := [2]float64{hx - nx*L*0.75, hy - ny*L*0.75}
	if inTri(px, py, tip, b1, b2) {
		r, g, b = 0.96, 0.95, 0.93
	}
	return r, g, b, 1
}

func inTri(px, py float64, a, b, c [2]float64) bool {
	s := func(p, q, r [2]float64) float64 { return (p[0]-r[0])*(q[1]-r[1]) - (q[0]-r[0])*(p[1]-r[1]) }
	p := [2]float64{px, py}
	d1, d2, d3 := s(p, a, b), s(p, b, c), s(p, c, a)
	neg := d1 < 0 || d2 < 0 || d3 < 0
	pos := d1 > 0 || d2 > 0 || d3 > 0
	return !(neg && pos)
}

func roundRect(px, py, x, y, w, h, r float64) float64 {
	cx, cy := x+w/2, y+h/2
	qx := math.Abs(px-cx) - (w/2 - r)
	qy := math.Abs(py-cy) - (h/2 - r)
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

func lerp(a, b, t float64) float64 { t = clamp(t); return a + (b-a)*t }
func mix(a, b, t float64) float64  { return a + (b-a)*t }
func clamp(t float64) float64      { return math.Max(0, math.Min(1, t)) }
func u8(v float64) uint8           { return uint8(math.Round(clamp(v) * 255)) }

// resize is a box-filter downscale (sizes here divide evenly or nearly).
func resize(src *image.NRGBA, n int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, n, n))
	s := float64(src.Bounds().Dx()) / float64(n)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var r, g, b, a, cnt float64
			for sy := int(float64(y) * s); sy < int(float64(y+1)*s); sy++ {
				for sx := int(float64(x) * s); sx < int(float64(x+1)*s); sx++ {
					c := src.NRGBAAt(sx, sy)
					al := float64(c.A) / 255
					r += float64(c.R) * al
					g += float64(c.G) * al
					b += float64(c.B) * al
					a += al
					cnt++
				}
			}
			if a > 0 {
				dst.SetNRGBA(x, y, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(255 * a / cnt)})
			}
		}
	}
	return dst
}

func save(p string, img image.Image) {
	f, err := os.Create(p)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	png.Encode(f, img)
}

// writeICO writes a Windows icon of 32-bit bitmaps (read by everything,
// including the NSIS installer builder, which can't read PNG icons).
func writeICO(p string, img *image.NRGBA, sizes []int) {
	var imgs [][]byte
	for _, n := range sizes {
		imgs = append(imgs, dib(resize(img, n)))
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	off := 6 + 16*len(sizes)
	for i, n := range sizes {
		dim := byte(n)
		if n >= 256 {
			dim = 0
		}
		out.Write([]byte{dim, dim, 0, 0})
		binary.Write(&out, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(imgs[i])), uint32(off)})
		off += len(imgs[i])
	}
	for _, b := range imgs {
		out.Write(b)
	}
	os.WriteFile(p, out.Bytes(), 0o644)
}

// dib encodes an icon image as a BITMAPINFOHEADER, bottom-up BGRA pixels and
// an (unused, all-zero) AND mask.
func dib(m *image.NRGBA) []byte {
	n := m.Bounds().Dx()
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, struct {
		Size          uint32
		Width, Height int32
		Planes, Bits  uint16
		Compression   uint32
		ImageSize     uint32
		XPPM, YPPM    int32
		Used, Imp     uint32
	}{40, int32(n), int32(2 * n), 1, 32, 0, 0, 0, 0, 0, 0})
	for y := n - 1; y >= 0; y-- {
		for x := 0; x < n; x++ {
			c := m.NRGBAAt(x, y)
			b.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	stride := ((n + 31) / 32) * 4
	b.Write(make([]byte, stride*n))
	return b.Bytes()
}
