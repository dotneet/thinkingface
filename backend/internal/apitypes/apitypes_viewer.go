package apitypes

// ---------------------------------------------------------- parquet viewer

// ParquetColumn describes one column of a parquet schema.
type ParquetColumn struct {
	// Name is the column's name.
	Name string `json:"name"`
	// Type is the physical parquet type, e.g. "INT64", "BYTE_ARRAY". For
	// non-leaf (nested group) columns this is "GROUP".
	Type string `json:"type"`
	// LogicalType is the logical type annotation, e.g. "STRING",
	// "TIMESTAMP(MICROS)", "LIST", "MAP", or "" when none is set.
	LogicalType string `json:"logical_type"`
	// Optional reports whether the column may be null.
	Optional bool `json:"optional"`
	// Repeated reports whether the column may hold multiple values per row.
	Repeated bool `json:"repeated"`
	// Feature is the Hugging Face `datasets` feature type of the column,
	// lower-cased (e.g. "image", "audio", "classlabel"), when the file or the
	// repository's README declares one; "" otherwise. The viewer reads it
	// from the parquet key-value metadata written by `datasets` (the
	// "huggingface" key) and falls back to the README's
	// `dataset_info.features`. It is a rendering hint only: an "image"
	// column's values are the usual `{bytes, path}` struct or raw bytes.
	Feature string `json:"feature"`
}

// ParquetSchemaResponse describes a parquet file without reading its rows.
type ParquetSchemaResponse struct {
	Path         string          `json:"path"`
	Size         int64           `json:"size"`
	NumRows      int64           `json:"num_rows"`
	NumRowGroups int             `json:"num_row_groups"`
	Compression  string          `json:"compression"`
	Columns      []ParquetColumn `json:"columns"`
}

// ParquetRowsResponse is one page of decoded parquet rows.
type ParquetRowsResponse struct {
	Path string `json:"path"`
	// Offset is the row offset this page starts at.
	Offset int64 `json:"offset"`
	// Limit is the page size that was applied, after clamping.
	Limit int `json:"limit"`
	// NumRows is the total number of rows in the file, not just this page.
	NumRows int64 `json:"num_rows"`
	// Columns describes the columns present in Rows, in the requested order.
	Columns []ParquetColumn `json:"columns"`
	// Rows holds one JSON-safe object per row, keyed by column name.
	Rows []map[string]any `json:"rows"`
}

// ---------------------------------------------------------- model inspector

// ModelTensor is one named tensor in a checkpoint.
type ModelTensor struct {
	Name string `json:"name"`
	// DType is the framework-neutral dtype name, e.g. "float32", "bfloat16".
	DType string  `json:"dtype"`
	Shape []int64 `json:"shape"`
	// NumParameters is the product of Shape (1 for a scalar tensor).
	NumParameters int64 `json:"num_parameters"`
	// SizeBytes is NumParameters * the dtype's width, 0 when the width is
	// unknown.
	SizeBytes int64 `json:"size_bytes"`
}

// ModelDTypeStat aggregates the tensors sharing one dtype.
type ModelDTypeStat struct {
	DType         string `json:"dtype"`
	NumTensors    int    `json:"num_tensors"`
	NumParameters int64  `json:"num_parameters"`
	SizeBytes     int64  `json:"size_bytes"`
}

// ModelInfo is everything the inspector learns from a checkpoint's header.
type ModelInfo struct {
	Format ModelFormat `json:"format"`
	// NumTensors, NumParameters and TensorBytes cover the whole file even
	// when Tensors below is truncated.
	NumTensors    int              `json:"num_tensors"`
	NumParameters int64            `json:"num_parameters"`
	TensorBytes   int64            `json:"tensor_bytes"`
	DTypes        []ModelDTypeStat `json:"dtypes"`
	// Metadata is the file's own metadata: the safetensors `__metadata__`
	// map, or the scalar entries sitting next to the weights in a PyTorch
	// checkpoint (epoch, global_step, ...).
	Metadata map[string]string `json:"metadata"`
	// HeaderBytes is the size of the parsed header (the safetensors JSON
	// header or the pickled `data.pkl`).
	HeaderBytes int64         `json:"header_bytes"`
	Tensors     []ModelTensor `json:"tensors"`
	// Truncated reports that Tensors lists only the first few thousand
	// entries; the totals above still cover every tensor.
	Truncated bool `json:"truncated"`
	// Warnings carries recoverable problems, e.g. a structure the reader
	// only understood in part.
	Warnings []string `json:"warnings"`
}

// ModelMetaResponse flattens an inspection into the file's own identity, so
// the UI gets `path` and `size` alongside the header fields.
type ModelMetaResponse struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	ModelInfo `tstype:",extends"`
}
