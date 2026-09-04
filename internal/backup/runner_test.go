package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// dumpScript makes execFn behave like a real container: pg_dump writes the
// dump file, rm removes it.
func dumpScript(rt *fakeRT, dump []byte) func(c execCall) (string, error) {
	return func(c execCall) (string, error) {
		if len(c.cmd) == 0 {
			return "", nil
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		switch c.cmd[0] {
		case "pg_dump":
			rt.files[containerDump] = append([]byte(nil), dump...)
		case "rm":
			delete(rt.files, c.cmd[len(c.cmd)-1])
		}
		return "", nil
	}
}

func TestRunBackupSuccess(t *testing.T) {
	mgr, st, svc, rt, destDir := newTestManager(t)
	cfg := enable(t, mgr, svc)

	dump := []byte("PGDMP:custom-format-bytes-0123456789")
	rt.execFn = dumpScript(rt, dump)
	mgr.run(context.Background(), svc, cfg)

	if !hasEvent(st, store.EventBackupOK) {
		t.Fatal("no backup_ok event")
	}
	if hasEvent(st, store.EventBackupFailed) || hasEvent(st, store.EventBackupSkipped) {
		t.Fatal("unexpected failure/skip event")
	}
	if lastRunAt(t, st, svc) == "" {
		t.Fatal("last_run_at not persisted")
	}

	// Exactly one blob + one meta in the destination.
	var blobs, metas []string
	err := filepath.WalkDir(destDir, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(destDir, p)
		if strings.HasSuffix(rel, blobSuffix) {
			blobs = append(blobs, rel)
		} else if strings.HasSuffix(rel, metaSuffix) {
			metas = append(metas, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(blobs) != 1 || len(metas) != 1 {
		t.Fatalf("want 1 blob + 1 meta, got %d + %d (%v %v)", len(blobs), len(metas), blobs, metas)
	}

	// Blob decrypts with the service key and gunzips back to the dump.
	key, err := mgr.keyFromEnc(cfg.KeyEnc)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(filepath.Join(destDir, filepath.FromSlash(blobs[0])))
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := crypto.DecryptBytes(key, blob)
	if err != nil {
		t.Fatalf("decrypt blob: %v", err)
	}
	back, err := gunzipBytes(compressed)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	if string(back) != string(dump) {
		t.Errorf("round trip = %q, want %q", back, dump)
	}

	// Meta is plaintext and carries the encrypted blob's digest + key id.
	metaBytes, err := os.ReadFile(filepath.Join(destDir, filepath.FromSlash(metas[0])))
	if err != nil {
		t.Fatal(err)
	}
	var meta Meta
	if err := decodeMeta(metaBytes, &meta); err != nil {
		t.Fatalf("meta decode: %v", err)
	}
	sum := sha256.Sum256(blob)
	if meta.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("meta sha = %s, want blob sha", meta.SHA256)
	}
	if meta.Slug != svc.Slug || meta.Type != "postgres" || meta.KeyID != keyID(key) || meta.Size != int64(len(blob)) {
		t.Errorf("meta = %+v", meta)
	}

	// The dump ran inside the container with the right command + PGPASSWORD,
	// and the temp file was cleaned up.
	calls := rt.execCalls()
	if len(calls) < 1 || calls[0].cmd[0] != "pg_dump" {
		t.Fatalf("first exec = %+v, want pg_dump", calls)
	}
	cmd := calls[0].cmd
	want := []string{"pg_dump", "-Fc", "-U", "dm", "-d", "app", "-f", containerDump}
	if fmt.Sprint(cmd) != fmt.Sprint(want) {
		t.Errorf("pg_dump cmd = %v, want %v", cmd, want)
	}
	if !strings.Contains(fmt.Sprint(calls[0].env), "PGPASSWORD=pw-secret") {
		t.Errorf("env = %v, want PGPASSWORD", calls[0].env)
	}
	rt.mu.Lock()
	_, stillThere := rt.files[containerDump]
	rt.mu.Unlock()
	if stillThere {
		t.Error("container dump file not cleaned up")
	}
}

// TestRunBackupPruneKeepsNewest proves retention: with keep=1 a successful
// run removes the two older pre-seeded pairs and keeps only its own.
func TestRunBackupPruneKeepsNewest(t *testing.T) {
	mgr, st, svc, rt, destDir := newTestManager(t)
	cfg := enable(t, mgr, svc, func(c *store.BackupConfig) { c.Keep = 1 })

	dest := mgr.dests["default"]
	oldKeys := []string{
		"backups/pg/20260101T010000Z-aaaaaaaaaaaa" + blobSuffix,
		"backups/pg/20260102T010000Z-bbbbbbbbbbbb" + blobSuffix,
	}
	for _, k := range oldKeys {
		if err := dest.Put(context.Background(), k, []byte("old")); err != nil {
			t.Fatal(err)
		}
		if err := dest.Put(context.Background(), metaKeyFor(k), []byte(`{"slug":"pg"}`)); err != nil {
			t.Fatal(err)
		}
	}

	rt.execFn = dumpScript(rt, []byte("dump"))
	mgr.run(context.Background(), svc, cfg)

	if !hasEvent(st, store.EventBackupOK) {
		t.Fatal("no backup_ok")
	}
	if hasEvent(st, store.EventBackupPruneFailed) {
		t.Fatal("unexpected prune failure")
	}
	objs, err := dest.List(context.Background(), backupsPrefix(svc.Slug))
	if err != nil {
		t.Fatal(err)
	}
	var blobs int
	for _, o := range objs {
		if strings.HasSuffix(o.Key, blobSuffix) {
			blobs++
		}
	}
	if blobs != 1 {
		t.Fatalf("blob count after prune = %d, want 1 (keep=1)", blobs)
	}
	// The survivor is the newest — the fresh run's own object, not the
	// pre-seeded 202601xx pairs.
	survived := ""
	for _, o := range objs {
		if strings.HasSuffix(o.Key, blobSuffix) {
			survived = o.Key
		}
	}
	if strings.Contains(survived, "2026010") {
		t.Errorf("old object %s survived; keep=1 must prune the two pre-seeded pairs", survived)
	}
	_ = destDir
}

// TestPruneFailureDoesNotFailRun: retention errors are warnings — the fresh
// backup itself stays a backup_ok.
func TestPruneFailureDoesNotFailRun(t *testing.T) {
	mgr, st, svc, rt, _ := newTestManager(t)
	cfg := enable(t, mgr, svc, func(c *store.BackupConfig) { c.Keep = 1 })
	// Pre-seed one old object so the sweep actually has something to delete.
	if err := mgr.dests["default"].Put(context.Background(), "backups/pg/20260101T010000Z-aaaaaaaaaaaa"+blobSuffix, []byte("old")); err != nil {
		t.Fatal(err)
	}
	mgr.dests["default"] = &failingDeleteDest{inner: mgr.dests["default"]}

	rt.execFn = dumpScript(rt, []byte("dump"))
	mgr.run(context.Background(), svc, cfg)

	if !hasEvent(st, store.EventBackupOK) {
		t.Fatal("no backup_ok — prune failure must not fail the run")
	}
	if !hasEvent(st, store.EventBackupPruneFailed) {
		t.Fatal("no backup_prune_failed event")
	}
}

type failingDeleteDest struct{ inner Destination }

func (d *failingDeleteDest) Put(ctx context.Context, k string, b []byte) error {
	return d.inner.Put(ctx, k, b)
}
func (d *failingDeleteDest) Get(ctx context.Context, k string) ([]byte, error) {
	return d.inner.Get(ctx, k)
}
func (d *failingDeleteDest) List(ctx context.Context, p string) ([]Object, error) {
	return d.inner.List(ctx, p)
}
func (d *failingDeleteDest) Delete(context.Context, string) error {
	return errors.New("bucket write forbidden")
}

func TestRunBackupStoppedContainerSkips(t *testing.T) {
	mgr, st, svc, rt, destDir := newTestManager(t)
	cfg := enable(t, mgr, svc)

	rt.running = false
	mgr.run(context.Background(), svc, cfg)

	if !hasEvent(st, store.EventBackupSkipped) {
		t.Fatal("no backup_skipped event")
	}
	if hasEvent(st, store.EventBackupFailed) {
		t.Fatal("stopped container must skip, not fail")
	}
	// The window is handled: no retry storm next minute.
	if lastRunAt(t, st, svc) == "" {
		t.Fatal("last_run_at not set on skip")
	}
	if len(rt.execCalls()) != 0 {
		t.Fatalf("no execs expected for a stopped container, got %+v", rt.execCalls())
	}
	entries, err := os.ReadDir(destDir)
	if err == nil && len(entries) != 0 {
		t.Errorf("nothing should be uploaded for a skipped run, found %v", entries)
	}
}

func TestRunBackupMissingContainerSkips(t *testing.T) {
	mgr, st, svc, rt, _ := newTestManager(t)
	cfg := enable(t, mgr, svc)
	rt.inspectErr = runtime.ErrContainerNotFound
	mgr.run(context.Background(), svc, cfg)
	if !hasEvent(st, store.EventBackupSkipped) {
		t.Fatal("no backup_skipped event for a missing container")
	}
	if hasEvent(st, store.EventBackupFailed) {
		t.Fatal("missing container must skip, not fail")
	}
}

func TestRunBackupDumpFailure(t *testing.T) {
	mgr, st, svc, rt, _ := newTestManager(t)
	cfg := enable(t, mgr, svc)
	rt.execFn = func(c execCall) (string, error) {
		if len(c.cmd) > 0 && c.cmd[0] == "pg_dump" {
			return "pg_dump: error", errors.New("exit status 1")
		}
		return "", nil
	}
	mgr.run(context.Background(), svc, cfg)

	if !hasEvent(st, store.EventBackupFailed) {
		t.Fatal("no backup_failed event")
	}
	if lastRunAt(t, st, svc) == "" {
		t.Fatal("last_run_at not set on failure — window must be handled")
	}
}
