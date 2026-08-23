// model: the checkpoint model inspector (summary, dtype breakdown, tensor list).
export const model = {
  inspector: {
    loadFailed: "モデルのメタ情報を読み込めませんでした",
    download: "ダウンロード",
  },
  summary: {
    format: "フォーマット",
    parameters: "パラメータ数",
    tensors: "テンソル数",
    fileSize: "ファイルサイズ",
  },
  dtypes: {
    title: "Dtype",
    colDtype: "dtype",
    colTensors: "テンソル数",
    colParameters: "パラメータ数",
    colSize: "サイズ",
  },
  metadata: {
    title: "メタデータ",
  },
  tensors: {
    title: "テンソル",
    filterPlaceholder: "名前で絞り込み...",
    countSummary: "{total} 件中 {filtered} 件のテンソル",
    noMatch: "「{filter}」に一致するテンソルはありません。",
    colName: "名前",
    colDtype: "dtype",
    colShape: "形状",
    colParameters: "パラメータ数",
    colSize: "サイズ",
  },
  notes: {
    truncated:
      "最初の {count} 件のテンソルのみ表示しています。チェックポイントにはさらに多くのテンソルが含まれます。",
  },
};
