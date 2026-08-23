package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"
)

// pgDialect renders the PostgreSQL spellings: these are the fragments the
// package was originally written with, moved here verbatim.
type pgDialect struct{}

func (pgDialect) name() string { return "postgres" }

func (pgDialect) schemaMigrationsDDL() string {
	return `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`
}

func (pgDialect) inArray(placeholder string) string { return `= ANY(` + placeholder + `)` }

func (pgDialect) arrayHas(column, placeholder string) string {
	return placeholder + ` = ANY(` + column + `)`
}

func (pgDialect) stringArrayArg(v []string) any {
	if v == nil {
		return nil
	}
	return v
}

func (pgDialect) stringArrayDest(p *[]string) any { return p }

func (pgDialect) jsonArrayContainsAll(column, key string, bind func(any) string, vals []string) string {
	raw, _ := json.Marshal(vals)
	return column + `->'` + key + `' @> ` + bind(string(raw)) + `::jsonb`
}

func (pgDialect) jsonArrayHas(column, key, placeholder string) string {
	return column + `->'` + key + `' @> to_jsonb(` + placeholder + `::text)`
}

func (pgDialect) jsonArrayElements(column, key string) (string, string) {
	return `CROSS JOIN LATERAL jsonb_array_elements_text(
			CASE WHEN jsonb_typeof(` + column + `->'` + key + `') = 'array' THEN ` + column + `->'` + key + `' ELSE '[]'::jsonb END
		) elem`, `elem`
}

func (pgDialect) searchPredicate(bind func(any) string, text string) string {
	q := BuildPrefixTSQuery(text)
	if q == "" {
		return ""
	}
	return `r.search_vector @@ to_tsquery('simple', ` + bind(q) + `)`
}

func (pgDialect) forUpdate(suffix string) string { return ` FOR UPDATE` + suffix }

func (pgDialect) advisoryXactLock(ctx context.Context, ex executor, name string, id int64) error {
	// hashtextextended keeps the lock in the bigint key space without
	// colliding with a bare id used elsewhere as an advisory key.
	_, err := ex.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, $2))`, name, id)
	return err
}

func (pgDialect) nowPlusSeconds(placeholder string) string {
	return `now() + (` + placeholder + `::double precision * interval '1 second')`
}

func (pgDialect) dateArg(t time.Time) any { return t }

func (pgDialect) isUniqueViolation(err error) bool { return pgIsUniqueViolation(err) }

func (pgDialect) queries() dialectQueries {
	return dialectQueries{
		upsertExpRun: `INSERT INTO exp_runs (project_id, name, status, config, summary, metric_keys, last_step, num_points, started_at, group_name, job_type)
			 VALUES ($1, $2, COALESCE(NULLIF($3, ''), 'finished'),
			         COALESCE($4::jsonb, '{}'::jsonb), COALESCE($5::jsonb, '{}'::jsonb),
			         COALESCE($6::jsonb, '[]'::jsonb), $7, $8, $9,
			         COALESCE($10::text, ''), COALESCE($11::text, ''))
			 ON CONFLICT (project_id, name) DO UPDATE SET
			   status      = COALESCE(NULLIF($3, ''), exp_runs.status),
			   config      = COALESCE($4::jsonb, exp_runs.config),
			   summary     = COALESCE($5::jsonb, exp_runs.summary),
			   metric_keys = COALESCE($6::jsonb, exp_runs.metric_keys),
			   last_step   = GREATEST(exp_runs.last_step, $7),
			   num_points  = GREATEST(exp_runs.num_points, $8),
			   started_at  = COALESCE(exp_runs.started_at, $9),
			   group_name  = COALESCE($10::text, exp_runs.group_name),
			   job_type    = COALESCE($11::text, exp_runs.job_type),
			   updated_at  = now()
			 RETURNING id`,
		updateExpRunAnnotation: `UPDATE exp_runs SET
			   tags        = COALESCE($3::text[], tags),
			   archived    = COALESCE($4::boolean, archived),
			   is_baseline = COALESCE($5::boolean, is_baseline),
			   note        = COALESCE($6::text, note)
			 WHERE project_id = $1 AND name = $2
			 RETURNING ` + runColumns,
		linkLFSObjectsInsert: `INSERT INTO repo_lfs_objects (repo_id, oid)
			 SELECT $1, o FROM unnest($2::text[]) AS o
			 WHERE EXISTS (SELECT 1 FROM lfs_objects WHERE oid = o)
			 ON CONFLICT DO NOTHING`,
	}
}

// BuildPrefixTSQuery turns free text into a Postgres tsquery string that
// AND-matches a prefix of every word, e.g. "bert base" -> "bert:* & base:*".
// It returns "" for input with no usable tokens (e.g. only punctuation),
// which callers should treat the same as "no search query".
//
// Hyphens, dots, and other non-operator punctuation are kept so
// to_tsquery('simple') tokenizes the query the same way the search_vector
// trigger tokenizes with to_tsvector('simple', ...). Stripping them would turn
// "gpt-2" into "gpt2:*", which does not match the lexemes "gpt" and "-2"
// that Postgres emits for hyphenated model names, tags, and licenses.
//
// tsquery operators (& | ! ( ) : * ' < >) are treated as word breaks so
// they cannot change the query's parse.
func BuildPrefixTSQuery(input string) string {
	fields := strings.FieldsFunc(input, isTSQuerySplit)
	tokens := make([]string, 0, len(fields))
	for _, word := range fields {
		if !hasLetterOrDigit(word) {
			continue
		}
		tokens = append(tokens, word+":*")
	}
	return strings.Join(tokens, " & ")
}

func isTSQuerySplit(r rune) bool {
	if unicode.IsSpace(r) {
		return true
	}
	switch r {
	case '&', '|', '!', '(', ')', ':', '*', '\'', '<', '>':
		return true
	default:
		return false
	}
}

func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
