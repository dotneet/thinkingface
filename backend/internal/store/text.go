package store

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Text that reaches PostgreSQL has to be valid UTF-8 without NUL, and text
// that reaches this package frequently is neither.
//
// A git path name is any byte string that contains neither NUL nor '/', so a
// repository created on a Latin-1 Windows box carries `caf\xe9.txt` and a
// README's YAML front matter can carry any byte the author's editor emitted.
// SQLite stores those bytes verbatim; PostgreSQL refuses them outright --
// SQLSTATE 22021 (invalid byte sequence for encoding "UTF8") for a text or
// COPY parameter, 22P05 (unsupported Unicode escape sequence) for a NUL
// inside a JSONB string. The refusal lands on the sync worker, which retries
// five times and then parks the job, freezing that repository's file index,
// search entry and blobs/ publication at the push before the offending one.
// One `git push` from an old workstation was enough to do that permanently.
//
// So every value on that path is normalised here instead, and identically on
// both engines: the two backends must not disagree about what is storable.
// The conversion is deliberately lossy and deliberately not reversible --
// git remains the source of truth for the bytes, and every path that hands
// content back to a client (resolve, the tree listing, the git transport)
// reads git rather than this index. What lives in the database is a
// searchable, displayable *description* of the revision, and a replacement
// character in it is a better answer than a repository that stops indexing.
//
// Not being reversible also means not being injective: `Gr\xf6\xdfe.csv` and
// `Gr\xfc\xdfe.csv` are two files to git and one name here, since a whole run
// of invalid bytes becomes a single U+FFFD. Anything keyed on a folded value
// has to expect that (ReplaceRepoFiles keeps the first and skips the rest).
// A reversible escape would not remove the need: whatever it produced is a
// valid name some other file may already have, so collisions would only get
// rarer, and every stored path would stop being what a person reads in git.

// sanitizeText makes s storable on both engines: NUL is dropped and every
// byte that is not part of a valid UTF-8 sequence becomes U+FFFD. A string
// that is already clean is returned unchanged, without allocating.
func sanitizeText(s string) string {
	if utf8.ValidString(s) && !strings.ContainsRune(s, 0) {
		return s
	}
	// ToValidUTF8 fixes the encoding; NUL is valid UTF-8, so it is removed
	// separately. Dropping it rather than replacing it keeps the far more
	// common case -- a trailing NUL from a fixed-width buffer -- from
	// growing a visible glyph.
	return strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "")
}

// SanitizeIndexPath is sanitizeText, exported for the one caller that has to
// agree with it from outside the package: the syncer compares the paths it
// read out of git against the paths ListParquetFiles hands back, and those
// have been through the fold. Without it a `.parquet` whose name is not valid
// UTF-8 is written by UpsertParquetFile under its folded name, fails to match
// the raw name a moment later, and is deleted again as stale within the same
// sync -- so the file the fold was meant to rescue ends up with no index row
// at all.
func SanitizeIndexPath(p string) string { return sanitizeText(p) }

// sanitizeJSONValue walks a decoded JSON value and applies sanitizeText to
// every string in it, keys included. It is what makes a repository card
// storable as JSONB: one NUL anywhere inside it is enough for PostgreSQL to
// reject the whole document.
//
// Only the shapes encoding/json produces are traversed (map[string]any,
// []any, string); anything else -- numbers, booleans, nil -- is returned as
// it came. Maps and slices are rebuilt rather than mutated so a caller's
// value is never modified underneath it: the syncer parses one card and
// passes it to two writers.
func sanitizeJSONValue(v any) any {
	switch t := v.(type) {
	case string:
		return sanitizeText(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[sanitizeText(k)] = sanitizeJSONValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = sanitizeJSONValue(val)
		}
		return out
	default:
		return v
	}
}

// sanitizeJSONMap is sanitizeJSONValue for the top-level card object.
func sanitizeJSONMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out, _ := sanitizeJSONValue(m).(map[string]any)
	return out
}

// sanitizeJSONRaw is sanitizeJSONValue for a document that arrives already
// encoded -- a parquet schema, a webhook payload, an audit entry's details.
// encoding/json replaces invalid UTF-8 as it marshals, but it writes NUL as
// the escape \u0000, which SQLite stores and PostgreSQL's JSONB rejects
// (SQLSTATE 22P05): one column named "a\u0000b" in a parquet file was enough
// to fail UpsertParquetFile on every sync of that repository. So a document
// that could carry either is decoded, walked, and encoded again.
//
// The fast path is the common one and returns raw untouched: valid UTF-8 with
// no \u0000 escape anywhere cannot hold a NUL. (A literal backslash followed
// by "u0000" also trips the check and takes the slow path, which round-trips
// it unchanged.) Numbers are decoded as json.Number so the round trip cannot
// reshape them. A document that does not parse is returned as it came: that
// is a caller's bug for the database to report, not one to mask here. nil
// comes back as nil, which the writers that use it to mean "leave the stored
// value alone" rely on.
func sanitizeJSONRaw(raw []byte) []byte {
	if utf8.Valid(raw) && !bytes.Contains(raw, []byte(`\u0000`)) {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	out, err := json.Marshal(sanitizeJSONValue(v))
	if err != nil {
		return raw
	}
	return out
}
