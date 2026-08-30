# parraform Design

*(日本語版は[こちら](./DESIGN.ja.md))*

## The problem

When `terraform` uses remote state locking, running `plan` in parallel in CI
causes lock contention and CI failures. `plan` never writes state, so it
doesn't actually need to acquire the lock in the first place.

## Approach

`parraform` is a transparent wrapper around the `terraform` command.

- `plan` never acquires the lock; it only performs a read-only check of the
  backend's real lock state, enabling safe parallel execution.
- Commands that write state, like `apply`, acquire and check the lock as
  usual, preserving exclusivity.
- Implemented in Go, using cobra so it can emit bash/zsh/fish completion
  scripts.
- Compatible with every terraform command (complete passthrough).

## Architecture

### Execution model

Operates as a thin wrapper around the real `terraform` binary.

- Unix: replaces the process via `syscall.Exec`. stdio, TTY detection,
  signals, and the exit code become identical to the real invocation, with no
  need for extra signal-forwarding code.
- Windows: `exec.Command` + inherited stdio, reflecting the exit code via
  `ExitError.ExitCode()`.

The lock check runs **before** exec, so no information is lost even with
process replacement.

### Binary resolution

- `PARRAFORM_TERRAFORM_BIN` environment variable takes priority.
- Otherwise, search `PATH`, excluding any entry that resolves to the same
  absolute path as `os.Executable()` (to prevent infinite recursion when
  parraform is symlinked under the name `terraform`).

### Avoiding the lock during plan

Inject `-lock=false` into the `TF_CLI_ARGS_plan` environment variable
(appending to any existing value). No argv rewriting needed.

Verified facts (confirmed against the local backend):

- Setting `TF_CLI_ARGS_plan="-lock=false"` lets `plan` run without acquiring
  the lock.
- When the user passes `-lock=true` / `-lock=false` explicitly on the command
  line, the explicit flag takes precedence over the env-injected one
  (`TF_CLI_ARGS` is inserted right after the subcommand, and last-flag-wins
  parsing means the explicit flag wins).
- Read-only commands like `terraform state list` / `terraform show` never
  acquired the lock to begin with.

The target command is `plan` only (including `plan -destroy` /
`plan -refresh-only`). Commands that write state — `apply` / `import` /
`refresh` / `state mv`, etc. — pass through completely unmodified, preserving
terraform's standard locking behavior.

### Backend-aware lock check

Before running `plan`, perform a read-only peek at the configured backend's
real lock state and print a warning if someone holds it. The peek consists
solely of side-effect-free reads; it never calls terraform's own Lock/Unlock
RPC.

The backend type is determined by parsing `.terraform/terraform.tfstate` (the
backend config terraform caches after `init`, containing the type/config
attributes in plaintext). No HCL parsing needed.

**Workspace-awareness turned out to be mandatory**, confirmed by hands-on
testing. Non-default workspaces use a different lock path/key than default,
so building the lock identifier from backend config alone misbehaves for any
non-default workspace — which is exactly the primary use case for CI running
parallel plan/apply per workspace. The current workspace is resolved by
preferring the `TF_WORKSPACE` env var, then falling back to
`.terraform/environment` (which doesn't exist at all for the default
workspace). Each backend's lock identifier folds in the workspace as follows
(confirmed against real runs and official docs):

- local: for non-default, `<dir of path>/terraform.tfstate.d/<workspace>/<basename of path>`
- S3: for non-default, `<workspace_key_prefix>/<workspace>/<key>` (default
  `workspace_key_prefix` is `env:`)
