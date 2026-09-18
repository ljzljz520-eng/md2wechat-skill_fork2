package mediacache

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// GCPlan is a point-in-time garbage collection plan. It is safe to serialize
// and display (dry-run).
type GCPlan struct {
	GeneratedAt     time.Time      `json:"generated_at"`
	GraceDays       int            `json:"grace_days"`
	ScannedBlobs    int            `json:"scanned_blobs"`
	RetainedBlobs   int            `json:"retained_blobs"`
	BlobsToDelete   []BlobDeletion `json:"blobs_to_delete"`
	ThumbsToDelete  []string       `json:"thumbs_to_delete"`
	RecordsToDelete []RecordRef    `json:"records_to_delete"`
}

// BlobDeletion names one unreferenced content blob.
type BlobDeletion struct {
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"size_bytes"`
}

// RecordRef identifies a record by its natural key.
type RecordRef struct {
	AccountKey string `json:"account_key"`
	DerivedKey string `json:"derived_key"`
}

// GCReport summarizes an executed (or dry-run) collection.
type GCReport struct {
	Plan           *GCPlan  `json:"plan"`
	DeletedBlobs   []string `json:"deleted_blobs"`
	DeletedThumbs  []string `json:"deleted_thumbs"`
	DeletedRecords int      `json:"deleted_records"`
	DeletedBytes   int64    `json:"deleted_bytes"`
}

// retentionSet is every digest protected from deletion: surviving records and
// all manifest items, regardless of record status.
type retentionSet struct {
	blobs map[string]bool
}

func (s *Service) buildRetention(survivors []MediaRecord, manifests []*Manifest) retentionSet {
	r := retentionSet{blobs: make(map[string]bool)}
	for _, rec := range survivors {
		r.blobs[rec.SourceDigest] = true
		if rec.UploadedBlobDigest != "" {
			r.blobs[rec.UploadedBlobDigest] = true
		}
	}
	for _, m := range manifests {
		for _, item := range m.Items {
			r.blobs[item.SourceDigest] = true
			if item.UploadedBlobDigest != "" {
				r.blobs[item.UploadedBlobDigest] = true
			}
		}
	}
	return r
}

// PlanGC scans the cache and computes what would be collected.
func (s *Service) PlanGC(graceDays int) (*GCPlan, error) {
	ilock, err := s.store.LockIndex()
	if err != nil {
		return nil, err
	}
	defer ilock.unlock()
	mlock, err := s.store.LockManifests()
	if err != nil {
		return nil, err
	}
	defer mlock.unlock()

	idx, err := s.store.ReadIndex()
	if err != nil {
		return nil, err
	}
	manifests, err := s.ListManifests()
	if err != nil {
		return nil, err
	}

	now := s.now()
	cutoff := now.Add(-time.Duration(graceDays) * 24 * time.Hour)

	var recordsToDelete []RecordRef
	survivors := make([]MediaRecord, 0, len(idx.Records))
	for _, rec := range idx.Records {
		if rec.Status == StatusInvalid && len(rec.ManifestPins) == 0 {
			anchor := rec.LastValidatedAt
			if anchor.IsZero() {
				anchor = rec.CreatedAt
			}
			if !anchor.After(cutoff) {
				recordsToDelete = append(recordsToDelete, RecordRef{
					AccountKey: rec.AccountKey, DerivedKey: rec.DerivedKey,
				})
				continue
			}
		}
		survivors = append(survivors, rec)
	}
	retain := s.buildRetention(survivors, manifests)

	plan := &GCPlan{
		GeneratedAt:     now,
		GraceDays:       graceDays,
		RecordsToDelete: recordsToDelete,
	}

	blobs, err := s.store.listShardedFiles(filepath.Join(s.store.root, dirBlobs), "")
	if err != nil {
		return nil, err
	}
	for _, p := range blobs {
		digest := filepath.Base(p)
		plan.ScannedBlobs++
		if retain.blobs[digest] {
			plan.RetainedBlobs++
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		plan.BlobsToDelete = append(plan.BlobsToDelete, BlobDeletion{
			Digest: digest, SizeBytes: info.Size(),
		})
	}

	thumbs, err := s.store.listShardedFiles(filepath.Join(s.store.root, dirThumbs), ".jpg")
	if err != nil {
		return nil, err
	}
	for _, p := range thumbs {
		name := filepath.Base(p)
		sourceDigest := name[:len(name)-len(".jpg")]
		if !retain.blobs[sourceDigest] {
			plan.ThumbsToDelete = append(plan.ThumbsToDelete, sourceDigest)
		}
	}

	sort.Slice(plan.BlobsToDelete, func(i, j int) bool {
		return plan.BlobsToDelete[i].Digest < plan.BlobsToDelete[j].Digest
	})
	sort.Strings(plan.ThumbsToDelete)
	return plan, nil
}

// listShardedFiles lists files under aa/ shards, verifying each filename is a
// 64-hex digest (plus optional suffix).
func (s *Store) listShardedFiles(dir, suffix string) ([]string, error) {
	var out []string
	shards, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, shard := range shards {
		if !shard.IsDir() || len(shard.Name()) != 2 {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(dir, shard.Name()))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if suffix != "" {
				if filepath.Ext(name) != suffix {
					continue
				}
				name = name[:len(name)-len(suffix)]
			}
			if err := validateDigest(name); err != nil {
				continue // never touch files we cannot identify
			}
			out = append(out, filepath.Join(dir, shard.Name(), entry.Name()))
		}
	}
	return out, nil
}

// ExecuteGC applies a plan. With yes=false nothing is deleted (dry run). With
// yes=true, retention and record eligibility are recomputed at execution time
// so a pin created after planning still protects its blob.
func (s *Service) ExecuteGC(plan *GCPlan, yes bool) (*GCReport, error) {
	if plan == nil {
		return nil, errors.New("mediacache: nil gc plan")
	}
	report := &GCReport{Plan: plan}
	if !yes {
		return report, nil
	}

	// Fresh snapshot: never delete based on a stale plan.
	fresh, err := s.PlanGC(plan.GraceDays)
	if err != nil {
		return nil, err
	}
	safeBlobs := make(map[string]bool, len(fresh.BlobsToDelete))
	var safeBytes int64
	plannedBlobSizes := make(map[string]int64, len(plan.BlobsToDelete))
	for _, b := range plan.BlobsToDelete {
		plannedBlobSizes[b.Digest] = b.SizeBytes
	}
	for _, b := range fresh.BlobsToDelete {
		if _, planned := plannedBlobSizes[b.Digest]; planned {
			safeBlobs[b.Digest] = true
			safeBytes += b.SizeBytes
		}
	}
	safeThumbs := make(map[string]bool, len(fresh.ThumbsToDelete))
	for _, digest := range fresh.ThumbsToDelete {
		for _, planned := range plan.ThumbsToDelete {
			if digest == planned {
				safeThumbs[digest] = true
				break
			}
		}
	}
	safeRecords := make(map[RecordRef]bool, len(fresh.RecordsToDelete))
	for _, ref := range fresh.RecordsToDelete {
		for _, planned := range plan.RecordsToDelete {
			if ref == planned {
				safeRecords[ref] = true
				break
			}
		}
	}

	for digest := range safeBlobs {
		if err := s.store.DeleteBlob(digest); err != nil {
			return report, err
		}
		report.DeletedBlobs = append(report.DeletedBlobs, digest)
	}
	for digest := range safeThumbs {
		p, err := s.store.ThumbPath(digest)
		if err != nil {
			return report, err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return report, err
		}
		report.DeletedThumbs = append(report.DeletedThumbs, digest)
	}
	if len(safeRecords) > 0 {
		if err := s.deleteRecords(safeRecords); err != nil {
			return report, err
		}
		report.DeletedRecords = len(safeRecords)
	}
	report.DeletedBytes = safeBytes
	sort.Strings(report.DeletedBlobs)
	sort.Strings(report.DeletedThumbs)
	return report, nil
}

func (s *Service) deleteRecords(remove map[RecordRef]bool) error {
	lock, err := s.store.LockIndex()
	if err != nil {
		return err
	}
	defer lock.unlock()
	idx, err := s.store.ReadIndex()
	if err != nil {
		return err
	}
	kept := idx.Records[:0]
	for _, rec := range idx.Records {
		ref := RecordRef{AccountKey: rec.AccountKey, DerivedKey: rec.DerivedKey}
		if remove[ref] {
			continue
		}
		kept = append(kept, rec)
	}
	idx.Records = kept
	return s.store.WriteIndex(idx)
}
