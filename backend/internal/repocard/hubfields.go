package repocard

import "strings"

// The HuggingFace card fields that carry lineage outside the `lineage:` block
// and outside the two obvious ones (`datasets:` / `base_model:`): a dataset
// card's `source_datasets:`, and the evaluation datasets buried in a model
// card's `model-index:`.
//
// Everything here is a pure function of the parsed front matter, so the rules
// can be exercised from a YAML string alone.

// sourceDatasetRefs reads a HuggingFace dataset card's top-level
// `source_datasets:` -- the datasets this one was derived from.
//
// The Hub's vocabulary mixes two things in that one list: bare classification
// words ("original", "extended", "crowdsourced") and, in the "extended" case,
// the dataset that was extended, written either as a plain Hub id or with an
// `extended|` prefix. Only the references survive here: an edge saying a
// dataset came from "original" would be noise the UI could never resolve,
// while a real reference that happens to be a typo is still kept (dangling)
// the same way every other reference is.
//
// A reference is anything left over that names a namespace, i.e. contains a
// slash. The Hub's unnamespaced canonical ids ("squad", "imdb") are dropped
// with the classification words, because on this server every repository
// lives under a namespace and such a value could never resolve.
func sourceDatasetRefs(v any) []string {
	var out []string
	for _, s := range stringList(v) {
		if i := strings.Index(s, "|"); i >= 0 {
			s = strings.TrimSpace(s[i+1:])
		}
		if !strings.Contains(s, "/") {
			continue
		}
		out = append(out, s)
	}
	return out
}

// evalDatasets pulls the datasets a model card says it was *evaluated* on,
// which is a different claim from the `datasets:` it was trained on.
//
// Two spellings are read, because the Hub has two:
//
//  1. `model-index:`, the standard one -- a list of models, each with
//     `results:`, each result naming a `dataset:` mapping whose `type:` is the
//     Hub id;
//  2. `eval-results:` / `eval_results:`, the flat shape `huggingface_hub`'s
//     EvalResult serialises to, where the same value is `dataset_type:`.
//
// Only the dataset reference is read. The metric values next to it are out of
// scope: showing evaluation numbers is the experiment tracker's job, not the
// lineage index's.
func (c Card) evalDatasets() []string {
	var out []string
	for _, entry := range anyList(c.Data["model-index"], c.Data["model_index"]) {
		model, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		for _, item := range anyList(model["results"]) {
			result, ok := item.(map[string]any)
			if !ok {
				continue
			}
			// `dataset:` is one mapping in the spec; a list of them appears in
			// the wild and costs nothing to accept.
			for _, ds := range anyList(result["dataset"], result["datasets"]) {
				out = append(out, mappingRef(ds, "type", "name")...)
			}
		}
	}
	for _, entry := range anyList(c.Data["eval-results"], c.Data["eval_results"]) {
		out = append(out, mappingRef(entry, "dataset_type", "dataset", "dataset_name")...)
	}
	return out
}

// mappingRef reads the first non-empty of keys from a YAML mapping, or reads
// the value itself when the card wrote a bare string where a mapping was
// expected.
func mappingRef(v any, keys ...string) []string {
	if s, ok := v.(string); ok {
		return stringList(s)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	for _, key := range keys {
		if refs := stringList(m[key]); len(refs) > 0 {
			return refs[:1]
		}
	}
	return nil
}

// anyList flattens the given YAML values into one sequence, treating a lone
// mapping or scalar as a sequence of one. YAML lets an author write either,
// and both spellings mean the same thing here.
func anyList(vs ...any) []any {
	var out []any
	for _, v := range vs {
		switch t := v.(type) {
		case nil:
		case []any:
			out = append(out, t...)
		default:
			out = append(out, t)
		}
	}
	return out
}
