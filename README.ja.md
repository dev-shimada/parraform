# parraform

<p align="center">
  <a href="https://github.com/dev-shimada/parraform/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/dev-shimada/parraform/ci.yml?branch=main&style=flat-square&logo=githubactions&logoColor=white&label=CI"></a>
  <a href="https://codecov.io/gh/dev-shimada/parraform"><img alt="Coverage" src="https://codecov.io/gh/dev-shimada/parraform/branch/main/graph/badge.svg?style=flat-square"></a>
  <a href="https://github.com/dev-shimada/parraform/blob/main/go.mod"><img alt="Go Version" src="https://img.shields.io/github/go-mod/go-version/dev-shimada/parraform?style=flat-square&logo=go&logoColor=white"></a>
  <a href="https://github.com/dev-shimada/parraform/blob/main/LICENSE"><img alt="MIT License" src="https://img.shields.io/github/license/dev-shimada/parraform?style=flat-square"></a>
</p>

<p align="center">
  <a href="https://github.com/dev-shimada/parraform/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/dev-shimada/parraform?style=flat-square&logo=github&logoColor=white"></a>
  <a href="#インストール"><img alt="Homebrew" src="https://img.shields.io/badge/homebrew-dev--shimada%2Fparraform-FBB040?style=flat-square&logo=homebrew&logoColor=white"></a>
</p>

`terraform` コマンドの透過的なラッパーです。
`plan` をステートロックを取得せずに実行するため、CI で並列に実行した `plan` 同士がロックを奪い合って失敗する問題を防げます。
`apply` を含むそれ以外のコマンドは、通常の terraform と同じように動作し、ロックも従来どおり取得されます。

*(英語版は[こちら](./README.md))*

## インストール

### Homebrew

```sh
brew tap dev-shimada/parraform
brew install parraform
```

### Go

```sh
go install github.com/dev-shimada/parraform/cmd/parraform@latest
```

## GitHub Actions

```yaml
- uses: dev-shimada/parraform@main
```

> [!CAUTION]
> `hashicorp/setup-terraform` と組み合わせる場合は、同 action の `terraform_wrapper` 入力を常に `false` にしてください。

| 入力 | デフォルト | 説明 |
|---|---|---|
| `version` | `latest` | インストールする parraform のバージョンです（例: `v0.1.1`） |
| `terraform_wrapper` | `false` | `parraform` の実行結果をキャプチャします（後述） |
| `github-token` | `github.token` | リリースのダウンロードに使うトークンです |

### 出力のキャプチャ(`terraform_wrapper: true`)

`hashicorp/setup-terraform` の同名のオプションに相当します。
`parraform` を実行したステップで、次の output を取得できます。

| 出力 | 説明 |
|---|---|
| `stdout` | `parraform` の標準出力です |
| `stderr` | `parraform` の標準エラー出力です |
| `exitcode` | `parraform` の終了コードです（`0` と `2` の場合も、ステップは成功します） |

```yaml
- uses: hashicorp/setup-terraform@v4
  with:
    terraform_wrapper: false

- uses: dev-shimada/parraform@main
  with:
    terraform_wrapper: true

- run: parraform init

- id: plan
  run: parraform plan -detailed-exitcode -out=tfplan

- if: steps.plan.outputs.exitcode == '2'
  run: parraform apply tfplan
```

## 使い方

`terraform` の代わりに `parraform` を実行してください。
サブコマンドとオプションは、そのまま terraform に渡されます。

```sh
parraform init
parraform plan -out=tfplan
parraform apply tfplan
```

`terraform` という名前で PATH に置くこともできます。
その場合、既存のパイプラインを変更せずに導入できます。

### オプション

| オプション | 説明 |
|---|---|
| `-lock-check=warn`（デフォルト） | `plan` 専用です。ステートロックが他のプロセスに保持されている場合は、警告を表示したうえで `plan` を実行します |
| `-lock-check=strict` | `plan` 専用です。ステートロックが他のプロセスに保持されている場合は、`plan` を実行せずに終了コード 1 で終了します |

- ロックの状態を確認できない場合（未対応のバックエンドやタイムアウトなど）は、どちらのモードでも `plan` を実行します。
- `-lock-check` は `TF_CLI_ARGS_plan` でも指定できます（例: `TF_CLI_ARGS_plan=-lock-check=strict`）。コマンドラインでの指定が優先されます。

### 環境変数

| 環境変数 | デフォルト | 説明 |
|---|---|---|
| `PARRAFORM_TERRAFORM_BIN` | PATH から検索 | 実行する terraform バイナリのパスです |
| `PARRAFORM_LOCK_CHECK_TIMEOUT` | `3s` | ロック確認のタイムアウトです（Go の duration 形式）。`0` 以下を指定すると、ロック確認を行いません |

### シェル補完

```sh
parraform completion bash > /etc/bash_completion.d/parraform
```

`bash`、`zsh`、`fish`、`powershell` に対応しています。

## 何をしているか

- `plan` は `-lock=false` を付けて実行します（`TF_CLI_ARGS_plan` に追加します）。そのため、ステートロックを取得しません。コマンドラインで `-lock=true` または `-lock=false` を明示した場合は、そちらが優先されます。
- `plan` の実行前に、バックエンドのロック状態を読み取り専用で確認し、他のプロセスが保持していれば警告します（`-lock-check` を参照してください）。
- `apply`、`import`、`refresh`、`state mv` などのその他のコマンドは、変更せずに terraform に渡します。そのため、通常どおりロックが取得されます。
- Unix 系 OS では、parraform 自身を terraform に置き換えて実行します（`exec`）。そのため、標準入出力、TTY の判定、シグナル、終了コードは、terraform を直接実行した場合と同じです。

## なぜ安全か

- S3 や GCS などへの書き込みはアトミックで、書き込み直後の読み取りも一貫しています。そのため、`apply` の実行中に `plan` を実行しても、壊れた状態を読み取ることはありません（最悪でも、`apply` の完了直前のスナップショットを読み取るだけです）。
- `plan -out=` で保存した plan は、その後に state が変更されていると、適用時に `Error: Saved plan is stale` で失敗します。そのため、古い state をもとにした plan が、そのまま適用されることはありません。

## ロックチェックの対象バックエンド

| バックエンド | 対応状況 |
|---|---|
| `local`、`s3`、`gcs`、`azurerm`、`consul`、`kubernetes`、`cos`、`oci` | 対応しています |
| `pg`、`oss` | 対応しています（実環境での動作は未検証です） |
| `remote`、`cloud`（HCP Terraform / Terraform Enterprise） | `workspaces.name` を固定した場合のみ対応しています（`tags` と `project` には未対応です） |
| `http` | 未対応です（terraform に、ロックを読み取り専用で確認する手段がないためです） |
