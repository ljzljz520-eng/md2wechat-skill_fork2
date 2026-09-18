package mediacache

import (
	"bytes"
	"image/jpeg"
	"testing"
)

func TestGenerateThumbResizesWideImages(t *testing.T) {
	src := mustPNGBytes(t, newPattern(600, 300, "v"))
	thumb, err := GenerateThumb(src)
	if err != nil {
		t.Fatalf("GenerateThumb: %v", err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(thumb))
	if err != nil {
		t.Fatalf("thumb is not decodable jpeg: %v", err)
	}
	if cfg.Width != 256 {
		t.Fatalf("thumb width = %d, want 256", cfg.Width)
	}
	if cfg.Height != 128 {
		t.Fatalf("thumb height = %d, want 128", cfg.Height)
	}
}

func TestGenerateThumbKeepsSmallImagesWidth(t *testing.T) {
	src := mustPNGBytes(t, newPattern(120, 60, "v"))
	thumb, err := GenerateThumb(src)
	if err != nil {
		t.Fatalf("GenerateThumb: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(thumb))
	if err != nil {
		t.Fatalf("thumb decode: %v", err)
	}
	if img.Bounds().Dx() != 120 {
		t.Fatalf("thumb width = %d, want 120", img.Bounds().Dx())
	}
}

func TestGenerateThumbRejectsUndecodableBytes(t *testing.T) {
	if _, err := GenerateThumb([]byte("not-an-image")); err == nil {
		t.Fatal("expected error for undecodable bytes")
	}
}
