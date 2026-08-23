# クイックスタート

このページでは、何もない状態から、データセットが 1 つ入ったインスタンスを動かすところまでを行い
ます。Web UI で表示でき、Python から読み込める状態がゴールです。所要時間は 15 分ほどで、その大半
は最初のコンテナビルドの待ち時間です。

必要なものは Compose プラグイン入りの Docker と、アップロードの手順を Python でたどる場合は
Python 3.9 以降です。

## 1. サーバーを起動する { #1-start-the-server }

compose スタックは API と web のイメージをソースからビルドするため、リポジトリをチェックアウトし
たところから始めます。

```bash
git clone https://github.com/dotneet/thinkingface.git
cd thinkingface
cp .env.example .env
docker compose up -d
```

これで 4 つのサービスが起動します。Web UI、API、メタデータ用の PostgreSQL、そして実際のバケットの
代わりとなるローカルの Google Cloud Storage エミュレータです。初回はイメージのビルドが走るため数
分かかりますが、次回以降は数秒で起動します。

| 対象 | URL |
|---|---|
| Web UI | <http://localhost:3000> |
| API エンドポイント | <http://localhost:8080> |
| 初期ログイン | `admin` / `admin` |

起動の様子は `docker compose logs -f` で確認できます。停止するときは `docker compose down` です
（データは名前付きボリュームに残ります）。

!!! warning "他の人から届く場所に置く前に、デフォルト値を変更してください"

    `.env` の `TF_ADMIN_PASSWORD` と `TF_SESSION_SECRET` には開発用の値が入っています。手元の
    マシンなら問題ありませんが、それ以外の場所では許容できません。`https://` 経由の場合、これらを
    変更するまでサーバーは起動を拒否します。[設定](self-hosting/configuration.md) を参照してくだ
    さい。

## 2. アクセストークンを作る { #2-create-an-access-token }

