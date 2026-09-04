package store

import (
	"errors"
	"path/filepath"
	"testing"
)

// newBackupTestStore seeds an owner/project/service the backup rows hang off.
func newBackupTestStore(t *testing.T) (*Store, Service) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	owner, err := st.CreateUser(User{Email: "o@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	proj, err := st.CreateProject(Project{UserID: owner.ID, Name: "T", Slug: "t"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	sv, err := st.CreateService(Service{
		ProjectID: proj.ID, Type: "postgres", Name: "PG", Slug: "pg",
		Image: "postgres:16-alpine", Status: "running", VolumeName: "dm-svc-pg-data", Port: 5432,
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	return st, sv
}

func TestBackupConfigNotFound(t *testing.T) {
	st, sv := newBackupTestStore(t)
	if _, err := st.GetBackupConfig(sv.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetBackupConfig on a fresh service = %v, want ErrNotFound", err)
	}
}

func TestBackupConfigDefaultsAndUpsert(t *testing.T) {
	st, sv := newBackupTestStore(t)

	// Opt in with explicit settings; defaults are the schema's, applied only
	// when the caller saves a zero config.
	if err := st.SaveBackupConfig(BackupConfig{
		ServiceID: sv.ID, Enabled: true, Schedule: "13 * * * *", Keep: 3,
		Destination: "dev", KeyEnc: "v1:key", LastRunAt: "t1",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := st.GetBackupConfig(sv.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := BackupConfig{ServiceID: sv.ID, Enabled: true, Schedule: "13 * * * *", Keep: 3,
		Destination: "dev", KeyEnc: "v1:key", LastRunAt: "t1"}
	if got != want {
		t.Errorf("GetBackupConfig = %+v, want %+v", got, want)
	}

	// Upsert overwrites in place (1:1 row, no duplicates).
	if err := st.SaveBackupConfig(BackupConfig{ServiceID: sv.ID, Enabled: false, Schedule: "0 2 * * *", Keep: 14}); err != nil {
		t.Fatalf("save update: %v", err)
	}
	got, err = st.GetBackupConfig(sv.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	// The upsert replaces everything except last_run_at (its only writer is
	// SetBackupLastRun, so config saves can never reset window bookkeeping).
	if got.Enabled || got.Schedule != "0 2 * * *" || got.Keep != 14 || got.Destination != "default" || got.KeyEnc != "" || got.LastRunAt != "t1" {
		t.Errorf("upsert did not replace the row cleanly: %+v", got)
	}

	// Sanity: still one row.
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM service_backups WHERE service_id = ?`, sv.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("row count = %d, want 1", n)
	}
}

func TestBackupConfigDefaultsOnInsert(t *testing.T) {
	st, sv := newBackupTestStore(t)
	if err := st.SaveBackupConfig(BackupConfig{ServiceID: sv.ID, Enabled: true}); err != nil {
		t.Fatalf("save: %v", err)
	}
	var schedule string
	var keep int
	var destination string
	if err := st.db.QueryRow(`SELECT schedule, keep, destination FROM service_backups WHERE service_id = ?`, sv.ID).
		Scan(&schedule, &keep, &destination); err != nil {
		t.Fatal(err)
	}
	if schedule != DefaultBackupSchedule || keep != DefaultBackupKeep || destination != "default" {
		t.Errorf("defaults = %q/%d/%q, want %q/%d/default", schedule, keep, destination, DefaultBackupSchedule, DefaultBackupKeep)
	}
}

func TestSetBackupLastRun(t *testing.T) {
	st, sv := newBackupTestStore(t)
	if err := st.SaveBackupConfig(BackupConfig{ServiceID: sv.ID, Enabled: true}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := st.SetBackupLastRun(sv.ID, "2026-09-04T02:00:00Z"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := st.GetBackupConfig(sv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastRunAt != "2026-09-04T02:00:00Z" {
		t.Errorf("LastRunAt = %q", got.LastRunAt)
	}
}

func TestListEnabledBackupTargets(t *testing.T) {
	st, sv := newBackupTestStore(t)
	owner, err := st.CreateUser(User{Email: "o2@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := st.CreateProject(Project{UserID: owner.ID, Name: "U", Slug: "u"})
	if err != nil {
		t.Fatal(err)
	}
	offSvc, err := st.CreateService(Service{
		ProjectID: proj.ID, Type: "postgres", Name: "Off", Slug: "off",
		Image: "postgres:16-alpine", Status: "stopped", VolumeName: "dm-svc-off-data", Port: 5432,
	})
	if err != nil {
		t.Fatal(err)
	}

	// sv enabled, offSvc disabled, second store's sv untouched (absent row).
	if err := st.SaveBackupConfig(BackupConfig{ServiceID: sv.ID, Enabled: true, KeyEnc: "v1:k"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveBackupConfig(BackupConfig{ServiceID: offSvc.ID, Enabled: false}); err != nil {
		t.Fatal(err)
	}

	targets, err := st.ListEnabledBackupTargets()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("len = %d, want 1", len(targets))
	}
	got := targets[0]
	if got.ServiceID != sv.ID || got.Service.Slug != "pg" || !got.Enabled || got.KeyEnc != "v1:k" {
		t.Errorf("target = %+v, want the enabled pg service with its key", got)
	}
}
