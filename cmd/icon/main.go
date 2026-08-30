// Command icon renders the canonical Kerneon pressure-arc monogram using only
// the Go standard library. The native UI and remote SVG use these same ratios.
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
	designRadius := 250.0 * scale
	arcRadius := designRadius * 0.65
	strokeRadius := designRadius * 0.14 / 2
	rayStartX := -designRadius * 0.04
	rayEndX := designRadius * 0.54
	rayEndY := designRadius * 0.51
	mark := color.NRGBA{R: 224, G: 244, B: 246, A: 255}
	for y := 0; y < size*scale; y++ {
		for x := 0; x < size*scale; x++ {
			dx, dy := float64(x)+0.5-center, float64(y)+0.5-center
			distance := math.Hypot(dx, dy)
			angle := math.Atan2(dy, dx) * 180 / math.Pi
			if angle < 0 {
				angle += 360
			}
			pixel := color.NRGBA{}

			arcDistance := math.Inf(1)
			if angle >= 100 && angle <= 260 {
				arcDistance = math.Abs(distance - arcRadius)
			}
			for _, degrees := range []float64{100, 260} {
				radians := degrees * math.Pi / 180
				capDistance := math.Hypot(dx-arcRadius*math.Cos(radians), dy-arcRadius*math.Sin(radians))
				arcDistance = math.Min(arcDistance, capDistance)
			}
			rayDistance := math.Min(
				segmentDistance(dx, dy, rayStartX, 0, rayEndX, -rayEndY),
				segmentDistance(dx, dy, rayStartX, 0, rayEndX, rayEndY),
			)
			markDistance := math.Min(arcDistance, rayDistance)
			if markDistance <= strokeRadius+5*scale {
				falloff := 1 - math.Max(0, markDistance-strokeRadius)/(5*scale)
				pixel = color.NRGBA{R: mark.R, G: mark.G, B: mark.B, A: uint8(math.Round(24 * falloff))}
			}
			if markDistance <= strokeRadius {
				pixel = mark
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

func segmentDistance(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	if dx == 0 && dy == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	t := ((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}