- GCS: `<prefix>/<workspace>.tflock`. **The default workspace gets no special
  case either** — it becomes `<prefix>/default.tflock` (confirmed against
  terraform's own source, `stateFile`/`lockFile` in
  `internal/backend/remote-state/gcs/backend_state.go`. Unlike S3/local, note
  that there's no special-casing of default).
- azurerm: for non-default, `"env:" + workspace` is **concatenated to `<key>`
  with no separator** (confirmed against terraform's source, `Backend.path()`
  in `internal/backend/remote-state/azure/backend_state.go` — a naming scheme
  distinct from other backends' `/`-separated ones). Locking is done via a
  lease on the state blob itself (not a separate object), and lock info (Who,
  etc.) is stored base64+JSON-encoded not in the blob body but in a blob
  metadata key called `terraformlockid` (confirmed against `client.go`'s
  `Lock()` implementation).
- consul: for non-default, `"-env:" + workspace` is concatenated to `<path>`
  with no separator (confirmed against `statePath()` in
  `backend_state.go`). Lock info is written to a separate KV key,
  `<path>/.lockinfo`, and explicitly deleted on `Unlock()` (confirmed against
  `client.go`), so checking whether this key exists is enough to accurately
  determine lock state (no need to inspect Consul sessions).
- kubernetes: like GCS, no special case for default; the Lease name is
  `"lock-tfstate-<workspace>-<secret_suffix>"` (confirmed against
  `createSecretName`/`createLeaseName` in `client.go`). Whether it's locked
  must be determined by whether `Spec.HolderIdentity` is non-nil, not by
  whether the Lease object exists (`Unlock()` doesn't delete the Lease, it
  just resets `HolderIdentity` to nil — confirmed against `client.go`'s
  `Unlock()` implementation).
- pg (Postgres): no special case for default; it looks up the row in the
  `<schema_name>.states` table whose `name` column exactly matches the
  workspace name, and uses that row's `id` column value as the advisory
  lock's key (confirmed against `client.go`/`backend_state.go`). Lock holder
  info (Who) is never persisted anywhere — it only exists in the locking
  process's memory (confirmed against `client.go`'s `Lock()`/`Unlock()`
  implementation) — so `Info.Who` is always empty for this backend (that's
  expected, not a bug).
- cos: S3-like; default is `<prefix>/<key>`, others are
  `<prefix>/<workspace>/<key>` (confirmed against `stateFile()` in
  `backend_state.go`). The lock file is `stateFile()+".tflock"` (confirmed
  against the `lockFileSuffix` constant), and checking its existence as a COS
  object is all that's needed (the Tags-API-based distributed lock is only
  for preventing races during `Lock()` itself; irrelevant to a peek).
- oci: same shape as S3 — default is `<key>` unchanged, others are
  `<workspace_key_prefix>/<workspace>/<key>` (default prefix is
  `"tf-state-env"`). The lock file is `path(name)+".lock"` (verified by
  fetching `backend.go`'s raw source directly via `gh api` and confirming
  `lockFileSuffix = ".lock"` — note it's *not* `.tflock`; this is a spot
  where writing it by analogy to the other backends would have gotten it
  wrong).
- oss: same `<prefix>/<key>`-family scheme as cos/oci, but the locking
  mechanism itself differs — it's S3's DynamoDB equivalent (a separate
  TableStore service, row key `"LockID"` column = `"<bucket>/<stateFile>"`)
  (confirmed against `lockPath()`/`pkName` in `client.go`). If
  `tablestore_table` isn't configured, terraform itself never locks either,
  so in that case it returns `supported=false`.

### Supported backends (targeting every type that can be set as remote state)

The terraform official docs currently list the following 11 backend types
(excluding `local`). **The policy is to implement a LockChecker for every one
that's reachable.**

