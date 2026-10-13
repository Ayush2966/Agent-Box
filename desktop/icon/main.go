//go:build ignore

// Renders the app icon with the standard library only, so the icon is
// reproducible from source: go run desktop/icon/main.go -o desktop/icon/agentbox-1024.png
// It draws a macOS-style rounded square (Big Sur proportions: the square is
// 824/1024 of the canvas) holding an isometric box — agentbox's whole idea in
// one shape — supersampled 2x and averaged down for smooth edges.
package main

import (
	"flag"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

type pt struct{ x, y float64 }

// inPoly is the even-odd rule for a simple polygon.
func inPoly(p pt, poly []pt) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		a, b := poly[i], poly[j]
		if (a.y > p.y) != (b.y > p.y) && p.x < (b.x-a.x)*(p.y-a.y)/(b.y-a.y)+a.x {
			in = !in
		}
	}
	return in
}

// inRounded reports whether p lies in the rounded square [lo,hi]² with corner radius r.
func inRounded(p pt, lo, hi, r float64) bool {
	if p.x < lo || p.y < lo || p.x > hi || p.y > hi {
		return false
	}
	cx := math.Max(lo+r, math.Min(p.x, hi-r))
	cy := math.Max(lo+r, math.Min(p.y, hi-r))
	return math.Hypot(p.x-cx, p.y-cy) <= r
}

func main() {
	out := flag.String("o", "agentbox-1024.png", "output PNG (1024x1024)")
	flag.Parse()

	const size = 1024
	const ss = 2 // supersampling factor
	const n = size * ss

	// Rounded square: Apple's icon grid leaves ~10% transparent margin.
	lo, hi := 100.0*ss, 924.0*ss
	radius := (hi - lo) * 0.2237

	// Isometric box: C is the front vertex where the three visible faces meet.
	cx, cy := 512.0*ss, 540.0*ss
	w, v, e := 300.0*ss, 170.0*ss, 330.0*ss // horizontal reach, rhombus half-height, edge height
	top := []pt{{cx, cy}, {cx + w, cy - v}, {cx, cy - 2*v}, {cx - w, cy - v}}
	left := []pt{{cx, cy}, {cx - w, cy - v}, {cx - w, cy - v + e}, {cx, cy + e}}
	right := []pt{{cx, cy}, {cx, cy + e}, {cx + w, cy - v + e}, {cx + w, cy - v}}

	bg := color.NRGBA{0x15, 0x19, 0x22, 0xff}
	cTop := color.NRGBA{0x8c, 0xf5, 0xbd, 0xff}
	cLeft := color.NRGBA{0x2e, 0xb8, 0x7a, 0xff}
	cRight := color.NRGBA{0x1d, 0x8a, 0x5c, 0xff}
	seam := color.NRGBA{0x15, 0x19, 0x22, 0xff} // thin gaps between faces read as edges

	big := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			p := pt{float64(x) + 0.5, float64(y) + 0.5}
			var c color.NRGBA
			switch {
			case !inRounded(p, lo, hi, radius):
				c = color.NRGBA{}
			case nearEdge(p, cx, cy, w, v, e, 7*ss):
				c = seam
			case inPoly(p, top):
				c = cTop
			case inPoly(p, left):
				c = cLeft
			case inPoly(p, right):
				c = cRight
			default:
				c = bg
			}
			big.SetNRGBA(x, y, c)
		}
	}

	// Box-filter downsample (premultiplied, so transparent edges blend right).
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for dy := 0; dy < ss; dy++ {
				for dx := 0; dx < ss; dx++ {
					c := big.NRGBAAt(x*ss+dx, y*ss+dy)
					af := float64(c.A) / 255
					r += float64(c.R) * af
					g += float64(c.G) * af
					b += float64(c.B) * af
					a += af
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(a / (ss * ss) * 255)})
		}
	}

	f, err := os.Create(*out)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}

// nearEdge is true within d of the three inner edges meeting at C, so the
// faces are separated by a hairline of background.
func nearEdge(p pt, cx, cy, w, v, e, d float64) bool {
	segs := [][2]pt{
		{{cx, cy}, {cx, cy + e}},     // vertical front edge
		{{cx, cy}, {cx - w, cy - v}}, // top-left edge
		{{cx, cy}, {cx + w, cy - v}}, // top-right edge
	}
	for _, s := range segs {
		if distSeg(p, s[0], s[1]) <= d {
			return true
		}
	}
	return false
}

func distSeg(p, a, b pt) float64 {
	vx, vy := b.x-a.x, b.y-a.y
	t := ((p.x-a.x)*vx + (p.y-a.y)*vy) / (vx*vx + vy*vy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(p.x-(a.x+t*vx), p.y-(a.y+t*vy))
}
