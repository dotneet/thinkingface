package repocard

import (
	"path"
	"strings"
)

// Base model relations, matching the values HuggingFace Hub accepts in a model
// card's top-level `base_model_relation:` field. A repository that declares a
// base model always carries exactly one of these -- declared by the card, or
// inferred from what the repository contains.
const (
	// RelationFinetune is the default: the weights were trained further from
	// the base model's own weights.
	RelationFinetune = "finetune"
	// RelationAdapter is a LoRA/PEFT adapter, meaningless without its base.
	RelationAdapter = "adapter"
	// RelationQuantized is the same model at a lower precision (GGUF, AWQ,
	// GPTQ, bitsandbytes, ...).
	RelationQuantized = "quantized"
	// RelationMerge is a blend of two or more base models.
	RelationMerge = "merge"
)

// KnownRelations are the four relations the UI groups by. Anything else a card
// writes is carried through verbatim and shown under "other", the same way a
// reference that does not resolve is still shown rather than dropped.
var KnownRelations = []string{RelationFinetune, RelationAdapter, RelationQuantized, RelationMerge}

// maxRelationLen bounds a relation coming off a card. The value reaches a
// database column and a UI grouping key, and nothing about a card is trusted.
const maxRelationLen = 64

// adapterMarkers are the file names that only exist in a PEFT/LoRA adapter
// repository. adapter_config.json is what `peft` itself writes and what the
// Hub keys on.
var adapterMarkers = map[string]bool{
	"adapter_config.json": true,
}

// quantMarkerFiles are file names written by a quantisation toolchain.
var quantMarkerFiles = map[string]bool{
	"quantize_config.json":     true,
	"quant_config.json":        true,
	"quantization_config.json": true,
}

// quantMarkerExts are container formats that only exist quantised in practice.
// A .gguf file may hold f16 weights, but a repository publishing one is a
// conversion of some other repository either way, which is what the relation
// records.
var quantMarkerExts = map[string]bool{
	".gguf": true,
	".ggml": true,
}

// quantMarkerWords are tokens that mark a file name or a card tag as belonging
// to a quantised checkpoint. They are matched as whole tokens (a file name is
// split on every non-alphanumeric character) so that "model-Q4_K_M.gguf" and
// the tag "4-bit" hit while a repository merely named "int8-benchmarks" in
// prose does not.
var quantMarkerWords = map[string]bool{
	"gguf": true, "ggml": true, "gptq": true, "awq": true, "exl2": true,
	"aqlm": true, "hqq": true, "quantized": true, "bitsandbytes": true,
	"int2": true, "int3": true, "int4": true, "int8": true,
	"fp4": true, "fp8": true, "nf4": true,
	"2bit": true, "3bit": true, "4bit": true, "5bit": true, "6bit": true, "8bit": true,
	"q2": true, "q3": true, "q4": true, "q5": true, "q6": true, "q8": true,
}

// BaseModelRelation returns the relation the card declares, or "" when it
// declares none.
//
// The HuggingFace spelling -- a top-level `base_model_relation:` -- is the one
// to write, and the one a card authored for the Hub already carries. The
// `lineage:` block may also carry `relation:` / `base_model_relation:`, and
// wins when both are present, for the same reason the block wins over the
// top-level `base_model:` list.
//
// A value outside KnownRelations is returned verbatim (trimmed, and cut to
// maxRelationLen): the card said something, and silently turning that into
// "finetune" would be a lie about provenance. Known values are canonicalised,
// so `Quantized` and `QUANTIZED` both land on "quantized".
func (c Card) BaseModelRelation() string {
	block, _ := c.Data["lineage"].(map[string]any)
	for _, v := range []any{block["relation"], block["base_model_relation"], c.Data["base_model_relation"]} {
		if s, ok := v.(string); ok {
			if r := canonicalRelation(s); r != "" {
				return r
			}
		}
	}
	return ""
}

