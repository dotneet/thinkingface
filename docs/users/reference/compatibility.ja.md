# 互換性

thinkingface は、Hugging Face エコシステムのツールがすでに話しているのと同じプロトコルを公開して
います。よくある用途に thinkingface 専用のフォークしたクライアントや SDK は必要ありません。このペー
ジは、実際に動作すると確認済みの範囲を、ライブインスタンスに対して実行される `e2e/` の pytest スイー
トを根拠として一覧にし、thinkingface が huggingface.co とどこで異なるかを率直に示します。

## `huggingface_hub` { #huggingface_hub }

`HF_ENDPOINT` を自分のインスタンスに向け、`HF_TOKEN` にアクセストークンを設定すれば、`huggingface.co`
に対するのとまったく同じように `HfApi`（またはそれに相当するトップレベル関数）を使えます。

```bash
export HF_ENDPOINT=http://localhost:8080
export HF_TOKEN=tf_xxxxxxxxxxxx
export HF_HUB_DISABLE_XET=1
```

| Operation | Verified |
|---|---|
| `whoami()` | Yes |
| `create_repo()` | Yes（`model` と `dataset` の両方） |
| `upload_file()` | Yes。プレーンなファイルと、Git LFS 経由になるファイル（例: `*.parquet`、`*.safetensors`）の両方 |
| `list_repo_files()` | Yes |
| `list_repo_tree()` | Yes。`recursive=True` によるディレクトリエントリの取得を含む |
| `hf_hub_download()` | Yes |
| `repo_info()` | Yes。`delete_repo()` 後は 404 相当のエラーを返す |
| `delete_repo()` | Yes |
| `list_organization_members()` | Yes |
| `get_user_overview()` / `get_organization_overview()` | Yes。それぞれ、もう一方の種類のネームスペースに対しては 404 になる |

`upload_folder()` と `snapshot_download()` はテストスイートで検証されていないため、推測でこの表に含
めることはせず、あえて除外しています。どちらかに依存する場合は、利用前に自分のインスタンスで動作を確
認してください。

## `datasets` { #datasets }

`hf_hub_download()` でダウンロードしたファイルに対する
`datasets.load_dataset("parquet", data_files=...)` は検証済みです。リポジトリ ID を直接指定して読み
込む場合（`load_dataset("admin/imdb-reviews")`）も、`datasets` 自身がその処理で `huggingface_hub` を
呼び出しているため、`huggingface_hub` によるリゾルブと同じ仕組みで動作します。

## git と Git LFS { #git-and-git-lfs }

`git clone` と `git push` は、HTTP と、インスタンスの運用者が有効化していれば SSH の両方で動作しま
す。LFS オブジェクトも含め、チェックアウト時にはポインタではなく実ファイルとして転送されます。手順の
解説は [Git を使う](../guides/git.md) を、SSH 鍵の設定は
[認証](authentication.md#ssh-keys) を参照してください。

**サポートされるのは git のスマート HTTP プロトコルのみです。** ダム HTTP プロトコルにフォールバック
するクライアントには、わかりにくい失敗ではなく、最新の git クライアントを使うよう促す明確なエラーが
返されます。

## `gcloud storage` / 素の GCS アクセス { #gcloud-storage-plain-gcs-access }

プッシュ後、ファイルは内容アドレス方式のオブジェクトストアに公開されます。非 LFS の blob は
`blobs/{sha}`（git の blob SHA-1 をキーとする）に、LFS オブジェクトは `lfs/{oid}`（SHA-256 のコンテン
ツハッシュをキーとする）に置かれ、どちらもリポジトリをまたいで重複排除されます。そのため、バケット内
のオブジェクトはどれもネームスペースやパスにちなんだ名前を持ちません。リポジトリページのサイドバーに
ある **GCS access** ダイアログでは、リポジトリのパスからこれらのキーへのマッピングを、そのまま実行で
きる `gcloud storage cp` スクリプトとして生成できます（`.parquet` ファイル向けの DuckDB
`read_parquet()` スニペットも生成されます）。これにより、API を一切経由せずにストレージから直接デー
タを取得できます。

## trackio { #trackio }

`thinkingface.trackio` シムは、`trackio` の `init` / `log` / `finish` API のドロップイン代替です。
run、config、メトリクスは、データセットリポジトリにバックアップされる Parquet ファイル
（`{project}/metrics.parquet` + `{project}/aux/configs.parquet`）と、run が稼働中であればネイティブ
の ingest API の両方から抽出されます。そのため、フラッシュを待たずとも run 一覧やチャートにメトリク
スが反映されます。詳細は [実験のトラッキング](../guides/experiments.md) を参照してください。

## 既知の非互換性と制限事項 { #known-incompatibilities-and-limitations }

**プライベートリポジトリは存在しません。** thinkingface にはリポジトリ単位の可視性設定が一切なく、イ
ンスタンスに到達できる人であれば、サインインの有無にかかわらず、すべてのリポジトリを読み取り・クロー
ン・ダウンロードできます。`create_repo(..., private=True)` や `visibility="private"` は、既存の
`huggingface_hub` のコードがそのまま動作するように受け付けられますが、どちらも作成されるものを変える
ことはなく、リポジトリの一覧は常に `private: false` を返します。トークンと Organization のロールが制
御するのは**書き込み**のみです。これを前提に計画してください。インスタンスを取り巻くネットワーク境界
だけが、あなたが持つ唯一の読み取り境界です。

**Xet はサポートされていません。** `huggingface_hub` 1.0 以降、`hf_xet` パッケージがインストールされ
ていると、大きいファイルの転送では Xet プロトコルが優先的に使われます。thinkingface は大きいファイル
を Git LFS のみで転送し、Xet 用のエンドポイントは、わかりにくい失敗の代わりに、次に何をすべきかを伝え
る明示的な `501` を返します。thinkingface インスタンスに対して `huggingface_hub` を使う前に、環境変数
`HF_HUB_DISABLE_XET=1` を設定してください（`thinkingface` Python パッケージの `login()` ヘルパーはこ
れを自動的に設定します）。

**SQLite モードは PostgreSQL より検索セマンティクスが狭くなります。** インスタンスは PostgreSQL と
SQLite のどちらでも動作します（`DATABASE_URL` のスキームでどちらを使うか選択します）。SQLite の場合:

- サポートされるのは単一プロセス・単一ライターの接続のみです。複数レプリカからの同時書き込みはサポー
  トされません。
- HF 互換の `search=` による部分一致（PostgreSQL では `ILIKE`）は単純な `LIKE` になり、ASCII 文字に
  限って大文字小文字を区別しません。Unicode のケースフォールディングは行われません。
- Web UI の全文検索は、PostgreSQL の `tsvector` ではなく SQLite の FTS5（`unicode61` トークナイザー）
  上で動作し、同一の結果にはなりません（たとえば、言語固有のステミングが行われません）。

これらはいずれもプロトコルの互換性には影響しません。クライアント側からは、話している相手のインスタン
スがどちらのデータベースを使っているか区別できません。ただし、検索結果や同時書き込み時の挙動は、同じ
バージョンの 2 つのインスタンス間でも異なることがあります。

## 関連ページ { #see-also }

- [tf CLI](tf-cli.md) — ここで説明した API を土台にした、thinkingface 専用のクライアント
- [認証](authentication.md) — トークン、SSH 鍵、そして各プロトコルがそれらをどう使うか
