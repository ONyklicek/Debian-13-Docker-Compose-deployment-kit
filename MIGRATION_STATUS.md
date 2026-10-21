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
  unset/replace, read-only `app show`/`info`, plus domain and worker configuration
  commands. File edits are validated and atomically written; config-only
  commands do not call Docker. `app remove --yes` stops a Go-managed Compose
  project and archives app config while preserving its volumes and local state;
  explicit `--purge` removes Compose-managed volumes and that app's local state.
  Both removal modes require a trusted portable Compose model; legacy models
  without one are refused. `apps`/`ls`, `domains`, and `workers` legacy aliases
  are routed to portable implementations.
- Git app build and supported Compose model generation/lifecycle, including
  `app compose apply` which rerenders the current app config before a
  health-waiting Compose `up`; per-app MariaDB/PostgreSQL/Redis, workers, health
  probe, and named volumes. The web route is managed by the Traefik file
  provider and atomically replaced after successful `up/apply/start`, removed
  before `down/stop/remove`; a failed stop/down restores the previous route.
  A per-app OS-level lock serializes mutating Compose operations, app removal,
  Git builds, blue/green deploy/rollback, and direct model writes.
  Worker path routes use Docker-provider labels.
  `start/restart` also wait for app and HTTP probe health before updating the route.
  This is not blue/green or a healthy slot switch.
- `dkit app compose deploy <name>` and the matching `rollback` now provide a
  limited web-only blue/green path for image apps and already-built Git apps.
  The candidate is pinned to its Docker image ID, the inactive profiled slot is
  started and HTTP-probe checked before route/state promotion, and the former
  slot remains available. This is separate from the ordinary single-service
  lifecycle. Workers, `PUBLISH_PORT`, automatic migrations and coordinated
  worker deployment are rejected/not performed; database and persistent data
  remain shared. Deployment state records the Docker context and refuses a
  context switch once an app has active slot state.
- DB shell/dump/import/migrate; app logs/exec; Restic backup, snapshot listing,
  staged restore, and export of existing Compose named volumes. Live named-volume
  archives do not guarantee application consistency.
- Portable UI engine: the UI image bundles the Go CLI and can be selected with
  `DKIT_UI_ENGINE=portable` and `DKIT_BIN=/usr/local/bin/dkit-go`. It supports
  app/config/env/preset/template editing and supported Compose actions,
  including Git ref builds and a validated Compose override editor in
  single-service mode. UI bulk deploy/apply/backup run as sequential per-app
  jobs; legacy remains the default and retains platform management and its
  broader deploy/migration workflows. Portable blue/green rejects Compose
  overrides and automatic migrations.
- Read-only `dkit app compose config` validates Compose syntax/interpolation;
  `dkit app compose health [--json]` reports service status and requires the app
  plus a healthy HTTP probe.
- In single-service mode `dkit app compose rollback <name>` keeps a verified one-step Compose model
  history, re-applies the previous model through the selected Docker context,
  waits for health, swaps the old/new local model history after success, and
  attempts to re-apply a private snapshot of the prior active model if rollback
  fails. This is local Compose model/image rollback only; no public traffic,
  dynamic web route, blue/green, database, or migration rollback.
- `app show` reports config/build metadata only and explicitly marks Docker
  runtime, slots, and public routing as not queried.
- README includes the public-command parity table and documents self-contained
  file-provider web routing, Docker-provider worker routes, socket-proxy exposure, network
  prerequisites, and remote-context limits. CI contains a Windows/macOS/Linux
  Go test/build matrix.
- The platform Compose model now enables Traefik's Docker provider through a
  non-published `docker-socket-proxy` on an internal control network. The proxy
  allows container discovery/events and read-only HTTP methods only; container
  metadata can still include environment configuration.

The latest verified work passed:

- `go test ./...`
- `go vet ./...`
- `docker compose -f platform/compose.yaml --env-file dkit.env.example config -q`
- Cross-build: Windows/amd64, macOS/arm64, Linux/amd64
- `git diff --check`
- Tests cover file-provider web route generation/replacement/removal,
  lifecycle integration, failed stop/down/remove route restoration, worker
  Docker-provider path priority, `PUBLIC=0`, and per-app lock exclusion.
  Blue/green mock integration tests cover first blue deploy, green promotion,
  rollback, pinned image IDs, health-gated route changes, unsupported workers,
  stale or context-mismatched Git builds rejected before Docker operations,
  safe rollback after image-selection changes and refusal on runtime config drift,
  pre-promotion revalidation when runtime files change during deploy or rollback,
  Compose-model checksum
  and runtime app-config plus `env`/`runtime.env`/`services.env` checksum
  enforcement (while image/build selection changes remain rollback-compatible),
  and protection from single-service destructive actions.
