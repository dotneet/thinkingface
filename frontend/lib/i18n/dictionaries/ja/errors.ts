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
  unauthorized: "この操作にはログインが必要です。",
  forbidden: "この操作を行う権限がありません。",
  notFound: "見つかりませんでした。",
  conflict: "すでに存在します。",
  payloadTooLarge: "リクエストが大きすぎます。",
  internalError: "サーバー側で問題が発生しました。しばらくしてから再度お試しください。",
  repositoryArchived:
    "このリポジトリはアーカイブされ読み取り専用です。変更するにはリポジトリ設定でアーカイブを解除してください。",
  repoMoved: "このリポジトリは移動しました。",
  transferNotPending: "この移管はすでに承認待ちではありません。",
  methodNotAllowed: "この操作はサポートされていません。",
  xetNotSupported: "この操作は Xet 管理下のファイルではサポートされていません。",
  rateLimited: "リクエストが多すぎます。しばらくしてから再度お試しください。",
  overloaded: "サーバーが混み合っています。少し時間をおいて再度お試しください。",
};
