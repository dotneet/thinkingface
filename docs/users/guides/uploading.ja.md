# ファイルのアップロード

Python、CLI ツール、git、ブラウザ — thinkingface にファイルを取り込む方法はどれも、最後は
同じ場所にたどり着きます。git リポジトリのブランチ上のコミットであり、大きなファイルは
Git LFS オブジェクトとして保存されます。このページでは、それぞれの経路と、どれをいつ選ぶか、
そしてバイト列がサーバーに届いたあとに何が起きるかを説明します。

アクセストークンをまだ作成していない場合は、[クイックスタート](../getting-started.md)
から始めて、それから戻ってきてください。

## 経路を選ぶ { #choose-a-route }

| 経路 | 向いている用途 |
|---|---|
| Python からの `huggingface_hub` | すでに書いている学習スクリプトや前処理スクリプトからのアップロード |
| `hf` CLI | Hugging Face クライアントが導入済みで、シェルから 1 ファイルを送りたいとき |
| `tf` CLI | ディレクトリ全体を 1 コマンドでリポジトリとして登録したいとき |
| `git push` | クローンして作業するリポジトリ、および履歴を残したいすべての場合 |
| Web UI | ブラウザから離れずに、数個のファイルを追加する / テキストファイルを作成・編集する / 削除する |

いずれの方法でも **write** スコープを持つアクセストークンが必要です。スコープとロールが
どう関係するかは [認証](../reference/authentication.md) を参照してください。

## Hugging Face クライアントを設定する { #set-up-the-hugging-face-clients }

`huggingface_hub` と `datasets` は、手を加えなくても thinkingface と通信できます。変える
必要があるのは 3 つの環境変数だけです。

```bash
export HF_ENDPOINT=http://localhost:8080
export HF_TOKEN=tf_xxxxxxxxxxxx
export HF_HUB_DISABLE_XET=1
```

!!! warning "Python が `huggingface_hub` を import する前に `HF_ENDPOINT` を設定してください"

    `huggingface_hub` は、import 時に一度だけデフォルトのエンドポイントを解決します。上記の
    ようにシェルで環境変数を export するのが確実な方法です。どうしても Python の中から設定
    する必要がある場合は、プロセス内のどこであっても最初の `import huggingface_hub` より前に
    行うか、エンドポイントを明示的に渡してください: `HfApi(endpoint="http://localhost:8080",
    token="tf_xxxxxxxxxxxx")`。

### `HF_HUB_DISABLE_XET=1` が必要な理由 { #why-hf_hub_disable_xet1-is-required }

`huggingface_hub` 1.0 以降、クライアントは `hf_xet` パッケージがインストールされていれば、
大きなファイルの転送に Xet プロトコルを優先して使います。`hf_xet` はよくあるインストール
構成のいくつかで依存関係として入るため、意図せず導入されていることがあります。

thinkingface は大きなファイルを Git LFS で転送し、Xet は実装していません。クライアントが
分かりにくい箇所で失敗するのを避けるため、2 つの Xet エンドポイントは説明付きで `501` を
返します。

```text
thinkingface transfers large files over Git LFS, not Xet. Set HF_HUB_DISABLE_XET=1 in the
environment (or call thinkingface.login(), which sets it for you) and retry.
```

この環境変数を設定すると、`huggingface_hub` は代わりに LFS の経路を使います。コード側は
何も変える必要がなく、結果として得られるファイルも同一です。

### `thinkingface.login()` ヘルパー { #the-thinkingfacelogin-helper }

このリポジトリには、上記の環境変数を設定してくれる小さな Python パッケージが同梱されて
います。実験管理向けの trackio 互換クライアントも含まれています。

```bash
pip install -e clients/python
```

```python
import thinkingface

thinkingface.login("http://localhost:8080", token="tf_xxxxxxxxxxxx")
```

このヘルパーは現在のプロセスに `HF_ENDPOINT`、`HF_TOKEN`、`HF_HUB_DISABLE_XET` を設定し、
`huggingface_hub.login()` を呼び出します。これにより、`hf auth login` を実行したときと同じ
形でトークンがキャッシュされます。前述の import 時解決があるため、`huggingface_hub` を
import する前に呼び出すか、シェルの環境変数を使い続けたうえでこのヘルパーは補助として
扱ってください。

## 単一ファイルをアップロードする { #upload-a-single-file }

