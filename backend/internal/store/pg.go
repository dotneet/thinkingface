package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgQuerier adapts a pgxpool.Pool to querier. It is the production path and
// deliberately changes nothing about how pgx is used: native array / jsonb
// codecs, CopyFrom for bulk loads, the pool settings below.
type pgQuerier struct {
	pool *pgxpool.Pool
}

func openPostgres(ctx context.Context, databaseURL string) (*pgQuerier, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 16
	cfg.MaxConnLifetime = time.Hour

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	return &pgQuerier{pool: pool}, nil
}

func (p *pgQuerier) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := p.pool.Exec(ctx, sql, args...)
	return tag.RowsAffected(), err
}

func (p *pgQuerier) Query(ctx context.Context, sql string, args ...any) (rows, error) {
	r, err := p.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (p *pgQuerier) QueryRow(ctx context.Context, sql string, args ...any) rowScanner {
	return p.pool.QueryRow(ctx, sql, args...)
}

func (p *pgQuerier) Begin(ctx context.Context) (tx, error) {
	t, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &pgTx{tx: t}, nil
}

func (p *pgQuerier) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }
func (p *pgQuerier) Close()                         { p.pool.Close() }

type pgTx struct {
	tx pgx.Tx
}

func (t *pgTx) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := t.tx.Exec(ctx, sql, args...)
	return tag.RowsAffected(), err
}

func (t *pgTx) Query(ctx context.Context, sql string, args ...any) (rows, error) {
	r, err := t.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (t *pgTx) QueryRow(ctx context.Context, sql string, args ...any) rowScanner {
	return t.tx.QueryRow(ctx, sql, args...)
}

func (t *pgTx) BulkInsert(ctx context.Context, table string, columns []string, rows [][]any) error {
	_, err := t.tx.CopyFrom(ctx, pgx.Identifier{table}, columns, pgx.CopyFromRows(rows))
	return err
}

func (t *pgTx) Commit(ctx context.Context) error   { return t.tx.Commit(ctx) }
func (t *pgTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

// pgIsUniqueViolation recognises SQLSTATE 23505.
func pgIsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
