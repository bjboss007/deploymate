package backup

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Object layout, flat per service (the bucket listing is the catalog):
//
//	backups/{service-slug}/{YYYYMMDDTHHMMSSZ}-{12-hex nonce}.dump.enc
//	backups/{service-slug}/{same base}.meta.json   ← plaintext, no data
//
// The timestamp is fixed-width UTC, so sorting keys lexicographically is
// chronological. Meta carries no SQL data, no credentials, no key material —
// only what the UI needs and what lets a downloaded key be matched to
// objects (key_id = first bytes of the service key).
const (
	blobSuffix       = ".dump.enc"
	metaSuffix       = ".meta.json"
	keyTimeFormat    = "20060102T150405Z" // UTC, fixed width
	containerDump    = "/tmp/dm-backup.dump"
	containerRestore = "/tmp/dm-restore.dump"
)

// Meta is the plaintext sibling of every stored blob.
type Meta struct {
	Slug    string `json:"slug"`     // service slug — restore refuses mismatches
	Type    string `json:"type"`     // "postgres" (MVP)
	Image   string `json:"image"`    // image tag the dump came from
	TakenAt string `json:"taken_at"` // RFC3339 UTC
	Size    int64  `json:"size"`     // encrypted blob bytes
	SHA256  string `json:"sha256"`   // hex digest of the ENCRYPTED blob
	KeyID   string `json:"key_id"`   // hex(key[:4]) — matches a downloaded key
}

func backupsPrefix(slug string) string { return "backups/" + slug + "/" }

// blobKey builds a fresh object key: ts + nonce keep same-second runs from
// colliding.
func blobKey(slug string, takenAt time.Time) (string, error) {
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return backupsPrefix(slug) + takenAt.UTC().Format(keyTimeFormat) + "-" + hex.EncodeToString(nonce[:]) + blobSuffix, nil
}

// metaKeyFor derives an object's meta key from its blob key.
func metaKeyFor(blobKey string) string {
	return blobKey[:len(blobKey)-len(blobSuffix)] + metaSuffix
}

// keyID is the short hex identifier stamped into meta and shown in events —
// enough to match a downloaded key to its objects without exposing the key.
func keyID(key [32]byte) string { return hex.EncodeToString(key[:4]) }

// parseTakenAt reads the fixed-width UTC timestamp from a blob key.
func parseTakenAt(key string) (time.Time, error) {
	base := key[strings.LastIndexByte(key, '/')+1:]
	// base is {ts}-{nonce}; the nonce never contains a '-'.
	if i := strings.IndexByte(base, '-'); i == len(keyTimeFormat) {
		if t, err := time.Parse(keyTimeFormat, base[:i]); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("key has no parseable timestamp")
}

// encodeMeta JSON-encodes a meta file.
func encodeMeta(m *Meta) ([]byte, error) { return json.Marshal(m) }

func decodeMeta(b []byte, m *Meta) error { return json.Unmarshal(b, m) }

// gzipBytes compresses a dump (BestSpeed: pg_dump -Fc is already compressed,
// this is cheap belt-and-braces before encryption).
func gzipBytes(plain []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(plain); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzipBytes(compressed []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("not a gzip stream: %w", err)
	}
	defer zr.Close()
	return io.ReadAll(zr)
}
