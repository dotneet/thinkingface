# tf CLI

`tf` は、データセットやモデルを thinkingface に登録するための、単一の静的バイナリで動く
コマンドラインクライアントです。このページは、コマンド、フラグ、認証情報の解決、設定ファイルに
ついての完全なリファレンスです。最初にひととおり試す流れは
[ファイルのアップロード](../guides/uploading.md) を参照してください。ここでは、`tf` を使う理由
はすでに分かっていて、正確な詳細を知りたいという前提で書いています。

```bash
tf login http://localhost:8080   # 最初の 1 回だけ
tf up ./imdb-ja                  # あとはこれだけ
```

`tf` に独自のプロトコルはありません。`huggingface_hub` が使うのと同じ HF 互換 HTTP API
（`whoami` / `create_repo` / `preupload` / LFS batch / `commit`）に対する薄いクライアントなので、
`tf up` がやることはすべて `hf upload` でも実現できます。後述の
[`hf upload` との関係](#relationship-to-hf-upload) を参照してください。

## インストール { #installation }

Go 1.25 以降が入っている場合は次のとおりです。

```bash
go install github.com/dotneet/thinkingface/backend/cmd/tf@latest
```

リポジトリのチェックアウトから、ローカルでビルドすることもできます。

```bash
make tf   # backend/bin/tf に生成されます
```

`make tf` は `git describe` から得たバージョン文字列を埋め込み、`tf version` はそれを表示します。

## クイックスタート { #quick-start }

```bash
# 1. サーバーにログインする（トークンを 1 つ発行して保存します）
tf login http://localhost:8080

# 2. ディレクトリをそのまま登録する
#    - 種別はファイルの内容から推測されます（safetensors や config.json があればモデル、
#      なければデータセット）
#    - 名前はディレクトリ名、ネームスペースは自分自身になります
tf up ./imdb-ja
```

すでに存在するリポジトリに対しては、`tf up` は差分だけを 1 つのコミットとして push します。
事前に内容を確認するには `--dry-run` を、ローカルに存在しなくなったリモートのファイルもあわせて
削除するには `--delete` を使います。

CI やスクリプトなど、対話的にログインできない場所では、代わりに `THINKINGFACE_API_KEY`（と
`THINKINGFACE_ENDPOINT`）を設定します。これで設定ファイルに触れることなく、すべてのコマンドが
`tf login` 済みと同じ状態になります。

```bash
export THINKINGFACE_ENDPOINT=http://localhost:8080
export THINKINGFACE_API_KEY=tf_xxxxxxxxxxxx   # /settings/tokens で発行した write スコープのトークン
tf status
tf up ./imdb-ja
```

## コマンドリファレンス { #command-reference }

`version` を除くすべてのサブコマンドは、次のフラグを受け付けます。

| フラグ | 意味 |
|---|---|
| `--endpoint URL` | サーバーの URL。省略した場合は [認証情報の解決順序](#credential-resolution-order) に従います |
| `--token TOKEN` / `--api-key KEY` | アクセストークン。どちらのフラグも同じ値を設定します。省略した場合は解決順序に従います |
| `--verbose` | エンドポイントとトークンがどのように解決されたかを stderr に表示します |

すべてのサブコマンドは `-h` / `--help` も受け付けます。これは使い方を stdout に表示して `0` で
終了します（使い方の誤りの場合は stderr に表示して `2` で終了するので、区別されます）。

**終了コード**: `0` は成功、`1` は失敗（stderr に `tf: <message>`）、`2` は使い方の誤りです。

### `tf login [ENDPOINT] [flags]` { #tf-login-endpoint-flags }

サーバーにログインし、トークンを設定ファイルに保存します。

```text
tf login [ENDPOINT] [--token TOKEN | --token -]
         [--username USER] [--password-stdin] [--name NAME]
```

| フラグ | 意味 |
|---|---|
| `ENDPOINT` | サーバーの URL。省略時は設定ファイルのデフォルトエンドポイント。それもなく stdin が端末であれば、`tf` が入力を求めます |
| `--token TOKEN` | 指定されたトークンを `whoami` で検証し、そのまま保存します。`--token -` は、代わりに stdin から（1 行で）トークンを読み取ります |
| `--username USER` | パスワードによるログインで使うユーザー名（`--token` を指定しない場合に使われます） |
| `--password-stdin` | エコーを無効にしたプロンプトの代わりに、stdin から（1 行で）パスワードを読み取ります |
| `--name NAME` | パスワードログインで発行されるトークンの名前（デフォルトは `tf-cli@<hostname>`） |

`--token` を指定しない場合、`tf login` はユーザー名とパスワードでサインインし、新しい write
スコープのパーソナルアクセストークンを発行します。パスワードの入力は、端末ではエコーを無効に
したプロンプトで行われ、パイプ経由の場合は `--password-stdin` で stdin から読み取られます。
発行されたトークンのスコープが `read` だった場合は警告が表示されます（`tf up` には write
スコープのトークンが必要です）。

### `tf logout [ENDPOINT]` { #tf-logout-endpoint }

サーバーに対して保存されている認証情報を破棄します（デフォルトは、設定されているデフォルト
エンドポイント）。保存されていたトークンが（`--token` で貼り付けたものではなく）`tf login`
自身が発行したものである場合は、サーバー側での失効もベストエフォートで行われます。

### `tf whoami` { #tf-whoami }

現在のトークンが表す身元を表示します。名前、メールアドレス、トークンのスコープ、所属している
Organization、そして push できるネームスペース（自分自身と、`admin` または `write` を持っている
Organization）です。

### `tf status [--json]` { #tf-status-json }

現時点で `tf` がどこへ、誰として接続するのかをまとめて表示します。解決されたエンドポイントと
トークン（およびそれぞれの取得元）、サーバーがそのトークンを受け付けるかどうか、そのトークンが
表す身元、push できるネームスペース、設定ファイルの場所、そして保存されているすべてのログイン
です。

```text
$ tf status
endpoint:   http://localhost:8080 (from env THINKINGFACE_ENDPOINT)
token:      tf_…9f2a (from env THINKINGFACE_API_KEY)
logged in:  yes
user:       admin (Admin) <admin@example.com>
scope:      write
push to:    admin
config:     /home/admin/.config/thinkingface/config.json (no saved logins)
```

トークンはマスクして表示されます（先頭 3 文字と末尾 4 文字）。終了コードはログイン済みなら
`0`、そうでなければ `1` なので、スクリプトから `tf status` をそのまま前提条件のチェックとして
使えます。`--json` を付けると、上の表の代わりに、同じ情報が 1 つの JSON オブジェクト
（`logged_in`、`user`、`push_to`、`saved_endpoints` など）として stdout に出力されます。

### `tf up PATH [flags]` { #tf-up-path-flags }

中心となるコマンドです。PATH（ファイルまたはディレクトリ）の内容を、1 つのコミットとして
リポジトリへ push します。リポジトリが存在しない場合は、先に作成します。

```text
tf up PATH [--to NS/NAME|NAME] [--kind dataset|model] [--rev BRANCH]
           [-m/--message MSG] [--license L] [--tag T ...] [--desc TEXT]
           [--include GLOB ...] [--exclude GLOB ...] [--delete] [--dry-run]
           [--workers N] [--quiet] [--json]
```

| フラグ | デフォルト | 意味 |
|---|---|---|
| `--to NS/NAME` または `NAME` | 自分のネームスペース + PATH のディレクトリ名 | push 先のリポジトリ。`NS/NAME` に `datasets/` または `models/` のプレフィックスを付けると、種別も固定されます |
| `--kind dataset\|model` | 内容から推測 | リポジトリの種別を明示的に固定します。`--to` のプレフィックスより優先されます |
| `--rev` | `main` | push 先のブランチ |
| `-m`, `--message` | `Upload N files with tf` | コミットの要約 |
| `--license` | （未設定） | リポジトリカードの `license` |
| `--tag` | （未設定） | リポジトリカードの `tags`。繰り返し指定でき、1 回の指定にカンマ区切りで複数の値を書くこともできます（`--tag a,b --tag c` → `a`、`b`、`c`） |
| `--desc` | （未設定） | リポジトリカードの `description`。生成される README の冒頭の段落としても使われます |
| `--include` | すべて含める | このシェル glob に一致するファイルだけを含めます（繰り返し指定可） |
| `--exclude` | （なし） | このシェル glob に一致するファイルを除外します（繰り返し指定可） |
| `--delete` | off | ローカルに存在しないリモートのファイルを削除します |
| `--dry-run` | off | 何も変更せずに、何が起きるかを表示します |
| `--workers` | `4` | LFS の並列転送数 |
| `--quiet` | off | stderr への進捗表示を抑制します |
| `--json` | off | 最終結果を 1 行の JSON として stdout に出力します（`--quiet` と併用しない限り、進捗は stderr に出ます） |

**種別の決定順序**: `--kind` が `--to` の `datasets/` / `models/` プレフィックスより優先され、
そのプレフィックスがディレクトリの内容からの推測（`*.safetensors` や `config.json` などがあれば
モデル、なければデータセット）より優先されます。

**push 先が存在しない場合**: `--kind` も `--to` のプレフィックスも種別を固定しておらず、推測
された種別のリポジトリも見つからないとき、`tf up` は新しく作る前に *もう一方* の種別に既存の
リポジトリがないかも確認します（例えば、推測ではデータセットだが同じ名前のモデルリポジトリが
すでにある場合、そちらが使われます）。

!!! warning "`--delete` が保護する 2 つのファイル"
    `--delete` は、ローカルに存在しなくても、ルートの `.gitattributes` と `README.md` を削除
    することはありません。`.gitattributes` はサーバーが生成するもので、以後のアップロードでの
    LFS の振り分けを決めます。`README.md` には、前回の実行で `--license`/`--tag`/`--desc` から
    生成されたリポジトリカードが入っている可能性があります。

**README の扱い**: `--license`、`--tag`、`--desc` のいずれかが指定されていて、ローカルに
`README.md` がない場合は、リポジトリカードを含む README が生成され、アップロードに含まれます。
ローカルに `README.md` がある場合は、指定された値だけが既存のフロントマターにマージされます
（本文とキーの順序は保持されます）。カード関連のフラグがどれも指定されていない場合、README には
一切手を付けません。

`tf up --json` の出力の形は次のとおりです。

```json
{
  "repo": "admin/imdb-reviews",
  "kind": "dataset",
  "rev": "main",
  "created": true,
  "commit": "abc1234def5678",
  "url": "http://localhost:8080/datasets/admin/imdb-reviews",
  "commit_url": "http://localhost:8080/datasets/admin/imdb-reviews/commit/abc1234def5678",
  "files": 3,
  "lfs_files": 2,
  "unchanged": 1,
  "deleted": 0,
  "bytes": 141557760,
  "uploaded_bytes": 129394688,
  "dry_run": false,
  "nothing_to_do": false
}
```

!!! note "表示される URL について"
    `url` は、エンドポイントのオリジンに Web UI 側のパス（`/datasets/{ns}/{name}` または
    `/models/{ns}/{name}`）を続けたものです。API と Web UI が別のオリジンにある場合
    （docker compose の開発環境のように `:8080` と `:3000` に分かれている場合）は、パスは
    そのままに、オリジンだけ Web UI のものに読み替えてください。

### `tf version` { #tf-version }

`tf <version> (<GOOS>/<GOARCH>)` を表示します。

### `tf help [COMMAND]` { #tf-help-command }

全体の使い方、または 1 つのコマンドについての詳しい使い方を表示します（`tf COMMAND --help`
と同じです）。

## 認証情報の解決順序 { #credential-resolution-order }

すべてのコマンドで、`tf` は次の優先順位でエンドポイントとトークンを決定します。

**エンドポイント**: `--endpoint` フラグ > `TF_ENDPOINT` > `THINKINGFACE_ENDPOINT` >
`HF_ENDPOINT` > 設定ファイルのデフォルトエンドポイント。どれも設定されていない場合はエラーに
なります。

**トークン**: `--token` / `--api-key` フラグ > `THINKINGFACE_API_KEY` > `TF_TOKEN` >
`THINKINGFACE_TOKEN` > 解決されたエンドポイント向けに設定ファイルへ保存されているトークン >
`HF_TOKEN`（正規化した `HF_ENDPOINT` が解決されたエンドポイントと一致する場合のみ。本物の
huggingface.co 向けのトークンを thinkingface のサーバーへうっかり送ってしまわないための安全策
です） > 未設定（匿名。`tf up` と `tf whoami` は匿名での実行を拒否します）。

したがって、`THINKINGFACE_API_KEY` と `THINKINGFACE_ENDPOINT` を設定するだけで、設定ファイルを
一度も書くことなく、すべてのコマンドを `tf login` 済みと同じ挙動にできます。どの値がどこから
解決されたか（`from flag`、`from env TF_ENDPOINT`、`from config` など）を stderr で確認するに
は、任意のコマンドに `--verbose` を渡します。

## 設定ファイル { #config-file }

保存先は、`$TF_CONFIG` が設定されていればその値、なければ
`$XDG_CONFIG_HOME/thinkingface/config.json`、それも無ければ
`~/.config/thinkingface/config.json` です。パーミッションはファイルが `0600`、ディレクトリが
`0700` で、書き込みは一時ファイルを経由したアトミックな rename で行われます。

トークンはエンドポイントごとに 1 つ保存され（正規化したエンドポイント URL をキーにします）、
最後にログインしたエンドポイントが、`--endpoint` もエンドポイント用の環境変数もなしにコマンドを
実行したときのデフォルトになります。ファイルの中身は次のような形です。

```json
{
  "default_endpoint": "http://localhost:8080",
  "credentials": {
    "http://localhost:8080": {
      "endpoint": "http://localhost:8080",
      "token": "tf_xxxxxxxxxxxx",
      "token_id": 42,
      "username": "admin",
      "created_at": "2026-08-23T09:00:00Z"
    }
  }
}
```

保存されたトークンが `tf login` の発行したものではなく `--token` で貼り付けたものである場合、
`token_id` は `0` になります。`tf logout` はこれを見て、サーバー側に失効させるものがあるか
どうかを判断します。

## `hf upload` との関係 { #relationship-to-hf-upload }

`tf` は、thinkingface のサーバーがもともと公開している HF 互換 API をそのまま話すだけなので、
`tf up` にできること（種別の推測とリポジトリカードの生成を除く）は `huggingface_hub` の
`hf upload` / `HfApi` でも実現できます。

```bash
export HF_ENDPOINT=http://localhost:8080
export HF_TOKEN=tf_xxxxxxxxxxxx
export HF_HUB_DISABLE_XET=1
hf upload admin/imdb-reviews ./imdb-ja . --repo-type dataset
```

`tf` はこの手順を包んで、`HF_ENDPOINT`/`HF_TOKEN` の管理、`--repo-type` の指定、リポジトリの
事前作成を不要にしているだけで、`tf` 自身が互換性の差を持ち込むことはありません。
`huggingface_hub` 経由で動作が確認されているもの・されていないものについては
[互換性](compatibility.md) を参照してください。

## 既知の制限 { #known-limitations }

- `tf up gs://...` はサポートされていません（`gs:// import is not supported yet` という
  エラーになります）。GCS にあるデータは、いったんローカルにコピーしてから `tf up` を実行して
  ください。
- コマンド名 `tf` は、Terraform（`terraform` を `tf` にエイリアスしている環境）や TensorFlow
  自身のツールと衝突することがあります。ここに書かれたとおりに `tf` が動かない場合は、シェルの
  エイリアス設定を確認してください。

## 関連項目 { #see-also }

- [ファイルのアップロード](../guides/uploading.md) — データを入れるまでの、タスク指向の手順
- [認証](authentication.md) — アクセストークンがどのように発行され、どうスコープされるか
- [互換性](compatibility.md) — `huggingface_hub` と git 経由で動作が確認されていること
