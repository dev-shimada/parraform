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

`plan` はステートロックを取得せずに実行できるため、CI などで並列に実行した `plan` 同士がロックを奪い合って失敗する問題を防げます。

`apply` を含むそれ以外のコマンドは、通常の terraform とまったく同じように動作し、ロックも従来どおり取得されます。

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
| `verify-attestation` | `true` | インストール前にアーカイブのビルド証明（provenance）を検証します（後述） |
| `sha256` | なし | アーカイブの期待する SHA-256 です（後述）。`version` の固定が必要です |

### ダウンロードの検証

アーカイブは次の順に検証され、最初に失敗した時点で止まります。

1. `sha256`（指定した場合）: 利用側のリポジトリに書いたハッシュと一致する必要があります。
2. `verify-attestation`（デフォルト）: `gh attestation verify` で、このリポジトリの `release.yml` が、そのリリース自身のタグからビルドしたものであることを確認します。
3. リリースに同梱された `checksums.txt`。アーカイブと同じリリースに置かれているため、ダウンロードの破損しか検知できません。

```yaml
- uses: dev-shimada/parraform@main
  with:
    version: v0.2.0
    sha256: |
      <hash>  parraform_Linux_x86_64.tar.gz
      <hash>  parraform_Darwin_arm64.tar.gz
```

- `sha256` の行は、リリースの `parraform_<version>_checksums.txt` からコピーします。使うランナーのアーキテクチャごとに1行ずつ記載します。
- attestation が導入される前のリリース（v0.1.3 以前）は検証できません。インストールするには `verify-attestation: false` を指定してください。

### 出力のキャプチャ(`terraform_wrapper: true`)

`hashicorp/setup-terraform` の同名のオプションに相当し、`parraform` を実行したステップに次の output が設定されます。

| 出力 | 説明 |
|---|---|
| `stdout` | `parraform` の標準出力です |
| `stderr` | `parraform` の標準エラー出力です |
| `exitcode` | `parraform` の終了コードです |

- ステップは、終了コードが `0` または `2` の場合に成功として扱われます。
  - `2` は、`-detailed-exitcode` で差分が検出されたことを表します。

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

`terraform` の代わりに `parraform` を実行すると、すべてのサブコマンドとオプションがそのまま terraform に渡されます。

```sh
parraform init
parraform plan -out=tfplan
parraform apply tfplan
```

`terraform` という名前で PATH に置けば、既存のパイプラインを変更せずに導入できます。

### オプション

`plan` で指定できるオプションです。

| オプション | 説明 |
|---|---|
| `-lock=false`（デフォルト） | ステートロックを取得しません |
| `-lock=true` | ステートロックを取得します |
| `-lock-check=warn`（デフォルト） | ロックが保持されている場合は、警告を表示して `plan` を実行します |
| `-lock-check=strict` | ロックが保持されている場合は、`plan` を実行せずに終了コード 1 で終了します |
| `-lock-check=require` | `strict` と同様に動作し、さらにロックが空いていることを確認できない場合も終了コード 1 で終了します |

- `-lock` は terraform のオプションで、parraform は `plan` のデフォルトだけを `false` に変更しています。
- ロックの状態を確認できない場合（未対応のバックエンドやタイムアウトなど）でも、`warn` と `strict` は `plan` を実行します。
- `require` は確認できなかった場合に止める（フェイルクローズ）モードで、ロックが空いていることを確認できたときだけ `plan` を実行します。次の場合は理由を表示して終了コード 1 で終了します。
  - `.terraform/terraform.tfstate`（`TF_DATA_DIR` 指定時はその配下の `terraform.tfstate`）がない、またはバックエンドの記載がない（`init` 未実行、または暗黙のローカルバックエンド）
  - 未対応のバックエンド、またはロックが設定されていない（例: `dynamodb_table` も `use_lockfile` も未指定の `s3`）
  - 確認がタイムアウトした、または失敗した（例: `dynamodb:GetItem` の権限がない）
  - `PARRAFORM_LOCK_CHECK_TIMEOUT` が `0` 以下
- `-lock-check` は `TF_CLI_ARGS_plan` でも指定できます（例: `TF_CLI_ARGS_plan=-lock-check=strict`）。
  - コマンドラインでの指定が優先されます。
- `plan` 以外のコマンド（`apply`、`import`、`state mv` など）は、変更せずに terraform に渡されます。

### 環境変数

| 環境変数 | デフォルト | 説明 |
|---|---|---|
| `PARRAFORM_TERRAFORM_BIN` | なし | 実行する terraform バイナリのパスです |
| `PARRAFORM_LOCK_CHECK_TIMEOUT` | `3s` | ロック確認のタイムアウトです（Go の duration 形式） |

- `PARRAFORM_TERRAFORM_BIN` を指定しない場合は、PATH から terraform を探します。
- `PARRAFORM_LOCK_CHECK_TIMEOUT` に `0` 以下を指定すると、ロック確認を行いません（`-lock-check=require` の場合は、代わりに `plan` を拒否します）。

### シェル補完

```sh
parraform completion bash > /etc/bash_completion.d/parraform
```

`bash`、`zsh`、`fish`、`powershell` に対応しています。
