// errors: translated copy for each backend error.type (apitypes.ApiError.Type).
// lib/api-error-message.ts maps an ApiResult failure's type to an entry
// here, so the backend's assembled English message is never shown directly
// on screen (see [S12]). Keep this in sync with the writeError call sites
// under backend/internal/api (errors.go / redirect.go / transfers.go / git.go).
export const errors = {
  networkError: "サーバーに接続できませんでした。接続を確認してもう一度お試しください。",
  // bad_request messages carry a specific, actionable detail from the
  // backend ("name must not contain spaces"), so it's appended rather than
  // dropped, unlike the other types.
  badRequest: "リクエストが不正です: {detail}",
  // 本文が空のときの文言。badRequest は {detail} を含むテンプレートなので、
  // params なしで描画すると "{detail}" がそのまま画面に出る。ERROR_TYPE_KEYS は
  // bad_request をこちらに、DETAIL_KEYS だけが上の埋め込み文に対応させる。
  badRequestGeneric: "リクエストが不正です。",
  unauthorized: "この操作にはログインが必要です。",
  forbidden: "この操作を行う権限がありません。",
  // forbidden の message は拒否された理由そのもの（"sign-up is disabled on this
  // instance" など）なので、捨てずに埋め込む。どの type で本文を信頼するかは
  // lib/api-error-message.ts の DETAIL_KEYS を参照。
  forbiddenDetail: "許可されていません: {detail}",
  notFound: "見つかりませんでした。",
  conflict: "すでに存在します。",
  // 409 は衝突した対象を名指しして返ってくる（"main is the default branch of
  // acme/bert and cannot be deleted" など）。上の汎用文言では曖昧になるどころか
  // 事実と食い違うため、本文を残す。信頼してよい type の判断根拠は
  // lib/api-error-message.ts の DETAIL_KEYS を参照。
  conflictDetail: "競合が発生しました: {detail}",
  payloadTooLarge: "リクエストが大きすぎます。",
  unsupportedMediaType: "この形式のリクエストはサーバーが読み取れません。",
  insufficientStorage: "この名前空間にはこのアップロードを保存する空き容量がありません。",
  insufficientStorageDetail: "容量が足りません: {detail}",
  internalError: "サーバー側で問題が発生しました。しばらくしてから再度お試しください。",
  repositoryArchived:
    "このリポジトリはアーカイブされ読み取り専用です。変更するにはリポジトリ設定でアーカイブを解除してください。",
  repoMoved: "このリポジトリは移動しました。",
  transferNotPending: "この移管はすでに承認待ちではありません。",
  methodNotAllowed: "この操作はサポートされていません。",
  xetNotSupported: "この操作は Xet 管理下のファイルではサポートされていません。",
  accountDisabled: "このアカウントは無効化されています。サイト管理者に連絡してください。",
  approvalPending:
    "アカウントを作成しました。サイト管理者の承認待ちです。承認されるとサインインできるようになります。",
  accountPending: "このアカウントはサイト管理者の承認待ちです。",
  rateLimited: "リクエストが多すぎます。しばらくしてから再度お試しください。",
  overloaded: "サーバーが混み合っています。少し時間をおいて再度お試しください。",
  // ファイルの範囲指定読み取りが返す 416（backend/internal/api/resolve.go）。
  // 要求した範囲がファイル末尾以降を指しており、UI から見ると読んでいた実体が
  // サーバー上のものと食い違っている状態。
  rangeNotSatisfiable:
    "読み取り中にファイルが変更されました。ページを再読み込みしてもう一度お試しください。",
  // writeError ではなく net/http の TimeoutHandler が返す 504
  // （backend/internal/api/server.go の handlerTimeoutBody）。
  timeout: "サーバーの応答に時間がかかりすぎました。しばらくしてから再度お試しください。",
};
