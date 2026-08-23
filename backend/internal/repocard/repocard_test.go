package repocard

import (
	"strings"
	"testing"
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
