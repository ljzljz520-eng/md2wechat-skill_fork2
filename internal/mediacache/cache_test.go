package mediacache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeUploader struct {
	mu       sync.Mutex
	count    int32
	block    chan struct{} // non-nil: every upload blocks until closed
	started  chan struct{}
	err      error
	mediaIDs []string
}

func (f *fakeUploader) upload(_ context.Context, payload []byte) (UploadBinding, error) {
	atomic.AddInt32(&f.count, 1)
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.block != nil {
		<-f.block
	}
	if f.err != nil {
		return UploadBinding{}, f.err
	}
	id := "mid-" + string(rune('a'+int(atomic.LoadInt32(&f.count)-1)))
	f.mu.Lock()
	f.mediaIDs = append(f.mediaIDs, id)
	f.mu.Unlock()
	return UploadBinding{MediaID: id, WechatURL: "http://example/" + id}, nil
}

type fakeProber struct {
	mu     sync.Mutex
	count  int32
	exists map[string]bool
	err    error
}

func (p *fakeProber) probe(_ context.Context, mediaID string) (bool, error) {
	atomic.AddInt32(&p.count, 1)
	if p.err != nil {
		return false, p.err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exists[mediaID], nil
}

func newTestService(t *testing.T, ttlDays int, threshold int, up UploadFunc, pr ProbeFunc, now time.Time) (*Service, *Store, func(time.Time)) {
	t.Helper()
	store := openTestStore(t)
	clock := now
	svc := NewService(store, Options{
		TTLDays:                ttlDays,
		NearDuplicateThreshold: threshold,
		Probe:                  pr,
		Upload:                 up,
		Now:                    func() time.Time { return clock },
	})
	return svc, store, func(t2 time.Time) { clock = t2 }
}

func resolveReq(account string, original, processed []byte) ResolveRequest {
	return ResolveRequest{
		AccountKey: account,
		Spec:       ForContent(nil),
		Original:   original,
		Processed:  processed,
	}
}

func TestResolveTTLHitMakesNoRequest(t *testing.T) {
	img := mustPNGBytes(t, newPattern(120, 120, "v"))
	up := &fakeUploader{}
	pr := &fakeProber{}
	svc, _, _ := newTestService(t, 7, 5, up.upload, pr.probe, time.Now())

	r1, err := svc.Resolve(context.Background(), resolveReq("acct", img, img))
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if r1.CacheHit || r1.MediaID != "mid-a" {
		t.Fatalf("first resolve = %+v", r1)
	}
	r2, err := svc.Resolve(context.Background(), resolveReq("acct", img, img))
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if !r2.CacheHit || r2.MediaID != "mid-a" {
		t.Fatalf("second resolve must be a TTL hit: %+v", r2)
	}
	if atomic.LoadInt32(&up.count) != 1 {
		t.Fatalf("uploads = %d, want 1", up.count)
	}
	if atomic.LoadInt32(&pr.count) != 0 {
		t.Fatalf("probes = %d, want 0", pr.count)
	}
}

func TestResolveExpiredProbesAndReuploads(t *testing.T) {
	img := mustPNGBytes(t, newPattern(120, 120, "v"))
	up := &fakeUploader{}
	pr := &fakeProber{exists: map[string]bool{}}
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	svc, _, setClock := newTestService(t, 7, 0, up.upload, pr.probe, base)

	r1, err := svc.Resolve(context.Background(), resolveReq("acct", img, img))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	pr.exists[r1.MediaID] = true

	// Within TTL: no probe.
	setClock(base.Add(6 * 24 * time.Hour))
	if _, err := svc.Resolve(context.Background(), resolveReq("acct", img, img)); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&pr.count) != 0 {
		t.Fatalf("probe before TTL expiry: %d", pr.count)
	}

	// Past TTL, server still has it: probe once, no reupload.
	setClock(base.Add(8 * 24 * time.Hour))
	r2, err := svc.Resolve(context.Background(), resolveReq("acct", img, img))
	if err != nil {
		t.Fatalf("expired resolve: %v", err)
	}
	if !r2.CacheHit || r2.MediaID != r1.MediaID {
		t.Fatalf("probed-alive resolve = %+v", r2)
	}
	if atomic.LoadInt32(&up.count) != 1 {
		t.Fatalf("unexpected reupload")
	}

	// Server deleted it: next expired probe returns false → reupload once.
	pr.exists[r1.MediaID] = false
	setClock(base.Add(20 * 24 * time.Hour))
	r3, err := svc.Resolve(context.Background(), resolveReq("acct", img, img))
	if err != nil {
		t.Fatalf("deleted resolve: %v", err)
	}
	if !r3.Reuploaded || r3.MediaID == r1.MediaID || r3.MediaID != "mid-b" {
		t.Fatalf("deleted resolve = %+v", r3)
	}
	if atomic.LoadInt32(&up.count) != 2 {
		t.Fatalf("uploads = %d, want 2", up.count)
	}
}

