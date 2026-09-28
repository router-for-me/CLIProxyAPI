# Auto Router — Jev AI Hybrid Gate Design

Date: 2026-09-28
Status: Validated (brainstormed section-by-section; scope agreed: hybrid gate, global Settings card, comparison harness)

## Context

The Auto Router (`internal/autorouter`, wired in `sdk/api/handlers/handlers_auto_router.go`) classifies each
incoming request into one of four complexity tiers (SIMPLE / MEDIUM / COMPLEX / REASONING) and maps the tier
to a concrete upstream target. Today:

- **Classification is purely heuristic** — 7 dimensions (token count, code density, reasoning markers,
  technical terms, simple-indicator penalty, multi-step, question complexity) with fixed weights, fixed
  thresholds, plus literal keyword-tier override rules. Per-router scoring profiles live in PG, versioned
  and hashed (`internal/store/pg_auto_router_profiles.go`).
- **The heuristic is lexical, not semantic** — it counts words and symbols. It cannot tell that
  "explain why this deadlocks" needs more reasoning than "explain why the sky is blue", because both contain
  the same marker words. It carries no calibrated notion of its own uncertainty: a score of 0.58 and 0.59
  are treated as equally trustworthy on either side of a threshold.
- **Runtime cost is effectively zero** — 3.27µs/op for a small body, no I/O, no network. This is the property
  any replacement must not casually destroy.
- **Observability exists** — every routed request persists a `DecisionSnapshot` into
  `usage_events.auto_router_decision` (jsonb) with tier, cause, score fields, matched rules, fallback chain.
  Four dashboard analysis tabs read it (Overview, Decision distribution, Simulation, Replay).

The goal of this design is to add a **semantic, calibrated classifier** in front of the existing heuristic —
without giving up the existing heuristic's zero-latency, zero-cost, zero-dependency guarantees.

## What Jev AI is (System One)

TypeSafe's Jev (`docs.typesafe.ai`) is a *System One* model: a classifier, not a generative LLM. It accepts a
`state` (text / JSON object / array of text) and a set of typed `questions`, and returns typed `answers` with
calibrated probability distributions. Three primitives exist:

- **Choice** — pick one of up to 255 options. Returns `choice`, `probabilities`, `confidence`.
- **Score** — rate on an ordered 2–10 level rubric. Returns `score`, `legend`, `probabilities`, `confidence`.
- **Noul** — a single yes/no probability. Returns `noul` only (no confidence).

Key properties this design relies on:

- **Confidence is derived from the shape of the distribution**, not the top probability: `(n × peak − 1) / (n − 1)`
  clamped to `[0,1]`. A concentrated distribution gives 1.0; an even spread gives 0.0. This is why "what the
  answer is" and "whether to trust it" are two separate signals.
- **Questions in one call are evaluated in parallel and independently** — adding questions has little effect
  on latency.
- **Typical query latency is ~100ms**; pricing is $0.042/Mtok input with output tokens free. Context limit is
  64k tokens/request (32k for state plus the longest question).
- **It does not generate text, write code, or hold a conversation.** It is explicitly not a drop-in LLM
  replacement (`docs.typesafe.ai/introduction/coding-agents`).

The two documented routing patterns this design borrows from:

- **Intent routing** — classify into a fixed set, dispatch to the cheapest capable handler; the documented
  example gates `intent.confidence < 0.5` to a human.
- **Confidence-gated routing** — "the answer tells you what; confidence tells you whether to act", with
  thresholds that scale with the cost of being wrong.

## Goals

1. Add semantic tier classification that beats the lexical heuristic on borderline requests.
2. Keep the existing heuristic as the guaranteed path: no request may fail, slow down, or change tier because
   Jev is unreachable, misconfigured, or unsure.
3. Make the feature opt-in and globally killable from the dashboard.
4. Produce an honest, measured comparison of heuristic vs Jev tiering for the agreed evaluation corpus.

## Non-goals (YAGNI)

