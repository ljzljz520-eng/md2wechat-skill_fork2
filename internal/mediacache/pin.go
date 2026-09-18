package mediacache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

// PinRequest is one published asset set to protect from GC.
type PinRequest struct {
	AccountKey string
	// Source is optional provenance (e.g. source markdown path / draft id).
	Source string
	Items  []ManifestItem
}

// ManifestID deterministically identifies a pin from account + sorted keys.
func ManifestID(accountKey string, items []ManifestItem) (string, error) {
	if accountKey == "" {
		return "", errors.New("mediacache: empty account key")
	}
	if len(items) == 0 {
		return "", errors.New("mediacache: manifest requires at least one item")
	}
	keys := make([]string, len(items))
	for i, item := range items {
		if item.DerivedKey == "" {
			return "", fmt.Errorf("mediacache: manifest item %d missing derived_key", i)
		}
		keys[i] = item.DerivedKey
	}
	sort.Strings(keys)
	h := sha256.New()
	fmt.Fprintf(h, "account=%s\n", accountKey)
	for _, key := range keys {
		_, _ = h.Write([]byte(key))
		_, _ = h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PinManifest writes a manifest idempotently and backfills manifest_pins on
// the corresponding records. Repeating the same item set (in any order)
// returns the same manifest and creates no extra file.
func (s *Service) PinManifest(req PinRequest) (*Manifest, error) {
	id, err := ManifestID(req.AccountKey, req.Items)
	if err != nil {
		return nil, err
	}
	for i, item := range req.Items {
		if item.UploadedBlobDigest == "" || item.MediaID == "" {
			return nil, fmt.Errorf("mediacache: manifest item %d missing digest or media_id", i)
		}
	}

	// Idempotent: an existing manifest with identical items is returned as-is.
	if existing, err := s.store.ReadManifest(id); err == nil {
		if manifestItemsEqual(existing.Items, req.Items) {
			return existing, nil
		}
		// Same key set, refreshed bindings (e.g. re-published): overwrite the
		// snapshot; pins on records are unchanged.
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	items := make([]ManifestItem, len(req.Items))
	copy(items, req.Items)
	sort.Slice(items, func(i, j int) bool { return items[i].DerivedKey < items[j].DerivedKey })

	m := &Manifest{
		ManifestID: id,
		AccountKey: req.AccountKey,
		Source:     req.Source,
		CreatedAt:  s.now(),
		Items:      items,
	}

	mlock, err := s.store.LockManifests()
	if err != nil {
		return nil, err
	}
	defer mlock.unlock()
	if err := s.store.WriteManifest(m); err != nil {
		return nil, err
	}

	// Backfill pins onto records under the index lock.
	ilock, err := s.store.LockIndex()
	if err != nil {
		return nil, err
	}
	defer ilock.unlock()
	idx, err := s.store.ReadIndex()
	if err != nil {
		return nil, err
	}
	changed := false
	for _, item := range items {
		if rec := findRecord(idx, req.AccountKey, item.DerivedKey); rec != nil {
			if rec.AddPin(id) {
				changed = true
			}
		}
	}
	if changed {
		if err := s.store.WriteIndex(idx); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func manifestItemsEqual(a, b []ManifestItem) bool {
	if len(a) != len(b) {
		return false
	}
	// Compare as multisets keyed by derived_key (pins are order-independent).
	seen := make(map[string]ManifestItem, len(a))
	for _, item := range a {
		seen[item.DerivedKey] = item
	}
	for _, item := range b {
		other, ok := seen[item.DerivedKey]
		if !ok || other != item {
			return false
		}
	}
	return true
}

// ListManifests returns all stored manifests (GC / CLI use).
func (s *Service) ListManifests() ([]*Manifest, error) {
	ids, err := s.store.ListManifestIDs()
	if err != nil {
		return nil, err
	}
	out := make([]*Manifest, 0, len(ids))
	for _, id := range ids {
		m, err := s.store.ReadManifest(id)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
