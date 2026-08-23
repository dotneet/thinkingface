package repocard

import (
	"strings"
	"testing"
)

func lineageOf(t *testing.T, kind, front string) Lineage {
	t.Helper()
	return Parse([]byte("---\n" + front + "---\n")).LineageFor(kind)
}

func TestSourceDatasetsFallback(t *testing.T) {
	t.Run("hub dataset card", func(t *testing.T) {
		l := lineageOf(t, "dataset", `source_datasets:
  - laion/laion-2b
  - team/wiki-ja
`)
		if got := strings.Join(l.Datasets, ","); got != "laion/laion-2b,team/wiki-ja" {
			t.Errorf("Datasets = %v", l.Datasets)
		}
	})

	t.Run("classification words are not references", func(t *testing.T) {
		l := lineageOf(t, "dataset", `source_datasets:
  - original
  - extended
  - crowdsourced
  - squad
`)
		if len(l.Datasets) != 0 {
			t.Errorf("Datasets = %v, want none", l.Datasets)
		}
	})

	t.Run("the extended| prefix is stripped", func(t *testing.T) {
		l := lineageOf(t, "dataset", "source_datasets: extended|team/squad-ja\n")
		if len(l.Datasets) != 1 || l.Datasets[0] != "team/squad-ja" {
			t.Errorf("Datasets = %v", l.Datasets)
		}
	})

	t.Run("only for a dataset repository", func(t *testing.T) {
		l := lineageOf(t, "model", "source_datasets: [team/wiki-ja]\n")
		if len(l.Datasets) != 0 {
			t.Errorf("Datasets = %v, want none on a model", l.Datasets)
		}
	})

	t.Run("a lineage block wins", func(t *testing.T) {
		l := lineageOf(t, "dataset", `source_datasets: [wrong/one]
lineage:
  datasets: right/one
`)
		if len(l.Datasets) != 1 || l.Datasets[0] != "right/one" {
			t.Errorf("Datasets = %v", l.Datasets)
		}
	})

	t.Run("a top-level datasets list wins too", func(t *testing.T) {
		l := lineageOf(t, "dataset", `source_datasets: [second/choice]
datasets: [first/choice]
`)
		if len(l.Datasets) != 1 || l.Datasets[0] != "first/choice" {
			t.Errorf("Datasets = %v", l.Datasets)
		}
	})
}

func TestEvalDatasetsFromModelIndex(t *testing.T) {
	l := lineageOf(t, "model", `model-index:
  - name: bert-ja
    results:
      - task:
          type: text-classification
        dataset:
          type: team/imdb-ja
          name: IMDb (ja)
          split: test
        metrics:
          - type: accuracy
            value: 0.93
      - task:
          type: text-classification
        dataset:
          type: team/livedoor
          name: Livedoor
        metrics:
          - type: f1
            value: 0.88
`)
	if got := strings.Join(l.EvalDatasets, ","); got != "team/imdb-ja,team/livedoor" {
		t.Errorf("EvalDatasets = %v", l.EvalDatasets)
	}
	// The evaluation sets must not be confused with the training ones.
	if len(l.Datasets) != 0 {
		t.Errorf("Datasets = %v, want none", l.Datasets)
	}
}

func TestEvalDatasetsDedupeAndNameFallback(t *testing.T) {
	l := lineageOf(t, "model", `model-index:
  - name: m
    results:
      - dataset:
          type: team/imdb-ja
      - dataset:
          type: team/imdb-ja
      - dataset:
          name: team/only-a-name
`)
	if got := strings.Join(l.EvalDatasets, ","); got != "team/imdb-ja,team/only-a-name" {
		t.Errorf("EvalDatasets = %v", l.EvalDatasets)
	}
}

// huggingface_hub's EvalResult serialises flat, with the dataset reference on
// `dataset_type:`. A card written that way must index the same edges.
func TestEvalDatasetsFromEvalResults(t *testing.T) {
	l := lineageOf(t, "model", `eval-results:
  - task_type: text-classification
    dataset_type: team/imdb-ja
    dataset_name: IMDb
    metric_type: accuracy
    metric_value: 0.9
`)
	if len(l.EvalDatasets) != 1 || l.EvalDatasets[0] != "team/imdb-ja" {
		t.Errorf("EvalDatasets = %v", l.EvalDatasets)
	}
}

func TestEvalDatasetsLineageBlockWins(t *testing.T) {
	l := lineageOf(t, "model", `model-index:
  - name: m
    results:
      - dataset:
          type: wrong/one
lineage:
  eval_dataset: right/one
`)
	if len(l.EvalDatasets) != 1 || l.EvalDatasets[0] != "right/one" {
		t.Errorf("EvalDatasets = %v", l.EvalDatasets)
	}
}

func TestEvalDatasetsMalformedModelIndex(t *testing.T) {
	for name, front := range map[string]string{
		"scalar":        "model-index: nonsense\n",
		"no results":    "model-index:\n  - name: m\n",
		"empty dataset": "model-index:\n  - results:\n      - dataset: {}\n",
		"string result": "model-index:\n  - results: [oops]\n",
	} {
		if l := lineageOf(t, "model", front); len(l.EvalDatasets) != 0 {
			t.Errorf("%s: EvalDatasets = %v, want none", name, l.EvalDatasets)
		}
	}
}

func TestNewVersion(t *testing.T) {
	t.Run("hub spelling", func(t *testing.T) {
		l := lineageOf(t, "model", "new_version: team/foo-v2\n")
		if l.NewVersion != "team/foo-v2" {
			t.Errorf("NewVersion = %q", l.NewVersion)
		}
	})

	t.Run("lineage block wins", func(t *testing.T) {
		l := lineageOf(t, "model", "new_version: wrong/one\nlineage:\n  new_version: right/one\n")
		if l.NewVersion != "right/one" {
			t.Errorf("NewVersion = %q", l.NewVersion)
		}
	})

	t.Run("only the first of a list is read", func(t *testing.T) {
		l := lineageOf(t, "model", "new_version:\n  - team/foo-v2\n  - team/foo-v3\n")
		if l.NewVersion != "team/foo-v2" {
			t.Errorf("NewVersion = %q", l.NewVersion)
		}
	})

	t.Run("a dataset may declare one too", func(t *testing.T) {
		l := lineageOf(t, "dataset", "new_version: team/imdb-ja-v2\n")
		if l.NewVersion != "team/imdb-ja-v2" {
			t.Errorf("NewVersion = %q", l.NewVersion)
		}
	})

	t.Run("absent", func(t *testing.T) {
		if l := lineageOf(t, "model", "license: mit\n"); l.NewVersion != "" || !l.Empty() {
			t.Errorf("NewVersion = %q, Empty = %v", l.NewVersion, l.Empty())
		}
	})
}
