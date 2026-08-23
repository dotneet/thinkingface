// namespace: the /{ns} profile page shared by users and organizations (docs/namespace-design.md §8.3).
export const namespace = {
  kind: {
    user: "ユーザー",
    org: "組織",
  },
  tabs: {
    models: "モデル",
    datasets: "データセット",
    experiments: "実験",
    members: "メンバー",
  },
  counts: {
    modelsOne: "{count} 件のモデル",
    modelsOther: "{count} 件のモデル",
    datasetsOne: "{count} 件のデータセット",
    datasetsOther: "{count} 件のデータセット",
    membersOne: "{count} 人のメンバー",
    membersOther: "{count} 人のメンバー",
  },
  joinedOn: "{date} に登録",
  editProfile: "プロフィールを編集",
  settings: "設定",
  yourProfile: "自分のプロフィール",
  empty: {
    models: "モデルはまだありません",
    datasets: "データセットはまだありません",
    experiments: "実験リポジトリはまだありません",
    ownModels: "まだモデルを公開していません",
    ownDatasets: "まだデータセットを公開していません",
    ownExperiments: "まだ実験が記録されていません",
    createFirstDescription:
      "ここに作るリポジトリは {ns}/<repo> という名前になり、git でクローンできます。",
    createFirst: "最初のリポジトリを作る",
  },
  errorTitle: "読み込めませんでした",
  backendHint:
    "バックエンド API が起動していない可能性があります。API_URL / NEXT_PUBLIC_API_URL を確認してください。",
};
