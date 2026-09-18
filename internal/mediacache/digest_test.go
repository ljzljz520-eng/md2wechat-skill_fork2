package mediacache

import (
	"strings"
	"testing"
)

func TestDigestMatchesKnownVectors(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"empty", []byte(""), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DigestBytes(tc.data); got != tc.want {
				t.Fatalf("DigestBytes = %q, want %q", got, tc.want)
			}
			got, err := BlobDigest(strings.NewReader(string(tc.data)))
			if err != nil {
				t.Fatalf("BlobDigest error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("BlobDigest = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDigestIsDeterministicAndSensitive(t *testing.T) {
	data := []byte("the quick brown fox jumps over the lazy dog")
	first := DigestBytes(data)
	second := DigestBytes(append(append([]byte(nil), data...)))
	if first != second {
		t.Fatalf("identical bytes produced different digests: %q vs %q", first, second)
	}

	changed := append(append([]byte(nil), data...), '!')
	if DigestBytes(changed) == first {
		t.Fatal("single byte change did not change the digest")
	}

	fromReader, err := BlobDigest(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("BlobDigest error: %v", err)
	}
	if fromReader != first {
		t.Fatalf("BlobDigest = %q, want %q", fromReader, first)
	}
}
