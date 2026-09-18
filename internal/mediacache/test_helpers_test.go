package mediacache

import (
	"bytes"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/png"
	"testing"
)

// newPattern builds a black/white image.
//
//	"v" (vertical split): left half white, right half black
//	"h" (horizontal split): top half white, bottom half black
//	"blank": all white
//	"noise": deterministic pseudo-random checker noise
func newPattern(w, h int, mode string) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	seed := uint32(0x1234)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			setWhite := false
			switch mode {
			case "v":
				setWhite = x < w/2
			case "h":
				setWhite = y < h/2
			case "blank":
				setWhite = true
			case "noise":
				seed = seed*1103515245 + 12345
				setWhite = (seed>>16)&1 == 1
			}
			if setWhite {
				img.Set(x, y, white)
			}
		}
	}
	return img
}

func mustPNGBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func toPaletted(img image.Image) *image.Paletted {
	p := image.NewPaletted(img.Bounds(), palette.Plan9)
	draw.Draw(p, p.Bounds(), img, img.Bounds().Min, draw.Src)
	return p
}

// animatedGIFBytes builds a 2-frame gif whose first frame is frame0.
func animatedGIFBytes(t *testing.T, frame0, frame1 image.Image) []byte {
	t.Helper()
	g := &gif.GIF{
		Image: []*image.Paletted{toPaletted(frame0), toPaletted(frame1)},
		Delay: []int{0, 0},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	return buf.Bytes()
}
