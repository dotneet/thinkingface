package repocard

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestInferBaseModelRelation(t *testing.T) {
	tests := []struct {
		name       string
		baseModels int
		tags       []string
		files      []string
		want       string
	}{
		{
			name:       "two base models is a merge",
			baseModels: 2,
			files:      []string{"model.safetensors", "config.json"},
			want:       RelationMerge,
		},
		{
			name:       "merge wins over every content signal",
			baseModels: 3,
			tags:       []string{"gguf"},
			files:      []string{"adapter_config.json", "model-q4_k_m.gguf"},
			want:       RelationMerge,
		},
		{
			name:       "adapter_config.json makes it an adapter",
			baseModels: 1,
			files:      []string{"adapter_config.json", "adapter_model.safetensors"},
			want:       RelationAdapter,
		},
		{
			name:       "adapter is found in a subdirectory too",
			baseModels: 1,
			files:      []string{"README.md", "checkpoint-500/adapter_config.json"},
			want:       RelationAdapter,
		},
		{
			name:       "adapter beats a quantisation marker",
			baseModels: 1,
			tags:       []string{"4-bit"},
			files:      []string{"adapter_config.json", "adapter_model-4bit.safetensors"},
			want:       RelationAdapter,
		},
		{
			name:       "a gguf file makes it quantized",
			baseModels: 1,
			files:      []string{"README.md", "llama-3-8b.Q4_K_M.gguf"},
			want:       RelationQuantized,
		},
		{
			name:       "a quantize_config.json makes it quantized",
			baseModels: 1,
			files:      []string{"model.safetensors", "quantize_config.json"},
			want:       RelationQuantized,
		},
		{
			name:       "a quantisation token in the file name counts",
			baseModels: 1,
			files:      []string{"model-awq-4bit.safetensors"},
			want:       RelationQuantized,
		},
		{
			name:       "a card tag alone is enough",
			baseModels: 1,
			tags:       []string{"transformers", "8-bit"},
			files:      []string{"model.safetensors", "config.json"},
			want:       RelationQuantized,
		},
		{
			name:       "a plain checkpoint is a finetune",
			baseModels: 1,
			tags:       []string{"transformers", "text-generation"},
			files:      []string{"config.json", "model-00001-of-00003.safetensors", "tokenizer.json"},
			want:       RelationFinetune,
		},
		{
			name:       "no files at all is still a finetune",
			baseModels: 1,
			want:       RelationFinetune,
		},
		{
			name:       "an ordinary shard name is not a quantisation marker",
			baseModels: 1,
			files:      []string{"pytorch_model-00008-of-00008.bin", "special_tokens_map.json"},
			want:       RelationFinetune,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InferBaseModelRelation(tt.baseModels, tt.tags, tt.files); got != tt.want {
				t.Errorf("InferBaseModelRelation(%d, %v, %v) = %q, want %q",
					tt.baseModels, tt.tags, tt.files, got, tt.want)
			}
		})
	}
}

func TestCardBaseModelRelation(t *testing.T) {
	tests := []struct {
		name   string
		readme string
		want   string
	}{
		{
			name: "top-level HuggingFace field",
			readme: `---
base_model: team/llama-3
base_model_relation: quantized
---
`,
			want: RelationQuantized,
		},
		{
			name: "case and padding are folded onto the canonical value",
			readme: `---
base_model_relation: "  Adapter "
---
`,
			want: RelationAdapter,
		},
		{
			name: "an unknown value is kept verbatim",
			readme: `---
base_model_relation: distillation
---
`,
			want: "distillation",
		},
		{
			name: "the lineage block wins over the top-level field",
			readme: `---
base_model_relation: finetune
lineage:
  base_model: team/bert-base
  relation: merge
---
`,
			want: RelationMerge,
		},
		{
			name: "the lineage block also accepts the HuggingFace key name",
			readme: `---
lineage:
  base_model_relation: adapter
---
`,
			want: RelationAdapter,
		},
		{
			name:   "a card that says nothing declares nothing",
			readme: "---\nbase_model: team/bert-base\n---\n",
			want:   "",
		},
		{
			name: "a non-string value is ignored",
			readme: `---
base_model_relation: 42
---
`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			card := Parse([]byte(tt.readme))
			if got := card.BaseModelRelation(); got != tt.want {
				t.Errorf("BaseModelRelation() = %q, want %q", got, tt.want)
			}
			if got := card.Lineage().BaseModelRelation; got != tt.want {
				t.Errorf("Lineage().BaseModelRelation = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCanonicalRelation_TruncatesUnknownValues(t *testing.T) {
	// Regression: an unknown ASCII relation longer than maxRelationLen is
	// still cut at exactly maxRelationLen characters, unchanged from the
	// byte-slicing behaviour for single-byte text.
	long := strings.Repeat("x", maxRelationLen+20)
	if got := canonicalRelation(long); got != strings.Repeat("x", maxRelationLen) {
		t.Errorf("canonicalRelation(long ASCII) = %q, want %d x's", got, maxRelationLen)
	}

	// The maxRelationLen-th character lands inside a run of 3-byte Japanese
	// characters; the cut must land on a rune boundary so the result stays
	// valid UTF-8 instead of a mid-character byte slice.
	longJA := strings.Repeat("a", maxRelationLen-1) + "あいうえお"
	got := canonicalRelation(longJA)
	if !utf8.ValidString(got) {
		t.Fatalf("canonicalRelation(long Japanese) = %q is not valid UTF-8", got)
	}
	want := strings.Repeat("a", maxRelationLen-1) + "あ"
	if got != want {
		t.Errorf("canonicalRelation(long Japanese) = %q, want %q", got, want)
	}
}

func TestResolveBaseModelRelation(t *testing.T) {
	t.Run("no base model means no relation", func(t *testing.T) {
		card := Parse([]byte("---\ndatasets: team/imdb\n---\n"))
		if got := ResolveBaseModelRelation(card, []string{"adapter_config.json"}); got != "" {
			t.Errorf("relation = %q, want \"\"", got)
		}
	})

	t.Run("a declared relation overrides the inference", func(t *testing.T) {
		card := Parse([]byte("---\nbase_model: team/llama-3\nbase_model_relation: finetune\n---\n"))
		if got := ResolveBaseModelRelation(card, []string{"adapter_config.json"}); got != RelationFinetune {
			t.Errorf("relation = %q, want %q", got, RelationFinetune)
		}
	})

	t.Run("an undeclared relation is inferred from the files", func(t *testing.T) {
		card := Parse([]byte("---\nbase_model: team/llama-3\n---\n"))
		if got := ResolveBaseModelRelation(card, []string{"adapter_config.json"}); got != RelationAdapter {
			t.Errorf("relation = %q, want %q", got, RelationAdapter)
		}
	})

	t.Run("quantized_by marks a re-upload as quantized", func(t *testing.T) {
		card := Parse([]byte("---\nbase_model: team/llama-3\nquantized_by: someone\n---\n"))
		if got := ResolveBaseModelRelation(card, []string{"model.safetensors"}); got != RelationQuantized {
			t.Errorf("relation = %q, want %q", got, RelationQuantized)
		}
	})

	t.Run("a two-entry base_model list merges without any files", func(t *testing.T) {
		card := Parse([]byte("---\nbase_model:\n  - team/a\n  - team/b\n---\n"))
		if got := ResolveBaseModelRelation(card, nil); got != RelationMerge {
			t.Errorf("relation = %q, want %q", got, RelationMerge)
		}
	})
}
