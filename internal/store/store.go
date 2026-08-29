// Package store owns the SQLite metadata database: opening, migrations, and
// domain queries. All other packages go through Store — nothing touches SQL
// directly.
//
// SQLite specifics: WAL journal + busy_timeout so the single worker and the
// HTTP handlers never deadlock on writes; SetMaxOpenConns(1) keeps writes
// serialized and safe with modernc.org/sqlite.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
	"github.com/pressly/goose/v3"

	"github.com/habibmuhammad/deploymate/internal/migrations"
)

// Environment values for apps and services. Production is the default
// everywhere; staging apps and services are scoped to each other.
const (
	EnvProduction = "production"
	EnvStaging    = "staging"
)

// Service origin: who created the service. A manifest deploy may flag a
// manifest-origin service as orphaned when it stops declaring that type;
// manual services are never flagged. See migration 0010.
const (
	OriginManual   = "manual"
	OriginManifest = "manifest"
)

// Store wraps the database handle.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the database at path, applies pending migrations,
// and tunes SQLite for our access pattern.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	// goose's sqlite3 dialect works with any registered driver named
	// "sqlite" (modernc registers as such).
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(db, "."); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for queries written outside this package.
func (s *Store) DB() *sql.DB { return s.db }

// Now returns the current time as the RFC3339 string used throughout the
// schema's *_at columns.
func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
