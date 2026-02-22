# Merged Fragmented Markdown

## Source: operations/auth-refresh-failure-symptom-fix.md

# Auth Refresh Failure Symptom/Fix Table

Use this table when token refresh is failing for OAuth/session-based providers.

| Symptom | How to Confirm | Fix |
| --- | --- | --- |
| Requests return repeated `401` after prior success | Check logs + provider metrics for auth errors | Trigger manual refresh: `POST /v0/management/auths/{provider}/refresh` |
| Manual refresh returns `401` | Verify management key header | Use `Authorization: Bearer <management-key>` or `X-Management-Key` |
| Manual refresh returns `404` | Check if management routes are enabled | Set `remote-management.secret-key`, restart service |
| Refresh appears to run but token stays expired | Inspect auth files + provider-specific auth state | Re-login provider flow to regenerate refresh token |
| Refresh failures spike after config change | Compare active config and recent deploy diff | Roll back auth/provider block changes, then re-apply safely |

## Fast Commands

```bash
# Check management API is reachable
curl -sS http://localhost:8317/v0/management/config \
  -H "Authorization: Bearer <management-key>" | jq

# Trigger a refresh for one provider
curl -sS -X POST http://localhost:8317/v0/management/auths/<provider>/refresh \
  -H "Authorization: Bearer <management-key>" | jq

# Inspect auth file summary
curl -sS http://localhost:8317/v0/management/auth-files \
  -H "Authorization: Bearer <management-key>" | jq
```

## Related

- [Provider Outage Triage Quick Guide](./provider-outage-triage-quick-guide.md)
- [Critical Endpoints Curl Pack](./critical-endpoints-curl-pack.md)

---
Last reviewed: `2026-02-21`  
Owner: `Auth Runtime On-Call`  
Pattern: `YYYY-MM-DD`


---

## Source: operations/checks-owner-responder-map.md

# Checks-to-Owner Responder Map

Route each failing check to the fastest owner path.

| Check | Primary Owner | Secondary Owner | First Response |
| --- | --- | --- | --- |
| `GET /health` fails | Runtime On-Call | Platform On-Call | Verify process/pod status, restart if needed |
| `GET /v1/models` fails/auth errors | Auth Runtime On-Call | Platform On-Call | Validate API key, provider auth files, refresh path |
| `GET /v1/metrics/providers` shows one provider degraded | Platform On-Call | Provider Integrations | Shift traffic to fallback prefix/provider |
| `GET /v0/management/config` returns `404` | Platform On-Call | Runtime On-Call | Enable `remote-management.secret-key`, restart |
| `POST /v0/management/auths/{provider}/refresh` fails | Auth Runtime On-Call | Provider Integrations | Validate management key, rerun provider auth login |
| Logs show sustained `429` | Platform On-Call | Capacity Owner | Reduce concurrency, add credentials/capacity |

## Paging Guidelines

1. Page primary owner immediately when critical user traffic is impacted.
2. Add secondary owner if no mitigation within 10 minutes.
3. Escalate incident lead when two or more critical checks fail together.

## Related

- [Provider Outage Triage Quick Guide](./provider-outage-triage-quick-guide.md)
- [Auth Refresh Failure Symptom/Fix Table](./auth-refresh-failure-symptom-fix.md)

---
Last reviewed: `2026-02-21`  
Owner: `Incident Commander Rotation`  
Pattern: `YYYY-MM-DD`


---

## Source: operations/critical-endpoints-curl-pack.md

# Critical Endpoints Curl Pack

Copy/paste pack for first-response checks.

## Runtime Canonical Probes

```bash
# Health probe
curl -sS -f http://localhost:8317/health | jq

# Operations provider status
curl -sS -f http://localhost:8317/v0/operations/providers/status | jq

# Operations load-balancing status
curl -sS -f http://localhost:8317/v0/operations/load_balancing/status | jq

# Runtime metrics surface (canonical unauth probe)
curl -sS -f http://localhost:8317/v1/metrics/providers | jq

# Exposed models (requires API key)
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer <api-key>" | jq '.data[:10]'
```

## Management Safety Checks

```bash
# Effective runtime config
curl -sS http://localhost:8317/v0/management/config \
  -H "Authorization: Bearer <management-key>" | jq

# Auth files snapshot
curl -sS http://localhost:8317/v0/management/auth-files \
  -H "Authorization: Bearer <management-key>" | jq

# Recent logs
curl -sS "http://localhost:8317/v0/management/logs?lines=200" \
  -H "Authorization: Bearer <management-key>"
```

