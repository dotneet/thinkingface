# Git を使う

thinkingface インスタンス上のすべてのリポジトリは、本物の git リポジトリです — サーバー上のベアリポジトリであり、大きなファイルには Git LFS を使います。特別なことは何もなく、`git clone`、`git push`、ブランチやタグはすべて期待どおりに動作します。このページでは、URL、認証情報、LFS のセットアップ、そして知っておくべきいくつかの制限について説明します。

## HTTP でクローンする { #clone-over-http }

データセットの URL には `/datasets` プレフィックスが付き、モデルはルートに置かれます:

```bash
git clone http://localhost:8080/datasets/admin/imdb-reviews.git
git clone http://localhost:8080/admin/sentiment-base.git
```

モデルは明示的な `/models/{namespace}/{name}.git` でも応答するので、ツールがリポジトリの種類を曖昧にしたくない場合に便利です。`.git` サフィックスはどこでも省略可能です。

読み取りはオープンです — クローンには認証情報が一切不要です。プッシュには必要です。

### 認証情報 { #credentials }

HTTP 経由の認証は Basic 認証で、アクセストークンを **パスワード** として使います。トークンを使う場合ユーザー名は無視されるので何を入れても構いませんが、わかりやすくするためにアカウント名を使うとよいでしょう。

```bash
git config --global credential.helper store
git push origin main
```

最初のプッシュではユーザー名とパスワードの入力を求められます。アカウント名（`admin`）とトークン（`tf_xxxxxxxxxxxx`）を入力してください。credential helper を設定しておけば、git が回答を保存し、それ以降は尋ねられなくなります。トークンをリモート URL に直接書き込むこともできますが、その場合 `.git/config` に平文で書き込まれてしまうため、helper を使うことをお勧めします。

アカウントのパスワードもトークンの代わりに使えます。これにより、まだ何も発行していない段階でも最初のプッシュが可能になります。とはいえトークンの方が優れた認証情報です。`read` にスコープを絞れますし、単体で失効させられますし、ログインに使うパスワードを一切露出しません。[認証](../reference/authentication.md) を参照してください。

## Git LFS { #git-lfs }

大きなファイルは Git LFS 経由でやり取りされます。マシンごとに一度インストールしてください:

```bash
git lfs install
```

### クローン時 { #on-clone }

LFS ファイルを含むリポジトリをクローンすると、実際のバイト列がチェックアウトの一部としてダウンロードされるため、作業ツリーにはポインタテキストではなく実物のファイルが置かれます。本番環境では、これらのオブジェクトは有効期限付きの署名付き URL 経由で取得され、あなたのマシンとオブジェクトストアの間で直接やり取りされます — API サーバーはデータの経路に入りません。

クローン時にこれをスキップし、オブジェクトを後から取得したい場合:

```bash
GIT_LFS_SKIP_SMUDGE=1 git clone http://localhost:8080/datasets/admin/imdb-reviews.git
cd imdb-ja
git lfs pull
```

### プッシュ時 { #on-push }

どのファイルが LFS 送りになるかは `.gitattributes` によって決まります。すべてのリポジトリは、常に大きくなりがちな形式をカバーするデフォルトのパターンセット — `*.safetensors`、`*.parquet`、`*.bin`、`*.gguf`、`*.ckpt`、`*.onnx`、`*.zip` など約 30 種類 — を持った状態で作成されるため、クローンしたばかりのリポジトリでもすでに正しく追跡されています。

パターンを追加するには `git lfs track` を使い、結果をコミットします:

```bash
git lfs track "*.jsonl"
git add .gitattributes data/train.jsonl
git commit -m "Track jsonl files with LFS"
git push origin main
```

!!! warning "`.gitattributes` は対象ファイルより先にコミットする必要があります"

    git-lfs は、ファイルがステージされる際に作業ツリーの `.gitattributes` を読み取ります。大きなファイルを先にコミットしてからパターンを追加すると、そのファイルは通常の blob として git の履歴に残ってしまい、修正には履歴の書き換えが必要になります。

