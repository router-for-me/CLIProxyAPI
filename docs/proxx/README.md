# proxx: fork guide

`proxx` is a fork of [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI).
It adds three features on top of upstream while staying cheap to sync:

1. **Model groups / auto model**: a virtual model (e.g. `auto`) that routes to the first
   available real model across any configured providers, skipping rate-limited ones.
2. **Visibility**: an in-process usage and spend aggregator, management endpoints for it,
   and a reskinned management panel (`iamkunal9/proxx-panel`, fork of
   `router-for-me/Cli-Proxy-API-Management-Center`).
3. **Pricing**: per-token prices per model (catalog + config overrides), per-request cost
   (upstream-reported cost first, config/catalog otherwise, with an override flag), and
   `pricing` objects in model listings.

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

Migrating from Homebrew CLIProxyAPI: `brew services stop cliproxyapi`, then run the installer.

## Releases

- Backend: push a tag `v<upstream version>-proxx.<n>` (e.g. `v8.0.73-proxx.1`) on `main`. The upstream
  `release.yaml` workflow builds `proxx_<version>_<os>_<arch>` archives for macOS, Linux, Windows and
  FreeBSD plus `checksums.txt`; `install.sh` picks them up from "latest".
- Panel: push a `v*` tag on proxx-panel (e.g. `v1.28.0-proxx.2`); the proxy auto-updates the panel.
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
| `config.example.yaml` | commented `proxx:` example block | documentation only |
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

Fork-owned files that other proxx features also touch: `internal/api/server_management_proxx.go`
(one `s.registerProxx*Routes(proxx)` line per feature).

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

## Tests

```bash
go test ./internal/proxx/...                                     # unit tests (pricing formula, lookup, aggregator, groups)
PROXX_E2E=1 go test ./test/proxx/groups_e2e/ -run E2E -v -count=1  # e2e: model groups
go test -tags proxxe2e -count=1 -v ./test/proxx/pricing_e2e/     # e2e: builds and runs the real binary
```

The e2e test builds `./cmd/server`, starts it with `--local-model` and a temp `config-version: 8`
config (test client key only), points an `openai-compatibility` entry named `openrouter` and an
`api-keys.claude` group at an in-process mock upstream, and checks cost source and amount for upstream
`usage.cost` (non-stream and stream), `cost_in_usd_ticks`, a cost header, config, per-model override,
catalog, unknown, and the Claude cache formula; `pricing` objects in OpenAI/Claude/Gemini listings; the management endpoints;
snapshot persistence across a SIGINT restart; and hot reload of a price, `override-upstream` and
`proxx.usage.recent-limit`.
