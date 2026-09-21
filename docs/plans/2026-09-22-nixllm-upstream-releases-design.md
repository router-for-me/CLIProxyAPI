# Design: NixLLM Enhancements Inspired by CLIProxyAPI Releases

**Date:** 2026-09-22
**Status:** Validated design
**Reference:** [CLIProxyAPI releases](https://github.com/router-for-me/CLIProxyAPI/releases) (v7.2.129 – v7.3.8)

## Goal

Absorb the operational value of the CLIProxyAPI releases published after NixLLM's fork point without taking upstream code. NixLLM diverged from the CLIProxyAPI core at `v7.2.128` (`bd34ceca`) and has since accumulated 598 commits of its own — the PostgreSQL-first control plane, the dashboard SPA, the policy engine, proxy pools, and the upstream-provider model. Upstream is now at `v7.3.8`, 598 commits ahead, with 107 files touched by both sides (a dry-run merge conflicts on 217 files, 25 of them translators).

This design therefore does not merge, rebase, or cherry-pick. It re-implements the *ideas* of those releases natively against NixLLM's architecture, one independently shippable phase at a time. Where upstream added a large new provider (Devin), it is rebuilt from the public protocol, not from upstream's reverse-engineered implementation.

## Decisions

1. **Native re-implementation, not a sync.** No upstream code enters the tree. Every phase reads the release notes as a requirement statement and lands as NixLLM-style code.
2. **Five phases, ordered by value per unit of risk.** Reliability/observability, Claude model-level cooling, translator correctness, Meta provider, then Devin. Each phase is one branch, one PR to the fork's `main`, and independently shippable.
3. **Versioning follows the fork convention.** Tags are `v<core>-0.<patch>`; NixLLM-only work bumps only the `0.x.x` half (`v7.2.138-0.1.26` onward). The core half stays at the fork point until a real core sync happens.
4. **Translator changes are never standalone.** Upstream `router-for-me/CLIProxyAPI` reports `READ` permission for this account, so the AGENTS.md rule binds: a change touching `internal/translator/` must ship alongside non-translator changes in the same changeset. Phase 3 satisfies this naturally — its usage-token and finish-reason work lives partly in the executors.
5. **Reliability work reuses existing machinery.** Per-`(auth, model)` cooldown, the cooldown snapshot, model-aware alerting, the policy usage windows, and the bounded-LRU caches already exist. Phases 1 and 2 extend them rather than introducing parallel systems.
6. **Blind providers ship behind flags, and say so.** Meta and Devin are built without a live account. Their network layers go behind small interfaces so unit tests can exercise the logic with fakes, and both are disabled by default until validated against a real account.
7. **Devin's wire format is the only knowingly unverifiable part.** It is isolated into a four-layer stack so that a later calibration session touches one file, not the provider.
8. **Out of scope by intent:** mDNS/DNS-SD LAN discovery, the plugin scheduler and priority system, FreeBSD/CI build changes, upstream's meta/kimi stream-observability internals, and plugin-quota metrics. None serve a PostgreSQL-first control plane.

## Context and current state

The exploration that grounds this design found that NixLLM is closer to several target features than the release notes imply, and further from others:

- **Cooldown is already per-`(auth, model)`.** `MarkResult` keys failure state on `canonicalModelKey(result.Model)` into `auth.ModelStates[model]`, `CooldownStateSnapshot()` already emits one record per auth/model pair, and the alert runner already distinguishes `level: "model"` from `level: "auth"`. What does not exist is a *pool-wide* per-model cooldown: when one credential is cooling for model X, the other credentials in the same upstream are still tried for model X.
- **The Claude executor never parses `Retry-After`.** Claude's `statusErr` does not implement `RetryAfter() *time.Duration`, so `retryAfterFromError` never fires and every Claude 429 takes the generic exponential ladder. Only the fast-mode-credit 429 is reclassified (as request-scoped, deliberately).
- **Model substitution is hidden, not detected.** `ClaudeExecutor.restoreResponseModel` rewrites the upstream-returned model back to the requested alias and discards the served value at that exact point. `usage.Record` has `Model` and `RouteModel` fields and their doc comment tells the operator to compare them *by hand* — and `SetRouteModel` is wired only in `openai_compat_executor.go`, so most rows have an empty `route_model`.
- **One unbounded stream accumulator exists.** `helps.AppendAPIResponseChunk` appends every stream line into a per-request `strings.Builder` for the whole response when request-log capture is on, with no size cap. Every other accumulator in the tree (reasoning-replay caches, SSE rewriter pending buffer, scanner line caps) is already bounded.
- **A transport-error classifier exists but is dead code.** `IsRetryableError` classifies `io.EOF`, `*url.Error`, and `*net.OpError` (dial, DNS, TLS handshake), but its only caller is `RunInnerLoop`, which has no production call sites. A dial failure today earns the generic one-minute transient cooldown and relies on ordinary credential rotation.
- **Translator tooling is uneven.** Collision-aware name capping (`SanitizedFunctionNameMap`, 64-char cap with hash suffix) exists in `internal/util` and is used by the Gemini/Antigravity translators; the Codex translators duplicate a weaker version in three files; the Claude↔OpenAI directions cap nothing. Per-turn tool-call handling is most correct in the Codex translator and is conversation-global (last-writer-wins on duplicate IDs) in the Antigravity, Gemini, and Claude→OpenAI translators. JSON-schema cleaning has keyword stripping but no boolean-subschema handling anywhere.
- **Adding a provider is a known, mapped path.** Roughly thirty touch points, from `internal/constant` through the executor registration, the registry catalog, the OAuth routes, and a single line in the dashboard's `OAUTH_TYPES`. The dashboard already renders device-flow UI generically (`state.flow === 'device'` with `user_code`).
- **There is no Connect-RPC dependency, anywhere.** `go.mod` carries only `google.golang.org/protobuf`, used solely for hand-rolled `protowire` decoding in `internal/signature`. The repository's precedent is dependency-free field walking. `codex_client_models.json` plus its updater is the exact template for a provider-specific model catalog.

## Architecture

```text
Phase 1  reliability & observability
         served-model capture -> usage_events.served_model -> substitution alert
         SetRouteModel wired in all executors
         bounded request-log capture
         IsRetryableError wired into the conductor's failure path
                 |
Phase 2  Claude cooling
         Retry-After parsing (overage-aware) -> MarkResult
         pool-wide per-model cooldown (auth-manager aggregate, next to globalPoolBreaker)
         scoped overage state (Quota.Reason = "overage")
                 |
Phase 3  translator correctness
         canonical tool-name capping + restore (internal/util)
         per-turn tool-call-ID scoping (translator/common helper)
         boolean subschemas + keyword stripping in cleanJSONSchema
         centralized finish-reason mapping
         cache-usage/input-token audit against billableUncachedInput
                 |
Phase 4  Meta (Muse Code)                      Phase 5  Devin (Connect-RPC)
         OAuth device flow -> mint API key               wire/ (protowire field codec)
         openai-compat executor, refresh = re-mint      auth/ (session, cascade, OAuth)
         ~26 touch points, flag-gated                   executor/ (stream, tool calls)
         dashboard: one line in OAUTH_TYPES             devin_models.json + updater
                                                        flag-gated, wire-unverified
```

## Phase 1 — Reliability and observability

**F1. Served-model capture and substitution detection.** Add `ServedModel` to `usage.Record`, populated from the upstream response body before any alias rewrite. The natural capture points are `ClaudeExecutor.restoreResponseModel` (which already reads the upstream value), the `ForceMapping` path in `response_model_rewriter.go`, and the stream path where usage is parsed per SSE event. On publish, the usage manager compares `ServedModel` against the requested `Model`/`RouteModel` and marks the record substituted when they differ. Persist as a new `served_model` column on `usage_events` (additive migration; `usage_errors` carries it too) and surface it as a new alert detector beside `alertProviderCooldown`, fingerprinting on provider + auth + model.

**F2. Wire `SetRouteModel` in the remaining executors.** Claude, Codex, Gemini, and Antigravity follow the `openai_compat_executor.go` pattern so `route_model` stops being empty for most traffic. This is what makes F1's comparison meaningful for those providers.

**F3. Bound the request-log accumulator.** Give `AppendAPIResponseChunk` a per-request byte cap, following the existing `BoundedLRU`/`maxPendingBufSize` precedent. On overflow, stop appending and increment a truncated counter so the operator sees that the captured log is partial rather than silently losing the tail.

**F4. Retry pre-HTTP transport failures.** Integrate the existing `IsRetryableError` classification into the conductor's live failure path so a dial/DNS/TLS failure is recorded as a retryable transport failure with a short credential cooldown, instead of falling through to the generic one-minute transient default. The 429 schedule is untouched.

## Phase 2 — Claude model-level cooling and scoped overage

**F1. Parse `Retry-After` in the Claude executor.** Wrap the executor's error in a type implementing `RetryAfter() *time.Duration`, parsed from `Retry-After` / `Retry-After-Ms` (the same header pair already parsed in `internal/auth/claude/anthropic_auth.go`). `retryAfterFromError` then picks it up with no change to `MarkResult`. Overage-only rejections are excluded — they are an account cap, not a wait hint, and are handled by F3.

**F2. Pool-wide per-model cooldown.** Today a model's cooldown is scoped to one credential, so a five-credential pool still rotates through four more credentials for the same model after the first 429. Add a manager-level aggregate next to `globalPoolBreaker`: when at least half (and at least two) of a provider's credentials are cooling for the same canonical model key, record a pool-model deadline at the maximum contributing `NextRetryAfter`. Selection checks this before per-credential rotation and fails fast through the existing `newModelCooldownError` (429 with `Retry-After`). Expiry is lazy, matching `updateAggregatedAvailability`. Surface it as a `CooldownStateRecord` with an empty `AuthID` (pool level) so the existing model-aware alert detector and dashboard pick it up without a new alert type. Config toggle `pool_model_cooldown`, default on.

**F3. Scoped overage state.** Introduce an explicit overage reason on the auth-scoped `Quota` state (`Reason: "overage"` with a long recovery horizon), set from 429/403 responses whose body indicates overage rather than throttling. Selection skips overage-limited credentials for the affected models. Plan status can be read from the existing `claude_oauth_profile_fetcher`.

**F4. Tests.** Unit coverage for the `Retry-After` parser and overage classification, a fake-clock test for the pool-model aggregation thresholds, and a selection test asserting fail-fast before credential rotation.

## Phase 3 — Translator correctness

**C1. One canonical tool-name capping path.** Extend `SanitizedFunctionNameMap` in `internal/util` to preserve `mcp__` prefixes explicitly and keep its deterministic collision suffix, then route every direction through it. Retire the three duplicated weaker copies in the Codex translators and give Claude↔OpenAI a cap where it currently has none. Response-side restoration reuses `RestoreSanitizedToolName`.

**C2. Per-turn tool-call-ID scoping.** Generalize the Codex translator's `pendingToolCall` + per-turn reset pattern into a small helper in `translator/common`, and adopt it in the three translators that currently build conversation-global `tcID2Name`/`toolResponses` maps (Antigravity, Gemini, Claude→OpenAI). This fixes cross-turn ID reuse matching the wrong tool. Scope response-side tool results per turn as well.

**C3. Schema normalization.** Add boolean-subschema handling to `cleanJSONSchema` (`true` becomes a permissive object, `false` becomes a rejected-with-description), and extend keyword stripping to the identifier keywords not yet listed. Apply it through the path-targeted `sanitizeAntigravityRequestSchemas` entry rather than whole-document cleaning, which the existing comment explains corrupts replayed arguments.

**C4. Centralized finish-reason mapping.** Replace the six inline per-direction switches with two shared helpers (stop-reason ↔ finish-reason) in `translator/common`, giving `content_filter` and unknown values explicit handling instead of a silent default. The executor side additionally records the raw `finish_reason` into usage — this is the non-translator half of the changeset.

**C5. Cache-usage and input-token consistency.** Audit the roughly fifteen translator sites that copy cached-token fields against the executor's `billableUncachedInput`, which is the single place that subtracts cache reads and writes from total input. Fix directions that double-count cached tokens or drop cache-creation tokens.

## Phase 4 — Meta (Muse Code) provider, built blind

Meta's flow is two-stage: an OAuth device flow establishes identity, then an LLM API key is minted from that identity and used as the inference bearer (the API itself is OpenAI-compatible). Credentials therefore store the minted key, and OAuth refresh exists to re-mint when a key dies.

The touch points follow the Antigravity template: `internal/auth/meta/` (constants, device-flow service modelled on `internal/auth/xai/xai.go`, minting, filename), `sdk/auth/meta.go` plus registration in `auth_manager.go`, `refresh_registry.go`, and the OAuth-provider list in `postgresstore.go`; an OpenAI-compatible executor whose refresh re-mints; executor registration in `service_executors.go`; a registry catalog section across `models.json`, `model_definitions.go`, and `model_updater.go`; the OAuth routes in `auth_files_provider_oauth.go` (device-flow template), `server_management.go`, `server_routes.go`, and `NormalizeOAuthProvider`; a CLI login command; `TypeOAuthMeta = "oauth:meta"` in `upstreamsync/render.go`; and one line in the dashboard's `OAUTH_TYPES` plus the `OAUTH_PROVIDERS`/endpoint/label registries.

Because the device-flow UI already renders generically from `state.flow === 'device'`, the dashboard cost is genuinely small.

**Blind validation strategy.** All outbound HTTP goes through a small `metaClient` interface so device start, polling, minting, refresh, and error rules are exercised against `httptest` fakes. Values that cannot be validated blind — client ID/secret, scopes, exact response shapes — live in `constants.go` with config overrides and are marked `TODO(verify-live)`. The provider is disabled behind `meta_provider_enabled` until validated. The honest expectation is one calibration session when an account exists, isolated to `internal/auth/meta/` by construction.

## Phase 5 — Devin provider, built blind

Devin is last because it is the heaviest and least verifiable: Connect-RPC against Cognition with a wire format upstream obtained by reverse engineering. Built blind and without reading their code, the payload layer will almost certainly not work on first contact. The design therefore maximizes the layers that can be made correct without the wire.

**Four layers, outermost first.** (1) `internal/runtime/executor/devin_executor*.go` — streaming and tool-call mapping, config-only sensitive-words pass-through, subagent identity sanitization; testable against synthetic stream fixtures. (2) `internal/auth/devin/` — session lifecycle, cascade-ID binding, loopback OAuth callback, quota/seat status; testable against a fake transport. (3) `internal/auth/devin/wire/` — hand-rolled `protowire` field encode/decode with field roles named rather than scattered magic numbers; this single file is what a calibration session edits. (4) Registry — `devin_models.json` plus its updater, cloned from the `codex_client_models.json` pair, with the `devin/` namespace and alias/UID mapping.

**Transport.** A `devinTransport` interface with a hand-rolled Connect-JSON client. Connect's framing is a public specification, so the envelope can be built correctly blind; only the payload fields are unverifiable. No new dependency is added — the repository has no Connect-RPC and its precedent is hand decoding.

**Blind validation.** Synthetic protobuf fixtures cover decode, usage extraction, and tool-call assembly; a fake transport covers session and cascade handling. Flag-gated by `devin_provider_enabled`. Phase 5 is expected to land as *wire-complete but unverified*, and its PR states that plainly.

## Definition of done

Each phase, before its PR:

- `gofmt -w .` clean, `go build -o test-output ./cmd/server` succeeding, `go test ./...` green for touched packages.
- One branch, one PR to the fork's `main`, tagged by bumping only the `0.x.x` half.
- Tests appropriate to available evidence: unit and fixture tests where no live account exists; Meta and Devin default-off behind flags.
- Config additions documented in `config.example.yaml`.
- No timeout added after an upstream connection is established, per AGENTS.md.
- Design and plan documents force-added (`git add -f`), since `docs/*` is gitignored.

## Out of scope

mDNS/DNS-SD LAN discovery; the plugin scheduler and priority system; FreeBSD and CI build changes; upstream's provider-specific stream-observability internals; plugin-quota metrics; and any upstream doc, sponsor, or community-link churn.