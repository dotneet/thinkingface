// settingsDetail の日本語訳。形は en/settingsDetail.ts が決める。
import type { Dict } from "@/lib/i18n/dictionaries/en";

export const settingsDetail: Dict["settingsDetail"] = {
  sshKeys: {
    publicKeyToggle: "公開鍵",
    publicKeyToggleAria: "{title} の公開鍵",
    copyPublicKey: "公開鍵をコピー",
  },
  deliveries: {
    viewResponse: "表示",
    viewResponseAria: "この配信のレスポンスを表示",
    responseTitle: "配信のレスポンス",
    metaEvent: "イベント",
    metaStatus: "ステータス",
    httpStatus: "HTTP {status}",
    bodyLabel: "レスポンスボディ",
    copyResponse: "レスポンスボディをコピー",
    notAttemptedTitle: "まだ配信されていません",
    notAttemptedBody:
      "この配信はまだキューに入ったままで、表示できるレスポンスはありません。今すぐ送るには再配信してください。",
    noResponseTitle: "レスポンスを受け取っていません",
    noResponseBody:
      "リクエストがエンドポイントに届かなかったか、応答が返る前にタイムアウトしたため、何も保存されていません。レスポンス列にステータスコードが出ないのも同じ理由です。",
    emptyBodyTitle: "レスポンスボディは空でした",
    emptyBodyBody: "エンドポイントは HTTP {status} を返しましたが、ボディはありませんでした。",
    truncationHint: "レスポンスは先頭 4 KiB のみが保存されます。",
  },
};
