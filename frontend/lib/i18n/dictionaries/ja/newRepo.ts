// newRepo: the new repository creation page.
export const newRepo = {
  title: "新しいリポジトリ",
  blurb:
    "データセットまたはモデルのリポジトリを作成します。作成後に git でファイルを push できます。",
  loginNotice: {
    prefix: "リポジトリを作成するにはログインが必要です。まず",
    link: "ログイン",
    suffix: "してください。",
  },
  // バックエンドがエラーを返した／応答しない場合は、未ログインとは別の状態
  // （DESIGN.md §9）。上のログイン案内の代わりにこちらを表示する。
  accountLoadFailed: "作成先に指定できる名前空間を確認できませんでした。",
  accountLoadFailedHint:
    "バックエンド API に接続できない可能性があります。ページを再読み込みしてください。",
  kind: {
    dataset: "データセット",
    model: "モデル",
  },
  namespace: "名前空間",
  namespacePlaceholder: "your-username",
  kindUser: "個人",
  kindOrg: "組織",
  name: "名前",
  nameHint:
    "1〜96 文字の英数字・ドット・ハイフン・アンダースコア。先頭は英数字で、末尾を .git にすることはできません。",
  namePlaceholder: "my-dataset",
  description: "説明",
  descriptionPlaceholder: "このリポジトリの用途は何ですか？",
  create: "リポジトリを作成",
  creating: "作成中…",
  errors: {
    namespaceRequired: "名前空間を入力してください。",
    loginRequired:
      "リポジトリを作成するにはログインが必要です。ログインしてから再度お試しください。",
    nameRequired: "リポジトリ名を入力してください。",
    nameInvalid:
      "リポジトリ名は 1〜96 文字の英数字・ドット・ハイフン・アンダースコアで、先頭は英数字にしてください。",
    nameGitSuffix: "リポジトリ名の末尾を .git にすることはできません。",
  },
};
