# parraform 実装方針

## 課題意識

`terraform` はremote stateのロックを使う場合、CIなどで並列に `plan` が走ると
ロックの取り合いが発生し、CIが失敗する。`plan` はstateを書き換えないため、
本来ロックを取得する必要はない。

## 解決方針

`parraform` は `terraform` コマンドへの透過ラッパー。

- `plan` 実行時はロックを取得せず、バックエンドの実ロック状態を読み取り専用で
  チェックするのみとし、安全な並列実行を可能にする。
- `apply` など state を書き換えるコマンドは通常通りロックを取得・チェックし、
  排他制御を維持する。
- Go実装。cobraを使い、bash/zsh/fishの補完スクリプトを出力できる。
- terraformの全コマンドと互換性を持つ（完全パススルー）。

## アーキテクチャ

### 実行方式

`terraform` 実バイナリへの薄いラッパーとして動作する。

- Unix: `syscall.Exec` でプロセス置換する。stdio・TTY判定・シグナル・終了コードが
  実行結果と完全に同一になり、追加のシグナル転送コードが不要。
- Windows: `exec.Command` + stdio継承、`ExitError.ExitCode()` で終了コードを反映。

チェック処理は exec の**前**に行うため、プロセス置換方式でも情報は失われない。

### バイナリ解決

- `PARRAFORM_TERRAFORM_BIN` 環境変数を優先。
- 未設定時はPATH探索。ただし `os.Executable()` と同一絶対パスに解決する
  エントリは除外する（`terraform` という名前でparraformにシンボリックリンク
  された場合の無限再帰を防ぐ）。

### plan時のロック回避

`TF_CLI_ARGS_plan` 環境変数に `-lock=false` を注入する（既存の値があれば追記）。
argvの書き換えは不要。

検証済みの事実（ローカルbackendで確認）:

- `TF_CLI_ARGS_plan="-lock=false"` を設定すると、plan はロック未取得で実行できる。
- ユーザーが明示的にコマンドラインで `-lock=true` / `-lock=false` を渡した場合、
  env注入より明示フラグが優先される（TF_CLI_ARGS はサブコマンド直後に挿入され、
  後勝ちのフラグパース規則により明示フラグが勝つ）。
- `terraform state list` / `terraform show` などの読み取り専用コマンドは
  もともとロックを取得しない。

対象コマンドは `plan` のみ（`plan -destroy` / `plan -refresh-only` も含む）。
`apply` / `import` / `refresh` / `state mv` 等、state を書き換えるコマンドは
一切変更しない完全パススルーとし、terraform標準のロック挙動を維持する。

### バックエンド対応ロックチェック

`plan` 実行前に、設定されているバックエンドの実ロック状態を読み取り専用（peek）で
確認し、ロック保持者がいれば警告を表示する。peekは副作用のない読み取りのみで
完結し、terraform自身のLock/Unlock RPCは一切呼ばない。

バックエンド種別は `.terraform/terraform.tfstate`（`terraform init` 後にキャッシュ
される backend 設定。type/config属性を平文で含む）をパースして判別する。
HCLパースは不要。

**ワークスペース対応が必須**であることを実機検証で確認した。非defaultワーク
スペースではロック対象のパス/キーがdefaultと異なるため、backend設定だけから
ロック識別子を組み立てるとdefault以外のワークスペースで誤動作する
（並列plan/applyがワークスペース単位で走るCIではこれがまさに主要ユース
ケース）。現在のワークスペースは `TF_WORKSPACE` 環境変数を優先し、なければ
`.terraform/environment`（defaultの場合はファイル自体が存在しない）を読んで
判定する。各backendのロック識別子は以下の通りワークスペースを畳み込む
（実機・公式ドキュメントで確認済み）:

- local: default以外は `<pathのdir>/terraform.tfstate.d/<workspace>/<pathのbasename>`
- S3: default以外は `<workspace_key_prefix>/<workspace>/<key>`
  （`workspace_key_prefix` のデフォルトは `env:`）
- GCS: `<prefix>/<workspace>.tflock`。**defaultも特別扱いされず**
  `<prefix>/default.tflock` になる（terraform本体のソース
  `internal/backend/remote-state/gcs/backend_state.go` の `stateFile`/
  `lockFile` を確認済み。S3/localとは違いdefaultの特別扱いがない点に注意）。
- azurerm: default以外は `<key>` に `"env:" + workspace` を**区切り文字なしで
  文字列連結**する（terraform本体のソース `internal/backend/remote-state/azure/
  backend_state.go` の `Backend.path()` を確認済み。他backendの `/` 区切りとは
  異なる独特な命名）。ロックはstate blob自体のリース（別オブジェクトではない）
  で行われ、ロック情報（Who等）はblob本体ではなく `terraformlockid` という
  blobメタデータキーにbase64+JSONで格納される（`client.go` のLock()実装を
  確認済み）。
