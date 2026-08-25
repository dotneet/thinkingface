package store

import (
	"strings"
	"testing"
)

// The unit half of the LIKE escaping: the value that gets bound and the SQL
// that has to interpret it the same way on both engines. The behavioural half
// -- that a search for "%" really does return nothing on Postgres and SQLite
// alike -- lives in the integration tests next to each listing.

func TestEscapeLikeNeutralisesEveryMetacharacter(t *testing.T) {
	cases := []struct{ in, want string }{
		{"bert", "bert"},
		{"100%", `100\%`},
		{"a_b", `a\_b`},
		{`a\b`, `a\\b`},
		// The escape character is rewritten in the same pass as the
		// wildcards, so escaping one never produces a sequence that the
		// other's escaping would then read as already escaped.
		{`\%`, `\\\%`},
		{`%_\`, `\%\_\\`},
	}
	for _, c := range cases {
		if got := escapeLike(c.in); got != c.want {
			t.Errorf("escapeLike(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLikeContainsWrapsOnlyItsOwnWildcards(t *testing.T) {
	if got, want := likeContains("bert"), "%bert%"; got != want {
		t.Errorf("likeContains(bert) = %q, want %q", got, want)
	}
	// The two "%" the query means are the only two left unescaped.
	got := likeContains("50%")
	if want := `%50\%%`; got != want {
		t.Fatalf("likeContains(50%%) = %q, want %q", got, want)
	}
	if n := strings.Count(got, "%") - strings.Count(got, `\%`); n != 2 {
		t.Errorf("%q carries %d unescaped wildcards, want 2", got, n)
	}
}

// The escaping is only half a fix: without the ESCAPE clause SQLite reads the
// backslashes as ordinary characters and matches nothing, so a predicate that
// forgets it is broken on exactly one of the two engines.
func TestLikeAnyOfDeclaresTheEscapeOnEveryColumn(t *testing.T) {
	got := likeAnyOf("$1", "n.name", "n.display_name")
	want := `(n.name ILIKE $1 ESCAPE '\' OR n.display_name ILIKE $1 ESCAPE '\')`
	if got != want {
		t.Fatalf("likeAnyOf = %q, want %q", got, want)
	}
	// SQLite gets ILIKE rewritten to LIKE by a plain substring replace, which
	// only fires on " ILIKE " -- the ESCAPE suffix must not have run the two
	// words together.
	if translated := translateSQLite(got); strings.Contains(translated, "ILIKE") {
		t.Errorf("translateSQLite(%q) = %q: the rewrite no longer matches", got, translated)
	}
}
