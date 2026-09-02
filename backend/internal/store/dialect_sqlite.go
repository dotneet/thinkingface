package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

// sqliteDialect renders the SQLite spellings. Arrays are JSON text read with
// json_each; cards are JSON text (`->>` with a bare key works as on
// Postgres); full text search is the repositories_fts FTS5 table kept in
// step by triggers (migrations/sqlite/0001_init.sql); and locks are no-ops
// because every write transaction runs alone on the writer connection
// (see sqliteQuerier).
type sqliteDialect struct{}

func (sqliteDialect) name() string { return "sqlite" }

func (sqliteDialect) schemaMigrationsDDL() string {
	return `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at DATETIME NOT NULL DEFAULT (` + sqliteNow + `))`
}

func (sqliteDialect) inArray(placeholder string) string {
	return `IN (SELECT value FROM json_each(` + placeholder + `))`
}

func (sqliteDialect) arrayHas(column, placeholder string) string {
	return `EXISTS (SELECT 1 FROM json_each(` + column + `) WHERE value = ` + placeholder + `)`
}

func (sqliteDialect) stringArrayArg(v []string) any {
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

func (sqliteDialect) stringArrayDest(p *[]string) any { return &jsonStringSlice{p: p} }

// jsonStringSlice scans a JSON array of strings (or NULL) into a *[]string.
type jsonStringSlice struct {
	p *[]string
}

var _ sql.Scanner = (*jsonStringSlice)(nil)

func (j *jsonStringSlice) Scan(src any) error {
	var raw []byte
	switch v := src.(type) {
	case nil:
		*j.p = nil
		return nil
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("store: cannot scan %T into []string", src)
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("store: decode string array: %w", err)
	}
	*j.p = out
	return nil
}

// jsonPath renders the SQLite path expression for a top-level card key. Keys
// are compile-time constants in this package, never user input.
func jsonPath(key string) string { return `'$.` + key + `'` }

func (sqliteDialect) jsonArrayContainsAll(column, key string, bind func(any) string, vals []string) string {
	// json_each over a scalar yields that scalar as a single row, so the
	// json_type guard keeps Postgres' `@>` semantics: a string-typed
	// `tags` never contains a list.
	parts := []string{`json_type(` + column + `, ` + jsonPath(key) + `) = 'array'`}
	for _, v := range vals {
		parts = append(parts, `EXISTS (SELECT 1 FROM json_each(`+column+`, `+jsonPath(key)+`) WHERE value = `+bind(v)+`)`)
	}
	return `(` + strings.Join(parts, " AND ") + `)`
}

func (sqliteDialect) jsonArrayHas(column, key, placeholder string) string {
	// A scalar at the key is one row with that value, which matches the
	// Postgres `@> to_jsonb(text)` behaviour for string-typed fields.
	return `EXISTS (SELECT 1 FROM json_each(` + column + `, ` + jsonPath(key) + `) WHERE value = ` + placeholder + `)`
}

func (sqliteDialect) jsonArrayElements(column, key string) (string, string) {
	return `JOIN json_each(
			CASE WHEN json_type(` + column + `, ` + jsonPath(key) + `) = 'array' THEN ` + column + `->'` + key + `' ELSE '[]' END
		) elem`, `elem.value`
}

// jsonScalarText restores the Postgres answer. SQLite's `->>` yields the
// value in its own type -- an integer for `license: 2`, 1 or 0 for a boolean
// -- so `r.card->>'license' = $1` never matched a non-string license and the
// facet that listed it led to an empty page. CAST fixes the numbers; the two
// booleans have to be spelled out, because casting SQLite's 1 would give "1"
// where jsonb gives "true".
func (sqliteDialect) jsonScalarText(column, key string) string {
	return `CASE json_type(` + column + `, ` + jsonPath(key) + `)
			WHEN 'true' THEN 'true'
			WHEN 'false' THEN 'false'
			ELSE CAST(` + column + `->>'` + key + `' AS TEXT) END`
}

func (sqliteDialect) searchPredicate(bind func(any) string, text string) string {
	q := BuildFTS5PrefixQuery(text)
	if q == "" {
		return ""
	}
	return `r.id IN (SELECT rowid FROM repositories_fts WHERE repositories_fts MATCH ` + bind(q) + `)`
}

func (sqliteDialect) forUpdate(string) string { return "" }

func (sqliteDialect) advisoryXactLock(context.Context, executor, string, int64) error { return nil }

func (sqliteDialect) nowPlusSeconds(placeholder string) string {
	return `strftime('%Y-%m-%d %H:%M:%f', 'now', '+' || ` + placeholder + ` || ' seconds')`
}

func (sqliteDialect) dateArg(t time.Time) any { return t.UTC().Format("2006-01-02") }

func (sqliteDialect) isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() {
	case sqlitelib.SQLITE_CONSTRAINT_UNIQUE, sqlitelib.SQLITE_CONSTRAINT_PRIMARYKEY:
		return true
	}
	return false
}

