package mediacache

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geekjourneyx/md2wechat-skill/internal/config"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func digestN(n byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = n
	}
	return hex.EncodeToString(b)
}

func TestStorePermissionsAndLayout(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, dir := range []string{".", dirBlobs, dirThumbs, dirManifests, dirLocks} {
		info, err := os.Stat(filepath.Join(root, dir))
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("dir %s perm = %o, want 0700", dir, perm)
		}
	}
	// Index file must exist on Open.
	if _, err := os.Stat(s.indexPath()); err != nil {
		t.Fatalf("index missing: %v", err)
	}
}

func TestPutBlobIdempotentAndSharded(t *testing.T) {
	s := openTestStore(t)
	digest := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	data := []byte("hello-asset")

	if err := s.PutBlob(digest, data); err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	p, err := s.BlobPath(digest)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(s.root, dirBlobs, "aa", digest); p != want {
		t.Fatalf("blob path %q, want %q", p, want)
	}
	got, err := s.ReadBlob(digest)
	if err != nil {
		t.Fatalf("ReadBlob: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("blob content mismatch")
	}
	if info, _ := os.Stat(p); info.Mode().Perm() != fileMode {
		t.Fatalf("blob perm = %o, want %o", info.Mode().Perm(), fileMode)
	}

	// Make blob read-only; a second identical Put must not rewrite it.
	if err := os.Chmod(p, 0o400); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(p, fileMode)
	if err := s.PutBlob(digest, data); err != nil {
		t.Fatalf("identical PutBlob must be a no-op: %v", err)
	}
	missingBytes := make([]byte, 32)
	for i := range missingBytes {
		missingBytes[i] = 0xFE
	}
	if _, err := s.ReadBlob(hex.EncodeToString(missingBytes)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing blob want ErrNotFound, got %v", err)
	}
	// A length-different Put is a rewrite (must fail on the read-only file,
	// proving it did not silently keep stale content).
	if err := s.PutBlob(digest, []byte("much longer replacement payload")); err == nil {
		t.Fatal("different-length PutBlob must attempt a rewrite")
	}
}

func TestValidateDigest(t *testing.T) {
	if err := validateDigest(""); err == nil {
		t.Fatal("empty digest accepted")
	}
	if err := validateDigest("zz" + digestN(0)[2:]); err == nil {
		t.Fatal("non-hex digest accepted")
	}
	if err := validateDigest(digestN(1)); err != nil {
		t.Fatalf("valid digest rejected: %v", err)
	}
}

func TestPutAndHasThumb(t *testing.T) {
	s := openTestStore(t)
	digest := digestN(7)
	if ok, _ := s.HasThumb(digest); ok {
		t.Fatal("thumb should not exist yet")
	}
	if err := s.PutThumb(digest, []byte("jpeg")); err != nil {
		t.Fatalf("PutThumb: %v", err)
	}
	ok, err := s.HasThumb(digest)
	if err != nil || !ok {
		t.Fatalf("HasThumb = %v, %v", ok, err)
	}
}

func TestCrossProcessIndexLock(t *testing.T) {
	s := openTestStore(t)
	first, err := s.LockIndex()
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	second, err := acquireLock(filepath.Join(s.root, dirLocks, indexLock), 60*time.Millisecond)
	if !errors.Is(err, ErrLockBusy) {
		if second != nil {
			_ = second.unlock()
		}
		t.Fatalf("contended lock = %v, want ErrLockBusy", err)
	}
	if err := first.unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	third, err := acquireLock(filepath.Join(s.root, dirLocks, indexLock), time.Second)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	_ = third.unlock()
}

func TestIndexRoundtripAndCorruption(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()
	idx := &Index{
		Records: []MediaRecord{
			{
				DerivedKey:   "dk1",
				AccountKey:   "wxapp",
				SourceDigest: digestN(1),
				ProcessSpec:  ForContent(nil),
				MediaID:      "mid-1",
				WechatURL:    "http://example/1",
				CreatedAt:    now,
				Status:       StatusValid,
			},
		},
	}
	if err := s.WriteIndex(idx); err != nil {
		t.Fatalf("WriteIndex: %v", err)
	}
	got, err := s.ReadIndex()
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(got.Records) != 1 || got.Records[0].MediaID != "mid-1" || got.Version != indexVersion {
		t.Fatalf("index roundtrip mismatch: %+v", got)
	}

	// Corrupt index must surface a parse error rather than silent reset.
	if err := os.WriteFile(s.indexPath(), []byte("{not json"), fileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadIndex(); err == nil {
		t.Fatal("corrupt index must return error")
	}
}

func TestLockedConcurrentIndexUpdates(t *testing.T) {
	s := openTestStore(t)
	const writers = 8
	done := make(chan error, writers)
	for i := 0; i < writers; i++ {
		i := i
		go func() {
			lock, err := s.LockIndex()
			if err != nil {
				done <- err
				return
			}
			defer lock.unlock()
			idx, err := s.ReadIndex()
			if err != nil {
				done <- err
				return
			}
			idx.Records = append(idx.Records, MediaRecord{DerivedKey: "dk-" + string(rune('a'+i)), Status: StatusValid})
			// Hold the lock briefly so contenders actually queue.
			time.Sleep(5 * time.Millisecond)
			done <- s.WriteIndex(idx)
		}()
	}
	for i := 0; i < writers; i++ {
		if err := <-done; err != nil {
			t.Fatalf("writer: %v", err)
		}
	}
	idx, err := s.ReadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Records) != writers {
		t.Fatalf("lost updates: got %d records, want %d", len(idx.Records), writers)
	}
}

func TestManifestRoundtripAndDelete(t *testing.T) {
	s := openTestStore(t)
	m := &Manifest{
		ManifestID: digestN(3),
		AccountKey: "wxapp",
		Items: []ManifestItem{
			{DerivedKey: "dk1", MediaID: "mid-1", UploadedBlobDigest: digestN(2)},
		},
	}
	if err := s.WriteManifest(m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	ids, err := s.ListManifestIDs()
	if err != nil || len(ids) != 1 || ids[0] != m.ManifestID {
		t.Fatalf("ListManifestIDs = %v, %v", ids, err)
	}
	got, err := s.ReadManifest(m.ManifestID)
	if err != nil || len(got.Items) != 1 {
		t.Fatalf("ReadManifest: %+v, %v", got, err)
	}
	if err := s.DeleteManifest(m.ManifestID); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}
	if _, err := s.ReadManifest(m.ManifestID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	// Idempotent delete.
	if err := s.DeleteManifest(m.ManifestID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestAccountKey(t *testing.T) {
	if got := AccountKey(nil); got != "" {
		t.Fatalf("nil cfg account key = %q", got)
	}
	unnamed := &config.Config{WechatAppID: "wxid"}
	if got := AccountKey(unnamed); got != "wxid" {
		t.Fatalf("unnamed key = %q", got)
	}
	named := &config.Config{WechatAppID: "wxid", WechatAccount: "work", WechatAccountNamed: true}
	if got := AccountKey(named); got != "work" {
		t.Fatalf("named key = %q", got)
	}
}
