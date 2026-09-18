package mediacache

import (
	"testing"

	"github.com/geekjourneyx/md2wechat-skill/internal/config"
)

func TestDerivedKeyParameterMatrix(t *testing.T) {
	const sourceDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	baseline := ProcessSpec{
		PipelineKind:     PipelineContent,
		CompressEnabled:  true,
		MaxWidth:         1920,
		MaxSizeBytes:     5 * 1024 * 1024,
		JPEGQuality:      DefaultJPEGQuality,
		OutputFormat:     FormatAuto,
		Crop:             NoCrop(),
		ProcessorVersion: ProcessorVersion,
	}
	baseKey, err := baseline.DerivedKey(sourceDigest)
	if err != nil {
		t.Fatalf("baseline DerivedKey: %v", err)
	}
	// Baseline repeats → identical key.
	if k, _ := baseline.DerivedKey(sourceDigest); k != baseKey {
		t.Fatalf("baseline keys not stable: %q vs %q", k, baseKey)
	}

	variants := map[string]ProcessSpec{
		"pipeline_kind":    withSpec(baseline, func(s *ProcessSpec) { s.PipelineKind = PipelineCover }),
		"compress_enabled": withSpec(baseline, func(s *ProcessSpec) { s.CompressEnabled = false }),
		"max_width":        withSpec(baseline, func(s *ProcessSpec) { s.MaxWidth = 1080 }),
		"max_size_bytes":   withSpec(baseline, func(s *ProcessSpec) { s.MaxSizeBytes = 1024 * 1024 }),
		"jpeg_quality":     withSpec(baseline, func(s *ProcessSpec) { s.JPEGQuality = 70 }),
		"output_format":    withSpec(baseline, func(s *ProcessSpec) { s.OutputFormat = FormatJPEG }),
		"crop": withSpec(baseline, func(s *ProcessSpec) {
			s.Crop = CropSpec{Enabled: true, X: 1, Y: 2, Width: 100, Height: 200}
		}),
		"processor_version": withSpec(baseline, func(s *ProcessSpec) { s.ProcessorVersion = "proc-v2" }),
	}

	for name, variant := range variants {
		t.Run(name, func(t *testing.T) {
			key, err := variant.DerivedKey(sourceDigest)
			if err != nil {
				t.Fatalf("variant %s DerivedKey: %v", name, err)
			}
			if key == baseKey {
				t.Fatalf("variant %s reused baseline key", name)
			}
		})
	}

	// Different source digest with the same spec → different key.
	if k, _ := baseline.DerivedKey(sourceDigest[:62] + "00"); k == baseKey {
		t.Fatal("key did not distinguish source digest")
	}
}

func withSpec(spec ProcessSpec, mutate func(*ProcessSpec)) ProcessSpec {
	copy := spec
	mutate(&copy)
	return copy
}

func TestCanonicalJSONIsStable(t *testing.T) {
	spec := ForContent(&config.Config{
		CompressImages: true,
		MaxImageWidth:  1920,
		MaxImageSize:   5 * 1024 * 1024,
	})
	first, err := spec.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	second, err := spec.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON second: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("canonical json not stable:\n%s\nvs\n%s", first, second)
	}
}

func TestForContentAndForCoverMapConfig(t *testing.T) {
	cfg := &config.Config{
		CompressImages: true,
		MaxImageWidth:  1280,
		MaxImageSize:   3 * 1024 * 1024,
	}

	content := ForContent(cfg)
	if content.PipelineKind != PipelineContent ||
		!content.CompressEnabled ||
		content.MaxWidth != 1280 ||
		content.MaxSizeBytes != 3*1024*1024 ||
		content.JPEGQuality != DefaultJPEGQuality ||
		content.OutputFormat != FormatAuto ||
		content.Crop != NoCrop() ||
		content.ProcessorVersion != ProcessorVersion {
		t.Fatalf("ForContent mapping wrong: %+v", content)
	}

	cover := ForCover(cfg)
	if cover.PipelineKind != PipelineCover ||
		cover.CompressEnabled ||
		cover.MaxWidth != 0 ||
		cover.MaxSizeBytes != 0 ||
		cover.JPEGQuality != 0 {
		t.Fatalf("ForCover must be uncompressed raw upload: %+v", cover)
	}

	// nil config must not panic and must produce a valid spec.
	_ = ForContent(nil)
	_ = ForCover(nil)
}
