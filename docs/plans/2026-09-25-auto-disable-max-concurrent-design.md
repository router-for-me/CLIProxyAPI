# Upstream Entries — Max Concurrent & Auto-Disable — Design

Status: Draft · Scope: per-`api_key_entry` concurrency cap + rule-based permanent disable, both PG-first
Related: [2026-09-22-nixllm-upstream-releases-design.md](./2026-09-22-nixllm-upstream-releases-design.md),
[2026-09-24-claude-cooling-phase-2-plan.md](./2026-09-24-claude-cooling-phase-2-plan.md)

---

## 1. Overview & Goals

**Why now:**
- Multiple `api_key_entry`s per upstream provider already fan out for round-robin/least-loaded routing, but there is **no hard cap** on how many in-flight requests one entry may carry. A single entry's upstream account can be hammered while the sibling entries stay idle.
- Errors that permanently impair an entry (invalid/revoked key, account suspended, project deleted, billing error) are today only handled by **transient** cooldown (`conductor_cooldown.go`) or by a **manual** operator toggle (`Disabled` on the entry). There is no way to teach the runtime "when you see error code X on this provider, take that entry out of rotation permanently (or for a fixed time)".

**Locked decisions (from brainstorming):**
1. **Fitur 1 — config shape:** `max_concurrent` + `max_wait_ms` are **per-entry** fields (`UpstreamProviderAPIKey`). `nil`/`0` = unlimited (feature off for that entry). No global/provider fallback layer.
2. **Fitur 1 — behavior on cap hit:** the request **waits up to `max_wait_ms`** for a slot on that entry; when the wait times out it **failovers** to another entry of the same provider.
3. **Fitur 1 — all-entries-exhausted:** already handled by the layer above (routing strategy per model / per API key / auto router / model group). This feature only constrains *entry selection*; it never introduces a new request error of its own.
4. **Fitur 2 — config shape:** `auto_disable_error_codes` (list) + `auto_disable_cooldown_seconds` (optional) are **per-provider row** fields. No global default — if the list is empty/absent the feature is off for that provider.
5. **Fitur 2 — default re-enable:** manual (operator toggles the entry back on). **Optional auto re-enable** after `auto_disable_cooldown_seconds`, swept by a server-side background job.
6. **Fitur 2 — persistence:** runtime detects the error and writes `auto_disabled` **back to PG via a sink** (fire-and-forget, non-blocking), then **re-renders** config so the entry leaves selection. PG stays the single source of truth.
7. **Fitur 2 — distinct state:** an entry carries two independent dimensions — `disabled` (manual, operator) vs `auto_disabled` (system). Auto re-enable clears only `auto_disabled`; it never clears a manual `disabled`.
8. **Fitur 2 — UI:** badge + status + per-entry reason and a Re-enable action inside the existing provider editor and list. No dedicated page.

**What ships:**
- Two nullable PG columns on the entries table (`max_concurrent`, `max_wait_ms`).
- Two nullable PG columns / one list column on the providers table for auto-disable (`auto_disable_cooldown_seconds`, auto-disable codes as a child list or text array).
- Renderer (`internal/upstreamsync/render.go`) copies the new fields onto `config.ClaudeKey` / `config.OpenAICompatibilityAPIKey` / provider-level config types.
- Synthesizer stamps them onto auth attributes alongside the existing `AttributeEntryProviderKey`.
- Scheduler selection honors the per-entry in-flight cap with wait-then-failover.
- Conductor error classification fires an auto-disable sink; a server-side sink adapter persists `auto_disabled` and re-renders.
- A server-side background sweeper re-enables entries whose `auto_disable_cooldown_seconds` elapsed (only when auto-re-enable is configured).
- Dashboard editor + list show the new fields and the auto-disabled badges.
- Config snapshot round-trip + config example updates.

**Non-goals:**
- No new request-level error/status code for "all entries busy" (handled above).
- No per-key / per-user control-plane changes — the client-side `max_parallel_requests` (`internal/policy/parallel.go`) is unchanged.
- No change to transient cooldown semantics in `conductor_cooldown.go`.
- No separate UI page for auto-disabled entries.
- No global-default layer for auto-disable codes.