- consul: default以外は `<path>` に `"-env:" + workspace` を区切り文字なしで
  連結（`backend_state.go` の `statePath()` を確認済み）。ロック情報は
  `<path>/.lockinfo` という別KVキーに書き込まれ、Unlock()で明示的に削除される
  （`client.go` を確認済み）ため、このキーの存在確認だけでロック状態を正確に
  判定できる（Consulセッションの検査は不要）。
- kubernetes: GCSと同様にdefaultの特別扱いはなく、Lease名は
  `"lock-tfstate-<workspace>-<secret_suffix>"`（`client.go`の
  `createSecretName`/`createLeaseName`を確認済み）。ロック中かどうかは
  Leaseオブジェクトの存在ではなく`Spec.HolderIdentity`が非nilかどうかで
  判定する必要がある（Unlock()はLeaseを削除せず`HolderIdentity`をnilに
  戻すだけ、`client.go`のUnlock()実装を確認済み）。

### 対応バックエンド一覧（remote stateに設定可能な全種別が対象）

現在のterraform公式ドキュメントに掲載されているbackend種別は以下の11種類
（`local`を除く）。**このうち到達可能なもの全てにLockCheckerを実装する**の
が方針。

| Backend | Peek方法 | 必要権限/前提 | 状態 |
|---|---|---|---|
| local | state ファイルへの non-blocking flock 試行(即解放) | 不要 | 実装済み |
| s3 (native lockfile, `use_lockfile`, TF≥1.11) | `<effective key>.tflock` の GetObject | `s3:GetObject` | 実装済み |
| s3 (legacy, DynamoDB) | `LockID="<bucket>/<effective key>"` で GetItem | `dynamodb:GetItem` | 実装済み |
| gcs | `<prefix>/<workspace>.tflock` の存在確認(NewReader) | `storage.objects.get` | 実装済み |
| azurerm | state blob の `x-ms-lease-status` ヘッダ確認(GetBlobProperties) | blob読み取り権限 | 実装済み |
| remote (`backend "remote"`, TFC/TFE) | `go-tfe` の `Workspaces.Read` で `Locked` | TFEの読み取りトークン(`TF_TOKEN_*`/`token`属性/`credentials.tfrc.json`) | 実装済み(要注意、後述) |
| cloud (`cloud{}`ブロック、TFC/TFE) | 同上。`workspaces.name`固定のみ対応、`tags`/`project`による動的ワークスペース解決は非対応 | 同上 | 実装済み(範囲限定) |
| consul | `<path>/.lockinfo` キーへのKV GET(存在確認) | `kv:read` (ACL有効時) | 実装済み |
| kubernetes | `coordination.k8s.io/v1 Lease` の `holderIdentity` 確認(GET) | leaseへのget権限 | 実装済み |
| pg (Postgres) | advisory lockへの非ブロッキング試行+即解放(localと同じ手法) | 接続権限のみ | 未実装 |
| oss (Alibaba Cloud OSS) | ロック方式を一次情報で確認できず | - | **意図的に未対応**(後述) |
| cos (Tencent Cloud COS) | ロック方式を一次情報で確認できず | - | **意図的に未対応**(後述) |
| oci (Oracle Cloud Infrastructure) | ロック方式を一次情報で確認できず | - | **意図的に未対応**(後述) |
| http | Peek手段なし(LOCK/UNLOCKのみでpeek用APIがcontractに存在しない) | - | 対応不可(ポータブル動作にフォールバック) |

**remote/cloudの注意点**: `workspaces` ネストブロックがキャッシュJSON内でどの
形（単一map / 要素数1のlist）で表現されるかは、実際のTFC/TFEアカウントを
使ったキャッシュファイルで検証できていない（terraformのschemaソースからの
推測に留まる）。抽出コードは両方の形を試し、想定外の形なら安全に
`supported=false`へフォールバックする設計にしてあるため、誤ったロック識別子
で偽の警告を出す心配はない（最悪ケースはチェックが黙ってスキップされるだけ）。
TFC/TFEアカウントで実際に検証できる機会があれば要再確認。

**oss/cos/ociを意図的に未対応とした理由**: この3つはS3/GCS/AzureRMのように
terraform本体のソースを直接参照してロック識別子の組み立て方を検証する
ところまで到達できておらず、一次情報のない推測でLockCheckerを書くと
「間違ったロック識別子で自信満々に警告を出す/出さない」という、ワークスペース
バグで一度踏んだのと同じ失敗を再現しかねない。設定の抽出だけ失敗するのと
違い、ロック機構そのものの理解が不確実なため、`supported=false`にすらならず
誤答するリスクがある。よって未登録のまま（`lockcheck.For`が見つからず
ポータブル動作にフォールバック）とし、一次情報（terraform本体のソース、
または実際のクラウド環境での検証）にアクセスできた時点で追加する。

