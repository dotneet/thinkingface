package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// sqliteTimeLayout is how every timestamp is written to SQLite: UTC,
// millisecond precision, the same text shape strftime('%Y-%m-%d %H:%M:%f')
// produces for now(). One format means ORDER BY and `<= now()` compare
// correctly as text, and the driver parses it back into a UTC time.Time for
// columns declared DATETIME.
const sqliteTimeLayout = "2006-01-02 15:04:05.000"

// sqliteNow is the SQLite spelling of now() (see sqliteTimeLayout).
const sqliteNow = "strftime('%Y-%m-%d %H:%M:%f','now')"

// sqliteBusyTimeout is how long a statement waits for the database lock
// before failing with SQLITE_BUSY. It only matters across processes (a `gc`
// run next to a live server); inside the process the single writer
// connection already serialises writes.
const sqliteBusyTimeout = 10 * time.Second

// sqliteReaderConns bounds the read pool. Readers never block each other or
// the writer under WAL.
const sqliteReaderConns = 8

// sqliteQuerier adapts database/sql + modernc.org/sqlite to querier.
//
// Concurrency model: SQLite allows one writer at a time, so all writes (and
// every transaction) go through a pool of exactly one connection, opened with
// _txlock=immediate so BEGIN takes the write lock up front instead of
// failing with SQLITE_BUSY on upgrade. Reads go through a separate pool and,
// thanks to WAL mode, neither wait for the writer nor make it wait. With
// every write transaction alone on its connection, the row locks and
// advisory locks the Postgres path relies on are simply unnecessary here
// (see sqliteDialect.forUpdate / advisoryXactLock).
type sqliteQuerier struct {
	writer *sql.DB
	reader *sql.DB
}

// sqlitePath turns sqlite:///abs/path.db, sqlite://relative.db or
// sqlite:relative.db into the file path. A query string is dropped.
func sqlitePath(databaseURL string) (string, error) {
	if strings.Contains(databaseURL, ":memory:") || strings.Contains(databaseURL, "mode=memory") {
		// The writer and reader pools would each get their own private
		// in-memory database.
		return "", errors.New("DATABASE_URL: in-memory sqlite databases are not supported; use a file path")
	}
	p := strings.TrimPrefix(databaseURL, "sqlite:")
	p = strings.TrimPrefix(p, "//")
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	if p == "" {
		return "", errors.New("DATABASE_URL: sqlite:// needs a file path, e.g. sqlite:///data/db/thinkingface.db")
	}
	return p, nil
}

