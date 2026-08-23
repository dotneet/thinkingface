package repocard

import (
	"reflect"
	"testing"
)

func TestDatasetFeatures_SingleConfig(t *testing.T) {
	card := Parse([]byte("---\n" +
		"dataset_info:\n" +
		"  features:\n" +
		"    - name: image\n" +
		"      dtype: image\n" +
		"    - name: label\n" +
		"      dtype: {class_label: {names: [neg, pos]}}\n" +
		"    - name: text\n" +
		"      dtype: string\n" +
		"    - name: created\n" +
		"      dtype: timestamp[us]\n" +
		"    - name: tags\n" +
		"      dtype: {sequence: {dtype: string}}\n" +
		"---\nbody\n"))

	got := card.DatasetFeatures()
	want := map[string]string{
		"image": "image",
		"label": "classlabel",
		"tags":  "sequence",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DatasetFeatures() = %#v, want %#v", got, want)
	}
}

func TestDatasetFeatures_MultipleConfigsUsesFirstWithFeatures(t *testing.T) {
	card := Parse([]byte("---\n" +
		"dataset_info:\n" +
		"  - config_name: no-features-here\n" +
		"  - config_name: default\n" +
		"    features:\n" +
		"      - name: audio\n" +
		"        dtype: audio\n" +
		"  - config_name: other\n" +
		"    features:\n" +
		"      - name: should_not_be_used\n" +
		"        dtype: image\n" +
		"---\nbody\n"))

	got := card.DatasetFeatures()
	want := map[string]string{"audio": "audio"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DatasetFeatures() = %#v, want %#v", got, want)
	}
}

func TestDatasetFeatures_NestedListStructKeyWithoutDtype(t *testing.T) {
	card := Parse([]byte("---\n" +
		"dataset_info:\n" +
		"  features:\n" +
		"    - name: items\n" +
		"      list:\n" +
		"        dtype: string\n" +
		"    - name: meta\n" +
		"      struct:\n" +
		"        - name: k\n" +
		"          dtype: string\n" +
		"---\nbody\n"))

	got := card.DatasetFeatures()
	want := map[string]string{"items": "list", "meta": "struct"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DatasetFeatures() = %#v, want %#v", got, want)
	}
}

func TestDatasetFeatures_NoDatasetInfo(t *testing.T) {
	card := Parse([]byte("---\nlicense: mit\n---\nbody\n"))
	if got := card.DatasetFeatures(); len(got) != 0 {
		t.Errorf("DatasetFeatures() = %#v, want empty", got)
	}
}

func TestIsScalarDtype(t *testing.T) {
	scalar := []string{
		"string", "large_string", "binary", "large_binary", "bool", "null",
		"int8", "int64", "uint32", "float16", "float64",
		"date32", "date64",
		"timestamp[us]", "time32[s]", "time64[ns]", "duration[us]",
		"decimal128(9,2)",
	}
	for _, dt := range scalar {
		if !isScalarDtype(dt) {
			t.Errorf("isScalarDtype(%q) = false, want true", dt)
		}
	}
	nonScalar := []string{"image", "audio", "class_label"}
	for _, dt := range nonScalar {
		if isScalarDtype(dt) {
			t.Errorf("isScalarDtype(%q) = true, want false", dt)
		}
	}
}