- `docker compose config -q` passed for the platform model and a generated
  public app with a worker path route and the internal blue/green model.

No real Docker deployment was performed. Platform Compose and a generated
public app/worker Compose model passed `docker compose config -q`; CLI lifecycle
integration tests use mock executables.

## Current boundary and follow-up

The worktree now contains and tests `internal/composehistory`, which retains one
previous Go-managed rendered Compose model and validates SHA-256 manifests.
Rollback is wired to the CLI and mock tests cover success, unhealthy health
probe, selected context, stale/tampered active config, failed Compose up,
re-applying the prior model, and manifest promotion behavior. The
implementation must still receive code review as the project grows. A legacy
Compose file without a history manifest is not trusted as rollback material;
users need a later Go-managed render change before rollback is available.

The Go blue/green deploy flow uses the two-image Compose renderer and validated
slot route. Candidate images are pinned to Docker image IDs so a mutable image
tag cannot silently change the prior slot's rollback target. Candidate web and
probe are started under a slot profile; only after the HTTP probe is healthy is
the route atomically changed and deployment state promoted. The old active slot
is not stopped. Rollback re-starts/health-checks the previous slot before
switching the route and swapping state. It does not wait for Traefik to confirm
that its asynchronous file-provider reload completed. Workers and
`PUBLISH_PORT` are rejected, migrations are not run, and both versions share
the app's DB/Redis and persistent volumes; apps must tolerate both versions
accessing shared data during the transition. At active blue/green state,
single-service lifecycle, render-with-write and app removal are blocked to
avoid taking down the slot project; `ps`, health, logs and exec use the active
slot. Portable UI uses Go deployment metadata and Compose status, but does not
coordinate workers or database migrations; legacy UI retains its Bash
deployment state model.

The ordinary web route file is atomically replaced after successful Compose
`up/apply/start`, and contains its own namespaced security middleware. Traefik
file-provider health checks against `HEALTH_PATH` gate forwarding to its single
backend. This can leave the router returning 503; no previous healthy slot is
retained or promoted. Worker path routes still use Docker-provider labels. The
generated HTTP probe is a separate container and does not set the app
container's Docker health status.
The proxy's minimal Docker API sections (`CONTAINERS`, `EVENTS`, `PING`,
`VERSION`) match the provider's list/inspect, event stream, ping, and
server-version requests.
The Go CLI writes the dynamic route in its local state directory; the platform
Traefik bind mount must see that same file. The CLI does not copy it to a remote
Docker daemon host.
Neither Go rollback mode is full parity with legacy `dkit rollback`.

## Public command and interface parity audit

Audit performed against the checked-in CLI dispatch and command handlers,
not just README examples. “Linux fallback” means `cmd/dkit.runLegacy` launches
`bash bin/dkit` on Linux; it is not a portable Go implementation. On Windows
and macOS that fallback returns an explicit unsupported-platform error.

### Legacy CLI crosswalk

