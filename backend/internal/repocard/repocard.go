// Package repocard reads the YAML front matter that HuggingFace repositories
// keep at the top of README.md.
package repocard

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// Card is the parsed front matter plus the markdown body that follows it.
type Card struct {
	Data map[string]any
	Body string
}

// Parse splits a README into front matter and body. A README without front
// matter yields an empty Data map and the whole file as Body.
func Parse(readme []byte) Card {
	text := strings.ReplaceAll(string(readme), "\r\n", "\n")
	card := Card{Data: map[string]any{}, Body: text}

	if !strings.HasPrefix(text, "---\n") {
		return card
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return card
	}
	front := rest[:end]

	body := rest[end+len("\n---"):]
	body = strings.TrimPrefix(body, "\n")

	var data map[string]any
	if err := yaml.Unmarshal([]byte(front), &data); err != nil || data == nil {
		// Malformed front matter is not worth failing an upload over; show the
		// README as-is and leave the card empty.
		return card
	}
	card.Data = normalize(data)
	card.Body = body
	return card
}

// normalize converts YAML's map[any]any into JSON-encodable maps.
func normalize(v any) map[string]any {
	out := map[string]any{}
	m, ok := v.(map[string]any)
	if !ok {
		return out
	}
	for k, val := range m {
		out[k] = normalizeValue(val)
	}
	return out
}

func normalizeValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return normalize(t)
	case map[any]any:
		out := map[string]any{}
		for k, val := range t {
			if ks, ok := k.(string); ok {
				out[ks] = normalizeValue(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = normalizeValue(item)
		}
		return out
	default:
		return v
	}
}

// Description picks a one-line summary: an explicit field if present,
// otherwise the first non-heading paragraph of the body.
func (c Card) Description() string {
	for _, key := range []string{"description", "short_description", "summary"} {
		if v, ok := c.Data[key].(string); ok && v != "" {
			return truncate(strings.TrimSpace(v), 300)
		}
	}
	for _, line := range strings.Split(c.Body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "<") {
			continue
		}
		return truncate(line, 300)
	}
	return ""
}

// Tags returns the card's tag list, always non-nil.
func (c Card) Tags() []string {
	raw, ok := c.Data["tags"]
	if !ok {
		return []string{}
	}
	switch v := raw.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return []string{}
}

// maxLineageRefs bounds one lineage list. A card is user input that anyone
// with write access can push; without a ceiling a single README could write an
// unbounded number of rows into repo_lineage.
const maxLineageRefs = 64

// Lineage is the provenance a repository card declares, as the raw reference
// strings the author wrote. Normalising them into targets is the caller's job
// (internal/syncer), because what a reference resolves to is a question about
// the server's repositories rather than about the card.
type Lineage struct {
	// Datasets are the datasets this repository was built or trained from.
	Datasets []string
	// BaseModels are the checkpoints it started from.
	BaseModels []string
	// EvalDatasets are the datasets it was *evaluated* on, which is a
	// different claim from having been trained on them.
	EvalDatasets []string
	// Runs are the experiment runs that produced it.
	Runs []string
	// NewVersion is the repository that supersedes this one, or "" when the
	// card names none. At most one: a repository has a single successor, and
	// a card listing several has only its first entry read.
	NewVersion string
	// BaseModelRelation is how this repository relates to its base models, as
	// the card declared it, or "" when the card is silent. See
	// BaseModelRelation for the accepted spellings.
	BaseModelRelation string
}

// Empty reports that the card declares no lineage at all.
func (l Lineage) Empty() bool {
	return len(l.Datasets) == 0 && len(l.BaseModels) == 0 && len(l.EvalDatasets) == 0 &&
		len(l.Runs) == 0 && l.NewVersion == ""
}

// Lineage reads the card's `lineage:` block:
//
//	lineage:
//	  datasets:
//	    - team/imdb-ja@v1
//	  base_model: team/bert-base@main
//	  run: team/trackio-metrics/sentiment/run-42
//
// Each key accepts either a single string or a list, and both the singular and
// plural spellings are read (`dataset`/`datasets`, `base_model`/`base_models`,
// `eval_dataset`/`eval_datasets`, `run`/`runs`, `new_version`/`new_versions`).
// A key the block leaves out falls back to the HuggingFace card's own top-level
// fields, so a card written for the Hub carries its lineage over unchanged:
//
//	datasets:            -> Datasets
//	base_model:          -> BaseModels
//	model-index:         -> EvalDatasets (the `dataset:` of each result)
//	eval-results:        -> EvalDatasets (huggingface_hub's flat spelling)
//	new_version:         -> NewVersion
//
// A dataset card's top-level `source_datasets:` is read too, but only for a
// dataset repository, so it goes through LineageFor rather than through here.
func (c Card) Lineage() Lineage {
	block, _ := c.Data["lineage"].(map[string]any)

	pick := func(keys ...string) []string {
		var out []string
		for _, key := range keys {
			out = append(out, stringList(block[key])...)
		}
		return dedupe(out)
	}

	l := Lineage{
		Datasets:     pick("datasets", "dataset"),
		BaseModels:   pick("base_models", "base_model"),
		EvalDatasets: pick("eval_datasets", "eval_dataset"),
		Runs:         pick("runs", "run"),
		NewVersion:   first(pick("new_version", "new_versions")),
	}
	if len(l.Datasets) == 0 {
		l.Datasets = dedupe(stringList(c.Data["datasets"]))
	}
	if len(l.BaseModels) == 0 {
		l.BaseModels = dedupe(append(stringList(c.Data["base_model"]), stringList(c.Data["base_models"])...))
	}
	if len(l.EvalDatasets) == 0 {
		l.EvalDatasets = dedupe(c.evalDatasets())
	}
	if l.NewVersion == "" {
		l.NewVersion = first(dedupe(append(
			stringList(c.Data["new_version"]), stringList(c.Data["new_versions"])...)))
	}
	l.BaseModelRelation = c.BaseModelRelation()
	return l
}

// LineageFor is Lineage as it applies to a repository of the given kind
// ("model" or "dataset"). The only kind-dependent fallback is a dataset card's
// top-level `source_datasets:`, which names the datasets a dataset was derived
// from -- the dataset-card counterpart of a model card's `base_model:`. A
// `lineage:` block, and a plain top-level `datasets:`, both still win over it.
func (c Card) LineageFor(kind string) Lineage {
	l := c.Lineage()
	if kind == "dataset" && len(l.Datasets) == 0 {
		l.Datasets = dedupe(sourceDatasetRefs(c.Data["source_datasets"]))
	}
	return l
}

// first returns the head of a list, or "" for an empty one.
func first(in []string) string {
	if len(in) == 0 {
		return ""
	}
	return in[0]
}

// stringList reads a YAML value that may be a single string or a sequence of
// them, dropping blanks and anything that is not a string.
func stringList(v any) []string {
	switch t := v.(type) {
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return []string{s}
		}
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	}
	return nil
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) == maxLineageRefs {
			break
		}
	}
	return out
}

// IsExperiment reports whether the card marks this repository as holding
// experiment tracking data. trackio-written datasets carry a trackio tag; we
// also accept an explicit thinkingface field.
func (c Card) IsExperiment() bool {
	if v, ok := c.Data["thinkingface_experiment"].(bool); ok {
		return v
	}
	for _, tag := range c.Tags() {
		if strings.EqualFold(tag, "trackio") || strings.EqualFold(tag, "experiment") {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
