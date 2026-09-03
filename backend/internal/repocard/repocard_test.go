package repocard

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLineage(t *testing.T) {
	card := Parse([]byte("---\n" +
		"lineage:\n" +
		"  datasets: [team/imdb-ja@v1, team/wiki-ja]\n" +
		"  base_model: team/bert-base\n" +
		"  run: team/metrics/proj/run-1\n" +
		"---\nbody\n"))

	l := card.Lineage()
	if l.Empty() {
		t.Fatal("Lineage() reported empty for a card that declares one")
	}
	// The raw strings are passed through untouched: resolving a reference to a
	// repository is the syncer's job, not the parser's.
	if len(l.Datasets) != 2 || l.Datasets[0] != "team/imdb-ja@v1" || l.Datasets[1] != "team/wiki-ja" {
		t.Errorf("Datasets = %v", l.Datasets)
	}
	if len(l.BaseModels) != 1 || l.BaseModels[0] != "team/bert-base" {
		t.Errorf("BaseModels = %v", l.BaseModels)
	}
	if len(l.Runs) != 1 || l.Runs[0] != "team/metrics/proj/run-1" {
		t.Errorf("Runs = %v", l.Runs)
	}

	if !Parse([]byte("---\nlicense: mit\n---\n")).Lineage().Empty() {
		t.Error("a card without lineage should report Empty()")
	}
}

func TestParse_NoFrontMatter(t *testing.T) {
	readme := "# Just a title\n\nSome body text.\n"
	card := Parse([]byte(readme))
	if len(card.Data) != 0 {
		t.Errorf("Data = %v, want empty map", card.Data)
	}
	if card.Body != readme {
		t.Errorf("Body = %q, want the whole file unchanged (%q)", card.Body, readme)
	}
}

func TestParse_WithFrontMatter(t *testing.T) {
	// Note: Parse only strips the single newline that terminates the closing
	// "---" line, so a blank separator line between the fence and the body
	// (if present in the source) is preserved in Body as-is. This fixture
	// has no such separator, so Body starts directly with the heading.
	readme := "---\nlicense: mit\ntags:\n  - foo\n  - bar\n---\n# Title\n\nBody text.\n"
	card := Parse([]byte(readme))
	if card.Data["license"] != "mit" {
		t.Errorf("Data[license] = %v, want %q", card.Data["license"], "mit")
	}
	wantBody := "# Title\n\nBody text.\n"
	if card.Body != wantBody {
		t.Errorf("Body = %q, want %q", card.Body, wantBody)
	}
}

func TestParse_MalformedYAML(t *testing.T) {
	// Unbalanced quote / bad indentation - not valid YAML.
	readme := "---\nlicense: \"mit\ntags: [oops\n---\n\nBody\n"
	card := Parse([]byte(readme))
	if len(card.Data) != 0 {
		t.Errorf("Data on malformed YAML = %v, want empty map (front matter should be discarded, not fatal)", card.Data)
	}
	if card.Body != readme {
		t.Errorf("Body on malformed YAML = %q, want the whole original file preserved (%q)", card.Body, readme)
	}
}

func TestParse_UnclosedFrontMatterFence(t *testing.T) {
	readme := "---\nlicense: mit\n\n# Title\n\nNo closing fence.\n"
	card := Parse([]byte(readme))
	if len(card.Data) != 0 {
		t.Errorf("Data with unclosed '---' fence = %v, want empty map", card.Data)
	}
	if card.Body != readme {
		t.Errorf("Body with unclosed fence = %q, want original file unchanged", card.Body)
	}
}

func TestParse_CRLFLineEndings(t *testing.T) {
	readme := "---\r\nlicense: mit\r\n---\r\n# Title\r\n\r\nBody.\r\n"
	card := Parse([]byte(readme))
	if card.Data["license"] != "mit" {
		t.Errorf("Data[license] = %v, want mit (CRLF should be normalized before parsing)", card.Data["license"])
	}
	if strings.Contains(card.Body, "\r") {
		t.Errorf("Body still contains CR bytes after CRLF normalization: %q", card.Body)
	}
	wantBody := "# Title\n\nBody.\n"
	if card.Body != wantBody {
		t.Errorf("Body = %q, want %q", card.Body, wantBody)
	}
}

