# Webhook

Webhook は、このインスタンス上で何かが起きたことを、あなたが管理するエンドポイントに通知する仕組み
です。プッシュが届いた、リポジトリが作成された／アーカイブされた、転送が承認待ちになった、実験の run
が終了した — サーバーはこうしたイベントを JSON ボディの HTTP `POST` として送り、検証用の署名を付け、
失敗すればリトライし、あとから確認・再送できる配信履歴を残します。

## 設定する場所 { #where-to-configure-them }

| ネームスペース | 画面 |
|---|---|
| 自分自身 | `/settings/webhooks`（<http://localhost:3000/settings/webhooks>） |
| Organization | `/orgs/{org}/settings/webhooks` |

どちらも同じマネージャーです。個人側では自分が管理者であるネームスペースを選べ、Organization 側は
その Organization に固定されています。

**Webhook は管理者専用です。** Organization では `write` ではなく `admin` ロールが必要です — webhook
はネームスペースのシークレットを外部 URL へ持ち出すものなので、コンテンツの変更ではなく管理操作として
扱われます。`write` メンバーにはネームスペースの選択肢が表示されず、そのネームスペースの webhook を
一覧・作成しようとすると（`GET`/`POST /api/v1/namespaces/{ns}/webhooks`）API から 403 を受け取ります。一方
`/api/v1/webhooks/{id}` 配下のルート(get、update、delete、deliveries、redeliver)は、管理権限のない
webhook に対して、存在しない場合とまったく同じ 404 を返します — ここで 403 を返すと、連番の小さい id
を総当たりするだけで、どの webhook を誰が所有しているかを読み取れてしまうためです。自分のネームスペース
では自分自身が管理者なので、何も変わりません。

各 webhook には**スコープ**があります。空のままにすればそのネームスペースの全リポジトリで発火し、
リポジトリを 1 つ選べばそのリポジトリだけで発火します。作成時（またはあとから）に無効化しておくことも
でき、その場合は削除せずに停止した状態になります。

## イベント { #events }

イベントは 10 種類あります。10 種すべてが UI に表示され、API でも受け付けられます。それ以外の値は、黙って
保存されるのではなく「不明なイベント」として拒否されます。

| イベント | 発火するタイミング |
|---|---|
| `repo.push` | ある ref のプッシュ後処理（blob の公開、ファイル／parquet インデックスの更新）が完了したとき。API でのブランチ作成も同じ処理をスケジュールするため、こちらも `repo.push` を発火します — ブランチ作成専用のイベントはありません。**タグ**の作成では発火しません |
| `repo.created` | リポジトリが作成されたとき |
| `repo.deleted` | リポジトリが削除されたとき。受け取れるのはネームスペース全体の購読だけです（リポジトリ単位の webhook はリポジトリと一緒に削除されるため） |
| `repo.moved` | 転送またはリネームが完了したとき。**移動先**のネームスペースの購読に配信されます |
| `repo.transfer_requested` | 転送が移動先の承認待ちになったとき。**移動先**のネームスペースに配信されます |
| `repo.archived` | リポジトリが読み取り専用に凍結されたとき |
| `repo.unarchived` | アーカイブされたリポジトリが解除されたとき |
| `repo.ref_deleted` | ブランチまたはタグが削除されたとき。ブランチは `git push --delete` と API のどちらでも発火しますが、**タグ**は API 経由(`DELETE .../tag/{tag}`)でのみ発火します — タグの作成が push からは見えないのと同じ理由で、タグに対する `git push --delete` はサーバーから見えず、いかなる webhook も発火しません |
| `run.finished` | 実験の run が `finished` に遷移したとき |
| `run.failed` | 実験の run が `failed` に遷移したとき |

`run.finished` / `run.failed` はそのステータスへの**遷移**時にのみ発火します。そのため、終了後もログを
送り続ける run や、finish 呼び出しのリトライによって、毎回新しい配信が飛ぶことはありません。

!!! warning "run 系イベントにはライブ ingest API が必要です"

    `run.finished` / `run.failed` が発火するのは、ingest API 経由で報告された run
    ——run の最後に `finish()` を呼ぶ `thinkingface.trackio`—— だけです。もう一方の経路、
    つまり trackio が自分で書いた Parquet をデータセットリポジトリに push する形で届いた run は、
    ステータス付きでインデックスされますが webhook は発火しません。その経路を使っている場合は、
    実験リポジトリの `repo.push` を購読してください。詳しくは
    [実験のトラッキング](experiments.md) を参照してください。

## 配信の中身 { #what-a-delivery-looks-like }

リクエストは JSON ボディを持つ `POST` です。