`LockChecker` インターフェースで抽象化し、バックエンドごとに実装を追加。
各SDKの依存分離は行わず、**単一バイナリに全部同梱**する方針で決定済み
（ビルド・配布のシンプルさを優先。バイナリサイズ・ビルド時間の増加は許容）。

### 利用ライブラリの検討

terraformを直接操作する既存ライブラリの採用可否を検討した結果:

- **`hashicorp/terraform-exec`**: 不採用。サブコマンドごとに型付きAPIを持つため
  「terraformの将来のフラグも含めて全部素通しする」という要件と相性が悪く、
  tfexec自身がterraformの新フラグに追従するまで使えなくなる。対話的な承認
  プロンプトのTTY継承も `syscall.Exec` によるプロセス置換ほどの等価性は
  保証されない。execレイヤーは自前の薄いラッパーのままとする。
- **`hashicorp/hcl` / `hcl/v2`**: 不採用。backend設定ブロックはinterpolationを
  許さない（静的値のみ）ため、`terraform init` 後に `.terraform/terraform.tfstate`
  へ平文キャッシュされる値で判別できる。HCLパーサは不要で `encoding/json` で
  十分。
- **`hashicorp/hc-install`**: 不採用。バージョン管理/ダウンロード用ライブラリで
  あり、今回は「既にPATHにあるterraformに委譲する」だけなのでスコープ外。
- **terraform本体の内部ロック実装**（`internal/backend/remote-state/*`）:
  Goの `internal/` 可視性制限で外部からimport不可。S3/GCS/Azureのpeekは
  各クラウドSDK（`aws-sdk-go-v2` のs3/dynamodbクライアント、
  `cloud.google.com/go/storage`、`azure-sdk-for-go`）を直接叩いて自前実装する
  しかない。
- **`hashicorp/go-tfe`**: 採用。Terraform Cloud/Enterpriseをbackendに使う場合、
  `Workspaces.Read` 一発でロック状態（`Locked`/`LockedBy`）が取得できるため、
  他クラウドバックエンドより実装コストが低い。

バックエンドごとに異なるクラウドSDKへの依存が増える点は将来的な懸念事項。
使わないバックエンドのSDKまでバイナリに含めたくない場合はbuild tagでの
分離を検討する（未決定）。

**ロック検出時の挙動**: デフォルトは「警告表示して続行」。plan自体は
`-lock=false` のまま実行する（ここでブロックすると並列plan問題を別の形で
再現してしまうため）。fail-fastにする `-lock-check=strict` 等のオプションは
将来の拡張候補とし、初期実装のスコープには含めない。

### cobra / シェル補完

ルートコマンドは `DisableFlagParsing: true` とし、完全パススルーを守る。

terraformの主要サブコマンド（`plan` / `apply` / `destroy` / `state` /
`workspace` / `import` / `refresh` / `console` / `fmt` / `validate` /
`output` / `show` / `taint` / `untaint` / `force-unlock` / `graph` /
`providers` / `init` など約25個）をcobraのサブコマンドとして個別に登録し、
それぞれ `DisableFlagParsing: true` で同一のexecパスに委譲する。これにより
cobraが生成する補完スクリプトが実際にterraformコマンド名を補完するようになる。
`version` もterraform本来のコマンドとして一覧に含め、そのままterraformへ
パススルーする（`terraform version` の透過性を壊さないよう、parraform自身の
バージョン表示に `version` サブコマンドを流用しない。parraform自身のバージョン
確認手段は未実装・スコープ外）。`completion` はterraformに存在しないコマンド名
なので衝突せず、cobraの標準実装をそのまま使う。

terraformのバージョンアップでサブコマンドが増減した場合、この一覧を追従させる
保守コストが発生する点は許容する。

### 安全性の論拠

`-lock=false` でのplanが安全な理由: S3/GCSへの書き込みはアトミックで
read-after-write一貫性があるため、apply中のplanが「壊れた」状態を読む
（torn read）ことはない。最悪でも「apply完了直前のスナップショットを読む」
だけであり、破損は起きない。バックエンドロックのpeekはこれに加えて
「apply進行中である」ことを利用者に知らせるための追加シグナル。

