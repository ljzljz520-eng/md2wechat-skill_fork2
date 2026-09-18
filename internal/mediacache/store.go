package mediacache

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/geekjourneyx/md2wechat-skill/internal/atomicfile"
)

const (
	dirBlobs     = "blobs"
	dirThumbs    = "thumbs"
	dirManifests = "manifests"
	dirLocks     = "locks"
	indexName    = "index.json"
	indexLock    = "index.lock"
	manifestLock = "manifest.lock"

	indexVersion                = 1
	defaultLockWait             = 10 * time.Second
	dirMode         os.FileMode = 0o700
	fileMode        os.FileMode = 0o600
)

// ErrNotFound means the requested cache object does not exist.
var ErrNotFound = errors.New("mediacache: not found")

// Index is the on-disk record catalog for one cache root.
type Index struct {
	Version int           `json:"version"`
	Records []MediaRecord `json:"records"`
}

// Store is the filesystem-backed media cache for all WeChat accounts.
type Store struct {
	root string
}

// Open creates or opens a cache root with private (0700) permissions.
func Open(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("mediacache: empty cache directory")
	}
	s := &Store{root: root}
	for _, dir := range []string{"", dirBlobs, dirThumbs, dirManifests, dirLocks} {
		p := filepath.Join(root, dir)
		if err := os.MkdirAll(p, dirMode); err != nil {
			return nil, fmt.Errorf("mediacache: create %s: %w", p, err)
		}
		if err := os.Chmod(p, dirMode); err != nil && !errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("mediacache: chmod %s: %w", p, err)
		}
	}
	if _, err := os.Stat(s.indexPath()); errors.Is(err, os.ErrNotExist) {
		if err := s.WriteIndex(&Index{Version: indexVersion}); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Root returns the cache root directory.
func (s *Store) Root() string { return s.root }

func (s *Store) indexPath() string { return filepath.Join(s.root, indexName) }

// LockIndex acquires the cross-process catalog lock.
func (s *Store) LockIndex() (*fileLock, error) {
	return acquireLock(filepath.Join(s.root, dirLocks, indexLock), defaultLockWait)
}

// LockManifests acquires the cross-process manifest lock.
func (s *Store) LockManifests() (*fileLock, error) {
	return acquireLock(filepath.Join(s.root, dirLocks, manifestLock), defaultLockWait)
}

// ReadIndex loads the catalog without taking a lock; callers needing
// read-modify-write consistency must hold LockIndex.
func (s *Store) ReadIndex() (*Index, error) {
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		return nil, fmt.Errorf("mediacache: read index: %w", err)
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("mediacache: corrupt index %s: %w", s.indexPath(), err)
	}
	if idx.Version != indexVersion {
		return nil, fmt.Errorf("mediacache: unsupported index version %d", idx.Version)
	}
	return &idx, nil
}

// WriteIndex atomically replaces the catalog.
func (s *Store) WriteIndex(idx *Index) error {
	if idx == nil {
		return errors.New("mediacache: nil index")
	}
	idx.Version = indexVersion
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	if _, err := atomicfile.Write(s.indexPath(), data); err != nil {
		return fmt.Errorf("mediacache: write index: %w", err)
	}
	return os.Chmod(s.indexPath(), fileMode)
}

// validateDigest enforces 64 lowercase hex chars (SHA-256 hex).
func validateDigest(digest string) error {
	if len(digest) != 64 {
		return fmt.Errorf("mediacache: invalid digest length %d", len(digest))
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("mediacache: invalid digest %q: %w", digest, err)
	}
	return nil
}

// BlobPath returns the sharded path for a content blob.
func (s *Store) BlobPath(digest string) (string, error) {
	if err := validateDigest(digest); err != nil {
		return "", err
	}
	return filepath.Join(s.root, dirBlobs, digest[:2], digest), nil
}

// PutBlob stores data under digest. Idempotent: an existing blob with the same
// length is left untouched (content-addressed).
func (s *Store) PutBlob(digest string, data []byte) error {
	p, err := s.BlobPath(digest)
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(p); statErr == nil && info.Size() == int64(len(data)) {
		return nil
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("mediacache: stat blob: %w", statErr)
	}
	if err := os.MkdirAll(filepath.Dir(p), dirMode); err != nil {
		return err
	}
	if err := os.WriteFile(p, data, fileMode); err != nil {
		return fmt.Errorf("mediacache: write blob: %w", err)
	}
	return nil
}

// ReadBlob returns the blob bytes.
func (s *Store) ReadBlob(digest string) ([]byte, error) {
	p, err := s.BlobPath(digest)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, digest)
	}
	return data, err
}

// HasBlob reports blob existence.
func (s *Store) HasBlob(digest string) (bool, error) {
	p, err := s.BlobPath(digest)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

// DeleteBlob removes a blob; missing blobs are not an error.
func (s *Store) DeleteBlob(digest string) error {
	p, err := s.BlobPath(digest)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("mediacache: delete blob: %w", err)
	}
	return nil
}

// ThumbPath returns the sharded path for a 256px reference thumbnail.
func (s *Store) ThumbPath(sourceDigest string) (string, error) {
	if err := validateDigest(sourceDigest); err != nil {
		return "", err
	}
	return filepath.Join(s.root, dirThumbs, sourceDigest[:2], sourceDigest+".jpg"), nil
}

// PutThumb stores a JPEG reference thumbnail keyed by source digest.
func (s *Store) PutThumb(sourceDigest string, jpeg []byte) error {
	p, err := s.ThumbPath(sourceDigest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), dirMode); err != nil {
		return err
	}
	if err := os.WriteFile(p, jpeg, fileMode); err != nil {
		return fmt.Errorf("mediacache: write thumb: %w", err)
	}
	return nil
}

// HasThumb reports thumbnail existence.
func (s *Store) HasThumb(sourceDigest string) (bool, error) {
	p, err := s.ThumbPath(sourceDigest)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

// ManifestPath returns the path of a manifest file.
func (s *Store) ManifestPath(manifestID string) (string, error) {
	if err := validateDigest(manifestID); err != nil {
		return "", err
	}
	return filepath.Join(s.root, dirManifests, manifestID+".json"), nil
}

// WriteManifest atomically stores a manifest.
func (s *Store) WriteManifest(m *Manifest) error {
	if m == nil {
		return errors.New("mediacache: nil manifest")
	}
	p, err := s.ManifestPath(m.ManifestID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if _, err := atomicfile.Write(p, data); err != nil {
		return fmt.Errorf("mediacache: write manifest: %w", err)
	}
	return os.Chmod(p, fileMode)
}

// ReadManifest loads one manifest.
func (s *Store) ReadManifest(manifestID string) (*Manifest, error) {
	p, err := s.ManifestPath(manifestID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: manifest %s", ErrNotFound, manifestID)
	}
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("mediacache: corrupt manifest %s: %w", manifestID, err)
	}
	return &m, nil
}

// ListManifestIDs returns all manifest IDs on disk.
func (s *Store) ListManifestIDs() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, dirManifests))
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		ids = append(ids, name[:len(name)-len(".json")])
	}
	return ids, nil
}

// DeleteManifest removes a manifest file; missing is not an error.
func (s *Store) DeleteManifest(manifestID string) error {
	p, err := s.ManifestPath(manifestID)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("mediacache: delete manifest: %w", err)
	}
	return nil
}