```http
POST /your-endpoint HTTP/1.1
Content-Type: application/json
User-Agent: thinkingface-webhooks/1.0
X-Thinkingface-Event: repo.push
X-Thinkingface-Delivery: 4127
X-Thinkingface-Signature: sha256=9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
```

### ペイロード { #payloads }

各イベントのボディはフラットな JSON オブジェクトです。`kind` は `"dataset"` か `"model"`、
`full_name` は `"{namespace}/{name}"` です。

`repo.push`:

```json
{
  "namespace": "acme",
  "repo": "sentiment-base",
  "full_name": "acme/sentiment-base",
  "kind": "model",
  "ref": "main",
  "old_sha": "0a1b2c3…",
  "new_sha": "4d5e6f7…",
  "changed_files": 3
}
```

`repo.created` と `repo.deleted`:

```json
{ "namespace": "acme", "name": "sentiment-base", "kind": "model", "full_name": "acme/sentiment-base" }
```

`repo.archived` と `repo.unarchived` — 同じフィールドに加えて、結果の状態が入ります。イベント名から
推測しなくて済むようにするためです。

```json
{
  "namespace": "acme", "name": "sentiment-base", "kind": "model",
  "full_name": "acme/sentiment-base", "archived": true
}
```

`repo.ref_deleted`:

```json
{
  "namespace": "acme",
  "repo": "sentiment-base",
  "full_name": "acme/sentiment-base",
  "kind": "model",
  "ref": "old-experiment",
  "ref_type": "branch",
  "old_sha": "0a1b2c3…",
  "new_sha": ""
}
```

`ref_type` は `"branch"` か `"tag"` です。短い ref 名だけではどちらか区別できないためです。`new_sha` は常に空文字列です — ref が消えた時点で新しい値というもの自体が存在しません。

`repo.moved`:

```json
{
  "kind": "model",
  "from": { "namespace": "admin", "name": "sentiment-base" },
  "to": { "namespace": "acme", "name": "sentiment-base" },
  "full_name": "acme/sentiment-base"
}
```

`repo.transfer_requested`:

```json
{
  "transfer_id": 12,
  "kind": "model",
  "from": { "namespace": "admin", "name": "sentiment-base" },
  "to": { "namespace": "acme", "name": "sentiment-base" },
  "requested_by": "admin",
  "expires_at": "2026-01-31T09:00:00Z"
}
```

`run.finished` と `run.failed`:

```json
{
  "namespace": "acme",
  "repo": "training-metrics",
  "full_name": "acme/training-metrics",
  "project": "sentiment",
  "run": "run-2026-01-30-a",
  "status": "finished"
}
```

## 署名の検証 { #verifying-the-signature }

Webhook を作成すると、サーバーが `whsec_` で始まるシークレットを生成し、**1 度だけ**表示します。その場
でコピーしてください — 以降どのレスポンスも返しません。紛失した場合は webhook を編集してシークレットを
ローテートします。新しいシークレットが 1 度だけ表示され、古いものは無効になります。

`X-Thinkingface-Signature` は `sha256=` に続けて、シークレットを鍵とした**リクエストボディ生バイト**の
HMAC-SHA256 を小文字 16 進で表したものです。

```text
X-Thinkingface-Signature: sha256=<hex(HMAC_SHA256(secret, raw_body))>
```

正しく実装するうえで重要な点が 2 つあります。

- **生のバイト列をハッシュしてください。** JSON をパースして再シリアライズしたあとではいけません。
  再エンコードすると空白やキーの順序が変わり、署名が一致しなくなります。
- **定数時間で比較してください**（Python なら `hmac.compare_digest`、Go なら `hmac.Equal` など）。
  `==` は使わないでください。

シークレットは `whsec_` プレフィックスを含めて、そのまま HMAC の鍵として使われます。表示された文字列を
まるごと渡してください。

Flask を使った最小のレシーバーの例です。

```python
import hashlib
import hmac
import os

from flask import Flask, request

app = Flask(__name__)
SECRET = os.environ["THINKINGFACE_WEBHOOK_SECRET"].encode()  # "whsec_..." 全体


@app.post("/thinkingface")
def receive():
    body = request.get_data()  # request.json ではなく生のバイト列
    expected = "sha256=" + hmac.new(SECRET, body, hashlib.sha256).hexdigest()
    sent = request.headers.get("X-Thinkingface-Signature", "")
    if not hmac.compare_digest(sent, expected):
        return "bad signature", 401

    event = request.headers["X-Thinkingface-Event"]
    delivery = request.headers["X-Thinkingface-Delivery"]
    payload = request.get_json()
    app.logger.info("%s delivery=%s %s", event, delivery, payload)
    return "", 204  # 2xx であれば成功として扱われます
```

