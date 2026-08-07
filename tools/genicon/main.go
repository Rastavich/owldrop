// genicon generates icon.png — the Owldrop owl mark: two cream facial
// discs, dark pupils and a gold beak on the brand indigo→violet rounded
// square, matching site/public/favicon.svg (the vector master; keep the
// two in sync when tweaking shapes). Run with `go run ./tools/genicon`.
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

func main() {
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

	// Geometry mirrors site/public/favicon.svg (viewBox 0 0 512 512,
	// group translate(0,18) already applied here).
	discs := [][3]float64{{192, 258, 88}, {320, 258, 88}}
	pupils := [][3]float64{{192, 258, 36}, {320, 258, 36}}
	highlights := [][3]float64{{205, 245, 12}, {333, 245, 12}}
	tufts := [][][2]float64{
		{{148, 134}, {214, 196}, {128, 208}},
		{{364, 134}, {298, 196}, {384, 208}},
	}
	beak := [][2]float64{{234, 320}, {278, 320}, {256, 364}}

	inCircles := func(cs [][3]float64, x, y float64) bool {
		for _, c := range cs {
			dx, dy := x-c[0], y-c[1]
			if dx*dx+dy*dy <= c[2]*c[2] {
				return true
			}
		}
		return false
	}
	inTri := func(t [][2]float64, x, y float64) bool {
		sign := func(a, b [2]float64) float64 {
			return (x-b[0])*(a[1]-b[1]) - (a[0]-b[0])*(y-b[1])
		}
		d1, d2, d3 := sign(t[0], t[1]), sign(t[1], t[2]), sign(t[2], t[0])
		hasNeg := d1 < 0 || d2 < 0 || d3 < 0
		hasPos := d1 > 0 || d2 > 0 || d3 > 0
		return !(hasNeg && hasPos)
	}
	inTris := func(ts [][][2]float64, x, y float64) bool {
		for _, t := range ts {
			if inTri(t, x, y) {
				return true
			}
		}
		return false
	}
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

	f, err := os.Create("icon.png")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}
