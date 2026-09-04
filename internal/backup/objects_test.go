package backup

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestGzipRoundTrip(t *testing.T) {
	plain := bytes.Repeat([]byte("custom-format-dump\x00\x01"), 1000)
	compressed, err := gzipBytes(plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(compressed) >= len(plain) {
		t.Error("BestSpeed gzip did not shrink repetitive input")
	}
	back, err := gunzipBytes(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, plain) {
		t.Error("round trip mismatch")
	}
	if _, err := gunzipBytes([]byte("not gzip")); err == nil {
		t.Error("gunzip of garbage succeeded")
	}
}

func TestBlobKeyShape(t *testing.T) {
	at := time.Date(2026, 9, 4, 2, 0, 0, 0, time.UTC)
	k, err := blobKey("my-svc", at)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := "backups/my-svc/20260904T020000Z-"
	if !strings.HasPrefix(k, wantPrefix) {
		t.Errorf("key = %s, want prefix %s", k, wantPrefix)
	}
	if len(k) != len(wantPrefix)+12+len(blobSuffix) {
		t.Errorf("key %s: nonce not 12 hex chars", k)
	}
	k2, err := blobKey("my-svc", at)
	if err != nil {
		t.Fatal(err)
	}
	if k == k2 {
		t.Error("two same-second keys collided")
	}
	if metaKeyFor(k) != k[:len(k)-len(blobSuffix)]+metaSuffix {
		t.Errorf("metaKeyFor(%s) = %s", k, metaKeyFor(k))
	}
}

func TestParseTakenAt(t *testing.T) {
	good := "backups/pg/20260904T020000Z-deadbeefcafe" + blobSuffix
	ts, err := parseTakenAt(good)
	if err != nil {
		t.Fatal(err)
	}
	if !ts.Equal(time.Date(2026, 9, 4, 2, 0, 0, 0, time.UTC)) {
		t.Errorf("parsed %v", ts)
	}
	for _, bad := range []string{"backups/pg/not-a-date-abcdef" + blobSuffix, "backups/pg/" + blobSuffix} {
		if _, err := parseTakenAt(bad); err == nil {
			t.Errorf("parseTakenAt(%q) succeeded", bad)
		}
	}
}

func TestKeyID(t *testing.T) {
	var k [32]byte
	for i := range k {
		k[i] = byte(i)
	}
	if got := keyID(k); got != "00010203" {
		t.Errorf("keyID = %s, want first 4 bytes hex", got)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	m := Meta{Slug: "pg", Type: "postgres", Image: "postgres:16-alpine", TakenAt: "2026-09-04T02:00:00Z", Size: 10, SHA256: "ab", KeyID: "0001"}
	b, err := encodeMeta(&m)
	if err != nil {
		t.Fatal(err)
	}
	var back Meta
	if err := decodeMeta(b, &back); err != nil {
		t.Fatal(err)
	}
	if back != m {
		t.Errorf("round trip = %+v, want %+v", back, m)
	}
}
