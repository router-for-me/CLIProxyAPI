# CLIProxyAPI Usage Event Publisher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a non-blocking usage event publisher to CLIProxyAPI that writes monthly local JSONL and syncs signed usage events to yui.web.

**Architecture:** Reuse the existing `sdk/cliproxy/usage` plugin pipeline so every executor path that already publishes usage can produce a structured event. The publisher writes a local JSONL ledger first, then optionally POSTs to yui.web with `x-internal-token`, timestamp, and HMAC signature; all failures are logged but never propagated to the user request.

**Tech Stack:** Go, existing CLIProxyAPI usage manager, logrus, Gin request context, HTTP client, HMAC SHA-256, JSONL files.

---

## Scope

This is the CLIProxyAPI-side execution slice of the cross-repo plan. The yui.web-side canonical plan is:

`/Users/wujianxiang/CodeSpace/yui.web/docs/ai/context/20260609-142619-shop-usage-monitoring-mvp-implementation-plan_CN.md`

## File Structure

- Create `internal/usage/event_types.go`: event DTO and helper functions.
- Create `internal/usage/event_writer.go`: JSONL writer and retention cleanup.
- Create `internal/usage/event_sync.go`: signed sync client.
- Create `internal/usage/event_plugin.go`: plugin wiring.
- Create or extend `internal/usage/event_plugin_test.go`: unit tests.
- Modify `cmd/server/main.go`: register publisher from environment.
- Modify `config.example.yaml`: document env settings.

## Task 1: Event Types and API Key Privacy

**Files:**
- Create: `internal/usage/event_types.go`
- Test: `internal/usage/event_plugin_test.go`

- [ ] Write tests proving marshaled events do not contain full API keys.
- [ ] Write tests proving key hash is stable and preview is non-empty.
- [ ] Write tests proving token totals normalize from input + output + reasoning when total is zero.
- [ ] Run `go test ./internal/usage -run TestUsageEvent -count=1` and confirm failure.
- [ ] Implement `UsageEvent`, `newUsageEvent`, `hashAPIKey`, `resolveEventRequestID`, and `resolveEventEndpoint`.
- [ ] Run `go test ./internal/usage -run TestUsageEvent -count=1` and confirm pass.
- [ ] Commit:

```bash
git add internal/usage/event_types.go internal/usage/event_plugin_test.go
git commit -m "feat: add usage event model"
```

## Task 2: Monthly JSONL Ledger

**Files:**
- Create: `internal/usage/event_writer.go`
- Test: `internal/usage/event_plugin_test.go`

- [ ] Write tests for append-only monthly JSONL files.
- [ ] Write tests for 90-day cleanup limited to `usage-events-*.jsonl`.
- [ ] Run `go test ./internal/usage -run TestUsageEventWriter -count=1` and confirm failure.
- [ ] Implement writer with `usage-events-YYYY-MM.jsonl` naming, append writes, and safe directory creation.
- [ ] Run `go test ./internal/usage -run TestUsageEventWriter -count=1` and confirm pass.
- [ ] Commit:

```bash
git add internal/usage/event_writer.go internal/usage/event_plugin_test.go
git commit -m "feat: write usage event jsonl"
```

## Task 3: Signed yui.web Sync Client

**Files:**
- Create: `internal/usage/event_sync.go`
- Test: `internal/usage/event_plugin_test.go`

- [ ] Write tests with `httptest.Server` that assert `x-internal-token`, `x-usage-timestamp`, and `x-usage-signature`.
- [ ] Write tests that server failure returns an error to the plugin but does not panic.
- [ ] Run `go test ./internal/usage -run TestUsageEventSync -count=1` and confirm failure.
- [ ] Implement HMAC over `timestamp + "\n" + raw_body`.
- [ ] Run `go test ./internal/usage -run TestUsageEventSync -count=1` and confirm pass.
- [ ] Commit:

```bash
git add internal/usage/event_sync.go internal/usage/event_plugin_test.go
git commit -m "feat: sign usage event sync"
```

## Task 4: Plugin Registration

**Files:**
- Create: `internal/usage/event_plugin.go`
- Modify: `cmd/server/main.go`
- Modify: `config.example.yaml`
- Test: `internal/usage/event_plugin_test.go`

- [ ] Write tests for enabled env, disabled env, sync failure, and JSONL write failure behavior.
- [ ] Run `go test ./internal/usage -run TestUsageEventPlugin -count=1` and confirm failure.
- [ ] Implement `RegisterUsageEventPluginFromEnv`.
- [ ] Register from `cmd/server/main.go`.
- [ ] Document env settings in `config.example.yaml`.
- [ ] Run:

```bash
go test ./internal/usage -count=1
go build -o test-output ./cmd/server
rm test-output
```

- [ ] Commit:

```bash
git add internal/usage/event_plugin.go internal/usage/event_plugin_test.go cmd/server/main.go config.example.yaml
git commit -m "feat: publish usage events"
```

## Task 5: Integration With yui.web

**Files:**
- Modify only files with bugs found during integration.

- [ ] Start yui.web with internal token and HMAC secret.
- [ ] Start CLIProxyAPI with matching env values.
- [ ] Send one test request with a local client key.
- [ ] Confirm `logs/usage/usage-events-YYYY-MM.jsonl` exists.
- [ ] Confirm yui.web receives or can import the event.
- [ ] Run:

```bash
go test ./internal/usage -count=1
go build -o test-output ./cmd/server
rm test-output
```

- [ ] Commit verification note if integration changes operational assumptions.

## Implementation Guardrails

- Never write full API keys to event JSON, logs, tests, or API responses.
- Never record prompt, response body, or client IP.
- Never let JSONL or sync errors block model requests.
- Do not make `usage-statistics-enabled` gate the JSONL event ledger.
- Keep all new code outside `internal/translator/`.
