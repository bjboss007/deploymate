package backup

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

func TestSaveConfigValidation(t *testing.T) {
	mgr, _, svc, _, _ := newTestManager(t)

	cases := []struct {
		name string
		cfg  store.BackupConfig
		want string
	}{
		{"bad cron", store.BackupConfig{Enabled: true, Schedule: "not a cron", Keep: 14, Destination: "default"}, "invalid schedule"},
		{"keep zero", store.BackupConfig{Enabled: true, Schedule: "0 2 * * *", Keep: 0, Destination: "default"}, "keep"},
		{"keep huge", store.BackupConfig{Enabled: true, Schedule: "0 2 * * *", Keep: 500, Destination: "default"}, "keep"},
		{"unknown destination", store.BackupConfig{Enabled: true, Schedule: "0 2 * * *", Keep: 14, Destination: "nope"}, "unknown backup destination"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := mgr.SaveConfig(svc, tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("SaveConfig = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestSaveConfigNoDestinationsAndWrongType(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var encKey [32]byte
	mgr := NewManager(st, &fakeRT{}, encKey, alerts.New(st, encKey), map[string]Destination{})
	owner, err := st.CreateUser(store.User{Email: "o@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := st.CreateProject(store.Project{UserID: owner.ID, Name: "T", Slug: "t"})
	if err != nil {
		t.Fatal(err)
	}
	mysql, err := st.CreateService(store.Service{
		ProjectID: proj.ID, Type: "mysql", Name: "My", Slug: "my",
		Image: "mysql:8.4", Status: "stopped", VolumeName: "dm-svc-my-data", Port: 3306,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := store.BackupConfig{Enabled: true, Schedule: "0 2 * * *", Keep: 14, Destination: "default"}
	if err := mgr.SaveConfig(mysql, cfg); err == nil || !strings.Contains(err.Error(), "Postgres-only") {
		t.Errorf("mysql enable = %v, want Postgres-only error", err)
	}
	if err := mgr.SaveConfig(mysql, store.BackupConfig{Schedule: "0 2 * * *", Keep: 14}); err != nil {
		t.Errorf("disabled config save should not need postgres/destinations: %v", err)
	}
	if err := mgr.SaveConfig(store.Service{ID: mysql.ID, Type: "postgres", Name: "X", Slug: "x"}, cfg); err == nil ||
		!strings.Contains(err.Error(), "no backup destinations") {
		t.Errorf("enable with no destinations = %v, want no-destinations error", err)
	}
}

func TestSaveConfigKeyLifecycle(t *testing.T) {
	mgr, st, svc, _, _ := newTestManager(t)

	// Enable → key generated, backup_enabled event.
	cfg := enable(t, mgr, svc)
	if cfg.KeyEnc == "" {
		t.Fatal("enabling did not generate a key")
	}
	if !hasEvent(st, store.EventBackupEnabled) {
		t.Fatal("no backup_enabled event")
	}
	hex1, err := mgr.ServiceKeyHex(svc.ID)
	if err != nil {
		t.Fatalf("key hex: %v", err)
	}
	if len(hex1) != 64 {
		t.Errorf("key hex length = %d, want 64", len(hex1))
	}

	// Disable → key retained (existing backups stay restorable).
	if err := mgr.SaveConfig(svc, store.BackupConfig{Schedule: "0 2 * * *", Keep: 14, Destination: "default"}); err != nil {
		t.Fatal(err)
	}
	if !hasEvent(st, store.EventBackupDisabled) {
		t.Fatal("no backup_disabled event")
	}
	got, _ := st.GetBackupConfig(svc.ID)
	if got.KeyEnc != cfg.KeyEnc {
		t.Error("disable wiped the key")
	}
	if _, err := mgr.ServiceKeyHex(svc.ID); !errors.Is(err, ErrDisabled) {
		t.Errorf("key download while disabled = %v, want ErrDisabled", err)
	}

	// Re-enable → same key retained (no rotation on a settings save).
	cfg2 := enable(t, mgr, svc)
	if cfg2.KeyEnc != cfg.KeyEnc {
		t.Error("re-enable rotated the key; expected the retained one")
	}
	hex2, err := mgr.ServiceKeyHex(svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hex1 != hex2 {
		t.Error("hex changed after re-enable")
	}
}

func TestBackupNowGates(t *testing.T) {
	mgr, st, svc, rt, _ := newTestManager(t)

	// Not enabled → ErrDisabled (no row yet).
	if err := mgr.BackupNow(svc); !errors.Is(err, ErrDisabled) {
		t.Errorf("BackupNow without config = %v, want ErrDisabled", err)
	}

	// Enabled → async run lands a backup_ok.
	enable(t, mgr, svc)
	rt.execFn = dumpScript(rt, []byte("dump"))
	if err := mgr.BackupNow(svc); err != nil {
		t.Fatalf("BackupNow = %v", err)
	}
	waitForEvent(t, st, store.EventBackupOK)

	// In flight → ErrInFlight.
	if !mgr.acquire(svc.ID) {
		t.Fatal("acquire failed")
	}
	err := mgr.BackupNow(svc)
	mgr.release(svc.ID)
	if !errors.Is(err, ErrInFlight) {
		t.Errorf("BackupNow while in flight = %v, want ErrInFlight", err)
	}

	// Disabled → ErrDisabled.
	if err := mgr.SaveConfig(svc, store.BackupConfig{Schedule: "0 2 * * *", Keep: 14, Destination: "default"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.BackupNow(svc); !errors.Is(err, ErrDisabled) {
		t.Errorf("BackupNow disabled = %v, want ErrDisabled", err)
	}
}

func TestRestoreValidatesObjectKey(t *testing.T) {
	mgr, _, svc, _, _ := newTestManager(t)
	enable(t, mgr, svc)

	cases := []string{
		"backups/other/20260101T010000Z-aaaaaaaaaaaa" + blobSuffix, // other service's prefix
		"backups/pg/../../etc/passwd",                              // traversal
		"backups/pg/20260101T010000Z-aaaaaaaaaaaa.meta.json",       // meta, not a blob
		"backups/pg/20260101T010000Z-aaaaaaaaaaaa.dump.enc.extra",  // suffix graft
	}
	for _, key := range cases {
		if err := mgr.Restore(svc, key); !errors.Is(err, ErrBadObject) {
			t.Errorf("Restore(%q) = %v, want ErrBadObject", key, err)
		}
	}
	// A service with no config row has no key to decrypt with.
	noSvc := svc
	noSvc.ID = "does-not-exist"
	noSvc.Slug = "nope"
	if err := mgr.Restore(noSvc, "backups/nope/20260101T010000Z-aaaaaaaaaaaa"+blobSuffix); !errors.Is(err, ErrNoKey) {
		t.Errorf("Restore on keyless service = %v, want ErrNoKey", err)
	}
}

func TestListBackupsNewestFirstWithSha(t *testing.T) {
	mgr, _, svc, _, _ := newTestManager(t)
	// No config row yet → no backups, no error.
	objs, err := mgr.ListBackups(context.Background(), svc)
	if err != nil || len(objs) != 0 {
		t.Fatalf("ListBackups without config = %v, %v; want empty", objs, err)
	}

	enable(t, mgr, svc)
	dest := mgr.dests["default"]
	older := "backups/pg/20260101T010000Z-aaaaaaaaaaaa" + blobSuffix
	newer := "backups/pg/20260102T010000Z-bbbbbbbbbbbb" + blobSuffix
	for _, k := range []string{older, newer} {
		if err := dest.Put(context.Background(), k, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	// Only the newer object gets a meta → sha shows for it.
	if err := dest.Put(context.Background(), metaKeyFor(newer), []byte(`{"sha256":"abc123","taken_at":"2026-01-02T01:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}

	objs, err = mgr.ListBackups(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Fatalf("len = %d, want 2", len(objs))
	}
	if objs[0].Key != newer || objs[1].Key != older {
		t.Errorf("order = %s, %s — want newest first", objs[0].Key, objs[1].Key)
	}
	if objs[0].SHA != "abc123" {
		t.Errorf("newest sha = %q, want enriched from meta", objs[0].SHA)
	}
	if objs[1].SHA != "" {
		t.Errorf("older sha = %q, want empty (no meta)", objs[1].SHA)
	}
	if !objs[0].TakenAt.Equal(objs[0].LastModified) && objs[0].TakenAt.IsZero() {
		t.Error("taken-at not parsed from key")
	}
}

// waitForEvent polls the events table until kind appears (backup runs are
// async when spawned through BackupNow).
func waitForEvent(t *testing.T, st *store.Store, kind string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if hasEvent(st, kind) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("event %s never appeared", kind)
}

var _ runtime.Runtime = (*fakeRT)(nil)
