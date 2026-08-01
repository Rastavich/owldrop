// genicon generates electron/icon.png — a simple cloud + down-arrow glyph in
// the brand indigo, matching the web UI's logo. Run with `go run ./tools/genicon`.
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
		size = 256
		ss   = 4 // supersampling factor
	)
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	brand := color.RGBA{0x5f, 0x6c, 0xff, 0xff}
	white := color.RGBA{0xff, 0xff, 0xff, 0xff}

	inCloud := func(x, y float64) bool {
		circles := [][3]float64{
			{96, 128, 40}, {128, 112, 48}, {160, 128, 40},
		}
		for _, c := range circles {
			dx, dy := x-c[0], y-c[1]
			if dx*dx+dy*dy <= c[2]*c[2] {
				return true
			}
		}
		return x >= 56 && x <= 200 && y >= 128 && y <= 200
	}
	inArrow := func(x, y float64) bool {
		// stem
		if x >= 121 && x <= 135 && y >= 148 && y <= 180 {
			return true
		}
		// triangle pointing down: apex (128, 208), base y=180 from x=105..151
		if y >= 180 && y <= 208 {
			t := (y - 180) / (208 - 180) // 0 at base, 1 at apex
			half := (151 - 105) / 2 * t
			mid := 128.0
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

	f, err := os.Create("electron/icon.png")
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
