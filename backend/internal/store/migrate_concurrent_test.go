package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Every process this binary can start -- serve, gc, compact, migrate -- runs
// Migrate before it does anything else, and a Cloud Run rollout starts several
// of them at the same instant. The check and the INSERT that records a version
// used to sit far apart with nothing between them, so both processes read "not
// applied", both applied the file, and the loser died on the schema_migrations
// primary key.
//
// SQLite is what these tests can drive on their own; the Postgres path adds
// pg_advisory_xact_lock on top (WithBootstrapLock) and runs here too when
// TF_TEST_DATABASE_URL is set.

// migrateBackends opens one store per concurrent "process" over a single
// database, which is what makes the race reachable: separate Stores mean
// separate connections and separate pools.
func migrateBackends(t *testing.T, n int) []*Store {
	t.Helper()
	ctx := context.Background()

	url := "sqlite://" + filepath.Join(t.TempDir(), "migrate.db")
	if pg := os.Getenv("TF_TEST_DATABASE_URL"); pg != "" {
		url = pg
	}
	out := make([]*Store, 0, n)
	for range n {
		s, err := Open(ctx, url)
		if err != nil {
			t.Fatalf("open %s: %v", url, err)
		}
		t.Cleanup(s.Close)
		if err := s.WaitReady(ctx, 30*time.Second); err != nil {
			t.Fatalf("database not ready: %v", err)
		}
		out = append(out, s)
	}
	return out
}

func TestMigrateSurvivesConcurrentProcesses(t *testing.T) {
	const processes = 4
	stores := migrateBackends(t, processes)

	ctx := context.Background()
	start := make(chan struct{})
	errs := make([]error, processes)
	var wg sync.WaitGroup
	for i, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = s.Migrate(ctx)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("process %d: Migrate failed while another was running it: %v", i, err)
		}
	}

	// Applied once, not once per process: a duplicate row would mean the
	// bookkeeping is only accidentally consistent.
	var rows int
	if err := stores[0].db.QueryRow(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = $1`, "0001_init.sql").Scan(&rows); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if rows != 1 {
		t.Fatalf("schema_migrations holds %d rows for 0001_init.sql; want exactly 1", rows)
	}

	// And the schema is usable afterwards, so "no error" is not just a
	// migration everybody skipped.
	if _, err := stores[0].CountUsers(ctx); err != nil {
		t.Fatalf("count users after a concurrent migration: %v", err)
	}
}

// The race itself, driven rather than hoped for: applyMigration is exactly
// the window between the loop's fast-path check and the transaction that
// applies the file, so calling it for a version another process has already
// recorded *is* the losing replica's position. It has to skip, not fail.
//
// Without the re-read inside the transaction this is where the process died:
// the file reapplies (every statement in it is IF NOT EXISTS, so that part is
// silent) and then the INSERT hits the schema_migrations primary key.
func TestApplyMigrationSkipsAVersionAnotherProcessRecorded(t *testing.T) {
	s := migrateBackends(t, 1)[0]
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	dir := "migrations/" + s.d.name()
	name := "0001_init.sql"
	if err := s.applyMigration(ctx, dir, name); err != nil {
		t.Fatalf("applyMigration for an already-recorded version: %v", err)
	}

	var rows int
	if err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = $1`, name).Scan(&rows); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if rows != 1 {
		t.Fatalf("schema_migrations holds %d rows for %s; want exactly 1", rows, name)
	}
}

// Migrate stays idempotent: a second pass over an already-migrated database
// applies nothing and records nothing.
func TestMigrateIsIdempotent(t *testing.T) {
	s := migrateBackends(t, 1)[0]
	ctx := context.Background()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	var before int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&before); err != nil {
		t.Fatalf("count: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var after int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&after); err != nil {
		t.Fatalf("count: %v", err)
	}
	if before != after {
		t.Fatalf("schema_migrations grew from %d to %d on a repeat run", before, after)
	}
}

// WithBootstrapLock is the mechanism seeding the first administrator needs
// too (cmd/thinkingface/main.go's seedAdmin is the other CountUsers-then-
// CreateUser race). It has to run fn exactly once and propagate its error.
func TestWithBootstrapLockRunsFnAndReportsItsError(t *testing.T) {
	s := migrateBackends(t, 1)[0]
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	calls := 0
	if err := s.WithBootstrapLock(ctx, "seed-admin", func(context.Context) error {
		calls++
		return nil
	}); err != nil {
		t.Fatalf("WithBootstrapLock: %v", err)
	}
	if calls != 1 {
		t.Fatalf("fn ran %d times; want 1", calls)
	}

	want := ErrConflict
	if err := s.WithBootstrapLock(ctx, "seed-admin", func(context.Context) error {
		return want
	}); err != want { //nolint:errorlint // the sentinel must come back unwrapped
		t.Fatalf("WithBootstrapLock swallowed fn's error: got %v, want %v", err, want)
	}
}

// The lock must be released when fn returns, or the second process to start
// would block until the first exits.
func TestWithBootstrapLockIsReleased(t *testing.T) {
	s := migrateBackends(t, 1)[0]
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for i := range 3 {
		done := make(chan error, 1)
		go func() {
			done <- s.WithBootstrapLock(ctx, "seed-admin", func(context.Context) error { return nil })
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("round %d: %v", i, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("round %d: WithBootstrapLock blocked; the previous round did not release", i)
		}
	}
}
