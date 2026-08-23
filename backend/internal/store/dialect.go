package store

import (
	"context"
	"time"
)

// dialect captures everything about a query that differs between PostgreSQL
// and SQLite and cannot be expressed in SQL both engines accept. Each method
// is a small SQL fragment builder or an argument/result converter; the Store
// methods compose them into otherwise shared statements. A few statements
// whose shape differs too much to share (casts, unnest, GREATEST) are handed
// over whole via queries().
//
// Rule of thumb for adding a method: if the difference is a keyword or
// function both engines spell differently, prefer the sqlite adapter's
// translate() (now(), ILIKE); if it changes the shape of the
// predicate or the way a value is bound or scanned, it belongs here.
type dialect interface {
	// name is "postgres" or "sqlite"; it selects migrations/<name>/.
	name() string
	// schemaMigrationsDDL creates the bookkeeping table Migrate writes to.
	schemaMigrationsDDL() string

	// --- arrays -------------------------------------------------------
	// Text arrays are TEXT[] on Postgres and JSON text on SQLite.

	// inArray renders "<column> IN <placeholder-bound array>", e.g.
	// `oid = ANY($1)` / `oid IN (SELECT value FROM json_each($1))`.
	inArray(placeholder string) string
	// arrayHas renders "<placeholder-bound scalar> is an element of <column>".
	arrayHas(column, placeholder string) string
	// stringArrayArg converts a []string to the bound representation; nil
	// stays nil so COALESCE(...) keeps the stored value.
	stringArrayArg(v []string) any
	// stringArrayDest wraps a *[]string so Scan fills it from the engine's
	// representation.
	stringArrayDest(p *[]string) any

	// --- JSON --------------------------------------------------------
	// Repository cards are jsonb on Postgres and JSON text on SQLite;
	// `->>` with a bare key works on both, so only array semantics differ.

	// jsonArrayContainsAll renders "every value in vals is an element of
	// the array at column.key" (the `@>` containment check). bind appends
	// a value and returns its placeholder.
	jsonArrayContainsAll(column, key string, bind func(any) string, vals []string) string
	// jsonArrayHas renders "the value bound at placeholder is an element
	// of the array (or equal to the scalar) at column.key".
	jsonArrayHas(column, key, placeholder string) string
	// jsonArrayElements returns a FROM-clause fragment that joins one row
	// per element of the array at column.key (nothing when it is not an
	// array) and the expression that yields the element's text.
	jsonArrayElements(column, key string) (from string, value string)

	// --- search --------------------------------------------------------

	// searchPredicate renders the full text search predicate over
	// repositories r for free text, or "" when the text has no usable
	// tokens (which callers treat as "no search filter").
	searchPredicate(bind func(any) string, text string) string

	// --- locking / time / errors ----------------------------------------

	// forUpdate renders a row lock clause (" FOR UPDATE" + suffix on
	// Postgres). SQLite has no row locks: writes are serialised on a single
	// connection instead, so it renders "".
	forUpdate(suffix string) string
	// advisoryXactLock serialises concurrent transactions on a logical key
	// until the transaction ends. A no-op on SQLite (see forUpdate).
	advisoryXactLock(ctx context.Context, ex executor, name string, id int64) error
	// nowPlusSeconds renders "current time + <placeholder-bound seconds>".
	nowPlusSeconds(placeholder string) string
	// dateArg converts a UTC-midnight day (see utcDay) to what a DATE
	// column is written with and compared against.
	dateArg(t time.Time) any
	isUniqueViolation(err error) bool

	// queries returns the statements that are written per engine.
	queries() dialectQueries
}

// dialectQueries are whole statements that differ per engine. Placeholder
// numbering is shared with the caller and documented next to each field.
type dialectQueries struct {
	// upsertExpRun: $1 project_id, $2 name, $3 status ('' = keep),
	// $4 config JSON (nil = keep), $5 summary JSON (nil = keep),
	// $6 metric_keys JSON (nil = keep), $7 last_step, $8 num_points,
	// $9 started_at (nil = keep), $10 group_name (nil = keep),
	// $11 job_type (nil = keep). RETURNING id.
	upsertExpRun string
	// updateExpRunAnnotation: $1 project_id, $2 name, $3 tags array
	// (nil = keep), $4 archived (nil = keep), $5 is_baseline (nil = keep),
	// $6 note (nil = keep). RETURNING runColumns.
	updateExpRunAnnotation string
	// linkLFSObjectsInsert: $1 repo_id, $2 oid array. Inserts one
	// repo_lfs_objects row per oid that exists in lfs_objects, ignoring
	// duplicates.
	linkLFSObjectsInsert string
}
