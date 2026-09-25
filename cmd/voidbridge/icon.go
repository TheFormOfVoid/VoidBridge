package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// makeIcon draws the tray icon: a ring with a filled centre, in the given
// colour. On Windows systray wants ICO bytes; ICO may wrap a PNG directly.
func makeIcon(c color.NRGBA, ico bool) []byte {
	const size = 32
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := math.Hypot(float64(x)-15.5, float64(y)-15.5)
			var a float64
			switch {
			case d <= 6: // core
				a = 1
			case d >= 10 && d <= 14: // ring
				a = 1
			case d > 6 && d < 7:
				a = 7 - d
			case d > 9 && d < 10:
				a = d - 9
			case d > 14 && d < 15:
				a = 15 - d
			}
			if a > 0 {
				img.SetNRGBA(x, y, color.NRGBA{c.R, c.G, c.B, uint8(a * 255)})
			}
		}
	}
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, img)
	if !ico {
		return pngBuf.Bytes()
	}

	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, []uint16{0, 1, 1}) // reserved, type=icon, count
	out.Write([]byte{size, size, 0, 0})                         // w, h, palette, reserved
	binary.Write(&out, binary.LittleEndian, []uint16{1, 32})     // planes, bpp
	binary.Write(&out, binary.LittleEndian, []uint32{uint32(pngBuf.Len()), 22})
	out.Write(pngBuf.Bytes())
	return out.Bytes()
}
