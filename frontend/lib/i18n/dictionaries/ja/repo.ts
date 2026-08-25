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
    // 「ダウンロード」ではなく「ファイルダウンロード」。カウント対象は resolve
    // エンドポイントだけなので、リポジトリ全体の指標のように見せない。
    downloads: "ファイルダウンロード",
    downloads30d: "ファイルダウンロード（30日）",
    downloadsHint:
      "このサーバーが配信した単一ファイルのダウンロード数です（resolve URL・hf_hub_download・snapshot_download）。git clone・git lfs pull・バケットからの直接取得は数えていません。",
    size: "サイズ",
    files: "ファイル数",
    license: "ライセンス",
    updated: "更新日",
    // カードのトピックタグ。git のタグとは別物。
    tags: "トピック",
    branches: "ブランチ",
    gitTags: "git タグ",
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
  // clone URL のブロック。SSH 鍵を登録できるのに、その鍵で使う URL が
  // どこにも出ていなかった（ポートは環境依存で推測できない）。
  clone: {
    title: "クローン",
    protocolLabel: "クローンのプロトコル",
    http: "HTTP",
    ssh: "SSH",
    sshHint: "SSH の認証は公開鍵のみです。設定 → SSH 鍵 で公開鍵を登録してください。",
    sshLfsHint:
      "Git LFS の転送は常に HTTP 経由です。LFS ファイルを含むリポジトリは HTTP でクローンしてください。",
  },
  // 「このモデル / データセットを使う」: huggingface_hub・datasets・transformers を
  // このサーバーに向けるスニペット。
  usage: {
    labelModel: "このモデルを使う",
    labelDataset: "このデータセットを使う",
    intro:
      "huggingface_hub・datasets・transformers はコードを変えずにそのまま動きます。HF_ENDPOINT でこのサーバーを指すだけです。",
    envLabel: "環境変数",
    envHint:
      "Python の起動前に export してください。huggingface_hub はエンドポイントを import 時に一度だけ読み込みます。",
    copyEnv: "環境変数をコピー",
    tokenHint: "トークン認証を使う場合は HF_TOKEN=… も併せて設定してください。",
    datasetsLabel: "datasets",
    downloadLabel: "huggingface_hub",
    transformersLabel: "transformers",
    transformersHint:
      "AutoModel / AutoTokenizer は、このモデルに合ったタスク別クラスに置き換えてください。",
    copySnippet: "スニペットをコピー",
    revisionHint: "revision=… でブランチ・タグ・コミットを固定できます。ここでは {rev} です。",
  },
  // Web UI からのブランチ / タグの作成・削除。HF 互換 API には最初から
  // 4 本そろっていて、UI だけが無かった。
  refs: {
    newBranch: "新しいブランチ",
    newBranchTitle: "ブランチを作成",
    newBranchBody: "新しいブランチは {rev} を起点に作成されます。",
    branchNameLabel: "ブランチ名",
    branchNamePlaceholder: "feature/my-change",
    createBranch: "ブランチを作成",
    creating: "作成中…",
    cancel: "キャンセル",
    manageTitle: "ブランチとタグ",
    manageDescription:
      "タグはリビジョンに名前を付け、その名前でダウンロードできるようにします。ref を削除しても消えるのは名前だけで、コミットは残ります。",
    branchesTitle: "ブランチ",
    tagsTitle: "タグ",
    noBranches: "このリポジトリにはまだブランチがありません。",
    noTags: "このリポジトリにはまだタグがありません。",
    defaultBadge: "デフォルト",
    defaultUndeletable: "デフォルトブランチは削除できません。",
    newTagTitle: "タグを作成",
    tagNameLabel: "タグ名",
    tagNamePlaceholder: "v1.0",
    tagRevLabel: "タグを付けるリビジョン",
    tagMessageLabel: "メッセージ（任意）",
    tagMessageHint: "メッセージを付けると、git tag -m と同じく注釈付きタグになります。",
    createTag: "タグを作成",
    deleteBranchAction: "ブランチ {name} を削除",
    deleteTagAction: "タグ {name} を削除",
    deleteBranchTitle: "このブランチを削除しますか？",
    deleteBranchBody:
      "ブランチ {name} を {repo} から削除します。コミット自体は、どこからも参照されなくなったものを git が GC するまで残ります。",
    deleteTagTitle: "このタグを削除しますか？",
    deleteTagBody:
      "タグ {name} を {repo} から削除します。この名前で固定している参照は解決できなくなります。",
    confirmDelete: "削除",
    deleting: "削除中…",
    blockedByArchive:
      "ブランチやタグを変更する前に、このリポジトリのアーカイブを解除してください。",
    noPermission: "ブランチとタグを変更するには、このリポジトリへの書き込み権限が必要です。",
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
    // 各 ref が指すコミット。API は最初から返していた（RefUI.target_oid）。
    targetTitle: "コミット {oid} を指しています",
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
    viewDiff: "差分を見る",
  },
  // コミット差分。additions / deletions は has_patch が true のときしか
  // 意味を持たないため、パッチが無い 3 つの理由（バイナリ / LFS / サイズ超過）
  // をそれぞれ言い分ける。
  diff: {
    metaTitle: "コミット",
    backToHistory: "コミット履歴に戻る",
    browseFiles: "このコミット時点のファイルを見る",
    copySha: "コミット SHA 全体をコピー",
    // {oid} は親コミットの短縮 SHA（等幅で表示）。
    parent: "親コミット {oid}",
    rootCommit: "最初のコミットです。親が無いため、すべてのファイルが追加として表示されます。",
    filesChangedOne: "{count} 件のファイルを変更",
    filesChangedOther: "{count} 件のファイルを変更",
    additions: "+{count}",
    additionsTitle: "{count} 行追加",
    deletions: "−{count}",
    deletionsTitle: "{count} 行削除",
    countsPartial:
      "この合計は下に差分を表示できたファイルの分だけです。バイナリ・LFS・スキップされたファイルの行は数えられていません。",
    filesTruncatedTitle: "変更された {total} 件のうち {shown} 件を表示しています",
    filesTruncated:
      "1 回の応答に載る件数を超えるパスが変更されています。省かれたファイルはここには一切表示されません。コミット全体を見るにはリポジトリを clone してください。",
    empty: "このコミットはファイルを変更していません",
    emptyDescription: "第一親との間に差分がありません。",
    revisionNotFound: "コミットが見つかりません",
    revisionNotFoundMessage:
      "このリビジョンはコミットに解決できません。削除されたか、リポジトリにまだコミットが無い可能性があります。",
    errorHint: "バックエンド API に接続できないか、このコミットが存在しない可能性があります。",
    status: {
      added: "追加",
      modified: "変更",
      deleted: "削除",
    },
    // 行数の代わりに表示する。行数そのものが存在しないため。
    noPatch: {
      binary: "バイナリファイルです。テキスト差分は表示できません。",
      lfs: "Git LFS で保存されています。変わったのはポインタの oid で、内容の差分はここには出ません。",
      tooLarge: "差分を取るには大きすぎるためスキップされました。行数も数えられていません。",
      noTextChange:
        "表示する行がありません。前後とも空のファイルであるか、モードだけが変わっています。",
      unsupported:
        "通常のファイルではありません。サブモジュールなどの特別なエントリにテキスト差分はありません。",
      linesNotCounted: "行数は未計測",
    },
    sizeAdded: "{size} を追加",
    sizeDeleted: "{size} を削除",
    // {from} と {to} はコミット前後のバイトサイズ。
    sizeChanged: "{from} → {to}",
    patchEmpty: "このファイルに表示できる行の変更はありません。",
    patchTruncated: "このパッチは途中で打ち切られています。以降の変更は表示されていません。",
  },
  blob: {
    fileNotFound: "ツリー一覧にファイルが見つかりません。",
    edit: "編集",
    download: "ダウンロード",
  },
  // ソースファイルのプレビュー。行番号の欄と、強調表示を諦めた理由。
  codePreview: {
    lineLink: "{line} 行目",
    tooManyLines:
      "このファイルは {lines} 行あり、プレビューで強調表示できる {limit} 行を超えています。行番号なしのプレーンテキストとして表示しています。",
    tooLarge:
      "このファイルはブラウザで強調表示するには大きすぎるため、行番号なしのプレーンテキストとして表示しています。",
  },
  markdownPreview: {
    previewMode: "プレビュー表示",
    modeRendered: "レンダリング",
    modeRaw: "Raw",
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
  // ブラウザからのファイル追加: ツリーの「ファイルを追加」メニュー、新規
  // ファイルのパス入力、アップロードダイアログ。
  upload: {
    menuLabel: "ファイルを追加",
    menuNewFile: "新しいファイルを作成",
    menuUpload: "ファイルをアップロード",
    newFileTitle: "新しいファイルを作成",
    newFileBody:
      "ファイルはリポジトリのルート直下に作成されます。サブディレクトリに置くには / を使います。",
    newFileBodyIn:
      "ファイルは {dir} の下に作成されます。さらにサブディレクトリに置くには / を使います。",
    newFileResolved: "{path} を作成します",
    newFileRelativeSegment:
      'パスに "." や ".." のセグメントは使えません — git が拒否しますし、ここに表示された場所には作成されません。',
    newFileGitDirectory: 'パスに ".git" のセグメントは使えません — git が予約している名前です。',
    newFilePathLabel: "ファイルパス",
    newFilePathPlaceholder: "notes.md",
    newFileConfirm: "ファイルを作成",
    newFileIsLFS:
      "{file} はこのリポジトリで Git LFS の管理対象です。そのため中身をブラウザのエディタから書くことはできません。代わりに「ファイルを追加 → ファイルをアップロード」から追加してください — アップロードなら LFS も自動で処理されます。",
    newFileIsLFSAction: "代わりにアップロードする",
    title: "ファイルをアップロード",
    dropLabel: "ここにファイルをドロップ、またはクリックして選択",
    dropHint: "{rev} の {dir} にコミットされます。",
    browseHint: "アップロードするファイルを選択",
    selectedOne: "{count} 件",
    selectedOther: "{count} 件",
    totalSize: "合計 {size}",
    remove: "{file} を取り消す",
    emptyTitle: "ファイルがまだ選択されていません",
    emptyDescription: "ファイルを 1 つ以上選ぶまで、何もアップロードされません。",
    commitMessageLabel: "コミットメッセージ",
    // Not translated, to match the server's default commit message (English).
    commitMessagePlaceholder: "Upload files",
    submit: "アップロード",
    uploading: "アップロード中…",
    progressLabel: "アップロードの進捗",
    progressCount: "{done} / {total} 送信済み",
    tooMany: "一度にアップロードできるのは最大 {count} 件です。",
    lfsNote: "大きいファイルと既知のバイナリ形式は自動的に Git LFS に保存されます。",
  },
  // ファイル画面からの削除。破壊的な操作なので必ず ConfirmDialog を挟む。
  deleteFile: {
    action: "削除",
    title: "このファイルを削除しますか？",
    body: "{file} は新しいコミットで {rev} から削除されます。過去のコミットには残ります。",
    lfsNote: "LFS オブジェクトの実体は、どこからも参照されなくなり GC が回収するまで保持されます。",
    confirm: "ファイルを削除",
    deleting: "削除中…",
    cancel: "キャンセル",
  },
  renameFile: {
    action: "リネーム",
    title: "ファイルのリネーム・移動",
    body: "{file} を {rev} 上の 1 コミットで新しいパスへ移動します。それ以前のコミットには元の場所のまま残ります。",
    pathLabel: "新しいパス",
    pathHint:
      "リポジトリルートからのフルパスです。末尾だけを変えるとリネーム、それ以外を変えると別ディレクトリへの移動になります。",
    confirm: "リネームする",
    renaming: "リネーム中…",
    cancel: "キャンセル",
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
    rename: {
      title: "リポジトリのリネーム",
      description:
        "所有者を変えずにリポジトリ名だけを変更します。移管と同じくリダイレクトが残るため、以前の名前の URL も引き続き解決されます。",
      label: "リポジトリ名",
      hint: "英数字・ドット・ハイフン・アンダースコアが使えます（先頭は英数字）。",
      save: "リネームする",
      saving: "リネーム中…",
      blockedByArchive: "リネームするには先にアーカイブを解除してください。",
      errors: {
        nameInvalid: "その名前は使用できません。",
        nameGitSuffix: "リポジトリ名の末尾に「.git」は使用できません。",
      },
    },
    description: {
      title: "説明",
      description: "一覧やリポジトリ画面の先頭に表示される 1 行の説明です。",
      label: "説明",
      placeholder: "このリポジトリの内容",
      cardNote:
        "README のカードに description があるとそちらが優先され、push のたびにこの内容を上書きします。この欄はカードに description が無い場合の説明になります。",
      save: "保存",
      saving: "保存中…",
      saved: "説明を更新しました。",
      blockedByArchive: "説明を変更するには先にアーカイブを解除してください。",
    },
    defaultBranch: {
      hint: "すでにデフォルトになっているブランチを保存すると、そのブランチのインデックスを再作成します。",
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