// canonicalRelation trims a declared value and folds it onto a known relation
// when it is one, leaving anything else alone.
func canonicalRelation(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, known := range KnownRelations {
		if strings.EqualFold(s, known) {
			return known
		}
	}
	if len(s) > maxRelationLen {
		s = s[:maxRelationLen]
	}
	return s
}

// InferBaseModelRelation guesses how a repository relates to its base models
// from what it contains. It is the fallback for a card that declares
// `base_model:` without `base_model_relation:`, which is the common case.
//
// The rules, in the order they are tried:
//
//  1. two or more base models -- a model built from several others is a merge,
//     which is also how the Hub reads a multi-entry `base_model:` list;
//  2. an adapter_config.json anywhere in the tree -- PEFT writes one, and no
//     full checkpoint has it;
//  3. a quantisation marker in a file name or a card tag (a .gguf file, a
//     quantize_config.json, an "awq" / "4-bit" token, or a `quantized_by:`
//     field);
//  4. otherwise finetune, the relation a further-trained checkpoint has.
//
// It is deliberately a pure function of three plain values -- how many base
// models the card names, the card's tags, and the repository's file paths --
// so it can be exercised without a repository, a card parse or a database.
// filePaths are repository-relative ("subdir/adapter_config.json"); the caller
// passes what the file index already holds, so no blob is read to decide this.
func InferBaseModelRelation(baseModels int, tags []string, filePaths []string) string {
	if baseModels >= 2 {
		return RelationMerge
	}
	quantized := false
	for _, p := range filePaths {
		base := strings.ToLower(path.Base(p))
		if adapterMarkers[base] {
			// Adapter beats quantised: a quantised adapter is still an adapter,
			// and it is the stronger statement about what the repository is.
			return RelationAdapter
		}
		if quantized {
			continue
		}
		if quantMarkerFiles[base] || quantMarkerExts[strings.ToLower(path.Ext(base))] || hasQuantWord(base) {
			quantized = true
		}
	}
	if quantized {
		return RelationQuantized
	}
	for _, tag := range tags {
		if hasQuantWord(strings.ToLower(tag)) {
			return RelationQuantized
		}
	}
	return RelationFinetune
}

// ResolveBaseModelRelation is what a caller with a parsed card and a file list
// wants: the declared relation when there is one, the inferred relation
// otherwise, and "" when the card names no base model at all (there is nothing
// for a relation to be about).
func ResolveBaseModelRelation(card Card, filePaths []string) string {
	l := card.Lineage()
	if len(l.BaseModels) == 0 {
		return ""
	}
	if l.BaseModelRelation != "" {
		return l.BaseModelRelation
	}
	return InferBaseModelRelation(len(l.BaseModels), card.quantHintTags(), filePaths)
}

// quantHintTags are the card fields that hint at quantisation: the tag list,
// plus `quantized_by:`, which GGUF re-uploads carry and which says outright
// that the repository is a quantisation of something else.
func (c Card) quantHintTags() []string {
	tags := c.Tags()
	if by, ok := c.Data["quantized_by"].(string); ok && strings.TrimSpace(by) != "" {
		tags = append(tags, RelationQuantized)
	}
	return tags
}

// hasQuantWord reports whether s (already lower-cased) contains a
// quantisation marker as a whole token, where a token is a maximal run of
// letters or digits.
//
// The separators are dropped a second time over the whole string as well, so
// the Hub's hyphenated tag spellings ("4-bit", "8_bit") match the same entry
// as the run-together ones without the table having to list both.
func hasQuantWord(s string) bool {
	var joined strings.Builder
	for _, token := range strings.FieldsFunc(s, isNotAlnum) {
		if quantMarkerWords[token] {
			return true
		}
		joined.WriteString(token)
	}
	return quantMarkerWords[joined.String()]
}

func isNotAlnum(r rune) bool {
	return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
}