| Backend | Peek method | Required permission/prerequisite | Status |
|---|---|---|---|
| local | Non-blocking flock attempt on the state file (released immediately) | None | Implemented |
| s3 (native lockfile, `use_lockfile`, TF≥1.11) | GetObject on `<effective key>.tflock` | `s3:GetObject` | Implemented |
| s3 (legacy, DynamoDB) | GetItem with `LockID="<bucket>/<effective key>"` | `dynamodb:GetItem` | Implemented |
| gcs | Existence check on `<prefix>/<workspace>.tflock` (NewReader) | `storage.objects.get` | Implemented |
| azurerm | Check the state blob's `x-ms-lease-status` header (GetBlobProperties) | Blob read permission | Implemented |
| remote (`backend "remote"`, TFC/TFE) | `go-tfe`'s `Workspaces.Read`, checking `Locked` | TFE read token (`TF_TOKEN_*` / `token` attribute / `credentials.tfrc.json`) | Implemented (caveats below) |
| cloud (`cloud{}` block, TFC/TFE) | Same as above. Only fixed `workspaces.name` is supported; `tags`/`project`-based dynamic workspace resolution is not | Same as above | Implemented (scope-limited) |
| consul | KV GET on the `<path>/.lockinfo` key (existence check) | `kv:read` (when ACLs are enabled) | Implemented |
| kubernetes | Check `coordination.k8s.io/v1 Lease`'s `holderIdentity` (GET) | get permission on the lease | Implemented |
| pg (Postgres) | Non-blocking attempt + immediate release on the advisory lock (same technique as local) | Connection permission only | Implemented (**not verified against a real DB**) |
| cos (Tencent Cloud COS) | GetObject on the `<stateFile>.tflock` object (existence check) | Object read permission | Implemented |
| oci (Oracle Cloud Infrastructure) | GetObject on the `<path(workspace)>.lock` object (existence check) | Object read permission | Implemented |
| oss (Alibaba Cloud OSS) | GetRow on the corresponding TableStore row (existence check). Unsupported when `tablestore_table` isn't configured, since terraform itself never locks in that case | TableStore read permission | Implemented (**not verified against a real instance**) |
| http | No way to peek (only LOCK/UNLOCK exist; there's no peek API in the contract) | - | Not supportable (falls back to portable behavior) |

**All 11 types (excluding `local`) have been reached.** Every one was
implemented by directly reading terraform's own source
(`client.go`/`backend.go`/`backend_state.go` under
`internal/backend/remote-state/<name>/`) to confirm how the lock identifier
is built — none was guessed. That said, the depth of verification varies,
across three confidence tiers:

1. **Source-confirmed, and the actual SDK wiring verified live/via
   httptest**: local, s3, gcs, azurerm, consul, kubernetes, cos, oci. The
   HTTP requests the SDK actually sends (path, error type, 404 handling,
   etc.) were confirmed by exercising them against an `httptest` server
   (client-go's fake clientset for kubernetes only). That said, **this
   verification is limited to the scope the tests directly exercise the
   client in**. The actual client-construction code used in production
   (`ossClient` / `gcsClientOptions` / `azurermClient` / `cosClient` /
   `ociClient`, etc. — the part that builds an SDK client from credentials)
   is itself outside test coverage and wasn't directly verified via
   httptest. For example, cos's bucket URL format
   (`https://<bucket>.cos.<region>.myqcloud.com`) was confirmed directly
   against `backend.go`'s source, but the alternate URL format used with
   `accelerate` / a custom `endpoint` isn't handled (an unsupported
   configuration results in a connection error, falling back to
   `supported=false` — so there's no risk of connecting to the wrong
   endpoint).
2. **Source-confirmed only; no integration test against a real DB/instance
   yet**: pg, oss. Postgres's wire protocol (for pg) and the protobuf-based
   protocol TableStore (used by oss) uses aren't easily mocked with
   httptest. Docker/real-DB access wasn't available in this environment
   either. Only the pure logic (identifier construction, etc.) is unit
   tested. This should be re-verified against a real environment when one
   becomes available.
3. **Source-confirmed and live/httptest-verified, but with a narrowed
   scope**: remote/cloud (TFC/TFE). Exactly which shape (a single object vs.
   a one-element list) the `workspaces` nested block takes in the cached
   JSON hasn't been confirmed against a real TFC/TFE account; the extraction
   code tries both shapes and safely falls back to `supported=false` on
   anything unexpected. `cloud{}`'s `tags`/`project`-based dynamic workspace
   resolution is also unsupported (only fixed `workspaces.name` is).

In every case, wherever confidence is lacking, the design consistently
avoids "confidently printing a warning or not printing one with the wrong
lock identifier" by falling back safely to `supported=false` (a lesson
learned from having actually hit the workspace-support bug once).

Abstracted behind the `LockChecker` interface, with implementations added per
backend. The decision was made **not** to isolate each SDK's dependencies,
and instead **bundle everything into a single binary** (prioritizing build
and distribution simplicity; the resulting increase in binary size/build
time is accepted).

### Libraries considered

Results of evaluating existing libraries that operate on terraform directly:

