package api

import (
	"reflect"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

// -------------------------------------------------------- applyReadmeFeatures

func TestApplyReadmeFeatures(t *testing.T) {
	cols := []apitypes.ParquetColumn{
		{Name: "image", Type: "GROUP"},                      // no KV metadata hint: README should fill it in
		{Name: "label", Type: "INT64", Feature: "sequence"}, // KV metadata already set a hint: README must not override
		{Name: "text", Type: "BYTE_ARRAY"},                  // README has no hint for this column: stays ""
	}
	feats := map[string]string{
		"image": "image",
		"label": "classlabel",
	}

	got := applyReadmeFeatures(cols, feats)

	want := []apitypes.ParquetColumn{
		{Name: "image", Type: "GROUP", Feature: "image"},
		{Name: "label", Type: "INT64", Feature: "sequence"},
		{Name: "text", Type: "BYTE_ARRAY", Feature: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("applyReadmeFeatures() = %#v, want %#v", got, want)
	}

	// The input slice must not be mutated in place.
	if cols[0].Feature != "" {
		t.Errorf("applyReadmeFeatures mutated its input: cols[0].Feature = %q", cols[0].Feature)
	}
}

func TestApplyReadmeFeatures_NoFeats(t *testing.T) {
	cols := []apitypes.ParquetColumn{{Name: "image", Type: "GROUP"}}
	got := applyReadmeFeatures(cols, nil)
	if !reflect.DeepEqual(got, cols) {
		t.Errorf("applyReadmeFeatures(cols, nil) = %#v, want unchanged %#v", got, cols)
	}
}
