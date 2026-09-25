package clipboard

import (
	"encoding/binary"
	"image"
	"image/color"
	"testing"
)

func testImage() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	img.SetNRGBA(0, 0, color.NRGBA{255, 0, 0, 255})
	img.SetNRGBA(1, 0, color.NRGBA{0, 255, 0, 255})
	img.SetNRGBA(2, 0, color.NRGBA{0, 0, 255, 255})
	img.SetNRGBA(0, 1, color.NRGBA{10, 20, 30, 255})
	img.SetNRGBA(1, 1, color.NRGBA{200, 100, 50, 255})
	img.SetNRGBA(2, 1, color.NRGBA{0, 0, 0, 0}) // transparent -> white in DIB
	return img
}

func TestDIBRoundTrip(t *testing.T) {
	src := testImage()
	got, err := DIBToImage(ImageToDIB(src))
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			want := src.NRGBAAt(x, y)
			if want.A == 0 {
				want = color.NRGBA{255, 255, 255, 255}
			}
			if c := color.NRGBAModel.Convert(got.At(x, y)); c != want {
				t.Errorf("(%d,%d) = %v, want %v", x, y, c, want)
			}
		}
	}
}

// A classic bottom-up 24-bit DIB, as Windows screenshots used to produce.
func TestDIB24BottomUp(t *testing.T) {
	w, h := 2, 2
	stride := 8 // 2*3 = 6 rounded up to 8
	dib := make([]byte, 40+stride*h)
	le := binary.LittleEndian
	le.PutUint32(dib[0:], 40)
	le.PutUint32(dib[4:], uint32(w))
	le.PutUint32(dib[8:], uint32(h))
	le.PutUint16(dib[12:], 1)
	le.PutUint16(dib[14:], 24)
	// Bottom row first: (0,1)=blue.
	copy(dib[40:], []byte{255, 0, 0})
	// Top row: (0,0)=red.
	copy(dib[40+stride:], []byte{0, 0, 255})
	img, err := DIBToImage(dib)
	if err != nil {
		t.Fatal(err)
	}
	if c := color.NRGBAModel.Convert(img.At(0, 0)); c != (color.NRGBA{255, 0, 0, 255}) {
		t.Errorf("top-left = %v", c)
	}
	if c := color.NRGBAModel.Convert(img.At(0, 1)); c != (color.NRGBA{0, 0, 255, 255}) {
		t.Errorf("bottom-left = %v", c)
	}
}

func TestToPNG(t *testing.T) {
	p, err := EncodePNG(testImage())
	if err != nil {
		t.Fatal(err)
	}
	out, img, err := ToPNG(p)
	if err != nil || img.Bounds().Dx() != 3 || &out[0] != &p[0] {
		t.Fatal("PNG should pass through unchanged")
	}
	if _, _, err := ToPNG([]byte("not an image")); err == nil {
		t.Fatal("garbage decoded")
	}
}
