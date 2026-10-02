# parraform

<p align="center">
  <a href="https://github.com/dev-shimada/parraform/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/dev-shimada/parraform/ci.yml?branch=main&style=flat-square&logo=githubactions&logoColor=white&label=CI"></a>
  <a href="https://codecov.io/gh/dev-shimada/parraform"><img alt="Coverage" src="https://codecov.io/gh/dev-shimada/parraform/branch/main/graph/badge.svg?style=flat-square"></a>
  <a href="https://github.com/dev-shimada/parraform/blob/main/go.mod"><img alt="Go Version" src="https://img.shields.io/github/go-mod/go-version/dev-shimada/parraform?style=flat-square&logo=go&logoColor=white"></a>
  <a href="https://github.com/dev-shimada/parraform/blob/main/LICENSE"><img alt="MIT License" src="https://img.shields.io/github/license/dev-shimada/parraform?style=flat-square"></a>
</p>

<p align="center">
  <a href="https://github.com/dev-shimada/parraform/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/dev-shimada/parraform?style=flat-square&logo=github&logoColor=white"></a>
  <a href="#install"><img alt="Homebrew" src="https://img.shields.io/badge/homebrew-dev--shimada%2Fparraform-FBB040?style=flat-square&logo=homebrew&logoColor=white"></a>
</p>

A transparent wrapper around the `terraform` CLI.

`plan` can run without acquiring the state lock, so parallel `plan` runs in CI don't fail by fighting over it.

Every other command, including `apply`, behaves exactly like normal terraform and keeps the usual locking.

*(日本語版は[こちら](./README.ja.md))*

## Install

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
> If you pair this action with `hashicorp/setup-terraform`, always set its `terraform_wrapper` input to `false`.

| Input | Default | Description |
|---|---|---|
| `version` | `latest` | parraform release to install (e.g. `v0.1.1`) |
| `terraform_wrapper` | `false` | Capture the output of `parraform` (see below) |
| `github-token` | `github.token` | Token used to download the release |

### Capturing output (`terraform_wrapper: true`)

Equivalent to the option of the same name in `hashicorp/setup-terraform`, it sets these outputs on the step that runs `parraform`.

| Output | Description |
|---|---|
| `stdout` | Standard output of `parraform` |
| `stderr` | Standard error of `parraform` |
| `exitcode` | Exit code of `parraform` |

- The step succeeds when the exit code is `0` or `2`.
  - `2` means `-detailed-exitcode` found changes.

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

## Usage

Use `parraform` in place of `terraform`, and all subcommands and flags are passed through unchanged.

```sh
parraform init
parraform plan -out=tfplan
parraform apply tfplan
```

Put it on `PATH` under the name `terraform` to use it in existing pipelines without changes.

### Options

Controls what `plan` does when the state lock is held by another process.

| Option | Behavior |
|---|---|
| `-lock-check=warn` (default) | Prints a warning and runs `plan` |
| `-lock-check=strict` | Exits with code 1 without running `plan` |

- If the lock state cannot be determined (unsupported backend, timeout, etc.), `plan` runs in both modes.
- `-lock-check` can also be set with `TF_CLI_ARGS_plan` (e.g. `TF_CLI_ARGS_plan=-lock-check=strict`).
  - The command line takes precedence.

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `PARRAFORM_TERRAFORM_BIN` | none | Path of the terraform binary to run |
| `PARRAFORM_LOCK_CHECK_TIMEOUT` | `3s` | Timeout of the lock check (Go duration) |

- If `PARRAFORM_TERRAFORM_BIN` is not set, terraform is looked up on `PATH`.
- Setting `PARRAFORM_LOCK_CHECK_TIMEOUT` to `0` or less disables the lock check.

### Shell completion

```sh
parraform completion bash > /etc/bash_completion.d/parraform
```

`bash`, `zsh`, `fish`, and `powershell` are supported.
