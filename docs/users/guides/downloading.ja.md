# ファイルのダウンロード

thinkingface からファイルを取り出す方法には、根本的に異なる 2 つの経路があります。
`huggingface_hub`、`datasets`、git、素の HTTP がいずれも使っているサーバー経由の経路と、
サーバーをまったく介さずオブジェクトストアから直接取り出す経路です。このページではその
両方と、後者がひと手間かける価値のある場面を説明します。

## 経路を選ぶ { #choose-a-route }

| 経路 | 向いている用途 |
|---|---|
| `hf_hub_download` | パスの分かっているファイルを 1 つ、ローカルキャッシュ付きで取得する |
| `snapshot_download` | あるリビジョンの全ファイルをローカルディレクトリに取得する |
| `datasets.load_dataset` | データセットリポジトリを `Dataset` オブジェクトとして読む |
| `resolve` URL | `curl`、`wget`、その他 HTTP を話すあらゆるもの |
| `git clone` | 履歴すべてと、コミットできる作業ツリー |
| 生成される `gcloud storage cp` スクリプト | 大量の復元、およびサーバーを経由せずにバケット間でコピーする場合 |

## Hugging Face クライアントを設定する { #set-up-the-hugging-face-clients }

アップロード側と同じ 3 つの環境変数です。

```bash
export HF_ENDPOINT=http://localhost:8080
export HF_TOKEN=tf_xxxxxxxxxxxx
export HF_HUB_DISABLE_XET=1
```

