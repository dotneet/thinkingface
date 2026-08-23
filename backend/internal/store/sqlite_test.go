package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSQLitePath(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"sqlite:///data/db/tf.db", "/data/db/tf.db", false},
		{"sqlite://relative/tf.db", "relative/tf.db", false},
		{"sqlite:tf.db", "tf.db", false},
		{"sqlite:///tmp/tf.db?cache=shared", "/tmp/tf.db", false},
		{"sqlite://", "", true},
		{"sqlite::memory:", "", true},
		{"sqlite://file::memory:?mode=memory", "", true},
	}
	for _, tt := range tests {
		got, err := sqlitePath(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("sqlitePath(%q) = %q, %v; want %q, err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestTranslateSQLite(t *testing.T) {
	in := `UPDATE t SET updated_at = now() WHERE name ILIKE $1 AND ts <= now()`
	want := `UPDATE t SET updated_at = strftime('%Y-%m-%d %H:%M:%f','now') WHERE name LIKE $1 AND ts <= strftime('%Y-%m-%d %H:%M:%f','now')`
	if got := translateSQLite(in); got != want {
		t.Fatalf("translateSQLite =\n%s\nwant\n%s", got, want)
	}
	// Cached path returns the same result.
	if got := translateSQLite(in); got != want {
		t.Fatalf("cached translateSQLite = %s", got)
	}
	// Placeholders are left alone: modernc binds $N by number.
	if got := translateSQLite(`SELECT $2, $1, $2`); got != `SELECT $2, $1, $2` {
		t.Fatalf("placeholders rewritten: %s", got)
	}
}

func TestSQLiteArgs(t *testing.T) {
	ts := time.Date(2026, 3, 4, 5, 6, 7, 891000000, time.FixedZone("JST", 9*3600))
	var nilTime *time.Time
	var nilBytes []byte
	var nilRaw json.RawMessage
	got := sqliteArgs([]any{ts, &ts, nilTime, []byte(`{"a":1}`), nilBytes, json.RawMessage(`[1]`), nilRaw, "s", int64(3), true, nil})
	want := []any{"2026-03-03 20:06:07.891", "2026-03-03 20:06:07.891", nil, `{"a":1}`, nil, `[1]`, nil, "s", int64(3), true, nil}
	if len(got) != len(want) {
		t.Fatalf("len = %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestSQLiteIsRead(t *testing.T) {
	for q, want := range map[string]bool{
		"SELECT 1":                              true,
		"\n\tselect count(*) FROM t":            true,
		"WITH x AS (SELECT 1) SELECT * FROM x":  true,
		"INSERT INTO t VALUES (1) RETURNING id": false,
		"UPDATE t SET a = 1":                    false,
		"DELETE FROM t":                         false,
		"CREATE TABLE t (a)":                    false,
		"WITHDRAW":                              false,
	} {
		if got := sqliteIsRead(q); got != want {
			t.Errorf("sqliteIsRead(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestBuildFTS5PrefixQuery(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"single word", "bert", `"bert"*`},
		{"two words AND together", "bert base", `"bert"* AND "base"*`},
		{"hyphenated model name is one phrase", "gpt-2", `"gpt-2"*`},
		{"tsquery syntax characters split tokens", "foo|bar&baz(qux)", `"foo"* AND "bar"* AND "baz"* AND "qux"*`},
		{"embedded double quote is escaped", `say"hi`, `"say""hi"*`},
		{"empty input yields empty query", "", ""},
		{"only punctuation yields empty query", "!!!---", ""},
		{"unicode letters pass through", "実験 モデル", `"実験"* AND "モデル"*`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BuildFTS5PrefixQuery(tt.input); got != tt.want {
				t.Errorf("BuildFTS5PrefixQuery(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
	// FTS5 operators in the input never escape the quotes.
	for _, in := range []string{"a OR b", "NOT c", "(x) NEAR y", `^start`, `col:value`, `a*b`} {
		q := BuildFTS5PrefixQuery(in)
		for _, tok := range strings.Split(q, " AND ") {
			if !strings.HasPrefix(tok, `"`) || !strings.HasSuffix(tok, `"*`) {
				t.Errorf("BuildFTS5PrefixQuery(%q) = %q has an unquoted token %q", in, q, tok)
			}
		}
	}
}

func TestBuildRepoWhereSQLiteDialect(t *testing.T) {
	f := RepoFilter{Search: "bert base", Tags: []string{"nlp", "pytorch"}, Task: "summarization", License: "mit"}
	clause, args := buildRepoWhere(sqliteDialect{}, f, repoFilterScopeAll)
	for _, want := range []string{
		`r.id IN (SELECT rowid FROM repositories_fts WHERE repositories_fts MATCH $1)`,
		`json_type(r.card, '$.tags') = 'array'`,
		`EXISTS (SELECT 1 FROM json_each(r.card, '$.tags') WHERE value = $2)`,
		`EXISTS (SELECT 1 FROM json_each(r.card, '$.tags') WHERE value = $3)`,
		`r.card->>'license' = $4`,
		`r.card->>'pipeline_tag' = $5 OR EXISTS (SELECT 1 FROM json_each(r.card, '$.task_categories') WHERE value = $5)`,
	} {
		if !strings.Contains(clause, want) {
			t.Errorf("clause %q does not contain %q", clause, want)
		}
	}
	wantArgs := []any{`"bert"* AND "base"*`, "nlp", "pytorch", "mit", "summarization"}
	if len(args) != len(wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
	for i := range wantArgs {
		if args[i] != wantArgs[i] {
			t.Errorf("args[%d] = %#v, want %#v", i, args[i], wantArgs[i])
		}
	}
	// Search text with no tokens adds no predicate on either dialect.
	for _, d := range []dialect{pgDialect{}, sqliteDialect{}} {
		clause, args := buildRepoWhere(d, RepoFilter{Search: "!!!"}, repoFilterScopeAll)
		if clause != "" || len(args) != 0 {
			t.Errorf("%s: empty search clause = %q args %v", d.name(), clause, args)
		}
	}
}