<http://localhost:3000> を開き、`admin` でログインします。**Settings → Access tokens**
(<http://localhost:3000/settings/tokens>) に移動し、トークンに名前を付け、**Write** スコープを選ん
で作成します。

値は `tf_xxxxxxxxxxxx` のような文字列です。表示されるのはこの一度きりで二度と表示されないので、
その場でコピーしてください。

必要な資格情報はこのトークン 1 つだけです。HTTP 経由の git のパスワードであり、API の
`Authorization: Bearer` の値であり、Python クライアントの `HF_TOKEN` でもあります。詳しくは
[認証](reference/authentication.md) を参照してください。

## 3. データセットをアップロードする { #3-upload-a-dataset }

Hugging Face のクライアントライブラリをインストールし、自分のインスタンスに向けます。

```bash
pip install huggingface_hub datasets
export HF_ENDPOINT=http://localhost:8080
export HF_TOKEN=tf_xxxxxxxxxxxx
export HF_HUB_DISABLE_XET=1
```

手元に適当なデータセットがなければ、アップロード用に小さな Parquet ファイルを作ります。

```python
import pyarrow as pa
import pyarrow.parquet as pq

table = pa.table({
    "text": ["この映画は最高だった", "退屈で途中で寝てしまった", "また観たい"],
    "label": [1, 0, 1],
})
pq.write_table(table, "train.parquet")
```

続いてリポジトリを作成し、ファイルを push します。これはごく普通の `huggingface_hub` のコードで、
変わっているのは向き先だけです。

```python
from huggingface_hub import HfApi, upload_file

api = HfApi()
api.create_repo("admin/imdb-reviews", repo_type="dataset", exist_ok=True)

upload_file(
    path_or_fileobj="train.parquet",
    path_in_repo="data/train.parquet",
    repo_id="admin/imdb-reviews",
    repo_type="dataset",
)
```

`*.parquet` は、サーバーが新しいリポジトリすべてに書き込む `.gitattributes` によって Git LFS 経由
になります。そのためファイルの実体はオブジェクトストレージに送られ、git にはポインタが記録されま
す。これを実現するために設定すべきことは何もありません。

!!! note "なぜ `HF_HUB_DISABLE_XET=1` が必要なのか"

    `huggingface_hub` 1.0 以降では、`hf_xet` パッケージが入っていると大きなファイルは Xet プロト
    コルで転送されます。thinkingface の転送方式は Git LFS で Xet には対応していないため、無効にす
    る必要があります。忘れた場合は、サーバーがまさにそのことを伝えるエラーを返します。

## 4. Web UI で確認する { #4-look-at-it-in-the-web-ui }

<http://localhost:3000/datasets/admin/imdb-reviews> を開きます。最初に表示されるのはリポジトリの
**Card**（レンダリングされた `README.md`）で、サイズ・ファイル数・ライセンス・ブランチがサイド
バーに並びます。**Files** に切り替えると、`LFS` バッジの付いた `data/train.parquet` が見えます。
これを開いて **Open in viewer** を選ぶと、ダウンロードせずに行とスキーマをテーブルとして閲覧でき
ます。

ついでに触ってみる価値のある機能が 2 つあります。

- リポジトリページの **GCS access** は、このリビジョンを指定した場所に展開する
  `gcloud storage cp` スクリプトを生成します。[ファイルのダウンロード](guides/downloading.md) を
  参照してください。
- ファイル一覧の上にある **History** には、今回のアップロードが作ったコミットが表示されます。
  thinkingface の書き込み経路は git ただ 1 つなので、Python・CLI・git・ブラウザのいずれからの
  アップロードも、同じようにここに現れます。

## 5. 読み戻す { #5-read-it-back }

ダウンロード側も対称的です。同じ環境変数、同じ関数で済みます。

```python
from huggingface_hub import hf_hub_download
from datasets import load_dataset

path = hf_hub_download(
    repo_id="admin/imdb-reviews", repo_type="dataset", filename="data/train.parquet"
)
ds = load_dataset("parquet", data_files=path)
```

このファイルは `data/` の下にあり `train` という接頭辞が付いているので、`datasets` は split の
レイアウトも認識します。そのため `load_dataset("admin/imdb-reviews")` でリポジトリを直接解決でき
ます。

## その他のアップロード方法 { #other-ways-to-upload }

Python だけが手段ではありませんし、ディレクトリ単位で扱うなら最短の方法であることはまずありませ
ん。

- **`tf up ./imdb-ja`** はディレクトリ全体をコマンド 1 つで登録します。リポジトリの作成、データ
  セットかモデルかの推定、そして全体を 1 コミットとして push するところまで行います。
  [tf CLI リファレンス](reference/tf-cli.md) を参照してください。
- **`git clone` / `git push`** は期待どおりに動きます。HTTP でも SSH でも使え、大きなファイルは
  Git LFS が扱います。[Git を使う](guides/git.md) を参照してください。
- **Web UI** では <http://localhost:3000/new> でリポジトリを作成でき、Markdown やテキストファイル
  をブラウザ上で編集できます。

いずれの方法も [ファイルのアップロード](guides/uploading.md) で横並びに解説しています。

## 次に読むもの { #where-to-go-next }

- [基本コンセプト](concepts.md) — リポジトリ、リビジョン、実データが実際にどう保存されるか、そし
  てそのレイアウトによってなぜバケットを直接読めるのか。
- [データセットの閲覧](guides/dataset-viewer.md) と [モデルの確認](guides/model-checkpoints.md)
  — 何もダウンロードせずにブラウザで確認できること。
- [実験のトラッキング](guides/experiments.md) — 学習スクリプトを自分のインスタンスに向けて、メト
  リクスのグラフが埋まっていく様子を眺めます。
- [Organization](guides/organizations.md) — `admin` ネームスペースから、チームで共有するネーム
  スペースへ。
- [デプロイ](self-hosting/deployment.md) — 手元のインスタンスを本物にするとき。
