package mediacache

import (
	"encoding/json"
	"testing"
	"time"
)

type gcFixture struct {
	digests map[string]string
}

func seedGCFixture(t *testing.T, now time.Time) (*Service, *Store, gcFixture) {
	t.Helper()
	store := openTestStore(t)
	svc := NewService(store, Options{TTLDays: 7, Now: func() time.Time { return now }})

	d := map[string]string{
		"sA": digestN(0x01), "uA": digestN(0x11),
		"sB": digestN(0x02), "uB": digestN(0x12),
		"sC": digestN(0x03), "uC": digestN(0x13),
		"sD": digestN(0x04), "uD": digestN(0x14),
		"sO": digestN(0x0F), "sT": digestN(0x1F),
	}
	blob := func(key string, size int) {
		data := make([]byte, size)
		for i := range data {
			data[i] = key[1]
		}
		if err := store.PutBlob(d[key], data); err != nil {
			t.Fatal(err)
		}
	}
	blob("sA", 100)
	blob("uA", 110)
	blob("sB", 200)
	blob("uB", 210)
	blob("sC", 300)
	blob("uC", 310)
	blob("sD", 400)
	blob("uD", 410)
	blob("sO", 999)
	if err := store.PutThumb(d["sA"], []byte("jpeg")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutThumb(d["sT"], []byte("jpeg-orphan")); err != nil {
		t.Fatal(err)
	}

	spec := ForContent(nil)
	rec := func(dk, src, up string, status RecordStatus, validated time.Time, pins ...string) MediaRecord {
		return MediaRecord{
			DerivedKey: "dk-" + dk, AccountKey: "acct", SourceDigest: src,
			UploadedBlobDigest: up, ProcessSpec: spec, MediaID: "mid-" + dk,
			CreatedAt: now.Add(-30 * 24 * time.Hour), LastValidatedAt: validated,
			Status: status, ManifestPins: pins,
		}
	}
	old := now.Add(-8 * 24 * time.Hour)
	records := []MediaRecord{
		rec("A", d["sA"], d["uA"], StatusValid, now),
		rec("B", d["sB"], d["uB"], StatusInvalid, old),
		rec("C", d["sC"], d["uC"], StatusInvalid, old, digestN(0x3C)),
		rec("D", d["sD"], d["uD"], StatusInvalid, now.Add(-1*time.Hour)),
	}
	idx := &Index{Version: indexVersion, Records: records}
	lock, _ := store.LockIndex()
	if err := store.WriteIndex(idx); err != nil {
		t.Fatal(err)
	}
	lock.unlock()

	// Manifest protects B's blobs even though its record is unpinned/invalid.
	mB := &Manifest{
		ManifestID: digestN(0x2B), AccountKey: "acct", CreatedAt: old,
		Items: []ManifestItem{{
			DerivedKey: "dk-B", SourceDigest: d["sB"], UploadedBlobDigest: d["uB"],
			MediaID: "mid-B",
		}},
	}
	if err := store.WriteManifest(mB); err != nil {
		t.Fatal(err)
	}
	mC := &Manifest{
		ManifestID: digestN(0x3C), AccountKey: "acct", CreatedAt: old,
		Items: []ManifestItem{{
			DerivedKey: "dk-C", SourceDigest: d["sC"], UploadedBlobDigest: d["uC"],
			MediaID: "mid-C",
		}},
	}
	if err := store.WriteManifest(mC); err != nil {
		t.Fatal(err)
	}
	return svc, store, gcFixture{digests: d}
}

func TestGCPlanRetention(t *testing.T) {
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	svc, store, fx := seedGCFixture(t, now)

	plan, err := svc.PlanGC(7)
	if err != nil {
		t.Fatalf("PlanGC: %v", err)
	}
	if plan.ScannedBlobs != 9 || plan.RetainedBlobs != 8 {
		t.Fatalf("scan stats = %d/%d", plan.ScannedBlobs, plan.RetainedBlobs)
	}
	if len(plan.BlobsToDelete) != 1 || plan.BlobsToDelete[0].Digest != fx.digests["sO"] {
		t.Fatalf("blob plan = %+v", plan.BlobsToDelete)
	}
	if plan.BlobsToDelete[0].SizeBytes != 999 {
		t.Fatalf("size = %d", plan.BlobsToDelete[0].SizeBytes)
	}
	if len(plan.RecordsToDelete) != 1 || plan.RecordsToDelete[0].DerivedKey != "dk-B" {
		t.Fatalf("record plan = %+v", plan.RecordsToDelete)
	}
	if len(plan.ThumbsToDelete) != 1 || plan.ThumbsToDelete[0] != fx.digests["sT"] {
		t.Fatalf("thumb plan = %+v", plan.ThumbsToDelete)
	}

	// Dry run deletes nothing.
	dry, err := svc.ExecuteGC(plan, false)
	if err != nil || len(dry.DeletedBlobs) != 0 || dry.DeletedRecords != 0 {
		t.Fatalf("dry run mutated state: %+v, %v", dry, err)
	}
	if ok, _ := store.HasBlob(fx.digests["sO"]); !ok {
		t.Fatal("dry run deleted orphan blob")
	}

	// Real run.
	report, err := svc.ExecuteGC(plan, true)
	if err != nil {
		t.Fatalf("ExecuteGC: %v", err)
	}
	if len(report.DeletedBlobs) != 1 || report.DeletedBlobs[0] != fx.digests["sO"] ||
		report.DeletedBytes != 999 || report.DeletedRecords != 1 {
		t.Fatalf("report = %+v", report)
	}
	if ok, _ := store.HasBlob(fx.digests["sO"]); ok {
		t.Fatalf("orphan blob survives GC")
	}
	// Manifest-only blobs survive record deletion.
	for _, key := range []string{"sA", "uA", "sB", "uB", "sC", "uC", "sD", "uD"} {
		if ok, _ := store.HasBlob(fx.digests[key]); !ok {
			t.Fatalf("retained blob %s deleted", key)
		}
	}
	if ok, _ := store.HasThumb(fx.digests["sA"]); !ok {
		t.Fatal("retained thumb deleted")
	}
	if ok, _ := store.HasThumb(fx.digests["sT"]); ok {
		t.Fatal("orphan thumb survives")
	}
	idx, _ := store.ReadIndex()
	got := map[string]bool{}
	for _, rec := range idx.Records {
		got[rec.DerivedKey] = true
	}
	for _, key := range []string{"dk-A", "dk-C", "dk-D"} {
		if !got[key] {
			t.Fatalf("record %s must survive", key)
		}
	}
	if got["dk-B"] {
		t.Fatal("expired-grace unpinned invalid record survives")
	}
	// Second GC is a no-op.
	plan2, err := svc.PlanGC(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan2.BlobsToDelete) != 0 || len(plan2.RecordsToDelete) != 0 {
		t.Fatalf("second GC plan not empty: %+v", plan2)
	}

	// Plans/reports must be JSON-serializable for CLI --json output.
	if _, err := json.Marshal(plan); err != nil {
		t.Fatalf("plan json: %v", err)
	}
	if _, err := json.Marshal(report); err != nil {
		t.Fatalf("report json: %v", err)
	}
}

func TestGCRespectsPinsCreatedAfterPlan(t *testing.T) {
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	svc, store, fx := seedGCFixture(t, now)
	plan, err := svc.PlanGC(7)
	if err != nil {
		t.Fatal(err)
	}
	// A publish pins the orphan after the plan was made.
	m := &Manifest{
		ManifestID: digestN(0x99), AccountKey: "acct", CreatedAt: now,
		Items: []ManifestItem{{
			DerivedKey: "dk-late", SourceDigest: fx.digests["sO"], UploadedBlobDigest: fx.digests["sO"],
			MediaID: "mid-late",
		}},
	}
	if err := store.WriteManifest(m); err != nil {
		t.Fatal(err)
	}
	report, err := svc.ExecuteGC(plan, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range report.DeletedBlobs {
		if d == fx.digests["sO"] {
			t.Fatal("stale plan deleted a blob pinned after planning")
		}
	}
	if ok, _ := store.HasBlob(fx.digests["sO"]); !ok {
		t.Fatal("pinned orphan must survive")
	}
}