他のアップロード経路との非対称性に注意してください。git 経由では、**ローカル** の git-lfs が何を LFS オブジェクトにするかを決めます。`huggingface_hub`、`hf` CLI、`tf` を通じてアップロードする場合は、**サーバー** がリポジトリの `.gitattributes` と 10 MiB のサイズしきい値から判断します。完全なルールは [ファイルのアップロード](uploading.md#how-files-are-routed-to-git-lfs) を参照してください。

## SSH でクローンする { #clone-over-ssh }

SSH 経由の Git は、サーバーが `TF_SSH_ENABLED=true` で動作している場合に利用できます。`docker compose` スタックではこれが有効になっておりポート 2222 が公開されますが、自分で構築したデプロイではデフォルトで無効です。

### 公開鍵を登録する { #register-a-public-key }

SSH は公開鍵のみで認証します — フォールバックとなるパスワードプロンプトはありません。鍵を持っていない場合は生成し、**公開鍵** の方を Web UI の **Settings → SSH keys**（<http://localhost:3000/settings/ssh-keys>）に登録してください:

```bash
ssh-keygen -t ed25519 -C "you@example.com"
cat ~/.ssh/id_ed25519.pub
```

`.pub` ファイルの 1 行をそのまま貼り付けます。サーバーが受け付けるものは次のとおりです:

| 鍵の種類 | 対応 |
|---|---|
| `ssh-ed25519` | 可 |
| `ecdsa-sha2-nistp256` / `-nistp384` / `-nistp521` | 可 |
| セキュリティキー系のバリアント（`sk-ssh-ed25519@openssh.com`, `sk-ecdsa-sha2-nistp256@openssh.com`） | 可 |
| `ssh-rsa` | 可（2048 ビット以上） |
| `ssh-dss`（DSA） | 不可 |

鍵は `authorized_keys` のオプションを含まない 1 行である必要があり、フィンガープリントはインスタンス全体で一度しか登録できません — アカウントは鍵のみで識別されるため、同じ鍵を 2 つのアカウントで共有することはできません。誤って秘密鍵を貼り付けた場合は検出され、保存されずに拒否されます。

### クローンする { #clone }

```bash
git clone ssh://git@localhost:2222/datasets/admin/imdb-reviews.git
git clone ssh://git@localhost:2222/admin/sentiment-base.git
```

SSH のユーザー名は無視されます — あなたは鍵によって識別されます — が、慣習として `git@` と書きます。パスには `{namespace}/{name}`、`models/{namespace}/{name}`、`datasets/{namespace}/{name}` のいずれかを、`.git` サフィックスの有無を問わず指定できます。

### SSH が提供しないもの { #what-ssh-does-not-offer }

この SSH サーバーは `git-upload-pack` と `git-receive-pack` という 2 つのコマンドを実行するために存在し、それ以外はすべて拒否します。シェルもなく、PTY もなく、sftp や scp もなく、ポートフォワーディングもありません。コマンドを指定せずに接続すると、その旨を伝えるあいさつメッセージが返ってきます。

!!! warning "Git LFS には HTTP リモートが必要です"

    git リモートが SSH の場合でも、Git LFS の転送は HTTP 経由で行われます。通常は SSH ホスト上で
    `git-lfs-authenticate` を実行してエンドポイントを発見しますが、このサーバーはそのコマンドを
    提供していません。大きなファイルが LFS オブジェクトになっているリポジトリでは、HTTP 経由で
    クローンするか、`git config lfs.url http://localhost:8080/datasets/admin/imdb-reviews/info/lfs`
    のようにして自分で git-lfs に HTTP エンドポイントを指定してください（この場合、鍵に加えて
    HTTP の認証情報も必要になります）。

権限は両方のトランスポートで同一です。SSH のパスは、リポジトリの検索と書き込みチェックを HTTP のパスとまったく同じコードに委譲しています。

!!! note "エフェメラルストレージ上のホスト鍵"

    サーバーは初回起動時に `TF_SSH_HOST_KEY_PATH` に SSH ホスト鍵を生成します。このパスが永続化されていない場合、再起動のたびに新しい鍵が発行され、どのクライアントもホスト鍵の不一致を警告するようになります。[設定](../self-hosting/configuration.md) を参照してください。

## ブランチ・タグ・リビジョン { #branches-tags-and-revisions }

新しいリポジトリは `main` をデフォルトブランチとして作成され、`README.md` と `.gitattributes` を含む 1 つの初期コミットを持ちます。

ブランチとタグは通常どおり動作します:

```bash
git checkout -b experiment
git push origin experiment

git tag v1.0
git push origin v1.0
```

`huggingface_hub` なら、クローンせずに同じことができます。これらの呼び出しも thinkingface に対して
動作します:

```python
from huggingface_hub import HfApi

api = HfApi()
api.create_branch("admin/my-model", branch="experiment")
api.create_tag("admin/my-model", tag="v1.0", tag_message="first release")
api.delete_branch("admin/my-model", branch="experiment")
api.delete_tag("admin/my-model", tag="v1.0")
```

Web UI からも同じ 4 つの操作ができます。ブランチの作成はリビジョンセレクタにあり、リポジトリの **Settings** タブには両方の ref が一覧され、タグ付けと削除の操作が付いています（[Web インターフェース](web-ui.md#manage-branches-and-tags) を参照）。シェルも Python セッションも不要です。

`create_branch(..., revision=...)` は任意のリビジョンからブランチを開始し、
`create_tag(..., revision=...)` は任意のリビジョンにタグを打ちます。`exist_ok=True` を渡すと、繰り返
し呼んでも no-op になります。**リポジトリのデフォルトブランチは削除できません** — HEAD、リポジトリ
カード、そしてリビジョンを指定しないすべての読み取りがそれに依存しているためです。ブランチやタグの作
成にはプッシュと同じ書き込み権限が必要で、アーカイブ済みリポジトリに対しては拒否されます。

API や UI がリビジョンを要求する場所であれば — `hf_hub_download(revision=...)`、`resolve` URL、ファイルブラウザのリビジョンセレクタなど — ブランチ名、タグ名、コミット SHA のいずれでも指定できます。注釈付きタグ（annotated tag）は、それが指すコミットに解決されます。

覚えておくべき 2 つの挙動があります:

- **デフォルト以外のブランチもインデックスされます。** ブランチをプッシュすると、そのファイルがオブジェクトストレージに公開され、そのブランチのファイルインデックスが更新されます。デフォルトブランチだけが更新するのは、リポジトリ自体のメタデータです。すなわち `README.md` から解析されるカード、ライセンスとタグ、一覧に表示されるサイズ、そして lineage（系譜）グラフです。
- **タグを作るだけではインデックス処理はスケジュールされません。** インデックスワーカーは、`git push` によるものであれ `create_branch()` によるものであれ、ブランチの先端（tip）が動くことをトリガーとして起動します。すでにいずれかのブランチ上にあるコミットを指すタグであれば問題ありません — そのファイルはすでにインデックスされています — が、他のどこにも存在しないリビジョンは、バケットアクセススクリプトの生成元となるファイルインデックスに現れない場合があります。

### デフォルトブランチを変更する { #changing-the-default-branch }

`main` のつもりが `master` に push してしまった場合でも、再度 push し直す必要はありません。リポジトリの **Settings** タブ（管理権限がある場合に表示されます）を開き、**Default branch**（デフォルトブランチ）で正しいブランチを選んで保存してください。選択できるのはすでに存在するブランチだけです — まだなければ先に push してください。

これにより、素の `git clone` が checkout するブランチが切り替わり、ファイル一覧・README カード（タグ・ライセンス・説明文）・lineage グラフが読み込むブランチも切り替わります — 上で説明した「デフォルトブランチだけが更新するメタデータ」と同じものです。そのブランチがデフォルトになる前に push されていた場合、メタデータの反映に少し時間がかかることがあります。反映中はリポジトリページにインデックス処理中のインジケータが表示されます。デフォルトブランチの変更にはネームスペース管理者権限が必要です（アーカイブや移管と同じ権限レベルです）。また、アーカイブ中のリポジトリでは拒否されます — 先にアーカイブを解除してください。

## プッシュがサーバー上で引き起こすこと { #what-a-push-triggers-on-the-server }

ブランチの先端が動くと、バックグラウンドワーカーがそのリビジョンの非 LFS ファイルをオブジェクトストレージに公開し、ファイルインデックスを再構築し、各 Parquet ファイルのフッターを読んでスキーマと行数を取得し、README カードを解析し、該当する場合は実験の run インデックスを更新します。通常はプッシュから 1 秒程度で完了します。

そのため、Parquet ファイルをプッシュすると、特に何もしなくてもすぐに [データセットビューア](dataset-viewer.md) で閲覧できるようになります。各ステップの詳細は [ファイルのアップロード](uploading.md#what-happens-after-an-upload) を参照してください。

## 既知の制限 { #known-limitations }

- **Smart HTTP のみ対応。** dumb HTTP プロトコルは提供されません。古いクライアントが原因不明のまま失敗するのではなく、サーバーはその旨をはっきりと伝えます。現行の git であれば問題ありません。
- **アーカイブされたリポジトリはプッシュを拒否します。** どちらのトランスポートでも、まずリポジトリのアーカイブを解除するよう伝えるメッセージが表示されます。
- **SSH リモート経由の Git LFS** には、上記のとおり HTTP エンドポイントの設定が必要です。
- **リポジトリに可視性の設定はありません。** サーバーにアクセスできる人であれば誰でもどのリポジトリもクローンできます。権限が制御するのは書き込みのみです。[ファイルのダウンロード](downloading.md#who-can-read-what) を参照してください。

## 次のステップ { #next-steps }

- [ファイルのアップロード](uploading.md) — git 以外の経路と、LFS へのルーティングがどう決まるか。
- [ファイルのダウンロード](downloading.md) — `resolve` URL と、バケットを直接読む方法。
- [認証](../reference/authentication.md) — トークン、スコープ、SSH 鍵。
