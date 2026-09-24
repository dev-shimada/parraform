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

A transparent wrapper around the `terraform` CLI. `plan` runs without acquiring
the state lock, eliminating the failures that happen when parallel `plan` runs
in CI fight over it. Every other command, including `apply`, behaves exactly
like normal terraform and keeps the usual locking in place.

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

parraform finds the real `terraform` binary on `PATH` automatically. To point
it at a specific binary instead, set `PARRAFORM_TERRAFORM_BIN`.

## Usage

Just call `parraform` instead of `terraform`. Every subcommand and flag is
passed through unchanged.

```
parraform init
parraform plan -out=tfplan
parraform apply tfplan
```

You can also drop it onto `PATH` under the name `terraform` in CI, so existing
pipelines pick it up without any config changes.

### Shell completion

Being cobra-based, it can generate completion scripts for bash/zsh/fish/powershell.

```
parraform completion bash > /etc/bash_completion.d/parraform
```

## What it does

- Only for `plan`, it appends `-lock=false` to `TF_CLI_ARGS_plan`, so the run
  never acquires the lock. An explicit `-lock=true`/`-lock=false` passed on the
  command line still takes precedence.
- Before running `plan`, it performs a read-only peek at the configured
  backend's actual lock state and prints a warning if another process holds
  it — without ever blocking `plan` itself.
- `apply` / `import` / `refresh` / `state mv` and every other command that
  writes state pass through completely unmodified, keeping terraform's normal
  locking and checks.
- It replaces the current process with the real `terraform` binary via
  `syscall.Exec` (Unix), so stdio, TTY detection, signal handling, and the
  exit code are byte-for-byte identical to running terraform directly.

## Why it's safe

`-lock=false` is safe for `plan` because writes to S3/GCS-style backends are
atomic and read-after-write consistent, so a `plan` running during an `apply`
can never read a "torn" state. At worst it reads a snapshot from just before
the `apply` completed — never a corrupted one.

Verified empirically: a plan file saved via `plan -out=` fails explicitly with
`Error: Saved plan is stale` when `apply <planfile>` is later run against a
state that changed since the plan was captured. So even in the typical CI
workflow of saving a plan in one job and applying it in another, an unlocked
plan that read a slightly stale state fails closed at apply time rather than
silently applying against outdated assumptions.

The backend lock peek adds one more signal on top of that — letting you know
an `apply` is in flight — without ever gating `plan` itself.

## Backends the lock check supports

Lock checking is implemented for all 11 backend types terraform currently
supports for remote state (everything except `local`). The depth of
verification varies, across three tiers:

| Confidence | What it means | Backends |
|---|---|---|
| High | terraform's source was read to confirm the lock mechanism, and the real SDK's outgoing HTTP requests were verified against `httptest` (client-go's fake clientset for kubernetes) | local, s3, gcs, azurerm, consul, kubernetes, cos, oci |
| Medium | terraform's source was read, but there's been no integration test against a real DB/instance | pg, oss |
| Scope-limited | Source-verified and tested, but the supported configuration shapes are narrower | remote / cloud (TFC/TFE — only fixed `workspaces.name` is supported, not the `tags`/`project` dynamic-workspace forms) |

The `http` backend has no supported way to peek at all, since terraform's own
contract for it exposes only `LOCK`/`UNLOCK`, not a read-only check (so
`-lock=false` still applies, but no lock warning is ever shown).

For every backend, whenever there's doubt about how to extract config or
interpret the lock mechanism, the checker silently skips the check rather than
risk a wrong-but-confident answer — `plan` still runs with `-lock=false`.

## Environment variables

| Variable | Description |
|---|---|
| `PARRAFORM_TERRAFORM_BIN` | Explicit path to the terraform binary to use |
| `PARRAFORM_LOCK_CHECK_TIMEOUT` | Timeout for the lock peek (a Go duration string, default `3s`). This latency is added to every `plan` invocation, so tune it down in environments with slow cloud credential resolution. `0` or negative disables the check entirely |

## Development

```
go build ./...
go test ./...
golangci-lint run ./...
```

Two kinds of docker-based tests, both gated behind the `integration` build
tag so the commands above never need docker or a terraform binary:

- The S3 backend's lock checker has an integration test against a real
  S3/DynamoDB-compatible server ([ministack](https://github.com/ministackorg/ministack)):

  ```
  go test -tags=integration ./internal/lockcheck/... -run TestS3Integration -v
  ```

- An end-to-end test builds the actual `parraform` binary and runs it
  against a real `terraform` binary and ministack, confirming that a real
  `terraform plan` gets blocked by a held state lock while `parraform plan`
  does not — and that many concurrent `parraform plan` runs never contend
  for the lock at all:

  ```
  go test -tags=integration ./cmd/parraform/ -run TestE2E -v
  ```

Both skip themselves if docker (or, for the E2E test, terraform) isn't
installed.

CI runs the same three checks (across Linux/macOS/Windows for build/test)
on every push and pull request, plus both integration tests on Linux;
releases are cut by pushing a `v*` tag,
which [GoReleaser](https://goreleaser.com/) builds and publishes to GitHub
Releases and the [homebrew-parraform](https://github.com/dev-shimada/homebrew-parraform)
tap.
