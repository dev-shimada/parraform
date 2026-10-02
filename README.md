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
`plan` runs without acquiring the state lock, so parallel `plan` runs in CI no longer fail by fighting over it.
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

Equivalent to the option of the same name in `hashicorp/setup-terraform`.
The step that runs `parraform` gets these outputs:

| Output | Description |
|---|---|
| `stdout` | Standard output of `parraform` |
| `stderr` | Standard error of `parraform` |
| `exitcode` | Exit code of `parraform` (the step succeeds for `0` and `2`) |

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

Use `parraform` in place of `terraform`.
All subcommands and flags are passed through.

```sh
parraform init
parraform plan -out=tfplan
parraform apply tfplan
```

You can also put it on `PATH` under the name `terraform`, so existing pipelines work without changes.

### Options

| Option | Description |
|---|---|
| `-lock-check=warn` (default) | `plan` only. If the state lock is held, prints a warning and runs `plan` anyway |
| `-lock-check=strict` | `plan` only. If the state lock is held, exits with code 1 without running `plan` |

- If the lock state cannot be determined (unsupported backend, timeout, etc.), `plan` runs in both modes.
- `-lock-check` can also be set with `TF_CLI_ARGS_plan` (e.g. `TF_CLI_ARGS_plan=-lock-check=strict`). The command line takes precedence.

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `PARRAFORM_TERRAFORM_BIN` | found on `PATH` | Path of the terraform binary to run |
| `PARRAFORM_LOCK_CHECK_TIMEOUT` | `3s` | Timeout of the lock check (Go duration). `0` or less disables it |

### Shell completion

```sh
parraform completion bash > /etc/bash_completion.d/parraform
```

`bash`, `zsh`, `fish`, and `powershell` are supported.

## What it does

- `plan` runs with `-lock=false` (added to `TF_CLI_ARGS_plan`), so it never takes the state lock. An explicit `-lock=true` or `-lock=false` on the command line takes precedence.
- Before `plan`, it checks the backend's lock state (read-only) and warns if another process holds it. See `-lock-check`.
- Other commands (`apply`, `import`, `refresh`, `state mv`, ...) are passed through unchanged and keep terraform's normal locking.
- On Unix, parraform replaces itself with terraform (`exec`), so stdio, TTY detection, signals, and exit codes are identical to running terraform directly.

## Why it's safe

- Writes to S3/GCS-style backends are atomic and read-after-write consistent, so a `plan` running during an `apply` never reads a corrupted state (at worst, a snapshot from just before the `apply` finished).
- A plan saved with `plan -out=` fails with `Error: Saved plan is stale` if the state changed before it is applied, so a plan based on an outdated state is never applied silently.

## Backends the lock check supports

| Backend | Support |
|---|---|
| `local`, `s3`, `gcs`, `azurerm`, `consul`, `kubernetes`, `cos`, `oci` | Supported |
| `pg`, `oss` | Supported (not verified against a real instance) |
| `remote`, `cloud` (HCP Terraform / Terraform Enterprise) | Only with a fixed `workspaces.name` (`tags` and `project` are not supported) |
| `http` | Not supported (terraform has no read-only lock check for it) |
