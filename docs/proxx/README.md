# proxx: fork guide

`proxx` is a fork of [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI).
It adds five features on top of upstream while staying cheap to sync:

1. **Model groups / auto model**: a virtual model (e.g. `auto`) that routes to the first
   available real model across any configured providers, skipping rate-limited ones.
2. **Visibility**: an in-process usage and spend aggregator, management endpoints for it,
   and a reskinned management panel (`iamkunal9/proxx-panel`, fork of
   `router-for-me/Cli-Proxy-API-Management-Center`).
3. **Pricing**: per-token prices per model (catalog + config overrides), per-request cost
   (upstream-reported cost first, config/catalog otherwise, with an override flag), and
   `pricing` objects in model listings.
4. **Request log viewer**: browse, search, inspect and live-tail the per-request log files
   (`request-log: true`) through `/v8/management/proxx/requests`, joined with usage/cost, plus
   size-based retention (`proxx.request-logs.max-total-size-mb`).
5. **Self-update**: check, download, verify (ed25519-signed `checksums.txt`) and restart in place when idle
   (same PID, same listening socket, automatic rollback), from the panel or on a schedule
   (`/v8/management/proxx/update`, `proxx.update.*`).

Decisions live in [`decisions/`](decisions/) as numbered ADRs. Read them before changing a feature.

Note: the root `.gitignore` ignores `docs/*` but un-ignores `docs/proxx/` (hook below).

## Repositories

| Repo | Local path | Upstream remote |
|---|---|---|
| `iamkunal9/proxx` (backend, this repo) | `.` | `upstream` → `router-for-me/CLIProxyAPI` |
| `iamkunal9/proxx-panel` (management UI) | `../proxx-panel` | `upstream` → `router-for-me/Cli-Proxy-API-Management-Center` |