```python
from huggingface_hub import HfApi, upload_file

api = HfApi()
api.create_repo("admin/imdb-reviews", repo_type="dataset", exist_ok=True)

upload_file(
    path_or_fileobj="train.parquet",
    path_in_repo="data/train.parquet",
    repo_id="admin/imdb-reviews",
    repo_type="dataset",
    commit_message="Add training data",
)
```

`repo_type` のデフォルトは `model` なので、データセットの場合は毎回明示する必要があります。
新しいリポジトリはデフォルトブランチを `main` として作成され、`README.md` と
`.gitattributes` を含む最初のコミットが作られます。この 2 つ目のファイルの役割については
下の [LFS への振り分け](#how-files-are-routed-to-git-lfs) を参照してください。

!!! note "`private=True` は受け付けられますが、何の効果もありません"

    thinkingface にはリポジトリの公開設定がありません。インスタンス上のすべてのリポジトリは、
    匿名の呼び出し元も含め、そのサーバーに到達できる全員が読み取れます。権限が制御するのは
    *書き込み* です。これがデプロイの要件に合わない場合は、インスタンス全体をネットワーク
    境界の内側に置いてください。読み取り側への影響については
    [ファイルのダウンロード](downloading.md#who-can-read-what) で説明しています。

## フォルダをアップロードする { #upload-a-folder }

`upload_folder` はローカルディレクトリをたどり、その中身をすべてコミットします。

```python
from huggingface_hub import HfApi

api = HfApi()
api.create_repo("acme/sentiment-base", repo_type="model", exist_ok=True)

api.upload_folder(
    repo_id="acme/sentiment-base",
    folder_path="out/checkpoint",
    commit_message="Add fine-tuned checkpoint",
)
```

各ファイルは転送が始まる前にサーバー側で通常ファイルか LFS かに分類されるため、
`config.json` と `model.safetensors` が混在するフォルダでも特別な扱いは不要です。

## `hf` を使ってシェルからアップロードする { #upload-from-the-shell-with-hf }

`huggingface_hub` に同梱されている `hf` CLI も同じエンドポイントを使うため、環境変数を
export してあればそのまま動作します。

```bash
hf upload admin/imdb-reviews ./train.parquet data/train.parquet --repo-type dataset
```

引数は順に、リポジトリ、ローカルパス、リポジトリ内のパスです。Python の場合と同様に、
データセットには `--repo-type dataset` が必要です。

## `tf` でディレクトリ全体を push する { #push-a-whole-directory-with-tf }

`tf` は thinkingface 独自のクライアントで、「このディレクトリを登録する」という操作を
1 コマンドにまとめた単一の静的バイナリです。リポジトリが存在しなければ作成し、中身が
データセットかモデルかを推定し、ディレクトリ名をリポジトリ名にして、すべてを 1 つの
コミットとして push します。

```bash
tf login http://localhost:8080
tf up ./imdb-ja
```

既存のリポジトリへ push する場合は差分だけが送られます。`--dry-run` は何も変更せずに何が
起きるかを表示し、`--to` は送り先を上書きします。

```bash
tf up ./imdb-ja --to acme/imdb-reviews --dry-run
```

CI では `tf login` を省略し、代わりに環境変数で認証情報を渡してください。

```bash
export THINKINGFACE_ENDPOINT=http://localhost:8080
export THINKINGFACE_API_KEY=tf_xxxxxxxxxxxx
tf up ./imdb-ja
```

すべてのコマンド、フラグ、認証情報の解決ルールは
[tf CLI リファレンス](../reference/tf-cli.md) にまとまっています。

## git で push する { #push-with-git }

リポジトリは通常の git リモートです。クローンして、コミットして、push できます。

```bash
git clone http://localhost:8080/datasets/admin/imdb-reviews.git
cd imdb-ja
git add data/train.parquet
git commit -m "Add training data"
git push origin main
```

この経路だけは、何を LFS に送るかを決めるのがサーバーではなく *あなたの* ローカルの
`.gitattributes` と `git lfs track` の設定です。認証情報、LFS のセットアップ、SSH、ブランチに
ついては [Git を使う](git.md) で詳しく説明しています。

## ブラウザからファイルをアップロードする { #upload-files-from-the-browser }

リポジトリの **Files** タブを開き、**Add file → Upload files** を選びます。ダイアログに
ファイルをドロップする（またはクリックして選ぶ）、コミットメッセージを書く、**Upload** を
押す、それだけです。1 つのダイアログで選んだものはすべて **1 つのコミット** になり、表示中の
ディレクトリに配置されます。

- ファイルは他の経路とまったく同じ `.gitattributes` のルールで Git LFS に振り分けられます
  （[後述](#how-files-are-routed-to-git-lfs)）。事前に `git lfs track` を実行する必要も、何かを
  インストールする必要もありません — 振り分けとオブジェクトのアップロードはサーバーが行います。
- 送信済みの量はプログレスバーで表示されるので、大きなファイルでもダイアログが固まったように
  見えることはありません。
- 1 回のアップロードあたり **最大 64 ファイル**、**1 ファイルあたり最大 10GB** です。これを
  超える場合は、並列転送と再開ができる `git push` や `huggingface_hub` を使ってください。
- **Add file → Create a new file** はパスを尋ね、そのパスで空のドキュメントとしてブラウザの
  エディタを開きます。まだ空のリポジトリに `README.md` を書き始めるにはこれを使います。

このメニューは、書き込み権限を持ってログインしていて、かつブランチを表示しているときにだけ
表示され、アーカイブ済みのリポジトリでは表示されません。画面については
[Web UI を使う](web-ui.md) を参照してください。

## ブラウザでファイルを削除する { #delete-a-file-in-the-browser }

ファイルを開いて **Delete** を押し、確認します。そのファイルを削除する専用のコミットが作られ
ます。それ以前のコミットにはファイルが残るので、履歴から消えるものはありません。

編集はできない LFS 管理下のファイルも、この方法なら削除できます。削除するとツリーからポインタ
が取り除かれますが、保存されているオブジェクト自体は、どこからも参照されなくなり GC が回収する
までバケットに残ります。つまり、削除した時点で容量が空くわけではありません。

## ブラウザでファイルを編集する { #edit-a-file-in-the-browser }

リポジトリにすでに存在する Markdown やテキストファイルは、リポジトリページから編集して
コミットできます。README を直すにはこれが最速です。LFS 管理下のファイルはこの方法では編集
できず、編集対象はコミット SHA ではなくブランチである必要があります。まだ存在しないファイルを
作るには、上記の **Add file → Create a new file** を使います。
[Web UI を使う](web-ui.md) を参照してください。

## ファイルが Git LFS に振り分けられる仕組み { #how-files-are-routed-to-git-lfs }

ファイルの中身が git のオブジェクトデータベースに入るのか、オブジェクトストレージ上の
[LFS オブジェクト](../concepts.md) になるのかは、2 つのルールで決まります。

1. **`.gitattributes` のパターンが優先されます。** すべてのリポジトリは、常にサイズが大きい
   形式を網羅したデフォルトの `.gitattributes` とともに作成されるため、最初のアップロード
   から LFS への振り分けが機能します。

    ```text
    *.safetensors filter=lfs diff=lfs merge=lfs -text
    *.parquet filter=lfs diff=lfs merge=lfs -text
    *.bin filter=lfs diff=lfs merge=lfs -text
    *.gguf filter=lfs diff=lfs merge=lfs -text
    *.pt filter=lfs diff=lfs merge=lfs -text
    *.onnx filter=lfs diff=lfs merge=lfs -text
    ```

    これは抜粋です。実際には `*.ckpt`、`*.h5`、`*.npy`、`*.npz`、`*.tar.*`、`*.zip`、`*.zst`、
    `*tfevents*` を含む 30 数個のパターンが初期設定されています。完全な一覧はリポジトリ自身の
    `.gitattributes` を読んでください。独自のパターンを追加したい場合は、他のファイルと同じ
    ように編集できます。git とまったく同じく、後の行が前の行より優先されます。

    **データセットリポジトリにはさらに多くのパターンが入ります。** メディアファイルは
    データセットにとっての中身そのものなので、データセットには音声（`*.wav`、`*.flac`、
    `*.mp3`、`*.ogg`、`*.aac`、`*.pcm` など）、画像（`*.png`、`*.jpg`、`*.jpeg`、`*.webp`、
    `*.gif`、`*.bmp`、`*.tiff`）、動画（`*.mp4`、`*.mov`、`*.webm`、`*.mkv`、`*.avi`）、
    パック済みデータセット（`*.db`、`*.duckdb`、`*.sqlite`、`*.lz4`、`*.mds`）のパターンも
    初期設定されます。これらは個々のファイルがどれほど小さくても LFS に振り分けられます。
    モデルリポジトリにはこれらは入らないため、モデルカードに貼ったスクリーンショットは通常の
    git blob のままになります。

    パターンには `**` を使ってディレクトリをまたぐ指定（`data/**/*.bin`）もでき、API 経由の
    アップロードでも `git push` でも同じように解釈されます。

2. **どのパターンにも一致しない場合、10 MiB 以上のファイルは無条件で LFS に送られます。**
   これにより、何をコミットしても bare リポジトリが安価にクローンできるサイズに保たれます。

なお、`-filter=lfs` に一致するルールがある場合は、サイズにかかわらずそのファイルは通常の
git blob のままになります。

これらのルールを誰が適用するかは経路によって異なります。

| 経路 | 判定する主体 |
|---|---|
| `huggingface_hub`、`hf`、`tf` | サーバー。対象リビジョンの `.gitattributes` と 10 MiB のしきい値による |
| `git push` | ローカルの git-lfs。作業ツリーの `.gitattributes` による |

いずれの場合も、本番デプロイでは LFS オブジェクトの中身が API サーバーを経由することは
ありません。クライアントは署名付き URL を受け取り、バケットと直接やり取りします。

## アップロード後に何が起きるか { #what-happens-after-an-upload }

アップロードで話が終わるわけではありません。ブランチが更新されると、バックグラウンド
ワーカーがその push を拾い、対象のリビジョンについて次の処理を行います。

1. LFS 以外のすべてのファイルをオブジェクトストレージの `blobs/{sha}` に公開します
   （LFS オブジェクトはすでにそこにあります。クライアントがアップロードした先だからです）。
2. ファイルインデックスを作り直します。ファイルツリー、サイズの合計、`gcloud storage cp`
   スクリプトは、いずれもこのインデックスから生成されます。
3. すべての `.parquet` ファイルのフッターを読み、スキーマと行数を記録します。これにより
   Parquet ファイルが [データセットビューア](dataset-viewer.md) で閲覧できるようになります。
4. デフォルトブランチについて、`README.md` の YAML フロントマターを解析してリポジトリ
   カード（ライセンス、タグ、`lineage:` のエッジ）を作ります。
5. リポジトリが実験のメトリクスを持っている場合は、run インデックスを更新します。

通常はこれが push から 1 秒程度で完了します。Parquet ファイルがまだ閲覧できない場合や、
「GCS アクセス」のスクリプトが想定より少ないファイル数を返す場合は、ページを再読み込みして
みてください。インデックス作成がまだ進行中である可能性が高いです。

!!! note "インデックスの対象はブランチへの push だけです"

    ワーカーはブランチの先端が動いたことをきっかけに起動します。タグだけを push しても
    インデックス作成は予約されないため、タグとしてしか存在しないリビジョンは git や
    resolve URL からはダウンロードできても、バケットアクセス用スクリプトの元になる
    ファイルインデックスには現れないことがあります。

## アップロードが拒否されたとき { #when-an-upload-is-refused }

| レスポンス | 意味 |
|---|---|
| `401 authentication required to write to {repo}` | 認証情報がサーバーに届いていません。`HF_TOKEN` か git のクレデンシャルヘルパーを確認してください |
| `403 this token is read-only` | トークンのスコープが `read` です。`write` のトークンを発行してください |
| `403 you do not have write access to {repo}` | トークンは有効ですが、そのネームスペースでアカウントに書き込みロールがありません。Organization の場合は管理者にロールの引き上げを依頼してください — [Organization](organizations.md) を参照 |
| `403 {repo} is archived and read-only` | 先にリポジトリ設定でアーカイブを解除してください |
| `501 xet_not_supported` | `HF_HUB_DISABLE_XET=1` が設定されていません — [上記](#why-hf_hub_disable_xet1-is-required) を参照 |
| `400 commits must target a branch, not a commit SHA` | アップロードがリビジョンとしてコミットを指定しています。ブランチに push してください |
| `413 {file} is too large` | ブラウザアップロードの 1 ファイルあたりの上限を超えています。そのファイルには `git push` や `huggingface_hub` を使ってください |
| `400 at most 64 files can be uploaded in one request` | ブラウザからのアップロードを分割するか、フォルダ全体を扱える経路を使ってください |
| `400 invalid upload path …` | パスがリポジトリのルートの外や `.git` の下を指しています。通常のパスにアップロードしてください |

## 次のステップ { #next-steps }

- [ファイルのダウンロード](downloading.md) — このページの内容の取得側。
- [Git を使う](git.md) — クローン、LFS、SSH、ブランチとタグ。
- [データセットの閲覧](dataset-viewer.md) — push した Parquet ファイルをブラウザでどう扱うか。
