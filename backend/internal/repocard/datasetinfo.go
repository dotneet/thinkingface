package repocard

import "strings"

// scalarDtypes is the set of `datasets`/Arrow dtype names that describe a
// plain scalar (a Value feature), which carries no rendering hint.
var scalarDtypes = map[string]bool{
	"string": true, "large_string": true,
	"binary": true, "large_binary": true,
	"bool": true, "null": true,
	"int8": true, "int16": true, "int32": true, "int64": true,
	"uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"float16": true, "float32": true, "float64": true,
	"date32": true, "date64": true,
}

// scalarDtypePrefixes covers the parameterized scalar dtypes ("timestamp[us]",
// "decimal128(9,2)", ...), which are only ever emitted with an argument list.
var scalarDtypePrefixes = []string{"timestamp[", "time32[", "time64[", "duration[", "decimal"}

// isScalarDtype reports whether dtype names a plain scalar (Value) type, as
// opposed to a nested/complex feature such as "image" or "audio".
func isScalarDtype(dtype string) bool {
	if scalarDtypes[dtype] {
		return true
	}
	for _, prefix := range scalarDtypePrefixes {
		if strings.HasPrefix(dtype, prefix) {
			return true
		}
	}
	return false
}

// DatasetFeatures reads the `dataset_info.features` declared in the card's
// front matter and returns a map from column name to a lower-cased
// rendering hint, using the same vocabulary as the parquet viewer's own
// `feature` metadata (e.g. "image", "audio", "classlabel"). Plain scalar
// (Value) columns, and any column the card does not describe, are omitted
// from the result.
//
// `dataset_info` may be a single mapping or a list of them (one per dataset
// config); the first entry that declares a `features` list is used. Each
// element of `features` is a `{name, dtype}` pair, where `dtype` is either a
// scalar type name, a single-key mapping such as
// `{class_label: {names: [...]}}` (the key, with underscores stripped and
// lower-cased, is the hint), or absent -- in which case the element itself
// may carry a `list:`/`struct:`/`sequence:` key directly (nested notation),
// whose key name is the hint.
func (c Card) DatasetFeatures() map[string]string {
	entry := firstDatasetInfoWithFeatures(c.Data["dataset_info"])
	if entry == nil {
		return nil
	}
	items, _ := entry["features"].([]any)

	out := make(map[string]string, len(items))
	for _, item := range items {
		elem, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := elem["name"].(string)
		if name == "" {
			continue
		}
		if f := featureHintFromElement(elem); f != "" {
			out[name] = f
		}
	}
	return out
}

// firstDatasetInfoWithFeatures returns the first mapping in raw (itself a
// mapping, or a list of them) that declares a "features" key, or nil when
// there is none.
func firstDatasetInfoWithFeatures(raw any) map[string]any {
	switch v := raw.(type) {
	case map[string]any:
		if _, ok := v["features"]; ok {
			return v
		}
	case []any:
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if _, ok := m["features"]; ok {
				return m
			}
		}
	}
	return nil
}

// featureHintFromElement extracts the rendering hint from one
// `dataset_info.features` element (already known to have a `name`).
func featureHintFromElement(elem map[string]any) string {
	if dtype, ok := elem["dtype"]; ok {
		return featureHintFromDtype(dtype)
	}
	// No `dtype`: nested notation may put the shape key directly on the
	// element, e.g. {name: "col", sequence: {...}}.
	for _, key := range []string{"list", "struct", "sequence"} {
		if _, ok := elem[key]; ok {
			return key
		}
	}
	return ""
}

// featureHintFromDtype extracts the rendering hint from a `dtype` value,
// which is either a scalar type name (string) or a single-key mapping
// naming a complex feature (e.g. {class_label: {...}}).
func featureHintFromDtype(dtype any) string {
	switch v := dtype.(type) {
	case string:
		if isScalarDtype(v) {
			return ""
		}
		return strings.ToLower(v)
	case map[string]any:
		key := soleOrFirstKey(v)
		if key == "" {
			return ""
		}
		return strings.ToLower(strings.ReplaceAll(key, "_", ""))
	}
	return ""
}

// soleOrFirstKey returns m's only key, or -- when m unexpectedly has more
// than one -- its lexicographically smallest, for deterministic output.
// Returns "" for an empty map.
func soleOrFirstKey(m map[string]any) string {
	var first string
	for k := range m {
		if first == "" || k < first {
			first = k
		}
	}
	return first
}
