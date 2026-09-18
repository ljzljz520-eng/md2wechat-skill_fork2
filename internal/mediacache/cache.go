package mediacache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ProbeFunc asks WeChat whether a permanent-material media_id still exists.
// Implementations must follow the conservative convention:
// (true, nil) confirmed exists; (false, nil) confirmed gone;
// (false, err) unknown — callers must not treat it as invalid.
type ProbeFunc func(ctx context.Context, mediaID string) (bool, error)

// UploadFunc uploads processed bytes to WeChat and returns the new binding.
type UploadFunc func(ctx context.Context, payload []byte) (UploadBinding, error)

// UploadBinding is the minimal WeChat upload result the cache needs.
type UploadBinding struct {
	MediaID   string
	WechatURL string
}

// Options configures a Service.
type Options struct {
	// TTLDays trusts a cached media_id without probing; 0 probes every time.
	TTLDays int
	// NearDuplicateThreshold is the maximum Hamming distance for an advisory
	// (64-bit pHash); 0 disables advisories.
	NearDuplicateThreshold int
	Probe                  ProbeFunc
	Upload                 UploadFunc
	// Now overrides the clock for tests.
	Now func() time.Time
}

// Service resolves assets against the cache and owns single-flight.
type Service struct {
	store     *Store
	ttl       time.Duration
	threshold int
	probe     ProbeFunc
	upload    UploadFunc
	now       func() time.Time

	inflightMu sync.Mutex
	inflight   map[string]*inflightCall
}

type inflightCall struct {
	done   chan struct{}
	result *ResolveResult
	err    error
}

// NewService wires a cache Service. Probe/Upload may be nil for read-only use
// (Resolve will fail closed if it needs the missing seam).
func NewService(store *Store, opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:     store,
		ttl:       time.Duration(opts.TTLDays) * 24 * time.Hour,
		threshold: opts.NearDuplicateThreshold,
		probe:     opts.Probe,
		upload:    opts.Upload,
		now:       now,
		inflight:  make(map[string]*inflightCall),
	}
}

// ResolveRequest carries one asset resolution.
type ResolveRequest struct {
	AccountKey      string
	Spec            ProcessSpec
	Original        []byte // pre-processing bytes
	Processed       []byte // bytes that will actually be uploaded
	ReferenceSource string // optional caller-side reference (e.g. source doc)
}

// Advisory is a non-blocking near-duplicate hint.
type Advisory struct {
	NearDuplicate      bool
	Distance           int
	ExistingDerivedKey string
	ExistingMediaID    string
}

// ResolveResult is the resolved WeChat binding.
type ResolveResult struct {
	MediaID    string
	WechatURL  string
	CacheHit   bool // served from an unprobed TTL-fresh record
	Reuploaded bool // an invalid/expired-on-server media_id was replaced
	DerivedKey string
	Advisory   Advisory
}

var (
	// ErrResolveMissUploadMissing is returned when a miss needs uploading but
	// no upload seam is configured.
	ErrResolveMissUploadMissing = errors.New("mediacache: cache miss but no uploader configured")
	// ErrProbeUnavailable is returned when a TTL-expired media_id cannot be
	// probed; callers must not silently re-upload or silently publish.
	ErrProbeUnavailable = errors.New("mediacache: media_id validation unavailable")
)

// Resolve returns a usable media_id for (account, source, process spec):
// TTL-fresh record → zero network; expired → probe then reupload if gone;
// missing/invalid → upload once. Concurrent identical requests share one
// in-process upload.
func (s *Service) Resolve(ctx context.Context, req ResolveRequest) (*ResolveResult, error) {
	if req.AccountKey == "" {
		return nil, errors.New("mediacache: empty account key")
	}
	if len(req.Original) == 0 {
		return nil, errors.New("mediacache: empty original bytes")
	}
	if len(req.Processed) == 0 {
		return nil, errors.New("mediacache: empty processed bytes")
	}
	identity, err := ComputeIdentity(req.Original)
	if err != nil {
		return nil, err
	}
	sourceDigest := identity.SourceDigest
	derivedKey, err := req.Spec.DerivedKey(sourceDigest)
	if err != nil {
		return nil, err
	}
	sfKey := req.AccountKey + "|" + derivedKey

	return s.singleFlight(sfKey, func() (*ResolveResult, error) {
		return s.resolve(ctx, req, identity, derivedKey)
	}, ctx)
}

