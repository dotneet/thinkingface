package store

import "strings"

// The `search=` / `q=` parameters are plain substrings, not patterns. That is
// what huggingface_hub relies on -- "bert" has to match "distilbert" and
// nothing cleverer -- and it is also what a user typing into the Web UI's
// filter boxes means. Interpolating that text straight into "%...%" hands the
// caller the pattern language on top of it: "%" then matches every row,
// "100%" matches any row containing "100", and "a_b" matches "axb".
//
// Worse than the extra wildcards, the two engines disagree about the
// backslash. PostgreSQL's LIKE takes "\" as its escape character unless told
// otherwise, while SQLite's has no escape character at all unless an ESCAPE
// clause names one -- so a search for `a\b` matches the *opposite* rows on the
// two backends: 'ab' on Postgres, 'a\b' on SQLite.
//
// Closing both needs the two halves below used together: escapeLike
// neutralises the three characters that mean something to LIKE, and likeAnyOf
// renders the predicate with the matching `ESCAPE '\'` (which both engines
// accept, so nothing here has to branch on the dialect). A bound value that
// skips the escaping, or a predicate that skips the ESCAPE clause, is a bug
// in one engine or the other -- so every caller goes through this pair rather
// than spelling out its own ILIKE.
//
// Note this was never an injection: the pattern has always been a bound
// parameter. What leaks is the pattern's meaning, not SQL.

// likeEscapeClause is the ESCAPE suffix every LIKE / ILIKE predicate in this
// package carries. Naming the backslash explicitly is what makes SQLite agree
// with the escaping escapeLike applies (and restates what PostgreSQL already
// assumes).
const likeEscapeClause = ` ESCAPE '\'`

// likeEscaper neutralises the LIKE metacharacters. The escape character goes
// first in a single pass, so an already-escaped sequence is never produced by
// escaping something else.
var likeEscaper = strings.NewReplacer(
	`\`, `\\`,
	`%`, `\%`,
	`_`, `\_`,
)

// escapeLike renders text as a LIKE pattern that matches exactly that text.
func escapeLike(text string) string { return likeEscaper.Replace(text) }

// likeContains is the value to bind for a "contains this text anywhere" match:
// the text escaped to a literal, wrapped in the only two wildcards the query
// itself means.
func likeContains(text string) string { return "%" + escapeLike(text) + "%" }

// likeAnyOf renders "(col1 ILIKE $n ESCAPE '\' OR col2 ILIKE $n ESCAPE '\')"
// over the given columns. They share one placeholder -- both engines allow a
// parameter to appear more than once -- so the caller binds likeContains(text)
// exactly once. ILIKE is rewritten to LIKE for SQLite (see sqliteReplacer),
// whose LIKE already folds case for ASCII.
func likeAnyOf(placeholder string, columns ...string) string {
	parts := make([]string, 0, len(columns))
	for _, col := range columns {
		parts = append(parts, col+` ILIKE `+placeholder+likeEscapeClause)
	}
	return `(` + strings.Join(parts, ` OR `) + `)`
}
