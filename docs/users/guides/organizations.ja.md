# Organization

Organization は、チームのための共有ネームスペースです。リポジトリ、メンバー、ロール、ポリシーが
1 つの場所にまとまっています。このページでは、Organization の作成方法、各ロールでできること、
メンバーの管理方法、そして `huggingface_hub` から見た Organization リポジトリの振る舞いについて
説明します。

## Users and organizations share one namespace { #users-and-organizations-share-one-namespace }

ネームスペースは 1 つしかありません。`admin`（ユーザー）と `acme`（Organization）は同じ種類の
名前であり、どちらも Web UI では `/{name}` で応答します。Organization が所有するリポジトリは、
単に `{org}/{name}` となります。

```text
admin/imdb-reviews        a repository in a user namespace
acme/sentiment-base    a repository in an organization namespace
```

そのため、クライアント側はこの違いを一切意識する必要がありません。`huggingface_hub` は
`acme/sentiment-base` を他のあらゆるリポジトリ ID とまったく同じように扱います — 同じ
`create_repo`、同じ `upload_file`、同じ `hf_hub_download`、同じ `git clone` です。

あらかじめ知っておくべき帰結が 1 つあります。名前は一度しか取得できません。`acme` がすでにユーザー
アカウントとして存在する場合、Organization として使うことはできません。

## Create an organization { #create-an-organization }

