package mediacache

import "time"

// RecordStatus is the local validity state of an uploaded WeChat asset.
type RecordStatus string

const (
	// StatusValid means the media_id was uploaded or probed valid within TTL.
	StatusValid RecordStatus = "valid"
	// StatusExpired means the TTL elapsed; the next resolve must probe.
	StatusExpired RecordStatus = "expired"
	// StatusInvalid means WeChat confirmed the media_id gone or upload-side
	// rejection; the record is kept until GC after the grace period.
	StatusInvalid RecordStatus = "invalid"
)

// Reference records who uses a source blob (e.g. source document path).
type Reference struct {
	Source  string    `json:"source"`
	AddedAt time.Time `json:"added_at"`
}

// MediaRecord is one cached upload, scoped to one WeChat account and one
// (source digest, process spec) derived key.
type MediaRecord struct {
	DerivedKey         string       `json:"derived_key"`
	AccountKey         string       `json:"account_key"`
	SourceDigest       string       `json:"source_digest"`
	SourcePHash        uint64       `json:"source_phash,omitempty"`
	PHashVersion       string       `json:"phash_version,omitempty"`
	ProcessSpec        ProcessSpec  `json:"process_spec"`
	UploadedBlobDigest string       `json:"uploaded_blob_digest"`
	MediaID            string       `json:"media_id"`
	WechatURL          string       `json:"wechat_url"`
	CreatedAt          time.Time    `json:"created_at"`
	LastValidatedAt    time.Time    `json:"last_validated_at"`
	Status             RecordStatus `json:"status"`
	InvalidReason      string       `json:"invalid_reason,omitempty"`
	References         []Reference  `json:"references,omitempty"`
	ManifestPins       []string     `json:"manifest_pins,omitempty"`
}

// AddReference appends source if not already tracked.
func (r *MediaRecord) AddReference(source string, now time.Time) bool {
	if source == "" {
		return false
	}
	for _, ref := range r.References {
		if ref.Source == source {
			return false
		}
	}
	r.References = append(r.References, Reference{Source: source, AddedAt: now})
	return true
}

// AddPin records a manifest that protects this record from GC.
func (r *MediaRecord) AddPin(manifestID string) bool {
	if manifestID == "" {
		return false
	}
	for _, pin := range r.ManifestPins {
		if pin == manifestID {
			return false
		}
	}
	r.ManifestPins = append(r.ManifestPins, manifestID)
	return true
}

// HasPin reports whether manifestID pins this record.
func (r *MediaRecord) HasPin(manifestID string) bool {
	for _, pin := range r.ManifestPins {
		if pin == manifestID {
			return true
		}
	}
	return false
}