// TestParse_LeadingBlankLines documents the fix for a card being silently
// dropped when the README starts with one or more blank lines before the
// opening "---" fence -- something an editor's own newline normalization can
// introduce without the author noticing. huggingface_hub's card-loading
// regex is `^\s*---`, so a README that HF reads correctly must not lose its
// front matter here.
func TestParse_LeadingBlankLines(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix string
	}{
		{"one blank line", "\n"},
		{"several blank lines", "\n\n\n"},
		{"leading spaces then newline", "  \n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readme := tc.prefix + "---\nlicense: mit\ntags: [nlp, ja]\n---\nbody\n"
			card := Parse([]byte(readme))
			if card.Data["license"] != "mit" {
				t.Errorf("Data[license] = %v, want mit", card.Data["license"])
			}
			tags := card.Tags()
			if len(tags) != 2 || tags[0] != "nlp" || tags[1] != "ja" {
				t.Errorf("Tags() = %v, want [nlp ja]", tags)
			}
			if card.Body != "body\n" {
				t.Errorf("Body = %q, want %q", card.Body, "body\n")
			}
		})
	}
}

// TestParse_UTF8BOM documents the fix for a card being silently dropped when
// the README starts with a UTF-8 byte-order mark, with or without a leading
// blank line after it -- some editors write a BOM by default, and it is
// invisible in most viewers.
func TestParse_UTF8BOM(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix string
	}{
		{"BOM only", "\uFEFF"},
		{"BOM then blank line", "\uFEFF\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readme := tc.prefix + "---\nlicense: mit\n---\nbody\n"
			card := Parse([]byte(readme))
			if card.Data["license"] != "mit" {
				t.Errorf("Data[license] = %v, want mit", card.Data["license"])
			}
			if card.Body != "body\n" {
				t.Errorf("Body = %q, want %q", card.Body, "body\n")
			}
		})
	}
}

// TestParse_ConventionalLeadingFenceStillWorks guards against a regression
// where skipping leading whitespace before the fence changes behaviour for
// the ordinary case, where "---" is already the very first thing in the
// file.
func TestParse_ConventionalLeadingFenceStillWorks(t *testing.T) {
	readme := "---\nlicense: mit\n---\nbody\n"
	card := Parse([]byte(readme))
	if card.Data["license"] != "mit" {
		t.Errorf("Data[license] = %v, want mit", card.Data["license"])
	}
	if card.Body != "body\n" {
		t.Errorf("Body = %q, want %q", card.Body, "body\n")
	}
}

func TestTags_StringForm(t *testing.T) {
	card := Parse([]byte("---\ntags: trackio\n---\nbody\n"))
	tags := card.Tags()
	if tags == nil {
		t.Fatalf("Tags() = nil, want non-nil")
	}
	if len(tags) != 1 || tags[0] != "trackio" {
		t.Errorf("Tags() = %v, want [trackio]", tags)
	}
}

func TestTags_ArrayForm(t *testing.T) {
	card := Parse([]byte("---\ntags:\n  - a\n  - b\n---\nbody\n"))
	tags := card.Tags()
	if tags == nil {
		t.Fatalf("Tags() = nil, want non-nil")
	}
	if len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Errorf("Tags() = %v, want [a b]", tags)
	}
}

func TestTags_Missing(t *testing.T) {
	card := Parse([]byte("---\nlicense: mit\n---\nbody\n"))
	tags := card.Tags()
	if tags == nil {
		t.Fatalf("Tags() = nil, want non-nil empty slice")
	}
	if len(tags) != 0 {
		t.Errorf("Tags() = %v, want empty", tags)
	}
}

func TestDescription_ExplicitFieldPreferred(t *testing.T) {
	card := Parse([]byte("---\ndescription: The real description.\n---\n\n# Heading\n\nFirst body paragraph.\n"))
	if got := card.Description(); got != "The real description." {
		t.Errorf("Description() = %q, want explicit field value", got)
	}
}

func TestDescription_FallsBackToFirstNonHeadingLine(t *testing.T) {
	card := Parse([]byte("---\nlicense: mit\n---\n\n# Heading one\n\n<div>html noise</div>\n\nThe actual first paragraph.\n\nSecond paragraph.\n"))
	if got := card.Description(); got != "The actual first paragraph." {
		t.Errorf("Description() = %q, want %q", got, "The actual first paragraph.")
	}
}

func TestDescription_EmptyWhenNothingAvailable(t *testing.T) {
	card := Parse([]byte("---\nlicense: mit\n---\n\n# Just a heading\n\n"))
	if got := card.Description(); got != "" {
		t.Errorf("Description() = %q, want empty string", got)
	}
}

func TestDescription_TruncatesASCIIAtRuneLimit(t *testing.T) {
	// Regression: plain ASCII text must still be cut at exactly 300
	// characters, same as before rune-aware truncation.
	long := strings.Repeat("x", 350)
	card := Parse([]byte("---\ndescription: " + long + "\n---\n"))
	got := card.Description()
	want := strings.Repeat("x", 300) + "…"
	if got != want {
		t.Errorf("Description() = %q, want 300 x's plus an ellipsis", got)
	}
}