Web UI でアカウントメニューを開き、**New organization**
(<http://localhost:3000/orgs/new>) を選択します。入力する項目は次のとおりです。

- **Organization ID** — ネームスペースです。すべての URL とリポジトリ名に登場し、**後から変更す
  ることはできません**。リポジトリ名と同じ規則に従います。1〜96 文字の英数字、ドット、ハイフン、
  アンダースコアで構成し、先頭は英数字である必要があり、`.git` で終わってはいけません。
- **Display name** — 任意項目です。Organization ページで ID の代わりに表示され、いつでも編集で
  きます。
- **Description** — 任意項目です。

作成した本人が Organization の最初のメンバーとなり、admin になります。

!!! note

    誰が Organization を作成できるかは、インスタンス全体の設定です。`TF_ORG_CREATION` は既定で
    `anyone` になっており、`admin` にすると作成をサイト管理者に限定できます。
    [設定](../self-hosting/configuration.md) を参照してください。

## Roles { #roles }

各メンバーは、Organization 内で 3 つのロールのうちいずれか 1 つを必ず持ちます。ロールには順序が
あり、それぞれ下位のロールが持つ権限をすべて含みます。

| Operation | Non-member | `read` | `write` | `admin` |
|---|---|---|---|---|
| Organization のページとリポジトリ一覧を閲覧する | はい | はい | はい | はい |
| 読み取り、クローン、ダウンロード、実験の閲覧 | はい | はい | はい | はい |
| メンバー一覧を閲覧する | ポリシーが public の場合のみ | はい | はい | はい |
| Organization のストレージ使用量を閲覧する | いいえ | はい | はい | はい |
| ネームスペース内にリポジトリを作成する | いいえ | いいえ | はい | はい |
| push、コミット、ブラウザでの編集、実験メトリクスの取り込み | いいえ | いいえ | はい | はい |
| Organization へのリポジトリ移管を受け入れる | いいえ | いいえ | はい | はい |
| リポジトリを削除する | いいえ | いいえ | いいえ | はい |
| リポジトリをアーカイブ / アーカイブ解除する | いいえ | いいえ | いいえ | はい |
| Organization からリポジトリを移管する | いいえ | いいえ | いいえ | はい |
| webhook を管理する | いいえ | いいえ | いいえ | はい |
| メンバーの追加・削除、ロールの変更 | いいえ | いいえ | いいえ | はい |
| プロフィールとポリシーを編集する | いいえ | いいえ | いいえ | はい |
| 監査ログを閲覧する | いいえ | いいえ | いいえ | はい |
| Organization を削除する | いいえ | いいえ | いいえ | はい |
| Organization を脱退する | — | はい | はい | はい（最後の admin である場合を除く） |

サイト管理者は、メンバーになっていなくてもすべてのネームスペースに対して `admin` 権限を持ちます。
メンバー一覧には決して現れず、その Organization は `whoami()["orgs"]` にも表示されません。

!!! warning "There is no repository visibility"

    thinkingface には、リポジトリの公開・非公開という区別が意図的にありません。このインスタンス
    上のすべてのリポジトリは、サインアウトした訪問者を含め、インスタンスに到達できる全員が読み取
    れます。ロールが統制するのは**書き込み**とメンバーシップ自体であり、読み取りアクセスではあり
    ません。データを読み取られたくない場合は、インスタンスに置かないか、インスタンス全体へのアク
    セスを制限してください。

    `huggingface_hub` は、既存のコードを壊さないよう、`create_repo` の `private=True` /
    `visibility="private"` を引き続き受け付けます。ただし値はデコードされるだけで無視されます。

つまり `read` が実際に付与するのはメンバーシップです — メンバー一覧、Organization のストレージ
使用量、そしてチームの一員として名前が載ることです。`write` はリポジトリの作成と push を追加しま
す。`admin` は、破壊的な操作や管理的な操作をすべて追加します。

`write` が意図的に含まないものが 2 つあります。リポジトリの削除と、webhook の管理です。webhook
はネームスペースの秘密情報を外部の URL に送り出しますし、削除は取り消せません — どちらもコンテン
ツの変更というより管理的な行為だからです。

## Manage members { #manage-members }

メンバーは Organization の **Settings → Members**（`/orgs/acme/settings/members`）で管理し
ます。

![Organization のメンバーページ。各メンバーのロールと参加日が一覧表示されている](../images/org-members.png)

- **メンバーを追加する** にはユーザー名を指定します。追加対象は、このインスタンスに既にアカウン
  トを持っている必要があります — メールによる招待機能はありません。ロールを選ばない場合の既定は
  `read` です。
- **ロールを変更する** には、その行のロール操作を使います。
- **メンバーを削除する** には、その行から操作します。
- Organization を **脱退する** には、自分自身を削除します。操作としては同じものです。

Organization には常に少なくとも 1 人の admin が残るようになっています。最後の admin を削除しよ
うとしたり、降格させようとしたりすると、先に別の admin を任命するよう伝えるメッセージとともに拒
否されます。

### Who can see the member list { #who-can-see-the-member-list }

これは Organization の **Policy** 設定で制御します。

| `members_visibility` | メンバー一覧を閲覧できる人 |
|---|---|
| `members`（既定） | Organization のメンバーのみ |
| `public` | サインアウトした訪問者を含む、誰でも |

メンバーでない人が公開された名簿を読む場合、メールアドレスを含まないユーザー名だけが表示されます。

## Work with organization repositories from Python { #work-with-organization-repositories-from-python }

変わるのはネームスペースの部分だけです。いつもどおりエンドポイントとトークンを設定してから、次の
ようにします。

```python
import os
from huggingface_hub import HfApi

os.environ["HF_ENDPOINT"] = "http://localhost:8080"
os.environ["HF_TOKEN"] = "tf_xxxxxxxxxxxx"
os.environ["HF_HUB_DISABLE_XET"] = "1"

api = HfApi()

# Create under the organization: the namespace is just the first segment.
api.create_repo("acme/sentiment-base", repo_type="model", exist_ok=True)

api.upload_file(
    path_or_fileobj="config.json",
    path_in_repo="config.json",
    repo_id="acme/sentiment-base",
    repo_type="model",
)
```

所属している Organization と、それぞれでの自分のロールは `whoami()` から取得できます。

```python
for org in api.whoami()["orgs"]:
    print(org["name"], org["roleInOrg"])
```

```text
acme admin
```

名簿も同様です。

```python
for member in api.list_organization_members("acme"):
    print(member.username)
```

`list_organization_members` は Web UI と同じ可視性のルールに従います。メンバーには常に見え、そ
れ以外の人にはポリシーが `public` の場合にのみ見えます。

`read` のメンバーが Organization のリポジトリに push すると、HTTP 403 が返ります。`write` に昇
格させれば、同じ push が成功するようになります — それ以外に相手側で変更すべきことはなく、トーク
ンもそのままで構いません。

## Organization settings { #organization-settings }

設定画面は `/orgs/{org}/settings` にあり、**admin のみ**アクセスできます。admin ロールを持たな
いメンバーには、404 ではなく「admins only」という明示的なメッセージが表示されます。Organization
の存在自体は公開情報だからです。

| Screen | What it does |
|---|---|
| **Profile** | 表示名、説明、Web サイト、アバター URL（画像を外部でホストしているものへのリンクで、アップロード機能はありません）。ネームスペース名は固定です。 |
| **Policy** | 上で説明した `members_visibility`。 |
| **Members** | 追加、昇格、降格、削除。上で説明したとおりです。 |
| **Webhooks** | Organization のリポジトリで発生したイベント（プッシュ、リポジトリのライフサイクル、転送、実験 run のステータス）を通知する HTTP エンドポイント。9 種類のイベント全部、ペイロード、署名の検証、リトライ方針は [Webhook](webhooks.md) を参照してください。 |
| **Storage** | Organization のリポジトリがオブジェクトストレージ上に保持する LFS のバイト数を、リポジトリごとに内訳表示します。 |
| **Audit log** | 管理上の変更とリポジトリのライフサイクルイベントを、新しい順に表示します。 |
| **Delete organization** | 危険な操作です。詳しくは後述します。 |

admin ロールを持たないメンバーも、Organization のストレージ使用量は閲覧できます。自分の
**Storage usage** ページ（`/settings/storage`）に、所属するすべてのネームスペースが一覧表示され
ます。

### The audit log { #the-audit-log }

各エントリには、いつ発生したか、誰が行ったか、何が行われ、何に対してだったかが記録されます。記録
される操作は次のとおりです。

```text
org.created            org.updated
member.added           member.role_changed      member.removed      member.left
repo.created           repo.deleted
repo.transferred_in    repo.transferred_out
webhook.created        webhook.updated          webhook.deleted
```

Organization の削除自体は、その監査ログには記録されません — ログはネームスペースに紐づいており、
Organization と一緒に削除されるためです。このイベントは代わりにサーバーのプロセスログに記録され
ます。

### Deleting an organization { #deleting-an-organization }

削除すると、Organization のメンバー、webhook、監査ログが失われ、名前は再利用できるようになりま
す。**リポジトリは決して削除されません**。そのため、Organization に所属するリポジトリが 1 つで
も残っている間は削除が拒否されます。先にリポジトリを削除するか移管してください。

## Transfer a repository between namespaces { #transfer-a-repository-between-namespaces }

リポジトリはネームスペース間を移動できます — 自分の個人ネームスペースから Organization へ、2 つ
の Organization の間、あるいは個人ネームスペースへ戻すこともできます。リポジトリの **Settings**
タブを開き、**Transfer ownership** を使います。同じフォームから、同一ネームスペース内でのリネー
ムも行えます。

Git の履歴、LFS オブジェクト、ダウンロード数はそのまま引き継がれ、オブジェクトストレージ上でバイ
トが移動することもありません — オブジェクトキーはリポジトリ名ではなくコンテンツから導出されてい
るためです。

誰が実行できるか:

- **移管元**では、移管元ネームスペースの admin である必要があります。Organization であれば
  `admin` ロールを意味するため、`write` のメンバーがチームに無断でリポジトリを持ち出すことはでき
  ません。個人ネームスペースでは所有者だけが admin なので、この点は変わりません。
- **移管先**では、すでにそこにリポジトリを作成できる立場（`write` 以上）であれば、移動は即座に完
  了します。そうでない場合は移管リクエストが登録され、移管先が受け入れるのを待ちます。

保留中の移管（受信・送信の両方）は **Repository transfers**（`/settings/transfers`）に一覧表示
され、移管先が承認・拒否でき、依頼した側はキャンセルできます。誰も応答しないリクエストは 7 日後
に期限切れになります。

Python からは同じ操作を `move_repo` で行います。

```python
api.move_repo(from_id="admin/sentiment-base", to_id="acme/sentiment-base", repo_type="model")
```

!!! tip

    移動後も古いリポジトリ ID は使い続けられます — 読み取りも書き込みもリダイレクトされ、
    `git clone` / `git pull` もリダイレクトに従います。移管した当日にすべてのスクリプトを更新す
    る必要はありません。

アーカイブされたリポジトリは移管できません。先にアーカイブを解除してください。

## Related pages { #related-pages }

- [基本コンセプト](../concepts.md) — ネームスペース、リポジトリ、リビジョン
- [ファイルのアップロード](uploading.md) — `write` を持っている状態でリポジトリに push する
- [認証](../reference/authentication.md) — トークンと、その読み取り / 書き込みスコープ
- [実験のトラッキング](experiments.md) — 実験リポジトリをチームで共有する
- [設定](../self-hosting/configuration.md) — `TF_ORG_CREATION` などのインスタンス設定
