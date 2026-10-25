// genicon generates the Owldrop owl mark in two forms:
//
//   - icon.png: two cream facial discs, dark pupils and a gold beak on the
//     brand indigo→violet rounded square, matching site/public/favicon.svg
//     (the vector master; keep the two in sync when tweaking shapes).
//   - tray_template.png: the same owl as a black-on-transparent silhouette
//     for the macOS menu bar. macOS tints template images from their alpha
//     channel alone, so the full-colour icon (an opaque rounded square)
//     would show up there as a solid white or black block.
//
// Run with `go run ./tools/genicon`.
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

// Geometry mirrors site/public/favicon.svg (viewBox 0 0 512 512,
// group translate(0,18) already applied here).
var (
	discs      = [][3]float64{{192, 258, 88}, {320, 258, 88}}
	pupils     = [][3]float64{{192, 258, 36}, {320, 258, 36}}
	highlights = [][3]float64{{205, 245, 12}, {333, 245, 12}}
	tufts      = [][][2]float64{
		{{148, 134}, {214, 196}, {128, 208}},
		{{364, 134}, {298, 196}, {384, 208}},
	}
	beak = [][2]float64{{234, 320}, {278, 320}, {256, 364}}
)

func main() {
	writePNG("icon.png", appIcon())
	writePNG("tray_template.png", trayTemplate())
}

func appIcon() *image.RGBA {
	const (
		size = 512
		ss   = 4 // supersampling factor
	)
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	cream := [3]float64{0xef, 0xea, 0xff}
	dark := [3]float64{0x1a, 0x14, 0x40}
	gold := [3]float64{0xff, 0xb2, 0x24}
	gradA := [3]float64{0x6d, 0x7b, 0xff}
	gradB := [3]float64{0x9a, 0x5c, 0xff}

	const radius = 112.0
	inRoundedRect := func(x, y float64) bool {
		// distance from the rounded-rect SDF boundary
		qx := math.Abs(x-size/2) - (size/2 - radius)
		qy := math.Abs(y-size/2) - (size/2 - radius)
		ax := math.Max(qx, 0)
		ay := math.Max(qy, 0)
		return math.Hypot(ax, ay)+math.Min(math.Max(qx, qy), 0) <= radius
	}

	for py := range size {
		for px := range size {
			var r, g, b, a float64
			for sy := range ss {
				for sx := range ss {
					x := float64(px) + (float64(sx)+0.5)/ss
					y := float64(py) + (float64(sy)+0.5)/ss
					if !inRoundedRect(x, y) {
						continue // transparent corner
					}
					var c [3]float64
					switch {
					case inCircles(highlights, x, y):
						c = [3]float64{0xff, 0xff, 0xff}
					case inCircles(pupils, x, y):
						c = dark
					case inTri(beak, x, y):
						c = gold
					case inCircles(discs, x, y) || inTris(tufts, x, y):
						c = cream
					default:
						t := (x + y) / (2 * size)
						c = [3]float64{
							gradA[0] + (gradB[0]-gradA[0])*t,
							gradA[1] + (gradB[1]-gradA[1])*t,
							gradA[2] + (gradB[2]-gradA[2])*t,
						}
					}
					r += c[0]
					g += c[1]
					b += c[2]
					a += 1
				}
			}
			n := float64(ss * ss)
			img.Set(px, py, color.RGBA{
				R: uint8(r / n),
				G: uint8(g / n),
				B: uint8(b / n),
				A: uint8(255 * a / n),
			})
		}
	}
	return img
}

// trayTemplate renders the macOS menu-bar glyph at 44×44 px: the 22 pt
// status-bar thickness at @2x (wails scales the image to that thickness).
// The owl is cropped to its own bounds and scaled to ~18 pt wide, leaving
// the usual padding around a menu-bar glyph. Only alpha matters: the pupils
// are cut out (keeping the catch-lights), and a thin gap separates the beak
// from the facial discs so it still reads at this size.
func trayTemplate() *image.RGBA {
	const (
		size    = 44
		ss      = 8            // supersampling factor
		scale   = 36.0 / 304.0 // owl spans x 104..408 → 36 px
		cx, cy  = 256.0, 249.0 // centre of the owl's bounding box
		beakGap = 14.0         // favicon units (~1.7 px here)
	)
	ink := func(x, y float64) bool {
		switch {
		case inTri(beak, x, y):
			return true
		case distToTri(beak, x, y) < beakGap:
			return false
		case inCircles(pupils, x, y):
			return inCircles(highlights, x, y)
		default:
			return inCircles(discs, x, y) || inTris(tufts, x, y)
		}
	}

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for py := range size {
		for px := range size {
			hits := 0
			for sy := range ss {
				for sx := range ss {
					x := cx + (float64(px)+(float64(sx)+0.5)/ss-size/2)/scale
					y := cy + (float64(py)+(float64(sy)+0.5)/ss-size/2)/scale
					if ink(x, y) {
						hits++
					}
				}
			}
			img.Set(px, py, color.RGBA{A: uint8(255*float64(hits)/(ss*ss) + 0.5)})
		}
	}
	return img
}

func inCircles(cs [][3]float64, x, y float64) bool {
	for _, c := range cs {
		dx, dy := x-c[0], y-c[1]
		if dx*dx+dy*dy <= c[2]*c[2] {
			return true
		}
	}
	return false
}

func inTri(t [][2]float64, x, y float64) bool {
	sign := func(a, b [2]float64) float64 {
		return (x-b[0])*(a[1]-b[1]) - (a[0]-b[0])*(y-b[1])
	}
	d1, d2, d3 := sign(t[0], t[1]), sign(t[1], t[2]), sign(t[2], t[0])
	hasNeg := d1 < 0 || d2 < 0 || d3 < 0
	hasPos := d1 > 0 || d2 > 0 || d3 > 0
	return !(hasNeg && hasPos)
}

func inTris(ts [][][2]float64, x, y float64) bool {
	for _, t := range ts {
		if inTri(t, x, y) {
			return true
		}
	}
	return false
}

// distToTri is 0 inside the triangle, else the distance to its nearest edge.
func distToTri(t [][2]float64, x, y float64) float64 {
	if inTri(t, x, y) {
		return 0
	}
	d := math.Inf(1)
	for i := range t {
		a, b := t[i], t[(i+1)%len(t)]
		dx, dy := b[0]-a[0], b[1]-a[1]
		u := math.Max(0, math.Min(1, ((x-a[0])*dx+(y-a[1])*dy)/(dx*dx+dy*dy)))
		d = math.Min(d, math.Hypot(x-a[0]-u*dx, y-a[1]-u*dy))
	}
	return d
}

func writePNG(name string, img image.Image) {
	f, err := os.Create(name)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}