Note: `../CLIProxyAPI` on this machine is an unrelated upstream checkout. The panel's own
AGENTS.md says to read backend contracts from `../CLIProxyAPI`; for proxx work use `../proxx`.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/iamkunal9/proxx/main/install.sh | sh
```

[`install.sh`](../../install.sh) downloads the latest release for the OS/CPU, checks it against
`checksums.txt`, and installs `proxx` to `~/.local/bin` (`/usr/local/bin` as root). The config lives in
`~/.proxx/config.yaml`. On first install it copies Homebrew's `cliproxyapi.conf` when present (same
format; the panel repo is switched to proxx-panel), otherwise `config.example.yaml`; an existing config
is never overwritten. It then starts a launchd agent (`io.github.iamkunal9.proxx`, logs in
`~/.proxx/proxx.log`) or a systemd user unit (`proxx`). Options: `sh -s -- --no-service`,
`--version <tag>`, `--install-dir <dir>`, `--home <dir>`, `--no-plugin`, `--uninstall` (or the `PROXX_*`
variables listed at the top of the script). Re-running it upgrades in place.

It also writes `~/.proxx/.install-method` (`script`). The service gives proxx time to drain on stop:
systemd `TimeoutStopSec=130`, and launchd `ExitTimeOut 0` (launchd caps agent values at 60s, so proxx
bounds its own shutdown instead). After installing, updates can also be done from the panel (ADR 0007).

The installer only stops, replaces or removes a service whose plist/unit runs **its own** binary (`$BIN`).
- A service file for the same label that runs another binary is left alone, and a service install over it
  is refused.
- `--no-service` never stops or restarts a service.
- The stop wait matches processes whose command line starts exactly with `$BIN --config ` (it is not a
  `pgrep -f` substring match).
- **Tests and experiments must never run install.sh with the real `HOME` or the default label.** Use a temp
  `HOME`, `PROXX_HOME`, `PROXX_INSTALL_DIR` and a unique `PROXX_SERVICE_LABEL`, as
  `test/proxx/install_e2e` does. On 2026-11-17 a manual `install.sh --no-service` run with the real `HOME`
  unloaded the developer's own agent; that is what these rules prevent.

Migrating from Homebrew CLIProxyAPI: `brew services stop cliproxyapi`, then run the installer.

## Releases

- Backend: push a tag `v<upstream version>-proxx.<n>` (e.g. `v8.0.73-proxx.1`) on `main`. The upstream
  `release.yaml` workflow builds `proxx_<version>_<os>_<arch>` archives for macOS, Linux, Windows and
  FreeBSD plus `checksums.txt` and `checksums.txt.sig`; `install.sh` picks them up from "latest".
  - `checksums.txt.sig` is the base64 ed25519 signature of `checksums.txt`, made by `cmd/proxx-sign` from
    the repo secret `PROXX_RELEASE_SIGNING_KEY` (base64 32-byte seed).
  - The workflow verifies the signature against the key embedded in `internal/proxx/selfupdate/source.go`.
    The self-updater refuses unsigned releases.
- Tags containing `-rc` (e.g. `v8.0.73-proxx.2-rc1`) become GitHub prereleases, so they never become "latest".
  Use them for a real release test (see "Pinning a release candidate" in ADR 0007).
- Panel: push a `v*` tag on proxx-panel (e.g. `v1.28.0-proxx.2`); the proxy auto-updates the panel.
  - If the panel needs a newer backend, put the line `proxx-min-backend-api: N` in the release body.
    It may sit inside an HTML comment: `<!-- proxx-min-backend-api: 2 -->`.
  - Backends whose `api_level` (in `GET /v8/management/proxx/update`) is below N keep their installed panel.
- Forks start with Actions off: if a pushed tag starts no run, enable Actions
  (`gh api -X PUT repos/<repo>/actions/permissions -F enabled=true -f allowed_actions=all`) and re-push the tag.
- Disabled in the fork (`gh workflow disable`): `docker-image.yml` (pushes to upstream's Docker Hub),
  `auto-retarget-main-pr-to-dev.yml`, `agents-md-guard.yml`.

## Quick start

- Build from source: `go build -o proxx ./cmd/server`
- Example config for a bot on free APIs with an `auto` model: [`examples/free-auto.yaml`](examples/free-auto.yaml)
- Known pre-existing upstream test failure (not proxx): `internal/pluginhost` `TestPluginErrorCarriesRetryAfterIntoCooldownAndHTTPHeader` (fails on unmodified upstream main, Nov 2026).

## Syncing with upstream

```bash
git fetch upstream
git merge upstream/main     # conflicts should be limited to the hook lines listed below
gofmt -w . && go build ./cmd/server && go test ./...
```

Same flow in `../proxx-panel` (`bun run verify` afterwards).

## Fork rules (keep merges cheap)

- The Go module path stays `github.com/router-for-me/CLIProxyAPI/v8` (ADR 0001).
- New code goes in **new files**: `internal/proxx/...` packages, `*_proxx.go` files, `docs/proxx/`.
- Changes to upstream files are **one-line hooks** marked with a `proxx:` comment.
  List every hook in the table below when you add one.
- All proxx config lives under the single top-level `proxx:` key (`internal/config/proxx.go`).
- All proxx management routes live under `/v8/management/proxx/` (`internal/api/server_management_proxx.go`).

## Upstream hook points

| File | Hook | Purpose |
|---|---|---|
| `internal/config/config.go` | `Proxx ProxxConfig` field | the `proxx:` config root |
| `internal/config/config_v8.go` | `"proxx": true` in `v8AllowedRoots` | stop v8 saves from commenting out `proxx:` |
| `internal/api/server_management_v8.go` | `s.registerProxxManagementRoutes(v8)` | proxx management routes |
| `sdk/cliproxy/usage/manager.go` | `applyCostHook(ctx, &record)` in `Publish` | per-request cost on every usage record (ADR 0004) |
| `sdk/cliproxy/usage/detail.go` | `FinalCost`, `CostSource` fields + 2 lines in `CostDetails.Clone` | cost carried to all usage sinks (queue JSON `cost_details.final_cost/cost_source`) |
| `sdk/cliproxy/service_lifecycle.go` | `proxx.Start(...)` in `Run`, `proxx.Shutdown()` in `Shutdown` (+ import) | load model groups, start pricing catalog + usage aggregator; flush snapshot |
| `sdk/cliproxy/service_config.go` | `proxx.Reload(newCfg)` in `commitConfigUpdate` (+ import) | hot reload of `proxx.model-groups` / `proxx.pricing` / `proxx.usage` |
| `sdk/api/handlers/openai/openai_handlers.go` | `pricing.AttachPricing(filteredModels, "id")` (+ import) | `pricing` object in OpenAI `/v1/models` |
| `sdk/api/handlers/claude/code_handlers.go` | `pricing.AttachPricing(h.Models(), "id")` inside `ClaudeModels` (+ import) | `pricing` object in Claude `/v1/models` |
| `sdk/api/handlers/gemini/gemini_handlers.go` | `pricing.AttachPricing(normalizedModels, "name")` (+ import) | `pricing` object in Gemini `/v1beta/models` |
| `internal/api/server_routes.go` | `pricing.AttachPricing(...)` in `handleHomeModels` (2 lines + import) | `pricing` object in Home-mode listings |
| `config.example.yaml` | commented `proxx:` example block (incl. `request-logs`) | documentation only |
| `.gitignore` | `!docs/proxx/` after `docs/*` | track fork docs |
| `README.md` | one proxx header line at the top | branding (ADR 0001) |
| `internal/config/config_defaults.go`, `internal/managementasset/updater.go`, `config.example.yaml` | default panel repo → `iamkunal9/proxx-panel` (1 line each) | proxx panel by default (ADR 0001) |
| `Dockerfile` | binary built and run as `proxx` (3 lines) | binary name (ADR 0001) |
| `.github/workflows/release.yaml` | `cli-proxy-api` → `proxx`, `CLIProxyAPI_` → `proxx_` throughout | release binary/archive names (ADR 0001) |
| `.github/workflows/release.yaml` | fetch upstream tags + cap generated notes at 60k chars (2 lines in `prepare-release`) | fork releases: changelog range and GitHub's 125k body limit |
| `sdk/api/handlers/handlers_routing.go` | `proxxgroups.RequestProviders(model)` in `getRequestDetailsWithOptions` | model groups bypass provider lookup and win over `auto` resolution |
| `sdk/api/handlers/openai/openai_responses_websocket_session.go` | `proxxgroups.IsGroup(model)` in `responsesWebsocketResolvedModelName` | same for Responses websocket sessions |
| `sdk/cliproxy/auth/conductor_execution.go` | `m.proxxExecuteGroup` / `proxxExecuteCountGroup` / `proxxExecuteStreamGroup` at the top of `Execute` / `ExecuteCount` / `ExecuteStream` | expand a group into member executions |
| `sdk/cliproxy/auth/conductor_execution.go` | `maxWait = proxxGroupMaxWait(ctx, maxWait)` after `retrySettings()` (3x) | group members fail over instead of sleeping for a cooldown |
| `cmd/server/main.go` | `selfupdate.Startup()` + `os.Exit(selfupdate.Finish(run()))` in `main` (+ import) | self-update: start attempts/rollback, `--proxx-selfcheck`; exec the staged binary after shutdown (ADR 0007) |
| `internal/cmd/run.go` | `selfupdate.SetRestartTrigger(cancel)` after `signal.NotifyContext` (+ import) | self-update restarts through the normal graceful shutdown |
| `internal/api/server.go` | `selfupdate.Track(...)` around the root handler; `selfupdate.Listen(addr)` instead of `net.Listen` (+ import) | in-flight counter (incl. hijacked WebSockets); inherit the socket across the exec |
| `sdk/cliproxy/service_lifecycle.go` | `proxx.ShutdownContext(s.currentConfig())` instead of the 30s `context.WithTimeout` in `Run` | shutdown budget follows the drain (default 120s) + exit watchdog |
| `internal/managementasset/updater.go` | `proxxPanelCompatible(data)` in `fetchLatestAsset` | skip panel releases that declare `proxx-min-backend-api` above our `api_level` |
| `.github/workflows/release.yaml` | `--prerelease` for `-rc` tags (3 lines in `prepare-release`); checkout + setup-go + sign/verify/upload `checksums.txt.sig` in `publish-checksums` (all marked `# proxx:`) | signed releases for self-update; rc prereleases |
| `config.example.yaml` | `# proxx: 0 (unset) drains for up to 120s` under `shutdown-timeout` | documentation only |

Fork-owned files that other proxx features also touch: `internal/api/server_management_proxx.go`
(one `s.registerProxx*Routes(proxx)` line per feature), `internal/proxx/proxx.go` (lifecycle facade),
`internal/config/proxx.go` (config root).

## Request flow (upstream, for orientation)

```
client → gin route (internal/api/server_routes.go)
       → sdk/api/handlers/<format> handler
       → handlers_routing.go: getRequestDetailsWithOptions
             ("auto" → util.ResolveAutoModel upstream; provider list from registry.GetModelProviders)
       → sdk/cliproxy/auth Manager.Execute / ExecuteStream / ExecuteCount (conductor_execution.go)
             retry rounds → executeMixedOnce → pickNextMixed (scheduler: priority tiers, then
             round-robin / weighted / fill-first) → per-credential model candidates
             (alias pools in conductor_models.go) → executor
       → internal/runtime/executor/<provider> → upstream HTTP
       → usage: helps/usage_helpers.go publishRecord → usage.Manager.Publish → plugins
             (redisqueue, plugin host, SDK plugins)
       → MarkResult (conductor_cooldown.go): 429 → Retry-After or exp. backoff cooldown
```

Model catalogs: `internal/registry` (`models.json` embedded + remote refresh every 3h,
`models.catalog` / `codex-catalog` / `devin-catalog` override sources, `--local-model` = embedded only).

Management panel: `internal/managementasset` downloads `management.html` from the latest
GitHub release of `remote-management.panel-github-repository`.

## Feature map

Filled in as features land (see the ADRs for design):

- Model groups: ADR 0003. Config `proxx.model-groups`; store `internal/proxx/groups`; failover
  loop `sdk/cliproxy/auth/model_groups_proxx.go`; `GET /v8/management/proxx/groups`;
  response header `X-Proxx-Served-Model`. E2E:
  `PROXX_E2E=1 go test ./test/proxx/groups_e2e/ -run E2E -v -count=1` (builds the binary and runs it
  against a mock OpenAI-compatible upstream; needs no provider keys).
- Pricing and cost: ADR 0004
  - `internal/proxx/pricing` (catalog, lookup, cost, listings), embedded snapshot
    `internal/proxx/pricing/snapshot.json` (regenerate: `go run ./cmd/proxx_gen_pricing [-in api.json]`),
    `internal/registry/price_catalog_proxx.go` (refresh loop), `sdk/cliproxy/usage/cost_hook_proxx.go` (hook).
  - Management: `GET /v8/management/proxx/pricing`.
- Usage aggregator and panel: ADR 0005
  - `internal/proxx/usagestats` (usage plugin `proxx-usage`, snapshot file), `internal/proxx/proxx.go`
    (lifecycle facade), handlers in `internal/api/handlers/management/usage_proxx.go`, routes in
    `internal/api/server_management_proxx_usage.go`.
  - Management: `GET /v8/management/proxx/usage/summary`, `GET .../usage/recent`, `DELETE .../usage`.
- Request log viewer and retention: ADR 0006
  - `internal/proxx/requestlogs` (directory index, section parser, SSE assembler, usage join plugin
    `proxx-request-logs`, retention, live-tail poller), driven from the `internal/proxx/proxx.go` facade
    (no new upstream hooks). Handlers in `internal/api/handlers/management/requestlogs_proxx.go`, routes in
    `internal/api/server_management_proxx_requestlogs.go`.
  - Config: `proxx.request-logs.max-total-size-mb` (unset = 10240, 0 = unlimited).
  - Management: `GET /v8/management/proxx/requests` (list/filter/search), `GET .../requests/stream` (SSE),
    `GET .../requests/:id`, `GET .../requests/:id/raw`, `DELETE .../requests/:id`, `DELETE .../requests`.
    Request logging itself is toggled with the existing `PUT /v8/management/config/observability/logs/request-log`
    (raw JSON `true`/`false`).
- Self-update: ADR 0007
  - `internal/proxx/selfupdate` (release check with ETag, ed25519 + sha256 verification, staging,
    in-flight tracker, listener gate and fd handoff, in-place exec, rollback state file, auto-apply window),
    started from the `internal/proxx/proxx.go` facade. Handlers in
    `internal/api/handlers/management/selfupdate_proxx.go`, routes in `internal/api/server_management_proxx_update.go`.
    The panel compatibility check is in `internal/managementasset/panel_compat_proxx.go`, and release
    signing in `cmd/proxx-sign`.
  - Config: `proxx.update.check` (default true), `interval-hours` (24), `auto-apply` (false), `window`
    (`HH:MM-HH:MM` local, may wrap midnight, empty = any time). Set them with
    `PUT /v8/management/config/proxx/update/<key>` and a raw JSON body.
  - Management:
    - `GET /v8/management/proxx/update` (status, `api_level`, `inflight`, `last_result`);
    - `POST .../update/check`;
    - `POST .../update/apply` (202, or 409 `{error}`);
    - `POST .../update/cancel`.
  - Default drain: an unset `server.shutdown-timeout` drains in-flight requests for up to 120s on any stop
    (`-1` = close immediately).
  - State file: `proxx-update-state.json` next to the config. The previous binary is kept as `<exe>.prev`.

## Tests

```bash
go test ./internal/proxx/...                                     # unit tests (pricing formula, lookup, aggregator, groups, request logs)
PROXX_E2E=1 go test ./test/proxx/groups_e2e/ -run E2E -v -count=1  # e2e: model groups
PROXX_E2E=1 go test ./test/proxx/requestlogs_e2e/ -run E2E -v -count=1  # e2e: request log viewer + retention
go test -tags proxxe2e -count=1 -v ./test/proxx/pricing_e2e/     # e2e: builds and runs the real binary
PROXX_E2E=1 go test ./test/proxx/selfupdate_e2e/ -run TestE2ESelfUpdate -v -count=1 -timeout 30m  # e2e: self-update (also in CI: proxx-e2e.yml)
PROXX_E2E=1 PROXX_E2E_LAUNCHD=1 go test ./test/proxx/selfupdate_e2e/ -run TestE2ELaunchd -v -count=1  # macOS: real launchd agent (temp label/port)
PROXX_E2E=1 PROXX_E2E_INSTALL=1 go test ./test/proxx/install_e2e/ -v -count=1  # macOS: install.sh in a temp HOME with temp labels (downloads the latest release)
```

The e2e test builds `./cmd/server`, starts it with `--local-model` and a temp `config-version: 8`
config (test client key only), points an `openai-compatibility` entry named `openrouter` and an
`api-keys.claude` group at an in-process mock upstream, and checks cost source and amount for upstream
`usage.cost` (non-stream and stream), `cost_in_usd_ticks`, a cost header, config, per-model override,
catalog, unknown, and the Claude cache formula; `pricing` objects in OpenAI/Claude/Gemini listings; the management endpoints;
snapshot persistence across a SIGINT restart; and hot reload of a price, `override-upstream` and
`proxx.usage.recent-limit`.