| Legacy surface | Legacy implementation | Portable Go behavior and exact boundary |
| --- | --- | --- |
| `help`, `version`; global `--yes`/`-y` | `bin/dkit:25-75` | Go has help and version, but version currently says “development build”. Go does not strip `--yes` globally; it is accepted only by specific commands (`app remove`, `db import`, `db migrate`). |
| `apps`, `ls`; `app list`/`ls` | `bin/dkit:77-94`, `lib/app.sh:116-130` | Go supports `apps`/`ls`, `app list`/`ls`, `--json`. Go app list reads config only; it does not query runtime containers as legacy JSON/text output does. |
| `app create`/`new`/`add` | `bin/dkit:86-94`, `lib/app.sh:28-114` | Go implements `app create` only. Supports `--preset`/`-p`, repeated `--domain`/`-d`, `--source`, `--repo`/`-r`, `--branch`/`-b`, `--image`/`-i`, `--services`/`-s`, `--port`; `--yes`/`-y` is accepted but creation is already noninteractive. Legacy `new`/`add`, prompts, SSH-key setup and full `render_app` validation are not Go features. Unknown Go app subcommands try Linux fallback. |
| `app clone`/`copy` | `bin/dkit:86-94`, `lib/config.sh:287-317` | Go implements `clone` with repeatable `--domain`/`-d` and `--no-env`; it copies config/environment according to that flag and initializes fresh credentials through the Go package. `copy` is legacy-only; no state, SSH deploy key, or legacy Compose override cloning parity is promised. |
| `app show`/`info` | `lib/app.sh:131-141` | Go implements `show`/`info`, optional `--json`, and explicitly reports config/build metadata without runtime query. Legacy displays Docker project status when state exists. |
| `app get`, `app set` | `lib/config.sh:62-86` | Go implements get (single key or JSON) and validated set/`--unset`; edits are file-only and do not apply containers. |
| `app check`/`validate`, `app edit` | `lib/app.sh:143-159` | Go check/validate is static config validation with optional JSON. There is no Go app edit command; it falls back to Bash on Linux and fails as unsupported elsewhere. Legacy edit can edit `app.env`, `env` and `compose.override.yaml`, then render/validate. |
| `app remove`/`rm`/`delete` | `lib/app.sh:161-186` | Go requires `--yes` (or `-y`) and a trusted Go-managed Compose model; ordinary remove stops project, archives config and preserves state/volumes. `--purge` removes its Compose volumes and local app state. It does not remove images/shared services or purge legacy apps without a trusted model. Legacy removal has different interactive confirmation and shared-service/image cleanup behavior. |
| `domain`/`domains` list/add/remove | `lib/config.sh:143-157` | Go supports list/add/remove and aliases, canonicalizes and validates domains as app config edits. `app compose up/apply/start` atomically writes a file-provider web route; worker paths use Docker-provider labels. There are no slots or route rollback. |
| `env` list/set/unset/replace/import | `lib/config.sh:88-141` | Go supports list/`ls`, set, unset and stdin `replace`; list has `--show` or `--json` and masks secrets by default. Legacy `import` remains unsupported. No change automatically applies runtime env. |
| `worker`/`workers` list/add/remove | `lib/config.sh:159-203` | Go supports list/add/remove; add accepts `--cmd`, `--port`, `--path`, `--user`. Edits remain config-only; renderer maps configured worker paths to Traefik routers only when public routing is enabled. |
| `config get/set/edit` | `lib/config.sh:205-227` | Go implements config `get [--json]` and stdin/key-value `set`; the UI does not automatically restart the platform in portable mode. Legacy `edit` behavior remains Bash-only. |
| `preset`/`presets` list/create/edit/remove | `lib/config.sh:229-260` | Go implements list/show/create/remove. Portable custom presets are local to the configured config directory; full legacy interactive editing remains Bash-only. |
| `template`/`templates` list/edit/create/reset | `lib/config.sh:262-285` | Go implements list/show/create/reset in the configured config directory; full legacy template editing workflow remains Bash-only. |
| `deploy [--all] [--ref] [--migrate] [--no-migrate]` | `lib/deploy.sh:189-244`, `lib/config.sh:318-331` | `dkit app compose deploy <name> [--ref <git-ref>]` implements web-only blue/green and builds a requested Git ref first. Legacy `dkit deploy`, bulk orchestration, and migration flags still use Linux fallback only. |
| `rollback` | `lib/deploy.sh:246-272` | `dkit app compose rollback <name>` uses the prior slot and switches its route when blue/green state exists; otherwise it restores one verified Compose model. Neither mode reverses DB migrations. Legacy command also coordinates workers and other state not covered by Go. |
| `apply`, `start`, `stop`, `restart` (including `--all`) | `lib/deploy.sh:274-317`, `bin/dkit:94-96` | Go provides constrained `app compose apply/up/down/start/stop/restart` for a trusted one-project model. It does not preserve legacy active-slot/state/route semantics. Legacy `--all` and `--all` failure aggregation use Bash only. |
| `status` | `bin/dkit:97-100`, `cmd/dkit/main.go:284-312` | Go `status [--json]` reports Docker daemon and containers for selected context; legacy additionally checks Traefik and prints app registry/slot information. |
| `logs`, `exec`/`sh` | `lib/ops.sh:4-38` | Go supports logs `-f`/`--follow`, `--tail`/`-n`, service selection and app exec with optional service/`-- command`. Portable versions require rendered image apps; Git apps fall back to Bash on Linux. Legacy targets active `web-*` slot, while Go targets its supported Compose service model. |
| `db shell`, `dump`, `import`; migration | `lib/ops.sh:40-66`, `lib/deploy.sh:189-244` | Go supports dedicated DB shell/dump/import (import requires `--yes` and SQL stdin) and `db migrate <app> --yes` using `MIGRATE_CMD`. Dedicated rendered image apps only; shared services/Git apps may use Linux fallback. No shared DB/S3/global service parity. Legacy DB import confirms interactively and deploy can combine `--migrate`/`--no-migrate`. |
| `adminer` | `lib/ops.sh:68-91` | No Go equivalent; Linux fallback only. Legacy starts/stops a loopback-only container and requires an app DB. |
| `backup [app|--all]`, `backup list [app]` | `lib/ops.sh:150-166` | Go supports app backup, snapshot list, and `backup restore <app> [snapshot] --target <new-directory>`. Restore is staging-only and does not change live app/volume data. App backup supports dedicated rendered image app services; bulk/platform/config/shared S3 backup remains Linux fallback only. Legacy retention policy, DB dumps, persistent paths and shared/platform backup have distinct semantics; volume consistency limits remain documented. |
| `platform up/status/logs/restart`, `ui start/stop/status/token` | `lib/platform.sh:32-110` | No Go equivalent. Both remain Bash/Linux operations tied to `platform/compose.yaml`, Traefik, system paths and token lifecycle. |
| `doctor` | `lib/platform.sh:91-110` | Go doctor checks portable Docker CLI/Compose/context/daemon prerequisites. Legacy doctor also checks Linux kit config, edge network, Traefik, Git/jq/openssl/flock, Restic, backup config and each app. |
| New portable commands | `cmd/dkit/main.go:36-85` | Go adds `paths`, `context list/current/use`, context-aware `docker` and `compose`, `app compose render/config/health`, and standalone app build. These have no direct legacy command parity. |

