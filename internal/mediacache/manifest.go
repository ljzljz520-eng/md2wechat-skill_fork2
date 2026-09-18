package mediacache

import "time"

// ManifestItem pins one asset line in a published manifest.
type ManifestItem struct {
	DerivedKey         string `json:"derived_key"`
	SourceDigest       string `json:"source_digest"`
	UploadedBlobDigest string `json:"uploaded_blob_digest"`
	MediaID            string `json:"media_id"`
	WechatURL          string `json:"wechat_url"`
}

// Manifest is a published set of assets that GC must never delete while the
// manifest exists.
type Manifest struct {
	ManifestID string         `json:"manifest_id"`
	AccountKey string         `json:"account_key"`
	Source     string         `json:"source,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	Items      []ManifestItem `json:"items"`
}
