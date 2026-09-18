package mediacache

import (
	"bytes"
	"fmt"
	"image/jpeg"

	"github.com/disintegration/imaging"
)

const (
	thumbWidth       = 256 // 缩略参考图固定宽度
	thumbJPEGQuality = 70
)

// Identity is the content identity of an original asset.
type Identity struct {
	SourceDigest string `json:"source_digest"`
	PHash        uint64 `json:"phash"`
	PHashVersion string `json:"phash_version,omitempty"`
}

// ComputeIdentity calculates the original blob digest and best-effort pHash.
// A pHash failure (undecodable bytes) leaves PHash empty/version empty but
// never returns an error: the digest remains the primary identity.
func ComputeIdentity(original []byte) (*Identity, error) {
	id := &Identity{SourceDigest: DigestBytes(original)}
	if phash, err := PHashFromBytes(original); err == nil {
		id.PHash = phash
		id.PHashVersion = PHashVersion
	}
	return id, nil
}

// GenerateThumb creates the fixed-width JPEG thumbnail reference image.
func GenerateThumb(original []byte) ([]byte, error) {
	img, err := decodeImage(original)
	if err != nil {
		return nil, err
	}
	if img.Bounds().Dx() > thumbWidth {
		img = imaging.Resize(img, thumbWidth, 0, imaging.Lanczos)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: thumbJPEGQuality}); err != nil {
		return nil, fmt.Errorf("encode thumb: %w", err)
	}
	return buf.Bytes(), nil
}