func (s *Service) singleFlight(key string, leader func() (*ResolveResult, error), ctx context.Context) (*ResolveResult, error) {
	s.inflightMu.Lock()
	if call, ok := s.inflight[key]; ok {
		s.inflightMu.Unlock()
		select {
		case <-call.done:
			return call.result, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &inflightCall{done: make(chan struct{})}
	s.inflight[key] = call
	s.inflightMu.Unlock()

	call.result, call.err = leader()
	close(call.done)

	s.inflightMu.Lock()
	delete(s.inflight, key)
	s.inflightMu.Unlock()
	return call.result, call.err
}

func (s *Service) resolve(ctx context.Context, req ResolveRequest, identity *Identity, derivedKey string) (*ResolveResult, error) {
	now := s.now()

	// Phase 1: read candidate and gather a near-duplicate advisory.
	lock, err := s.store.LockIndex()
	if err != nil {
		return nil, err
	}
	idx, err := s.store.ReadIndex()
	if err != nil {
		_ = lock.unlock()
		return nil, err
	}
	candidate := findRecord(idx, req.AccountKey, derivedKey)
	advisory := s.nearDuplicateAdvisory(idx, req.AccountKey, *identity)
	referenceAdded := false
	if candidate != nil {
		referenceAdded = candidate.AddReference(req.ReferenceSource, now)
	}
	_ = lock.unlock()

	switch {
	case candidate != nil && candidate.Status == StatusValid && !s.isExpired(candidate, now):
		if referenceAdded {
			if err := s.persistRecord(candidate); err != nil {
				return nil, err
			}
		}
		return &ResolveResult{
			MediaID: candidate.MediaID, WechatURL: candidate.WechatURL,
			CacheHit: true, DerivedKey: derivedKey, Advisory: advisory,
		}, nil
	case candidate != nil && (candidate.Status == StatusExpired ||
		(candidate.Status == StatusValid && s.isExpired(candidate, now))):
		exists, probeErr := s.probeRemote(ctx, candidate.MediaID)
		if probeErr != nil {
			return nil, probeErr
		}
		if exists {
			candidate.Status = StatusValid
			candidate.LastValidatedAt = now
			candidate.AddReference(req.ReferenceSource, now)
			if err := s.persistRecord(candidate); err != nil {
				return nil, err
			}
			return &ResolveResult{
				MediaID: candidate.MediaID, WechatURL: candidate.WechatURL,
				CacheHit: true, DerivedKey: derivedKey, Advisory: advisory,
			}, nil
		}
		// Confirmed gone: upload and replace.
		return s.uploadAndStore(ctx, req, identity, derivedKey, advisory, candidate, "probe: media_id confirmed missing", now)
	case candidate != nil && candidate.Status == StatusInvalid:
		return s.uploadAndStore(ctx, req, identity, derivedKey, advisory, candidate, candidate.InvalidReason, now)
	default:
		return s.uploadAndStore(ctx, req, identity, derivedKey, advisory, nil, "", now)
	}
}

func (s *Service) probeRemote(ctx context.Context, mediaID string) (bool, error) {
	if s.probe == nil {
		return false, fmt.Errorf("%w: no prober configured", ErrProbeUnavailable)
	}
	exists, err := s.probe(ctx, mediaID)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrProbeUnavailable, err)
	}
	return exists, nil
}

func (s *Service) isExpired(r *MediaRecord, now time.Time) bool {
	if s.ttl <= 0 {
		return true
	}
	anchor := r.LastValidatedAt
	if anchor.IsZero() {
		anchor = r.CreatedAt
	}
	return now.Sub(anchor) >= s.ttl
}

func (s *Service) nearDuplicateAdvisory(idx *Index, accountKey string, incoming Identity) Advisory {
	if s.threshold <= 0 || incoming.PHash == 0 || incoming.PHashVersion == "" {
		return Advisory{}
	}
	best := Advisory{}
	for i := range idx.Records {
		r := &idx.Records[i]
		if r.AccountKey != accountKey || r.Status != StatusValid || r.SourcePHash == 0 ||
			r.PHashVersion != incoming.PHashVersion || r.SourceDigest == incoming.SourceDigest {
			continue
		}
		d := HammingDistance(incoming.PHash, r.SourcePHash)
		if d <= s.threshold && (!best.NearDuplicate || d < best.Distance) {
			best = Advisory{
				NearDuplicate: true, Distance: d,
				ExistingDerivedKey: r.DerivedKey, ExistingMediaID: r.MediaID,
			}
		}
	}
	return best
}

