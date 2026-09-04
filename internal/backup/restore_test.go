package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// craftObject puts a real dump (compressed + encrypted with the service's
// key) into the destination under a fixed ts, returning its blob key.
func craftObject(t *testing.T, mgr *Manager, cfg store.BackupConfig, dump []byte, ts string) string {
	t.Helper()
	key, err := mgr.keyFromEnc(cfg.KeyEnc)
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := gzipBytes(dump)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := crypto.EncryptBytes(key, compressed)
	if err != nil {
		t.Fatal(err)
	}
	blobKey := backupsPrefix("pg") + ts + "-cccccccccccc" + blobSuffix
	sum := sha256.Sum256(blob)
	meta, err := encodeMeta(&Meta{
		Slug: "pg", Type: "postgres", Image: "postgres:16-alpine",
		TakenAt: "2026-01-01T01:00:00Z", Size: int64(len(blob)),
		SHA256: hex.EncodeToString(sum[:]), KeyID: keyID(key),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.dests["default"].Put(context.Background(), blobKey, blob); err != nil {
		t.Fatal(err)
	}
	if err := mgr.dests["default"].Put(context.Background(), metaKeyFor(blobKey), meta); err != nil {
		t.Fatal(err)
	}
	return blobKey
}

func TestRunRestoreSuccess(t *testing.T) {
	mgr, st, svc, rt, _ := newTestManager(t)
	cfg := enable(t, mgr, svc)

	dump := []byte("PGDMP:rows-to-bring-back")
	key := craftObject(t, mgr, cfg, dump, "20260101T010000Z")
	// Snapshot the restore file the moment pg_restore runs (the final rm
	// removes it, so post-run inspection would see nothing).
	var seen []byte
	rt.execFn = func(c execCall) (string, error) {
		if len(c.cmd) > 0 && c.cmd[0] == "pg_restore" {
			rt.mu.Lock()
			seen = append([]byte(nil), rt.files[containerRestore]...)
			rt.mu.Unlock()
		}
		return dumpScript(rt, nil)(c)
	}

	mgr.runRestore(context.Background(), svc, cfg, key)

	if !hasEvent(st, store.EventRestoreStarted) || !hasEvent(st, store.EventRestoreOK) {
		t.Fatal("expected restore_started + restore_ok events")
	}
	if hasEvent(st, store.EventRestoreFailed) {
		t.Fatal("unexpected restore_failed")
	}

	// The dump was copied into the container before pg_restore ran.
	if string(seen) != string(dump) {
		t.Fatalf("container restore file at pg_restore = %q, want the dump", seen)
	}

	// Exec sequence: terminate → drop → create → pg_restore → rm.
	calls := rt.execCalls()
	var steps []string
	for _, c := range calls {
		steps = append(steps, c.cmd[0])
		if !strings.Contains(fmt.Sprint(c.env), "PGPASSWORD=pw-secret") {
			t.Errorf("exec %v ran without PGPASSWORD", c.cmd)
		}
	}
	want := []string{"psql", "psql", "psql", "pg_restore", "rm"}
	if fmt.Sprint(steps) != fmt.Sprint(want) {
		t.Fatalf("exec order = %v, want %v", steps, want)
	}
	pgrestore := calls[3].cmd
	wantCmd := []string{"pg_restore", "-Fc", "-U", "dm", "-d", "app", "--no-owner", containerRestore}
	if fmt.Sprint(pgrestore) != fmt.Sprint(wantCmd) {
		t.Errorf("pg_restore cmd = %v, want %v", pgrestore, wantCmd)
	}
	if !strings.Contains(fmt.Sprint(calls[0].cmd), "pg_terminate_backend") {
		t.Errorf("first psql should terminate connections: %v", calls[0].cmd)
	}
	rm := calls[4].cmd
	if rm[len(rm)-1] != containerRestore {
		t.Errorf("rm should clean the container restore file: %v", rm)
	}
}

// TestRunRestoreRefusesTampered proves sha verification happens BEFORE any
// exec touches the database.
func TestRunRestoreRefusesTampered(t *testing.T) {
	mgr, st, svc, rt, _ := newTestManager(t)
	cfg := enable(t, mgr, svc)
	key := craftObject(t, mgr, cfg, []byte("dump"), "20260101T010000Z")

	blob, err := mgr.dests["default"].Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	blob[0] ^= 0xff // corrupt
	if err := mgr.dests["default"].Put(context.Background(), key, blob); err != nil {
		t.Fatal(err)
	}

	mgr.runRestore(context.Background(), svc, cfg, key)
	if !hasEvent(st, store.EventRestoreFailed) {
		t.Fatal("no restore_failed for tampered object")
	}
	if len(rt.execCalls()) != 0 {
		t.Fatalf("no exec may run on a corrupt object, got %+v", rt.execCalls())
	}
}

func TestRunRestoreRefusesCrossService(t *testing.T) {
	mgr, st, svc, rt, _ := newTestManager(t)
	cfg := enable(t, mgr, svc)
	key := craftObject(t, mgr, cfg, []byte("dump"), "20260101T010000Z")

	// Rewrite the meta claiming another service owns the object.
	var meta Meta
	metaBytes, err := mgr.dests["default"].Get(context.Background(), metaKeyFor(key))
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeMeta(metaBytes, &meta); err != nil {
		t.Fatal(err)
	}
	meta.Slug = "someone-else"
	metaBytes, _ = encodeMeta(&meta)
	if err := mgr.dests["default"].Put(context.Background(), metaKeyFor(key), metaBytes); err != nil {
		t.Fatal(err)
	}

	mgr.runRestore(context.Background(), svc, cfg, key)
	if !hasEvent(st, store.EventRestoreFailed) {
		t.Fatal("no restore_failed for cross-service object")
	}
	if len(rt.execCalls()) != 0 {
		t.Fatalf("no exec may run on a cross-service object, got %+v", rt.execCalls())
	}
}

func TestRunRestoreRefusesForeignKey(t *testing.T) {
	mgr, st, svc, rt, _ := newTestManager(t)
	cfg := enable(t, mgr, svc)
	key := craftObject(t, mgr, cfg, []byte("dump"), "20260101T010000Z")

	// Point the meta's key id at a different key entirely.
	metaBytes, err := mgr.dests["default"].Get(context.Background(), metaKeyFor(key))
	if err != nil {
		t.Fatal(err)
	}
	var meta Meta
	if err := decodeMeta(metaBytes, &meta); err != nil {
		t.Fatal(err)
	}
	var other [32]byte
	other[0] = 9
	meta.KeyID = keyID(other)
	metaBytes, _ = encodeMeta(&meta)
	if err := mgr.dests["default"].Put(context.Background(), metaKeyFor(key), metaBytes); err != nil {
		t.Fatal(err)
	}

	mgr.runRestore(context.Background(), svc, cfg, key)
	if !hasEvent(st, store.EventRestoreFailed) {
		t.Fatal("no restore_failed for foreign-key object")
	}
	if len(rt.execCalls()) != 0 {
		t.Fatal("no exec may run on a foreign-key object")
	}
}

func TestRunRestoreStoppedContainer(t *testing.T) {
	mgr, st, svc, rt, _ := newTestManager(t)
	cfg := enable(t, mgr, svc)
	key := craftObject(t, mgr, cfg, []byte("dump"), "20260101T010000Z")

	rt.running = false
	mgr.runRestore(context.Background(), svc, cfg, key)
	if !hasEvent(st, store.EventRestoreFailed) {
		t.Fatal("no restore_failed for a stopped container")
	}
	rt.mu.Lock()
	_, written := rt.files[containerRestore]
	rt.mu.Unlock()
	if written {
		t.Error("dump was copied into a stopped container")
	}
}