Aliases exposed only by the Bash dispatcher include `app new/add/copy`,
`presets`, `templates` and the global `--yes` behavior. Go's default dispatch
uses `runLegacy` for selected unknown/legacy commands; this must not be counted
as portable support. The Linux bridge also requires the checked-out legacy
`bin/dkit` and Bash (`cmd/dkit/main.go:1701-1738`).

### UI/API, installer and CI crosswalk

The UI is a Go HTTP server with a selectable CLI engine. Legacy remains the
default and retains `/opt/dkit`, `/etc/dkit`, `/var/lib/dkit` and platform
actions. Portable mode is selected with `DKIT_UI_ENGINE=portable` and
`DKIT_BIN=/usr/local/bin/dkit-go`; the UI image bundles that CLI and shares
configured config/state paths. It supports app/config/env/preset/template
editing and supported Compose actions, but omits bulk actions and platform
management.

| UI routes/actions | Handler evidence | Migration state |
| --- | --- | --- |
| Login/logout, health, dashboard, app create/detail/logs | `ui/main.go` routes | Available in both modes; portable mode reads Go deployment metadata and queries Compose status when a model exists. `/healthz` is unauthenticated. |
| App files, actions, settings/env and clone | `ui/main.go`, `ui/settings.go` | Portable mode routes supported operations through Go CLI; unsupported legacy operations are hidden or rejected. |
| Bulk action | `ui/main.go` | Legacy mode only; portable mode explicitly rejects it. |
| Global settings and preset/template read/mutation | `ui/settings.go` | Portable CLI implements config get/set and preset/template operations in the configured config directory. |
| Jobs list/detail/raw output | `ui/main.go:718-720`; job storage in `ui/main.go` | UI-local job system persists under the legacy state directory and runs background CLI processes; no portable replacement/service lifecycle has been implemented. |

`install.sh:1-160` is explicitly Debian 13, root-only, and supports amd64/arm64.
It installs apt packages and Docker Engine/Compose/Buildx, copies Bash/UI assets
to `/opt/dkit`, initializes `/etc/dkit` and `/var/lib/dkit`, creates the legacy
token, and optionally starts platform/UI. Flags are `--email`, `--start`,
`--ui`; it has no Go binary distribution/install/upgrade path and does not
migrate legacy state into portable per-user directories. Its rsync `--delete`
is scoped to the kit-owned install directory; config/state are deliberately
preserved.

