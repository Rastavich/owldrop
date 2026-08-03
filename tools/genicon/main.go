// genicon generates icon.png — a simple cloud + down-arrow glyph in the
// brand indigo, matching the web UI's logo. Run with `go run ./tools/genicon`.
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
	// Glyph coordinates below are in 256-space; scale them to the canvas.
	u := float64(size) / 256
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	brand := color.RGBA{0x5f, 0x6c, 0xff, 0xff}
	white := color.RGBA{0xff, 0xff, 0xff, 0xff}

	inCloud := func(x, y float64) bool {
		circles := [][3]float64{
			{96 * u, 128 * u, 40 * u}, {128 * u, 112 * u, 48 * u}, {160 * u, 128 * u, 40 * u},
		}
		for _, c := range circles {
			dx, dy := x-c[0], y-c[1]
			if dx*dx+dy*dy <= c[2]*c[2] {
				return true
			}
		}
		return x >= 56*u && x <= 200*u && y >= 128*u && y <= 200*u
	}
	inArrow := func(x, y float64) bool {
		// stem
		if x >= 121*u && x <= 135*u && y >= 148*u && y <= 180*u {
			return true
		}
		// triangle pointing down: apex (128*u, 208*u), base y=180*u from x=105..151
		if y >= 180*u && y <= 208*u {
			t := (y - 180*u) / (208*u - 180*u) // 0 at base, 1 at apex
			half := (151*u - 105*u) / 2 * t
			mid := 128 * u
			return math.Abs(x-mid) <= half
		}
		return false
	}

	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var cCount, aCount int
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := float64(px) + (float64(sx)+0.5)/ss
					y := float64(py) + (float64(sy)+0.5)/ss
					if inArrow(x, y) {
						aCount++
					} else if inCloud(x, y) {
						cCount++
					}
				}
			}
			switch {
			case aCount > 0:
				img.Set(px, py, blend(white, aCount, ss*ss))
			case cCount > 0:
				img.Set(px, py, blend(brand, cCount, ss*ss))
			default:
				img.Set(px, py, color.RGBA{0, 0, 0, 0})
			}
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

func blend(c color.RGBA, n, total int) color.RGBA {
	a := float64(n) / float64(total)
	return color.RGBA{
		R: uint8(float64(c.R) * a),
		G: uint8(float64(c.G) * a),
		B: uint8(float64(c.B) * a),
		A: uint8(255 * a),
	}
}
