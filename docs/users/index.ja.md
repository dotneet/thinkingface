# thinkingface

thinkingface は、Hugging Face Hub をセルフホストするためのクローンです。データセットとモデルの
リポジトリをホストし、git + Git LFS でバージョン管理し、実データは自分たちの Google Cloud
Storage バケットに保存し、学習の run を記録します。しかもそれらはすべて、`huggingface_hub` と
`datasets` がすでに話し方を知っている API の背後にあります。

![インスタンス全体の件数と、最近更新されたデータセット・モデルが並ぶ thinkingface のホーム画面](images/home.png)

## 想定している利用者 { #who-it-is-for }

すでに Hugging Face Hub を使っていて、同じワークフローを別の場所で動かしたい方に向けたもので
す。自分たちのインフラで、自分たちのネットワークの内側で、自分たちが管理するストレージの上で。
`HF_ENDPOINT` を thinkingface のインスタンスに向けるだけで、既存の Python コードはそのまま動き
ます。コードの書き換えも、フォークの持ち込みも、専用 SDK も必要ありません。

## できること { #what-you-get }

- **データセット・モデルのリポジトリ** — Web UI、API、`tf` CLI のどれからでも作成でき、そのあと
  は HTTP または SSH 経由で `git clone` / `git push` できます。大きなファイルは Git LFS が扱い
  ます。
- **Hugging Face 互換の API** — `whoami`、`create_repo`、`upload_file`、`hf_hub_download`、
  `list_repo_files`、`load_dataset` といった関数が、そのまま自分のインスタンスに対して動きます。
- **ダウンロードせずに中身を見る** — ファイルツリー、Parquet のテーブルビューア、そして
  safetensors や PyTorch ファイルのヘッダーだけを読むチェックポイントのメタデータパネル。
- **実験のトラッキング** — trackio 互換のインターフェースがプロジェクト・run・config・メトリクス
  を記録し、Web UI 上でグラフにします。
- **Organization** — `admin` / `write` / `read` のロールを持つ、チームで共有するネームスペース。
- **そのまま読めるストレージ** — オブジェクトは内容アドレス方式のキーで自分のバケットに置かれ、
  Web UI は任意のリビジョンを元のファイル構成のまま復元する `gcloud storage cp` スクリプトを生成
  します。復元先はローカルでも、別のバケットでも構いません。

## どこから読むか { #where-to-start }

| やりたいこと | 参照先 |
|---|---|
| インスタンスを立ち上げてデータセットをアップロードする | [クイックスタート](getting-started.md) |
| まずリポジトリ・リビジョン・ストレージの考え方を理解する | [基本コンセプト](concepts.md) |
| Python・CLI・git からファイルを送る | [ファイルのアップロード](guides/uploading.md) |
| ファイルを取り出す、あるいはバケットから直接読む | [ファイルのダウンロード](guides/downloading.md) |
| ブラウザでデータセットやチェックポイントを見る | [Web UI を使う](guides/web-ui.md) |
| 学習の run を記録して比較する | [実験のトラッキング](guides/experiments.md) |
| チームで共有するネームスペースを用意する | [Organization](guides/organizations.md) |
| Hub と何が互換で何が互換でないかを正確に知る | [互換性](reference/compatibility.md) |
| 本番環境にデプロイする | [デプロイ](self-hosting/deployment.md) |

導入を決める前に評価したいという段階であれば、thinkingface が実際にデータをどう扱うのかを最短で
把握できるのは [基本コンセプト](concepts.md) です。
