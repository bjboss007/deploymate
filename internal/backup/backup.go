// Package backup implements per-service database snapshots: pg_dump runs
// INSIDE the service's own container (ports stay unpublished), the dump is
// encrypted with a per-service key, uploaded to a named destination
// (Cloudflare R2 through the s3 type in production, a local directory in
// dev/e2e), and restorable from the service page. Design and owner
// decisions: docs/specs/database-backups.md.
//
// Model: opt-in per service (service_backups row), default schedule daily
// 02:00 server-local, keep the newest 14 objects, "Back up now" for the
// urgent gap. The destination's object listing IS the catalog — there is no
// mirror table to drift.
package backup

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// runTimeout bounds one backup or restore. pg_dump itself runs inside the
// container via docker exec — if the deadline cuts it, the temp dump file
// is removed and the window is recorded failed (never half-counted).
const runTimeout = 30 * time.Minute

// Backup/restore result classifications — the flash-friendly text of these
// errors is shown on the service page.
var (
	ErrDisabled          = errors.New("backups are not enabled for this service")
	ErrNoKey             = errors.New("no backup key recorded for this service")
	ErrInFlight          = errors.New("a backup or restore is already running for this service")
	ErrNotPostgres       = errors.New("backups are Postgres-only in this version")
	ErrNoDestinations    = errors.New("no backup destinations are configured on this server")
	ErrBadObject         = errors.New("not one of this service's backups")
	ErrServiceNotRunning = errors.New("service container is not running")
)

// Manager runs and catalogues service backups. One per process: it owns the
// flight map that keeps backups and restores of the same service from
// overlapping, and the destination set configured at boot.
type Manager struct {
	st         *store.Store
	rt         runtime.Runtime
	encKey     [32]byte
	dispatcher *alerts.Dispatcher
	dests      map[string]Destination

	mu     sync.Mutex
	flying map[string]bool // service ids with a backup/restore in flight
}

// NewManager builds a Manager. dests is the destination set from server env
// (config.BackupDestinations → NewDestinations); an empty set means no
// service can enable backups until one is configured.
func NewManager(st *store.Store, rt runtime.Runtime, encKey [32]byte, dispatcher *alerts.Dispatcher, dests map[string]Destination) *Manager {
	return &Manager{st: st, rt: rt, encKey: encKey, dispatcher: dispatcher, dests: dests, flying: map[string]bool{}}
}

// DestinationIDs lists configured destination ids, sorted — for the UI's
// destination select and save-time validation messages.
func (m *Manager) DestinationIDs() []string {
	ids := make([]string, 0, len(m.dests))
	for id := range m.dests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// SaveConfig validates and persists a service's backup settings. Opting in
// on a keyless service generates the service's dedicated backup key (32
// random bytes, encrypted at rest under the server key) — a wiped server
// must never also wipe the key to its own off-box backups. Disabling never
// deletes a key: restore stays available for objects already taken.
func (m *Manager) SaveConfig(svc store.Service, cfg store.BackupConfig) error {
	if _, err := cron.ParseStandard(cfg.Schedule); err != nil {
		return fmt.Errorf("invalid schedule %q: %v", cfg.Schedule, err)
	}
	if cfg.Keep < 1 || cfg.Keep > 366 {
		return fmt.Errorf("keep must be between 1 and 366, got %d", cfg.Keep)
	}

	prior, err := m.st.GetBackupConfig(svc.ID)
	havePrior := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	// A settings save never wipes a stored key by omission.
	if cfg.KeyEnc == "" && havePrior {
		cfg.KeyEnc = prior.KeyEnc
	}
	cfg.ServiceID = svc.ID

	// Events below key off the transition; generate the key first.
	kind, data := store.EventBackupConfigChanged, "backup settings saved for "+svc.Name
	enabling := cfg.Enabled && (!havePrior || !prior.Enabled)
	if cfg.Enabled {
		if svc.Type != "postgres" {
			return fmt.Errorf("%w (%s support is not built yet)", ErrNotPostgres, svc.Type)
		}
		if len(m.dests) == 0 {
			return fmt.Errorf("%w (DEPLOYMATE_BACKUP_DEST_*)", ErrNoDestinations)
		}
		if _, ok := m.dests[cfg.Destination]; !ok {
			return fmt.Errorf("unknown backup destination %q (configured: %s)", cfg.Destination, strings.Join(m.DestinationIDs(), ", "))
		}
		if cfg.KeyEnc == "" {
			key, enc, err := generateServiceKey(m.encKey)
			if err != nil {
				return fmt.Errorf("generate backup key: %w", err)
			}
			cfg.KeyEnc = enc
			if enabling {
				kind = store.EventBackupEnabled
				data = "backups enabled for " + svc.Name + " with a NEW backup key — download and keep it off-box"
			} else {
				kind = store.EventBackupConfigChanged
				data = "backups re-enabled for " + svc.Name + " (existing key retained)"
			}
			_ = key // key material stays inside key_enc; hex only on download
		} else if enabling {
			kind = store.EventBackupEnabled
			data = "backups enabled for " + svc.Name + " (existing key retained)"
		}
	} else if havePrior && prior.Enabled {
		kind = store.EventBackupDisabled
		data = "backups disabled for " + svc.Name + " — existing backups stay listable and restorable"
	}

	if err := m.st.SaveBackupConfig(cfg); err != nil {
		return err
	}
	if kind != "" {
		if err := m.st.RecordEvent("", kind, data); err != nil {
			slog.Warn("backup: record event", "kind", kind, "err", err)
		}
	}
	return nil
}

// ServiceKeyHex returns the service's backup key as hex for download. The
// owner keeps one copy off-box; without it a backup made by a dead server is
// unreadable, by design. Same trust gate as webhook-secret viewing: the
// logged-in owner's session.
func (m *Manager) ServiceKeyHex(serviceID string) (string, error) {
	cfg, err := m.st.GetBackupConfig(serviceID)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrDisabled
	}
	if err != nil {
		return "", err
	}
	if !cfg.Enabled {
		return "", ErrDisabled
	}
	key, err := m.keyFromEnc(cfg.KeyEnc)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", key), nil
}

