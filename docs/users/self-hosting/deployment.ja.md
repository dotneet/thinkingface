# デプロイ

このページは thinkingface を運用する人向けです。評価用にどう起動するか、どのデータベース・ストレージバックエンドが存在してどちらを選ぶべきか、GCP 上の本番環境がどのようなものか、そしてアップグレードやバックアップまわりで何が起きる（あるいは起きない）のかを扱います。環境変数の完全なリファレンスは [設定](configuration.md) を参照してください。

!!! warning "ネットワークの到達範囲が、読み取りの境界です"

    thinkingface にはリポジトリ単位の公開設定がありません。デフォルトでは、インスタンス上のすべての
    リポジトリは、認証の有無にかかわらず、そこに到達できる誰からでも読み取り・クローン・ダウンロード
    が可能です — アカウントと Organization のロールが制御するのは書き込みだけです。想定する利用者
    だけが到達できる場所にデプロイし、ネットワークの到達可能性こそが実質的なアクセス制御であると
    捉えてください。`TF_REQUIRE_AUTH_FOR_READ`（[後述](#require-sign-in-for-reads)）を使うと
    インスタンス全体で匿名の読み取りを閉じられますが、サインインできるアカウントであれば、依然として
    すべてのリポジトリを読めます。

## ローカル環境と評価用デプロイ（Docker Compose） { #local-and-evaluation-deployment-docker-compose }

このリポジトリにはスタック全体（Web UI、API、PostgreSQL、ローカル GCS エミュレータ）を起動する
`docker-compose.yml` が同梱されています。

```bash
cp .env.example .env
docker compose up -d
```

これは `make up` と同等です。これにより次の 4 つのサービスが起動します。

| サービス | イメージ / ビルド元 | 役割 |
|---|---|---|
| `web` | `frontend/` からビルド | Next.js の UI。ポート 3000 で `next start` により提供される。ブラウザからの API 呼び出しを `api` へ中継する役割も担う（[後述](#how-the-web-ui-reaches-the-api)） |
| `api` | `backend/` からビルド | Go のバイナリ。HF 互換 REST API、git smart HTTP、LFS、Parquet ビューア、バックグラウンド同期ワーカーをすべて 1 プロセスに含み、ポート 8080 で待ち受ける |
| `postgres` | `postgres:17-alpine` | メタデータ用データベース（リポジトリ、ユーザー、トークン、ジョブ、実験の run） |
| `gcs` | `fsouza/fake-gcs-server` | 実際の GCS バケットの代わりとなるローカルエミュレータ |

起動後は次のようになります。

- Web UI: <http://localhost:3000>
- API: <http://localhost:8080> — `tf`、`huggingface_hub`、`git`、trackio シム用です。ブラウザに
  必要なのは Web UI のポートだけです
- デフォルトのログイン: `admin` / `admin`（下記の警告を参照）

これらのポートは `127.0.0.1` にだけ公開されるので、Docker を動かしているマシンからしか到達でき
ません。その理由と、LAN に開放する方法・リモートのマシンに SSH 経由で接続する方法については
[ネットワークからのアクセス](#network-access) を参照してください。

### データの永続化 { #data-persistence }

各ステートフルなサービスは、それぞれ名前付きの Docker ボリュームに書き込みます。

| ボリューム | 保持する内容 |
|---|---|
| `pg-data` | PostgreSQL のデータディレクトリ |
| `gcs-data` | fake-gcs-server のバックエンドファイルシステム（LFS オブジェクト、blob） |
| `git-data` | `GIT_ROOT`（`/data`）配下のベア git リポジトリ、および生成された SSH ホスト鍵 |
| `sqlite-data` | SQLite のデータベースファイル（SQLite モードでのみ使用） |

これらのボリュームは、コンテナの再起動や `docker compose down` をまたいでも維持されます。

### 停止とリセット { #stopping-and-resetting }

```bash
docker compose down    # stop and remove containers; volumes are kept
make clean              # down -v on both stacks (default and SQLite) -- also removes the named volumes
```

`docker compose down`（または `make down`）はデータをそのまま残すので、その後 `docker compose up -d`
を実行すれば中断したところからそのまま再開します。データベースをまっさらにし、バケットを空にし、
リポジトリを一切ない状態から始めたい場合は `make clean`（あるいは直接 `docker compose down -v`）を
実行してください。これはコンテナとともに名前付きボリュームも削除します。`make clean` は SQLite
モードのスタック（`sqlite-data`）も対象に含みます。素の `docker compose down -v` ではあちらは
残ります。

!!! warning "他の人に公開する前にデフォルト値を変更してください"
    デフォルトのまま `docker compose up` すると、よく知られたパスワード `admin` を持つ `admin`
    アカウントがシードされ、公開されている開発用のシークレットでセッションクッキーが署名されます。
    自分だけが到達できるラップトップ上であれば問題ありません。それ以外の誰かが到達できるネットワーク
    にこのインスタンスを置く前に、`.env` で `TF_ADMIN_PASSWORD` と `TF_SESSION_SECRET` を設定して
    ください — どちらも [設定](configuration.md) を参照してください。`TF_PUBLIC_URL` が
    `localhost`/`127.0.0.1`（および `.localhost` 名）以外になっている場合、サーバーはこれらの
    デフォルト値のままでは一切起動を拒否します。社内ホスト名や IP に対する平文の `http://` インスタンス
    も対象で、`https://` の場合に限りません。

## ネットワークからのアクセス { #network-access }

### 公開ポートはデフォルトでループバック限定 { #published-ports-are-loopback-only-by-default }

`docker-compose.yml` は `web`（3000）、`api`（8080）、git over SSH（2222）のポートを `127.0.0.1`
に公開します。**Docker が公開したポートはホストのファイアウォールを素通りします。** Docker は
`ufw` や `firewalld` などよりも前段に独自のパケットフィルタのルールを書き込むため、すべての
インターフェースに公開したポートは、ホストのファイアウォールの設定にかかわらずネットワーク全体
から到達できてしまいます。よく知られた `admin` / `admin` のログインを持つ立ち上げ直後のスタックを、
それが動いているマシンの中だけに閉じておけるのは、ループバックへのバインドのおかげです。

同じネットワーク上の他のマシンからスタックに到達できるようにするには、`.env` で `TF_BIND_ADDR`
を設定してコンテナを作り直します。

```bash
echo 'TF_BIND_ADDR=0.0.0.0' >> .env    # または特定のインターフェースのアドレス
docker compose up -d
```

その前に `TF_ADMIN_PASSWORD` と `TF_SESSION_SECRET` を設定し、`TF_PUBLIC_URL` を他のマシンが
使うアドレスに向けてください（ループバック以外になると、サーバーはデフォルトのシークレットのまま
では起動を拒否します — 上の警告を参照）。

`postgres`（5432）と `gcs` エミュレータ（4443）は、`TF_BIND_ADDR` の値にかかわらず常に
`127.0.0.1` に公開されます。エミュレータには独自の認証がなく、どちらかに直接到達する必要がある
のはホスト側のツール（`make test-store-pg`、E2E スイートのバケット検査）だけだからです。コンテナ
同士は、これとは関係なく Compose のネットワーク経由で通信します。

### Web UI が API に到達する仕組み { #how-the-web-ui-reaches-the-api }

ブラウザが通信するのは Web UI のオリジンだけです。ブラウザが行う API 呼び出し — `/api/` 配下と
ファイルのダウンロード — はすべて Next.js のサーバーに届き、そこから `API_URL`（Compose では
`http://api:8080`）の API へ転送されます。アップロードもダウンロードも、両方向ともストリーミング
で中継されます。したがって、次のようになります。

- **ブラウザに必要なのは Web UI のポートだけです。** API のポートは、ブラウザ以外のクライアント
  — `tf` CLI、trackio シム、`huggingface_hub`、`git`、`git-lfs` — のためのものです。
- **`API_URL` はコンテナの起動時に読まれます。** イメージに焼き込まれるわけではないので、同じ
  `web` イメージを API の置き場所にかかわらず使え、値を変えるときに必要なのはリビルドではなく
  再起動です。
- **Web UI のために `TF_ALLOWED_ORIGINS` へエントリを追加する必要はありません。** ブラウザが
  クロスオリジンで API を呼ぶことはないからです。状態を変更するリクエストについては、転送の前に
  プロキシ自身が、自分のオリジンに対して同等のクロスサイトリクエストのチェックを行います。
- **クローン URL と、使い方スニペット中の `HF_ENDPOINT` は、引き続き `TF_PUBLIC_URL` から
  作られます。** これらは API と直接通信する `git` や `huggingface_hub` のためのものなので、
  `TF_PUBLIC_URL` は依然として *それらのクライアント* が API に到達するアドレスでなければなりません。
  エミュレータモード（`STORAGE_DRIVER=gcs-emulator`）では、同じ URL が Git LFS の転送リンクにも
  埋め込まれるため、`TF_PUBLIC_URL` に到達できないマシンからの LFS のアップロードやダウンロードは
  失敗します。

これがデフォルトの動作で、`NEXT_PUBLIC_API_URL` を空にして web イメージをビルドした場合に適用され
ます。Compose と `frontend/Dockerfile` は、明示的に設定しない限りそのようにビルドします。
`NEXT_PUBLIC_API_URL` を設定すると、従来のクロスオリジンの動作になります。ブラウザがその URL を
直接呼ぶので、ブラウザからその URL に到達できる必要があり、値はイメージに組み込まれ（変更した後は
`docker compose up -d --build web` でリビルドします）、Web UI のオリジンを `TF_ALLOWED_ORIGINS`
に列挙しておく必要があります。

!!! note "プロキシの背後でのクライアントアドレス"
    プロキシの背後では、ブラウザからのリクエストはすべて `web` コンテナから API に届きます。一方で
    API は、パスワード認証の失敗をクライアントアドレスごとにレート制限しています（デフォルトで
    1 分あたり 10 回、`TF_AUTH_RATE_LIMIT_PER_MIN`）。1 人の訪問者がサインインに失敗し続けても他の
    ブラウザが巻き込まれないよう、web コンテナのサーバーは各ブラウザ自身のソケットアドレスを専用の
    ヘッダーで API に伝え、API はそのヘッダーを Web 層から届いたときだけ信用します。

    - API 側の `TF_TRUSTED_WEB_PROXIES` に接続元が含まれている場合。compose ファイルではこれを
      web コンテナの名前である `web` にしているので、追加の設定なしで機能します。API のポートを直接
      呼び出す相手は別のアドレスから届くため、このヘッダーを偽装できません。
    - あるいは、`TF_WEB_PROXY_SECRET` を持つリクエストの場合。API と web コンテナの両方に同じ値
      （32 バイト以上、`openssl rand -hex 32`）を設定します。Web UI がロードバランサ経由で API に
      到達し、接続元アドレスが何も示さない構成ではこちらを使います。Terraform のデプロイは値を自動で
      生成します。

    `X-Forwarded-For` に追記するリバースプロキシが Web UI の前段にある場合は、web コンテナの
    `TF_WEB_TRUSTED_PROXY_HOPS` にその段数を設定してください。ブラウザのアドレスを右からその数だけ
    手前のエントリとして読み取ります。そうでなければ `0` のままにしておけば、ブラウザが
    `X-Forwarded-For` に何を書いても無視されます。`TF_TRUST_PROXY_IPS` は *API* の前段のプロキシ
    向けの、これとは別の古いスイッチです。すべての接続から届く `X-Forwarded-For` を信用するので、
    API のポートが信頼できないクライアントから到達できない場合にだけ有効にしてください。

    `TF_TRUSTED_WEB_PROXIES` も `TF_WEB_PROXY_SECRET` も設定していない場合（あるいは Web UI を本番用
    サーバーではなく `next start` / `next dev` で動かしている場合）、ブラウザはすべて 1 つのアドレス
    の枠を共有する状態に戻ります。このとき Web UI に到達できる者は誰でも、自分のサインインを繰り返し
    失敗させるだけで、全員のブラウザサインインを `429` にできてしまいます。既存のセッション、個人
    アクセストークン、`git`、SSH はこの制限を経由しません。`huggingface_hub`、`git`、`tf` は API と
    直接通信するので、それぞれ自身のアドレスで識別されます。

### SSH トンネル越しのプライベートなデプロイ { #a-private-deployment-over-an-ssh-tunnel }

自分や小さなチームのために thinkingface を動かすよくある方法は、パブリック IP を一切持たない VM
に置き、SSH（直接、踏み台経由、あるいは `gcloud compute ssh --tunnel-through-iap`）で到達する
構成です。ポートがループバック限定なので何も外に公開されず、トンネルが唯一の入口になります。

VM 上で:

```bash
cp .env.example .env
# 下のトンネル越しにワークステーションから到達する API のアドレス。クローン URL、
# HF_ENDPOINT のスニペット、（エミュレータ使用時は）LFS のリンクに埋め込まれる。
echo 'TF_PUBLIC_URL=http://localhost:18080' >> .env
docker compose up -d
```

ワークステーション上で:

```bash
ssh -N -L 13000:127.0.0.1:3000 -L 18080:127.0.0.1:8080 my-vm
```

- Web UI は <http://localhost:13000> です。ブラウザに必要なのはこの転送だけです。
- 2 つ目の転送は、それ以外のすべて — `tf` CLI、trackio シム、`huggingface_hub`、`git` — のための
  ものです。

  ```bash
  tf login http://localhost:18080
  export THINKINGFACE_ENDPOINT=http://localhost:18080   # trackio シム
  export HF_ENDPOINT=http://localhost:18080 HF_HUB_DISABLE_XET=1
  ```

- 代わりに同じポート番号で転送する（`-L 3000:127.0.0.1:3000 -L 8080:127.0.0.1:8080`）と、
  デフォルトの `TF_PUBLIC_URL=http://localhost:8080` のまま動きます。ワークステーション側でそれらの
  ポートが空いていれば、こちらでも構いません。
- ここでの `TF_PUBLIC_URL` はループバックアドレスなので、サーバーはデフォルト以外のシークレットを
  要求しません。それでも、VM に他の誰かがシェルを持っていたりトンネルを共有したりするなら、
  `TF_ADMIN_PASSWORD` と `TF_SESSION_SECRET` を設定してください。
- git over SSH を使うなら `-L 12222:127.0.0.1:2222` を追加し、UI に表示されるクローン URL と一致
  するよう `TF_SSH_PUBLIC_PORT=12222` を設定します。

別の場所にある学習用マシンも、同じ方法 — そのマシンから VM への `ssh -L` — で同じ API に到達
できます。VM にまったく到達できないマシンなら、run をオフラインで記録して後から同期してください
（[オフラインの run](../guides/experiments.md#offline-runs-and-tf-experiments-sync)）。

### 読み取りにもサインインを必須にする { #require-sign-in-for-reads }

デフォルトでは、インスタンスに到達できる人なら誰でも、サインインせずにすべてのリポジトリとすべての
run を読めます。ネットワークの境界だけでは足りない場合 — 1 つの VPN の背後で複数のチームが共有する
インスタンスや、インターネットから到達できるインスタンス — は、次のように設定します。

```bash
TF_REQUIRE_AUTH_FOR_READ=true
TF_ALLOW_SIGNUP=false
```

こうすると、すべてのリクエストが身元 — セッションクッキー、アクセストークン、HTTP Basic 認証の
いずれか — を伴う必要があり、伴わないものは読み取りか書き込みかにかかわらず、エラー種別
`authentication_required` の `401` で応答されます。サインインが必要だと知り、実際にサインインする
ために必要な少数のルート — `/healthz`、サインイン・サインアップ・サインアウト、`/api/v1/me`、
`/api/v1/server-info` — だけは開いたままです。`/api/openapi.json` はこれに含まれません。

各クライアントの振る舞いは次のとおりです。

- **Web UI**: サインインしていない訪問者は `/login` にリダイレクトされ、サインイン後は元のページ
  に戻ります。
- **`git` / `git-lfs`**: `401` に `WWW-Authenticate` チャレンジが付くので、git は認証情報を尋ねて
  （あるいはクレデンシャルヘルパーに問い合わせて）再試行します。パスワードとしてアクセストークンを
  使ってください。サーバーが署名済みの Git LFS 転送リンクは引き続き使えます。git over SSH は影響を
  受けません。もともと登録済みの鍵が必要だからです。
- **`huggingface_hub` / `datasets`**: 書き込みのときと同じく `HF_TOKEN` を設定します。
- **`tf` と trackio シム**: 読み取りにもトークンが必要になります — `tf login`、
  `THINKINGFACE_API_KEY`、または `THINKINGFACE_TOKEN`。

停止中のアカウントや、サインアップの承認待ちのアカウントも、ここでは匿名として扱われます。
**`TF_ALLOW_SIGNUP=false` と組み合わせてください** — あるいは `TF_SIGNUP_REQUIRE_APPROVAL` /
`TF_SIGNUP_EMAIL_DOMAINS` と。そうしないと、サインアップフォームに到達できる人なら誰でも
アカウントを作って結局すべてを読めてしまいます。サインイン済みのアカウントと有効なトークン
（read スコープのものやリポジトリ限定のものも含む）は、依然としてすべてのリポジトリを読めます。
この設定は匿名アクセスを閉じるものであって、リポジトリ単位の公開設定を追加するものではありません。

## ブラウザを使わずにトークンを発行する { #provisioning-a-token-without-a-browser }

CI パイプライン、学習用マシン、AI エージェントといった自動化にはアクセストークンが必要ですが、
立ち上げたばかりのインスタンスでは、まだ誰も Web UI を開いていないかもしれません。
`thinkingface admin token create` は、Web UI のトークンフォームと同じルールでデータベースの
すぐそばからトークンを発行し、stdout にはトークンだけを出力します。

```bash
docker compose exec -T api thinkingface admin token create admin \
    --name ci --expires-in-days 30 --repo datasets/admin/trackio-metrics > ci-token.txt
chmod 600 ci-token.txt
```

`-T` は重要です。これがないと Docker が端末を割り当て、コマンドの stderr（何を、誰のために、
いつまで有効なものとして作ったか）がファイルに混ざってしまいます。`--repo` はトークンをその
リポジトリに限定しますが、リポジトリはあらかじめ存在している必要があります — 先に `tf up`、
`huggingface_hub`、または Web UI で作成してください。`--output FILE` を使うとトークンをファイルに
書き出せますが（モード `0600`、既存ファイルなら拒否）、そのファイルは *コンテナの中* に作られるので、
Compose では上のように stdout をリダイレクトするほうが簡単です。すべてのフラグについては
[コマンドラインからトークンを発行する](../reference/authentication.md#minting-a-token-from-the-command-line)
を、リポジトリ限定トークンで何ができるかについては
[トークンをリポジトリに限定する](../reference/authentication.md#restricting-a-token-to-repositories)
を参照してください。

## ログからのトラブルシューティング { #troubleshooting-from-the-logs }

### 実効設定のログ行 { #the-effective-configuration-line }

`thinkingface serve` は起動時に、実際に解決した設定 — `.env`、Compose の `environment:` ブロック、
組み込みのデフォルト値がすべて反映された後のもの — を 1 行ログに出力します。

```bash
docker compose logs api | grep 'effective configuration'
```

```json
{"time":"...","level":"INFO","msg":"effective configuration","public_url":"http://localhost:8080",
 "listen_addr":":8080","ssh_enabled":true,"ssh_addr":":2222","ssh_public_port":"",
 "allowed_origins":["http://localhost:8080","http://localhost:3000","http://127.0.0.1:3000"],
 "database_driver":"postgres","storage_driver":"gcs-emulator","storage_bucket":"thinkingface",
 "storage_prefix":"","storage_emulator_host":"http://gcs:4443","wal_mode":"shadow","allow_signup":true,
 "signup_require_approval":false,"signup_email_domains_count":0,"org_creation":"anyone",
 "require_auth_for_read":false,"cookie_secure":"inferred","trust_proxy_ips":false,
 "trusted_proxy_hops":1,"session_secret_default":true,"admin_password_default":true}
```

設定が効いていないように見えるときは、この行を見ればサーバーがそもそもその値を受け取ったかどうかが
分かります。シークレットがログに出ることはありません。データベースはドライバ名としてしか現れず、
`session_secret_default` / `admin_password_default` は、その 2 つがまだよく知られた開発用の
デフォルト値のままかどうかを示すだけです。

### "cors: origin not allowed" { #cors-origin-not-allowed }

```json
{"level":"WARN","msg":"cors: origin not allowed","origin":"http://10.0.0.5:3000",
 "hint":"add it to TF_ALLOWED_ORIGINS, or use the web UI's same-origin /api proxy"}
```

そのオリジンのブラウザのページが API を直接呼び、CORS ヘッダーを返してもらえなかったため、ページは
レスポンスを読めませんでした。オリジンごとに 1 回（異なるオリジン 64 個まで）記録されます。
デフォルトの同一オリジンのプロキシを使っていればブラウザがクロスオリジンで API を呼ぶことはない
ので、これが出るのは通常、web イメージが `NEXT_PUBLIC_API_URL` を設定した状態でビルドされている
場合です。Web UI のオリジン（スキーム・ホスト・ポートを、ブラウザのアドレスバーに表示されている
とおりに）を `TF_ALLOWED_ORIGINS` に追加するか、`NEXT_PUBLIC_API_URL` を空にして web イメージを
リビルドしてください。見覚えのないオリジンであれば、それは API を探っている別のページであり、
対処は不要です。

## データベースバックエンドを選ぶ { #choosing-a-database-backend }

thinkingface は単一の `DATABASE_URL` 環境変数を読み取り、そのスキームによって処理を振り分けます。

- `postgres://` または `postgresql://` — PostgreSQL
- `sqlite://` — SQLite（pure Go、`modernc.org/sqlite` 経由。CGo なし）

（オーバーライドなしの）`docker compose up` は常に PostgreSQL で動作します。`docker-compose.yml` が
`POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` から `DATABASE_URL` を組み立て、`api`
サービスに直接渡しているためです。

### SQLite モード { #sqlite-mode }

スタック全体を SQLite に対して起動する（`postgres` コンテナ自体を使わない）には、次のようにします。

```bash
make up-sqlite
```

これは `docker compose -f docker-compose.yml -f docker-compose.sqlite.yml up -d api web gcs` を
実行します。`postgres` サービスは除外され、api コンテナの `DATABASE_URL` は
`sqlite:///data/db/thinkingface.db` となり、`sqlite-data` ボリュームに永続化されます。

SQLite モードは評価用途、単一オペレータでの運用、あるいは小規模チームには妥当な選択であり、
下記の本番向け「SQLite + Litestream」構成もこれをベースにしています。ただし、その規模を超えると
選ぶべきではなくなるいくつかの明確な制限があります。

- **単一プロセス、単一の書き込みコネクション。** 複数レプリカからの同時書き込みはサポートされて
  おらず、アプリケーション層でのクラスタリングやレプリケーションの仕組みはありません。
- **水平スケールができません。** 同じ SQLite ファイルを指す `api` レプリカを複数実行することは
  できません。
- 検索の挙動が PostgreSQL と異なります。HF 互換の `search=` の部分文字列一致は `LIKE` になり、
  これは ASCII 文字に限って大文字小文字を区別しません（Unicode のケースフォールディングは行われ
  ません）。また Web UI の全文検索は PostgreSQL の `tsvector` ではなく SQLite の FTS5
  （`unicode61` トークナイザ）で動作するため、ランキングやステミングの挙動は両者で同一では
  ありません。

複数の `api` レプリカが必要な場合、ネームスペースや検索対象のコンテンツが非 ASCII のケース
フォールディングに依存する場合、あるいは PostgreSQL の挙動と完全に一致する全文検索が必要な場合は、
SQLite モードを選ばないでください。

### PostgreSQL モード { #postgresql-mode }

PostgreSQL は `docker compose up` のデフォルトであり、単一オペレータでの評価用インスタンスを
超える用途にとっては正しい選択です。同時書き込み、標準的な行レベルロック、そして（本番の Cloud SQL
上では）ポイントインタイムリカバリ対応の自動バックアップをサポートします。どちらを選ぶべきか
迷った場合は、こちらを選んでください。

## オブジェクトストレージ { #object-storage }

大きなファイル（Git LFS オブジェクト）と、push 後に公開される非 LFS の blob は、ディスク上の git
リポジトリではなくオブジェクトストアに保存されます。`STORAGE_DRIVER` が実装を選択します。

- `gcs-emulator` — `STORAGE_EMULATOR_HOST` にある `fake-gcs-server` と通信します。これはローカルの
  `docker compose up` が使うものです。エミュレータは署名付き URL を検証できないため、このモードでは
  サーバー自身がオブジェクトのバイト列をプロキシします。
- `gcs` — `GCS_BUCKET` で指定された実際の Google Cloud Storage バケット（オプションで `GCS_PREFIX`
  配下にスコープ）と通信します。このモードではサーバーが短命の署名付き URL（`TF_SIGNED_URL_TTL`）
  を発行し、クライアント（ブラウザ、`huggingface_hub`、`git-lfs`）が GCS に対して直接転送します。

### 実際の GCS における認証情報と権限 { #credentials-and-permissions-for-real-gcs }

`gcs` ドライバは標準の Google Cloud Go クライアントを使用し、通常の方法で認証情報を解決します —
Application Default Credentials、つまりマウントされたサービスアカウントキー
（`GOOGLE_APPLICATION_CREDENTIALS`）、ローカルでの `gcloud auth application-default login`、
あるいは（本番では）ワークロードにアタッチされたサービスアカウントです。thinkingface 固有の
認証情報用変数はありません。

サービスアカウントには最低限、次の権限が必要です。

- `roles/storage.objectAdmin`（プロジェクト全体ではなく、対象バケットにスコープする）
- 自分自身に対する `roles/iam.serviceAccountTokenCreator` — これにより、ダウンロード可能な
  秘密鍵を持たなくても URL に署名できるようになります（`signBlob`）。これが、ワークロード
  アイデンティティを使うサービスアカウントでキーレスな署名付き URL を機能させる仕組みです。

オブジェクトストレージには 3 つのトップレベルのプレフィックスがあります。`lfs/`（Git LFS
オブジェクト）、`blobs/`（sync ワーカーが push 後に公開するそれ以外のすべてのファイル）、そして
（下記の Continuity 移行が有効な場合）`wal/`（git の write-ahead log）です。これらはすべて内容
アドレス方式であり、同じファイルを生成する別々の push はストレージを共有します。

### バケットに CORS 設定が必要です。さもないとブラウザの機能が 2 つ壊れます { #bucket-cors }

Web UI には、通常のダウンロードリンクではなくブラウザから直接オブジェクトのバイト列を取得する
機能が 2 つあります。データセットビューアの [SQL モード](../guides/dataset-viewer.md#query-with-sql)
（DuckDB-WASM がローカルでクエリするために Parquet ファイル全体をダウンロードします）と、512 KB を
超える CSV / JSON Lines ファイルに対するプレーンファイルプレビューの全文フォールバック（[Web UI を
使う](../guides/web-ui.md#view-a-file) を参照）です。どちらも同じ resolve エンドポイントを経由し、
`STORAGE_DRIVER=gcs` の場合、このエンドポイントはバイト列自体をストリームする代わりに
`storage.googleapis.com` 上の短命な署名付き URL へのリダイレクトで応答します — これは Web UI と API
のどちらとも異なるオリジンです。バケット側がそのオリジンからのクロスオリジンリクエストに対して
CORS ヘッダーで応答するよう設定されていない限り、ブラウザはそのレスポンスの読み取りを拒否し、
両方の機能が失敗します — 通常は CORS や GCS を名指しするものではなく、一般的な「ネットワークエラー」
として報告されるため、原因を見落としやすくなっています。

これは `STORAGE_DRIVER=gcs` に固有の問題です。（`docker compose up` が使う）`gcs-emulator` では
API がリダイレクトの代わりにバイト列自体をストリームするため、ブラウザのリクエストは API 自身の
オリジンから出ることがなく、バケットの CORS ポリシーはまったく関与しません — そのため、実際の
バケットにデプロイを向けるまでこの問題に気づきにくいのです。

`infra/` の Terraform でバケットをプロビジョニングする場合は、すでに対応済みです。
[「GCP 上の本番環境」内の「バケットに CORS 設定が必要です」](#bucket-cors-terraform) を参照して
ください。バケットを自分で（手動、または別のインフラツールで）プロビジョニングする場合は、同じ
ポリシーを直接設定してください。例:

```bash
cat > cors.json <<'EOF'
[
  {
    "origin": ["https://your-web-ui-origin.example.com"],
    "method": ["GET", "HEAD"],
    "responseHeader": ["Content-Type", "Content-Length", "Content-Range", "ETag"],
    "maxAgeSeconds": 3600
  }
]
EOF
gcloud storage buckets update gs://your-bucket --cors-file=cors.json
```

ブラウザが Web UI を読み込んでいる正確なオリジン（スキーム・ホスト・ポート）を指定してください
— バケットの CORS ポリシーにはワイルドカードサブドメインの形式はないため、実際に UI を配信して
いるオリジンをすべて列挙する必要があります。また `"*"` は絶対に使わないでください。このバケットの
背後にあるすべてのオブジェクトは、誰かがブラウザに署名付き URL を取得させた瞬間に到達可能になる
ため、オリジンを明示することが、その読み取りを自分のデプロイだけに限定する手段になります。
`GET`/`HEAD` で上記 2 つの機能はどちらもカバーされます。`STORAGE_DRIVER=gcs` であっても、Web UI が
ブラウザから直接バケットに書き込むことはありません — アップロードは常に API を経由します。

## GCP 上の本番環境 { #production-on-gcp }

`infra/` ディレクトリには GCP 本番デプロイ用の Terraform が入っています。これは次のものを
プロビジョニングします。

- `lfs/`、`blobs/`、（該当する場合）`wal/` 用の GCS バケット。Web UI のオリジンを許可する CORS
  ポリシー付き（後述）
- バックエンドとフロントエンドのイメージ用の Artifact Registry リポジトリ
- API 用の `google_cloud_run_v2_service`（gen2、`h2c`、`min_instance_count = 1`、CPU を常時割り当て、
  データベースに到達するための Direct VPC egress）
- Web フロントエンド用の `google_cloud_run_v2_service`
- バケットと必要なシークレットにスコープされた、API ワークロード用のサービスアカウント
- オプションで、PostgreSQL 17 用の Cloud SQL（プライベート IP のみ、ポイントインタイムリカバリ
  対応の自動バックアップ）

次のコマンドで起動します。

```bash
cd infra
terraform init            # add -backend-config=... once you configure a real backend
terraform plan  -var="project_id=my-gcp-project"
terraform apply -var="project_id=my-gcp-project"
```

Terraform はインフラをプロビジョニングしますが、その後のコンテナイメージフィールドのドリフトは
意図的に無視するため、新しいイメージを push して Cloud Run サービスとジョブにそれを指すよう
設定するのは別の手順です（`gcloud run deploy` / `gcloud run jobs update`）。この最初の `apply`
だけでは動く状態には**なりません** — さらに2点、対応が必要です。どちらも `infra/README.md` の
「After `apply`」の手順に詳しく書かれています。

- **api の公開 URL は、自分で設定するまでプレースホルダー
  （`https://api.{environment}.example.com`）のままです。** LFS の href 生成、HF 互換の resolve
  リダイレクトは、デフォルトのままでは壊れます。`api` をデプロイし、`terraform output -raw
  api_url` で実際の URL を確認する（またはカスタムドメインをそこに向ける）、その値を
  `-var="api_public_url=..."` として渡し、再度 apply してください。CORS の許可リスト
  （`TF_ALLOWED_ORIGINS`）は別の変数（`web_public_url` から導出されます — 詳細は
  `infra/README.md` を参照）で、`web` サービスが存在すればその `*.run.app` URL にデフォルトで
  設定されるため、この手順をしなくても最初から機能します。`web_public_url` を明示的に設定するのは、
  `web` の前にカスタムドメインを置く場合だけで構いません。
- **`infra/README.md` では、api の URL が分かった *後に* web フロントエンドのイメージを
  ビルドします。** `docker build --build-arg NEXT_PUBLIC_API_URL=$(terraform output -raw api_url) ...`
  という形です。これは [Web UI が API に到達する仕組み](#how-the-web-ui-reaches-the-api) で説明した
  クロスオリジンのモードです。ブラウザは api の `*.run.app` の URL を直接呼び、値は起動時に読まれる
  のではなく `docker build` 時にブラウザバンドルに組み込まれ、Web UI のオリジンが
  `TF_ALLOWED_ORIGINS`（前述のとおり Terraform が導出します）に含まれている必要があります。
  build arg なしでビルドすると、代わりに同一オリジンのイメージになり、そのサーバーがブラウザからの
  API 呼び出しを、Terraform がすでに `web` サービスに設定している `API_URL` へ転送します — その
  方法を取る場合は、同セクションのクライアントアドレスに関する注意に気をつけてください。

この Terraform ではプロビジョニングされないもの: カスタムドメインや TLS のフロントエンドです。
Cloud Run 自体が TLS を終端し、各サービスをそれぞれの `*.run.app` の URL で提供するため、
最初のうちはこれで十分です。ドメイン戦略を決めたら、ドメインマッピングやロードバランサを
追加してください。

### バケットに CORS 設定が必要です { #bucket-cors-terraform }

これが何のためのものかは、上記の
[「バケットに CORS 設定が必要です。さもないとブラウザの機能が 2 つ壊れます」](#bucket-cors)を
参照してください。`infra/` のバケットリソースには、すでに正しいポリシーが設定されています。
`TF_ALLOWED_ORIGINS` と同じ値 — `web_public_url` を設定していればその値、そうでなければ `web`
Cloud Run サービス自身の `*.run.app` URL — から導出されるため、2 つの許可リストが食い違うこと
はありません。`TF_ALLOWED_ORIGINS` と同様、これは最初の `apply` の時点から実際の値に解決され
ます（`web` の `*.run.app` URL はその名前・リージョン・プロジェクトから決定的に決まるため。上記
で説明した手動の再 apply が必要な `api_public_url` がプレースホルダーにフォールバックするのとは
対照的です）— `web` の前にカスタムドメインを置く場合を除き、追加の手順は不要です。その場合は
`web_public_url` を設定して再度 apply すれば、両方の許可リストがそれに追従します。ポリシーの
キャッシュ有効期間は `var.bucket_cors_max_age_seconds`（デフォルト 1 時間。説明は
`infra/variables.tf` を参照）です。

### GCP 上のデータベース: Cloud SQL か SQLite + Litestream か { #database-on-gcp-cloud-sql-vs-sqlite-litestream }

Terraform の `database_backend` 変数（デフォルトは `postgres`、または `sqlite`）は、API とその
スケジュールされたメンテナンスジョブがメタデータをどう永続化するかを切り替えます。

- **`postgres`** — Cloud SQL for PostgreSQL 17 インスタンス（プライベート IP のみ）で、Cloud Run
  から Direct VPC egress 経由で到達します。`DATABASE_URL` は組み立てられ、Secret Manager に
  格納されます。同時稼働する複数の API インスタンス間で厳密な一貫性が必要な場合に選ぶべき
  構成です。
- **`sqlite`** — Cloud SQL インスタンスは一切作成されません。`DATABASE_URL` はコンテナの一時的な
  ファイルシステムを指す通常の（シークレットではない）`sqlite:///data/db/thinkingface.db` に
  なり、`TF_LITESTREAM_REPLICA_URL` に `gs://` パスが設定されます。コンテナのエントリポイント
  （`backend/entrypoint.sh`）は [Litestream](https://litestream.io) を使い、起動時に GCS から
  そのファイルを復元し、サーバー稼働中は書き込みを継続的にそこへレプリケートします。使うのは
  ワークロード自身の認証情報で、追加のキーは不要です。SQLite は単一の書き込み元を前提とするため、
  このモードでは Cloud Run サービスの `max_instances` は、設定された最大値にかかわらず強制的に
  `1` になります。

  これにより Cloud SQL をまったく実行しなくて済むため、小規模なデプロイにとっては魅力的ですが、
  実際には注意点があります。Cloud Run のリビジョンロールアウトでは、旧リビジョンと新リビジョンが
  短時間並行して動くことがあり、`sqlite` モードではそれが、デプロイのたびに同じ GCS レプリカに
  対する 2 つの書き込み元が短時間存在することを意味します。Litestream は複数の書き込み元を調停
  しないため、その窓の間に旧リビジョン側に到達した書き込みは失われる可能性があります。これを
  緩和するには `--no-traffic` でデプロイして手動でトラフィックを切り替えるか、デプロイ頻度が
  低いのであればこの小さな窓を許容してください。厳密な一貫性が必要な場合は、代わりに Cloud SQL
  構成を使ってください。

  **このモードではガベージコレクションが一切行われません。** `thinkingface gc` は不要になった
  `lfs/`/`blobs/` オブジェクト（削除されたリポジトリ、置き換えられたファイルなど）をデータベースの
  参照カウントを読んで回収しますが、これにはサービング中のプロセスと同じ「生きた」データを
  見る必要があります。`sqlite` モードではそれができません — `gc` が見るのは Litestream で復元
  された**スナップショット**でしかなく、そのスナップショット取得後にアップロードされた、まだ
  参照されている生きたオブジェクトを削除してしまう恐れがあります。`backend/entrypoint.sh` は
  このモードで `gc` の実行自体を拒否します（そのリスクを取らず即座に終了する）し、Terraform の
  `sqlite` 構成ではそもそもスケジュールされた `gc` Cloud Run Job 自体が作られません。実際には、
  `sqlite` デプロイの稼働期間中、バケット内の `lfs/`・`blobs/`・`tmp/uploads/` は増え続ける
  一方になります — ストレージコストの見積もりにこれを織り込んでください。削除・置き換え済みの
  コンテンツからストレージを回収できることが重要であれば、代わりに `postgres` 構成を選んで
  ください。

### Continuity / WAL 移行 { #the-continuity-wal-migration }

git 自体の最近の Cloud Run 対応は、Continuity 移行（[設定](configuration.md) の
`TF_WAL_MODE`）と呼ばれる設計の上に成り立っています。ベア git リポジトリのために永続ディスクを
要求する代わりに、push は GCS バケット内の世代ベースの write-ahead log（`wal/`）にも追加で書き込まれ、
これによりローカルディスクを、再構築可能なウォームキャッシュへと格下げできます。`docker-compose.yml`
はデフォルトで API を `shadow` モードで実行します（push はベストエフォートで WAL にもミラーされ、
ディスクが正とされ続けます）。そして Terraform の Cloud Run 構成は、永続ボリュームなしで動作する
ためにこの移行に依存しています。Cloud Scheduler によってトリガーされる毎日実行の Cloud Run Job
（`compact`）が WAL のコンパクションを行います。

## アップグレードとデータベースマイグレーション { #upgrades-and-database-migrations }

データベースに触れる `thinkingface` のすべての呼び出し — `serve` 自体を含む — は、起動時に他の何
よりも先に、保留中の SQL マイグレーションを自動的に適用します。マイグレーションは `schema_migrations`
テーブル内でファイル名によって追跡され、それぞれが順番に、ちょうど 1 回だけ適用されます。すでに
適用済みのマイグレーションを再実行しても何も起きません。つまり、通常のイメージアップグレードと
再起動（新しいイメージを pull した後の `docker compose up -d`、あるいは Cloud Run のデプロイ）は、
起動処理の一部としてデータベースマイグレーションを実行します — 一般的なケースで手動で実行すべき
別個のマイグレーション手順はありません。

ロールアウトに先立ってマイグレーションを適用したい場合（たとえばメンテナンスウィンドウを短く
保つため）は、同じバイナリで直接それを行えます。

```bash
docker compose run --rm api migrate
```

これは保留中のマイグレーションを適用し、サーバーを起動せずに終了します。

## バックアップとリストア { #backup-and-restore }

このリポジトリが実際に提供するものは、バックエンドによって異なります。

- **Cloud SQL 上の PostgreSQL**: Cloud SQL 自体が提供し、`infra/` の Terraform 構成で有効化される、
  ポイントインタイムリカバリ対応の自動日次バックアップ。
- **Cloud Run 上の SQLite + Litestream**: SQLite ファイルの GCS への継続的なレプリケーション。
  リストアは、コンテナのエントリポイントが起動時に実行するのと同じコマンドである
  `litestream restore` をレプリカの URL に対して実行することを意味します。thinkingface 独自の
  リストアコマンドは別途ありません。
- **オブジェクトストレージ**（`lfs/`、`blobs/`、Continuity が有効な場合は `wal/`）: Terraform
  構成はバケットのバージョニングを有効にするため、WAL のインデックスの古い世代や、上書きされた
  オブジェクトは、明示的に削除しない限り復元可能な状態で残ります。孤立した LFS オブジェクトや
  blob の削除は、経過時間ベースのライフサイクルルールではなく、参照カウント方式のガベージ
  コレクション（`thinkingface gc`、デフォルトは `--dry-run`）によって処理されるため、バケット内の
  ものが勝手に消えることはありません。`postgres` モードでは、Terraform 構成がこれをスケジュール
  実行するところまで面倒を見ます。Cloud Scheduler がトリガーする毎週実行の Cloud Run Job（`gc`）で、
  `compact` のスケジュールとは時刻をずらしてあり、2 つが同時に走ることはありません。**`sqlite` モード
  では gc の Job は作られません** — ライブのデータベースではなく Litestream が復元したスナップショット
  を読むことになり、そのスナップショット以降にアップロードされたオブジェクトを削除しかねないため、
  Job を作らず entrypoint 側でも拒否します。このモードではストレージは自動回収されません。オプトインするまでは孤立オブジェクトを
  *報告するだけ*で、Terraform 変数 `gc_delete_enabled` を `true` にすると実際に削除するようになり
  ます。いくつかの dry-run のレポートを確認し、実際にデプロイがまだ参照しているものと一致している
  ことに確信が持ててから切り替えてください。詳しい理由と、デフォルトの週次スケジュールを待たずに
  監督付きの単発削除を実行する方法は `infra/README.md` を参照してください。
- **ローカルの Docker Compose デプロイ**: バックアップの仕組みは一切ありません。データは
  `pg-data` / `sqlite-data`、`gcs-data`、`git-data` という名前付きボリュームに存在し、リポジトリ
  内の何も、それらをスナップショットしたりどこかへ送ったりしません。Compose ベースのデプロイで
  バックアップが必要な場合は、それらの Docker ボリューム自体を自分でバックアップする責任があります
  （たとえば、定期的な `docker run --rm -v pg-data:/data ... tar` ジョブなどで）。リポジトリは
  それを提供しません。

PostgreSQL/SQLite の外側では、ディスク上のベア git リポジトリ（あるいは Continuity 有効時は WAL）
がリポジトリコンテンツの正となります。上記のうち自分のデプロイに該当するものに従って、それらを
バックアップしてください。

## 関連ページ { #see-also }

- すべての環境変数（上記で参照した `TF_ADMIN_PASSWORD`、`TF_SESSION_SECRET`、`DATABASE_URL`、
  `STORAGE_DRIVER`、`TF_WAL_MODE` などを含む）については [設定](configuration.md) を
  参照してください。
- インスタンスが起動した後のアクセストークンと SSH 鍵については
  [認証](../reference/authentication.md) を参照してください。