`.github/workflows/ci.yml` tests and builds Go on Ubuntu/macOS/Windows, runs
shellcheck and Go vet/build for CLI and UI, and validates templates/Compose on
Ubuntu. It does not run the Linux deployment engine or real lifecycle
integration against a Docker daemon; CLI lifecycle behavior is covered with
mock executables.

**Audit conclusion:** this inventory covers every dispatcher command group,
documented legacy option family, UI route/action and installer entry point
found in the checked-in source. It is a parity map, not a claim that the
portable migration is complete. Detailed config-key-by-config-key compatibility
and every internal renderer field still need a separate renderer/service audit.

## Small next blocks

**Block 1 — complete the parity inventory (reviewed; boundaries recorded)**
The command dispatcher, command families/options, UI routes/actions, installer,
and CI are now cross-referenced above. Remaining compatibility work is the
separate app.env/render field and shared-service semantics review.

**Block 2 — harden Compose lifecycle history (active)**
Review atomic history writes, process interruption, recovery failure reporting,
old-model trust, context selection, and one-step swap semantics. Tests cover
tampered active/previous files, failed up, recovery, render-after-rollback and
selected context. A newly found failure-to-write-health-output inconsistency
was fixed with a regression test. `VerifyActive` now always requires a valid
manifest, including when the active file equals the current render; a stale or
missing manifest left by an interrupted write fails closed. Regression coverage
checks missing/malformed manifests and an interrupted active-model write.
`PromotePrevious` now writes a private transaction journal containing hashes
and pre-promotion snapshots before replacing the previous model, active model,
and manifest. The final manifest is the commit marker: startup recovery rolls
back incomplete promotions, recognizes complete commits, and removes the
journal. Reads/writes of history all attempt recovery first; malformed journals
fail closed. Tests simulate interruption before mutation, after each of the
three atomic writes, and a corrupt journal. Each history operation now also
takes an OS-level per-app exclusive file lock (released by the OS on process
exit); concurrent render tests verify the resulting active and previous model
hashes. Atomic writes sync file contents before rename and sync the containing
directory after rename on Unix. Windows file contents are synced, but directory
entry durability is best-effort because the Go standard library does not offer
a portable Windows directory fsync operation. Actual guarantees still depend
on the filesystem and storage device.

**Block 3 — routing and deployment design (implemented)**
The web route now uses Traefik's file provider to support atomic route-file
replacement and carries its own security middleware; worker path routes
continue through the Docker provider behind the internal read-only-method
socket proxy. Compose `up/apply/start` writes the web route only after the
Compose command succeeds; `down/stop/remove` removes it before stopping
containers and restores it if Compose fails. File-provider active
health checks use `HEALTH_PATH`; `start/restart` poll app/probe health before
writing the route. Single-service mode can still return 503 and is not
blue/green. The separate web-only blue/green flow now health-gates a second
slot before switching routes; mock tests cover promotion, rollback, failure
recovery and input-integrity checks. It does not coordinate workers or
migrations, and Traefik's asynchronous file-provider reload is not confirmed.
Keep live deployment out of verification.

**Block 4 — services and backups**  
Inventory shared DB/Redis/S3 provisioning, credentials, per-app isolation, and
global/app backup/restore. Port in small compatible pieces with explicit
consistency, security, and context boundaries.

**Block 5 — UI/API and installer**
A selectable portable UI/CLI mode is implemented; the default legacy UI and
installer remain Debian/Bash-specific. Portable UI accepts a Git ref for a
build-before-deploy flow, validates the effective Compose model when saving
`compose.override.yaml`, and runs bulk deploy/apply/backup sequentially as
per-app jobs. Remaining work is end-to-end UI testing and legacy workflow gaps
(platform operations and migration orchestration); do not imply full legacy
parity.

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

Latest Linux verification for the current portable CLI/UI wiring:

- `go test ./...`, `go vet ./...`, and native builds pass in both Go modules.
- CLI cross-builds pass for Windows/amd64, macOS/arm64 and Linux/amd64.
- Platform `docker compose ... config -q` and UI `docker buildx build --check`
  pass. No Docker image build or deployment was run.
- `gofmt` and `git diff --check` pass.
