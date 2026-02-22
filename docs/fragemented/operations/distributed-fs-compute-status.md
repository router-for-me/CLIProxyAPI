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