- **`hashicorp/terraform-exec`**: Rejected. It has a typed API per
  subcommand, which clashes with the requirement to "pass through
  everything, including terraform's future flags" — tfexec itself would need
  to catch up with terraform's new flags before they could be used. TTY
  inheritance for the interactive approval prompt also isn't guaranteed to
  be as faithful as process replacement via `syscall.Exec`. The exec layer
  stays its own thin, custom wrapper.
- **`hashicorp/hcl` / `hcl/v2`**: Rejected. Backend config blocks don't allow
  interpolation (static values only), so the value cached in plaintext to
  `.terraform/terraform.tfstate` after `terraform init` is enough to
  determine it. No HCL parser needed — `encoding/json` suffices.
- **`hashicorp/hc-install`**: Rejected. It's a version-management/download
  library; this project only delegates to whatever terraform is already on
  `PATH`, so that's out of scope.
- **terraform's own internal lock implementation**
  (`internal/backend/remote-state/*`): Can't be imported externally due to
  Go's `internal/` visibility rules. The only option for S3/GCS/Azure peeks
  is to hit each cloud SDK directly (`aws-sdk-go-v2`'s s3/dynamodb clients,
  `cloud.google.com/go/storage`, `azure-sdk-for-go`) and implement it
  ourselves.
- **`hashicorp/go-tfe`**: Adopted. When Terraform Cloud/Enterprise is the
  backend, lock state (`Locked`/`LockedBy`) can be fetched with a single
  `Workspaces.Read` call, making it cheaper to implement than the other
  cloud backends.

Growing dependence on different cloud SDKs per backend is a future concern.
Isolating unused backends' SDKs from the binary via build tags, if desired,
is left undecided.

**Behavior on detecting a lock**: the default is "print a warning and
proceed." `plan` still runs with `-lock=false` (blocking here would just
reproduce the parallel-plan problem in a different form). A fail-fast option
like `-lock-check=strict` is a candidate for future extension, but out of
scope for the initial implementation.

### cobra / shell completion

The root command uses `DisableFlagParsing: true` to preserve complete
passthrough.

terraform's main subcommands (roughly 25 of them — `plan` / `apply` /
`destroy` / `state` / `workspace` / `import` / `refresh` / `console` / `fmt`
/ `validate` / `output` / `show` / `taint` / `untaint` / `force-unlock` /
`graph` / `providers` / `init`, etc.) are each registered as individual
cobra subcommands, each with `DisableFlagParsing: true`, delegating to the
same exec path. This makes cobra's generated completion script actually
complete real terraform command names. `version` is included in the list as
a genuine terraform command too, and passed straight through to terraform
(so as not to break the transparency of `terraform version` — parraform's
own version display doesn't hijack the `version` subcommand; parraform's own
version-check mechanism is unimplemented and out of scope). `completion`
doesn't collide with any terraform command name, so cobra's standard
implementation is used as-is.

The maintenance cost of keeping this list current as terraform adds/removes
subcommands across versions is accepted.

### Safety argument

Why `-lock=false` is safe for `plan`: writes to S3/GCS are atomic and
read-after-write consistent, so a `plan` running during an `apply` can never
read a "torn" state. At worst it reads a snapshot from just before the
`apply` completed — no corruption. The backend lock peek adds one more
signal on top of that: letting the user know an `apply` is in flight.

**Verified empirically**: a plan file saved via `plan -out=` fails explicitly
with `Error: Saved plan is stale` when `apply <planfile>` is run after the
state's serial has advanced due to some other operation since the plan was
saved (confirmed this holds even for an operation with no config change,
like `taint`). In other words, even in the typical CI workflow of "save a
plan in one job, apply it in another," if the unlocked plan read a somewhat
stale state, it fails closed at apply time — an apply is never mistakenly
run against stale assumptions. This lets the safety claim extend beyond "no
corruption" to "a stale plan is reliably rejected at apply time."

## Testing approach

- Pure functions like lock avoidance / passthrough classification are
  table-driven tested independent of terraform.
- Passthrough, exit codes, and signal handling are tested with a fake script
  on `PATH` that echoes argv and returns the requested exit code, requiring
  neither real terraform nor cloud credentials.
- `internal/backendcfg` (shared by every backend: cache-file parsing and
  `TF_WORKSPACE`/`.terraform/environment` workspace resolution) has its own
  unit tests, independent of any specific backend.