## Auth Refresh Action

```bash
curl -sS -X POST \
  http://localhost:8317/v0/management/auths/<provider>/refresh \
  -H "Authorization: Bearer <management-key>" | jq
```

## Deprecated Probes (Not Implemented In Runtime Yet)

```bash
# Deprecated: cooldown endpoints are not currently registered
curl -sS http://localhost:8317/v0/operations/cooldown/status
```

## Use With

- [Provider Outage Triage Quick Guide](./provider-outage-triage-quick-guide.md)
- [Checks-to-Owner Responder Map](./checks-owner-responder-map.md)

---
Last reviewed: `2026-02-21`  
Owner: `SRE`  
Pattern: `YYYY-MM-DD`


---

## Source: operations/distributed-fs-compute-status.md

# Distributed FS/Compute Status

Last reviewed: `2026-02-21`  
Scope: current implementation status for distributed-ish auth storage, file-sync, and runtime compute control paths.

## Status Matrix

| Track | Status | Evidence (current code/docs) | Notes |
| --- | --- | --- | --- |
| Auth/config persistence backends (Postgres/Object/Git/File) | Implemented | `cmd/server/main.go:226`, `cmd/server/main.go:259`, `cmd/server/main.go:292`, `cmd/server/main.go:361`, `cmd/server/main.go:393`, `cmd/server/main.go:497` | Runtime can boot from multiple storage backends and register a shared token store. |
| Local file-change ingestion (config + auth dir) | Implemented | `pkg/llmproxy/watcher/watcher.go:88`, `pkg/llmproxy/watcher/events.go:36`, `pkg/llmproxy/watcher/events.go:42`, `pkg/llmproxy/watcher/events.go:77` | Uses `fsnotify`; this is node-local watching, not a distributed event system. |
| Auth update compute queue + burst drain | Implemented | `sdk/cliproxy/service.go:130`, `sdk/cliproxy/service.go:137`, `sdk/cliproxy/service.go:140`, `sdk/cliproxy/service.go:154`, `sdk/cliproxy/service.go:640` | Queue depth fixed at 256; drains backlog in tight loop. |
| Runtime compute attachment via websocket provider sessions | Implemented | `sdk/cliproxy/service.go:535`, `sdk/cliproxy/service.go:537`, `sdk/cliproxy/service.go:230` | Websocket channels can add/remove runtime auths dynamically. |
| Periodic auth refresh worker in core runtime | Implemented | `sdk/cliproxy/service.go:666` | Core manager auto-refresh starts at 15m interval. |
| Provider metrics surface for ops dashboards | Implemented | `pkg/llmproxy/api/server.go:370` | `/v1/metrics/providers` is live and should be treated as current operational surface. |
| Cooldown/recovery control plane endpoints (`/v0/operations/*`) | In Progress | `docs/features/operations/USER.md:720`, `docs/features/operations/USER.md:725`, `docs/features/operations/USER.md:740`; route reality: `pkg/llmproxy/api/server.go:331`, `pkg/llmproxy/api/server.go:518` | Docs/spec describe endpoints, but runtime only exposes `/v1` and `/v0/management` groups today. |
| Liveness endpoint (`/health`) contract | Blocked | `docs/api/operations.md:12`, `docs/features/operations/USER.md:710`; no matching route registration in `pkg/llmproxy/api/server.go` | Ops docs and runtime are currently out of sync on health probe path. |
| Distributed multi-node state propagation (cross-node auth event bus) | Blocked | local watcher model in `pkg/llmproxy/watcher/events.go:36`, `pkg/llmproxy/watcher/events.go:42`; queue wiring in `sdk/cliproxy/service.go:640` | Current flow is single-node event ingestion + local queue handling. |
| Generic operations API for cooldown status/provider status/load-balancing status | Blocked | docs claims in `docs/features/operations/USER.md:720`, `docs/features/operations/USER.md:725`, `docs/features/operations/USER.md:740`; runtime routes in `pkg/llmproxy/api/server.go:331`, `pkg/llmproxy/api/server.go:518` | No concrete handler registration found for `/v0/operations/...` paths. |

## Architecture Map (Current)