## リトライと配信保証 { #retries }

イベントの発火は配信行を書き込むだけで、実際の送信はバックグラウンドのワーカープールが行います。
そのため、サーバー側があなたのエンドポイントを待つことはありません。

- **1 回の試行のタイムアウトは 10 秒です。** 2xx 以外のレスポンス、および通信の失敗は、いずれも失敗した
  試行として数えられます。
- **試行は合計で最大 5 回**（初回 + リトライ 4 回）なので、待ち時間は 4 回分です。30 秒、1 分、2 分、
  4 分と倍々に増えます（上限は 15 分で、この回数では到達しません）。5 回目も失敗すると、その配信は
  `failed` として打ち切られ、自動では再試行されません。
- **無効化された webhook の配信は、失敗せずに保留されます。** webhook が無効な間、保留中の配信は手を
  つけられず（試行回数も消費されず）、再度有効にしたときに送信されます。
- **配信は at-least-once で、順序は保証されません。** 配信の途中でワーカーが停止すると、数分後にその
  予約が解放されて再試行されるため、すでに処理済みのリクエストが重複して届くことがあります。近い時刻に
  発火した 2 つのイベントが、発生順に届く保証もありません。ハンドラは冪等にし、
  `X-Thinkingface-Delivery` で重複排除してください。
- **あなたのレスポンスボディは記録されます。** 先頭 4 KB がステータスコード（エンドポイントに到達でき
  なかった場合は null）とともに配信履歴に保存され、下記のデバッグに使えます。

## 配信履歴と再送信 { #delivery-history }

設定画面で webhook を展開すると、その配信履歴が新しい順に表示されます。イベント、ペイロード、
ステータス（`pending` / `success` / `failed`）、試行回数、最後の試行時刻、そしてエンドポイントが返した
レスポンスです。

**再送信**は、同じイベントとペイロードを**新しい配信として**再度キューに入れます。元の行はそのまま
残るので、履歴には実際に起きたことが残り続けます。再送信には独自の配信 ID が振られます — これもまた、
「ID が 1 つ = 到達が 1 回」と考えず、このヘッダで重複排除すべき理由の 1 つです。

## 許可される URL { #ssrf-guard }

受け付けられるのは `http` と `https` の URL だけで、既定ではローカル／プライベートなアドレスへの配信を
拒否します。対象は `localhost` と `.localhost` 以下すべて、`127.0.0.0/8`、`10/8`、`172.16/12`、
`192.168/16`、`169.254/16` などのリンクローカル範囲（クラウドのインスタンスメタデータへの経路です）、
unspecified アドレス、`::1` です。

さらに、プライベートではないものの webhook の配信先として妥当でない範囲も拒否します。
`100.64.0.0/10`（キャリアグレード NAT。**Tailscale が tailnet に割り当てるのがこの範囲**なので、
tailnet 上の受信側は拒否されます）、`0.0.0.0/8`、`192.0.0.0/24`、`198.18.0.0/15`、`240.0.0.0/4`、
そしてマルチキャストです。IPv6 として書かれた IPv4 アドレス（`::ffff:10.0.0.1` や、プライベート IPv4 を
埋め込んだ NAT64 アドレス）は、素通しではなく中の IPv4 で判定されます。

配信はリダイレクトを追いません。3xx は失敗した試行としてそのままステータスごと配信履歴に記録され、
設定していない・チェックも通っていないホストへ追いかけていくことはありません。

アドレスのチェックは意図的に 2 回行われます。webhook の作成・編集時に 1 回、そして各配信が実際に張る TCP
接続の時点で、本当に解決されたアドレスに対してもう 1 回です。2 回目のチェックが、あとになってから
プライベートアドレスに解決されるホスト名 — DNS リバインディングや、webhook 作成後に運用者の DNS が
変わったケース — を止めます。

受信側が正当に `localhost` にいるローカル開発向けには、運用者が
`TF_WEBHOOKS_ALLOW_PRIVATE_TARGETS=true` を設定することで両方のチェックを無効化できます。本番では
オフのままにしてください。いずれかのネームスペースの管理者であれば誰でも、インスタンス自身の内部
ネットワークに webhook を向けられるようになってしまいます。

## 関連ページ { #see-also }

- [Organization](organizations.md) — ロールと、管理者専用の設定画面について
- [実験のトラッキング](experiments.md) — `run.finished` / `run.failed` の発生元
- [設定](../self-hosting/configuration.md) — `TF_WEBHOOKS_ALLOW_PRIVATE_TARGETS` などのサーバー設定