// uploadAndStore performs the upload, writes blobs/thumb, and replaces (or
// inserts) the record under the catalog lock.
func (s *Service) uploadAndStore(
	ctx context.Context,
	req ResolveRequest,
	identity *Identity,
	derivedKey string,
	advisory Advisory,
	old *MediaRecord,
	invalidReason string,
	now time.Time,
) (*ResolveResult, error) {
	sourceDigest := identity.SourceDigest
	if s.upload == nil {
		return nil, ErrResolveMissUploadMissing
	}
	binding, err := s.upload(ctx, req.Processed)
	if err != nil {
		return nil, fmt.Errorf("mediacache: upload: %w", err)
	}
	if binding.MediaID == "" {
		return nil, errors.New("mediacache: upload returned empty media_id")
	}
	uploadedDigest := DigestBytes(req.Processed)

	// Local content blobs (source + actually uploaded bytes).
	if err := s.store.PutBlob(sourceDigest, req.Original); err != nil {
		return nil, err
	}
	if uploadedDigest != sourceDigest {
		if err := s.store.PutBlob(uploadedDigest, req.Processed); err != nil {
			return nil, err
		}
	}
	// Reference thumbnail is best-effort.
	if thumb, terr := GenerateThumb(req.Original); terr == nil {
		_ = s.store.PutThumb(sourceDigest, thumb)
	}

	reuploaded := old != nil
	lock, err := s.store.LockIndex()
	if err != nil {
		return nil, err
	}
	defer lock.unlock()

	idx, err := s.store.ReadIndex()
	if err != nil {
		return nil, err
	}
	// Cross-process race: another process may have populated a fresh record
	// while we were uploading; prefer it and keep ours as a harmless orphan.
	if winner := findRecord(idx, req.AccountKey, derivedKey); winner != nil && winner != old &&
		winner.Status == StatusValid && !s.isExpired(winner, now) {
		return &ResolveResult{
			MediaID: winner.MediaID, WechatURL: winner.WechatURL,
			DerivedKey: derivedKey, Advisory: advisory,
		}, nil
	}

	rec := MediaRecord{
		DerivedKey:         derivedKey,
		AccountKey:         req.AccountKey,
		SourceDigest:       sourceDigest,
		ProcessSpec:        req.Spec,
		UploadedBlobDigest: uploadedDigest,
		MediaID:            binding.MediaID,
		WechatURL:          binding.WechatURL,
		CreatedAt:          now,
		LastValidatedAt:    now,
		Status:             StatusValid,
	}
	if old != nil {
		// Preserve tracking metadata across replacement.
		rec.CreatedAt = old.CreatedAt
		rec.References = old.References
		rec.ManifestPins = old.ManifestPins
		// A pin pointed at the old manifest item (old derived key identity is
		// unchanged — same key); pins therefore stay valid on the new media_id.
	}
	rec.AddReference(req.ReferenceSource, now)

	rec.SourcePHash = identity.PHash
	rec.PHashVersion = identity.PHashVersion

	idx.Records = upsertRecord(idx.Records, rec)
	if err := s.store.WriteIndex(idx); err != nil {
		return nil, err
	}

	return &ResolveResult{
		MediaID: binding.MediaID, WechatURL: binding.WechatURL,
		Reuploaded: reuploaded, DerivedKey: derivedKey, Advisory: advisory,
	}, nil
}

func findRecord(idx *Index, accountKey, derivedKey string) *MediaRecord {
	for i := range idx.Records {
		r := &idx.Records[i]
		if r.AccountKey == accountKey && r.DerivedKey == derivedKey {
			return r
		}
	}
	return nil
}

// upsertRecord replaces the record with the same (account, derived key) or
// appends a new one.
func upsertRecord(records []MediaRecord, rec MediaRecord) []MediaRecord {
	for i := range records {
		if records[i].AccountKey == rec.AccountKey && records[i].DerivedKey == rec.DerivedKey {
			records[i] = rec
			return records
		}
	}
	return append(records, rec)
}

// persistRecord writes a mutated record back under the catalog lock.
func (s *Service) persistRecord(rec *MediaRecord) error {
	lock, err := s.store.LockIndex()
	if err != nil {
		return err
	}
	defer lock.unlock()
	idx, err := s.store.ReadIndex()
	if err != nil {
		return err
	}
	idx.Records = upsertRecord(idx.Records, *rec)
	return s.store.WriteIndex(idx)
}

// MarkInvalid flags records carrying mediaID for one account as invalid.
// Used when WeChat rejects a media_id during draft creation (passive invalidation).
// Returns the number of records marked.
func (s *Service) MarkInvalid(accountKey, mediaID, reason string) (int, error) {
	if mediaID == "" {
		return 0, nil
	}
	lock, err := s.store.LockIndex()
	if err != nil {
		return 0, err
	}
	defer lock.unlock()
	idx, err := s.store.ReadIndex()
	if err != nil {
		return 0, err
	}
	now := s.now()
	marked := 0
	for i := range idx.Records {
		r := &idx.Records[i]
		if r.MediaID != mediaID {
			continue
		}
		if accountKey != "" && r.AccountKey != accountKey {
			continue
		}
		r.Status = StatusInvalid
		r.InvalidReason = reason
		r.LastValidatedAt = now
		marked++
	}
	if marked > 0 {
		if err := s.store.WriteIndex(idx); err != nil {
			return 0, err
		}
	}
	return marked, nil
}
