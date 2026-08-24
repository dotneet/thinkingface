# 設定

これは、インスタンスを設定する人のための、thinkingface の環境変数の完全なリファレンスです。正式な
ソースはリポジトリルートの `.env.example` です — これを `.env` にコピーして調整してください。以下の
値は、変数が未設定のときにサーバーがフォールバックするデフォルト値です。

!!! warning "他の人にインスタンスを公開する前に、これらを変更してください"
    `TF_ADMIN_PASSWORD` と `TF_SESSION_SECRET` は、いずれも公開されているよく知られたデフォルト値
    （それぞれ `admin` と、固定の開発用文字列）を持った状態で出荷されます。どちらも未設定のままに
    しておくのは、自分だけが到達できるラップトップ上であれば問題ありません。`TF_PUBLIC_URL` が
    `https://` の URL になっている場合、サーバーはどちらかがデフォルトのままでは起動を拒否します。
    また、これらが未設定のままの間は、プレーンな HTTP であっても起動のたびに警告をログに出力します。

## サーバー { #server }

| 変数 | 説明 | デフォルト | 備考 |
|---|---|---|---|
| `TF_ADDR` | HTTP API（git smart HTTP、LFS、REST、ビューア）の待ち受けアドレス。 | `:8080` | |
| `TF_PUBLIC_URL` | 外部から到達可能な API のベース URL。CORS のデフォルトオリジンと Cookie のセキュリティ設定を推測するために使われ、生成される LFS / HF 互換 URL にも埋め込まれます。 | `http://localhost:8080` | これを `https://` で始めると、サーバーは「本番」バリデーションに切り替わります。この場合、デフォルトの管理者パスワードやセッションシークレットのままでは起動を拒否します。 |
| `GIT_ROOT` | ディスク上のベア git リポジトリを保持するディレクトリ。 | `/data/git` | Continuity/WAL 移行が `authoritative`（下記の git・キャッシュの表を参照）でない限り、永続ストレージである必要があります。`authoritative` の場合、これは再構築可能なキャッシュに過ぎません。 |

## ストレージ { #storage }

| 変数 | 説明 | デフォルト | 備考 |
|---|---|---|---|
| `STORAGE_DRIVER` | オブジェクトストレージのバックエンド: `gcs`（実際のバケット）または `gcs-emulator`（fake-gcs-server）。 | `gcs-emulator` | これら以外の値を指定すると起動に失敗します。 |
| `GCS_BUCKET` | 対象のバケット名。 | `thinkingface` | |
| `GCS_PREFIX` | バケット内のキーに付けるオプションのプレフィックス。1 つのバケットを複数の環境で共有する場合に使います。 | *(empty)* | 先頭・末尾のスラッシュは取り除かれます。 |
| `STORAGE_EMULATOR_HOST` | fake-gcs-server エミュレータのアドレス。 | *(empty)* | `STORAGE_DRIVER=gcs-emulator` のときは必須で、設定しないと起動に失敗します。`STORAGE_DRIVER=gcs` の場合は使用されないため、未設定のままにしてください。 |
| `TF_SIGNED_URL_TTL` | 署名付き GCS URL（LFS 転送、直接ダウンロード）が有効な期間の下限。実際の有効期間はオブジェクトのサイズから算出され、`[TF_SIGNED_URL_TTL, TF_SIGNED_URL_MAX_TTL]` の範囲にクランプされるため、この値が効くのは主に小さい転送です。 | `1h` | `STORAGE_DRIVER=gcs` のときのみ意味を持ちます — エミュレータは署名付き URL を検証できないため、そのモードでは代わりにサーバーがバイト列をプロキシします。 |
| `TF_SIGNED_URL_MAX_TTL` | 同じクランプの上限。大きな転送に対して効きます。 | `12h` | `TF_SIGNED_URL_TTL` と同じく `STORAGE_DRIVER=gcs` のときのみ意味を持ちます。 |

## データベース { #database }

