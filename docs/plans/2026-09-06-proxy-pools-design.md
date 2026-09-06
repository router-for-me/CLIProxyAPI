# Design: Proxy Pools for NixLLM (adopted from 9router)

**Date:** 2026-09-06
**Status:** Approved design (brainstorming session 2026-09-06; implementation plan to follow)
**Goal:** Adopt the full 9router Proxy Pools workflow (https://github.com/decolua/9router) as a named egress-proxy-pool entity in NixLLM, with per-upstream-entry binding, test/health-check/batch-import/bulk operations, relay pools (header-mode), and one-click relay deployment to Cloudflare/Vercel/Deno — without touching the per-executor request path.

## Context & Motivation

NixLLM already has complete egress-proxy plumbing at the transport layer:

- `sdk/proxyutil` — `Parse` / `BuildHTTPTransport` / `BuildDialer` support `http`, `https`, `socks5`, `socks5h` (plus `direct`/`none` sentinels for explicit bypass).
- Global `proxy-url` config (`internal/config/sdk_config.go`) with management API `/v0/management/proxy-url`.
- **Per-entry `proxy-url` end-to-end**: `proxy_url` columns on PG `upstream_provider_api_key_entries` and `upstream_providers`, rendered by `internal/upstreamsync` → `internal/watcher/synthesizer` → `Auth.ProxyURL` → consumed by every executor via `helps.NewProxyAwareHTTPClient` / `helps.NewUtlsHTTPClient` / `rtprovider`.

What does **not** exist (verified by grep) is the operational layer 9router has: a named pool entity, pool test / health check / batch import / bulk ops, pool-to-entry binding (entries currently require a raw URL), `noProxy` host-lists, and relay pools (base-URL rewriting via `x-relay-target`/`x-relay-path` headers — no such header exists anywhere in the repo).

9router's workflow (source of truth, `/tmp/9router` clone):

1. Pool entity: `name`, `proxyUrl`, `noProxy`, `type` (`http` / `vercel` / `cloudflare` / `deno`), `isActive`, `strictProxy`, `testStatus`, `lastTestedAt`, `lastError`.
2. CRUD + `boundConnectionCount` on list (delete blocked with 409 while connections are bound).
3. Per-connection binding via `providerSpecificData.proxyPoolId` (`__none__` = explicit no-proxy), resolution order: pool → legacy per-connection URL → none.
4. Test: `HEAD https://google.com` through the proxy (8s default, 30s cap) → records status, **auto-deactivates on failure**.
5. Health check: concurrent test sweep with progress.
6. Batch import: lines of `scheme://host:port` or `host:port:user:pass` → auto-named `Imported host:port`, deduped by URL.
7. Bulk activate / deactivate / delete.
8. Relay deploy: embedded worker code, one-shot platform token (never stored), polling until ready, pool auto-created on success.
9. `strictProxy`: false = fall back to direct on proxy failure; true = fail hard.

## Decisions (from the brainstorming session)

1. **Binding scope:** per **upstream entry** (per API key inside a provider pool), mirroring 9router's per-connection model. Client-facing NixLLM API keys are out of scope.
2. **Feature scope: A + B full** — standard pools + `noProxy`/`strict` semantics + relay header-mode + one-click deploy to Cloudflare/Vercel/Deno. Relay deploy tokens are one-shot from the request body, never stored.
3. **Test behavior: status-only.** A failed test records `test_status=error` + `last_error`; it does **not** deactivate the pool. The admin decides. (9router auto-deactivates; rejected as too surprising.)
4. **`strict` default: `true`** (opposite of 9router's `false`), preserving NixLLM's existing fail-hard behavior — a silent fallback to direct would leak the server's real IP.
5. Binding is limited to the PG pool types (`claude-api-key`, `openai-compatibility`). YAML-only provider types (standalone gemini/codex/vertex keys) are out of scope; the legacy per-entry raw `proxy-url` remains available.
6. Pools are a **configuration concern, not a runtime concern**: they resolve at render time into concrete fields that already flow today; the conductor/routing logic is untouched.

## Architecture & Data Model

Pipeline follows the existing flow: **PG pool rows → render (upstreamsync) → config.yaml + Auth fields → synthesizer → transport**. Executors and the conductor are not modified.

### PG table `proxy_pools`

New table (idempotent migration in `internal/store/postgresstore.go`, following the `upstream_providers` pattern):

| column | type | notes |
|---|---|---|
| `id` | same id pattern as `upstream_providers` | PK |
| `name` | TEXT NOT NULL | shown in pickers |
| `proxy_url` | TEXT NOT NULL | proxy URL (type http) or relay base URL |
| `no_proxy` | TEXT default `''` | CSV host-list |
| `type` | TEXT default `http` | `http` / `vercel` / `cloudflare` / `deno` |
| `is_active` | BOOL default `true` | traffic toggle |
| `strict_proxy` | BOOL default `true` | see Transport semantics |
| `test_status` | TEXT default `unknown` | `unknown` / `active` / `error` |
| `last_tested_at` | TIMESTAMPTZ | last test result time |
| `last_error` | TEXT | last test error, NULL on success |

Binding columns (idempotent `ADD COLUMN IF NOT EXISTS`):

- `upstream_provider_api_key_entries.proxy_pool_id` — per-entry override
- `upstream_providers.proxy_pool_id` — row-level default, inherited by entries with NULL `proxy_pool_id`

### Resolution precedence

At render time, with inactive/deleted pools treated as unset:

```
entry.proxy_pool_id → row.proxy_pool_id → entry.proxy_url (manual, legacy) → row.proxy_url → global proxy-url → environment
```

The editor UI makes pool vs manual URL mutually exclusive per entry (choosing a pool clears the manual URL and vice versa).

## Render pipeline (PG → concrete fields)

- `internal/upstreamsync/render.go` (`claudeKeyFromProvider` / `openAICompatFromProvider`): before writing `entry.ProxyURL`, resolve the binding. If an active pool is bound, write the **composite URL** (http-type pools; format below) or stamp the relay base URL (relay-type pools).
- The rendered `proxy-url:` line in config.yaml stays self-contained: an operator reading the file can see which proxy an entry uses without joining back to the pool table.
- Inactive or deleted pool → binding treated as empty → entry falls through to manual URL / row default / global (precedence above).

### Relay path (separate representation)

Relay pools cannot be encoded as a proxy URL because the target base URL is rewritten via `x-relay-target`/`x-relay-path` headers. Therefore:

- `sdk/cliproxy/auth/types.go` gains `RelayBaseURL` on `Auth`.
- The synthesizer (`internal/watcher/synthesizer/config.go`) stamps `RelayBaseURL` when the bound pool is an active relay pool (overriding `ProxyURL`).
- Both fields flow through the conductor unchanged.

### Transport wiring

- `helps/proxy_helpers.go`, `helps/utls_client.go`, `sdk/cliproxy/rtprovider.go` constructors: when `Auth.RelayBaseURL` is set, build a `relayTransport{base, inner}` RoundTripper (see below) instead of a proxy dialer; when `Auth.ProxyURL` carries the `no_proxy`/`strict` suffix, forward it to `proxyutil.BuildHTTPTransport`.
- Delete protection: DELETE on a pool still bound by any entry or row returns **409 + `bound_entry_count`** (mirrors 9router's `boundConnectionCount`).

## Transport semantics: noProxy, strict, relay

### Composite URL format

Pool attributes are encoded as a query suffix on the proxy URL written into `entry.ProxyURL`:

```
socks5://user:pass@host:1080?no_proxy=api.anthropic.com,.internal&strict=true
```

`sdk/proxyutil.Parse()` is extended: parse the query before determining the mode, strip the suffix, and expose `NoProxy []string` + `Strict bool` on the result. `Redact` continues to work. Unknown suffix keys are rejected (fail-fast when an operator hand-edits rendered YAML).

### noProxy

Host-list semantics: exact host, `.suffix` (subdomain match), `*` (all). Applied inside `proxyutil` only — when the target URL's host matches, `Parse`/`BuildDialer` yield a direct transport (overriding proxy mode). One matching function, used by both paths; executors stay oblivious.

### strict (default true)

- `strict=true` — proxy failure fails the request (NixLLM's existing fail-hard behavior; no silent IP leak).
- `strict=false` — RoundTripper-level fallback: a dial via the proxy errors → one direct retry, with a warning log.

### Relay RoundTripper

`relayTransport` (in `helps`, wired through the existing ctx-injection path): stores relay `base` + inner transport; per request: parse target → set `x-relay-target` (scheme://host) and `x-relay-path` (path+query) → delete those two headers + `Host` → send to `base`. Validation: base must be `https`, target must be `http(s)`.

### Test endpoint semantics

- Relay pools: `GET relay + x-relay-target=https://httpbin.org + x-relay-path=/get`.
- http pools: `HEAD https://www.google.com` through the full transport (noProxy + strict apply). Timeout 8s default, 30s cap.
- Results write `test_status` / `last_error` / `last_tested_at` only — **status-only, no auto-deactivate** (decision 3).

## Management API & Store

- Store: `internal/store/pg_proxy_pools.go`, following `pg_upstream_providers.go`: `ProxyPool` struct + scan helper, `ProxyPoolStore` interface (List/Get/Create/Update/Delete), `bound_entry_count` query (LEFT JOIN entries + rows on `proxy_pool_id`, row-level bindings included). Feature detection without `PGSTORE_DSN` → 503, same as `upstreamProvidersStore()`.
- Routes (`internal/api/server_management.go`, group `/v0/management`):

```
GET    /proxy-pools             ?include_usage=1 → +bound_entry_count
POST   /proxy-pools             create (URL validated per allowed schemes)
GET    /proxy-pools/:id
PUT    /proxy-pools/:id         update (field merge, canonical type)
DELETE /proxy-pools/:id         409 + {bound_entry_count} while bound
POST   /proxy-pools/:id/test    status-only test
POST   /proxy-pools/batch-import    {lines: [string]} → {created, skipped, failed, errors[]}
POST   /proxy-pools/relay-deploy    {platform: vercel|cloudflare|deno, creds…, project_name}
```

- Re-render trigger: every pool mutation (create/update/delete/import/deploy) calls the existing `applyUpstreamProviders(ctx)` (`internal/api/handlers/management/handler.go` → `upstreamsync.ApplyArtifacts`), so bindings join the latest pool values at render. Same pattern as upstream-provider mutations today.
- Batch import is parsed server-side (not a client loop): per line `scheme://host[:port]` or `host:port:user:pass` → normalized `http://user:pass@host:port`, auto-named `Imported host:port`, deduped by `proxy_url` — one request, aggregated per-line results with line numbers on error.
- `relay-deploy`: one parameterized endpoint (vs 9router's three): platform tokens are one-shot from the request body, never stored; worker relay code embedded as Go constants per platform; on success the pool is created with the platform's `type`. Pure `net/http` multipart/JSON to each platform API, no new SDKs.

## Dashboard SPA

- New page `ProxyPoolsPage` (`web/dashboard/src/pages/`, route `/proxy-pools`, Sidebar entry), one file list + modals, `InternalUsersPage` pattern:
  - Table: name, type badge, URL (redacted), test status (+`last_error` tooltip), `last_tested_at`, `is_active` toggle, `bound_entry_count`, row actions (test, edit, delete with 409 bound-count message).
  - Toolbar: mass **Health Check** (concurrency 10, progress `n/total`, target = selection or all), bulk activate/deactivate/delete, **Batch Import** modal (textarea, per-line preview), **Relay Deploy** dropdown → 3 small modals (Vercel token / Cloudflare accountId+token / Deno token+orgDomain, + project name); server polls; result pool appears in list.
  - Form modal: name, type (default http), proxy_url, no_proxy, strict_proxy (default ON), is_active.
- Provider editor (`upstream-provider-editor/`): per-entry + row-level "Proxy pool" dropdown (searchable) listing active pools + `Direct (no proxy)` + `Manual URL` (legacy `proxy_url` field, mutually exclusive with pool). `form.js` hydrate/payload emits `proxy_pool_id` or `proxy_url`. The picker re-fetches the pool list when the editor opens.
- Client API (`api/client.js` + `managementEndpoints.js`): CRUD + `testProxyPool` + `importProxyPools` + `deployRelay`, following endpoint-specific list keys (`pools`) and `Array.isArray()` guards.

## Error handling

- Pool mutations re-render and reload config; render failures log and surface through the existing management-error path.
- Delete-while-bound → 409 with `bound_entry_count`; UI shows the count.
- Relay deploy: platform API errors are surfaced verbatim with status codes; polling failure cleans up (Deno path deletes the half-created app, mirroring 9router).
- Composite-URL parse failures at transport time fail fast with a clear error (invalid suffix key).
- Test timeouts: 8s default / 30s cap → `test_status=error`, `last_error` carries the cause.

## Testing

- Go: store round-trips (skipped without `PGSTORE_TEST_DSN`, same as upstream-providers tests), `proxyutil` suffix parsing + noProxy matching + relay transport (httptest), handler CRUD + test + import + deploy against mocked platform APIs, render/synthesizer stamping of pool resolution (composite URL and `RelayBaseURL`).
- JS: `ProxyPoolsPage.test.js` (list/bulk/test/deploy modal states), editor picker tests (pool vs manual-URL exclusivity, hydrate/payload).
- Regression net: the 494-test auth suite covers conductor behavior; executors/conductor are untouched by design.

## Out of scope / follow-ups

- Binding for YAML-only provider types (standalone gemini/codex/vertex keys).
- Pool-of-pools rotation strategy (`pickProxyPoolId` round-robin/random) — 9router applies it only to no-auth free providers, which NixLLM does not have.
- Relay-deploy token persistence / re-deploy management.
- TLS-fingerprint spoofing for proxied traffic (9router keeps it disabled too).