```text
Storage Backends (FS/Git/Postgres/Object)
  -> token store registration (cmd/server/main.go)
  -> core auth manager load (sdk/cliproxy/service.go)
  -> watcher fsnotify loop (pkg/llmproxy/watcher/events.go)
  -> auth update queue (sdk/cliproxy/service.go, buffered 256)
  -> auth apply/update + model registration (sdk/cliproxy/service.go)
  -> API server routes (/v1/* + /v0/management/* + /v1/metrics/providers)

Parallel runtime path:
Websocket gateway (/v1/ws and /v1/responses)
  -> runtime auth add/remove events
  -> same auth queue/apply pipeline
```

Key boundary today:
- Distributed storage backends exist.
- Distributed coordination plane does not (no cross-node watcher/event bus contract in runtime paths yet).

## Next 10 Actionable Items

1. Add a real `GET /health` route in `setupRoutes` and return dependency-aware status (`pkg/llmproxy/api/server.go`).
2. Introduce `/v0/operations/providers/status` handler backed by core auth + registry/runtime provider state (`sdk/cliproxy/service.go`, `pkg/llmproxy/api/server.go`).
3. Expose cooldown snapshot endpoint by wrapping existing Kiro cooldown manager state (`pkg/llmproxy/auth/kiro/cooldown.go`, `pkg/llmproxy/runtime/executor/kiro_executor.go`).
4. Add `/v0/operations/load_balancing/status` using current selector/routing strategy already switched in reload callback (`sdk/cliproxy/service.go`).
5. Emit queue depth/drain counters for `authUpdates` to make backpressure visible (`sdk/cliproxy/service.go:130`, `sdk/cliproxy/service.go:154`).
6. Add API tests asserting presence/response shape for `/health` and `/v0/operations/*` once implemented (`pkg/llmproxy/api` test suite).
7. Define a node identity + backend mode payload (file/git/postgres/object) for ops introspection using startup configuration paths (`cmd/server/main.go`).
8. Add an optional cross-node event transport (Postgres `LISTEN/NOTIFY`) so non-local auth mutations can propagate without filesystem coupling. See [Actionable Item 8 Design Prep](#actionable-item-8-design-prep-postgres-listennotify).
9. Reconcile docs with runtime in one pass: update `docs/features/operations/USER.md` and `docs/api/operations.md` to only list implemented endpoints until new handlers ship.
10. Extend `docs/operations/critical-endpoints-curl-pack.md` with the new canonical health + operations endpoints after implementation, and deprecate stale probes.

## Actionable Item 8 Design Prep (Postgres LISTEN/NOTIFY)

Goal: propagate auth/config mutation events across nodes without changing existing local watcher semantics.

Design constraints:
- Non-breaking: current single-node fsnotify + local queue path remains default.
- Optional transport: only enabled when a Postgres DSN and feature flag are set.
- At-least-once delivery semantics with idempotent consumer behavior.
- No cross-node hard dependency for startup; service must run if transport is disabled.

### Proposed Transport Shape

Channel:
- `cliproxy_auth_events_v1`

Emit path (future runtime implementation):
- On successful local auth/config mutation apply, issue `NOTIFY cliproxy_auth_events_v1, '<json-payload>'`.
- Local origin node should still process its own queue directly (no dependency on loopback notify).

Receive path (future runtime implementation):
- Dedicated listener connection executes `LISTEN cliproxy_auth_events_v1`.
- Each received payload is validated, deduped, and enqueued onto existing `authUpdates` path.

### Payload Schema (JSON)

```json
{
  "schema_version": 1,
  "event_id": "01JZ9Y2SM9BZXW4KQY4R6X8J6W",
  "event_type": "auth.upsert",
  "occurred_at": "2026-02-21T08:30:00Z",
  "origin": {
    "node_id": "node-a-01",
    "instance_id": "pod/cliproxy-7f6f4db96b-w2x9d",
    "backend_mode": "postgres"
  },
  "subject": {
    "auth_id": "openai-default",
    "provider": "openai",
    "tenant_id": "default"
  },
  "mutation": {
    "revision": 42,
    "kind": "upsert",
    "reason": "api_write"
  },
  "correlation": {
    "request_id": "req_123",
    "actor": "operations-api"
  }
}
```

Field notes:
- `event_id`: ULID/UUID for dedupe.
- `event_type`: enum candidate set: `auth.upsert`, `auth.delete`, `config.reload`.
- `mutation.revision`: monotonically increasing per `auth_id` if available; otherwise omitted and dedupe uses `event_id`.
- `origin.node_id`: stable node identity from startup config.

### Failure Modes and Handling

1. Notify payload dropped or listener disconnect:
- Risk: missed event on one or more nodes.
- Handling: periodic reconciliation poll (`N` minutes) compares latest auth/config revision and self-heals drift.

2. Duplicate delivery (at-least-once):
- Risk: repeated apply work.
- Handling: dedupe cache keyed by `event_id` (TTL 10-30m) before enqueue.

3. Out-of-order events:
- Risk: stale mutation applied after newer one.
- Handling: if `mutation.revision` exists, ignore stale revisions per `auth_id`; otherwise rely on timestamp guard plus eventual reconcile.

4. Oversized payload (> Postgres NOTIFY payload limit):
- Risk: event reject/truncation.
- Handling: keep payload metadata-only; never include secrets/token material; fetch full state from source-of-truth store on consume.

5. Channel flood/backpressure:
- Risk: queue saturation and delayed apply.
- Handling: preserve current bounded queue; add drop/lag metrics and alert thresholds before turning feature on by default.

6. Poison payload (invalid JSON/schema):
- Risk: listener crash or stuck loop.
- Handling: strict decode + schema validation, count and discard invalid events, continue loop.

### Rollout Plan (Non-Breaking)

Phase 0: Design + observability prep (this track)
- Finalize schema and channel names.
- Add docs for SLOs and required metrics.

Phase 1: Dark launch behind feature flag
- Add emitter/listener code paths disabled by default.
- Enable only in one non-prod environment.
- Validate no behavior change with flag off.

Phase 2: Canary
- Enable on 1 node in a multi-node staging cluster.
- Verify cross-node propagation latency and dedupe hit rate.
- Run failover drills (listener reconnect, DB restart).

Phase 3: Staged production enablement
- Enable for low-risk tenants first.
- Keep reconciliation poll as safety net.
- Roll back by toggling flag off (local path still active).

Phase 4: Default-on decision
- Require stable error budget over 2 release cycles.
- Promote only after ops sign-off on latency, drift, and invalid-event rates.

### Test Plan

Unit tests:
- Payload encode/decode and schema validation.
- Dedupe cache behavior for duplicate `event_id`.
- Revision ordering guard (`newer` wins).

Integration tests (Postgres-backed):
- Node A emits `auth.upsert`, Node B receives and enqueues.
- Listener reconnect after forced connection drop.
- Invalid payload does not crash listener loop.

Resilience tests:
- Burst notifications at > steady-state rate to validate queue pressure behavior.
- Simulated dropped notifications followed by reconciliation repair.
- Postgres restart during active mutation traffic.

Operational acceptance criteria:
- P95 propagation latency target defined and met in staging.
- No secret/token bytes present in emitted payload logs/metrics.
- Drift detector returns to zero after reconciliation window.


---

## Source: operations/provider-outage-triage-quick-guide.md

# Provider Outage Triage Quick Guide

Use this quick guide when a provider starts failing or latency spikes.

## 5-Minute Flow

1. Confirm process health:
   - `curl -sS -f http://localhost:8317/health`
2. Confirm exposed models still look normal:
   - `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <api-key>" | jq '.data | length'`
3. Inspect provider metrics for the failing provider:
   - `curl -sS http://localhost:8317/v1/metrics/providers | jq`
4. Check logs for repeated status codes (`401`, `403`, `429`, `5xx`).
5. Reroute critical traffic to fallback prefix/provider.

## Decision Hints

| Symptom | Likely Cause | Immediate Action |
| --- | --- | --- |
| One provider has high error ratio, others healthy | Upstream outage/degradation | Shift traffic to fallback provider prefix |
| Mostly `401/403` | Expired/invalid provider auth | Run auth refresh checks and manual refresh |
| Mostly `429` | Upstream throttling | Lower concurrency and shift non-critical traffic |
| `/v1/models` missing expected models | Provider config/auth problem | Recheck provider block, auth file, and filters |

## Escalation Trigger

Escalate after 10 minutes if any one is true:

- No successful requests for a critical workload.
- Error ratio remains above on-call threshold after reroute.
- Two independent providers are simultaneously degraded.

## Related

- [Critical Endpoints Curl Pack](./critical-endpoints-curl-pack.md)
- [Checks-to-Owner Responder Map](./checks-owner-responder-map.md)

---
Last reviewed: `2026-02-21`  
Owner: `Platform On-Call`  
Pattern: `YYYY-MM-DD`


---

## Source: operations/release-governance.md

# Release Governance and Checklist

Use this runbook before creating a release tag.

## 1) Release Gate: Required Checks Must Be Green

Release workflow gate:

- Workflow: `.github/workflows/release.yaml`
- Required-check manifest: `.github/release-required-checks.txt`
- Rule: all listed checks for the tagged commit SHA must have at least one successful check run.

If any required check is missing or non-successful, release stops before Goreleaser.

## 2) Breaking Provider Behavior Checklist

Complete this section for any change that can alter provider behavior, auth semantics, model routing, or fallback behavior.

- [ ] `provider-catalog.md` updated with behavior impact and rollout notes.
- [ ] `routing-reference.md` updated when model selection/routing semantics changed.
- [ ] `provider-operations.md` updated with new mitigation/fallback/monitoring actions.
- [ ] Backward compatibility impact documented (prefix rules, alias behavior, auth expectations).
- [ ] `/v1/models` and `/v1/metrics/providers` validation evidence captured for release notes.
- [ ] Any breaking behavior flagged in changelog under the correct scope (`auth`, `routing`, `docs`, `security`).

## 3) Changelog Scope Classifier Policy

CI classifier check:

- Workflow: `.github/workflows/pr-test-build.yml`
- Job name: `changelog-scope-classifier`
- Scopes emitted: `auth`, `routing`, `docs`, `security` (or `none` if no scope match)

Classifier is path-based and intended to keep release notes consistently scoped.

## 4) Pre-release Config Compatibility Smoke Test

CI smoke check:

- Workflow: `.github/workflows/pr-test-build.yml`
- Job name: `pre-release-config-compat-smoke`
- Verifies:
  - `config.example.yaml` loads via config parser.
  - OAuth model alias migration runs successfully.
  - migrated config reloads successfully.

## Related

- [Required Branch Check Ownership](./required-branch-check-ownership.md)
- [Checks-to-Owner Responder Map](./checks-owner-responder-map.md)

---
Last reviewed: `2026-02-21`  
Owner: `Release Engineering`  
Pattern: `YYYY-MM-DD`


---

## Source: operations/required-branch-check-ownership.md

# Required Branch Check Ownership

Ownership map for required checks and release gate manifests.

## Required Check Sources

- Branch protection check manifest: `.github/required-checks.txt`
- Release gate check manifest: `.github/release-required-checks.txt`
- Name integrity guard workflow: `.github/workflows/required-check-names-guard.yml`

## Ownership Matrix

| Surface | Owner | Backup | Notes |
| --- | --- | --- | --- |
| `.github/required-checks.txt` | Release Engineering | Platform On-Call | Controls required check names for branch governance |
| `.github/release-required-checks.txt` | Release Engineering | Platform On-Call | Controls release gate required checks |
| `.github/workflows/pr-test-build.yml` check names | CI Maintainers | Release Engineering | Check names must stay stable or manifests must be updated |
| `.github/workflows/release.yaml` release gate | Release Engineering | CI Maintainers | Must block releases when required checks are not green |
| `.github/workflows/required-check-names-guard.yml` | CI Maintainers | Release Engineering | Prevents silent drift between manifests and workflow check names |

## Change Procedure

1. Update workflow job name(s) and required-check manifest(s) in the same PR.
2. Ensure `required-check-names-guard` passes.
3. Confirm branch protection required checks in GitHub settings match manifest names.
4. For release gate changes, verify `.github/release-required-checks.txt` remains in sync with release expectations.

## Escalation

- If a required check disappears unexpectedly: page `CI Maintainers`.
- If release gate blocks valid release due to manifest drift: page `Release Engineering`.
- If branch protection and manifest diverge: escalate to `Platform On-Call`.

## Related

- [Release Governance and Checklist](./release-governance.md)
- [Checks-to-Owner Responder Map](./checks-owner-responder-map.md)

---
Last reviewed: `2026-02-21`  
Owner: `Release Engineering`  
Pattern: `YYYY-MM-DD`


---