| 変数 | 説明 | デフォルト | 備考 |
|---|---|---|---|
| `DATABASE_URL` | 接続文字列。スキームによってバックエンドが選ばれます: PostgreSQL は `postgres://` / `postgresql://`、SQLite は `sqlite://`。 | *(none — required)* | 未設定の場合や、それ以外のスキームを使った場合は起動に失敗します。両者のトレードオフについては [デプロイ](deployment.md) を参照してください。`docker compose up` の場合、これは `.env` から直接読み込まれるのではなく、以下の `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` から組み立てられます。 |
| `POSTGRES_USER` | PostgreSQL のロール名。`postgres` コンテナで使われるほか、Compose での `DATABASE_URL` の組み立てにも使われます。 | `tf` | Compose 専用。サーバー自体はこれを読み取りません。 |
| `POSTGRES_PASSWORD` | PostgreSQL のロールパスワード。 | `tf` | Compose 専用。ローカル用途を超える場合は、`TF_ADMIN_PASSWORD` と合わせてこれも変更してください。 |
| `POSTGRES_DB` | PostgreSQL のデータベース名。 | `thinkingface` | Compose 専用。 |
| `POSTGRES_PORT` | PostgreSQL が公開されるホスト側のポート。Compose ネットワークの外部から接続する場合に使います（`make test-store-pg`）。 | `5432` | Compose 専用。 |
| `TF_LITESTREAM_REPLICA_URL` | Litestream が SQLite ファイルをレプリケートする先（および復元元）となる `gs://` の宛先。 | *(unset)* | Cloud Run 上の本番 SQLite デプロイにのみ関係します（[デプロイ](deployment.md) を参照）。ローカルでは未設定のままにしてください — Compose の SQLite モードは通常のボリュームで問題なく動作します。 |

## 認証とセッション { #authentication-and-sessions }

