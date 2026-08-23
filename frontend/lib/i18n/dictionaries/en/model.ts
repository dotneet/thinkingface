// model: the checkpoint model inspector (summary, dtype breakdown, tensor list).
// NOTE: the en dictionary is the source of truth for shape, so it must not be `as const`.
export const model = {
  inspector: {
    loadFailed: "Failed to load model metadata",
    download: "Download",
  },
  summary: {
    format: "Format",
    parameters: "Parameters",
    tensors: "Tensors",
    fileSize: "File size",
  },
  dtypes: {
    title: "Dtypes",
    colDtype: "dtype",
    colTensors: "tensors",
    colParameters: "parameters",
    colSize: "size",
  },
  metadata: {
    title: "Metadata",
  },
  tensors: {
    title: "Tensors",
    filterPlaceholder: "Filter by name...",
    countSummary: "{filtered} of {total} tensors",
    noMatch: 'No tensors match "{filter}".',
    colName: "name",
    colDtype: "dtype",
    colShape: "shape",
    colParameters: "parameters",
    colSize: "size",
  },
  notes: {
    truncated: "Only the first {count} tensors are shown; the checkpoint contains more.",
  },
};
