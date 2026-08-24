# デプロイ

このページは thinkingface を運用する人向けです。評価用にどう起動するか、どのデータベース・ストレージバックエンドが存在してどちらを選ぶべきか、GCP 上の本番環境がどのようなものか、そしてアップグレードやバックアップまわりで何が起きる（あるいは起きない）のかを扱います。環境変数の完全なリファレンスは [設定](configuration.md) を参照してください。

!!! warning "ネットワークの到達範囲が、あなたの唯一の読み取り境界です"

    thinkingface にはリポジトリ単位の公開設定がありません。インスタンス上のすべてのリポジトリは、
    認証の有無にかかわらず、そこに到達できる誰からでも読み取り・クローン・ダウンロードが可能です
    — アカウントと Organization のロールが制御するのは書き込みだけです。想定する利用者だけが
    到達できる場所にデプロイし、ネットワークの到達可能性こそが実質的なアクセス制御であると
    捉えてください。

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
| `web` | `frontend/` からビルド | Next.js の UI。ポート 3000 で `next start` により提供される |
| `api` | `backend/` からビルド | Go のバイナリ。HF 互換 REST API、git smart HTTP、LFS、Parquet ビューア、バックグラウンド同期ワーカーをすべて 1 プロセスに含み、ポート 8080 で待ち受ける |
| `postgres` | `postgres:17-alpine` | メタデータ用データベース（リポジトリ、ユーザー、トークン、ジョブ、実験の run） |
| `gcs` | `fsouza/fake-gcs-server` | 実際の GCS バケットの代わりとなるローカルエミュレータ |

起動後は次のようになります。

- Web UI: <http://localhost:3000>
- API: <http://localhost:8080>
- デフォルトのログイン: `admin` / `admin`（下記の警告を参照）

### データの永続化 { #data-persistence }

各ステートフルなサービスは、それぞれ名前付きの Docker ボリュームに書き込みます。

| ボリューム | 保持する内容 |
|---|---|
| `pg-data` | PostgreSQL のデータディレクトリ |
| `gcs-data` | fake-gcs-server のバックエンドファイルシステム（LFS オブジェクト、blob） |
| `git-data` | `GIT_ROOT`（`/data`）配下のベア git リポジトリ、および生成された SSH ホスト鍵 |

これらのボリュームは、コンテナの再起動や `docker compose down` をまたいでも維持されます。

### 停止とリセット { #stopping-and-resetting }

```bash
docker compose down    # stop and remove containers; volumes are kept
make clean              # docker compose down -v -- also removes the named volumes
```

`docker compose down`（または `make down`）はデータをそのまま残すので、その後 `docker compose up -d`
を実行すれば中断したところからそのまま再開します。データベースをまっさらにし、バケットを空にし、
リポジトリを一切ない状態から始めたい場合は `make clean`（あるいは直接 `docker compose down -v`）を
実行してください。これはコンテナとともに名前付きボリュームも削除します。

!!! warning "他の人に公開する前にデフォルト値を変更してください"
    デフォルトのまま `docker compose up` すると、よく知られたパスワード `admin` を持つ `admin`
    アカウントがシードされ、公開されている開発用のシークレットでセッションクッキーが署名されます。
    自分だけが到達できるラップトップ上であれば問題ありません。それ以外の誰かが到達できるネットワーク
    にこのインスタンスを置く前に、`.env` で `TF_ADMIN_PASSWORD` と `TF_SESSION_SECRET` を設定して
    ください — どちらも [設定](configuration.md) を参照してください。`TF_PUBLIC_URL` が
    `https://` の URL になっている場合、サーバーはこれらのデフォルト値のままでは一切起動を拒否します。

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

## GCP 上の本番環境 { #production-on-gcp }

`infra/` ディレクトリには GCP 本番デプロイ用の Terraform が入っています。これは次のものを
プロビジョニングします。

- `lfs/`、`blobs/`、（該当する場合）`wal/` 用の GCS バケット
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
設定するのは別の手順です（`gcloud run deploy` / `gcloud run jobs update`）。Terraform の状態に
実際のバックエンドを配線する方法を含む、apply からデプロイまでの完全な手順については、
リポジトリ内の `infra/README.md` を参照してください。

この Terraform ではプロビジョニングされないもの: カスタムドメインや TLS のフロントエンドです。
Cloud Run 自体が TLS を終端し、各サービスをそれぞれの `*.run.app` の URL で提供するため、
最初のうちはこれで十分です。ドメイン戦略を決めたら、ドメインマッピングやロードバランサを
追加してください。

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
