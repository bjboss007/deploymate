package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// run executes one backup of svc under its config: flight-gated, and the
// window counts as handled (last_run_at) however it ends — ran, failed, or
// skipped-because-stopped. Only an already-running flight returns without
// marking anything.
func (m *Manager) run(ctx context.Context, svc store.Service, cfg store.BackupConfig) {
	if !m.acquire(svc.ID) {
		slog.Info("backup: skipped, run in flight", "slug", svc.Slug)
		return
	}
	defer m.release(svc.ID)
	defer m.markHandled(svc.ID)

	name := services.ContainerName(svc.Slug)
	info, err := m.rt.Inspect(ctx, name)
	if errors.Is(err, runtime.ErrContainerNotFound) || (err == nil && !info.Running) {
		_ = m.st.RecordEvent("", store.EventBackupSkipped, "backup skipped for "+svc.Name+": container not running")
		slog.Info("backup skipped", "slug", svc.Slug, "err", err)
		return
	}
	if err != nil {
		m.fail(svc, "inspect", err)
		return
	}
	if svc.Type != "postgres" {
		m.fail(svc, "type", fmt.Errorf("%w (%s)", ErrNotPostgres, svc.Type))
		return
	}
	key, err := m.keyFromEnc(cfg.KeyEnc)
	if err != nil {
		m.fail(svc, "key", err)
		return
	}
	creds, err := m.creds(svc)
	if err != nil {
		m.fail(svc, "credentials", err)
		return
	}
	user, db := creds["user"], creds["db"]
	if user == "" || db == "" {
		m.fail(svc, "credentials", errors.New("service has no stored user/db credentials"))
		return
	}

	// 1. Dump INSIDE the container (ports stay unpublished — nothing outside
	// the docker network can reach the database, so the dump must run there).
	out, err := m.rt.ExecEnv(ctx, name, []string{"pg_dump", "-Fc", "-U", user, "-d", db, "-f", containerDump}, pgEnv(creds))
	if err != nil {
		m.fail(svc, "pg_dump", fmt.Errorf("%w: %s", err, out))
		return
	}
	// Best-effort cleanup of the container-side dump whatever happens next.
	defer func() {
		if _, err := m.rt.Exec(ctx, name, []string{"rm", "-f", containerDump}); err != nil {
			slog.Warn("backup: container dump cleanup", "slug", svc.Slug, "err", err)
		}
	}()

	// 2. Copy the dump out, gzip, encrypt with the service's own key. The
	// blob stays in memory — fine for MVP sizes; streaming (tar to the
	// destination) is the natural follow-up if dumps ever grow.
	data, err := m.rt.ReadFile(ctx, name, containerDump)
	if err != nil {
		m.fail(svc, "read dump", err)
		return
	}
	compressed, err := gzipBytes(data)
	if err != nil {
		m.fail(svc, "gzip", err)
		return
	}
	blob, err := crypto.EncryptBytes(key, compressed)
	if err != nil {
		m.fail(svc, "encrypt", err)
		return
	}

	// 3. Upload blob then its plaintext meta; the object key is the
	// timestamp (sortable, and the ts-in-key IS the retention clock).
	dest, ok := m.dests[cfg.Destination]
	if !ok {
		m.fail(svc, "destination", fmt.Errorf("destination %q is not configured on this server anymore", cfg.Destination))
		return
	}
	now := time.Now().UTC()
	k, err := blobKey(svc.Slug, now)
	if err != nil {
		m.fail(svc, "object key", err)
		return
	}
	sum := sha256.Sum256(blob)
	meta, err := encodeMeta(&Meta{
		Slug: svc.Slug, Type: svc.Type, Image: svc.Image,
		TakenAt: now.Format(time.RFC3339), Size: int64(len(blob)),
		SHA256: hex.EncodeToString(sum[:]), KeyID: keyID(key),
	})
	if err != nil {
		m.fail(svc, "meta", err)
		return
	}
	if err := dest.Put(ctx, k, blob); err != nil {
		m.fail(svc, "upload", err)
		return
	}
	if err := dest.Put(ctx, metaKeyFor(k), meta); err != nil {
		// Blob up, meta failed: the object exists but is not catalogued.
		// Fail the run so the next window re-dumps; prune clears the orphan
		// once it ages past keep.
		m.fail(svc, "upload meta", err)
		return
	}

	// 4. Retention: keep the newest cfg.Keep objects. A prune failure is a
	// warning — never a failure of the fresh backup.
	if err := m.prune(ctx, svc, cfg); err != nil {
		msg := "retention prune failed for " + svc.Name + ": " + err.Error()
		if rerr := m.st.RecordEvent("", store.EventBackupPruneFailed, msg); rerr != nil {
			slog.Warn("backup: record prune event", "err", rerr)
		}
		slog.Warn("backup: prune", "slug", svc.Slug, "err", err)
	}

	_ = m.st.RecordEvent("", store.EventBackupOK,
		fmt.Sprintf("backup of %s: %d bytes, key %s", svc.Name, len(blob), keyID(key)))
	slog.Info("backup ok", "slug", svc.Slug, "size", len(blob))
}

// fail records the failure event + alert for a backup run.
func (m *Manager) fail(svc store.Service, step string, err error) {
	msg := fmt.Sprintf("backup of %s failed (%s): %v", svc.Name, step, err)
	if rerr := m.st.RecordEvent("", store.EventBackupFailed, msg); rerr != nil {
		slog.Warn("backup: record failure event", "err", rerr)
	}
	m.dispatcher.Notify(alerts.EventBackupFailed, "Backup failed for "+svc.Name, msg)
	slog.Error("backup failed", "slug", svc.Slug, "step", step, "err", err)
}

// prune deletes all but the newest cfg.Keep objects for the service. The ts
// in each key orders them; blob+meta are deleted as pairs. Idempotent and
// best-effort: an error stops the sweep (the next successful backup retries
// it).
func (m *Manager) prune(ctx context.Context, svc store.Service, cfg store.BackupConfig) error {
	dest, ok := m.dests[cfg.Destination]
	if !ok {
		return fmt.Errorf("destination %q not configured", cfg.Destination)
	}
	objs, err := dest.List(ctx, backupsPrefix(svc.Slug))
	if err != nil {
		return err
	}
	var blobs []Object
	for _, o := range objs {
		if strings.HasSuffix(o.Key, blobSuffix) {
			blobs = append(blobs, o)
		}
	}
	if len(blobs) <= cfg.Keep {
		return nil
	}
	sort.Slice(blobs, func(i, j int) bool { return blobs[i].Key > blobs[j].Key }) // newest first
	var firstErr error
	for _, old := range blobs[cfg.Keep:] {
		if err := dest.Delete(ctx, old.Key); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := dest.Delete(ctx, metaKeyFor(old.Key)); err != nil {
			// Blob gone but its meta lingers — listers skip meta files, so
			// this is only cosmetic litter.
			slog.Warn("backup: prune meta", "key", old.Key, "err", err)
		}
		slog.Info("backup: pruned", "slug", svc.Slug, "key", old.Key)
	}
	return firstErr
}

// pgEnv returns the PGPASSWORD override when the service has a stored
// password (belt-and-braces — the image's pg_hba trusts the local socket,
// which is what the provisioner's own readiness probe relies on).
func pgEnv(creds map[string]string) []string {
	if pw := creds["password"]; pw != "" {
		return []string{"PGPASSWORD=" + pw}
	}
	return nil
}
