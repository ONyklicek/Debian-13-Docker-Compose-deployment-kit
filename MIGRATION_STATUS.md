# Cross-platform migration handoff

Status date: 2026-10-21  
Repository: `ONyklicek/Debian-13-Docker-Compose-deployment-kit`  
Worktree: isolated Copilot worktree on the current feature branch  
Overall state: **in progress; not feature-complete**

## Goal and non-negotiable context

Migrate the Debian/Bash Docker Compose deployment kit toward a portable Go
CLI/core for Windows, macOS, and Linux. Use Docker CLI contexts for local or
remote daemons, user-scoped config/state paths, and Compose for containers.
Debian remains a supported legacy deployment host, not a prerequisite for the
portable CLI. Do not run a real deployment or mutate a user's Docker daemon as
part of development verification. Do not claim parity while significant
commands remain Bash-only or unsupported.

The user explicitly requested implementation and continued autonomous work,
not another proposal or approval checkpoint. Preserve existing app.env and
preset conventions when practical; explicitly reject unsupported legacy
assumptions rather than silently dropping them. Keep UI/API and installer
alignment in scope.

## Verified implementation

- Portable Go CLI under `cmd/dkit`, with cross-platform config/state paths,
  Docker context discovery/selection, daemon info/status/doctor, and context-aware
  Docker/Compose invocation.
- App config support: list, static check, create, clone, get/set, env list/set/
  unset, read-only `app show`/`info`, plus domain and worker configuration
  commands. File edits are validated and atomically written; config-only
  commands do not call Docker. `app remove --yes` stops a Go-managed Compose
  project and archives app config while preserving its volumes and local state;
  explicit `--purge` removes Compose-managed volumes and that app's local state.
  Both removal modes require a trusted portable Compose model; legacy models
  without one are refused. `apps`/`ls`, `domains`, and `workers` legacy aliases
  are routed to portable implementations.
- Git app build and supported Compose model generation/lifecycle, including
  `app compose apply` which rerenders the current app config before a
  health-waiting Compose `up`; per-app
  MariaDB/PostgreSQL/Redis, workers, health probe, and named volumes. Compose
  `up` is not blue/green and has no public routing.
- DB shell/dump/import/migrate; app logs/exec; Restic backup, snapshot listing,
  staged restore, and export of existing Compose named volumes. Live named-volume
  archives do not guarantee application consistency.
- Read-only `dkit app compose config` validates Compose syntax/interpolation;
  `dkit app compose health [--json]` reports service status and requires the app
  plus a healthy HTTP probe.
- `dkit app compose rollback <name>` now keeps a verified one-step Compose model
  history, re-applies the previous model through the selected Docker context,
  waits for health, swaps the old/new local model history after success, and
  attempts to re-apply a private snapshot of the prior active model if rollback
  fails. This is local Compose model/image rollback only; no public traffic,
  blue/green, database, or migration rollback.
- `app show` reports config/build metadata only and explicitly marks Docker
  runtime, slots, and public routing as not queried.
- README now includes a concise public-command parity table. CI contains a
  Windows/macOS/Linux Go test/build matrix.

The latest verified work passed:

- `go test ./...`
- `go vet ./...`
- Cross-build: Windows/amd64, macOS/arm64, Linux/amd64
- `git diff --check`

No real Docker daemon or Compose parser was available for integration execution;
CLI lifecycle integration tests use mock executables.

## Current boundary and follow-up

The worktree now contains and tests `internal/composehistory`, which retains one
previous Go-managed rendered Compose model and validates SHA-256 manifests.
Rollback is wired to the CLI and mock tests cover success, unhealthy health
probe, selected context, stale/tampered active config, failed Compose up,
re-applying the prior model, and manifest promotion behavior. The
implementation must still receive code review as the project grows. A legacy
Compose file without a history manifest is not trusted as rollback material;
users need a later Go-managed render change before rollback is available.

The application does not yet have the legacy dual-slot blue/green engine,
public router integration, traffic switching, or equivalent routing rollback.
The Compose rollback must not be described as parity with `dkit rollback`.

