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

| Backend | Peek方法 | 必要権限 |
|---|---|---|
| S3 (native lockfile, `use_lockfile`, TF≥1.11) | `<key>.tflock` オブジェクトの HeadObject/GetObject | `s3:GetObject` |
| S3 (legacy, DynamoDB) | `LockID = "<bucket>/<key>"` で GetItem | `dynamodb:GetItem` |
| GCS | `<prefix>/<name>.tflock` オブジェクトの存在確認（object generation） | `storage.objects.get` |
| Local | state ファイルへの non-blocking flock 試行（即解放） | 不要（検証済み） |
| AzureRM | state blob の `x-ms-lease-status` ヘッダ確認 | 実装時に要検証 |
| Terraform Cloud/Enterprise（`backend "remote"` / `cloud`ブロック） | `Workspaces.Read` の `Locked`/`LockedBy` を参照 | TFEの読み取りトークン |
| それ以外（http, Consul, Postgres 等） | Peek手段なし → ポータブル動作にフォールバック（チェック省略、`-lock=false` のみ適用） |

`LockChecker` インターフェースで抽象化し、バックエンドごとに実装を追加できる形に
する。初期実装は **S3（native lockfile優先、DynamoDBフォールバック）+ local** から
着手し、GCS / AzureRM / Terraform Cloud は後続で追加する。TFC/TFEはAPI一発で
ロック状態が取れるため、S3/GCS/Azureより実装コストは低い。優先度は要件次第で
前後してよい。

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