func TestDescription_TruncatesOnRuneBoundaryForMultibyteText(t *testing.T) {
	// The 300-character cut lands inside a run of 3-byte Japanese characters;
	// truncation must land on a rune boundary rather than slicing mid-character
	// and producing invalid UTF-8 (which json.Marshal would silently replace
	// with U+FFFD in the API response).
	long := strings.Repeat("a", 299) + "あいうえお"
	card := Parse([]byte("---\ndescription: " + long + "\n---\n"))
	got := card.Description()
	if !utf8.ValidString(got) {
		t.Fatalf("Description() = %q is not valid UTF-8", got)
	}
	want := strings.Repeat("a", 299) + "あ" + "…"
	if got != want {
		t.Errorf("Description() = %q, want %q", got, want)
	}
}

func TestIsExperiment(t *testing.T) {
	tests := []struct {
		name   string
		readme string
		want   bool
	}{
		{
			name:   "trackio tag",
			readme: "---\ntags: [trackio]\n---\nbody\n",
			want:   true,
		},
		{
			name:   "explicit thinkingface_experiment true",
			readme: "---\nthinkingface_experiment: true\n---\nbody\n",
			want:   true,
		},
		{
			name:   "explicit thinkingface_experiment false overrides tags",
			readme: "---\nthinkingface_experiment: false\ntags: [trackio]\n---\nbody\n",
			want:   false,
		},
		{
			name:   "neither present",
			readme: "---\nlicense: mit\ntags: [nlp]\n---\nbody\n",
			want:   false,
		},
		{
			name:   "experiment tag case-insensitive",
			readme: "---\ntags: [Experiment]\n---\nbody\n",
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			card := Parse([]byte(tt.readme))
			if got := card.IsExperiment(); got != tt.want {
				t.Errorf("IsExperiment() = %v, want %v (data=%v)", got, tt.want, card.Data)
			}
		})
	}
}

// ------------------------------------------------------- ClosingFence

// The fence rule is shared with the tf CLI (tfcli/local.MergeReadme), so both
// sides read one README the same way. These are the cases the two rules it
// replaced each got wrong: a substring search for "\n---" ended the block on a
// longer horizontal rule, and an exact `line == "---"` refused the trailing
// whitespace editors leave behind and dropped the card entirely.
func TestClosingFence(t *testing.T) {
	tests := []struct {
		name string
		rest string
		want bool // is a fence found at all
	}{
		{"plain fence", "license: mit\n---\nbody", true},
		{"fence with a trailing space", "license: mit\n--- \nbody", true},
		{"fence with a trailing tab", "license: mit\n---\t\nbody", true},
		{"fence at end of file with no newline", "license: mit\n---", true},
		{"four dashes is a horizontal rule, not a fence", "license: mit\n----\nbody", false},
		{"dashes with content is not a fence", "license: mit\n---foo\nbody", false},
		{"leading space is not a fence", "license: mit\n ---\nbody", false},
		{"no fence at all", "license: mit\nbody", false},
		{"the block's own first line does not close it", "---\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClosingFence(tt.rest)
			if (got >= 0) != tt.want {
				t.Fatalf("ClosingFence(%q) = %d, want found=%v", tt.rest, got, tt.want)
			}
		})
	}
}

// A trailing space on the closing fence used to be read differently by the
// two sides: the server accepted it (substring search) while the CLI did not
// (exact match), so `tf up --license` rewrote a card the sync then declined
// to read. Parse is the server half of that agreement.
func TestParse_ClosingFenceWithTrailingWhitespace(t *testing.T) {
	card := Parse([]byte("---\nlicense: mit\ntags:\n  - nlp\n--- \n\n# Title\n"))
	if got := card.Data["license"]; got != "mit" {
		t.Fatalf("license = %v, want mit (the front matter was not recognised)", got)
	}
	if !strings.Contains(card.Body, "# Title") {
		t.Errorf("body = %q, want the markdown after the fence", card.Body)
	}
	if strings.Contains(card.Body, "license:") {
		t.Errorf("body = %q, still contains the front matter", card.Body)
	}
}

// The complement: a longer horizontal rule in the body is not a fence, so a
// README with no front matter keeps all of it as body.
func TestParse_LongerHorizontalRuleDoesNotCloseTheBlock(t *testing.T) {
	card := Parse([]byte("---\nnot really yaml front matter\n----\nbody\n"))
	if len(card.Data) != 0 {
		t.Fatalf("Data = %v, want empty: ---- is a horizontal rule, not a closing fence", card.Data)
	}
}
