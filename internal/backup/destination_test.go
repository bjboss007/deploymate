package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/habibmuhammad/deploymate/internal/config"
)

func TestLocalDestinationRoundTrip(t *testing.T) {
	root := t.TempDir()
	d := &LocalDestination{root: root}

	ctx := context.Background()
	keys := []string{
		"backups/pg/20260101T010000Z-aaaaaaaaaaaa" + blobSuffix,
		"backups/pg/20260102T010000Z-bbbbbbbbbbbb" + blobSuffix,
	}
	for _, k := range keys {
		if err := d.Put(ctx, k, []byte("data-"+k)); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}

	// Get returns what was put.
	b, err := d.Get(ctx, keys[0])
	if err != nil || string(b) != "data-"+keys[0] {
		t.Fatalf("get = %q, %v", b, err)
	}

	// List filters by prefix.
	objs, err := d.List(ctx, "backups/pg/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Fatalf("list len = %d, want 2", len(objs))
	}
	// List under an empty prefix sees nothing outside it.
	objs, err = d.List(ctx, "backups/other/")
	if err != nil || len(objs) != 0 {
		t.Fatalf("foreign prefix list = %v, %v", objs, err)
	}

	// Delete is idempotent.
	if err := d.Delete(ctx, keys[0]); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(ctx, keys[0]); err != nil {
		t.Fatalf("double delete: %v", err)
	}
	if _, err := d.Get(ctx, keys[0]); !os.IsNotExist(err) {
		t.Fatalf("get after delete = %v, want not-exist", err)
	}
	objs, _ = d.List(ctx, "backups/pg/")
	if len(objs) != 1 {
		t.Fatalf("post-delete list len = %d, want 1", len(objs))
	}
	_ = filepath.Join(root, "unused")
}

func TestNewDestinationsLocal(t *testing.T) {
	cfgs := map[string]config.BackupDestination{
		"default": {ID: "default", Type: "local", Dir: filepath.Join(t.TempDir(), "bk")},
	}
	dests, err := NewDestinations(cfgs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dests["default"].(*LocalDestination); !ok {
		t.Fatalf("type = %T, want *LocalDestination", dests["default"])
	}
}

func TestNewDestinationsRejectsUnknownType(t *testing.T) {
	cfgs := map[string]config.BackupDestination{
		"x": {ID: "x", Type: "ftp", Dir: "/tmp"},
	}
	if _, err := NewDestinations(cfgs); err == nil {
		t.Fatal("unknown type accepted")
	}
}

// --- S3 against a minimal in-process S3 (no signing check) --------------

type fakeS3 struct {
	mu      sync.Mutex
	bucket  string
	objects map[string][]byte
}

func newFakeS3() *fakeS3 {
	return &fakeS3{bucket: "bkt", objects: map[string][]byte{}}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/"+f.bucket+"/")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		// minio signs PUTs with aws-chunked content encoding; decode the
		// framing back into the raw object bytes.
		if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
			body = decodeChunked(body)
		}
		f.objects[key] = body
		w.Header().Set("ETag", `"fake"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet, http.MethodHead:
		if _, list := r.URL.Query()["list-type"]; list {
			f.serveList(w, key)
			return
		}
		if _, loc := r.URL.Query()["location"]; loc {
			// Bucket-region probe minio does on first contact.
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></LocationConstraint>`)
			return
		}
		body, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<Error><Code>NoSuchKey</Code><Message>not found</Message></Error>`)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// decodeChunked unwraps AWS chunked encoding:
// "<hex-size>;chunk-signature=…\r\n<data>\r\n" … "0;chunk-signature=…\r\n\r\n".
func decodeChunked(b []byte) []byte {
	var out []byte
	for len(b) > 0 {
		lineEnd := bytes.Index(b, []byte("\r\n"))
		if lineEnd < 0 {
			return out
		}
		line := string(b[:lineEnd])
		size := 0
		if i := strings.IndexByte(line, ';'); i >= 0 {
			line = line[:i]
		}
		fmt.Sscanf(line, "%x", &size)
		b = b[lineEnd+2:]
		if size == 0 {
			return out
		}
		out = append(out, b[:size]...)
		b = b[size+2:]
	}
	return out
}

func (f *fakeS3) serveList(w http.ResponseWriter, prefix string) {
	var keys []string
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<ListBucketResult><Name>` + f.bucket + `</Name><Prefix>` + prefix + `</Prefix>`)
	for _, k := range keys {
		fmt.Fprintf(&sb, "<Contents><Key>%s</Key><LastModified>%s</LastModified><ETag>&quot;x&quot;</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>", k, now, len(f.objects[k]))
	}
	sb.WriteString("<IsTruncated>false</IsTruncated></ListBucketResult>")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, sb.String())
}

func TestS3DestinationRoundTrip(t *testing.T) {
	f := newFakeS3()
	srv := httptest.NewServer(f)
	defer srv.Close()

	cli, err := minio.New(strings.TrimPrefix(srv.URL, "http://"), &minio.Options{
		Creds:  credentials.NewStaticV4("ak", "sk", ""),
		Secure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	d := &S3Destination{client: cli, bucket: "bkt"}
	ctx := context.Background()

	key := "backups/pg/20260101T010000Z-aaaaaaaaaaaa" + blobSuffix
	if err := d.Put(ctx, key, []byte("s3-data")); err != nil {
		t.Fatalf("put: %v", err)
	}
	metaKey := metaKeyFor(key)
	if err := d.Put(ctx, metaKey, []byte(`{"sha256":"x"}`)); err != nil {
		t.Fatalf("put meta: %v", err)
	}

	b, err := d.Get(ctx, key)
	if err != nil || string(b) != "s3-data" {
		t.Fatalf("get = %q, %v", b, err)
	}
	if _, err := d.Get(ctx, "backups/pg/missing"+blobSuffix); err == nil {
		t.Fatal("get of a missing object succeeded")
	}

	objs, err := d.List(ctx, "backups/pg/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Fatalf("list len = %d, want 2", len(objs))
	}
	// S3 listing may also surface the meta — both are .dump.enc-prefixed
	// only via the blob key filter callers apply.

	if err := d.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(ctx, key); err != nil {
		t.Fatalf("double delete: %v", err)
	}
	if _, err := d.Get(ctx, key); err == nil {
		t.Fatal("get after delete succeeded")
	}
}
