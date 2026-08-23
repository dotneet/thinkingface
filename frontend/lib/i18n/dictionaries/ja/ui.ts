// UI primitives with built-in copy. Used from both Server and Client
// Components (e.g. ErrorState's required `title` default), so keys here
// cannot assume a client-only translator is available.
export const ui = {
  skipToContent: "コンテンツへスキップ",
  copy: "コピー",
  copied: "コピーしました",
  close: "閉じる",
  cellValue: "セルの値",
  viewFullValue: "クリックで全体を表示",
  // Table cell rendering (ValueCell / CellModal / JsonTree). The caller
  // picks `*One` / `*Other` (the translator has no pluralization mechanism;
  // the count is passed via params).
  cell: {
    image: "画像",
    imageUnavailable: "画像を表示できません",
    viewImage: "クリックで画像を表示",
    viewMode: "セルの表示モード",
    tree: "ツリー",
    raw: "生データ",
    expand: "展開",
    collapse: "折りたたむ",
    keysOne: "{count} キー",
    keysOther: "{count} キー",
    itemsOne: "{count} 要素",
    itemsOther: "{count} 要素",
    moreItems: "… 残り {count} 件",
    showFullString: "文字列全体を表示",
  },
  errorStateTitle: "読み込めませんでした",
  pagination: {
    range: "{total} 件中 {from}–{to} 件目",
    prev: "前へ",
    next: "次へ",
    outOfRangeTitle: "このページには結果がありません",
    outOfRangeDescription: "一覧の末尾を超えてページを送っています。",
    backToFirstPage: "最初のページに戻る",
  },
  confirmDialog: {
    defaultCancel: "キャンセル",
    defaultConfirm: "確定",
    typeToConfirm: "確認のため {value} と入力してください",
  },
  // Markdown rendering (components/ui/markdown.tsx and its leaf components).
  markdown: {
    headingAnchor: "この見出しへのリンク: {heading}",
    table: "表",
    copyCode: "コードをコピー",
  },
  markdownEditor: {
    modeLabel: "表示モード",
    modeEdit: "編集",
    modePreview: "プレビュー",
    modeSplit: "分割",
    nothingToPreview: "まだプレビューする内容がありません。",
    status: "{lines} 行 · {chars} 文字",
  },
  // [S14]: app/error.tsx, app/global-error.tsx, and the per-repo error.tsx.
  // global-error.tsx has no I18nProvider (it replaces the root layout
  // itself), so it calls createTranslator() directly instead of useT().
  unexpectedError: {
    title: "問題が発生しました",
    description:
      "このページの表示中に予期しないエラーが発生しました。再試行するか、前のページに戻ってください。",
    retry: "再試行",
    goHome: "ホームへ戻る",
    backToRepo: "リポジトリへ戻る",
  },
};