| 変数 | 説明 | デフォルト | 備考 |
|---|---|---|---|
| `TF_ADMIN_USERNAME` | users テーブルが空の場合にのみシードされる、最初のアカウントのユーザー名。 | `admin` | |
| `TF_ADMIN_PASSWORD` | そのシードされたアカウントのパスワード。 | `admin` | **これは変更してください。** よく知られたデフォルト値であり、`TF_PUBLIC_URL` が `https://` の間は未設定のままだと起動が拒否されます。**初回起動時のみ有効です** — 下の注記を参照してください。 |
| `TF_ADMIN_EMAIL` | シードされたアカウントのメールアドレス。 | `admin@example.com` | |
| `TF_SESSION_SECRET` | セッションクッキー（`tf_session`）と LFS 転送用 URL に署名する HMAC-SHA256 キー。 | `dev-insecure-session-secret` | **これは変更してください。** `TF_PUBLIC_URL` が `https://` になると、最低 32 バイト必要になり、その場合はデフォルト値のままにしておくこともできません。 |
| `TF_SESSION_TTL` | 発行されたセッションクッキーが有効な期間。 | `168h`（7 日） | ログアウト時やパスワード変更時にも、それより早く無効化されます。 |
| `TF_COOKIE_SECURE` | セッションクッキーに `Secure` 属性を強制的に付与します。 | *(inferred from `TF_PUBLIC_URL`)* | サーバーの手前（例: ロードバランサ）で TLS が終端し、コンテナ自体へのトラフィックがプレーンな HTTP である場合は、明示的に `true` を設定してください — そうした構成では自動推測が誤った結果になります。 |
| `TF_ALLOWED_ORIGINS` | 資格情報付き CORS を許可する、ブラウザのオリジンのカンマ区切りリスト。 | *(inferred: `TF_PUBLIC_URL`'s origin, plus `http://localhost:3000` / `http://127.0.0.1:3000` when not `https`)* | Web UI が API と異なるホストから配信される場合は、本番環境ではこれを明示的に設定してください — 許可リストの外にあるオリジンには CORS ヘッダーが付与されず、状態変更を伴う Cookie 認証リクエストは 403 で拒否されます。`huggingface_hub`、`git`、`curl` は `Origin` ヘッダーを送らないため、いずれにせよ影響を受けません。 |
| `TF_AUTH_RATE_LIMIT_PER_MIN` | クライアント IP ごとに 1 分間で許容されるパスワード失敗回数（ユーザー名ごとにはその半分）。`0` で無効化します。 | `10` | ログインエンドポイントと（すべてのルートで受け付けられる）HTTP Basic 認証の両方に適用されます。プロセス単位でカウントされるため、複数レプリカがある場合、制限はグローバルではなくレプリカごとに適用されます。 |
| `TF_TRUST_PROXY_IPS` | レート制限のために、クライアント IP を `X-Forwarded-For` の一番左のエントリから読み取ります。 | `false` | 自分が制御するプロキシがそのヘッダーを上書きする場合にのみ有効にしてください。そうでない場合、クライアントが自分自身のレート制限バケットを選べてしまいます。 |
| `TF_ALLOW_SIGNUP` | セルフサービスでのアカウント作成を開放するかどうか。 | `true` | `false` にすると公開の **Sign up** タブが閉じます。一方通行ではありません: サイト管理者は **Settings → Users**（`/settings/admin/users`）から引き続きアカウントを追加できます。この画面は設計上このフラグを見ません。 |
| `TF_ORG_CREATION` | 誰が Organization を作成できるか: `anyone` または `admin`。 | `anyone` | これら以外の値を指定すると起動に失敗します。 |

!!! warning "`TF_ADMIN_PASSWORD` が有効なのは初回起動時だけです"
    3 つの `TF_ADMIN_*` 変数は最初のアカウントをシードするためのもので、**users テーブルが空の間
    だけ**読み取られます。アカウントが 1 つでも存在すると、シード処理は即座に何もせず終了するた
    め、`TF_ADMIN_PASSWORD` を書き換えて再起動しても何も変わりません。

    初回起動より後のアカウントとパスワードの管理は Web UI から行います。各ユーザーは
    **Settings → Account**（`/settings/account`）で自分のパスワードを変更でき、サイト管理者は
    **Settings → Users**（`/settings/admin/users`）からアカウントの追加、任意のユーザーのパス
    ワードのリセット、2 人目の管理者の任命ができます。
    [認証](../reference/authentication.md#changing-your-password) を参照してください。2 人目の
    管理者を早めに任命しておくことをおすすめします。シードされた管理者がパスワードを失っても、
    インスタンスを復旧できる状態を保てるためです。

## SSH { #ssh }

| 変数 | 説明 | デフォルト | 備考 |
|---|---|---|---|
| `TF_SSH_ENABLED` | git-over-SSH のリスナーを有効にします。 | `true` in Compose (`false` in the server's own default) | Compose では `true`（サーバー自体のデフォルトでは `false`）。2 つ目のポートを開き、永続ストレージ上のホスト鍵が必要になるため、これは明示的なデプロイ判断として扱われます。 |
| `TF_SSH_ADDR` | SSH サーバーの待ち受けアドレス。 | `:2222` | `TF_SSH_ENABLED=true` のときは必須（空にできません）。 |
| `TF_SSH_HOST_KEY_PATH` | サーバーの SSH ホスト鍵が置かれる場所。 | `/data/ssh/host_ed25519` | 存在しない場合は初回起動時に生成されます。**必ず永続ストレージ上に置いてください** — 一時的なファイルシステムの場合、再起動のたびに新しい identity が作られ、すべてのクライアントでホスト鍵の不一致警告が表示されます。 |
| `TF_SSH_IDLE_TIMEOUT` | 反応がなくなった SSH 接続を閉じます。 | `10m` | `0` で無効化します。放置された接続だけを刈り取るものであり、進行中の clone はそれに関係なくストリーミングを継続します。 |
| `TF_SSH_PORT` | SSH リスナーが公開されるホスト側のポート。 | `2222` | Compose 専用（`docker-compose.yml` のポートマッピング）。サーバー自体はこれを読み取りません。 |

クライアントは公開鍵認証のみで接続します。鍵は Web UI の `/settings/ssh-keys` で登録します。

```bash
git clone ssh://git@localhost:2222/admin/imdb-reviews.git
```

## 実験 { #experiments }

| 変数 | 説明 | デフォルト | 備考 |
|---|---|---|---|
| `TF_EXP_FLUSH_INTERVAL` | ネイティブの ingest API のメトリクスポイントが、sync ワーカーによってデータセットリポジトリの Parquet ファイルへコミットされるまでの間、データベース上にのみ留まってよい時間。`0` は flush を無効化します（ポイントはデータベース上にのみ留まり続けます）。 | `1m` | `finished` または `failed` に達した run は、この値にかかわらず常に即座に flush されます。 |

## Git・キャッシュ・Continuity/WAL 移行 { #git-caching-and-the-continuitywal-migration }

| 変数 | 説明 | デフォルト | 備考 |
|---|---|---|---|
| `TF_SYNC_WORKERS` | push 後のジョブ（blob の公開、メタデータインデックスの更新）を処理する並行ワーカーの数。 | `2` | ワーカーは異なる ref を並行して処理します。1 つのリポジトリ・ref に対するジョブは常に順番に 1 つずつ実行されるため、この値を増やしても安全です。 |
| `TF_VIEWER_CACHE_DIR` | WAL compaction 用のローカル作業ディレクトリ。Parquet ビューアはストレージからレンジ読み込みでオブジェクトを直接読むため、もうここにディスクキャッシュを持ちません。 | `/data/cache` | |
| `TF_VIEWER_METADATA_CACHE_BYTES` | Parquet ビューアのプロセス内フッタ（メタデータ）キャッシュのバイト数の上限。 | `268435456`（256 MiB） | これはディスクではなくプロセスのヒープメモリであり、`TF_VIEWER_CACHE_DIR` の tmpfs 予算とは競合しません。 |
| `TF_WAL_MODE` | Continuity 移行がどこまで進んでいるか: `off`、`shadow`、`authoritative` のいずれか。 | `off`（Compose では `shadow`） | `off`: `GIT_ROOT` 配下のディスク上リポジトリが正となります。`shadow`: push は GCS バックエンドの write-ahead log にもベストエフォートでミラーされます — ディスクが正であり続け、WAL の失敗が push を失敗させることはありません。`authoritative`: WAL が正となり、読み取りはそこから実体化され、`GIT_ROOT` は容量に上限のある再構築可能なキャッシュになります。これら以外の値を指定すると起動に失敗します。 |
| `TF_GIT_HOOKS_PATH` | イメージに焼き込まれた `core.hooksPath` のディレクトリで、push を WAL 経由に配線します。 | *(empty)* | **`TF_WAL_MODE` が `off` でない場合は必須です** — これがないと、git smart HTTP 経由の push が WAL を静かにバイパスしてしまうため、設定しないと起動に失敗します。Compose ではこれを `/opt/thinkingface/hooks` に設定しています。 |
| `TF_GIT_CACHE_BYTES` | `TF_WAL_MODE=authoritative` のとき、`GIT_ROOT` 配下の実体化されたリポジトリキャッシュのバイト数の上限。 | `2147483648`（2 GiB） | `0` でエビクションを無効化します。WAL が authoritative でない場合は使われません。 |

## フロントエンド { #frontend }

| 変数 | 説明 | デフォルト | 備考 |
|---|---|---|---|
| `NEXT_PUBLIC_API_URL` | **ブラウザ**が使う API のベース URL。ビルド時／実行時にクライアントバンドルへ焼き込まれます。 | `http://localhost:8080` | ブラウザ自身が到達できるアドレスである必要があります — 内部の Compose ネットワーク名ではありません。 |
| `API_URL` | Next.js の **Server Components とルートハンドラ**が、コンテナ内部から使う API のベース URL。 | *(unset — falls back to `NEXT_PUBLIC_API_URL`)* | `.env.example` には記載されていません。`docker-compose.yml` が `web` サービスの環境変数に `http://api:8080` を直接設定します。これはデプロイごとに運用者が調整するものではなく、内部のサービス間アドレスだからです。この Compose ファイルの外でフロントエンドを実行する場合は、コンテナに直接設定してください。 |

!!! note "なぜ API の URL が 2 つあるのか？"
    `docker compose` の内部では、`web` コンテナは `http://api:8080`（内部のサービス名）で API に
    到達しますが、そのコンテナからページを読み込むブラウザは `api` をまったく解決できません —
    ホストが実際に公開している URL、`http://localhost:8080` が必要です。`API_URL` は前者のケース
    （コンテナ内部で動くサーバーサイドレンダリングとルートハンドラ）をカバーし、
    `NEXT_PUBLIC_API_URL` は後者のケース（ブラウザ上で動くすべて）をカバーします。実際のドメインの
    背後にデプロイする場合も同じ考え方が通用します。`API_URL` にはフロントエンド自身のランタイムが
    バックエンドに到達できるアドレスを、`NEXT_PUBLIC_API_URL` にはエンドユーザーのブラウザが呼び出す
    公開アドレスを、それぞれ設定してください。

## `.env.example` の外で設定する変数 { #variables-set-outside-envexample }

サーバーが読み取る環境変数の中には、典型的なローカル環境において `docker-compose.yml` がそれらを
変える必要がないという理由で、`.env.example` に記載されていないものがいくつかあります。それらは
直接設定すれば（たとえばスタンドアロンのデプロイや `docker-compose.override.yml` で）問題なく
動作します。

| 変数 | 説明 | デフォルト |
|---|---|---|
| `TF_WEBHOOK_WORKERS` | リポジトリ／Organization の webhook を配信する並行ワーカーの数。 | `1` |
| `TF_WEBHOOKS_ALLOW_PRIVATE_TARGETS` | webhook の SSRF ガードを無効化し、localhost やプライベートネットワーク上の webhook 宛先を許可します。 | `false` — ローカル開発以外ではデフォルトのままにしてください。 |

## 関連ページ { #see-also }

- これらの変数が Docker Compose、SQLite と PostgreSQL の選択、GCP の本番セットアップの中でどう
  組み合わさるかについては [デプロイ](deployment.md) を参照してください。
- `TF_ADMIN_USERNAME` / `TF_ADMIN_PASSWORD` でログインできた後のアクセストークンについては
  [認証](../reference/authentication.md) を参照してください。