func (sqliteDialect) queries() dialectQueries {
	return dialectQueries{
		upsertExpRun: `INSERT INTO exp_runs (project_id, name, status, config, summary, metric_keys, last_step, num_points, started_at, group_name, job_type)
			 VALUES ($1, $2, COALESCE(NULLIF($3, ''), 'finished'),
			         COALESCE($4, '{}'), COALESCE($5, '{}'), COALESCE($6, '[]'), $7, $8, $9,
			         COALESCE($10, ''), COALESCE($11, ''))
			 ON CONFLICT (project_id, name) DO UPDATE SET
			   status      = COALESCE(NULLIF($3, ''), exp_runs.status),
			   config      = COALESCE($4, exp_runs.config),
			   summary     = COALESCE($5, exp_runs.summary),
			   metric_keys = COALESCE($6, exp_runs.metric_keys),
			   last_step   = MAX(exp_runs.last_step, $7),
			   num_points  = MAX(exp_runs.num_points, $8),
			   started_at  = COALESCE(exp_runs.started_at, $9),
			   group_name  = COALESCE($10, exp_runs.group_name),
			   job_type    = COALESCE($11, exp_runs.job_type),
			   updated_at  = now()
			 RETURNING id`,
		updateExpRunAnnotation: `UPDATE exp_runs SET
			   tags        = COALESCE($3, tags),
			   archived    = COALESCE($4, archived),
			   is_baseline = COALESCE($5, is_baseline),
			   note        = COALESCE($6, note)
			 WHERE project_id = $1 AND name = $2
			 RETURNING ` + runColumns,
		linkLFSObjectsInsert: `INSERT INTO repo_lfs_objects (repo_id, oid, created_at, committed_at)
			 SELECT $1, o.value, now(), now() FROM json_each($2) AS o
			 WHERE EXISTS (SELECT 1 FROM lfs_objects WHERE oid = o.value)
			 ON CONFLICT (repo_id, oid) DO UPDATE SET committed_at = now()`,
	}
}

// BuildFTS5PrefixQuery turns free text into an FTS5 MATCH expression that
// AND-matches a prefix of every word, e.g. "bert base" -> `"bert"* AND
// "base"*`. It is the SQLite counterpart of BuildPrefixTSQuery and splits on
// the same characters, so the two engines agree on what counts as a word.
// Each token is double-quoted (with embedded quotes doubled), which makes
// FTS5 treat it as a phrase run through the unicode61 tokenizer -- "gpt-2"
// becomes the phrase gpt 2 with a prefix on the last token -- and keeps FTS5
// query operators in the input from changing the parse. It returns "" for
// input with no usable tokens.
func BuildFTS5PrefixQuery(input string) string {
	fields := strings.FieldsFunc(input, isTSQuerySplit)
	tokens := make([]string, 0, len(fields))
	for _, word := range fields {
		if !hasLetterOrDigit(word) {
			continue
		}
		tokens = append(tokens, `"`+strings.ReplaceAll(word, `"`, `""`)+`"*`)
	}
	return strings.Join(tokens, " AND ")
}