**Deferred (out of scope):**
- Auto re-enable with exponential backoff / jitter (sweeper v1 uses a single fixed duration).
- Auto-disable triggered by *accumulated* error counts (v1 is per-error-code match).
- Cross-instance consensus for the sweeper beyond PG row updates (single-writer assumptions documented in § 4).

---

## 2. Architecture & Data Flow

### 2.1 Layering

```
PG (single source of truth)
  upstream_providers (row: auto_disable_* cols)          ← configsnapshot / configvalidation
  upstream_providers_entries (row: max_concurrent, max_wait_ms,
                               auto_disabled, auto_disabled_at, auto_disabled_reason)
        │  upstreamsync render.go
        ▼
config.yaml (ClaudeKey / OpenAICompatibility / OpenAICompatibilityAPIKey carry the fields, JSON-hide the auto_disabled runtime flags)
        │  watcher/synthesizer/config.go
        ▼
core auth runtime (sdk/cliproxy/auth)
  - Auth attributes: per-entry concurrency + provider auto-disable codes
  - scheduler selection: in-flight cap + wait/timeout → failover (Fitur 1)
  - conductor classification → AutoDisableSink (Fitur 2)
        │  sink (server side, adapter)
        ▼
PG write back: auto_disabled=true + reason + timestamp → re-render via existing watcher
        ▼
server background sweeper: auto re-enable after cooldown
```

### 2.2 Data flow — Fitur 1 (per-request, hot path)