- **No Score/Noul primitives.** v1 sends exactly one Choice question. Composite scoring (multiple atomic Score
  questions combined with code-side weights) is a documented Jev pattern and the obvious v2 direction, but it
  multiplies moving parts and needs real confidence data before its weights can be chosen.
- **No per-tier confidence thresholds.** The risk-tiered pattern (a higher floor for REASONING than for
  SIMPLE) is deliberately deferred until the corpus evaluation shows whether confidence is a useful axis here.
- **No async shadow mode.** Considered and rejected: a cache key loose enough to make async useful ("same last
  user turn") is loose enough that a shadow verdict would routinely describe a request it never saw.
- **No per-router API key.** One global key in Settings, mirroring how LiteLLM sync stores its master key.
- **No new PG tables for router config.** Per-router Jev knobs ride in the existing `auto_routers` row.
- **No translator changes.**

## Architecture

```
request model_id == router.model_id
        │
        ▼
┌──────────────────────────┐
│ 1. Heuristic scorer      │  existing, always runs (~3-5µs)
│    → scoredTier, total   │  ScoreWithProfileCompiled
└─────────┬────────────────┘
          ▼
┌──────────────────────────┐
│ 2. Jev gate              │
│    effective-enabled?    │  global.enabled AND router.jev_enabled AND api_key_set
│    no ────────────────► heuristic (byte-identical to today)
│    yes ▼                 │
│    cache lookup          │
│    hit ──────────────► stored verdict
│    miss ▼                │
│    build state           │  latest user turn (≤6000 chars) + metadata
│    POST /v1/systemone    │  Choice of 4 tiers, timeout 400ms, fail-open
│    confidence ≥ τ ?      │
│    yes → tier = Jev      │  decision_cause = jev_classifier
│    no  → tier = heuristic│  decision_cause = jev_low_confidence
└─────────┬────────────────┘
          ▼
┌──────────────────────────┐
│ 3. Resolve(tier, config) │  existing, untouched: tier → model + fallback chain
└─────────┬────────────────┘
          ▼
┌──────────────────────────┐
│ 4. request adjustments   │  existing vision bridge / thinking inflation, untouched
└──────────────────────────┘
```

Three invariants hold the design together:

- **Fail-open.** Every Jev failure mode (timeout, 5xx, auth, parse, missing key) resolves to the heuristic.
  No request is ever rejected, delayed past its timeout, or errored because of Jev.
- **Fail-identical.** `jev_enabled: false` (the default, both globally and per router) means the request path
  is byte-identical to today: no state build, no cache lookup, no clock read. The existing golden tests
  (`TestScorerParityOnCorpus`, `decision_golden_test.go`) must pass unchanged.
- **Jev replaces the classifier, not the resolver.** The four Choice options are exactly the four existing
  tiers, so `TierMapping` configuration is untouched and the existing lower-tier fallback in `Resolve()`
  handles a Jev tier that has no mapping.

### New modules

| Module | Contents |
|---|---|
| `internal/autorouter/jevclient/` | HTTP client for `POST /v1/systemone`: request/response types, auth header, parse of `choice`/`probabilities`/`confidence`/`usage`, no retries |
| `internal/autorouter/jevgate/` | State builder, cache key, gate orchestration (heuristic → cache → call → confidence decision), circuit breaker |
| `internal/store/pg_jev.go` | `jev_settings` singleton store, mirroring `litellm_sync.go` |
| `internal/api/handlers/management/jev.go` | GET/PUT `/v0/management/jev/settings` |

## Components

### State sent to Jev

```json
{
  "latest_user_message": "<last user turn, truncated to 6000 chars>",
  "metadata": {
    "message_count": 12,
    "history_word_estimate": 3400,
    "has_tools": true,
    "has_code_fence": true,
    "has_images": false,
    "model_requested": "router:smart-router"
  }
}
```

- `latest_user_message` reuses `extractedRequest.LatestUserText`, which the v2 scorer already extracts
  per-format (`scorer.go:267` for OpenAI, `:343` for Gemini, and the Claude path). For a non-chat request the
  field is empty and Jev judges from metadata alone.
- Truncation at 6000 characters is ~1.5k tokens → roughly $0.000063 per uncached request. 10k unique prompts
  cost about $0.63. The Jev context limit (32k for state plus the longest question) is never approached.
- **No system prompt and no message history.** This is a deliberate three-way trade: it keeps token cost and
  privacy exposure to one turn, and it matches the role-aware split F1 already adopted (history is capped at
  20% of the token signal) — the last turn carries the strongest complexity signal.

### The question

Exactly one Choice question, one call:

```json
{
  "tier": {
    "type": "choice",
    "instructions": "Classify the LLM request by the cheapest model tier that can answer it well. Consider the latest user message primarily; metadata is secondary evidence.",
    "criteria": {
      "simple": "Greetings, chitchat, one-line factual lookups, trivial rewrites of short text.",
      "medium": "Summaries, translations, short Q&A, simple code edits on small snippets, single-step tasks.",
      "complex": "Multi-file or multi-step coding, debugging with context, longer document analysis, agent tool-use turns.",
      "reasoning": "Requires deep step-by-step reasoning: proofs, algorithm analysis, architecture trade-offs, math, planning."
    }
  }
}
```

The criteria deliberately restate the existing 4-tier rubric rather than inventing a parallel taxonomy. This
is what allows the gate to be dropped in without touching any tier→model mapping.

### Cache

- Key: `SHA-256(format | routerID | latestUserText | metadata-flags | jevModel | questionHash)`.
  - Deliberately **not** the full raw body, unlike the existing `scoreCache` (`score_cache.go:45`). Keying on
    the last user turn means the same conversational turn hits regardless of accumulated history — which is
    also why the async shadow mode this design rejects would not have worked.
  - `questionHash` covers instructions + criteria, so editing the rubric invalidates the cache automatically.
  - `metadata-flags` participates so a turn that gains tools or images is not served a stale tier.
- Storage: an entry type added to the existing per-router FIFO ring (`scoreCache`, 2048 entries). No new data
  structure. Bodies over 256 KiB are skipped exactly as today (`handlers_auto_router.go:87`).
- **Low-confidence verdicts are cached too.** The call was paid for; caching the verdict (including its
  confidence) avoids re-paying for a prompt that will keep falling back.

### Confidence gate

- `jev_min_confidence` per router, default **0.5** — the floor the Jev docs recommend for their routing
  examples. With four options an even spread gives confidence 0, so 0.5 means "at least a mild preference".
- Below the threshold the heuristic tier is used and the verdict is recorded as
  `jev_verdict: "rejected_low_confidence"` with its confidence value, so operators can see *how often* and
  *how narrowly* Jev is being overruled before tuning the threshold.
- The confidence value, the full probability distribution, and the Jev token usage are all persisted — tuning
  needs the distribution, not just the winner.

### Configuration

Two levels, mirroring the LiteLLM sync precedent (global secret + global toggle) plus the existing
per-router profile pattern.

**Global** — singleton PG table `jev_settings`, `id = 1` with a `CHECK (id = 1)` singleton constraint, seeded
on startup (identical structure to `litellm_sync_settings`, `postgresstore.go:771-810`):

| Column | Notes |
|---|---|
| `enabled` | master toggle, `NOT NULL DEFAULT FALSE` |
| `api_key_sealed` | AES-GCM ciphertext via the shared `Sealer`; plaintext when `PGSTORE_ENCRYPTION_KEY` is unset |
| `api_key_prefix` | display-only mask, e.g. `sk-ts-…ab12` |
| `model` | default `jev-1.13.0` |
| `updated_at` | |

**Per router** — new optional fields on the existing `auto_routers` row (no new table):

| Field | Default | Notes |
|---|---|---|
| `jev_enabled` | `false` | per-router opt-in |
| `jev_min_confidence` | `0.5` | confidence floor |
| `jev_timeout_ms` | `400` | call budget |
| `jev_model_override` | `""` | blank = use the global model |

**Effective gate at request time:** `global.enabled AND router.jev_enabled AND api_key_set`. The master toggle
is the kill switch: an operator can disable all Jev classification from one place without editing any router.

### Settings API and dashboard

- `GET /v0/management/jev/settings` → `{ settings: { enabled, api_key_set, api_key_prefix, model, updated_at } }`.
  **The plaintext key is never returned over HTTP** — same posture as `litellm_sync.go:132-133`.
- `PUT /v0/management/jev/settings` with pointer-typed partial merge:
  `api_key: nil` = keep, `""` = clear, non-empty = rotate and seal. `enabled` and `model` apply as given.
  Returns 503 without `PGSTORE_DSN`, like the other PG-backed management routes.
- Dashboard: a `JevSettingsCard` in `SettingsPage.jsx` between `AlertSettingsCard` and `DynamicSettingsCard`,
  built from the existing primitives — `ToggleRow` and `PasswordInput` from
  `pages/manage-cpa/FormPrimitives.jsx`, with `apiKey`/`apiKeyDirty` state and a "Clear key" button, mirroring
  `LiteLLMPage.jsx:1714-1749`. Content: master toggle, key field (blank = keep), stored prefix display,
  model field with a note that changing it invalidates tuned thresholds, and an "API key: set / not set"
  indicator.
- New client helpers `getJevSettings`/`putJevSettings` in `api/client.js` following `getLiteLLMSyncSettings`.

**Do not** put the key in `runtime_config` / `DynamicSettingsCard`: `GET /runtime-config` returns settings
verbatim, so a key placed there would be shipped plaintext to the browser.

### Secret resolution

`.env` / environment `JEV_API_KEY` is used **only as a fallback** when the PG row has never been populated
from the dashboard. Once an operator saves a key in Settings, the PG value wins. This is PG-first, consistent
with the control-plane direction of the repo, while keeping a zero-dashboard bootstrap path for scripted
deployments.

## Data flow

```
1. handlers_execution.go (non-stream) / handlers_stream.go (stream) / count endpoint   [existing hook]
2. resolveAutoRouterModel(ctx, format, modelID, rawJSON)
   a. router = store.GetByModelID(modelID); nil → pass-through                        [existing]
   b. ext = extractRequest(rawJSON, format)                                           [existing v2]
   c. heur = ScoreWithProfileCompiled(rawJSON, format, prof)                          [existing, always]
   d. if effective gate false → return heur                       (byte-identical today)
   e. state = jevgate.BuildState(ext, router)
      key   = jevgate.CacheKey(format, routerID, state, jevModel, questionHash)
   f. cache hit → applyJev(verdict, heur)
   g. ctx2 + cancel = WithTimeout(ctx, router.JevTimeoutMS)
      resp, err = jevclient.Call(ctx2, state, question); cancel()
   h. err != nil → record jev{called:true, verdict:"error", error_kind}; return heur   (fail-open)
   i. cache.Set(key, resp)                                        (including low-confidence)
   j. applyJev(resp, heur):
        resp.Confidence >= router.JevMinConfidence
          yes → tier = resp.Choice; cause = jev_classifier
          no  → tier = heur.Tier;   cause = jev_low_confidence
3. Resolve(tier, config) → model + providers/strategy/priorities                       [existing, untouched]
4. autoRouterRequestAdjustments (vision bridge, thinking inflation)                    [existing, untouched]
5. Decision context → usage_events.auto_router_decision jsonb (+ jev block)            [existing + new field]
```

### Error handling matrix

| Condition | Behavior | `decision_cause` | Log |
|---|---|---|---|
| Gate off | heuristic | `complexity_scorer` / `literal_keyword_match` | — |
| Cache hit | stored verdict | `jev_classifier` / `jev_low_confidence` | — |
| Timeout / ctx done | heuristic | `jev_fallback_heuristic` | warn (latency, router) |
| HTTP 401/403 | heuristic + circuit breaker opens 5 min | `jev_fallback_heuristic` | warn (no key material) |
| HTTP 429/529/5xx/network | heuristic, **zero retries** | `jev_fallback_heuristic` | warn |
| HTTP 422 | heuristic | `jev_fallback_heuristic` | error (indicates a state-builder bug) |
| Response parse failure | heuristic | `jev_fallback_heuristic` | warn |
| `confidence < τ` | heuristic tier, verdict cached | `jev_low_confidence` | debug |
| Jev tier has no mapping | existing `Resolve()` lower-tier fallback | `jev_classifier` | — |

### Circuit breaker

A per-router atomic flag: the first 401/403 logs, sets a 5-minute cooldown, and every request during the
cooldown goes straight to the heuristic without a call. This stops 1200 req/min from hammering an endpoint
configured with a bad key. In-memory only; a restart resets it. This is deliberately *not*
`authManager.CooldownStateSnapshot()` — that mechanism tracks upstream provider cooldowns, and Jev is a new
pre-upstream dependency whose simple failure mode does not warrant reusing it (KISS).

### Repo-rule compliance

The 400ms timeout is legal under the repo's timeout rule because it applies **before** any upstream model
connection exists — the same phase as the existing vision bridge
(`sdk/api/handlers/vision_bridge.go`, bounded by `vision-bridge-timeout-seconds`) and the management API quota
fetches. No timeout is added after an upstream connection is established. No `log.Fatal`; all failures return
an error and log via logrus without leaking key material. Test-time clock and random injection follow the
existing `resolveWithRand` pattern.

### Observability payload

Added to the existing `auto_router_decision` jsonb — no schema change needed:

```json
{
  "jev": {
    "called": true,
    "model": "jev-1.13.0",
    "choice": "complex",
    "confidence": 0.72,
    "probabilities": {"simple": 0.05, "medium": 0.18, "complex": 0.60, "reasoning": 0.17},
    "latency_ms": 96,
    "input_tokens": 1480,
    "cache": "miss",
    "verdict": "accepted"
  }
}
```

New `decision_cause` values `jev_classifier`, `jev_low_confidence`, and `jev_fallback_heuristic` flow into the
existing four analysis tabs without dashboard changes to the aggregation queries.

## Testing

| File | Coverage |
|---|---|
| `jevgate/state_test.go` | state build per format (OpenAI/Claude/Gemini), 6000-char truncation, metadata flags, non-chat requests valid |
| `jevgate/gate_test.go` | confidence ≥ τ → Jev tier; below τ → heuristic; cache hit makes no call; different `questionHash` → miss |
| `jevgate/error_test.go` | the full matrix above, including a timeout (server sleeps 1s), 401 → circuit breaker (second request never reaches the server), 429/500/parse-error all fail open |
| `jevclient/client_test.go` | request shape (state/questions/model), auth header, parse of probabilities + confidence + usage |
| `handlers_auto_router_test.go` (extend) | `jev_enabled=false` is byte-identical to today (golden); `jev_enabled=true` + fake server → Jev tier wins |
| existing golden tests | `TestScorerParityOnCorpus` and `decision_golden_test.go` must pass unchanged |

Verification gate: `gofmt -w .`, `go build ./...`, `go test ./...` green; `BenchmarkScoreSmallBody` must not
regress when the gate is off (the default path adds no work); dashboard SPA builds and `make dash-embed` runs.

## Performance comparison

A fair comparison needs ground truth, so the comparison runs in two layers.

### Layer 1 — labelled corpus evaluation (offline, `cmd/autorouter_eval`)

1. Collect ~150–300 representative prompts spanning trivial chat, summarization, single-file coding,
   multi-file debugging, proofs/math, and agent tool-use turns. Synthesized plus redacted samples from
   `usage_events`.
2. Label each with the ground-truth tier using the same 4-tier rubric, produced by a strong reasoning model
   and manually spot-verified (a sample of labels re-checked by hand; disagreements resolved by hand).
3. Run both classifiers over the corpus: the existing heuristic in-process, and Jev with one Choice call per
   prompt.
4. Report a 4×4 confusion matrix and macro-F1 per classifier, plus a heuristic-vs-Jev disagreement matrix
   showing where they diverge. The existing simulate/replay endpoints cross-check the heuristic results.

### Layer 2 — operational metrics

| Dimension | Existing (heuristic) | Hybrid gate | Source |
|---|---|---|---|
| Classifier latency | ~3–5µs (`BenchmarkScoreSmallBody`) | +~100ms on miss, ~1µs on hit | existing bench; Jev docs claim ~100ms; **re-measured at implementation** against a fake server, reporting client overhead separately from the claim |
| Cost per request | $0 | ~$0.000063 (~1.5k tokens × $0.042/Mtok) | models.md |
| Cost per 10k unique prompts | $0 | ~$0.63 | computed |
| Network dependency | none | 1 call per unique last turn | design |
| Tiering accuracy | baseline (Layer 1) | measured (Layer 1) | confusion matrix |
| Expressiveness | lexical; cannot distinguish intent | reads the request's meaning | qualitative + Layer 1 |
| Privacy | body never leaves the process | last user turn leaves to `api.typesafe.ai` (ZDR available on enterprise) | docs |

**Stated honestly in the results:** the ~100ms on the request path is only worth paying if (a) the cache hit
rate on real traffic is high — repetitive traffic is the norm for chat and agent loops — and/or (b) the tiering
accuracy gain translates into upstream cost savings, since a request routed one tier too low can cost hundreds
of times more to redo than the Jev call costs. Layer 1 supplies the accuracy number; the hit rate is measured
after deployment via the `jev.cache` field, and neither number is claimed in advance.

## Rollout

1. Ship with `jev_settings.enabled = FALSE` and `jev_enabled = false` on every router. Behavior is unchanged.
2. Implement the store, API, client, and gate; all tests green.
3. Run the Layer 1 corpus evaluation and record the numbers.
4. Enable globally + per router for one canary router; watch the analysis tabs for `jev.*` causes, the cache
   hit rate, and per-tier latency/cost in the Overview tab.
5. Tune `jev_min_confidence` from the recorded confidence distribution before widening.

## Files touched

| Path | Change |
|---|---|
| `internal/autorouter/jevclient/*.go` | new — HTTP client, types, parsing |
| `internal/autorouter/jevgate/*.go` | new — state builder, cache key, gate, circuit breaker |
| `internal/store/pg_jev.go` | new — `jev_settings` singleton store, `Sealer` integration |
| `internal/store/postgresstore.go` | DDL for `jev_settings` alongside `litellm_sync_settings` |
| `internal/api/handlers/management/jev.go` | new — GET/PUT settings handler |
| `internal/api/server_management.go` | register `/jev/settings` routes |
| `internal/api/handlers/management/handler.go` | `SetJevStore` wiring |
| `internal/autorouter/types.go` | Jev knobs on `Config`; `jev` block on `DecisionSnapshot` |
| `internal/store/pg_auto_routers.go` | persist the new per-router columns |
| `sdk/api/handlers/handlers_auto_router.go` | insert the gate after the heuristic scorer |
| `sdk/api/handlers/handlers_execution.go`, `handlers_stream.go` | pass the gate result through (minimal) |
| `web/dashboard/src/pages/SettingsPage.jsx` | `JevSettingsCard` |
| `web/dashboard/src/api/client.js` | `getJevSettings` / `putJevSettings` |
| `web/dashboard/src/components/AutoRouterForm.jsx` | per-router Jev fields |
| `cmd/autorouter_eval/` | new — corpus evaluation tool |
