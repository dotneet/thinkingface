// repo: repository detail (overview, file browser, commit history, editing, viewer).
export const repo = {
  overview: {
    backendHint:
      "バックエンド API に接続できない可能性があります。API_URL / NEXT_PUBLIC_API_URL を確認して再試行してください。",
    browseFilesOne: "{count} 件のファイルを見る",
    browseFilesOther: "{count} 件のファイルを見る",
  },
  // [S15]: shown instead of a bare 404 when a logged-out visitor can't
  // open a repository, so there's a way back for someone arriving via an
  // old link.
  notFoundOrNoAccess: {
    title: "見つかりません",
    message: "このリポジトリは存在しません。古いリンクをたどった場合はログインしてください。",
    login: "ログイン",
  },
  tabs: {
    card: "カード",
    files: "ファイル",
    viewer: "ビューア",
    experiments: "実験",
    settings: "設定",
  },
  breadcrumb: {
    datasets: "データセット",
    models: "モデル",
  },
  indexing: {
    message:
      "このリポジトリは直近の push のインデックス処理中です。ファイル数・Parquet ビューア・実験チャートは一時的に不完全な場合があります。",
  },
  archived: {
    badge: "アーカイブ済み",
    bannerTitle: "このリポジトリはアーカイブされています",
    bannerBody:
      "読み取り専用です。push・コミット・ブラウザからの編集・移管は拒否されますが、閲覧とダウンロードはこれまでどおり行えます。オーナーはいつでもアーカイブを解除できます。",
  },
  sidebar: {
    organization: "組織",
    userNamespace: "ユーザー",
    downloads: "ダウンロード",
    downloads30d: "ダウンロード（30日）",
    size: "サイズ",
    files: "ファイル数",
    license: "ライセンス",
    updated: "更新日",
    tags: "タグ",
    branches: "ブランチ",
    gcsAccess: {
      label: "GCS アクセス",
      emptyTitle: "インデックス済みファイルがありません",
      emptyDescription: "このリビジョンにはまだインデックス済みのファイルがありません。",
      summaryOne: "{count} 件のファイル、{size}",
      summaryOther: "{count} 件のファイル、{size}",
      scriptLabel: "gcloud storage スクリプト",
      copyScript: "スクリプトをコピー",
      destHint:
        "DEST をローカルパスの代わりに gs:// プレフィックスにすると、ダウンロードではなくバケット間コピーになります。",
      duckdbLabel: "DuckDB",
      copyDuckdb: "クエリをコピー",
    },
  },
  readme: {
    emptyTitle: "README がありません",
    emptyDescription: "このリポジトリにはまだ README.md がありません。",
    toc: "目次",
    tooLargeTitle: "README が大きすぎて表示できません",
    tooLargeDescription: "README.md が {limit} を超えているため、ここには表示されません。",
    tooLargeOpenFile: "README.md を開く",
  },
  lineage: {
    title: "リネージ",
    unavailable: "リネージを取得できません",
    // {code} is replaced with a `lineage:` code snippet.
    empty:
      "リネージが宣言されていません。このリポジトリの README フロントマターに {code} ブロックを追加すると、由来となったデータセット・ベースモデル・トレーニング run を記録できます。",
    trainedOn: "学習データ",
    baseModel: "ベースモデル",
    evaluatedOn: "評価データ",
    trainingRun: "トレーニング run",
    // Reverse direction: a run that pointed to this repository via
    // log_model(). Stored on the run side, not the card.
    producedByRun: "この run から生成",
    // {rev} is the revision (abbreviated) the run recorded.
    producedRevision: "リビジョン {rev}",
    derivedFromThis: "これから派生",
    evaluatedBy: "これで評価",
    supersededVersions: "これが後継",
    notFound: "このサーバーには見つかりません",
    fromRun: "run {run} から",
    newVersionTitle: "新しいバージョンがあります",
    newVersionBody: "今後は {link} を使ってください。",
    newVersionChain: "{count} 段の後継リンクを辿った結果です。",
    newVersionTruncated:
      "後継リンクが終端しません（循環しているか、辿れる上限を超えています）。直接指定された後継だけを表示しています。",
    newVersionDangling:
      "このリポジトリは {ref} を後継として宣言していますが、このサーバーには存在しません。",
    relationFinetune: "ファインチューン",
    relationAdapter: "アダプタ",
    relationQuantized: "量子化",
    relationMerge: "マージ",
    relationOther: "その他",
    showAllDerived: "{count} 件すべて表示",
    showFewerDerived: "折りたたむ",
  },
  tree: {
    errorHint:
      "バックエンド API に接続できないか、このリビジョンにこのパスが存在しない可能性があります。",
    emptyDir: "このディレクトリは空です",
    unknownRevTitle: "リビジョンが見つかりません",
    unknownRev: "このリポジトリに {rev} という名前のブランチ・タグ・コミットはありません。",
    unknownRevHint: "URL のリビジョン名を確認するか、既定ブランチ（{branch}）を開いてください。",
    unknownRevAction: "既定ブランチを開く",
  },
  treeTable: {
    name: "名前",
    lastCommit: "最終コミット",
    size: "サイズ",
    updated: "更新",
    openInViewer: "ビューアで開く",
    actionsSr: "操作",
    upDir: "上の階層へ",
  },
  fileNav: {
    copyPath: "パスをコピー",
  },
  refSwitcher: {
    viewingCommit: "表示中のコミット",
    branches: "ブランチ",
    noBranches: "ブランチがありません",
    tags: "タグ",
    filterLabel: "ブランチ・タグを絞り込み",
    noMatches: "該当するブランチ・タグがありません",
  },
  commitBar: {
    history: "履歴",
  },
  commits: {
    // {path} is replaced with the file path (shown in monospace).
    historyFor: "{path} の履歴",
    clearFilter: "フィルタを解除",
    errorHint: "バックエンド API に接続できないか、このリビジョンが存在しない可能性があります。",
    emptyPage: "履歴のこのページには、このパスに触れたコミットはありません。",
    older: "さらに古いコミット",
    emptyForPath: "このパスへのコミットはありません",
    empty: "まだコミットがありません",
    browseFiles: "ファイルを見る",
  },
  blob: {
    fileNotFound: "ツリー一覧にファイルが見つかりません。",
    edit: "編集",
  },
  preview: {
    emptyFile: "このファイルは空です",
    downloadOriginal: "元のファイルをダウンロード",
    download: "ダウンロード",
    parquetTitle: "Parquet ファイル",
    parquetDescription: "{size} — テーブルビューアで開くと行とスキーマを閲覧できます。",
    openInViewer: "ビューアで開く",
    loadErrorTitle: "ファイルを読み込めませんでした",
    loadErrorHint:
      "バックエンド API に接続できない可能性があります。ページを再読み込みするか、ファイルをダウンロードしてください。",
    noPreviewTitle: "プレビューはありません",
    noPreviewDescription: "{size} — このファイル形式はインラインでプレビューできません。",
    decodeErrorTitle: "ファイルをデコードできませんでした",
    decodeErrorMessage: "サーバーが返したプレビューをテキストとして読み取れません。",
    // {link} is replaced with a "download the full file" link.
    truncatedNotice: "このプレビューは 512KB で切り詰められています。{link}。",
    truncatedNoticeLink: "ファイル全体をダウンロード",
    gcsCommandLabel: "GCS アクセス",
    gcsCopyCommand: "gcloud コマンドをコピー",
  },
  tabular: {
    modeTable: "テーブル",
    modeRaw: "Raw",
    previewMode: "プレビュー表示",
    stats: "{rows} 行 · {columns} 列 · {size}",
    rawFallbackTitle: "テキストとして表示しています",
    rawFallbackBody: "このファイルはテーブルとして読み取れませんでした: {message}",
    parseNoRows: "表示できる行がありません。",
    parseTooManyColumns: "列数が多すぎて表示できません（{columns}）。",
    parseRaggedRows: "行がヘッダー行と対応していません。",
    parseNoJsonObjects: "JSON オブジェクトが見つかりません（1 行に 1 件の形式を想定しています）。",
    parseTooManyInvalidLines: "有効な JSON オブジェクトでない行が多すぎます。",
    fetchFailedTitle: "切り詰められたプレビューを表示しています",
    fetchFailedBody:
      "ファイル全体をダウンロードできなかったため（{error}）、最初の 512KB のみを解析しています。",
    networkError: "ネットワークエラー",
    // {link} is replaced with a "download the full file" link.
    rowLimit: "最初の {rows} 行で打ち切りました。残りを見るには{link}してください。",
    rowLimitLink: "ファイル全体をダウンロード",
    malformedJsonlOne: "{count} 行が有効な JSON オブジェクトではなくスキップされました。",
    malformedJsonlOther: "{count} 行が有効な JSON オブジェクトではなくスキップされました。",
    malformedCsvOne: "{count} 行がヘッダーと一致せず、補完または切り詰められました。",
    malformedCsvOther: "{count} 行がヘッダーと一致せず、補完または切り詰められました。",
    switchToRaw: "ファイルをそのまま見るには Raw に切り替えてください。",
    emptyTitle: "行がありません",
    emptyDescription: "このファイルにはヘッダーのみがあり、データ行がありません。",
  },
  edit: {
    cantEditTitle: "このファイルは編集できません",
    noPermission: "このリポジトリを編集する権限がありません。",
    noPermissionHint:
      "書き込み権限のあるアカウントでログインするか、リポジトリのオーナーにアクセス権を依頼してください。",
    badType: "このファイル形式は Web UI から編集できません。",
    badTypeHint:
      "ここで編集できるのはプレーンテキストと Markdown ファイル（LFS に保存されていないもの）だけです。",
    notBranch: "「{rev}」はブランチではありません。",
    notBranchHint:
      "編集するにはブランチ（例: デフォルトブランチ）でこのファイルを開いてください — コミットとタグは読み取り専用です。",
    tooLarge: "このファイルは 512KB を超えています。",
    tooLargeHint:
      "512KB を超えるファイルは Web エディタに読み込めません。ローカルで編集して push してください。",
    notText: "このファイルはプレーンテキストではないため Web UI から編集できません。",
  },
  editor: {
    conflict:
      "編集中にこのファイルが変更されました。ページを再読み込みして変更を適用し直してください — 現在の編集内容はそのまま残っています。",
    editAria: "{file} を編集",
    commitMessageLabel: "コミットメッセージ",
    // Not translated, to match the server's default commit message (English).
    commitMessagePlaceholder: "Update {file}",
    descriptionLabel: "説明（任意）",
    descriptionPlaceholder: "追加の説明を入力…",
    committing: "コミット中…",
    commit: "変更をコミット",
    cancel: "キャンセル",
    discardTitle: "未保存の変更を破棄しますか？",
    discardBody: "{file} への編集はまだコミットされていません。このページを離れると失われます。",
    discardConfirm: "変更を破棄",
    keepEditing: "編集を続ける",
  },
  viewer: {
    errorHint:
      "このパスがこのリビジョンに存在する Parquet ファイルを指しているか確認してください。",
  },
  settings: {
    noPermissionTitle: "アクセス権がありません",
    noPermission: "このリポジトリの設定を変更するにはオーナーまたは組織 admin の権限が必要です。",
    dangerZone: "危険な操作",
    transfer: {
      title: "リポジトリの移管",
      description:
        "このリポジトリを別のユーザーまたは組織へ移すか、同じ名前空間内でリネームします。git 履歴・LFS オブジェクト・ダウンロード数は変わりません。",
      destinationLabel: "移管先の名前空間",
      destinationModeLabel: "移管先の種類",
      destinationModeMine: "自分の名前空間から選ぶ",
      destinationModeOther: "他のユーザー・組織を入力",
      otherNamespacePlaceholder: "例: some-org",
      noOwnNamespaces: "まだどの名前空間にも所属していません。",
      newNameLabel: "新しい名前（任意）",
      newNameHint: "空欄のままにすると現在の名前を維持します。",
      blockedByArchive: "移管するには先にアーカイブを解除してください。",
      submit: "移管を開始",
      confirmTitle: "移管の確認",
      confirmBody: "{from} を {to} へ移します。元に戻すには再度移管し直す必要があります。",
      confirmInputLabel: "確認のため「{value}」と入力してください",
      confirmCancel: "キャンセル",
      confirmSubmit: "リポジトリを移管",
      confirming: "移管中…",
      pendingTitle: "移管の承認待ち",
      pendingDestination: "{destination} がこの移管を承認するのを待っています。",
      pendingExpires: "期限: {date}",
      cancel: "移管を取り消す",
      cancelling: "取り消し中…",
      loginRequiredTitle: "ログインが必要です",
      loginRequiredMessage: "このリポジトリを移管するにはログインが必要です。",
      errors: {
        namespaceRequired: "移管先の名前空間を選択または入力してください。",
        nameInvalid: "その名前は使用できません。",
        nameGitSuffix: "リポジトリ名の末尾に「.git」は使用できません。",
      },
    },
    defaultBranch: {
      title: "デフォルトブランチ",
      description:
        "clone で checkout されるブランチで、ファイル一覧・README・リネージもここから読み込まれます。",
      label: "ブランチ",
      save: "保存",
      saving: "保存中…",
      saved: "デフォルトブランチを更新しました。",
      blockedByArchive: "デフォルトブランチを変更するには先にアーカイブを解除してください。",
      noBranches: "まだコミットがないため、切り替え先のブランチがありません。",
    },
    archive: {
      title: "リポジトリのアーカイブ",
      description:
        "リポジトリを読み取り専用にします。git 履歴・LFS オブジェクト・ダウンロード数はそのままで、閲覧・clone も引き続き可能ですが、アーカイブを解除するまで push・コミット・ブラウザからの編集・移管は拒否されます。",
      descriptionArchived:
        "このリポジトリはアーカイブ済み（読み取り専用）です。push・コミット・編集を再び許可するにはアーカイブを解除してください。",
      archive: "アーカイブする",
      unarchive: "アーカイブを解除",
      working: "処理中…",
      confirmTitle: "このリポジトリをアーカイブしますか？",
      confirmBody:
        "アーカイブを解除するまで push・コミット・ブラウザからの編集・移管は拒否されます。閲覧・clone・ダウンロードには影響しません。",
      confirmCancel: "キャンセル",
      confirmSubmit: "アーカイブする",
    },
    delete: {
      title: "リポジトリの削除",
      description:
        "リポジトリ本体と git 履歴を完全に削除します（このリポジトリだけが参照していたファイルは次回のストレージ GC で回収されます）。元に戻せません。まずアーカイブを検討してください。",
      button: "リポジトリを削除",
      confirmTitle: "リポジトリの削除",
      confirmWarningTitle: "この操作は取り消せません",
      confirmWarning: "{repo} と、その git 履歴・インデックス済みファイルを完全に削除します。",
      confirmCancel: "キャンセル",
      confirmSubmit: "このリポジトリを削除",
      deleting: "削除中…",
    },
  },
};
