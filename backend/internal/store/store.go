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
// leave an organisation with no admin at all (docs/dev/organization-design.md
// §5): demoting, removing, or leaving as the only admin. Someone must always
// be able to administer the organisation, so the caller has to appoint
// another admin first.
var ErrLastAdmin = errors.New("store: organisation would be left without an admin")

// ErrLastSiteAdmin is the instance-wide counterpart of ErrLastAdmin: clearing
// users.is_admin on the last remaining site administrator would leave nobody
// able to reset a password or appoint another administrator, and the only way
// back would be editing the database by hand. It is a separate sentinel from
// ErrLastAdmin so a handler cannot answer an organisation question with a
// site-wide message, or the other way round.
var ErrLastSiteAdmin = errors.New("store: instance would be left without a site administrator")

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

// bootstrapLockID separates the start-up locks WithBootstrapLock hands out.
// They are hashed with the name, so the number only has to be stable.
const bootstrapLockID = 0

// WithBootstrapLock runs fn while no other process is running it against the
// same database.
//
// Start-up work is the one place where several processes reliably do the same
// thing at the same instant: a Cloud Run revision rolls out N containers
// together, and `gc` / `compact` run beside them. Anything shaped as "check
// whether it has been done, and do it if not" -- Migrate, and seeding the
// first administrator -- has both processes evaluate the check before either
// writes, so one of them then fails on a constraint and the process exits.
// Postgres rolls DDL back, so nothing is corrupted; the cost is a crash loop
// on exactly the deploy that was supposed to be uneventful.
//
// Postgres holds a transaction-scoped advisory lock in a transaction of its
// own, released the moment fn is done. That transaction writes nothing and
// runs on its own pooled connection, so fn's own statements are unaffected.
//
// SQLite takes no lock at all, and must not: the engine allows one writer,
// this package gives it a pool of exactly one connection, and an open
// transaction held across fn would deadlock against fn's first write. Its
// deployment is a single process by construction
// (docs/dev/thinkingface-design.md §10), which is the same assumption
// sqliteDialect.advisoryXactLock already encodes.
func (s *Store) WithBootstrapLock(ctx context.Context, name string, fn func(context.Context) error) error {
	if s.d.name() != "postgres" {
		return fn(ctx)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin %s lock: %w", name, err)
	}
	// Rollback, not Commit: the transaction exists only to scope the lock and
	// has nothing to write. Either one releases it.
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.d.advisoryXactLock(ctx, tx, name, bootstrapLockID); err != nil {
		return fmt.Errorf("take %s lock: %w", name, err)
	}
	return fn(ctx)
}

// Migrate applies every embedded migration for the active engine that has
// not run yet. Migrations live in migrations/<engine>/ and are recorded in
// schema_migrations by bare file name, which is what keeps existing Postgres
// databases from re-running anything after the files moved into a
// subdirectory.
//
// Serialised across processes (see WithBootstrapLock): the SELECT EXISTS
// below and the INSERT that follows it are far apart, and two replicas
// starting together both read "not applied" and then race to record the same
// version. The loser used to die on the schema_migrations primary key.
func (s *Store) Migrate(ctx context.Context) error {
	return s.WithBootstrapLock(ctx, "schema-migrations", s.migrateLocked)
}

func (s *Store) migrateLocked(ctx context.Context) error {
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
		applied, err := migrationApplied(ctx, s.db, name)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied {
			continue
		}
		if err := s.applyMigration(ctx, dir, name); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs one migration file and records it, in a single
// transaction, unless somebody else got there first.
//
// The "unless" is the whole reason this is separate from the loop above. That
// loop's check runs on a pooled connection outside any transaction, so it is
// only a fast path: between it and this transaction another process can apply
// and record the same version, and the file would then be applied twice --
// the second time failing on the schema_migrations primary key and taking the
// process down with it. Asking again inside the transaction is what makes
// that a skip instead.
//
// It is also the only guard SQLite has, and it is enough for it: the engine
// has no advisory locks, and its writer opens every transaction with BEGIN
// IMMEDIATE, so this re-read cannot start until the other writer has
// committed and is guaranteed to see the row it wrote.
func (s *Store) applyMigration(ctx context.Context, dir, name string) error {
	sqlBytes, err := migrationsFS.ReadFile(dir + "/" + name)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", name, err)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	applied, err := migrationApplied(ctx, tx, name)
	if err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("check migration %s: %w", name, err)
	}
	if applied {
		_ = tx.Rollback(ctx)
		return nil
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
	return nil
}

// migrationApplied reports whether schema_migrations already records name.
// Taking an executor rather than the pool is the point: the same question has
// to be asked once cheaply and once inside the transaction that is about to
// apply the file (see migrateLocked).
func migrationApplied(ctx context.Context, ex executor, name string) (bool, error) {
	var exists bool
	err := ex.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&exists)
	return exists, err
}
