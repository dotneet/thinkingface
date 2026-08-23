package syncer

import (
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/repocard"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

func edgesFor(t *testing.T, readme string, paths ...string) []store.LineageEdge {
	t.Helper()
	return edgesForKind(t, "model", readme, paths...)
}

func edgesForKind(t *testing.T, kind, readme string, paths ...string) []store.LineageEdge {
	t.Helper()
	files := make([]store.RepoFile, len(paths))
	for i, p := range paths {
		files[i] = store.RepoFile{Path: p}
	}
	return lineageEdges(kind, repocard.Parse([]byte(readme)), files)
}

func find(edges []store.LineageEdge, kind, raw string) *store.LineageEdge {
	for i := range edges {
		if edges[i].Kind == kind && edges[i].Raw == raw {
			return &edges[i]
		}
	}
	return nil
}

func TestLineageEdgesFullCard(t *testing.T) {
	edges := edgesFor(t, `---
license: apache-2.0
lineage:
  datasets:
    - team/imdb-ja@a1b2c3d
    - team/wiki-ja
  base_model: team/bert-base@main
  run: team/trackio-metrics/sentiment/run-42
---

# model
`)
	if len(edges) != 4 {
		t.Fatalf("want 4 edges, got %d: %+v", len(edges), edges)
	}

	ds := find(edges, store.LineageKindDataset, "team/imdb-ja@a1b2c3d")
	if ds == nil {
		t.Fatal("pinned dataset edge missing")
	}
	if ds.Namespace != "team" || ds.Name != "imdb-ja" || ds.Rev != "a1b2c3d" {
		t.Errorf("dataset target = %+v", *ds)
	}
	if ds.Ordinal != 0 {
		t.Errorf("first dataset ordinal = %d, want 0", ds.Ordinal)
	}

	plain := find(edges, store.LineageKindDataset, "team/wiki-ja")
	if plain == nil || plain.Rev != "" || plain.Name != "wiki-ja" || plain.Ordinal != 1 {
		t.Errorf("unpinned dataset edge = %+v", plain)
	}

	base := find(edges, store.LineageKindBaseModel, "team/bert-base@main")
	if base == nil || base.Namespace != "team" || base.Name != "bert-base" || base.Rev != "main" {
		t.Errorf("base model edge = %+v", base)
	}
	if got := base.TargetKind("model"); got != "model" {
		t.Errorf("base model target kind = %q, want model", got)
	}

	run := find(edges, store.LineageKindRun, "team/trackio-metrics/sentiment/run-42")
	if run == nil {
		t.Fatal("run edge missing")
	}
	if run.Namespace != "team" || run.Name != "trackio-metrics" ||
		run.Project != "sentiment" || run.Run != "run-42" {
		t.Errorf("run target = %+v", *run)
	}
	if got := run.TargetKind("model"); got != "dataset" {
		t.Errorf("run target kind = %q, want dataset", got)
	}
}

func TestLineageEdgesSingularAndPluralKeys(t *testing.T) {
	edges := edgesFor(t, `---
lineage:
  dataset: team/imdb-ja
  base_models:
    - team/bert-base
    - other/roberta
  runs:
    - team/metrics/proj/run-1
---
`)
	if len(edges) != 4 {
		t.Fatalf("want 4 edges, got %d: %+v", len(edges), edges)
	}
	if e := find(edges, store.LineageKindDataset, "team/imdb-ja"); e == nil || e.Name != "imdb-ja" {
		t.Errorf("singular dataset key not read: %+v", e)
	}
	if e := find(edges, store.LineageKindBaseModel, "other/roberta"); e == nil || e.Ordinal != 1 {
		t.Errorf("second base model = %+v", e)
	}
}

// A card written for the HuggingFace Hub carries its provenance in top-level
// `datasets:` / `base_model:` fields; those must index the same way.
func TestLineageEdgesFallsBackToHubCardFields(t *testing.T) {
	edges := edgesFor(t, `---
datasets:
  - team/imdb-ja
base_model: team/bert-base
---
`)
	if len(edges) != 2 {
		t.Fatalf("want 2 edges, got %d: %+v", len(edges), edges)
	}
	if e := find(edges, store.LineageKindDataset, "team/imdb-ja"); e == nil || e.Namespace != "team" {
		t.Errorf("top-level datasets not read: %+v", e)
	}
	if e := find(edges, store.LineageKindBaseModel, "team/bert-base"); e == nil || e.Name != "bert-base" {
		t.Errorf("top-level base_model not read: %+v", e)
	}
}

// An explicit lineage block wins over the top-level field it overlaps with, so
// an author can correct a Hub card without deleting its original fields.
func TestLineageBlockOverridesHubCardField(t *testing.T) {
	edges := edgesFor(t, `---
datasets:
  - wrong/dataset
lineage:
  datasets:
    - right/dataset
---
`)
	if len(edges) != 1 || edges[0].Name != "dataset" || edges[0].Namespace != "right" {
		t.Fatalf("lineage block did not win: %+v", edges)
	}
}

// A reference that does not parse must survive as a dangling edge: the UI
// shows the raw text and marks it unresolved.
func TestLineageEdgesKeepUnparseableReferences(t *testing.T) {
	edges := edgesFor(t, `---
lineage:
  datasets:
    - just-a-name
    - team//empty
    - a/b/c
  run:
    - team/metrics/proj
---
`)
	if len(edges) != 4 {
		t.Fatalf("want 4 edges, got %d: %+v", len(edges), edges)
	}
	for _, e := range edges {
		if e.Namespace != "" || e.Name != "" || e.Project != "" || e.Run != "" {
			t.Errorf("edge %q resolved to a target but should be dangling: %+v", e.Raw, e)
		}
		if e.Raw == "" {
			t.Errorf("edge lost its raw text: %+v", e)
		}
	}
}

func TestLineageEdgesTrimsAndDedupes(t *testing.T) {
	edges := edgesFor(t, `---
lineage:
  datasets:
    - "  team/imdb-ja  "
    - team/imdb-ja
    - ""
  base_model: /team/bert-base/
---
`)
	if len(edges) != 2 {
		t.Fatalf("want 2 edges, got %d: %+v", len(edges), edges)
	}
	if e := find(edges, store.LineageKindDataset, "team/imdb-ja"); e == nil || e.Name != "imdb-ja" {
		t.Errorf("whitespace not trimmed before dedupe: %+v", edges)
	}
	if e := find(edges, store.LineageKindBaseModel, "/team/bert-base/"); e == nil ||
		e.Namespace != "team" || e.Name != "bert-base" {
		t.Errorf("surrounding slashes not tolerated: %+v", e)
	}
}

func TestLineageEdgesEmptyCard(t *testing.T) {
	for name, readme := range map[string]string{
		"no front matter": "# just a readme\n",
		"no lineage key":  "---\nlicense: mit\ntags: [a]\n---\n",
		"lineage is null": "---\nlineage:\n---\n",
		"lineage is text": "---\nlineage: nonsense\n---\n",
	} {
		if edges := edgesFor(t, readme); len(edges) != 0 {
			t.Errorf("%s: want no edges, got %+v", name, edges)
		}
	}
}

// A card is user input, so the number of rows one push can write is capped.
func TestLineageEdgesAreBounded(t *testing.T) {
	readme := "---\nlineage:\n  datasets:\n"
	for i := range 200 {
		readme += "    - team/ds-" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "\n"
	}
	readme += "---\n"
	edges := edgesFor(t, readme)
	if len(edges) == 0 || len(edges) > 64 {
		t.Fatalf("want 1..64 edges, got %d", len(edges))
	}
}

// The base model relation lands on the base_model edges and nowhere else. The
// inference itself is tested in internal/repocard; what matters here is that
// the file index reaches it and that the result is attached to the right rows.
func TestLineageEdgesCarryBaseModelRelation(t *testing.T) {
	t.Run("inferred from the file index", func(t *testing.T) {
		edges := edgesFor(t, `---
base_model: team/llama-3
datasets: team/imdb-ja
---
`, "README.md", "adapter_config.json", "adapter_model.safetensors")

		base := find(edges, store.LineageKindBaseModel, "team/llama-3")
		if base == nil || base.Relation != repocard.RelationAdapter {
			t.Errorf("base model relation = %+v, want %q", base, repocard.RelationAdapter)
		}
		if ds := find(edges, store.LineageKindDataset, "team/imdb-ja"); ds == nil || ds.Relation != "" {
			t.Errorf("dataset edge relation = %+v, want \"\"", ds)
		}
	})

	t.Run("declared on the card wins over the files", func(t *testing.T) {
		edges := edgesFor(t, `---
base_model: team/llama-3
base_model_relation: quantized
---
`, "adapter_config.json")

		if base := find(edges, store.LineageKindBaseModel, "team/llama-3"); base == nil ||
			base.Relation != repocard.RelationQuantized {
			t.Errorf("base model relation = %+v, want %q", base, repocard.RelationQuantized)
		}
	})

	t.Run("every base model of a merge gets the same relation", func(t *testing.T) {
		edges := edgesFor(t, `---
base_model:
  - team/a
  - team/b
---
`, "model.safetensors")

		for _, e := range edges {
			if e.Relation != repocard.RelationMerge {
				t.Errorf("edge %q relation = %q, want %q", e.Raw, e.Relation, repocard.RelationMerge)
			}
		}
	})

	t.Run("no base model means no relation anywhere", func(t *testing.T) {
		edges := edgesFor(t, "---\ndatasets: team/imdb-ja\n---\n", "adapter_config.json")
		for _, e := range edges {
			if e.Relation != "" {
				t.Errorf("edge %q relation = %q, want \"\"", e.Raw, e.Relation)
			}
		}
	})
}

// A HuggingFace dataset card declares where it came from in `source_datasets:`.
// It must index as a dataset edge, but only for a dataset repository.
func TestLineageEdgesReadSourceDatasets(t *testing.T) {
	card := `---
source_datasets:
  - original
  - team/wiki-ja
---
`
	edges := edgesForKind(t, "dataset", card)
	if len(edges) != 1 {
		t.Fatalf("want 1 edge, got %d: %+v", len(edges), edges)
	}
	if e := find(edges, store.LineageKindDataset, "team/wiki-ja"); e == nil || e.Name != "wiki-ja" {
		t.Errorf("source_datasets not read: %+v", edges)
	}
	if edges := edgesForKind(t, "model", card); len(edges) != 0 {
		t.Errorf("source_datasets read on a model: %+v", edges)
	}
}

// Evaluation datasets get their own edge kind: "measured on" is not "trained
// on", and the two must not collapse into one list.
func TestLineageEdgesReadEvalDatasets(t *testing.T) {
	edges := edgesFor(t, `---
datasets: team/train-ja
model-index:
  - name: bert-ja
    results:
      - dataset:
          type: team/imdb-ja
        metrics:
          - type: accuracy
            value: 0.9
---
`)
	if len(edges) != 2 {
		t.Fatalf("want 2 edges, got %d: %+v", len(edges), edges)
	}
	eval := find(edges, store.LineageKindEvalDataset, "team/imdb-ja")
	if eval == nil || eval.Namespace != "team" || eval.Name != "imdb-ja" {
		t.Errorf("eval dataset edge = %+v", eval)
	}
	if eval != nil && eval.TargetKind("model") != "dataset" {
		t.Errorf("eval dataset target kind = %q", eval.TargetKind("model"))
	}
	if find(edges, store.LineageKindDataset, "team/train-ja") == nil {
		t.Errorf("training dataset edge lost: %+v", edges)
	}
}

// The successor edge points forward in time and targets a repository of the
// declaring repository's own kind, so a dataset may declare one too.
func TestLineageEdgesReadNewVersion(t *testing.T) {
	for _, kind := range []string{"model", "dataset"} {
		edges := edgesForKind(t, kind, "---\nnew_version: team/foo-v2@main\n---\n")
		if len(edges) != 1 {
			t.Fatalf("%s: want 1 edge, got %+v", kind, edges)
		}
		e := edges[0]
		if e.Kind != store.LineageKindNewVersion || e.Namespace != "team" || e.Name != "foo-v2" || e.Rev != "main" {
			t.Errorf("%s: new version edge = %+v", kind, e)
		}
		if got := e.TargetKind(kind); got != kind {
			t.Errorf("%s: new version target kind = %q", kind, got)
		}
	}
}

// Only one successor edge is written even when the card lists several: the
// chain walk on the read side would have no way to choose between them.
func TestLineageEdgesKeepOneNewVersion(t *testing.T) {
	edges := edgesFor(t, "---\nnew_version:\n  - team/foo-v2\n  - team/foo-v3\n---\n")
	if len(edges) != 1 || edges[0].Name != "foo-v2" {
		t.Fatalf("new version edges = %+v", edges)
	}
}
