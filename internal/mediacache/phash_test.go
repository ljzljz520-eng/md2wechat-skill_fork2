package mediacache

import (
	"errors"
	"testing"
)

// scaleTolerance bounds the accepted Hamming distance for same-content resizes;
// aligned with the default near-duplicate threshold.
const scaleTolerance = 5

func TestPHashIsDeterministicAndDistinguishesPatterns(t *testing.T) {
	a := mustPNGBytes(t, newPattern(64, 64, "v"))
	b := mustPNGBytes(t, newPattern(64, 64, "h"))

	ha1, err := PHashFromBytes(a)
	if err != nil {
		t.Fatalf("PHashFromBytes(a): %v", err)
	}
	ha2, err := PHashFromBytes(a)
	if err != nil {
		t.Fatalf("PHashFromBytes(a) second: %v", err)
	}
	if ha1 != ha2 {
		t.Fatalf("identical bytes produced different phash: %#x vs %#x", ha1, ha2)
	}
	if PHashVersion == "" {
		t.Fatal("PHashVersion must be non-empty")
	}

	hb, err := PHashFromBytes(b)
	if err != nil {
		t.Fatalf("PHashFromBytes(b): %v", err)
	}
	if ha1 == hb {
		t.Fatalf("distinct patterns hashed equally: %#x", ha1)
	}
}

func TestPHashIsScaleInvariant(t *testing.T) {
	small := mustPNGBytes(t, newPattern(64, 64, "v"))
	large := mustPNGBytes(t, newPattern(300, 300, "v"))

	hs, err := PHashFromBytes(small)
	if err != nil {
		t.Fatalf("small phash: %v", err)
	}
	hl, err := PHashFromBytes(large)
	if err != nil {
		t.Fatalf("large phash: %v", err)
	}
	dist := HammingDistance(hs, hl)
	t.Logf("scaled copies distance = %d", dist)
	if dist > scaleTolerance {
		t.Fatalf("scaled copies distance = %d (> %d): %#x vs %#x", dist, scaleTolerance, hs, hl)
	}
}

func TestPHashGIFUsesFirstFrame(t *testing.T) {
	frame0 := newPattern(64, 64, "v")
	frame1 := newPattern(64, 64, "h")
	gifBytes := animatedGIFBytes(t, frame0, frame1)
	pngFirstFrame := mustPNGBytes(t, frame0)

	hGIF, err := PHashFromBytes(gifBytes)
	if err != nil {
		t.Fatalf("gif phash: %v", err)
	}
	hPNG, err := PHashFromBytes(pngFirstFrame)
	if err != nil {
		t.Fatalf("png phash: %v", err)
	}
	if hGIF != hPNG {
		t.Fatalf("gif first frame phash = %#x, want %#x", hGIF, hPNG)
	}
}

func TestPHashDecodeFailureIsSentinel(t *testing.T) {
	h, err := PHashFromBytes([]byte("definitely not an image"))
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !errors.Is(err, ErrImageDecode) {
		t.Fatalf("error = %v, want wrapping ErrImageDecode", err)
	}
	if h != 0 {
		t.Fatalf("hash = %#x, want 0", h)
	}
}

func TestComputeIdentityDegradesPHashWithoutFailing(t *testing.T) {
	garbage := []byte("definitely not an image")
	id, err := ComputeIdentity(garbage)
	if err != nil {
		t.Fatalf("ComputeIdentity error = %v", err)
	}
	if id.SourceDigest == "" || id.SourceDigest != DigestBytes(garbage) {
		t.Fatalf("source digest = %q", id.SourceDigest)
	}
	if id.PHash != 0 || id.PHashVersion != "" {
		t.Fatalf("undecodable image should have empty phash, got %#x/%q", id.PHash, id.PHashVersion)
	}

	pngBytes := mustPNGBytes(t, newPattern(64, 64, "v"))
	id2, err := ComputeIdentity(pngBytes)
	if err != nil {
		t.Fatalf("ComputeIdentity(png) error = %v", err)
	}
	if id2.PHashVersion != PHashVersion || id2.PHash == 0 {
		t.Fatalf("decodable identity = %+v, want version and non-zero hash", id2)
	}
}