func openSQLite(ctx context.Context, databaseURL string) (*sqliteQuerier, error) {
	path, err := sqlitePath(databaseURL)
	if err != nil {
		return nil, err
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite directory %s: %w", dir, err)
		}
	}
	pragmas := fmt.Sprintf("_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)",
		sqliteBusyTimeout.Milliseconds())
	writer, err := sql.Open("sqlite", "file:"+path+"?"+pragmas+"&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)

	reader, err := sql.Open("sqlite", "file:"+path+"?"+pragmas)
	if err != nil {
		writer.Close()
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	reader.SetMaxOpenConns(sqliteReaderConns)
	reader.SetMaxIdleConns(sqliteReaderConns)
	reader.SetConnMaxLifetime(0)

	q := &sqliteQuerier{writer: writer, reader: reader}
	if err := q.Ping(ctx); err != nil {
		q.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	return q, nil
}

// sqliteTranslate rewrites the handful of Postgres spellings the shared
// queries use into their SQLite equivalents. Neither token ever appears
// inside a string literal in this package, so plain substitution is safe;
// anything more structural goes through the dialect instead.
var sqliteTranslate = struct {
	sync.Map // string -> string
}{}

var sqliteReplacer = strings.NewReplacer(
	"now()", sqliteNow,
	" ILIKE ", " LIKE ",
)

func translateSQLite(query string) string {
	if v, ok := sqliteTranslate.Load(query); ok {
		return v.(string)
	}
	out := sqliteReplacer.Replace(query)
	sqliteTranslate.Store(query, out)
	return out
}

// sqliteArgs converts the values the Store binds into what the driver should
// receive: timestamps as sqliteTimeLayout text and JSON as TEXT rather than
// BLOB (json_* functions treat a BLOB as the binary JSONB encoding).
func sqliteArgs(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = sqliteArg(a)
	}
	return out
}

func sqliteArg(a any) any {
	switch v := a.(type) {
	case time.Time:
		return v.UTC().Format(sqliteTimeLayout)
	case *time.Time:
		if v == nil {
			return nil
		}
		return v.UTC().Format(sqliteTimeLayout)
	case []byte:
		if v == nil {
			return nil
		}
		return string(v)
	case json.RawMessage:
		if v == nil {
			return nil
		}
		return string(v)
	}
	return a
}

// sqliteIsRead routes a statement to the reader pool when it cannot write.
// Statements with RETURNING start with INSERT/UPDATE/DELETE and therefore
// land on the writer. SQLite has no writable CTEs, so WITH is read-only.
func sqliteIsRead(query string) bool {
	q := strings.TrimLeft(query, " \t\r\n")
	if len(q) < 6 {
		return false
	}
	head := strings.ToUpper(q[:6])
	return head == "SELECT" || strings.HasPrefix(head, "WITH ") || strings.HasPrefix(head, "WITH\n") || strings.HasPrefix(head, "WITH\t")
}

func (s *sqliteQuerier) pool(query string) *sql.DB {
	if sqliteIsRead(query) {
		return s.reader
	}
	return s.writer
}

func (s *sqliteQuerier) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	res, err := s.pool(query).ExecContext(ctx, translateSQLite(query), sqliteArgs(args)...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (s *sqliteQuerier) Query(ctx context.Context, query string, args ...any) (rows, error) {
	r, err := s.pool(query).QueryContext(ctx, translateSQLite(query), sqliteArgs(args)...)
	if err != nil {
		return nil, err
	}
	return sqlRows{r}, nil
}

func (s *sqliteQuerier) QueryRow(ctx context.Context, query string, args ...any) rowScanner {
	return s.pool(query).QueryRowContext(ctx, translateSQLite(query), sqliteArgs(args)...)
}

func (s *sqliteQuerier) Begin(ctx context.Context) (tx, error) {
	t, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqliteTx{tx: t}, nil
}

func (s *sqliteQuerier) Ping(ctx context.Context) error {
	if err := s.writer.PingContext(ctx); err != nil {
		return err
	}
	return s.reader.PingContext(ctx)
}

func (s *sqliteQuerier) Close() {
	_ = s.reader.Close()
	_ = s.writer.Close()
}

// sqlRows adapts *sql.Rows (whose Close returns an error) to rows.
type sqlRows struct{ *sql.Rows }

func (r sqlRows) Close() { _ = r.Rows.Close() }

type sqliteTx struct {
	tx *sql.Tx
}

func (t *sqliteTx) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	res, err := t.tx.ExecContext(ctx, translateSQLite(query), sqliteArgs(args)...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (t *sqliteTx) Query(ctx context.Context, query string, args ...any) (rows, error) {
	r, err := t.tx.QueryContext(ctx, translateSQLite(query), sqliteArgs(args)...)
	if err != nil {
		return nil, err
	}
	return sqlRows{r}, nil
}

func (t *sqliteTx) QueryRow(ctx context.Context, query string, args ...any) rowScanner {
	return t.tx.QueryRowContext(ctx, translateSQLite(query), sqliteArgs(args)...)
}

// BulkInsert is a prepared INSERT executed once per row. It runs inside the
// caller's transaction, so the whole batch is one fsync on commit.
func (t *sqliteTx) BulkInsert(ctx context.Context, table string, columns []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	placeholders := make([]string, len(columns))
	for i := range columns {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	stmt, err := t.tx.PrepareContext(ctx,
		`INSERT INTO `+table+` (`+strings.Join(columns, ", ")+`) VALUES (`+strings.Join(placeholders, ", ")+`)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, row := range rows {
		if len(row) != len(columns) {
			return fmt.Errorf("bulk insert into %s: row has %d values for %d columns", table, len(row), len(columns))
		}
		if _, err := stmt.ExecContext(ctx, sqliteArgs(row)...); err != nil {
			return err
		}
	}
	return nil
}

func (t *sqliteTx) Commit(ctx context.Context) error { return t.tx.Commit() }

func (t *sqliteTx) Rollback(ctx context.Context) error {
	err := t.tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}