- Each `LockChecker` is tested at two separate layers, and each layer's
  coverage is real but does **not** imply the other is covered:
  1. **Config → lock identifier.** A `<backend>LockTarget(cfg
     backendcfg.Config) (...)` function isolates "given raw backend config
     and a workspace, what bucket/key/lease-name/etc. does this checker
     compute" from client construction and network I/O. This is
     table-tested per backend (`Test<Backend>LockTarget`), workspace
     variations included, so a regression like the earlier
     workspace-unaware-lock bug would be caught here rather than only in
     production. This layer exists for every backend except `http`
     (unsupported) and is exercised via `Peek()`'s early-return paths for
     unsupported/missing config, plus the table test directly for the
     happy-path identifier math.
  2. **Lock identifier → Info.** A `peek<Backend>...` function takes an
     already-constructed (fake or real) client/object handle and interprets
     its response into `Info{Locked, Who}`. This is what the httptest-based
     `_Locked`/`_NotLocked` tests exercise.
  - **kubernetes is the one exception tested through the full chain in one
    piece**: `TestKubernetesChecker_Peek_EndToEnd` builds a temporary
    kubeconfig pointing at an `httptest` server and calls
    `kubernetesChecker{}.Peek(ctx, cfg)` directly, so config extraction,
    client construction, identifier resolution, and response interpretation
    are all exercised together through production code, not just their two
    halves separately. The other backends stop at the two layers above;
    their client-construction code (`azurermClient`, `cosClient`,
    `ociClient`, `gcsClientOptions`, etc.) remains outside test coverage, as
    already noted in the confidence tiers above.
  - **local is also tested through the full chain**
    (`TestLocalChecker_Peek_NotLocked`/`_Locked`), since it needs no SDK or
    mock server: a second, independently-opened file descriptor holding a
    real `flock` is exactly what "another process holds the lock" looks
    like, so the real `syscall.Flock` codepath in `Peek()` is exercised
    directly against a real file.
  - pg and oss have layer-1 (`pgLockTarget`/`ossLockTarget`) coverage but,
    consistent with their documented integration-unverified status, no
    layer-2 test — there is no fake Postgres/TableStore server backing
    `peekPgAdvisoryLock`/`peekOSSLockRow` in this suite.

**What this does and doesn't guarantee**: the lock-identifier computation is
table-tested per backend (including workspace handling), and one backend
(kubernetes) plus the local backend are verified through the full
production chain end-to-end. Every other backend's SDK client construction
and live wire behavior were verified by hand (or via `httptest` probing
during development) but are not exercised by the automated suite — that
gap is the confidence-tier table above, not something closed by this
section. "All providers are covered" would overstate it; "the two most
failure-prone seams — workspace-aware identifier construction, and
200/404-style response interpretation — are both under test for every
backend, and wired together end-to-end for two of them" is the accurate
claim.

## Known limitations in the current implementation

- The lock peek's timeout defaults to 3 seconds (configurable via
  `PARRAFORM_LOCK_CHECK_TIMEOUT`, disabled at `0` or below). This wait is
  **latency added to every single `plan` invocation** — if a cloud SDK's
  default credential-chain resolution (IMDS probe retries,
  `DefaultAzureCredential`'s fallback search, etc.) exceeds it, the peek is
  treated as an error and no warning is shown. A timeout and "unlocked" are
  indistinguishable to the user (a deliberate trade-off: this shape was
  chosen explicitly to prioritize speed and avoid extra noise on peek
  failure). **Implementation surfaced that passing `ctx` alone isn't always
  enough**: `go-tfe`'s `NewClient` makes a synchronous, ctx-less
  `GET /api/v2/ping` with retries at construction time (which stalls here if
  the TFC/TFE host is slow or unreachable), and oss's `tablestore.GetRow`
  also has an old signature that takes no ctx argument. To guarantee a
  timeout even when `ctx` is ignored inside `checker.Peek`,
  `peekWithTimeout` in `cmd/parraform/main.go` runs `Peek` in a goroutine and
  races it via `select`. A goroutine that times out is abandoned rather than
  collected (immediately afterward, `syscall.Exec` replaces the entire
  process image, so there's no risk of it lingering as a leak).
- azurerm's peek only supports `access_key` (shared key), or otherwise the
  Azure SDK's `DefaultAzureCredential` (Azure CLI login / env vars / MSI,
  etc.). Most of the auth methods terraform itself supports —
  `client_secret` / OIDC / service principal certificates — are out of MVP
  scope (adding `azidentity` options on the Azure SDK side would be needed
  for those configurations).
- cos's peek only supports `secret_id`/`secret_key` (including
  `TENCENTCLOUD_SECRET_ID`/`TENCENTCLOUD_SECRET_KEY` env var fallback).
  `assume_role`-based role assumption isn't supported.
