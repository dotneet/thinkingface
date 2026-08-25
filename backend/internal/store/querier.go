package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5"
)

// querier is the only I/O surface the Store methods touch. It is implemented
// once over pgxpool (pg.go) and once over database/sql + modernc.org/sqlite
// (sqlite.go), so every query in this package is written exactly once and the
// backend is chosen at Open time from the DATABASE_URL scheme.
//
// Placeholders are written as $1, $2, ... in every query: pgx uses them
// natively and modernc.org/sqlite binds $N by number as well (including the
// same number appearing more than once), so no rewriting happens on that
// axis. What does differ between the engines -- functions, array/JSON
// operators, locking, bulk loading -- goes through the dialect (dialect.go)
// or through the tiny SQL translation the sqlite adapter applies.
type querier interface {
	executor
	Begin(ctx context.Context) (tx, error)
	Ping(ctx context.Context) error
	Close()
}

// executor is the shared statement surface of a connection pool and of a
// transaction.
type executor interface {
	// Exec runs a statement and returns the number of rows it affected.
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	Query(ctx context.Context, sql string, args ...any) (rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) rowScanner
}

// tx is an open transaction. Rollback after Commit is a no-op, which lets
// callers keep the `defer tx.Rollback(ctx)` idiom.
type tx interface {
	executor
	// BulkInsert loads many rows into table in one go: COPY on Postgres, a
	// prepared INSERT loop on SQLite. Values are bound positionally in the
	// order of columns.
	BulkInsert(ctx context.Context, table string, columns []string, rows [][]any) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// rows is the iterator both pgx.Rows and *sql.Rows are adapted to.
type rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

// rowScanner is what QueryRow returns: pgx.Row and *sql.Row both satisfy it,
// and scanRepo / scanRun / scanWebhook read either a single row or the
// current row of a rows iterator through it.
type rowScanner interface {
	Scan(dest ...any) error
}

// collect runs a query and scans every row through scan. It is the loop this
// package writes out by hand around forty times -- Query, defer Close, an
// empty slice, `for rows.Next()`, `return out, rows.Err()` -- with the last
// step the reason it is worth having: rows.Err() is the one error a
// `for rows.Next()` loop drops silently, and a connection that dies mid-page
// then looks exactly like a listing that ended. Once here, it cannot be left
// out.
//
// The result is non-nil even when empty: several callers hand it straight to
// a JSON encoder, where nil is `null` and an empty slice is `[]`.
//
// Not every loop belongs here. The ones that scan extra columns alongside a
// shared column list (scanRepoWith, scanOrg) build their destinations per
// call site, and threading that through a scan function buys nothing.
func collect[T any](ctx context.Context, q executor, sql string, args []any, scan func(rowScanner) (T, error)) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// collectSet is collect for a query that selects one text column and wants it
// back as a set. The GC's reference counts (gc.go) are all this shape.
func collectSet(ctx context.Context, q executor, sql string, args ...any) (map[string]bool, error) {
	values, err := collect(ctx, q, sql, args, func(row rowScanner) (string, error) {
		var v string
		err := row.Scan(&v)
		return v, err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out, nil
}

// isNoRows reports whether err is either engine's "no rows" sentinel.
func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows)
}

// norm maps the engine's "no rows" error to ErrNotFound and passes everything
// else through.
func norm(err error) error {
	if isNoRows(err) {
		return ErrNotFound
	}
	return err
}
