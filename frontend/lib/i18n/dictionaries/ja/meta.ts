// meta の日本語訳。形は en/meta.ts が決める。
import type { Dict } from "@/lib/i18n/dictionaries/en";

export const meta: Dict["meta"] = {
  description: "データセット・モデル・実験トラッキング。",

  models: "モデル",
  datasets: "データセット",
  organizations: "組織",
  experiments: "実験",

  files: "ファイル",
  commits: "コミット",
  edit: "編集",
  viewer: "ビューア",
  settings: "設定",

  profile: "プロフィール",
  account: "アカウント",
  tokens: "アクセストークン",
  sshKeys: "SSH キー",
  storage: "ストレージ使用量",
  webhooks: "Webhook",
  transfers: "リポジトリの移管",
  language: "表示言語",
  members: "メンバー",
  auditLog: "監査ログ",
  dangerZone: "危険な操作",
  adminUsers: "ユーザー",
  adminSyncJobs: "同期ジョブ",
  adminQuotas: "ストレージ上限",

  newRepository: "新しいリポジトリ",
  newOrganization: "新しい組織",
  signIn: "ログイン",
};