func TestResolveInvalidRecordReuploadsWithoutProbe(t *testing.T) {
	img := mustPNGBytes(t, newPattern(120, 120, "v"))
	up := &fakeUploader{}
	pr := &fakeProber{}
	now := time.Now()
	svc, store, _ := newTestService(t, 7, 0, up.upload, pr.probe, now)

	spec := ForContent(nil)
	identity, err := ComputeIdentity(img)
	if err != nil {
		t.Fatal(err)
	}
	dk, err := spec.DerivedKey(identity.SourceDigest)
	if err != nil {
		t.Fatal(err)
	}
	idx := &Index{Version: indexVersion, Records: []MediaRecord{{
		DerivedKey:    dk,
		AccountKey:    "acct",
		SourceDigest:  identity.SourceDigest,
		ProcessSpec:   spec,
		MediaID:       "dead-id",
		CreatedAt:     now,
		Status:        StatusInvalid,
		InvalidReason: "wechat:40007",
	}}}
	lock, _ := store.LockIndex()
	if err := store.WriteIndex(idx); err != nil {
		t.Fatal(err)
	}
	lock.unlock()

	r, err := svc.Resolve(context.Background(), resolveReq("acct", img, img))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Reuploaded || r.MediaID != "mid-a" {
		t.Fatalf("invalid resolve = %+v", r)
	}
	if atomic.LoadInt32(&pr.count) != 0 {
		t.Fatal("invalid record must not be probed")
	}
}

func TestResolveProbeErrorFailsClosed(t *testing.T) {
	img := mustPNGBytes(t, newPattern(120, 120, "v"))
	up := &fakeUploader{}
	probeErr := errors.New("connection refused")
	pr := &fakeProber{err: probeErr}
	base := time.Now()
	svc, _, setClock := newTestService(t, 7, 0, up.upload, pr.probe, base)
	if _, err := svc.Resolve(context.Background(), resolveReq("acct", img, img)); err != nil {
		t.Fatal(err)
	}
	setClock(base.Add(8 * 24 * time.Hour))
	_, err := svc.Resolve(context.Background(), resolveReq("acct", img, img))
	if !errors.Is(err, ErrProbeUnavailable) {
		t.Fatalf("want ErrProbeUnavailable, got %v", err)
	}
	if atomic.LoadInt32(&up.count) != 1 {
		t.Fatal("probe failure must not trigger reupload")
	}
}

func TestResolveDifferentSpecsAndAccountsDoNotReuse(t *testing.T) {
	img := mustPNGBytes(t, newPattern(120, 120, "v"))
	up := &fakeUploader{}
	svc, _, _ := newTestService(t, 7, 0, up.upload, nil, time.Now())

	mkReq := func(account string, spec ProcessSpec) ResolveRequest {
		return ResolveRequest{AccountKey: account, Spec: spec, Original: img, Processed: img}
	}
	base := ForContent(nil)
	widthVariant := base
	widthVariant.MaxWidth = 1080
	cover := ForCover(nil)

	for _, spec := range []ProcessSpec{base, widthVariant, cover} {
		if _, err := svc.Resolve(context.Background(), mkReq("acct", spec)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Resolve(context.Background(), mkReq("other-account", base)); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&up.count); got != 4 {
		t.Fatalf("uploads = %d, want 4 (3 specs + 1 isolated account)", got)
	}
}

func TestResolveConcurrentSameAssetUploadsOnce(t *testing.T) {
	for _, n := range []int{2, 8, 32} {
		t.Run("", func(t *testing.T) {
			img := mustPNGBytes(t, newPattern(120, 120, "v"))
			up := &fakeUploader{block: make(chan struct{}), started: make(chan struct{}, 1)}
			svc, _, _ := newTestService(t, 7, 0, up.upload, nil, time.Now())

			var wg sync.WaitGroup
			results := make([]*ResolveResult, n)
			errs := make([]error, n)
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					results[i], errs[i] = svc.Resolve(context.Background(), resolveReq("acct", img, img))
				}(i)
			}
			<-up.started
			time.Sleep(50 * time.Millisecond) // let followers queue on single-flight
			close(up.block)
			wg.Wait()

			for i, err := range errs {
				if err != nil {
					t.Fatalf("goroutine %d: %v", i, err)
				}
			}
			if got := atomic.LoadInt32(&up.count); got != 1 {
				t.Fatalf("uploads = %d, want 1 (n=%d)", got, n)
			}
			for i, r := range results {
				if r == nil || r.MediaID != "mid-a" {
					t.Fatalf("goroutine %d result = %+v", i, r)
				}
			}
		})
	}
}

