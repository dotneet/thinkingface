package local

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBuildReadme(t *testing.T) {
	tests := []struct {
		name string
		opts CardOptions
		want string
	}{
		{
			name: "full card",
			opts: CardOptions{License: "mit", Tags: []string{"nlp", "ja"}, Title: "Title", Description: "Description"},
			want: "---\nlicense: mit\ntags:\n  - nlp\n  - ja\ndescription: Description\n---\n\n# Title\n\nDescription\n",
		},
		{
			name: "nothing set",
			opts: CardOptions{},
			want: "",
		},
		{
			name: "title only has no front matter",
			opts: CardOptions{Title: "Hello"},
			want: "# Hello\n",
		},
		{
			name: "description without title goes to front matter and body",
			opts: CardOptions{Description: "Just a description."},
			want: "---\ndescription: Just a description.\n---\n\nJust a description.\n",
		},
		{
			name: "license only, no tags",
			opts: CardOptions{License: "apache-2.0"},
			want: "---\nlicense: apache-2.0\n---\n",
		},
		{
			name: "tags only",
			opts: CardOptions{Tags: []string{"vision"}},
			want: "---\ntags:\n  - vision\n---\n",
		},
		{
			name: "values that YAML would misread are quoted",
			opts: CardOptions{License: "1.0", Tags: []string{"true", "a: b", "plain"}},
			want: "---\nlicense: \"1.0\"\ntags:\n  - \"true\"\n  - \"a: b\"\n  - plain\n---\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(BuildReadme(tt.opts))
			if got != tt.want {
				t.Errorf("BuildReadme() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMergeReadmeKeyOrderAndBody(t *testing.T) {
	existing := "---\ntitle: Existing\nlicense: mit\ntags:\n  - nlp\nauthor: me\n---\n\n# Body\n\nSome text.\n"
	out, err := MergeReadme([]byte(existing), CardOptions{License: "apache-2.0", Tags: []string{"nlp", "ja"}})
	if err != nil {
		t.Fatalf("MergeReadme: %v", err)
	}
	got := string(out)

	// Key order preserved: title, license, tags, author (unchanged keys not
	// reordered), license value updated, tags deduplicated+appended.
	want := "---\ntitle: Existing\nlicense: apache-2.0\ntags:\n  - nlp\n  - ja\nauthor: me\n---\n\n# Body\n\nSome text.\n"
	if got != want {
		t.Errorf("MergeReadme() =\n%q\nwant\n%q", got, want)
	}
}

func TestMergeReadmeNoFrontMatter(t *testing.T) {
	existing := "# Just a body\n\nNo front matter here.\n"
	out, err := MergeReadme([]byte(existing), CardOptions{License: "mit", Tags: []string{"nlp"}})
	if err != nil {
		t.Fatalf("MergeReadme: %v", err)
	}
	got := string(out)
	want := "---\nlicense: mit\ntags:\n  - nlp\n---\n\n# Just a body\n\nNo front matter here.\n"
	if got != want {
		t.Errorf("MergeReadme() =\n%q\nwant\n%q", got, want)
	}
}

func TestMergeReadmeDeduplicatesTags(t *testing.T) {
	existing := "---\ntags:\n  - nlp\n  - ja\n---\n\nBody\n"
	out, err := MergeReadme([]byte(existing), CardOptions{Tags: []string{"ja", "vision"}})
	if err != nil {
		t.Fatalf("MergeReadme: %v", err)
	}
	got := string(out)
	want := "---\ntags:\n  - nlp\n  - ja\n  - vision\n---\n\nBody\n"
	if got != want {
		t.Errorf("MergeReadme() =\n%q\nwant\n%q", got, want)
	}
}

func TestMergeReadmeDescription(t *testing.T) {
	existing := "---\nlicense: mit\n---\n\nBody\n"
	out, err := MergeReadme([]byte(existing), CardOptions{Description: "A new description."})
	if err != nil {
		t.Fatalf("MergeReadme: %v", err)
	}
	got := string(out)
	want := "---\nlicense: mit\ndescription: A new description.\n---\n\nBody\n"
	if got != want {
		t.Errorf("MergeReadme() =\n%q\nwant\n%q", got, want)
	}
}

func TestMergeReadmeMalformedYAML(t *testing.T) {
	existing := "---\nlicense: [unterminated\n---\n\nBody\n"
	if _, err := MergeReadme([]byte(existing), CardOptions{License: "mit"}); err == nil {
		t.Fatal("MergeReadme() with malformed YAML: want error, got nil")
	}
}

func TestMergeReadmeFrontMatterNotAMapping(t *testing.T) {
	existing := "---\n- a\n- b\n---\n\nBody\n"
	if _, err := MergeReadme([]byte(existing), CardOptions{License: "mit"}); err == nil {
		t.Fatal("MergeReadme() with sequence front matter: want error, got nil")
	}
}

func TestMergeReadmeCRLF(t *testing.T) {
	existing := "---\r\nlicense: mit\r\n---\r\n\r\nBody\r\n"
	out, err := MergeReadme([]byte(existing), CardOptions{Tags: []string{"nlp"}})
	if err != nil {
		t.Fatalf("MergeReadme: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "license: mit") || !strings.Contains(got, "tags:\n  - nlp") || !strings.Contains(got, "Body") {
		t.Errorf("MergeReadme() with CRLF input = %q", got)
	}
}

// A "---\n---\n" block (no blank line between the fences, as BuildReadme
// emits for a title-only card) is not recognised as front matter by
// repocard.Parse's delimiter rule (it requires a "\n---" not immediately
// following the opening fence). MergeReadme uses the identical rule for
// consistency, so this input is treated as having no front matter at all:
// a fresh block is prepended and the original bytes become body text.
func TestMergeReadmeEmptyFrontMatterIsNotRecognised(t *testing.T) {
	existing := "---\n---\n\nBody\n"
	out, err := MergeReadme([]byte(existing), CardOptions{License: "mit", Tags: []string{"nlp"}})
	if err != nil {
		t.Fatalf("MergeReadme: %v", err)
	}
	got := string(out)
	want := "---\nlicense: mit\ntags:\n  - nlp\n---\n\n---\n---\n\nBody\n"
	if got != want {
		t.Errorf("MergeReadme() =\n%q\nwant\n%q", got, want)
	}
}

// With a blank line between the fences, the block IS recognised as (empty)
// front matter and gets merged into in place.
func TestMergeReadmeEmptyFrontMatterWithBlankLine(t *testing.T) {
	existing := "---\n\n---\n\nBody\n"
	out, err := MergeReadme([]byte(existing), CardOptions{License: "mit", Tags: []string{"nlp"}})
	if err != nil {
		t.Fatalf("MergeReadme: %v", err)
	}
	got := string(out)
	want := "---\nlicense: mit\ntags:\n  - nlp\n---\n\nBody\n"
	if got != want {
		t.Errorf("MergeReadme() =\n%q\nwant\n%q", got, want)
	}
}

// A tag that looks like a YAML integer (e.g. from `tf up --tag 2024`) must
// come back as the string "2024" on reparse, not the integer 2024 -- an
// unquoted "2024" round-trips through repocard.Card.Tags() as an int and
// gets silently dropped there (item.(string) fails and the tag is skipped).
func TestMergeReadmeQuotesNumericLikeTag(t *testing.T) {
	existing := "---\nlicense: mit\ntags:\n  - nlp\n---\n\nBody\n"
	out, err := MergeReadme([]byte(existing), CardOptions{Tags: []string{"2024"}})
	if err != nil {
		t.Fatalf("MergeReadme: %v", err)
	}
	got := string(out)
	want := "---\nlicense: mit\ntags:\n  - nlp\n  - \"2024\"\n---\n\nBody\n"
	if got != want {
		t.Errorf("MergeReadme() =\n%q\nwant\n%q", got, want)
	}

	// Reparse the produced front matter and confirm the tag survives as a
	// string, the same guarantee BuildReadme already provides.
	var doc struct {
		Tags []string `yaml:"tags"`
	}
	front := strings.TrimPrefix(got, "---\n")
	front = front[:strings.Index(front, "\n---")]
	if err := yaml.Unmarshal([]byte(front), &doc); err != nil {
		t.Fatalf("reparse front matter: %v", err)
	}
	if len(doc.Tags) != 2 || doc.Tags[1] != "2024" {
		t.Errorf("reparsed tags = %#v, want [nlp 2024] with 2024 as a string", doc.Tags)
	}
}

// A license value that looks like a YAML boolean must round-trip as that
// literal string, not as a bool. Note that yaml.v3 (used by both this
// package and repocard.Parse) follows the YAML 1.2 core schema, where only
// true/false (and case variants) resolve as booleans -- unlike YAML 1.1,
// it does *not* treat yes/no/on/off as booleans, so only true/false are
// exercised here; the "no"/"yes"/"on" quoting the original bug report
// worried about was already a non-issue with this library's parser
// (verified with yaml.Unmarshal before writing this test).
func TestMergeReadmeQuotesBooleanLikeLicense(t *testing.T) {
	for _, value := range []string{"true", "false"} {
		t.Run(value, func(t *testing.T) {
			existing := "---\ntitle: Existing\n---\n\nBody\n"
			out, err := MergeReadme([]byte(existing), CardOptions{License: value})
			if err != nil {
				t.Fatalf("MergeReadme: %v", err)
			}
			got := string(out)
			wantLine := "license: \"" + value + "\"\n"
			if !strings.Contains(got, wantLine) {
				t.Errorf("MergeReadme() = %q, want it to contain %q", got, wantLine)
			}

			var doc struct {
				License string `yaml:"license"`
			}
			front := strings.TrimPrefix(got, "---\n")
			front = front[:strings.Index(front, "\n---")]
			if err := yaml.Unmarshal([]byte(front), &doc); err != nil {
				t.Fatalf("reparse front matter: %v", err)
			}
			if doc.License != value {
				t.Errorf("reparsed license = %q, want %q", doc.License, value)
			}
		})
	}
}

// Safe values (no quoting needed) must keep rendering unquoted through the
// yaml.Node merge path too, matching yamlScalar's behavior in BuildReadme.
func TestMergeReadmeLeavesSafeValuesUnquoted(t *testing.T) {
	existing := "---\ntitle: Existing\n---\n\nBody\n"
	out, err := MergeReadme([]byte(existing), CardOptions{License: "mit", Tags: []string{"nlp", "ja"}, Description: "A model."})
	if err != nil {
		t.Fatalf("MergeReadme: %v", err)
	}
	got := string(out)
	want := "---\ntitle: Existing\nlicense: mit\ntags:\n  - nlp\n  - ja\ndescription: A model.\n---\n\nBody\n"
	if got != want {
		t.Errorf("MergeReadme() =\n%q\nwant\n%q", got, want)
	}
}