## Public command parity: current evidence

| Legacy command area | Go support | Important missing behavior |
| --- | --- | --- |
| paths, context, docker, compose, status, doctor | Portable base supported | Not a replacement for daemon/platform provisioning |
| app list/check/create/clone/show/get/set/remove; env list/set/unset | Config-level support; removal requires `--yes` and a trusted Go-managed Compose project | No legacy full render/runtime validation; app edit, purge of legacy state, image cleanup, and shared-service deprovisioning are not migrated |
| domain and worker list/add/remove | Config-level support | No public proxy/TLS; worker routes explicitly rejected by renderer |
| build and app Compose lifecycle | Constrained support, including config-rendering `apply` | One app service, no blue/green, route switch, full legacy renderer or automatic deployment; `apply` can recreate services and does not build Git images |
| DB, logs, exec, backup | Partial portable support | Shared DB/S3/global backup parity and some legacy behaviors are absent |
| deploy, rollback, apply, start/stop/restart (legacy orchestration) | Linux/Bash only | Full blue/green deployment and state semantics remain missing in Go |
| Adminer, platform, UI/API, installer/upgrade | Linux/Bash only | Existing UI uses `/etc/dkit`, `/var/lib/dkit`, `/opt/dkit`, Docker and the legacy CLI |
| preset/template mutation, global config | Partial or Bash-only | Preset list/build use is present; create/edit/remove and full global settings parity are missing |

Primary sources inspected for this parity work: `bin/dkit`,
`lib/app.sh`, `lib/config.sh`, `lib/deploy.sh`, `lib/ops.sh`,
`lib/platform.sh`, `ui/main.go`, `ui/settings.go`, and `platform/compose.yaml`.
This is a working inventory, not a claim that every option/alias has been
exhaustively audited.

## Small next blocks

**Block 1 — complete the parity inventory (active)**  
Compare every public CLI command, alias, option, UI/API route/action, and
install/upgrade path against Go support. Record exact file/function evidence,
unsupported behavior, and prerequisites. The README matrix is only a summary,
not yet a complete option-by-option audit.

**Block 2 — harden Compose lifecycle history**  
Review atomic history writes, process interruption, recovery failure reporting,
old-model trust, context selection, and one-step swap semantics. Tests now
cover tampered active/previous files, failed up, recovery, and render-after-
rollback; inspect remaining write-failure and interrupted-write cases.

**Block 3 — routing and deployment design**  
Choose and document a portable routing model before implementation. Then add
health-gated switching and failure recovery in mock tests; keep actual live
deployment out of verification. Legacy Traefik behavior must not be represented
as migrated until equivalent behavior is implemented and tested.

**Block 4 — services and backups**  
Inventory shared DB/Redis/S3 provisioning, credentials, per-app isolation, and
global/app backup/restore. Port in small compatible pieces with explicit
consistency, security, and context boundaries.

**Block 5 — UI/API and installer**  
Replace hard-coded Linux UI paths and legacy CLI coupling with portable paths/
Go APIs, or provide a clearly supported portable alternative. Add an
installation and upgrade/migration path that does not hide a Bash/Debian
runtime requirement.

**Block 6 — final verification and handoff**  
Run Go tests, vet, formatting, three-platform builds and CI-relevant mock
integration tests. Review all public commands and docs. Do not mark complete
while any significant behavior is only a stub, Bash-only, or unsupported
without an explicitly agreed replacement.

## Validation commands

From repository root (PowerShell):

```powershell
go test ./...
go vet ./...
$env:CGO_ENABLED = '0'
$env:GOOS = 'windows'; $env:GOARCH = 'amd64'; go build ./cmd/dkit
$env:GOOS = 'darwin'; $env:GOARCH = 'arm64'; go build ./cmd/dkit
$env:GOOS = 'linux'; $env:GOARCH = 'amd64'; go build ./cmd/dkit
git diff --check
```

The active environment previously used Go 1.24.2 at
`C:\Users\Administrator\AppData\Local\Temp\dkit-go1.24.2\go\bin\go.exe`;
use the installed `go` command if available, otherwise this explicit path.
