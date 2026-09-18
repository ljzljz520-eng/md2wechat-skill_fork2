package mediacache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/geekjourneyx/md2wechat-skill/internal/config"
)

// PipelineKind identifies where in the publish flow the asset is uploaded.
type PipelineKind string

const (
	// PipelineContent is an in-article image processed by image.Processor.
	PipelineContent PipelineKind = "content"
	// PipelineCover is a draft cover (currently uploaded uncompressed).
	PipelineCover PipelineKind = "cover"
)

// OutputFormat selects the encoded output format. "auto" follows the compressor
// rule: png/jpeg keep their container, everything else becomes jpeg.
type OutputFormat string

const (
	FormatAuto OutputFormat = "auto"
	FormatJPEG OutputFormat = "jpeg"
	FormatPNG  OutputFormat = "png"
)

// CropSpec captures crop parameters. The current pipeline never crops, so new
// content always uses NoCrop; the field exists so a future crop bumps the key.
type CropSpec struct {
	Enabled bool `json:"enabled"`
	X       int  `json:"x,omitempty"`
	Y       int  `json:"y,omitempty"`
	Width   int  `json:"width,omitempty"`
	Height  int  `json:"height,omitempty"`
}

// NoCrop returns the empty, deterministic no-crop specification.
func NoCrop() CropSpec { return CropSpec{} }

// ProcessSpec is the complete, normalized description of how an original asset
// is transformed before upload. Every field participates in the derived key.
type ProcessSpec struct {
	PipelineKind     PipelineKind `json:"pipeline_kind"`
	CompressEnabled  bool         `json:"compress_enabled"`
	MaxWidth         int          `json:"max_width"`
	MaxSizeBytes     int64        `json:"max_size_bytes"`
	JPEGQuality      int          `json:"jpeg_quality"`
	OutputFormat     OutputFormat `json:"output_format"`
	Crop             CropSpec     `json:"crop"`
	ProcessorVersion string       `json:"processor_version"`
}

// ProcessorVersion tags the processing rules. Any behavior change must bump it.
const ProcessorVersion = "proc-v1"

// DefaultJPEGQuality is the compressor's current fixed JPEG quality.
const DefaultJPEGQuality = 85

// ForContent builds the ProcessSpec for in-article images from the current
// configuration and compressor behavior.
func ForContent(cfg *config.Config) ProcessSpec {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return ProcessSpec{
		PipelineKind:     PipelineContent,
		CompressEnabled:  cfg.CompressImages,
		MaxWidth:         cfg.MaxImageWidth,
		MaxSizeBytes:     cfg.MaxImageSize,
		JPEGQuality:      DefaultJPEGQuality,
		OutputFormat:     FormatAuto,
		Crop:             NoCrop(),
		ProcessorVersion: ProcessorVersion,
	}
}

// ForCover builds the ProcessSpec for draft covers. Covers preserve the current
// uncompressed direct-upload semantics.
func ForCover(cfg *config.Config) ProcessSpec {
	spec := ForContent(cfg)
	spec.PipelineKind = PipelineCover
	spec.CompressEnabled = false
	spec.MaxWidth = 0
	spec.MaxSizeBytes = 0
	spec.JPEGQuality = 0
	return spec
}

// CanonicalJSON serializes the spec with the struct's fixed field order; the
// bytes are stable across runs and Go versions (encoding/json guarantees order
// for struct fields).
func (s ProcessSpec) CanonicalJSON() ([]byte, error) {
	return json.Marshal(s)
}

// DerivedKey returns the cache key: SHA-256 over the original blob digest and
// the canonical process spec. Identical source bytes + identical spec → same
// key; any parameter change → different key.
func (s ProcessSpec) DerivedKey(sourceDigest string) (string, error) {
	canonical, err := s.CanonicalJSON()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, _ = h.Write([]byte(sourceDigest))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil)), nil
}
