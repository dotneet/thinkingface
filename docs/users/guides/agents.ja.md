# AI エージェントから thinkingface を使う

AI コーディングエージェント（Claude Code、あるいは Model Context Protocol を話す任意のクライアント）
は、本来なら人が手で回す実験ループを代わりに回せます。学習ジョブを起動し、終わるのを待ち、メトリクスを
読み、以前の run と比較し、わかったことを書き残す、という流れです。thinkingface はエージェント向けに
3 つの入口を用意しており、いずれも同じ API と同じアクセストークンの上に成り立っています。

| 入口 | 向いている用途 |
|---|---|
| MCP サーバー `tf mcp` | MCP に対応したエージェント。実験用のツールがネイティブのツールとして現れ、引数と制限がモデルに説明されます。 |
| `--json` 付きの `tf` CLI | シェル経由で作業するエージェントやスクリプト。すべてのコマンドが機械可読な JSON を stdout に出力します。 |
| `/api/openapi.json` で記述された HTTP API | それ以外のすべて。自前のツールや他の言語から使う場合です。 |

このページでは、それぞれの入口、典型的なループ、そしてエージェントのトークンを必要最小限に保つ方法を
説明します。run 自体はいつもどおり学習スクリプトが記録します。[実験のトラッキング](experiments.md)
を参照してください。

## エージェント専用のトークンを渡す { #give-the-agent-its-own-token }

エージェントには自分のトークンではなく、エージェント専用のトークンを渡してください。具体的には
**作業対象の実験リポジトリに制限し、有効期限を付けた write トークン**です。これでエージェントは、その
1 つのリポジトリで run を記録し、注釈を付け、メトリクスの目標を設定し、プロジェクトのノートを編集
できます。それ以外のことはできません。他のリポジトリへの push、何かの削除や名前変更、新しいトークン
の発行はいずれも不可能です。読み取りはこの制限で絞られないため、エージェントはもともと読めたリポジ
トリをすべて引き続き読めます。

トークンは **Settings → Access tokens** で作成します。スコープは **write**、有効期限は 7 日または
30 日とし、**Restrict to repositories** にリポジトリ（例: `datasets/alice/trackio-metrics`）を指定
します。サーバーのコマンドラインから作成することもできます。

```bash
docker compose exec -T api thinkingface admin token create alice \
    --name claude-ocr --expires-in-days 7 --repo datasets/alice/trackio-metrics > agent-token.txt
```

