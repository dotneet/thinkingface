// Package store is the relational data access layer, backed by PostgreSQL or
// SQLite depending on the DATABASE_URL scheme. It holds only the metadata
// needed to serve listings quickly; file bytes live in GCS and the
// authoritative history lives in git.
//
// Every query is written once against the querier / dialect pair
// (querier.go, dialect.go): Postgres runs on pgx natively, SQLite on
// database/sql + modernc.org/sqlite (pure Go, no CGo). See dialect.go for
// where the two engines are allowed to differ.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

//go:embed all:migrations
var migrationsFS embed.FS

var ErrNotFound = errors.New("store: not found")
var ErrConflict = errors.New("store: already exists")

// ErrLastAdmin is returned by the membership methods when a change would
// leave an organisation with no admin at all (docs/organization-design.md
// §5): demoting, removing, or leaving as the only admin. Someone must always
// be able to administer the organisation, so the caller has to appoint
// another admin first.
var ErrLastAdmin = errors.New("store: organisation would be left without an admin")

// ErrLFSObjectGone is returned by RecordLFSObject when the object is no
// longer in object storage after the lfs_objects row lock is acquired.
// An LFS upload batch that already Stat'ed a hit must treat this as "not
// present" and issue an upload action; otherwise the client never
// re-uploads and the content is gone.
var ErrLFSObjectGone = errors.New("store: lfs object is no longer in storage")

type Store struct {
	db querier
	d  dialect
}

// Open connects to the database named by databaseURL. The scheme selects the
// engine: postgres:// and postgresql:// use pgx, sqlite:// (followed by a
// file path, e.g. sqlite:///data/db/thinkingface.db or sqlite://relative.db)
// uses the embedded SQLite engine.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	switch {
	case strings.HasPrefix(databaseURL, "postgres://"), strings.HasPrefix(databaseURL, "postgresql://"):
		q, err := openPostgres(ctx, databaseURL)
		if err != nil {
			return nil, err
		}
		return &Store{db: q, d: pgDialect{}}, nil
	case strings.HasPrefix(databaseURL, "sqlite:"):
		q, err := openSQLite(ctx, databaseURL)
		if err != nil {
			return nil, err
		}
		return &Store{db: q, d: sqliteDialect{}}, nil
	default:
		return nil, fmt.Errorf("DATABASE_URL must start with postgres://, postgresql:// or sqlite://")
	}
}

// Dialect reports which engine backs the store ("postgres" or "sqlite").
// It exists for logging and for tests; nothing outside this package should
// branch on it.
func (s *Store) Dialect() string { return s.d.name() }

func (s *Store) Close() { s.db.Close() }

// WaitReady blocks until the database answers or the deadline passes. Compose
// health checks cover most of this, but a cold Cloud SQL instance can still
// refuse the first few connections.
func (s *Store) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := s.db.Ping(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("database not ready after %s: %w", timeout, lastErr)
}

// Migrate applies every embedded migration for the active engine that has
// not run yet. Migrations live in migrations/<engine>/ and are recorded in
// schema_migrations by bare file name, which is what keeps existing Postgres
// databases from re-running anything after the files moved into a
// subdirectory.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, s.d.schemaMigrationsDDL()); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	dir := "migrations/" + s.d.name()
	entries, err := migrationsFS.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var exists bool
		if err := s.db.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile(dir + "/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}