1. Request arrives → routing layer resolves provider + model (existing).
2. Entry selection runs the existing scheduler pick (round-robin / fill-first / weighted / least-used).
3. For each candidate entry with `MaxConcurrent > 0`: if `scheduler.inFlight[authID] >= MaxConcurrent`, the entry is **not eligible** (same treatment as cooling entries today).
4. If every candidate is busy, the selection loop **waits up to `max_wait_ms`** (entry's configured value, else the wait default) for a slot to free, re-testing eligibility. On timeout it **failovers** to the next candidate entry (normal rotation).
5. Selected entry increments `inFlight`; the existing lifecycle releases it on completion/release (`adjustInFlight`).
6. All-entries-exhausted at the provider level bubbles up to the higher routing layer unchanged — no new error.

### 2.3 Data flow — Fitur 2 (on error, cold path)

1. Executor classifies an upstream error (existing `conductor_cooldown.go` classification path) → `*auth.Error{Code, HTTPStatus, Message}`.
2. Conductor checks the auth's stamped auto-disable codes (from provider config) against `err.Code` (and numeric `HTTPStatus` when the code is a number, e.g. `"401"`).
3. On match → fire the `AutoDisableSink` (server side) with `(provider, entryID, code, message)`. Fire-and-forget, panic-recovered (same contract as `RefreshSink`).
4. Sink adapter (server side): within one PG tx, set `auto_disabled=true`, `auto_disabled_at=now`, `auto_disabled_reason=code[:..]`, only for entries that are not already manually `disabled`; then trigger a re-render (existing watcher) so the entry drops out of selection.
5. Runtime keeps the stale auth hot until the re-render lands; the sink does not mutate core auth state.
6. Re-enable: manual toggle (clears `auto_disabled`, never `disabled`) OR the server background sweeper flips `auto_disabled` off for entries whose `auto_disabled_cooldown_seconds` elapsed → re-render.

### 2.4 Config snapshot & validation

- New fields must round-trip through `configsnapshot` (`NormalizedResourcePlan`, `MarshalYAML`/`UnmarshalYAML`, `Checksum`) so a config change alters the plan identity.
- `internal/configvalidation` (`Validate(snapshot)`) picks up the new fields via the existing `config.ParseConfigBytes` round-trip with zero new code if the yaml tags are correct.

---

## 3. Key Components

### 3.1 Store (PG)

- `internal/store/pg_upstream_providers.go` — `UpstreamProviderAPIKey` gains:
  - `MaxConcurrent *int  `json:"max_concurrent,omitempty"``
  - `MaxWaitMs *int      `json:"max_wait_ms,omitempty"``
  - `AutoDisabled bool` `json:"auto_disabled,omitempty"` (runtime-written, not operator input)
  - `AutoDisabledAt *time.Time`, `AutoDisabledReason string`
- `UpstreamProvider` gains:
  - `AutoDisableCooldownSeconds *int` `json:"auto_disable_cooldown_seconds,omitempty"` (nil = manual re-enable)
  - `AutoDisableErrorCodes []string` `json:"auto_disable_error_codes,omitempty"` (renderer → config types; storage likely as a text array column on the provider row, or a child list if the project convention favors child rows)
- Migrations: 2 nullable int col on entries; `auto_disabled` bool + timestamps + reason on entries; auto-disable code list + cooldown on providers. Backfill = nil defaults (feature off).

### 3.2 Renderer (`internal/upstreamsync/render.go`)

- `config.ClaudeKey` gains `MaxConcurrent *int` + `MaxWaitMs *int` and provider-level auto-disable fields; `config.OpenAICompatibilityAPIKey` likewise (though OpenAI entries are rarely capped, same shape for parity).
- `buildClaudeKeyWithPools` / the OpenAI compat path copy the new store fields verbatim.
- Disabled entries (manual OR auto) must keep being skipped at render time — today `e.Disabled` gates the skip; auto-disabled entries are rendered as skipped only because the sink already flipped them in PG, i.e. **auto-disable persists as a PG-level `disabled=true` (+ auto flag) so rendering needs no change** — this is the chosen design (see § 4 decision).

### 3.3 Synthesizer (`internal/watcher/synthesizer/config.go`)

- Stamp `max_concurrent` / `max_wait_ms` onto the auth (attribute or struct field) alongside `AttributeEntryProviderKey`.
- Stamp provider auto-disable codes + cooldown onto each auth of that provider.

### 3.4 Scheduler (`sdk/cliproxy/auth/`)

- Eligibility check in the pick paths: `MaxConcurrent > 0 && inFlight(authID) >= MaxConcurrent → skip`.
- Wait with timeout: bounded by the entry `MaxWaitMs`; re-test loop; then failover.
- No changes to `internal/policy/parallel.go` (client-side cap).

### 3.5 Conductor (`sdk/cliproxy/auth/conductor_cooldown.go`)

- In the error-classification path, after the existing cooldown handling with `shouldSkipCredentialCooldown` unchanged, check the auto-disable code list; on match fire the sink.
- Add `SetAutoDisableSink(sink AutoDisableSink)` mirroring `SetRefreshSink` (atomic pointer, fire-and-forget, recovered).

### 3.6 Server side

- `internal/api/handlers/management/` — sink adapter (`auto_disable_sink.go`): writes PG, triggers re-render. Mirrors `alerts_runner.go` background-sweep style for auto re-enable (`auto_disable_sweeper.go`).
- Management routes: existing provider GET/PUT already round-trip the new fields via configsnapshot; a re-enable may be expressed as a normal PUT clearing `auto_disabled` (+ `disabled`), so **no new endpoint is strictly required**, but a thin `POST /v0/management/upstream-providers/:id/entries/:entry/reenable` convenience route is worth adding for operator ergonomics (dashboard button).

### 3.7 Dashboard (`web/dashboard/`)

- Entries tab: `max_concurrent` + `max_wait_ms` number inputs (0 = unlimited).
- Overview/Entries: per-entry auto-disabled badge (reason + when), Re-enable button.
- Provider form: `auto_disable_error_codes` (chip editor) + `auto_disable_cooldown_seconds`.
- List page: status chip for auto-disabled entries count.

---

## 4. Key Design Decisions

| # | Decision | Rationale |
|---|----------|-----------|
| D1 | Auto-disable persists as PG-level `disabled=true` + an `auto_disabled` flag, **not** a separate render-time filter. | Renderer, synthesizer, selection, and the existing "toggle back on re-renders" behavior all keep working with **zero change**; only the write + sweep paths are new. The `auto_disabled` flag separates "system set it" from "operator set it" for UI + re-enable semantics. |
| D2 | Sink (server-side adapter) receives `(provider, entryID, code, message)`; core auth never imports store/PG. | Keeps `sdk/cliproxy/auth` clean, matching the `RefreshSink` precedent and the AGENTS.md layering rule. |
| D3 | Wait-then-failover is implemented in the scheduler selection loop against the existing `inFlight` counter — no separate semaphore map per entry. | One counter already feeds least-loaded + eligibility; avoids a second bookkeeping structure and a second release path to leak. |
| D4 | Re-enable sweeper lives **server-side** (management), one writer on PG, survives restart; no in-memory timers in core. | Consistent with `alerts_runner.go`; single-writer avoids split-brain across instances. Restart-safe because both `disabled` and `auto_disabled_at` are in PG. |
| D5 | Auto re-enable only clears `auto_disabled`; a manually `disabled` entry is never touched by the sweeper. | `disabled` (operator) wins over `auto_disabled` (system) — manual intent must not be overridden. |
| D6 | No new request error for all-entries-busy. | Higher routing layers (auto router / model group / per-model strategy) already handle provider exhaustion. Keeps this feature a pure selection constraint. |
| D7 | Config snapshot + validation must round-trip the new fields. | PG-first control plane identity (`Checksum`) must change when config changes; dashboard saves and CLI imports share one validation pipeline. |

---

## 5. Error Handling

- **Wait timeout in scheduler:** logged at debug-level with entry identity (no API key), request proceeds to failover — never a server error from this feature.
- **Sink write failure (PG down):** logged (code + entry ID, no key), runtime unchanged; entry stays hot until the next re-render. A later successful sweep/hot-reload picks it up. Cooldown (transient) still protects the entry meanwhile.
- **Sink panic:** recovered (same contract as `RefreshSink`); never blocks classification.
- **Re-render racing a concurrent operator edit:** the sink writes are per-entry `auto_*` columns inside an upsert that preserves other fields (existing `replaceChildrenTx` shape) — an operator's concurrent save wins on other columns; the auto flag survives unless the entry itself was deleted.
- **Sweeper vs manual disabled:** D5 — the sweeper skips entries where `disabled=true` was operator-set (distinguishable via `auto_disabled=false`).
- **API key redaction:** sink payloads and logs carry provider + entryID + code only, never the key material.

---

## 6. Testing

### Fitur 1 — unit (`internal/policy` decision is client-side; scheduler tests live in `sdk/cliproxy/auth`)
- Entry with `MaxConcurrent=1`: first selection ok; second selection with first still in-flight is not eligible; releasing frees the slot.
- Wait behavior: with `MaxWaitMs` set, a busy entry is retried and then failover happens when the deadline expires (fake clock / short duration).
- `MaxConcurrent` unset/0 → unlimited (candidate always eligible).
- `MaxWaitMs` unset → default deadline used.
- Release on all exit paths (success, error, stream close) — no in-flight leak (mirror `scheduler_inflight_test.go`).

### Fitur 2 — unit (`sdk/cliproxy/auth`)
- Error code match → sink fired with correct `(provider, entryID, code)`; non-match → not fired.
- Numeric HTTPStatus code (e.g. `401`) matches when config lists `"401"`.
- `shouldSkipCredentialCooldown` semantics unchanged (skipped errors never trigger the sink).
- Manual `disabled` entries are never sink-targeted past the write adapter test.
- Firing the sink is non-blocking + panic-recovered.

### Fitur 2 — integration (management side)
- Sink adapter writes `auto_disabled=true` + reason + timestamp; re-render drops the entry from selection (existing watcher round-trip).
- Sweeper re-enables after `auto_disable_cooldown_seconds`; skips operator-manual-disabled entries; survives restart (PG-backed).
- Config snapshot round-trip + validation keep the new fields and bump `Checksum`.

### Dashboard
- Entries tab renders the new inputs; auto-disabled badge + Re-enable button work against the API.
- Provider form round-trips the code list + cooldown.

---

## 7. Migration & Compatibility

- PG migrations are additive (new nullable columns / new child rows), backfilled to nil → feature off; existing rows unaffected.
- `config.example.yaml` gains commented examples for the new per-entry and per-provider fields.
- Legacy YAML (no `UpstreamProviderEntryID`) — auto-disable is keyed on `entryID`, so legacy inline entries fall back to the existing behavior (flag can't attribute to a PG row → sink is a no-op with a logged warning, feature simply doesn't apply).
- No change to the client-side `max_parallel_requests` semantics or to transient cooling.

---

## 8. Future Work

- Accumulated-error-count triggers.
- Exponential backoff / jitter on auto re-enable.
- Cross-instance lock for the sweeper if multi-writer PG access is introduced later.
- Exposing auto-disable history as a dashboard timeline.
