package clipboard

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

// ToPNG decodes any supported image (PNG, JPEG, GIF, WebP, BMP) and returns
// PNG bytes. PNG input is returned unchanged.
func ToPNG(data []byte) ([]byte, image.Image, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	if format == "png" {
		return data, img, nil
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), img, nil
}

// EncodePNG encodes an image quickly.
func EncodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img)
	return buf.Bytes(), err
}

// ---- Windows DIB (device-independent bitmap) conversion ----

const (
	biRGB       = 0
	biBitfields = 3
)

// DIBToImage decodes a CF_DIB / CF_DIBV5 blob (a BITMAPINFO header followed by
// pixels, without the BMP file header).
func DIBToImage(dib []byte) (image.Image, error) {
	if len(dib) < 40 {
		return nil, errors.New("dib: too short")
	}
	le := binary.LittleEndian
	hdrSize := int(le.Uint32(dib[0:]))
	w := int(int32(le.Uint32(dib[4:])))
	h := int(int32(le.Uint32(dib[8:])))
	bpp := int(le.Uint16(dib[14:]))
	comp := le.Uint32(dib[16:])
	clrUsed := int(le.Uint32(dib[32:]))
	if hdrSize < 40 || hdrSize > len(dib) || w <= 0 || h == 0 || w > 32768 || h > 32768 || h < -32768 {
		return nil, errors.New("dib: bad header")
	}
	topDown := h < 0
	if topDown {
		h = -h
	}

	offset := hdrSize
	rMask, gMask, bMask, aMask := uint32(0x00ff0000), uint32(0x0000ff00), uint32(0x000000ff), uint32(0)
	if comp == biBitfields {
		if hdrSize >= 52 {
			rMask, gMask, bMask = le.Uint32(dib[40:]), le.Uint32(dib[44:]), le.Uint32(dib[48:])
			if hdrSize >= 56 {
				aMask = le.Uint32(dib[52:])
			}
		} else {
			if len(dib) < offset+12 {
				return nil, errors.New("dib: missing masks")
			}
			rMask, gMask, bMask = le.Uint32(dib[offset:]), le.Uint32(dib[offset+4:]), le.Uint32(dib[offset+8:])
			offset += 12
		}
	} else if comp != biRGB {
		return nil, errors.New("dib: compressed bitmaps are not supported")
	}

	var palette []color.NRGBA
	if bpp <= 8 {
		n := clrUsed
		if n == 0 {
			n = 1 << bpp
		}
		if len(dib) < offset+4*n {
			return nil, errors.New("dib: short palette")
		}
		for i := 0; i < n; i++ {
			p := dib[offset+4*i:]
			palette = append(palette, color.NRGBA{p[2], p[1], p[0], 255})
		}
		offset += 4 * n
	}

	stride := ((w*bpp + 31) / 32) * 4
	if len(dib) < offset+stride*h {
		return nil, errors.New("dib: short pixel data")
	}
	pix := dib[offset:]
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	anyAlpha := false

	for y := 0; y < h; y++ {
		srcY := h - 1 - y
		if topDown {
			srcY = y
		}
		row := pix[srcY*stride:]
		for x := 0; x < w; x++ {
			var c color.NRGBA
			switch bpp {
			case 32:
				v := le.Uint32(row[x*4:])
				c = color.NRGBA{channel(v, rMask), channel(v, gMask), channel(v, bMask), 255}
				if comp == biRGB {
					c.A = uint8(v >> 24)
				} else if aMask != 0 {
					c.A = channel(v, aMask)
				}
				if c.A != 0 {
					anyAlpha = true
				}
			case 24:
				p := row[x*3:]
				c = color.NRGBA{p[2], p[1], p[0], 255}
			case 16:
				v := uint32(le.Uint16(row[x*2:]))
				if comp == biRGB {
					rMask, gMask, bMask = 0x7c00, 0x03e0, 0x001f
				}
				c = color.NRGBA{channel(v, rMask), channel(v, gMask), channel(v, bMask), 255}
			case 8, 4, 1:
				bit := x * bpp
				idx := int(row[bit/8]>>(8-bpp-bit%8)) & (1<<bpp - 1)
				if idx < len(palette) {
					c = palette[idx]
				}
			default:
				return nil, errors.New("dib: unsupported bit depth")
			}
			img.SetNRGBA(x, y, c)
		}
	}
	// 32-bit bitmaps often leave alpha as 0 to mean "opaque".
	if bpp == 32 && !anyAlpha {
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
	}
	return img, nil
}

func channel(v, mask uint32) uint8 {
	if mask == 0 {
		return 0
	}
	shift := 0
	for mask&1 == 0 {
		mask >>= 1
		shift++
	}
	val := (v >> shift) & mask
	return uint8(val * 255 / mask)
}

// ImageToDIB encodes an image as a 32-bit top-down CF_DIB with a
// BITMAPINFOHEADER. Alpha is composited onto white, because most programs
// that read CF_DIB ignore it; programs that understand alpha read the PNG
// format that is set alongside.
func ImageToDIB(img image.Image) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]byte, 40+w*h*4)
	le := binary.LittleEndian
	le.PutUint32(out[0:], 40)
	le.PutUint32(out[4:], uint32(int32(w)))
	le.PutUint32(out[8:], uint32(int32(-h))) // top-down
	le.PutUint16(out[12:], 1)
	le.PutUint16(out[14:], 32)
	le.PutUint32(out[16:], biRGB)
	le.PutUint32(out[20:], uint32(w*h*4))
	pix := out[40:]
	i := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			a := uint32(c.A)
			blend := func(v uint8) byte { return byte((uint32(v)*a + 255*(255-a)) / 255) }
			pix[i], pix[i+1], pix[i+2], pix[i+3] = blend(c.B), blend(c.G), blend(c.R), 255
			i += 4
		}
	}
	return out
}
