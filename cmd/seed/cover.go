package main

import (
	"bytes"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"math"
)

const coverW, coverH = 1200, 630 // the Open Graph image size, so covers double as share cards

// drawCover renders a deterministic abstract cover: a diagonal two-tone gradient around `hue`
// (degrees), a soft dot grid and a few translucent rings placed from the slug's hash. The
// same slug always yields the same bytes, so re-uploads dedupe by checksum.
func drawCover(slug string, hue float64) []byte {
	h := fnv.New64a()
	_, _ = h.Write([]byte(slug))
	seed := h.Sum64()

	img := image.NewRGBA(image.Rect(0, 0, coverW, coverH))
	from := hsl(hue, 0.62, 0.32)
	to := hsl(math.Mod(hue+48, 360), 0.70, 0.52)

	type ring struct{ x, y, r, w float64 }
	rings := make([]ring, 3)
	for i := range rings {
		v := seed >> (i * 16)
		rings[i] = ring{
			x: float64(v % uint64(coverW)), y: float64((v >> 8) % uint64(coverH)),
			r: 120 + float64((v>>4)%220), w: 18 + float64((v>>12)%20),
		}
	}

	for y := range coverH {
		for x := range coverW {
			t := (float64(x)/coverW*0.65 + float64(y)/coverH*0.35)
			c := mix(from, to, t)
			for _, rg := range rings {
				d := math.Abs(math.Hypot(float64(x)-rg.x, float64(y)-rg.y) - rg.r)
				if d < rg.w {
					c = mix(c, color.RGBA{255, 255, 255, 255}, 0.10*(1-d/rg.w))
				}
			}
			// Dot grid every 30px, fading towards the bottom-left so titles laid over it stay legible.
			if x%30 == 15 && y%30 == 15 {
				c = mix(c, color.RGBA{255, 255, 255, 255}, 0.22*t)
			}
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err) // encoding an in-memory RGBA image cannot fail
	}
	return buf.Bytes()
}

func mix(a, b color.RGBA, t float64) color.RGBA {
	l := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
	return color.RGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 255}
}

func hsl(h, s, l float64) color.RGBA {
	c := (1 - math.Abs(2*l-1)) * s
	hp := h / 60
	x := c * (1 - math.Abs(math.Mod(hp, 2)-1))
	var r, g, b float64
	switch {
	case hp < 1:
		r, g = c, x
	case hp < 2:
		r, g = x, c
	case hp < 3:
		g, b = c, x
	case hp < 4:
		g, b = x, c
	case hp < 5:
		r, b = x, c
	default:
		r, b = c, x
	}
	m := l - c/2
	to8 := func(v float64) uint8 { return uint8(math.Round((v + m) * 255)) }
	return color.RGBA{to8(r), to8(g), to8(b), 255}
}
