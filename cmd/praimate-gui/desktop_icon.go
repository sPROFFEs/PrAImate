package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// GTK silently drops icons that exceed the X11 property request limit. Keep
// window icons below that limit; desktop launchers retain the original asset.
// Box sampling preserves alpha and aspect ratio without a runtime dependency.
func desktopWindowIcon(source []byte) []byte {
	img, err := png.Decode(bytes.NewReader(source))
	if err != nil {
		return source
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	const maxSide = 128
	if w <= maxSide && h <= maxSide {
		return source
	}
	scale := max(w, h)
	w, h = max(1, w*maxSide/scale), max(1, h*maxSide/scale)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			x0, x1 := x*bounds.Dx()/w, (x+1)*bounds.Dx()/w
			y0, y1 := y*bounds.Dy()/h, (y+1)*bounds.Dy()/h
			var r, g, b, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					pr, pg, pb, pa := img.At(bounds.Min.X+sx, bounds.Min.Y+sy).RGBA()
					r += uint64(pr)
					g += uint64(pg)
					b += uint64(pb)
					a += uint64(pa)
					n++
				}
			}
			out.SetRGBA(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(b / n >> 8), uint8(a / n >> 8)})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, out); err != nil {
		return source
	}
	return encoded.Bytes()
}
