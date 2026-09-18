package mediacache

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
)

// BlobDigest computes the SHA-256 hex digest of the original blob bytes read
// from r. Identical bytes always produce the same digest, independent of file
// name, path or modification time.
func BlobDigest(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DigestBytes returns the SHA-256 hex digest of data.
func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
