package store

import (
	"context"
	"encoding/json"
	"regexp"
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

// jsonArrayContainsAll has to find what the facet lists, and the facet
// (jsonArrayElements) lists each element as text: `tags: [2024, bert]` shows
// up as "2024". A plain `@> '["2024"]'` compares JSON types and misses the
// number, so a value the sidebar offered returned nothing when clicked.
//
// It stays a containment test rather than becoming a text comparison over
// jsonb_array_elements_text because idx_repositories_card_tags is a GIN index
// on card->'tags', and `@>` is what it serves. A value whose text is also a
// JSON number or boolean literal gets a second containment against that
// literal -- `@> '[2024]'` -- which the index serves just as well, so the
// OR costs a BitmapOr rather than a scan. Numeric containment compares by
// value, so it finds every element whose text is v -- and, harmlessly, a
// numerically equal spelling too ("2024.0" finds 2024).
func (pgDialect) jsonArrayContainsAll(column, key string, bind func(any) string, vals []string) string {
	col := column + `->'` + key + `'`
	strs := []string{}
	var parts []string
	for _, v := range vals {
		lit, ok := jsonNonStringScalar(v)
		if !ok {
			strs = append(strs, v)
			continue
		}
		raw, _ := json.Marshal([]string{v})
		parts = append(parts, `(`+col+` @> `+bind(string(raw))+`::jsonb OR `+
			col+` @> `+bind(`[`+lit+`]`)+`::jsonb)`)
	}
	// With no values at all this is `@> '[]'`, "is an array", as before.
	if len(strs) > 0 || len(parts) == 0 {
		raw, _ := json.Marshal(strs)
		parts = append([]string{col + ` @> ` + bind(string(raw)) + `::jsonb`}, parts...)
	}
	return `(` + strings.Join(parts, " AND ") + `)`
}

// jsonNonStringScalarNumberRe matches a plain decimal literal: no exponent,
// no leading zeros other than a lone "0", and an optional fractional part.
// This is deliberately narrower than JSON's own number grammar (which allows
// e.g. "1e999999") -- see jsonNonStringScalar for why.
var jsonNonStringScalarNumberRe = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// jsonNonStringScalarMaxLen caps how long a numeric literal can be before
// jsonNonStringScalar treats it as a plain string instead. A repo card tag
// is never a bare number of meaningful magnitude, and the cap keeps
// pathologically long digit strings from reaching the ::jsonb / numeric
// literal built in jsonArrayContainsAll.
const jsonNonStringScalarMaxLen = 32

// jsonNonStringScalar reports whether v, taken as JSON source, is a number or
// a boolean exactly as written -- no surrounding whitespace, which would make
// it a different string from the facet's -- and returns it as a literal.
//
// The number case only accepts a plain decimal literal (jsonNonStringScalarNumberRe),
// not full JSON number syntax: json.Number happily parses exponent forms like
// "1e999999", which this function used to accept and hand to jsonArrayContainsAll
// as a `[1e999999]::jsonb` bind -- valid JSON, but a value PostgreSQL's numeric
// type rejects outright ("value overflows numeric format"), turning an
// unauthenticated `GET /api/models?filter=1e999999` into a 500. A repo card
// tag is realistically a short plain number (a year, a parameter count), never
// scientific notation, so exponents are simply treated as a string instead.
func jsonNonStringScalar(v string) (string, bool) {
	if v == "true" || v == "false" {
		return v, true
	}
	if len(v) > jsonNonStringScalarMaxLen || !jsonNonStringScalarNumberRe.MatchString(v) {
		return "", false
	}
	return v, true
}

// jsonArrayHas compares text for the same reason jsonArrayContainsAll has to
// find numbers: the task facet lists task_categories elements as text. There
// is no index on task_categories to preserve, so this is the plain form --
// an element (or the scalar itself) whose text is the bound value.
func (pgDialect) jsonArrayHas(column, key, placeholder string) string {
	col := column + `->'` + key + `'`
	return `(CASE WHEN jsonb_typeof(` + col + `) = 'array'
			THEN EXISTS (SELECT 1 FROM jsonb_array_elements_text(` + col + `) e WHERE e = ` + placeholder + `::text)
			ELSE ` + column + `->>'` + key + `' = ` + placeholder + `::text END)`
}

func (pgDialect) jsonArrayElements(column, key string) (string, string) {
	return `CROSS JOIN LATERAL jsonb_array_elements_text(
			CASE WHEN jsonb_typeof(` + column + `->'` + key + `') = 'array' THEN ` + column + `->'` + key + `' ELSE '[]'::jsonb END
		) elem`, `elem`
}

// jsonScalarText is just `->>`: jsonb renders every scalar as text, so
// numbers, booleans and strings all compare against a bound string.
func (pgDialect) jsonScalarText(column, key string) string {
	return column + `->>'` + key + `'`
}

func (pgDialect) searchPredicate(bind func(any) string, text string) string {
	q := BuildPrefixTSQuery(text)
	if q == "" {
		return ""
	}
	return `r.search_vector @@ to_tsquery('simple', ` + bind(q) + `)`
}

func (pgDialect) forUpdate(suffix string) string { return ` FOR UPDATE` + suffix }

func (pgDialect) forShare() string { return ` FOR SHARE` }

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
		linkLFSObjectsInsert: `INSERT INTO repo_lfs_objects (repo_id, oid, created_at, committed_at)
			 SELECT $1, o, now(), now() FROM unnest($2::text[]) AS o
			 WHERE EXISTS (SELECT 1 FROM lfs_objects WHERE oid = o)
			 ON CONFLICT (repo_id, oid) DO UPDATE SET committed_at = now()`,
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
