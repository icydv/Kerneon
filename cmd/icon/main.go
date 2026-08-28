// Command icon renders Kerneon's deliberately simple core mark using only the
// Go standard library. Keeping the source geometric makes every release icon
// reproducible and avoids baking a generated illustration into the product.
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

const (
	size  = 512
	scale = 4
)

func main() {
	img := image.NewNRGBA(image.Rect(0, 0, size*scale, size*scale))
	center := float64(size*scale) / 2
	for y := 0; y < size*scale; y++ {
		for x := 0; x < size*scale; x++ {
			dx, dy := float64(x)+0.5-center, float64(y)+0.5-center
			distance := math.Hypot(dx, dy)
			angle := math.Atan2(dy, dx) * 180 / math.Pi
			if angle < 0 {
				angle += 360
			}
			var pixel color.NRGBA
			switch {
			case distance <= 218*scale:
				pixel = color.NRGBA{R: 20, G: 21, B: 24, A: 255}
			}
			// The gap is intentional: it keeps the core open, not button-like.
			if math.Abs(distance-142*scale) <= 15*scale && angle >= 28 && angle <= 332 {
				pixel = color.NRGBA{R: 232, G: 234, B: 239, A: 255}
			}
			if distance <= 18*scale {
				pixel = color.NRGBA{R: 139, G: 171, B: 216, A: 255}
			}
			img.SetNRGBA(x, y, pixel)
		}
	}
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var rr, gg, bb, aa uint32
			for sy := 0; sy < scale; sy++ {
				for sx := 0; sx < scale; sx++ {
					p := img.NRGBAAt(x*scale+sx, y*scale+sy)
					rr += uint32(p.R)
					gg += uint32(p.G)
					bb += uint32(p.B)
					aa += uint32(p.A)
				}
			}
			divisor := uint32(scale * scale)
			out.SetNRGBA(x, y, color.NRGBA{R: uint8(rr / divisor), G: uint8(gg / divisor), B: uint8(bb / divisor), A: uint8(aa / divisor)})
		}
	}
	file, err := os.Create("assets/kerneon-icon.png")
	if err != nil {
		panic(err)
	}
	defer file.Close()
	if err := png.Encode(file, out); err != nil {
		panic(err)
	}
}