func TestResolveNearDuplicateAdvisoryOnMiss(t *testing.T) {
	original := mustPNGBytes(t, newPattern(300, 300, "v"))
	nearCopy := mustPNGBytes(t, newPattern(64, 64, "v"))
	distinct := mustPNGBytes(t, newPattern(120, 120, "noise"))
	up := &fakeUploader{}

	t.Run("threshold 5 flags resize", func(t *testing.T) {
		svc, _, _ := newTestService(t, 7, 5, up.upload, nil, time.Now())
		r0, err := svc.Resolve(context.Background(), resolveReq("acct", original, original))
		if err != nil {
			t.Fatal(err)
		}
		rNear, err := svc.Resolve(context.Background(), resolveReq("acct", nearCopy, nearCopy))
		if err != nil {
			t.Fatal(err)
		}
		if !rNear.Advisory.NearDuplicate || rNear.Advisory.ExistingMediaID != r0.MediaID {
			t.Fatalf("near dup advisory missing: %+v", rNear.Advisory)
		}
		// Advisory must never block: upload still happened.
		if rNear.MediaID == "" {
			t.Fatal("advisory must not block upload")
		}
		rDistinct, err := svc.Resolve(context.Background(), resolveReq("acct", distinct, distinct))
		if err != nil {
			t.Fatal(err)
		}
		if rDistinct.Advisory.NearDuplicate {
			t.Fatalf("distinct content must not be flagged: %+v", rDistinct.Advisory)
		}
	})

	t.Run("threshold 0 disables advisory", func(t *testing.T) {
		svc, _, _ := newTestService(t, 7, 0, up.upload, nil, time.Now())
		if _, err := svc.Resolve(context.Background(), resolveReq("acct2", original, original)); err != nil {
			t.Fatal(err)
		}
		r, err := svc.Resolve(context.Background(), resolveReq("acct2", nearCopy, nearCopy))
		if err != nil {
			t.Fatal(err)
		}
		if r.Advisory.NearDuplicate {
			t.Fatal("advisory must be disabled at threshold 0")
		}
	})
}

func TestMarkInvalidScopedByAccount(t *testing.T) {
	img := mustPNGBytes(t, newPattern(120, 120, "v"))
	up := &fakeUploader{}
	svc, store, _ := newTestService(t, 7, 0, up.upload, nil, time.Now())

	rA, err := svc.Resolve(context.Background(), resolveReq("acct-a", img, img))
	if err != nil {
		t.Fatal(err)
	}
	rB, err := svc.Resolve(context.Background(), resolveReq("acct-b", img, img))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the same media_id string bound to both accounts (WeChat IDs are
	// account-scoped, so marking must not cross accounts).
	n, err := svc.MarkInvalid("acct-a", rA.MediaID, "wechat:40007")
	if err != nil || n != 1 {
		t.Fatalf("MarkInvalid = %d, %v", n, err)
	}
	idx, err := store.ReadIndex()
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range idx.Records {
		want := StatusValid
		if rec.AccountKey == "acct-a" {
			want = StatusInvalid
		}
		if rec.Status != want {
			t.Fatalf("record %s status = %s, want %s", rec.AccountKey, rec.Status, want)
		}
	}
	// MediaID from the other account must not match by accident.
	if _, err := svc.MarkInvalid("acct-b", rB.MediaID+"-nope", "x"); err != nil {
		t.Fatal(err)
	}
}