- oci's peek authenticates directly only when
  `tenancy_ocid`/`user_ocid`/`fingerprint`/`private_key` are all present,
  otherwise falling back to the SDK's `~/.oci/config`
  (`config_file_profile`, default `DEFAULT`). Instance Principal / Resource
  Principal / Security Token auth is out of MVP scope.
- oss's peek only supports `access_key`/`secret_key` (including
  `ALICLOUD_ACCESS_KEY`/`ALICLOUD_SECRET_KEY` env var fallback). STS tokens
  and ECS-role-based auth aren't supported.
- pg's peek is **unverified against an actual Postgres server**. Unlike the
  other HTTP-based backends, its wire protocol isn't easily mocked with
  httptest, and Docker/real-DB access for integration testing wasn't
  available in this environment either. The implementation carries high
  confidence from having read terraform's own source, but only the pure
  logic (e.g. `pgQuoteIdent`) is unit tested — the actual SQL issuance,
  session pinning, and error handling are unverified. Should be re-checked
  as a priority wherever a real DB is available.
- consul's peek only supports `address`/`scheme`/`datacenter`/
  `access_token`. `ca_file`/`cert_file`/`key_file` (mTLS) aren't supported
  (that configuration would need TLS settings added to `consulapi.Config`).
- kubernetes's peek only supports `in_cluster_config` / `config_path` /
  `config_context`. Standalone `host`+token auth, and each cloud provider's
  exec-style auth plugins (e.g. `aws eks get-token`), are out of MVP scope.
- For an IAM role that has `s3:GetObject` but not `s3:ListBucket`, S3's peek
  GetObject on a nonexistent `.tflock` object can come back as
  `403 AccessDenied` instead of `NoSuchKey`. In that case `isNotFound`
  returns false, `Peek` returns an error, and `warnIfLocked` silently skips
  the check ("unlocked" and "couldn't tell" become indistinguishable). This
  is consistent with the fail-silent design, but is worth spelling out as a
  known limitation.
- The S3 backend's peek only supports bucket/key/region/profile/
  dynamodb_table/use_lockfile. A custom S3-compatible endpoint
  (LocalStack/MinIO, etc.) or obtaining AWS credentials via `assume_role` is
  out of MVP scope (deferred to `config.LoadDefaultConfig`'s default chain).
  Supporting those configurations would require separately adding support
  for `s3.NewFromConfig` options and STS AssumeRole.
- If the CI environment has already set
  `TF_CLI_ARGS_plan="-lock=true"`, the `-lock=false` parraform appends wins
  under the last-flag-wins rule, and parraform's intent (running without
  acquiring the lock) silently prevails. This is reasonable given the tool's
  purpose, but it's worth noting that it's the opposite direction from an
  explicit command-line flag taking precedence.
- With an implicit default local backend (no `backend {}` block declared at
  all), `terraform init` doesn't write out the
  `.terraform/terraform.tfstate` cache file at all (confirmed by hands-on
  testing). In this case, `backendcfg.Discover` returns `(nil, nil)` and the
  lock check is silently skipped (treated the same as falling back to
  portable behavior). When `backend "local" {}` is declared explicitly, the
  cache is written and the check works correctly. S3/GCS and other
  non-local backends have no implicit default, so they're unaffected by
  this limitation.

## Out of scope (explicitly excluded items)

- Adjacent features like injecting a default `-lock-timeout` into `apply`.
- Implementing a strict mode that blocks/waits on lock detection (the
  interface only leaves room for future extension).
- Whether to support OpenTofu (`tofu`) is undecided (a small decision that
  only affects the binary-resolution part).
