package mediacache

import (
	"context"
	"testing"
	"time"
)

func resolveTwoAssets(t *testing.T, svc *Service, store *Store) (ManifestItem, ManifestItem) {
	t.Helper()
	imgA := mustPNGBytes(t, newPattern(120, 120, "v"))
	imgB := mustPNGBytes(t, newPattern(120, 120, "noise"))
	rA, err := svc.Resolve(context.Background(), resolveReq("acct", imgA, imgA))
	if err != nil {
		t.Fatal(err)
	}
	rB, err := svc.Resolve(context.Background(), resolveReq("acct", imgB, imgB))
	if err != nil {
		t.Fatal(err)
	}
	idx, err := store.ReadIndex()
	if err != nil {
		t.Fatal(err)
	}
	find := func(key string) MediaRecord {
		for _, rec := range idx.Records {
			if rec.DerivedKey == key {
				return rec
			}
		}
		t.Fatalf("record %s not found", key)
		return MediaRecord{}
	}
	recA, recB := find(rA.DerivedKey), find(rB.DerivedKey)
	return ManifestItem{
			DerivedKey: recA.DerivedKey, SourceDigest: recA.SourceDigest,
			UploadedBlobDigest: recA.UploadedBlobDigest, MediaID: recA.MediaID, WechatURL: recA.WechatURL,
		}, ManifestItem{
			DerivedKey: recB.DerivedKey, SourceDigest: recB.SourceDigest,
			UploadedBlobDigest: recB.UploadedBlobDigest, MediaID: recB.MediaID, WechatURL: recB.WechatURL,
		}
}

func TestManifestIDDeterministic(t *testing.T) {
	items := []ManifestItem{
		{DerivedKey: "key-z"}, {DerivedKey: "key-a"}, {DerivedKey: "key-m"},
	}
	id1, err := ManifestID("acct", items)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := ManifestID("acct", []ManifestItem{
		{DerivedKey: "key-m"}, {DerivedKey: "key-z"}, {DerivedKey: "key-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatal("manifest ID must be order-independent")
	}
	id3, err := ManifestID("other", items)
	if err != nil {
		t.Fatal(err)
	}
	if id3 == id1 {
		t.Fatal("manifest ID must distinguish accounts")
	}
	if _, err := ManifestID("acct", nil); err == nil {
		t.Fatal("empty items rejected")
	}
	if _, err := ManifestID("acct", []ManifestItem{{}}); err == nil {
		t.Fatal("empty key rejected")
	}
}

func TestPinManifestIdempotent(t *testing.T) {
	up := &fakeUploader{}
	svc, store, _ := newTestService(t, 7, 5, up.upload, nil, baseTime())
	itemA, itemB := resolveTwoAssets(t, svc, store)

	m1, err := svc.PinManifest(PinRequest{
		AccountKey: "acct", Source: "doc.md", Items: []ManifestItem{itemA, itemB},
	})
	if err != nil {
		t.Fatalf("PinManifest: %v", err)
	}
	if got := len(m1.Items); got != 2 || m1.Items[0].DerivedKey > m1.Items[1].DerivedKey {
		t.Fatalf("manifest items not sorted/complete: %+v", m1.Items)
	}
	persisted, err := store.ReadManifest(m1.ManifestID)
	if err != nil {
		t.Fatalf("manifest not persisted: %v", err)
	}
	if persisted.AccountKey != "acct" || persisted.Source != "doc.md" {
		t.Fatalf("persisted manifest wrong: %+v", persisted)
	}

	// Records carry the backfilled pin.
	idx, _ := store.ReadIndex()
	for _, rec := range idx.Records {
		if !rec.HasPin(m1.ManifestID) {
			t.Fatalf("record %s missing pin backfill", rec.DerivedKey)
		}
	}

	// Repeat with shuffled order: same id, no extra file, no error.
	m2, err := svc.PinManifest(PinRequest{
		AccountKey: "acct", Items: []ManifestItem{itemB, itemA},
	})
	if err != nil || m2.ManifestID != m1.ManifestID {
		t.Fatalf("repeat pin = %v, id %s vs %s", err, m2.ManifestID, m1.ManifestID)
	}
	ids, _ := store.ListManifestIDs()
	if len(ids) != 1 {
		t.Fatalf("manifests = %d, want 1", len(ids))
	}

	// Missing digest/media id must be rejected.
	bad := itemA
	bad.UploadedBlobDigest = ""
	if _, err := svc.PinManifest(PinRequest{AccountKey: "acct", Items: []ManifestItem{bad}}); err == nil {
		t.Fatal("incomplete item must be rejected")
	}
}

func baseTime() (t time.Time) {
	return time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
}