これらは Python を起動する前に export してください。`huggingface_hub` は import 時に一度
だけデフォルトのエンドポイントを解決します。詳細と `thinkingface.login()` ヘルパーについては
[ファイルのアップロード](uploading.md#set-up-the-hugging-face-clients) を参照してください。

## 1 つのファイルをダウンロードする { #download-one-file }

```python
from huggingface_hub import hf_hub_download

path = hf_hub_download(
    repo_id="admin/imdb-reviews",
    repo_type="dataset",
    filename="data/train.parquet",
)
```

戻り値はローカルの `huggingface_hub` キャッシュ内のパスです。そのため、変更のないファイルに
対して再度呼び出してもリクエスト 1 回で済み、転送は発生しません。リビジョンを固定するには
`revision=` を使います。ブランチ名、タグ、コミット SHA のいずれも指定できます。

```python
path = hf_hub_download(
    repo_id="acme/sentiment-base",
    filename="model.safetensors",
    revision="v1.0",
)
```

## リビジョン全体をダウンロードする { #download-a-whole-revision }

```python
from huggingface_hub import snapshot_download

local_dir = snapshot_download(repo_id="admin/imdb-reviews", repo_type="dataset")
```

`snapshot_download` は全パスのメタデータをまとめて 1 回でサーバーに問い合わせてから
ファイルを取得するため、自分で `hf_hub_download` をループさせるよりもかなり効率的です。
取得対象を絞り込むには `allow_patterns` / `ignore_patterns` を使います。

## データセットを読み込む { #load-a-dataset }

`datasets` はコードを変更せずに thinkingface に対して動作します。

```python
from datasets import load_dataset

ds = load_dataset("admin/imdb-reviews")
```

分割（split）の検出も通常どおり動作します。ファイルが `data/train-*.parquet` と
`data/test-*.parquet` のように配置されているリポジトリは、追加の設定なしに `train` と
`test` の split として解決されます。

明示的に指定したい場合、あるいはリポジトリが `datasets` の認識するレイアウトに従っていない
場合は、先にファイルをダウンロードしてパスから読み込んでください。

```python
from datasets import load_dataset
from huggingface_hub import hf_hub_download

path = hf_hub_download(
    repo_id="admin/imdb-reviews", repo_type="dataset", filename="data/train.parquet"
)
ds = load_dataset("parquet", data_files=path)
```

## 素の HTTP でダウンロードする { #download-over-plain-http }

すべてのファイルは `resolve` URL から取得できます。これは Hugging Face Hub と同じ URL の形
です。データセットには `/datasets` プレフィックスが付き、モデルはルート直下に配置されます。

```text
http://localhost:8080/datasets/{namespace}/{name}/resolve/{revision}/{path}
http://localhost:8080/{namespace}/{name}/resolve/{revision}/{path}
```

```bash
curl -L -H "Authorization: Bearer tf_xxxxxxxxxxxx" \
  -o train.parquet \
  http://localhost:8080/datasets/admin/imdb-reviews/resolve/main/data/train.parquet
```

これらの URL について知っておくべきことが 3 つあります。

- **リダイレクトをたどってください。** 通常のファイルは git から直接ストリーミングされます。
  LFS ファイルはバケット上のオブジェクトへの有効期限付き署名付き URL にリダイレクトされる
  ため、転送が API サーバーを通ることはありません。（URL に署名できないローカルの
  ストレージエミュレータに対しては、代わりにサーバーがオブジェクトを中継します。）
  `curl -L` はどちらのケースにも対応します。
- **`HEAD` が使えます。** しかも安価で、何も転送せずにサイズとオブジェクトの識別子を返します。
- **すべて添付ファイルとして配信されます。** `Content-Disposition: attachment` と
  `X-Content-Type-Options: nosniff` が付きます。リポジトリに push した `.html` ファイルは、
  意図的にレンダリングされずダウンロードされます。

## バケットから直接リビジョンを復元する { #restore-a-revision-straight-from-the-bucket }

バケット内のオブジェクトは **内容アドレス方式** で保存されます。LFS オブジェクトは
`lfs/{oid}`、通常のファイルは `blobs/{sha}` です。バケット内にネームスペース、リポジトリ、
パスにちなんだ名前を持つものは何もないため、`cp -r` できるディレクトリツリーは存在しません。
名前を復元するのは、サーバーが要求に応じて生成するスクリプトです。このスクリプトが、内容
アドレスで保存された各オブジェクトを、そのリビジョンにおけるパスに対応付けます。

次のような場合に使ってください。

- 大きなリビジョンを復元するにあたって、バイト列を API サーバーに一切通したくないとき。
- 送り先が別のバケットであるとき。`DEST` に `gs://` プレフィックスを渡すと、ダウンロード
  ではなくサーバーサイドのバケット間コピーになります。
- 再現可能なものを同僚に渡したいとき。スクリプトは決定的であり、その元になるファイル一覧は
  パス順にソートされています。

### Web UI から { #from-the-web-ui }

リポジトリページを開き、**GCS access** を選びます。ダイアログにはそのリビジョンのファイル数
と合計サイズが表示され、コピー可能な `gcloud storage script` が用意されています。リビジョン
に Parquet ファイルが含まれる場合は、対応する `read_parquet()` クエリを収めた **DuckDB**
タブも表示されます。ファイルブラウザ内の個々のファイルにも **GCS access** アクションがあり、
1 ファイル分の `gcloud storage cp` コマンドをコピーできます。

### API から { #from-the-api }

```bash
curl -s http://localhost:8080/api/v1/repos/dataset/admin/imdb-reviews/gcs/main \
  | jq -r .gcloud_script | DEST=./imdb-ja sh
```

レスポンスにはファイル一覧（`files`。各要素が `gs://` URI、サイズ、`lfs` フラグを持ちます）と
DuckDB 用のスニペット（`duckdb_snippet`）も含まれるため、スクリプトをそのまま実行するのでは
なく、これらを使って独自のツールを組むこともできます。

スクリプト自体は次のような内容です。

```bash
#!/bin/sh
# thinkingface: datasets/admin/imdb-reviews @ main -- 3 files, 536871936 bytes
# Objects are content-addressed; this script lays them out under DEST.
# DEST may be a local directory or a gs:// prefix.
set -eu
DEST="${DEST:-./imdb-ja}"
cp_one() {
  case "$DEST" in gs://*) ;; *) mkdir -p "$(dirname "$2")" ;; esac
  gcloud storage cp "$1" "$2"
}
cp_one 'gs://my-bucket/blobs/3f/2a/3f2a9c…' "$DEST"/'README.md'
cp_one 'gs://my-bucket/lfs/9b/1d/9b1de4…' "$DEST"/'data/train.parquet'
```

`DEST` のデフォルトはリポジトリ名を付けたディレクトリです。スクリプト内の `gs://` キーは
すべて内容アドレスなので、同じキーをオブジェクトストレージから読む他のもの — DuckDB の
`read_parquet()`、BigQuery の外部テーブル、別マシン上の学習ジョブなど — にそのまま渡せます。

### 2 つの注意点 { #two-caveats }

- **スクリプトが列挙するのは、git が知っている内容ではなくインデックス済みの内容です。**
  これは push のたびにバックグラウンドワーカーが作り直すファイルインデックスから生成される
  ため、たった今 push されたリビジョンや、タグとしてしか存在しない（インデックス作成が
  予約されない）リビジョンは、エラーではなく空のファイル一覧が返ることがあります。少し待って
  から再度取得してください。
- **バケットへのアクセスは thinkingface へのアクセスとは別物です。** スクリプトの生成は
  API の通常の読み取りですが、*実行* にはバケット自体の認証情報が必要で、これは
  thinkingface が発行するものではありません。ローカルの compose 環境では、`gcloud` CLI を
  ストレージエミュレータに向けることになります。

    ```bash
    gcloud config set api_endpoint_overrides/storage http://localhost:4443/storage/v1/
    ```

## リポジトリをクローンする { #clone-the-repository }

履歴付きの作業ツリーが欲しい場合はクローンしてください。HTTP または SSH 経由の
`git clone` を使い、大きなファイルの実体はチェックアウト時に Git LFS が取得します。
[Git を使う](git.md) を参照してください。

## 誰が何を読めるか { #who-can-read-what }

thinkingface には **リポジトリの公開設定がありません**。リポジトリに private / public の
フラグはなく、リポジトリ単位の読み取り権限もありません。インスタンス上のすべてのリポジトリ
は、認証情報をまったく提示しない呼び出し元も含め、サーバーに到達できる全員が読み取れます。
`create_repo` に渡す `private=True` はクライアント互換性のために受け付けられますが、何も
しません。

権限システムが制御するのは書き込みです。インスタンスそのものをセキュリティ境界とみなし、
中身が機微なものであればネットワークの内側に置いてください。

| 経路 | 必要な認証情報 |
|---|---|
| `hf_hub_download`、`snapshot_download`、`load_dataset` | 読み取りには不要。ただし同じスクリプトで書き込みもできるよう `HF_TOKEN` は設定しておく価値があります |
| `resolve` URL | 読み取りには不要。`Authorization: Bearer tf_...` は受け付けられます |
| HTTP 経由の `git clone` | 読み取りには不要。push にはトークンが必要 |
| SSH 経由の `git clone` | 常に登録済みの SSH 鍵が必要 — SSH トランスポートはすべての接続を認証します |
| `gcloud storage cp` スクリプト | 生成には不要。実行にはバケット自体の認証情報が必要 |

トークン、スコープ、ロールについては [認証](../reference/authentication.md) を参照して
ください。

## 次のステップ { #next-steps }

- [Git を使う](git.md) — クローン、LFS、リビジョンの詳細。
- [データセットの閲覧](dataset-viewer.md) — Parquet ファイルをダウンロードせずに読む。
- [基本コンセプト](../concepts.md) — バケットが内容アドレス方式で配置されている理由。
