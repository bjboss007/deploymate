package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// identRe guards the db/user identifiers interpolated into restore SQL.
// Both come from our own credential templates ("app"/"dm"), but a corrupted
// credential must fail loudly, never become SQL.
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// runRestore replays one stored object into the service's database:
// download → verify sha → decrypt → terminate connections → drop/recreate
// the app database → pg_restore. Flight-gated like backups — a restore and
// a backup of the same service must never overlap.
func (m *Manager) runRestore(ctx context.Context, svc store.Service, cfg store.BackupConfig, objectKey string) {
	if !m.acquire(svc.ID) {
		slog.Info("restore: skipped, run in flight", "slug", svc.Slug)
		return
	}
	defer m.release(svc.ID)

	name := services.ContainerName(svc.Slug)
	_ = m.st.RecordEvent("", store.EventRestoreStarted,
		fmt.Sprintf("restore of %s from %s started", svc.Name, objectKey))

	fail := func(step string, err error) {
		msg := fmt.Sprintf("restore of %s failed (%s): %v", svc.Name, step, err)
		if rerr := m.st.RecordEvent("", store.EventRestoreFailed, msg); rerr != nil {
			slog.Warn("backup: record restore event", "err", rerr)
		}
		m.dispatcher.Notify(alerts.EventRestoreFailed, "Restore failed for "+svc.Name, msg)
		slog.Error("restore failed", "slug", svc.Slug, "step", step, "err", err)
	}

	dest, ok := m.dests[cfg.Destination]
	if !ok {
		fail("destination", fmt.Errorf("destination %q is not configured on this server anymore", cfg.Destination))
		return
	}

	// 1. Fetch meta + blob; verify everything BEFORE touching the database.
	metaBytes, err := dest.Get(ctx, metaKeyFor(objectKey))
	if err != nil {
		fail("read meta", err)
		return
	}
	var meta Meta
	if err := decodeMeta(metaBytes, &meta); err != nil {
		fail("meta", err)
		return
	}
	if meta.Slug != svc.Slug {
		fail("meta", fmt.Errorf("object belongs to service %q, refusing cross-service restore", meta.Slug))
		return
	}
	blob, err := dest.Get(ctx, objectKey)
	if err != nil {
		fail("download", err)
		return
	}
	sum := sha256.Sum256(blob)
	if hex.EncodeToString(sum[:]) != meta.SHA256 {
		fail("verify", fmt.Errorf("sha256 mismatch: object is corrupt or was tampered with"))
		return
	}
	key, err := m.keyFromEnc(cfg.KeyEnc)
	if err != nil {
		fail("key", err)
		return
	}
	if keyID(key) != meta.KeyID {
		fail("key", fmt.Errorf("object was encrypted with key %s, this service holds %s — a different backup's object", meta.KeyID, keyID(key)))
		return
	}
	compressed, err := crypto.DecryptBytes(key, blob)
	if err != nil {
		fail("decrypt", err)
		return
	}
	dump, err := gunzipBytes(compressed)
	if err != nil {
		fail("decompress", err)
		return
	}

	// 2. The service must be running to receive the dump.
	info, err := m.rt.Inspect(ctx, name)
	if errors.Is(err, runtime.ErrContainerNotFound) || (err == nil && !info.Running) {
		fail("container", ErrServiceNotRunning)
		return
	}
	if err != nil {
		fail("inspect", err)
		return
	}

	// 3. Copy the dump in, then run the clean-slate sequence. Apps keep
	// running through the window; their clients reconnect after (the demo
	// apps degrade/recover gracefully).
	creds, err := m.creds(svc)
	if err != nil {
		fail("credentials", err)
		return
	}
	user, db := creds["user"], creds["db"]
	if user == "" || db == "" || !identRe.MatchString(user) || !identRe.MatchString(db) {
		fail("credentials", errors.New("service has no usable user/db credentials"))
		return
	}
	if err := m.rt.WriteFile(ctx, name, containerRestore, dump); err != nil {
		fail("copy dump in", err)
		return
	}
	env := pgEnv(creds)
	steps := [][]string{
		// Terminate app-DB connections first — drop fails on live clients.
		{"psql", "-U", user, "-d", "postgres", "-c",
			fmt.Sprintf("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()", db)},
		{"psql", "-U", user, "-d", "postgres", "-c", fmt.Sprintf("DROP DATABASE IF EXISTS %s", db)},
		{"psql", "-U", user, "-d", "postgres", "-c", fmt.Sprintf("CREATE DATABASE %s OWNER %s", db, user)},
		{"pg_restore", "-Fc", "-U", user, "-d", db, "--no-owner", containerRestore},
		{"rm", "-f", containerRestore},
	}
	for _, step := range steps {
		out, err := m.rt.ExecEnv(ctx, name, step, env)
		if err != nil {
			// Leave the temp dump in place on failure — a human can inspect
			// it; the next restore overwrites it anyway.
			fail("step "+step[0], fmt.Errorf("%w: %s", err, out))
			return
		}
	}

	_ = m.st.RecordEvent("", store.EventRestoreOK,
		fmt.Sprintf("restore of %s from %s completed", svc.Name, objectKey))
	slog.Info("restore ok", "slug", svc.Slug, "key", objectKey)
}
