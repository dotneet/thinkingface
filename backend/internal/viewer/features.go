package viewer

import (
	"encoding/json"
	"strings"

	"github.com/parquet-go/parquet-go"
)

// huggingfaceKVKey is the parquet key-value metadata key that the
// `datasets` library writes its feature schema under.
const huggingfaceKVKey = "huggingface"

// featureTypeHint is the subset of a `datasets` Feature JSON object this
// package cares about: just the `_type` discriminator. Every shape
// `datasets` emits for a top-level feature (Image, Audio, ClassLabel,
// Sequence, List, Value, or a nested struct carrying none of the above)
// carries at most this one relevant field.
type featureTypeHint struct {
	Type string `json:"_type"`
}

// parquetFeatureHints reads the parquet key-value metadata written by
// `datasets` (the "huggingface" key, shaped as
// {"info":{"features":{"<col>": <feature>, ...}}}) and returns a map from
// top-level column name to a lower-cased rendering hint (e.g. "image",
// "audio", "classlabel"). A column is omitted from the map when its feature
// has no `_type` (a nested struct, e.g. {"a": {...}, "b": {...}}) or when
// `_type` is "Value" (a plain scalar) -- both mean "no hint" to callers,
// which look the column up with a plain map index and treat a miss as "".
// Missing metadata, malformed JSON, or an unexpected shape are silently
// ignored: the feature is a rendering hint, never a hard requirement.
func parquetFeatureHints(pf *parquet.File) map[string]string {
	raw, ok := pf.Lookup(huggingfaceKVKey)
	if !ok {
		return nil
	}

	var doc struct {
		Info struct {
			Features map[string]json.RawMessage `json:"features"`
		} `json:"info"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil
	}

	out := make(map[string]string, len(doc.Info.Features))
	for name, featureRaw := range doc.Info.Features {
		if f := featureTypeString(featureRaw); f != "" {
			out[name] = f
		}
	}
	return out
}

// featureTypeString extracts the rendering hint from one column's feature
// JSON: the lower-cased `_type` discriminator, or "" when there is none, it
// failed to parse, or it is "Value" (a plain scalar).
func featureTypeString(raw json.RawMessage) string {
	var hint featureTypeHint
	if err := json.Unmarshal(raw, &hint); err != nil {
		return ""
	}
	if hint.Type == "" || hint.Type == "Value" {
		return ""
	}
	return strings.ToLower(hint.Type)
}
