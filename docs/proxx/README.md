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

Note: the root `.gitignore` ignores `docs/*`; add files here with `git add -f docs/proxx`.

## Repositories

| Repo | Local path | Upstream remote |
|---|---|---|
| `iamkunal9/proxx` (backend, this repo) | `.` | `upstream` → `router-for-me/CLIProxyAPI` |
| `iamkunal9/proxx-panel` (management UI) | `../proxx-panel` | `upstream` → `router-for-me/Cli-Proxy-API-Management-Center` |

Note: `../CLIProxyAPI` on this machine is an unrelated upstream checkout. The panel's own
AGENTS.md says to read backend contracts from `../CLIProxyAPI`; for proxx work use `../proxx`.

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
| `sdk/api/handlers/handlers_routing.go` | `proxxgroups.RequestProviders(model)` in `getRequestDetailsWithOptions` | model groups bypass provider lookup and win over `auto` resolution |
| `sdk/api/handlers/openai/openai_responses_websocket_session.go` | `proxxgroups.IsGroup(model)` in `responsesWebsocketResolvedModelName` | same for Responses websocket sessions |
| `sdk/cliproxy/auth/conductor_execution.go` | `m.proxxExecuteGroup` / `proxxExecuteCountGroup` / `proxxExecuteStreamGroup` at the top of `Execute` / `ExecuteCount` / `ExecuteStream` | expand a group into member executions |
| `sdk/cliproxy/auth/conductor_execution.go` | `maxWait = proxxGroupMaxWait(ctx, maxWait)` after `retrySettings()` (3x) | group members fail over instead of sleeping for a cooldown |
| `sdk/cliproxy/service_config.go` | `proxxgroups.Apply(newCfg)` in `commitConfigUpdate` | reload model groups |
| `sdk/cliproxy/service_lifecycle.go` | `proxxgroups.Apply(s.currentConfig())` in `Run` | load model groups at startup |

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
- Usage aggregator and panel: ADR 0005