**実機検証済み**: `plan -out=` で保存したplanファイルは、保存後に別の操作
（`taint` 等、設定変更を伴わない操作でも可）でstateのserialが進むと、
`apply <planfile>` 実行時に `Error: Saved plan is stale` で明示的に失敗する
ことを確認した。すなわち「CIでplanを保存し、別ジョブでapplyする」という
典型的なワークフローにおいても、unlocked planが多少古いstateを読んでいた
場合はapply時にfail-closedし、古い前提でのapplyが誤って実行されることは
ない。これにより安全性の主張は「破損しない」に加えて「stale planはapply時
に確実に弾かれる」まで含めて成立する。

## テスト方針

- ロック回避・パススルー判定などの純粋関数はterraform非依存でtable-driven test。
- passthrough・終了コード・シグナル系はPATH上にargvをechoして終了コードを返す
  フェイクスクリプトを置いてテストし、実terraformやクラウド認証情報を不要にする。

## 実装済みの既知の制約

- ロックpeekのタイムアウトはデフォルト3秒（`PARRAFORM_LOCK_CHECK_TIMEOUT`で
  変更可、0以下で無効化）。この待ち時間は**毎回のplan実行に確実に加算される
  レイテンシ**であり、クラウドSDKのデフォルト認証チェーン解決（IMDSプローブの
  リトライ、`DefaultAzureCredential`のフォールバック探索等）がこれを超えると
  peekはエラー扱いになり、警告は出ない。タイムアウトと「未ロック」はユーザー
  から見て区別できない（意図的なトレードオフ。速度を優先し、peek失敗時に
  余計なノイズを出さない設計を維持するため、明示的にこの形で決定した）。
- azurermのpeekは `access_key`（共有キー）か、なければAzure SDKの
  `DefaultAzureCredential`（Azure CLIログイン/環境変数/MSI等）のみ対応。
  `client_secret`/OIDC/サービスプリンシパル証明書などterraform本体が
  対応する認証方式の大半はMVPの対象外（該当構成ではAzure SDK側の
  `azidentity` オプションを追加実装する必要がある）。
- consulのpeekは `address`/`scheme`/`datacenter`/`access_token` のみ対応。
  `ca_file`/`cert_file`/`key_file`（mTLS）は未対応（該当構成では
  `consulapi.Config` にTLS設定を追加実装する必要がある）。
- kubernetesのpeekは `in_cluster_config` / `config_path` /
  `config_context` のみ対応。`host`+トークン単体指定や、クラウド各社の
  exec形式認証プラグイン（`aws eks get-token`等）はMVPの対象外。
- S3のpeekで `s3:GetObject` はあるが `s3:ListBucket` がないIAMロールの場合、
  存在しない `.tflock` オブジェクトへのGetObjectは `NoSuchKey` ではなく
  `403 AccessDenied` になりうる。この場合 `isNotFound` が false を返して
  `Peek` はエラーを返し、`warnIfLocked` は黙ってチェックをスキップする
  （「未ロック」と「判定不能」が区別できない）。fail-silent設計とは
  整合するが、既知の限界として明記しておく。
- S3バックエンドのpeekはbucket/key/region/profile/dynamodb_table/use_lockfile
  のみに対応。カスタムS3互換エンドポイント（LocalStack/MinIO等）や
  `assume_role`によるAWSクレデンシャル取得はMVPの対象外（`config.LoadDefaultConfig`
  のデフォルトチェーンに委ねる）。該当する構成では `s3.NewFromConfig`
  やSTS AssumeRoleへの対応を別途追加する必要がある。
- CI環境側が既に `TF_CLI_ARGS_plan="-lock=true"` のように設定している場合、
  parraformが末尾に追記する `-lock=false` が最後勝ちルールで優先され、
  parraformの意図（ロック未取得での実行）が黙って勝つ。これはツールの目的
  上妥当な挙動だが、コマンドラインでの明示指定が優先されるのとは逆方向
  なので明記しておく。

- `backend {}` ブロックを省略した暗黙のデフォルトlocalバックエンドの場合、
  `terraform init` は `.terraform/terraform.tfstate` キャッシュファイル自体を
  書き出さない（実機確認済み）。この場合 `backendcfg.Discover` は `(nil, nil)`
  を返し、ロックチェックは黙ってスキップされる（ポータブル動作にフォール
  バックするのと同じ扱い）。`backend "local" {}` を明示している場合は
  キャッシュが書かれ、チェックは正しく機能する。S3/GCS等の非localバックエンド
  は暗黙のデフォルトが存在しないため、この制約の影響を受けない。

## スコープ外（明示的に対象外とした項目）

- `apply` への `-lock-timeout` デフォルト注入などの隣接機能。
- ロック検出時にブロック/待機するstrictモードの実装（インターフェースは
  拡張余地を残すのみ）。
- OpenTofu (`tofu`) 対応可否は未決定（バイナリ解決部分のみに影響する小さな決定）。
