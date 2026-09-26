# tf CLI

`tf` は、データセットやモデルを thinkingface に登録し、実験の run を照会・管理するための、
単一の静的バイナリで動くコマンドラインクライアントです。このページは、コマンド、フラグ、認証
情報の解決、設定ファイルについての完全なリファレンスです。最初にひととおり試す流れは
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

`version` を除くすべてのサブコマンドは、次のフラグを受け付けます（`version` が受け付けるのは
`--json` だけです）。

| フラグ | 意味 |
|---|---|
| `--endpoint URL` | サーバーの URL。省略した場合は [認証情報の解決順序](#credential-resolution-order) に従います |
| `--token TOKEN` / `--api-key KEY` | アクセストークン。どちらのフラグも同じ値を設定します。省略した場合は解決順序に従います |
| `--verbose` | エンドポイントとトークンがどのように解決されたかを stderr に表示します |
| `--json` | 機械可読な出力です。結果を JSON として stdout に出力します（1 つのオブジェクトを 1 行で。`tf experiments sync --watch` は 1 回のパスごとに 1 行を出力します）。進捗、警告、エラーは stderr に出たままで、終了コードも変わらないので、スクリプトは stdout を絞り込まずにそのままパースできます。`tf mcp` を除くすべてのコマンドで受け付けられ、`tf version` も含まれます |

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
| `ENDPOINT` | サーバーの URL。省略した場合は他のすべてのコマンドと同じ [認証情報の解決順序](#credential-resolution-order)（`TF_ENDPOINT` / `THINKINGFACE_ENDPOINT` / `HF_ENDPOINT`、それから設定ファイルのデフォルトエンドポイント）に従います — `login`/`logout` だけ特別扱いされるわけではありません。それでも何も解決できず、かつ stdin が端末であれば、エラーにする代わりに `tf login` が入力を求めます |
| `--token TOKEN` | 指定されたトークンを `whoami` で検証し、そのまま保存します。`--token -` は、代わりに stdin から（1 行で）トークンを読み取ります |
| `--username USER` | パスワードによるログインで使うユーザー名（`--token` を指定しない場合に使われます） |
| `--password-stdin` | エコーを無効にしたプロンプトの代わりに、stdin から（1 行で）パスワードを読み取ります |
| `--name NAME` | パスワードログインで発行されるトークンの名前（デフォルトは `tf-cli@<hostname>`） |

`--token` を指定しない場合、`tf login` はユーザー名とパスワードでサインインし、新しい write
スコープのパーソナルアクセストークンを発行します。パスワードの入力は、端末ではエコーを無効に
したプロンプトで行われ、パイプ経由の場合は `--password-stdin` で stdin から読み取られます。
発行されたトークンのスコープが `read` だった場合は警告が表示されます（`tf up` には write
スコープのトークンが必要です）。

`--json` を付けると、`tf login` は確認のメッセージ行の代わりに `{"endpoint", "username", "scope",
"token_id", "minted", "config_path"}` を出力します。トークンのフィールドは意図的に含めていません。
ログインの出力は CI のログに取り込まれやすいからです。`--token` で貼り付けたトークンの場合、
`token_id` は `0`、`minted` は `false` になります。

既にログイン済みのエンドポイントに対してもう一度ログインすると、新しいトークンが無事に
保存された時点で、それまでの `tf login` がそのエンドポイント用に発行していたトークンが
失効させられます — この際 `tf` は stderr にメモを表示します
（`revoked the token saved by the previous tf login (id N)`）。`--token` で貼り付けた
トークンはこの仕組みでは失効されません。それを取り除くのは `tf logout`（またはあなた自身）
だけです。

### `tf logout [ENDPOINT]` { #tf-logout-endpoint }

サーバーに対して保存されている認証情報を破棄します（デフォルトは、設定されているデフォルト
エンドポイント）。保存されていたトークンが（`--token` で貼り付けたものではなく）`tf login`
自身が発行したものである場合は、サーバー側での失効もベストエフォートで行われます。`--json` は
`{"endpoint", "revoked"}` を出力し、ベストエフォートの失効に失敗した場合はそれに `"revoke_error"`
が加わります（ログアウト自体は成功します）。

### `tf whoami` { #tf-whoami }

現在のトークンが表す身元を表示します。名前、メールアドレス、トークンのスコープ、所属している
Organization、そして push できるネームスペース（自分自身と、`admin` または `write` を持っている
Organization）です。`--json` は `{"name", "fullname", "email", "scope", "endpoint",
"orgs": [{"name", "role"}], "push_to"}` を出力します。

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
           [--include GLOB ...] [--exclude GLOB ...] [--hidden]
           [--delete] [--dry-run]
           [--workers N] [--quiet] [--json]
```

| フラグ | デフォルト | 意味 |
|---|---|---|
| `--to NS/NAME` または `NAME` | 自分のネームスペース + PATH から導かれる名前 | push 先のリポジトリ。`NS/NAME` に `datasets/` または `models/` のプレフィックスを付けると、種別も固定されます。導かれる名前は、PATH がディレクトリならそのディレクトリ名、PATH が単一ファイルならその**拡張子を取り除いた**ファイル名です（`tf up ./model.safetensors` は `model.safetensors` ではなく `model` という名前のリポジトリを対象にします） |
| `--kind dataset\|model` | 内容から推測 | リポジトリの種別を明示的に固定します。`--to` のプレフィックスより優先されます |
| `--rev` | `main` | push 先のブランチ |
| `-m`, `--message` | `Upload N files with tf`（ちょうど 1 件なら `Upload 1 file with tf`、何もアップロードせず削除のみの場合は `Delete N files with tf`） | コミットの要約 |
| `--license` | （未設定） | リポジトリカードの `license` |
| `--tag` | （未設定） | リポジトリカードの `tags`。繰り返し指定でき、1 回の指定にカンマ区切りで複数の値を書くこともできます（`--tag a,b --tag c` → `a`、`b`、`c`） |
| `--desc` | （未設定） | リポジトリカードの `description`。生成される README の冒頭の段落としても使われます |
| `--include` | すべて含める | この glob に一致するファイルだけを含めます（繰り返し指定可）。シェルを通した glob ではなく `tf` 自身が照合します。`**` は任意個数のパスセグメントに一致し（`data/**`、`**/*.parquet`）、`[...]` は1文字の集合に一致し（`[ab].csv`、`[a-z]*`、否定は `[!0-9]*`）、`/` を含まないパターンはファイルのベース名に対しても試されます（`*.parquet` は `data/train.parquet` にも一致します） |
| `--exclude` | （なし） | この glob に一致するファイルを除外します（繰り返し指定可）。マッチングの規則は `--include` と同じです |
| `--hidden` | off | PATH 以下で見つかったドットファイル・ドットディレクトリもアップロードします。既定ではスキップされます（下記参照）。`.gitattributes` と `.gitignore` はどちらの場合も常にアップロードされます |
| `--delete` | off | PATH 以下のディスク上のどこにも存在しないリモートのファイルを削除します。`--include`/`--exclude` とは独立していて、それらのフラグが今回のアップロード対象から外したファイルでも、ディスク上に存在する限り削除されません |
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

!!! warning "ドットファイルは既定でアップロードされません"
    ここでのリポジトリはサーバーに到達できる誰からも読めます。そしてプロジェクトのディレクトリ
    には、データ以外のものも大抵置かれています — `.env`、`.envrc`、`.aws/credentials`、`.ssh/`、
    エディタの `.idea/` など。そのため `tf up` は、PATH の *内側* で見つけたドットファイルと
    ドットディレクトリをアップロードから外し、何をスキップしたかを stderr に 1 行で表示します
    （この警告は `--quiet` でも抑制されません）。ただし 2 つの名前は常にアップロードされます。
    マシン側の状態ではなくリポジトリの内容だからです: `.gitattributes`（LFS の振り分け規則を
    持ちます）と `.gitignore` です。

    この規則が扱わないことが 2 つあります。自分で指定したパスは自分で選んだものなので、
    `tf up ./.config` や `tf up ./.env` は今までどおりアップロードされます。そして、以前の実行で
    アップロード済みのドットファイルはリモートに残ります — ディスク上には存在しているので、
    `--delete` は「今回のアップロードに含まれない」ことを「ローカルから消えた」とは解釈しません
    （下記参照）。そうしたファイルをリモートから消したい場合は、このフラグに期待するのではなく、
    Web UI か `git push` で明示的に削除してください。

    アップロードしたい場合は `--hidden` を付けてください。`--include` だけでは上書きできません。
    パターンで名指ししたドットファイルであっても `--hidden` が必要です。

!!! warning "`--delete` が保護する 2 つのファイル"
    `--delete` は、ローカルに存在しなくても、ルートの `.gitattributes` と `README.md` を削除
    することはありません。`.gitattributes` はサーバーが生成するもので、以後のアップロードでの
    LFS の振り分けを決めます。`README.md` には、前回の実行で `--license`/`--tag`/`--desc` から
    生成されたリポジトリカードが入っている可能性があります。

!!! note "`--delete` と `--include`/`--exclude` の組み合わせ"
    `--include`/`--exclude` でアップロード対象から外れたファイルも、アップロード対象かどうかで
    はなくディスク上の存在で判定されます。PATH 以下のどこかに存在する限り `--delete` の対象には
    なりません。実際に PATH 以下から消えたファイルだけが削除されます。上記の規則でスキップされた
    ドットファイル、およびスキップされたドットディレクトリ配下のすべてについても同じです。

!!! note "`--delete` と symlink ディレクトリ: 死角は触れられない"
    ローカルのスキャンは、ディレクトリを指す symlink（たどるとループしうるため）、壊れた symlink、
    非通常ファイル（ソケットや fifo など）をたどりません — `.git` と `__pycache__` ディレクトリも
    同様にスキップされますが、こちらは無言です。`.git`/`__pycache__` 以外のスキップについては、
    `tf up` が stderr に警告を出します（最大 10 件、それ以降は件数のみ）。**この警告は `--quiet`
    でも抑制されません** — `--quiet` が抑えるのは進捗表示であって、アップロードの中身が黙って
    欠けることは進捗ではないからです。

    特に symlink ディレクトリは `--delete` にとっての死角です。スキャンがその中身を一切読んでい
    ないため、`tf` はそのディレクトリ配下のリモートパスがローカルにまだ存在するかどうかを判断で
    きず、当てずっぽうで判断するのではなくそこ以下をすべてそのままにします。ローカルの対応物が
    ディレクトリ symlink の背後に隠れてしまったリモートファイルは、そのシンボリックリンクが実
    ディレクトリに解決される（または削除される）まで、`--delete` を付けても削除されません。

**README の扱い**: `tf up` は、ローカルの `README.md` を自分の判断でアップロードから外すことは
ありません — ローカルの `README.md` は他のファイルと同じ普通のファイルであり、この実行に含まれ
る限り常にアップロードされ（そしてリモートの内容を上書きします）。以下の説明は、そのアップロード
の**前に中身が書き換わるかどうか**だけの話であり、また `--include`/`--exclude` でフィルタした
後のファイル集合を基準にしていて、ディスク上に物理的に何があるかは基準にしていません。

- `--license`、`--tag`、`--desc` のどれも指定されていない場合、README の中身は生成もマージも
  されません — フィルタ後の集合に含まれていれば、ローカルの `README.md` はディスク上のとおりに
  そのままアップロードされます。
- いずれかのフラグが指定されていて、**かつ** `README.md` がフィルタ後のファイル集合に含まれて
  いる場合は、指定された値だけが既存のフロントマターにマージされます（本文とキーの順序は保持
  されます）。
- いずれかのフラグが指定されていて、`README.md` がフィルタ後のファイル集合に**含まれていない**
  場合 — ローカルに本当に `README.md` が存在しないか、`--include`/`--exclude` で除外された場合
  のいずれか — は、カードフラグから新しい `README.md` が生成されてアップロードに含まれ、リモー
  トに存在する `README.md` を黙って置き換えます。この実行から `--include`/`--exclude` で除外さ
  れたローカルの `README.md` は、フラグを単に付けなかった場合のようにリモートファイルを保護して
  はくれません。

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

### `tf experiments` { #tf-experiments }

実験の run を照会し、待ち合わせ、注釈を付け、インポートし、同期します —
[実験のトラッキング](../guides/experiments.md#work-with-runs-from-the-command-line) のコマンド
ライン側にあたります。`tf exp` はエイリアスです。

```text
tf experiments runs     REPO PROJECT [--group G] [--status S] [--tag T] [--archived true|false]
                        [--sort SPEC] [--order asc|desc] [--limit N] [--columns LIST]
tf experiments run      REPO PROJECT RUN
tf experiments wait     REPO PROJECT RUN [--until EXPR] [--timeout 24h] [--ignore-stale]
tf experiments diff     REPO PROJECT [RUN ...] [--include-meta]
tf experiments goals    REPO PROJECT [METRIC=min|max|none ...]
tf experiments notes    REPO PROJECT [--set FILE|-] [--base-sha SHA] [--force] [-m MSG]
tf experiments annotate REPO PROJECT RUN [--note TEXT | --note-file FILE|-] [--tag T ...]
                        [--clear-tags] [--add-tag T ...] [--remove-tag T ...] [--archive | --unarchive]
tf experiments import   REPO PROJECT FILE... [--configs FILE] [--status finished|failed]
                        [--replace] [--format csv|jsonl] [--dry-run]
tf experiments sync     [DIR ...] [--watch] [--interval 60s]
```

どのサブコマンドも `--json` と、共通の `--endpoint` / `--token` / `--api-key` / `--verbose`
フラグを受け付けます。`tf experiments help SUBCOMMAND` でそのサブコマンドの詳しい使い方が表示
されます。

- `REPO` は実験リポジトリを `ns/name` の形で指定します（`datasets/` プレフィックスも受け付けます）。
  `PROJECT` と `RUN` はそのまま渡されるので、`/`、空白、`%`、非 ASCII 文字を含む名前も使えます。
- 匿名での読み取りを許可しているインスタンスでは、読み取りはトークンなしで動作します。書き込み
  — 引数付きの `goals`、`notes --set`、`annotate`、`import`、`sync` — には、そのリポジトリに
  対する write スコープのトークンが必要です。
- `--json` を付けると、`runs`、`run`、`diff`、`goals`、`notes`、`annotate` は API のレスポンスを
  そのまま出力します。`wait`、`import`、`sync` は以下で説明する形で出力します。

#### `tf experiments runs` { #tf-experiments-runs }

プロジェクトの run の一覧表です。名前、ステータス、最後のステップ、最終更新日時に続いて、追加の
列が並びます。絞り込みと並べ替えはサーバー側で行われます。

| フラグ | 意味 |
|---|---|
| `--group G` | スイープグループ `G` の run だけ。繰り返し指定可: いずれかに該当 |
| `--status S` | ステータスが `running`、`finished`、`failed`、`stale` のいずれかである run だけ。繰り返し指定可（またはカンマ区切り）: いずれかに該当 |
| `--tag T` | タグ `T` が付いた run だけ。繰り返し指定可: すべてに該当 |
| `--archived true\|false` | アーカイブ済みの run だけ / アーカイブされていない run だけ（デフォルト: 両方） |
| `--sort SPEC` | `name`、`started_at`、`updated_at`、`last_step`、`last:<metric>`、`min:<metric>`、`max:<metric>`、`best:<metric>`（[ゴール](#tf-experiments-goals) が必要）、`config:<dotted.key>`。その値を持たない run は常に最後に並びます |
| `--order asc\|desc` | デフォルトは `asc`。`best:` は常に最良の run を先頭にします |
| `--limit N` | 最大 `N` 件の run（1〜1000） |
| `--columns LIST` | カンマ区切りの追加の列: `config:<key>`、`last:<metric>`（エイリアス `metric:<metric>`）、`min:<metric>`、`max:<metric>`、`best:<metric>`（ゴールに従って min または max）、`group`、`job_type`、`tags`、`points`、`note` |

`--columns` を指定しない場合、表には、いずれかの run がグループを持っていればグループ、ゴールを
持つすべてのメトリクスをそのゴールの向きで（`min:loss`、`max:acc`）、その後にそれ以外のメトリ
クスを最大 3 つまで最終値で表示します（`_` で始まるシステムメトリクスは飛ばされます。いくつ省いた
かは stderr のメモで示されます）。`*` は、ゴールを持つメトリクスごとに、アーカイブされていない
run のうち最良のものを示します。`--json` は `{"runs": [...], "metric_goals": {...}, "best":
{metric: run}}` を出力します。

#### `tf experiments run` { #tf-experiments-run }

1 つの run をキーと値のブロックとして表示します。ステータス（ハートビート付き）、ステップ数と点の
数、タイムスタンプ、グループ、タグ、ノート、生成したモデル、フラット化した config、そしてすべての
メトリクスの最終値 / 最小値 / 最大値です。`--json` は `{"run": {...}}` を出力します。

#### `tf experiments wait` { #tf-experiments-wait }

run が `--until EXPR`（デフォルトは `status!=running`）を満たすまで、サーバーのロングポーリングを
使ってブロックします。run はまだ存在していなくても構いません。404 は 5 秒ごとに再試行されます。
ネットワークエラー、5xx、429 はバックオフしながら再試行され、400 / 401 / 403 は即座に待機を
終了させます。

```text
expr    := and ( "or" and )*          "and" binds tighter than "or"
and     := primary ( "and" primary )*
primary := "(" expr ")" | cond
cond    := FIELD OP VALUE             OP: == != >= <= > <
FIELD   := step | points | status | metric:<m> | last:<m> | min:<m> | max:<m>
```

- `step` は最後に記録されたステップ、`points` は点の数、`metric:` / `last:` はメトリクスの最終値、
  `min:` / `max:` はその時点までの極値です。
- `status` は `==` / `!=` だけで、`running`、`finished`、`failed`、`stale` のいずれかと比較します。
  それ以外の値は、黙って永遠に真にならない条件になるのではなく、パースエラーになります。
- メトリクス名は空白、括弧、`= ! < >`、引用符のところで終わるので、`metric:val/CER<0.2` は
  そのまま書けます。そうでない場合はコロンの直後から引用符で囲みます: `metric:"val loss" < 0.2`。
- キーワードとフィールド名は大文字・小文字を区別しません。run がまだ記録していないメトリクスに
  対する比較は、`!=` も含めてどの演算子でも偽になります。

| 終了コード | 意味 |
|---|---|
| `0` | 条件が成り立った |
| `1` | `--timeout` が経過した（デフォルトは `24h`。`0` は無期限に待ちます）、または run が条件を満たさないまま止まった |
| `2` | 使い方の誤り。パースできない `--until` も含みます（メッセージがその桁位置を示します） |

「止まった」とは、run がもう `running` ではなく — finished、failed、stale のいずれか — 、しかも
条件が成り立っておらず、`status` にも言及していないために、もう真になりえない状態のことです。
このときはタイムアウトまで待ち続けるのではなく、待機を終了します。`--ignore-stale` はこの判定を
無効にします。使い方の誤り以外のすべての場合で、最後の状態が stdout に出力されます。`--json` は
`{"run": {...}|null, "met": bool, "reason": "met"|"timeout"|"stopped", "until": "EXPR"}` を出力
します（run が一度も現れなかった場合、`run` は `null` です）。

#### `tf experiments diff` { #tf-experiments-diff }

ドット区切りのパスにフラット化した config のキーのうち、指定した run の間で値が異なるものを
表示します — run を指定しなかった場合は、アーカイブされていないすべての run（最大 200 件）の間で
比較します。run ごとに 1 列で、その run がキーを持たない箇所は `-` になります。`--include-meta` を
付けると、`_meta` と `_resume` のサブツリーも比較します。

#### `tf experiments goals` { #tf-experiments-goals }

引数なしでは、プロジェクトのメトリクスのゴールを表示します。`METRIC=min`、`METRIC=max`、
`METRIC=none` の引数を付けると、それらをマージします（`none` はゴールを削除し、言及されなかった
ゴールはそのまま残ります。分割は最後の `=` で行われるので、メトリクス名に `=` が含まれていても
構いません）。プロジェクトにまだ run が 1 つもなくても動作します。ゴールは、最良の run の印と
`--sort best:<metric>` の基準になります。

#### `tf experiments notes` { #tf-experiments-notes }

プロジェクトのノート、つまりデフォルトブランチ上の `{project}/NOTES.md` を表示します（まだ存在
しない場合は stdout には何も出力せず、stderr にメモを表示します）。`--set FILE`（stdin からの場合
は `-`）はノートを置き換え、`-m` がコミットメッセージになります。`--set` は先に現在の版を読み、
それをベースとして送信するので、その間に誰かが保存していればサーバーは書き込みを拒否します
（終了コード 1）。2 回の起動にまたがって読み込み・編集・書き込みを行う場合は、`--json` で読み込み、
その `blob_sha` を `--base-sha` で渡し返してください（`--base-sha ""` は「ノートがまだ存在して
いてはならない」という意味です）。`--force` は無条件に上書きします。

#### `tf experiments annotate` { #tf-experiments-annotate }

run のノート（`--note TEXT`、`--note-file FILE|-`。`--note ""` で消去）、タグ（`--tag T` は集合を
置き換え、`--clear-tags`、`--add-tag T` / `--remove-tag T` で編集）、アーカイブのフラグ
（`--archive` / `--unarchive`）を変更します。変わるのはフラグで指定したものだけです。フラグを
1 つも指定しないのは使い方の誤りです。

#### `tf experiments import` { #tf-experiments-import }

CSV（ヘッダー行付き）または JSONL のファイルから過去の run を `PROJECT` にインポートします。
リポジトリが存在しない場合は作成します。各行が 1 つの点です。`run` と `step`（整数）は必須、
`timestamp`（RFC 3339 または unix 秒）は任意で、それ以外の列はすべてメトリクスです。インポート
されるのは数値だけで、空、数値でない、`NaN`、無限大のセルはスキップされ、その数が数えられます。

| フラグ | 意味 |
|---|---|
| `--configs FILE` | `{"run", "config", "status", "group", "job_type"}` の JSONL。run とともに送る config、最終ステータス、スイープのグループ分けです |
| `--status S` | `--configs` でステータスが指定されていない run の最終ステータス: `finished`（デフォルト）または `failed` |
| `--replace` | 先に同じ名前の既存の run を削除します。これを付けない場合、既に存在する run が 1 つでもあれば、何かを送信する前にインポート全体が拒否されます |
| `--format csv\|jsonl` | すべてのファイルをこの形式としてパースします（デフォルト: 拡張子で判定。`.csv` / `.jsonl` / `.ndjson`） |
| `--dry-run` | パース、検証、既存の run の確認だけを行い、何も送信しません |

`--json` は `{"runs": [{"run", "points", "status", "replaced"}], "skipped_cells": N}` を出力します
（ドライランでは `"dry_run": true` が加わります）。

#### `tf experiments sync` { #tf-experiments-sync }

`thinkingface.trackio` がディスクに記録した run をアップロードします — オフラインモードで記録した
もの、またはオンラインモードで届けられなかった点です（[オフラインの run](../guides/experiments.md#offline-runs-and-tf-experiments-sync)
を参照してください）。各 `DIR` は、run ごとに 1 つのサブディレクトリを持つ親ディレクトリか、
1 つの run のディレクトリ（`run.jsonl` を含むもの）のどちらかです。デフォルトは
`./thinkingface-offline` です。

進捗は各 run ディレクトリの `sync-state.json` に保存されるので、同期は中断して再実行できます。
まだ終了の記録がない run は、その時点の末尾まで同期されて開いたままになります。終了した run は
アーティファクトがコミットされ、ステータスが設定され、生成したモデルが記録され、以後はスキップ
されます。`--watch` は、中断されるまで `--interval`（デフォルトは `60s`）ごとにパスを繰り返し
ます。1 つのディレクトリでのエラーは他のディレクトリの処理を止めません。いずれかが失敗した場合、
終了コードは `1` です。`--json` は 1 回のパスごとに 1 つのオブジェクトを出力します。

```json
{"runs": [{"dir": "thinkingface-offline/20260927T101500-ocr-lr_0.01-1a2b3c4d", "repo": "alice/trackio-metrics",
  "project": "ocr", "run": "lr-0.01", "synced_lines": 812, "points": 8100, "done": true, "status": "finished",
  "artifacts_uploaded": 2, "artifacts_skipped": 0}]}
```

### `tf mcp` { #tf-mcp }

```text
tf mcp [--endpoint URL] [--token TOKEN] [--verbose]
```

`tf experiments` の機能を、stdin/stdout 上の
[Model Context Protocol](https://modelcontextprotocol.io/) で AI エージェントに提供します。
エージェントのクライアントがこれをサブプロセスとして起動します。ポートもデーモンもありません。
他のすべてのコマンドと同じ認証情報を使いますが、その解決は起動時ではなく最初のツール呼び出しの
時点で行われます。そのため、`tf login` より前に起動したサーバーも動き続け、認証情報ができるまで
は各呼び出しに対して対処方法を示すエラーを返します。`--verbose` を付けると、認証情報の解決と
各リクエストを stderr に記録します。付けない場合、`tf mcp` は stderr に何も書きません。

クライアントへの登録方法と、提供されるツールについては
[AI エージェントから thinkingface を使う](../guides/agents.md) を参照してください。

### `tf version` { #tf-version }

`tf <version> (<GOOS>/<GOARCH>)` を表示します。`--json` は `{"version", "os", "arch",
"go_version"}` を出力します。

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
`token_id` は `0` になります。`tf logout`、そして同じエンドポイントへの後続の `tf login` は
これを見て、サーバー側に失効させるべき過去発行のトークンがあるかどうかを判断します。

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
- [実験のトラッキング](../guides/experiments.md) — `tf experiments` コマンドが操作する対象
- [AI エージェントから thinkingface を使う](../guides/agents.md) — `tf mcp` とエージェントでの作業の流れ
- [互換性](compatibility.md) — `huggingface_hub` と git 経由で動作が確認されていること