// BackupNow kicks off an immediate backup for an enabled service — the same
// run path the scheduler uses, no cron needed. Returns nil once the run is
// accepted; events carry the outcome (backup_ok / backup_failed /
// backup_skipped).
func (m *Manager) BackupNow(svc store.Service) error {
	cfg, err := m.st.GetBackupConfig(svc.ID)
	if errors.Is(err, store.ErrNotFound) {
		return ErrDisabled
	}
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return ErrDisabled
	}
	if m.inFlight(svc.ID) {
		return ErrInFlight
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
		defer cancel()
		m.run(ctx, svc, cfg)
	}()
	return nil
}

// Restore kicks off a restore of one of the service's stored objects. The
// object key is validated server-side (must live under this service's own
// prefix); the destructive part is gated by the typed confirmation at the
// handler.
func (m *Manager) Restore(svc store.Service, objectKey string) error {
	if !strings.HasPrefix(objectKey, backupsPrefix(svc.Slug)) || !strings.HasSuffix(objectKey, blobSuffix) {
		return fmt.Errorf("%w: %s", ErrBadObject, objectKey)
	}
	cfg, err := m.st.GetBackupConfig(svc.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && cfg.KeyEnc == "") {
		return ErrNoKey
	}
	if err != nil {
		return err
	}
	if m.inFlight(svc.ID) {
		return ErrInFlight
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
		defer cancel()
		m.runRestore(ctx, svc, cfg, objectKey)
	}()
	return nil
}

// ListBackups returns the service's stored backups, newest first, enriched
// best-effort with each object's meta (sha256 + taken-at). The destination
// listing is the catalog; a meta that cannot be read only blanks its row's
// sha — restore re-verifies it server-side anyway.
func (m *Manager) ListBackups(ctx context.Context, svc store.Service) ([]Object, error) {
	cfg, err := m.st.GetBackupConfig(svc.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	dest, ok := m.dests[cfg.Destination]
	if !ok {
		if len(m.dests) == 0 {
			return nil, nil
		}
		slog.Warn("backup: destination gone from config", "slug", svc.Slug, "dest", cfg.Destination)
		return nil, nil
	}
	objs, err := dest.List(ctx, backupsPrefix(svc.Slug))
	if err != nil {
		return nil, err
	}
	var out []Object
	for _, o := range objs {
		if !strings.HasSuffix(o.Key, blobSuffix) {
			continue
		}
		o.TakenAt = o.parseTakenAt()
		if meta, err := dest.Get(ctx, metaKeyFor(o.Key)); err == nil {
			var mta Meta
			if jsonErr := decodeMeta(meta, &mta); jsonErr == nil {
				o.SHA = mta.SHA256
			}
		}
		out = append(out, o)
	}
	// Key timestamps are fixed-width, so key-descending is newest-first.
	sort.Slice(out, func(i, j int) bool { return out[i].Key > out[j].Key })
	return out, nil
}

// --- flight + key plumbing ----------------------------------------------

func (m *Manager) inFlight(serviceID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.flying[serviceID]
}

func (m *Manager) acquire(serviceID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flying[serviceID] {
		return false
	}
	m.flying[serviceID] = true
	return true
}

func (m *Manager) release(serviceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.flying, serviceID)
}

// keyFromEnc decrypts a stored backup key (crypto.Encrypt envelope) back
// into the 32 raw bytes the blob AEAD runs under.
func (m *Manager) keyFromEnc(keyEnc string) ([32]byte, error) {
	var key [32]byte
	if keyEnc == "" {
		return key, ErrNoKey
	}
	raw, err := crypto.Decrypt(m.encKey, keyEnc)
	if err != nil {
		return key, fmt.Errorf("decrypt backup key: %w", err)
	}
	if len(raw) != len(key) {
		return key, fmt.Errorf("backup key has wrong size %d, want %d", len(raw), len(key))
	}
	copy(key[:], raw)
	return key, nil
}

// markHandled records that a schedule window was seen (ran, failed, or
// skipped-because-stopped). The scheduler's next() computes from last_run_at,
// so persisting it is what makes a server restart unable to re-fire a window
// or a skipped service retry-storm every minute.
func (m *Manager) markHandled(serviceID string) {
	if err := m.st.SetBackupLastRun(serviceID, store.Now()); err != nil {
		slog.Warn("backup: record last run", "service", serviceID, "err", err)
	}
}

// creds decrypts the service's stored credentials (same pair DATABASE_URL
// assembly uses — nothing new to manage).
func (m *Manager) creds(svc store.Service) (map[string]string, error) {
	enc, err := m.st.GetServiceCredentials(svc.ID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(enc))
	for k, v := range enc {
		plain, err := crypto.Decrypt(m.encKey, v)
		if err != nil {
			return nil, fmt.Errorf("decrypt credential %q: %w", k, err)
		}
		out[k] = plain
	}
	return out, nil
}

// generateServiceKey makes a service's dedicated backup key and returns it
// encrypted at rest under the server key.
func generateServiceKey(encKey [32]byte) ([32]byte, string, error) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return key, "", err
	}
	enc, err := crypto.Encrypt(encKey, string(key[:]))
	return key, enc, err
}