トークンをリポジトリに制限するには、そのリポジトリが先に存在している必要があります。制限付きトー
クンはリポジトリを作成できないからです。トークンによるリクエストにはすべて、そのトークンの ID と名前
がサーバーのアクセスログに記録されるため、エージェントが何をしたかを追跡できます。
[トークンをリポジトリに制限する](../reference/authentication.md#restricting-a-token-to-repositories)
を参照してください。

## MCP サーバー { #the-mcp-server }

`tf mcp` は、`tf experiments` の機能を MCP の stdio トランスポートで提供します。エージェントのクライ
アントがこれをサブプロセスとして起動し、stdin/stdout で通信します。開けるべきポートも、動かし続ける
べきデーモンもありません。先に `tf` をインストールしておいてください（[tf CLI](../reference/tf-cli.md#installation)）。

### 登録する { #register-it }

`tf login` を実行済みのマシンで Claude Code を使う場合は次のとおりです。

```bash
claude mcp add thinkingface -- tf mcp
```

保存済みのログインではなくエージェント専用のトークンを使わせるには、エンドポイントとトークンを環境
変数で渡します。

```bash
claude mcp add thinkingface \
  -e THINKINGFACE_ENDPOINT=http://localhost:8080 \
  -e THINKINGFACE_API_KEY=tf_xxxxxxxxxxxx \
  -- tf mcp
```

JSON で設定するクライアント（プロジェクトの `.mcp.json` や、`mcpServers` マップを持つ任意のクライア
ント）でも、同じコマンドと変数を使います。

```json
{
  "mcpServers": {
    "thinkingface": {
      "command": "tf",
      "args": ["mcp"],
      "env": {
        "THINKINGFACE_ENDPOINT": "http://localhost:8080",
        "THINKINGFACE_API_KEY": "tf_xxxxxxxxxxxx"
      }
    }
  }
}
```

`tf mcp` は、他のすべての `tf` コマンドとまったく同じ方法で資格情報を解決します。`--endpoint` /
`--token` フラグ、次に `TF_ENDPOINT` / `THINKINGFACE_ENDPOINT` / `HF_ENDPOINT` と
`THINKINGFACE_API_KEY` / `TF_TOKEN` / `THINKINGFACE_TOKEN`、最後に `tf login` が保存したログインの
順です（[資格情報の解決順序](../reference/tf-cli.md#credential-resolution-order) を参照）。解決は起動
時ではなく最初のツール呼び出しの時点で行われます。そのため、ログインする前に起動したサーバーも動き
続け、資格情報が用意されるまでは、何をすればよいかを示すエラーで各呼び出しに応答します。

`tf mcp` は `--verbose` を付けて起動しない限り stderr に何も書きません。`--verbose` を付けると資格情報
の解決と各リクエストをログに出すので、クライアントがサーバーの異常を報告したときに役立ちます。

### ツール { #tools }

`repo` は常に `ns/name` 形式の実験リポジトリです。各ツールは API の JSON をそのまま返します。

| ツール | 引数（`?` = 省略可） | 動作 |
|---|---|---|
| `list_experiment_repos` | `author?`, `search?` | 実験を保持しているリポジトリを一覧します |
| `list_projects` | `repo` | リポジトリのプロジェクトを、run 数とメトリクスの目標付きで返します |
| `list_runs` | `repo`, `project`, `group?`, `status?[]`, `tag?[]`, `archived?`, `sort?`, `order?`, `limit?` | サーバー側で絞り込み・並べ替えした run を返します。`sort` には `tf experiments runs --sort` と同じ指定（`best:<metric>` を含む）を使えます |
| `get_run` | `repo`, `project`, `run` | 1 つの run の、ステータス、step、config、全メトリクスの最終値 / 最小値 / 最大値を返します |
| `get_metrics` | `repo`, `project`, `runs[]`, `keys?[]`, `x?`（`step` または `time`）, `max_points?` | メトリクス系列を `[x, y]` の組で返します。`max_points` までダウンサンプリングされます（応答を小さく保つため**デフォルトは 200**、上限は 5000） |
| `wait_for_run` | `repo`, `project`, `run`, `until?`, `timeout_seconds?`, `ignore_stale?` | `tf experiments wait` と同じように待機します（`until` のデフォルトは `status!=running`、`timeout_seconds` のデフォルトは 600、上限は 3600） |
| `config_diff` | `repo`, `project`, `runs?[]`, `include_meta?` | run 間で異なる config キーを返します |
| `get_notes` | `repo`, `project` | プロジェクトの `NOTES.md` を、その `blob_sha` とともに返します |
| `update_notes` | `repo`, `project`, `content`, `base_sha?`, `message?` | ノートを置き換えます |
| `annotate_run` | `repo`, `project`, `run`, `note?`, `tags?[]`, `archived?` | 指定したフィールドだけを変更します。`tags` はリストを置き換えます |
| `set_metric_goals` | `repo`, `project`, `goals`（`{metric: "min" \| "max" \| ""}`） | 目標をマージします。`""` を指定するとその目標を削除します |

エージェントのトランスクリプトを読むときに知っておくとよい挙動がいくつかあります。

- **`wait_for_run` のタイムアウトは通常の応答です。** `met: false` と `reason: "timeout"` が返り、
  エージェントは待ち続けるためにもう一度呼び出します。`reason: "stopped"` は、run が条件を満たさない
  まま終了・失敗・stale になったことを意味し、それ以上待っても意味がありません。1 つの呼び出しが待機
  している間も、他のツールは動き続けます。
- **誤りはプロトコルエラーではなくツールの結果として返ります。** 引数の不足、解釈できない `until`、
  サーバーからの 403、ノートの競合などは、いずれもモデルが対処できる一文とともに返るので、エージェン
  トはそれを見て自分で修正します。
- **`get_notes` で得た `blob_sha` を `base_sha` として `update_notes` に渡す**と、同時編集（Web UI で
  人が同時にノートを編集している場合）は書き込みの消失ではなく、エージェントが解決すべき競合になり
  ます。`base_sha` を渡さなければ上書きします。
- 読み取り専用のツールにはその旨の印（`readOnlyHint`）が付いているため、クライアントは毎回確認を
  求めずにそれらを実行させられます。

## 典型的なループ { #a-typical-loop }

以下は、エージェントがこれらのツールを使って通常行う流れです。同じ手順はシェルから CLI でも実行
できます。

1. **何をもって「良い」とするかを宣言します。** プロジェクトごとに一度行えば、「best」が明確に定義
   されます。`set_metric_goals` に `{"val/CER": "min"}` を渡すか、`tf experiments goals alice/trackio-metrics ocr val/CER=min` を実行します。
2. **学習ジョブを起動します。** trackio シムを使い、run 名はエージェントが決めます。例:
   `THINKINGFACE_REPO=alice/trackio-metrics python train.py --lr 3e-4 --run-name lr-3e-4`
3. **完了を待ちます。** 起動直後から待ち始めて構いません。run がまだ存在していなくても大丈夫です。
   `wait_for_run` に `until: "step >= 12000 or status != running"` を渡すか、コマンドラインでは次の
   ようにします。

    ```bash
    tf experiments wait alice/trackio-metrics ocr lr-3e-4 \
        --until 'step >= 12000 or status != running' --timeout 6h --json
    ```

    クラッシュしたジョブは最後のハートビートから約 2 分で `stale` になり、タイムアウトまで待ち続ける
    のではなく `reason: "stopped"` で待機が終わります。
4. **結果を文脈の中で読みます。** リーダーボードには `sort: "best:val/CER"` と小さな `limit` を指定
   した `list_runs`、新しい run の最終値 / 最小値 / 最大値には `get_run`、これまでのベストの run との
   違いを見るには `config_diff`、曲線の形が重要なときは `get_metrics` を使います。
5. **わかったことを書き残します。** `annotate_run` で短いノートとタグを付け、`update_notes` でプロ
   ジェクトのノートブックに 1 行追加します。`[lr 3e-4](run:lr-3e-4)` のように書いたリンクは Web UI で
   その run へのリンクになるため、ノートを読む人はクリックして曲線まで辿れます。

プロジェクトのノートはリポジトリ内に `{project}/NOTES.md` として置かれます。そのためエージェントの
知見はメトリクスと並んでバージョン管理され、プロジェクトページに表示され、他のコミットと同じように
レビューできます。

## シェルから: どこでも `--json` { #from-a-shell-json-everywhere }

`tf mcp` 自身を除くすべての `tf` コマンド（`login`、`logout`、`status`、`whoami`、`up`、`version`、
そして `tf experiments` のすべて）は `--json` を受け付け、指定すると stdout に JSON オブジェクトを
1 つ出力します。進捗、警告、エラーは stderr に出たままで、終了コードも変わりません（`0` 成功、`1`
失敗、`2` 使い方の誤り）。そのため、スクリプトやエージェントは stdout をフィルタせずにそのままパース
できます。

```bash
best=$(tf experiments runs alice/trackio-metrics ocr --sort best:val/CER --limit 1 --json |
       jq -r '.runs[0].name')
tf experiments diff alice/trackio-metrics ocr "$best" lr-3e-4 --json
```

`tf experiments wait` には独自の終了コード（`0` 条件を満たした、`1` タイムアウトまたは run が停止
した）があるため、スクリプトの中で「学習を開始する」と「結果に応じて動く」の間に置くのにうってつけ
です。すべてのコマンドについては [tf CLI リファレンス](../reference/tf-cli.md#tf-experiments) を参照
してください。

## HTTP API と OpenAPI ドキュメント { #the-http-api-and-its-openapi-document }

`GET /api/openapi.json` は、プログラムから利用する API 面の OpenAPI 3.1 記述を返します。対象は、サイン
イン、アクセストークン、現在のユーザー、サーバー情報、リポジトリの一覧と作成、そして
`/api/v1/experiments` 配下の実験 API 全体です。`servers` エントリにはそのインスタンス自身の
`TF_PUBLIC_URL` が入るため、これから生成したクライアントは手直しなしで使えます。

```bash
curl -s http://localhost:8080/api/openapi.json | jq '.paths | keys'
```

HuggingFace 互換のエンドポイント（`whoami-v2` とリポジトリ作成を除く）と、git / Git LFS のトランス
ポートは意図的に含めていません。これらは `huggingface_hub` と `git` が定義するものだからです。認証は
他の API 呼び出しとまったく同じく `Authorization: Bearer tf_...` で行います。`TF_REQUIRE_AUTH_FOR_READ`
を有効にして動いているインスタンスでは、このドキュメント自体の取得にもトークンが必要です。

## 最小権限のまとめ { #least-privilege-in-short }

- 各エージェントには**専用の write トークンを、作業対象のリポジトリに制限し、有効期限を付けて**渡して
  ください。自分個人の制限なしトークンは決して渡さないでください。
- **リポジトリは事前に作成しておきます。** 制限付きトークンでは作成できません。
- **読み取り専用のツール**はクライアントに確認なしで実行させ、書き込みを行うツール（`annotate_run`、
  `update_notes`、`set_metric_goals`）には目を配ってください。
- **読み取りはインスタンス全体に及ぶ**ことを忘れないでください。トークンが制限するのはエージェントが
  変更できる範囲であって、見られる範囲ではありません。エージェントに読ませてはならないデータは、その
  エージェントがトークンを持たないインスタンスに置いてください。
- 作業が終わったら **Settings → Access tokens** でトークンを失効させるか、期限切れに任せてください。

## 関連ページ { #related-pages }

- [実験のトラッキング](experiments.md) — run の記録、目標、ノート、`tf experiments` コマンド
- [tf CLI](../reference/tf-cli.md#tf-mcp) — `tf mcp` をはじめとするすべてのコマンド
- [認証](../reference/authentication.md#restricting-a-token-to-repositories) — 制限付きトークン
