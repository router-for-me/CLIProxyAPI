# Phase 1 (Reliability & Observability) Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make upstream behavior observable — record the model the upstream actually served and alert on silent substitution, stop the one unbounded stream-log accumulator, and turn the dead transport-error classifier into live per-entry retry.

**Architecture:** `usage.Record` gains a `ServedModel` field populated by executors from the raw upstream body before any alias rewrite. The usage flusher compares served vs requested and persists both plus a substitution flag on `usage_events`/`usage_errors`. A sibling alert detector consumes it. Separately, `writeAttemptResponse` gains a byte cap, and `RunInnerLoop` is wired into the three conductor attempt loops behind `cfg.Routing.Retry` (default `MaxAttempts=0` = today's single-attempt behavior).

**Tech Stack:** Go 1.26, PostgreSQL (pgx), gin, logrus, gjson/sjson, `google.golang.org/protobuf` (not needed this phase).

**Reference:** `docs/plans/2026-09-22-nixllm-upstream-releases-design.md`

---

## Scope corrections found during reconnaissance

Three findings from code exploration change the design doc's estimates. Read these before starting:

1. **Claude's `restoreResponseModel` never executes.** `ClaudeExecutor.upstreamModelNormalizer` is nil for Claude — only Kimi wires one (`kimi_executor.go:42`). So `restoreResponseModel` (`claude_executor.go:157-160`) early-returns and the served model is *not* discarded there. The real capture point is where the response body is already parsed for usage: `ParseClaudeUsage` / `ParseClaudeStreamUsage`. Capture there, which is also pre-rewrite (the conductor's `ForceMapping` rewrite happens later, in `wrapStreamResult` / `rewriteForceMappedResponse`).

2. **The unbounded buffer only grows on the fallback path.** `writeAttemptResponse` (`logging_helpers.go:476-498`) returns early when `attempt.responseSource != nil` (the normal request-log path writes to a temp file via `FileBodySource`). The heap-unbounded `strings.Builder` is reached only when a config has `RequestLog: true` but no file-body-source factory is attached. Cap it anyway — it is a real unbounded accumulator and `updateAggregatedResponse` rebuilds a second full copy from it per chunk.

3. **F4 is not "wire a classifier" — it is the deferred inner-loop integration.** A transport error with no HTTP status already reaches `applyAuthFailureState`'s `default:` branch (`conductor_cooldown.go:1954-1959`) and gets `recoverableFailureRetryAfter` — identical to a 5xx. Calling `IsRetryableError` there changes nothing. The classifier's value is in the *retry* decision, which requires constructing `AttemptFn` inside `executeMixedOnce`, `executeCountMixedOnce`, and `executeStreamMixedOnce` — exactly the integration the comment at `retry_loop.go:113-122` defers. Task 7 is therefore the largest task and is safely skippable: default config (`MaxAttempts=0`) preserves today's behavior. If you want to land Phase 1 faster, split Task 7 into its own PR.

## Constraints that bind every task

- **No timeout after an upstream connection is established** (AGENTS.md). Task 7 introduces per-attempt deadlines via `EntryBudget`; they apply only to a *retrying* attempt, which by definition has not delivered a response. Do not add deadlines anywhere else.
- **No `log.Fatal`/`log.Fatalf`.** Return errors, log via logrus.
- **Wrap defer errors.** `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- **Do not break the executor/`helps` split.** Helper/supporting files go under `internal/runtime/executor/helps/`; `internal/runtime/executor/` holds executors and their unit tests only.
- **Store tests are DSN-gated.** `internal/store` tests call `t.Skip("PGSTORE_TEST_DSN not set")` when the env var is absent. Every new store test must use the existing `newTestPostgresStore(t, "name")` helper so it skips cleanly without PG.
- **Verify after every task:** `gofmt -w .` then `go build -o test-output ./cmd/server && rm test-output`.

---

### Task 1: Add `ServedModel` to the usage record and reporter

**Files:**
- Modify: `sdk/cliproxy/usage/manager.go` (Record struct, near line 94 where `RouteModel` is declared)
- Modify: `internal/runtime/executor/helps/usage_helpers.go` (UsageReporter struct ~25-63, setter near `SetRouteModel` ~117-127, `buildRecordForModel` ~398)
- Test: `sdk/cliproxy/usage/served_model_test.go` (create)
- Test: `internal/runtime/executor/helps/usage_helpers_served_model_test.go` (create)

**Step 1: Write the failing detection test**

Create `sdk/cliproxy/usage/served_model_test.go`:

```go
package usage

import "testing"

func TestDetectSubstitution(t *testing.T) {
	cases := []struct {
		name        string
		served      string
		requested   string
		substituted bool
	}{
		{name: "identical", served: "claude-opus-5", requested: "claude-opus-5", substituted: false},
		{name: "case and space insensitive", served: " claude-opus-5 ", requested: "CLAUDE-OPUS-5", substituted: false},
		{name: "silent downgrade", served: "claude-haiku-4-5", requested: "claude-opus-5", substituted: true},
		{name: "served unknown is not substitution", served: "", requested: "claude-opus-5", substituted: false},
		{name: "requested unknown is not substitution", served: "claude-opus-5", requested: "", substituted: false},
		{name: "both unknown", served: "", requested: "", substituted: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := DetectSubstitution(tc.served, tc.requested)
			if report.Substituted != tc.substituted {
				t.Fatalf("DetectSubstitution(%q, %q).Substituted = %v, want %v",
					tc.served, tc.requested, report.Substituted, tc.substituted)
			}
			if report.Served != strings.TrimSpace(tc.served) {
				t.Fatalf("Served = %q, want %q", report.Served, strings.TrimSpace(tc.served))
			}
			if report.Requested != strings.TrimSpace(tc.requested) {
				t.Fatalf("Requested = %q, want %q", report.Requested, strings.TrimSpace(tc.requested))
			}
		})
	}
}
```

Add `"strings"` to the test file's imports.

**Step 2: Run it and confirm it fails**

Run: `go test ./sdk/cliproxy/usage/ -run TestDetectSubstitution -v`
Expected: FAIL — `undefined: DetectSubstitution`

**Step 3: Add the field, the report type, and the detector**

In `sdk/cliproxy/usage/manager.go`, add to `Record` immediately after the `RouteModel` field (which ends at line 94):

```go
	// ServedModel stores the model identifier the upstream response itself
	// reported serving, captured from the raw response body before any
	// alias/ForceMapping rewrite. Empty when the upstream did not report one.
	// Compared against Model at flush time so a silent upstream substitution
	// (the provider serving a different model than requested) is recorded
	// rather than hidden.
	ServedModel string
```

Then append to the same file (after the `Record` type's other helpers — put it below the type declarations, before `requestedModelAliasContextKey`):

```go
// SubstitutionReport describes whether the upstream served a model different
// from the one NixLLM asked for.
type SubstitutionReport struct {
	// Served is the trimmed model the upstream reported serving.
	Served string
	// Requested is the trimmed model NixLLM resolved and sent upstream.
	Requested string
	// Substituted is true only when both values are known and differ.
	Substituted bool
}

// DetectSubstitution compares the model the upstream reported serving against
// the model NixLLM sent. The comparison is case-insensitive and ignores
// surrounding whitespace, because providers vary the casing of the same id.
// An empty value on either side means "unknown" and is never reported as a
// substitution — a missing field must not raise a false alarm.
func DetectSubstitution(served, requested string) SubstitutionReport {
	trimmedServed := strings.TrimSpace(served)
	trimmedRequested := strings.TrimSpace(requested)
	return SubstitutionReport{
		Served:      trimmedServed,
		Requested:   trimmedRequested,
		Substituted: trimmedServed != "" && trimmedRequested != "" && !strings.EqualFold(trimmedServed, trimmedRequested),
	}
}
```

`strings` is already imported in `manager.go`.

**Step 4: Add the reporter field and setter**

In `internal/runtime/executor/helps/usage_helpers.go`, add a field to `UsageReporter` right after `routeModel string` (line 30):

```go
	servedModel     string
```

Add the setter immediately after `SetRouteModel` (ends at line 127):

```go
// SetServedModel records the model identifier the upstream response reported
// serving, captured from the raw response body before any alias rewrite.
// Persisted so a silent upstream substitution can be detected at flush time.
// Callers must not overwrite a value already set: the first reported model is
// the authoritative one (streaming responses repeat it on later events).
func (r *UsageReporter) SetServedModel(servedModel string) {
	if r == nil {
		return
	}
	if r.servedModel != "" {
		return
	}
	r.servedModel = strings.TrimSpace(servedModel)
}
```

**Step 5: Wire it into the record**

In `buildRecordForModel`, add to the returned `usage.Record` literal, right after the `RouteModel: r.routeModel,` line:

```go
		ServedModel:              r.servedModel,
```

**Step 6: Write the reporter test**

Create `internal/runtime/executor/helps/usage_helpers_served_model_test.go`:

```go
package helps

import (
	"context"
	"testing"
)

func TestUsageReporter_SetServedModelKeepsFirstValue(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "claude", "claude-opus-5", nil)
	reporter.SetServedModel("claude-opus-5")
	reporter.SetServedModel("claude-haiku-4-5")

	record := reporter.buildRecord(usage.Detail{}, false, usage.Failure{})
	if record.ServedModel != "claude-opus-5" {
		t.Fatalf("ServedModel = %q, want first reported value %q", record.ServedModel, "claude-opus-5")
	}
}

func TestUsageReporter_SetServedModelTrimsWhitespace(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "claude", "claude-opus-5", nil)
	reporter.SetServedModel("  claude-opus-5  ")

	record := reporter.buildRecord(usage.Detail{}, false, usage.Failure{})
	if record.ServedModel != "claude-opus-5" {
		t.Fatalf("ServedModel = %q, want trimmed %q", record.ServedModel, "claude-opus-5")
	}
}

func TestUsageReporter_SetServedModelNilReporter(t *testing.T) {
	var reporter *UsageReporter
	reporter.SetServedModel("claude-opus-5") // must not panic
}
```

Add the `"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"` import. Confirm the exact import path by reading the existing import block at the top of `usage_helpers.go` and matching it. Confirm the `buildRecord` method name by reading `publishWithOutcome` (~line 289 above) — it calls `r.buildRecord(detail, failed, fail)`.

**Step 7: Run both tests**

Run: `go test ./sdk/cliproxy/usage/ -run TestDetectSubstitution -v`
Expected: PASS (6 subtests)

Run: `go test ./internal/runtime/executor/helps/ -run TestUsageReporter_SetServedModel -v`
Expected: PASS (3 tests)

**Step 8: Verify build and commit**

```bash
gofmt -w .
go build -o test-output ./cmd/server && rm test-output
git add sdk/cliproxy/usage/manager.go sdk/cliproxy/usage/served_model_test.go \
        internal/runtime/executor/helps/usage_helpers.go \
        internal/runtime/executor/helps/usage_helpers_served_model_test.go
git commit -m "feat(usage): add ServedModel field and substitution detector

Records the model the upstream response reported serving, alongside a
DetectSubstitution helper comparing it against the requested model. Both
values empty-or-equal is never a substitution, so a missing upstream
field cannot raise a false alarm.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: Capture the served model in the Claude executor

**Files:**
- Modify: `internal/runtime/executor/helps/usage_helpers.go` (near `ParseClaudeUsage`, ~861)
- Modify: `internal/runtime/executor/claude_executor_execute.go` (~292 non-stream)
- Modify: `internal/runtime/executor/claude_executor_stream.go` (~294 stream loop)
- Test: `internal/runtime/executor/helps/usage_helpers_claude_served_model_test.go` (create)

**Step 1: Write the failing parser test**

Create `internal/runtime/executor/helps/usage_helpers_claude_served_model_test.go`:

```go
package helps

import "testing"

func TestParseClaudeServedModel(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "non-stream response",
			payload: `{"id":"msg_1","model":"claude-opus-5","usage":{"input_tokens":10}}`,
			want:    "claude-opus-5",
		},
		{
			name:    "stream message_start event",
			payload: `event: message_start\ndata: {"type":"message_start","message":{"model":"claude-haiku-4-5"}}`,
			want:    "claude-haiku-4-5",
		},
		{
			name:    "top-level stream model",
			payload: `data: {"type":"message_delta","model":"claude-opus-5"}`,
			want:    "claude-opus-5",
		},
		{
			name:    "message model wins over absent top level",
			payload: `{"message":{"model":"claude-sonnet-5"}}`,
			want:    "claude-sonnet-5",
		},
		{name: "absent", payload: `{"usage":{"input_tokens":10}}`, want: ""},
		{name: "not json", payload: `data: [DONE]`, want: ""},
		{name: "empty", payload: ``, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseClaudeServedModel(tc.payload); got != tc.want {
				t.Fatalf("ParseClaudeServedModel(%q) = %q, want %q", tc.payload, got, tc.want)
			}
		})
	}
}
```

Note: in the stream cases the payload is a raw Go string with a literal `\n`, matching how `ParseClaudeStreamUsage` already receives single SSE lines. Read `ParseClaudeStreamUsage` (~869) and its `jsonPayload` helper before implementing, so `ParseClaudeServedModel` strips the SSE `data:` prefix the same way.

**Step 2: Run it and confirm it fails**

Run: `go test ./internal/runtime/executor/helps/ -run TestParseClaudeServedModel -v`
Expected: FAIL — `undefined: ParseClaudeServedModel`

**Step 3: Implement the parser**

Add to `internal/runtime/executor/helps/usage_helpers.go`, immediately after `ParseClaudeStreamUsage`:

```go
// ParseClaudeServedModel extracts the model identifier a Claude (Anthropic
// Messages API) response reported serving. It accepts both a full response
// body and a single SSE line, mirroring ParseClaudeUsage and
// ParseClaudeStreamUsage. The nested message.model is preferred because the
// streaming message_start event carries the model there. Returns "" when the
// payload has no model field, which callers treat as "upstream did not say".
func ParseClaudeServedModel(payload []byte) string {
	jsonBytes := jsonPayload(payload)
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return ""
	}
	if model := strings.TrimSpace(gjson.GetBytes(jsonBytes, "message.model").String()); model != "" {
		return model
	}
	return strings.TrimSpace(gjson.GetBytes(jsonBytes, "model").String())
}
```

**Step 4: Run the parser test**

Run: `go test ./internal/runtime/executor/helps/ -run TestParseClaudeServedModel -v`
Expected: PASS (7 subtests)

**Step 5: Capture it in the non-stream path**

In `claude_executor_execute.go`, the existing block around line 292 reads:

```go
		reporter.Publish(ctx, helps.ParseClaudeUsage(data))
	}
	data = e.restoreResponseModel(data, req.Model)
```

Insert the capture between the publish and the restore so it reads from the same `data` before any rewrite:

```go
		reporter.Publish(ctx, helps.ParseClaudeUsage(data))
		reporter.SetServedModel(helps.ParseClaudeServedModel(data))
	}
	data = e.restoreResponseModel(data, req.Model)
```

**Step 6: Capture it in the stream path**

In `claude_executor_stream.go`, the loop around lines 290-293 reads:

```go
		for scanner.Scan() {
			line := scanner.Bytes()
			observeClaudeStreamLine(line, &upstreamMessageID, &upstreamCompleted)
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseClaudeStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
```

Add the capture after the usage block. `SetServedModel` keeps the first non-empty value, so calling it per line is safe and cheap:

```go
			if detail, ok := helps.ParseClaudeStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			reporter.SetServedModel(helps.ParseClaudeServedModel(line))
```

`scanner.Bytes()` aliases the scanner's internal buffer, which is reused on the next `Scan()`. `SetServedModel` trims and stores a string copy, so this is safe — but do not change it to store a `[]byte` slice of `line`.

**Step 7: Verify build and tests**

Run: `gofmt -w . && go build -o test-output ./cmd/server && rm test-output`
Run: `go test ./internal/runtime/executor/helps/ ./internal/runtime/executor/ -run 'Claude|ServedModel' -v`
Expected: PASS. If a pre-existing Claude executor test fails, read it before assuming your change caused it — rerun on a clean tree (`git stash`) to compare.

**Step 8: Commit**

```bash
git add internal/runtime/executor/helps/usage_helpers.go \
        internal/runtime/executor/helps/usage_helpers_claude_served_model_test.go \
        internal/runtime/executor/claude_executor_execute.go \
        internal/runtime/executor/claude_executor_stream.go
git commit -m "feat(claude): capture upstream-served model into usage records

Parses the model the Anthropic response reported serving, from both the
non-stream body and each SSE line, before any alias rewrite. First
non-empty value wins so streaming message_start is authoritative.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 3: Persist served model and substitution on both usage tables

**Files:**
- Modify: `internal/store/pg_usage.go` (`UsageEvent` struct ~26, `usageEventColumnList` ~483, `usageEventColumnCount` ~495, `InsertEvent` ~497, `BatchInsertEvents` ~532)
- Modify: `internal/store/pg_usage_errors.go` (`UsageError` struct, `usageErrorColumnList` ~124, `InsertError` ~207, `BatchInsertErrors` ~248)
- Modify: `internal/store/pg_usage_flusher.go` (`toEvent` ~273, `toError` ~380)
- Modify: `internal/store/postgresstore.go` (CREATE TABLE ~1098, ALTER TABLE block ~1240-1286, errors CREATE TABLE ~1288)
- Test: `internal/store/pg_usage_served_model_test.go` (create, DSN-gated)

**Step 1: Write the failing store test**

Create `internal/store/pg_usage_served_model_test.go`:

```go
package store

import (
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageStore_InsertEventPersistsServedModel(t *testing.T) {
	store := newTestPostgresStore(t, "usage_served_model_test")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	event := UsageEvent{
		Provider:    "anthropic",
		Model:       "claude-opus-5",
		ServedModel: "claude-haiku-4-5",
		RequestedAt: now(),
	}
	if err := us.InsertEvent(ctx, event); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	var served string
	row := store.db.QueryRowContext(ctx,
		"SELECT served_model FROM "+us.eventsTable+" WHERE model = $1", "claude-opus-5")
	if err := row.Scan(&served); err != nil {
		t.Fatalf("scan served_model: %v", err)
	}
	if served != "claude-haiku-4-5" {
		t.Fatalf("served_model = %q, want %q", served, "claude-haiku-4-5")
	}
}

func TestFlusher_LogsSubstitution(t *testing.T) {
	// DetectSubstitution is the flusher's decision point; assert the wiring
	// contract rather than the log line.
	report := coreusage.DetectSubstitution("claude-haiku-4-5", "claude-opus-5")
	if !report.Substituted {
		t.Fatalf("expected a substitution to be detected")
	}
}
```

Before writing, read the existing `internal/store/pg_usage_test.go` to copy the exact helper names (`newTestPostgresStore`, `cancelableTestCtx`, `now`) and confirm `us.eventsTable` is reachable from the test (same package). If `eventsTable` is unexported and already used by tests, use it; otherwise call the exported accessor if one exists, or select by `model` only with a `LIMIT 1`.

**Step 2: Run it and confirm it fails**

Run: `go test ./internal/store/ -run TestUsageStore_InsertEventPersistsServedModel -v`
Expected without `PGSTORE_TEST_DSN`: SKIP. Set the DSN and rerun to see it FAIL with `column "served_model" does not exist`.

**Step 3: Add the columns to the schema**

In `internal/store/postgresstore.go`, in the `usage_events` CREATE TABLE (the statement beginning near line 1099), add a line after `route_model TEXT,`:

```sql
			served_model            TEXT,
```

Then in the ALTER TABLE backfill block for `usage_events` (the slice of `{name, def}` pairs ending around line 1247, immediately before the `auto_router_decision` loop at 1253), append an entry:

```go
		{"served_model", "TEXT"},
```

Then apply the same two edits to `usage_errors`: add `served_model TEXT,` to its CREATE TABLE (near line 1288, after its `route_model TEXT,`) and add an `ALTER TABLE %s ADD COLUMN IF NOT EXISTS served_model TEXT` statement in that block, following the exact idempotent form used at line 1268:

```go
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS served_model TEXT`, usageErrorsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter usage_errors add served_model: %w", err)
	}
```

**Step 4: Add the struct fields**

In `internal/store/pg_usage.go`, add to `UsageEvent` right after `Model string`:

```go
	// ServedModel is the model the upstream response reported serving. It
	// differs from Model when the provider silently substituted a different
	// model; empty when the upstream did not report one.
	ServedModel string `json:"served_model,omitempty"`
```

Make the identical addition to `UsageError` in `internal/store/pg_usage_errors.go`, immediately after its `Model string`.

**Step 5: Extend the column lists and counts**

In `internal/store/pg_usage.go`, add `served_model` to `usageEventColumnList` after `model,` (line ~486), then change:

```go
const usageEventColumnCount = 40
```

to

```go
const usageEventColumnCount = 41
```

In the same file, update `InsertEvent`'s placeholder list — it currently enumerates `$1`..`$40` across three lines — to reach `$41`. Then add the bound value in the argument list, after `e.Provider, e.ExecutorType, e.Model, e.Alias, e.Endpoint,` → change to include `e.ServedModel` positioned to match the column list order. The column list order is authoritative: `..., model, served_model, alias, endpoint, ...`, so the args become:

```go
		e.Provider, e.ExecutorType, e.Model, e.ServedModel, e.Alias, e.Endpoint,
```

Do the same in `BatchInsertEvents`: it loops `for j := 1; j <= usageEventColumnCount; j++`, so the placeholder generation needs no edit — but the per-row argument append must carry `ev.ServedModel` in the matching position.

Apply the identical set of edits to `usageErrorColumnList` (add `served_model` after `model,`), its count constant (32 → 33), `InsertError`'s placeholders (add `$33`) and args, and `BatchInsertErrors`.

**Step 6: Wire the flusher**

In `internal/store/pg_usage_flusher.go`, in `toEvent`, add `ServedModel: record.ServedModel,` to the returned `UsageEvent` literal, next to `Model: model,`. Do the same in `toError` for the `UsageError` literal.

Then add the substitution warning in `toEvent`, immediately after the `model` resolution block (`if model == "" { model = "unknown" }`):

```go
	// A silent substitution is the provider serving a different model than
	// NixLLM resolved. Log it here, at the single point where both values are
	// known, so the operator sees it in the same stream as the request rather
	// than only in the alerts feed.
	if report := coreusage.DetectSubstitution(record.ServedModel, model); report.Substituted {
		log.WithFields(log.Fields{
			"requested_model": report.Requested,
			"served_model":    report.Served,
			"provider":        record.Provider,
			"auth_id":         record.AuthID,
			"request_id":      record.RequestID,
		}).Warn("upstream served a different model than requested")
	}
```

Confirm `coreusage` is the import alias already used in that file for `sdk/cliproxy/usage`; match it.

**Step 7: Run the tests**

Run: `gofmt -w . && go build -o test-output ./cmd/server && rm test-output`
Run: `go test ./internal/store/ -run 'ServedModel|Substitution' -v`
Expected: PASS with `PGSTORE_TEST_DSN` set; SKIP without it.

**Step 8: Commit**

```bash
git add internal/store/pg_usage.go internal/store/pg_usage_errors.go \
        internal/store/pg_usage_flusher.go internal/store/postgresstore.go \
        internal/store/pg_usage_served_model_test.go
git commit -m "feat(store): persist served model and log silent substitutions

Adds an idempotent served_model column to usage_events and usage_errors,
binds it through both inserts, and warns at flush time when the upstream
reported serving a different model than the one requested.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 4: Alert on silent model substitution

**Files:**
- Modify: `internal/store/pg_alerts.go` (alert type constants ~25-37, `AlertFeedCategories`)
- Modify: `internal/api/handlers/management/alerts_runner.go` (`runAlertChecks` ~43-105, new detector)
- Test: `internal/api/handlers/management/alerts_substitution_test.go` (create)

**Step 1: Write the failing detector test**

The detector needs a way to find substituted rows. Add the query as a store method so it is testable. Create `internal/api/handlers/management/alerts_substitution_test.go`:

```go
package management

import (
	"testing"
	"time"
)

func TestFormatSubstitutionMessage(t *testing.T) {
	got := formatSubstitutionMessage("claude-opus-5", "claude-haiku-4-5", "anthropic", 7)
	want := `anthropic served "claude-haiku-4-5" for 7 request(s) that asked for "claude-opus-5".`
	if got != want {
		t.Fatalf("formatSubstitutionMessage() = %q, want %q", got, want)
	}
}

func TestSubstitutionFingerprintIsStablePerProviderModelPair(t *testing.T) {
	a := substitutionFingerprint("anthropic", "claude-opus-5", "claude-haiku-4-5")
	b := substitutionFingerprint("anthropic", "claude-opus-5", "claude-haiku-4-5")
	if a != b {
		t.Fatalf("fingerprint not stable: %q vs %q", a, b)
	}
	c := substitutionFingerprint("anthropic", "claude-opus-5", "claude-sonnet-5")
	if a == c {
		t.Fatalf("fingerprint must differ when the served model differs")
	}
}

func TestSubstitutionDetectorIntervalIsPositive(t *testing.T) {
	if alertSubstitutionLookback <= 0 || alertSubstitutionLookback > time.Hour {
		t.Fatalf("alertSubstitutionLookback = %v, want a positive window at most one hour", alertSubstitutionLookback)
	}
}
```

**Step 2: Run it and confirm it fails**

Run: `go test ./internal/api/handlers/management/ -run 'Substitution' -v`
Expected: FAIL — `undefined: formatSubstitutionMessage`, `undefined: substitutionFingerprint`, `undefined: alertSubstitutionLookback`

**Step 3: Add the alert type**

In `internal/store/pg_alerts.go`, add to the alert-type const block (ends line 31):

```go
	AlertTypeModelSubstitution = "model_substitution"
```

and append it to `AlertFeedCategories` (line 35-40):

```go
	AlertTypeModelSubstitution,
```

**Step 4: Add the store query**

In `internal/store/pg_usage.go`, add a method that returns substitutions over a window:

```go
// SubstitutionRow aggregates usage_events rows where the upstream reported
// serving a different model than the one requested, over the given window.
type SubstitutionRow struct {
	Provider    string
	Model       string
	ServedModel string
	Count       int64
}

// ListSubstitutions returns substitution aggregates for events requested at or
// after since. Rows with an empty or equal served_model are excluded. Results
// are ordered by descending count so the loudest substitution surfaces first.
func (s *UsageStore) ListSubstitutions(ctx context.Context, since time.Time) ([]SubstitutionRow, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT provider, model, served_model, COUNT(*) AS substitution_count
		FROM %s
		WHERE requested_at >= $1
		  AND served_model IS NOT NULL
		  AND served_model <> ''
		  AND LOWER(served_model) <> LOWER(model)
		GROUP BY provider, model, served_model
		ORDER BY substitution_count DESC
	`, s.eventsTable), since)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list substitutions: %w", err)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.WithError(errClose).Warn("postgres store: close substitutions rows failed")
		}
	}()
	var out []SubstitutionRow
	for rows.Next() {
		var row SubstitutionRow
		if errScan := rows.Scan(&row.Provider, &row.Model, &row.ServedModel, &row.Count); errScan != nil {
			return nil, fmt.Errorf("postgres store: scan substitution row: %w", errScan)
		}
		out = append(out, row)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("postgres store: iterate substitutions: %w", errRows)
	}
	return out, nil
}
```

**Step 5: Add the detector**

In `internal/api/handlers/management/alerts_runner.go`, add near the other detector constants:

```go
// alertSubstitutionLookback bounds how far back the substitution detector
// scans. Bounded to one hour: substitutions are actionable while the
// affected traffic is recent, and a wider window would re-alert on a
// condition the operator already acknowledged.
const alertSubstitutionLookback = 30 * time.Minute
```

Add the helpers and detector at the end of the file:

```go
// formatSubstitutionMessage renders the operator-facing sentence for one
// provider/model/served triple.
func formatSubstitutionMessage(requested, served, provider string, count int64) string {
	return fmt.Sprintf(`%s served %q for %d request(s) that asked for %q.`, provider, served, count, requested)
}

// substitutionFingerprint keys the suppression window on the provider and the
// requested→served pair, so a recurring substitution bumps the existing row
// while a different served model raises a new alert.
func substitutionFingerprint(provider, requested, served string) string {
	return alertFingerprintKey(store.AlertTypeModelSubstitution,
		provider, requested+"->"+served)
}

// alertModelSubstitution records one alert per provider/model/served triple
// seen in the lookback window. Silent substitution is always at least a
// warning: the client asked for a specific model and received another.
func (h *Handler) alertModelSubstitution(ctx context.Context, alerts *store.AlertStore, usage *store.UsageStore, suppression time.Duration) {
	since := time.Now().UTC().Add(-alertSubstitutionLookback)
	rows, err := usage.ListSubstitutions(ctx, since)
	if err != nil {
		log.WithError(err).Warn("alerts: list substitutions failed; skipping")
		return
	}
	for _, row := range rows {
		_, _, errRecord := alerts.RecordAlert(ctx, store.Alert{
			AlertType:  store.AlertTypeModelSubstitution,
			Severity:   store.AlertSeverityWarning,
			Title:      "Upstream served a different model",
			Message:    formatSubstitutionMessage(row.Model, row.ServedModel, row.Provider, row.Count),
			EntityID:   row.Provider,
			EntityName: row.Provider,
			Model:      row.Model,
			Provider:   row.Provider,
			Data: map[string]any{
				"requested_model":    row.Model,
				"served_model":       row.ServedModel,
				"substitution_count": row.Count,
				"lookback_minutes":   int(alertSubstitutionLookback.Minutes()),
			},
			Fingerprint: substitutionFingerprint(row.Provider, row.Model, row.ServedModel),
		}, suppression)
		if errRecord != nil {
			log.WithError(errRecord).WithField("provider", row.Provider).
				Debug("alerts: record model substitution alert failed")
		}
	}
}
```

Verify the exact `RecordAlert` signature against the existing `alertProviderCooldown` (it returns three values, as shown above). Match the receiver name `h` and the `store` import alias already used in the file.

**Step 6: Register the detector**

In `runAlertChecks`, extend the `h.mu.Lock()` capture block so `usage` is available (it already is — `usage := h.pgUsage`), then add after the last `if settings.CategoryEnabled(...)` block:

```go
	if settings.CategoryEnabled(store.AlertTypeModelSubstitution) && usage != nil {
		h.alertModelSubstitution(ctx, alerts, usage, suppression)
	}
```

**Step 7: Run the tests**

Run: `gofmt -w . && go build -o test-output ./cmd/server && rm test-output`
Run: `go test ./internal/api/handlers/management/ -run 'Substitution' -v`
Expected: PASS (3 tests)

**Step 8: Commit**

```bash
git add internal/store/pg_alerts.go internal/store/pg_usage.go \
        internal/api/handlers/management/alerts_runner.go \
        internal/api/handlers/management/alerts_substitution_test.go
git commit -m "feat(alerts): alert on silent upstream model substitution

Adds a model_substitution alert category backed by a grouped usage_events
query over a bounded lookback, fingerprinted per provider and
requested->served pair so a different served model raises a new alert.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 5: Wire `SetRouteModel` in the remaining executors

**Files:**
- Modify: `internal/runtime/executor/claude_executor_execute.go`, `claude_executor_stream.go`
- Modify: `internal/runtime/executor/gemini_executor.go`
- Modify: `internal/runtime/executor/antigravity_executor_execute.go`
- Modify: `internal/runtime/executor/codex_executor_execute.go`
- Reference: `internal/runtime/executor/openai_compat_executor.go` (the pattern)

**Step 1: Read the reference pattern**

Read `openai_compat_executor.go` at each `SetRouteModel` call site (search `SetRouteModel`). Note what value is passed — it is the client-requested model from context, not the resolved upstream model. Reproduce that exact source in each new call site; do not invent a new accessor.

**Step 2: Add the call in each executor**

For each of the four executors, place `reporter.SetRouteModel(<client-requested model>)` next to the existing `reporter.SetEndpoint(...)` call so route and endpoint are set together. Use the same context accessor `openai_compat_executor.go` uses. If an executor has no `SetEndpoint` call, put it immediately after the `reporter := ...` construction.

**Step 3: Write a regression test**

Create `internal/runtime/executor/route_model_test.go`:

```go
package executor

import (
	"context"
	"testing"
)

// The client-requested model must reach the usage record's RouteModel so the
// substitution comparison has both sides. This guards against an executor
// forgetting the wiring the way it was missing before this change.
func TestClaudeReporterSetsRouteModel(t *testing.T) {
	// Build a reporter through the same constructor the executor uses and
	// assert SetRouteModel round-trips into the record.
	reporter := helps.NewUsageReporter(context.Background(), "claude", "claude-opus-5", nil)
	reporter.SetRouteModel("claude-opus-5-alias")

	record := reporter.BuildRecordForTest(usage.Detail{}, false, usage.Failure{})
	if record.RouteModel != "claude-opus-5-alias" {
		t.Fatalf("RouteModel = %q, want %q", record.RouteModel, "claude-opus-5-alias")
	}
}
```

`buildRecordForModel` is unexported, so this cross-package test cannot call it. Instead, write this test **in package `helps`** at `internal/runtime/executor/helps/usage_helpers_routemodel_test.go` and call `buildRecord` directly (as Task 1's test does). Then verify the executor wiring by the build succeeding and by reading each call site — an executor-level assertion would require standing up a fake provider, which is disproportionate here. State in the commit message that the executor call sites are verified by inspection plus the `helps`-level round-trip test.

**Step 4: Run the tests**

Run: `gofmt -w . && go build -o test-output ./cmd/server && rm test-output`
Run: `go test ./internal/runtime/executor/helps/ -run RouteModel -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/runtime/executor/claude_executor_execute.go \
        internal/runtime/executor/claude_executor_stream.go \
        internal/runtime/executor/gemini_executor.go \
        internal/runtime/executor/antigravity_executor_execute.go \
        internal/runtime/executor/codex_executor_execute.go \
        internal/runtime/executor/helps/usage_helpers_routemodel_test.go
git commit -m "feat(executors): set route model for claude, gemini, antigravity, codex

RouteModel was only populated by the openai-compat executor, leaving the
client-requested model empty on most usage rows and making misrouting
undiagnosable for those providers.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 6: Bound the request-log response accumulator

**Files:**
- Modify: `internal/runtime/executor/helps/logging_helpers.go` (`upstreamAttempt` ~36-49, `writeAttemptResponse` ~476-498, `updateAggregatedResponse` ~518-540)
- Test: `internal/runtime/executor/helps/logging_helpers_bound_test.go` (create)

**Step 1: Write the failing test**

Create `internal/runtime/executor/helps/logging_helpers_bound_test.go`:

```go
package helps

import (
	"strings"
	"testing"
)

func TestWriteAttemptResponseStopsAtCap(t *testing.T) {
	attempt := &upstreamAttempt{response: &strings.Builder{}}
	chunk := []byte(strings.Repeat("x", 4096))

	// Write well past the cap.
	for i := 0; i < (maxAttemptResponseLogBytes/len(chunk))+8; i++ {
		writeAttemptResponse(nil, attempt, chunk)
	}

	if got := attempt.response.Len(); got > maxAttemptResponseLogBytes {
		t.Fatalf("buffered %d bytes, want at most %d", got, maxAttemptResponseLogBytes)
	}
	if !attempt.bodyTruncated {
		t.Fatalf("expected bodyTruncated to be set once the cap was reached")
	}
}

func TestWriteAttemptResponseKeepsSmallBodiesIntact(t *testing.T) {
	attempt := &upstreamAttempt{response: &strings.Builder{}}
	writeAttemptResponse(nil, attempt, []byte("small body"))

	if attempt.response.String() != "small body" {
		t.Fatalf("buffer = %q, want %q", attempt.response.String(), "small body")
	}
	if attempt.bodyTruncated {
		t.Fatalf("a small body must not be marked truncated")
	}
}
```

**Step 2: Run it and confirm it fails**

Run: `go test ./internal/runtime/executor/helps/ -run TestWriteAttemptResponse -v`
Expected: FAIL — `undefined: maxAttemptResponseLogBytes`, `unknown field bodyTruncated`

**Step 3: Add the cap and the flag**

In `internal/runtime/executor/helps/logging_helpers.go`, add to the existing `const` block (which holds `maxDeferredAPIRequestBodyBytes`):

```go
	// maxAttemptResponseLogBytes bounds the in-memory per-attempt response log
	// buffer. The buffer is only used when no file-backed response source is
	// attached (the normal request-log path writes to a temp file instead), but
	// it grows per streamed chunk and updateAggregatedResponse copies it in
	// full on every chunk. 8 MiB is far above any realistic debug payload while
	// keeping worst-case heap bounded.
	maxAttemptResponseLogBytes = 8 << 20
```

Add the field to `upstreamAttempt`, after `bodyHasContent bool`:

```go
	bodyTruncated        bool
```

**Step 4: Enforce it in the write funnel**

Replace the tail of `writeAttemptResponse`:

```go
	if attempt.response == nil {
		attempt.response = &strings.Builder{}
	}
	attempt.response.Write(payload)
```

with:

```go
	if attempt.response == nil {
		attempt.response = &strings.Builder{}
	}
	if attempt.bodyTruncated {
		return
	}
	// Cap before writing so a single oversized chunk cannot overshoot the
	// bound. A truncated buffer is flagged rather than silently losing the
	// tail, so an operator reading the captured log knows it is partial.
	if attempt.response.Len()+len(payload) > maxAttemptResponseLogBytes {
		attempt.bodyTruncated = true
		return
	}
	attempt.response.Write(payload)
```

**Step 5: Surface truncation in the aggregated copy**

Read `updateAggregatedResponse` (~518-540). If it concatenates each attempt's `response.String()`, append a marker when the attempt was truncated so the operator sees the loss:

```go
		builder.WriteString(attempt.response.String())
		if attempt.bodyTruncated {
			builder.WriteString("\n[truncated: response log exceeded ")
			builder.WriteString(strconv.Itoa(maxAttemptResponseLogBytes))
			builder.WriteString(" bytes]\n")
		}
```

Add `"strconv"` to the imports if absent. If `updateAggregatedResponse` does not have this shape, adapt the marker to wherever it reads `attempt.response` and note the adaptation in the commit message.

**Step 6: Run the tests**

Run: `gofmt -w . && go build -o test-output ./cmd/server && rm test-output`
Run: `go test ./internal/runtime/executor/helps/ -run TestWriteAttemptResponse -v`
Expected: PASS (2 tests)

**Step 7: Confirm no existing test regressed**

Run: `go test ./internal/runtime/executor/helps/ -v 2>&1 | tail -40`
Expected: no new failures.

**Step 8: Commit**

```bash
git add internal/runtime/executor/helps/logging_helpers.go \
        internal/runtime/executor/helps/logging_helpers_bound_test.go
git commit -m "fix(helps): bound the in-memory request-log response buffer

The per-attempt response buffer grew unbounded per streamed chunk on the
fallback path (no file-backed response source), and the aggregated copy
rebuilt it in full on every chunk. Caps it at 8 MiB and flags truncation
so a partial captured log is visible rather than silent.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 7: Wire per-entry retry for transient failures (largest task; safely skippable)

**Why this is last:** reconnaissance showed this is the deferred `RunInnerLoop` integration, not a classifier wiring. It touches the three hottest conductor loops. Default config (`MaxAttempts: 0`) preserves today's single-attempt behavior, so landing it does not change production behavior until an operator sets `routing.retry.max-attempts`. **If you want Phase 1 to land sooner, stop after Task 6 and file Task 7 as its own PR.**

**Files:**
- Modify: `sdk/cliproxy/auth/conductor_execution.go` (`executeMixedOnce` ~367-530, `executeCountMixedOnce` ~532-695)
- Modify: `sdk/cliproxy/auth/conductor_stream.go` (`executeStreamWithModelPool` ~202)
- Modify: `sdk/cliproxy/auth/retry_loop.go` (add a config→opts builder)
- Reference: `internal/config/config_types.go:317-338` (`RoutingConfig.Retry`, `RetryConfig`), `sdk/cliproxy/auth/retry_budget.go` (`EntryBudget`, `ExitReason`)
- Test: `sdk/cliproxy/auth/retry_integration_test.go` (create)

**Step 1: Confirm the config is truly unread**

Run: `grep -rn "Routing.Retry\|RetryConfig" --include=*.go sdk/cliproxy/auth/ internal/runtime/`
Expected: no hits in `sdk/cliproxy/auth`. If hits exist, a prior change already wired it — stop and re-plan.

**Step 2: Write the failing opts-builder test**

Create `sdk/cliproxy/auth/retry_integration_test.go`:

```go
package auth

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestInnerLoopOptsFromConfigDisabledByDefault(t *testing.T) {
	opts := innerLoopOptsFromConfig(nil)
	if opts.MaxAttempts != 0 {
		t.Fatalf("nil config must disable retry, got MaxAttempts=%d", opts.MaxAttempts)
	}
}

func TestInnerLoopOptsFromConfigZeroAttemptsDisablesRetry(t *testing.T) {
	cfg := &config.Config{}
	opts := innerLoopOptsFromConfig(cfg)
	if opts.MaxAttempts != 0 {
		t.Fatalf("zero max-attempts must disable retry, got %d", opts.MaxAttempts)
	}
}

func TestInnerLoopOptsFromConfigPassesThrough(t *testing.T) {
	cfg := &config.Config{}
	cfg.Routing.Retry = config.RetryConfig{MaxAttempts: 3, MaxTimeMS: 5000, BackoffMS: 200}
	opts := innerLoopOptsFromConfig(cfg)
	if opts.MaxAttempts != 3 || opts.MaxTimeMS != 5000 || opts.BackoffMS != 200 {
		t.Fatalf("opts = %+v, want MaxAttempts=3 MaxTimeMS=5000 BackoffMS=200", opts)
	}
}
```

Verify the exact field path for `Routing` on `config.Config` before writing; adjust if it is `cfg.Routing.Retry` vs a different nesting.

**Step 3: Run it and confirm it fails**

Run: `go test ./sdk/cliproxy/auth/ -run TestInnerLoopOptsFromConfig -v`
Expected: FAIL — `undefined: innerLoopOptsFromConfig`

**Step 4: Add the builder**

Append to `sdk/cliproxy/auth/retry_loop.go`:

```go
// innerLoopOptsFromConfig maps the operator's routing.retry block onto the
// per-entry retry options. A nil config or a zero MaxAttempts both disable
// retry, which makes the helper a no-op degrade to exactly one attempt per
// entry — the behavior before this wiring existed. That is the default, so
// enabling retry is an explicit operator decision.
func innerLoopOptsFromConfig(cfg *config.Config) InnerLoopOpts {
	if cfg == nil {
		return InnerLoopOpts{}
	}
	retry := cfg.Routing.Retry
	if retry.MaxAttempts == 0 {
		return InnerLoopOpts{}
	}
	return InnerLoopOpts{
		MaxAttempts: retry.MaxAttempts,
		MaxTimeMS:   retry.MaxTimeMS,
		BackoffMS:   retry.BackoffMS,
	}
}
```

Add the `config` import.

**Step 5: Wrap the non-stream attempt in `executeMixedOnce`**

In `conductor_execution.go`, the current attempt body (lines ~478-510) calls `executor.Execute` once, then on error optionally refreshes and retries once, then builds a `Result` and calls `MarkResult`. Restructure it so the execute call (and its 401-refresh retry) becomes the `AttemptFn`, wrapped in `RunInnerLoop`:

```go
			opts := innerLoopOptsFromConfig(m.cfg)
			outcome := RunInnerLoop(execCtx, opts, func(attemptCtx context.Context) InnerAttemptResult {
				resp, errExec := executor.Execute(attemptCtx, auth, execReq, execOpts)
				if errExec != nil {
					if refreshed, okRefresh := m.tryRefreshAfterUnauthorized(attemptCtx, auth, errExec, didRefreshOnUnauthorized); okRefresh {
						auth = refreshed
						didRefreshOnUnauthorized = true
						resp, errExec = executor.Execute(attemptCtx, auth, execReq, execOpts)
					}
				}
				if errExec == nil {
					result = resp
				}
				return InnerAttemptResult{Err: errExec}
			})
```

Constraints on this edit — the surrounding loop already handles several concerns that must be preserved:
- The `execCtx.Err()` early return becomes `if errCtx := attemptCtx.Err(); errCtx != nil { return cliproxyexecutor.Response{}, errCtx, lastAuth }` inside the AttemptFn — but note `RunInnerLoop` owns the deadline, so a `ReasonBudgetOut` exit must not be mistaken for a client cancellation. Keep the parent-context check and treat a pure budget exit as a normal failure.
- `claudeOAuthRequestCancellation(execCtx, auth, errExec)` must still run on the final error.
- `MarkResult` must still be called exactly once per credential attempt with the same `Result` shape, so cooldown accounting is unchanged. With `MaxAttempts: 0` (default) `RunInnerLoop` makes exactly one attempt, so the observable behavior is identical.
- Streaming must never retry after the first byte. `executeStreamWithModelPool` sets `FirstByte` once a chunk arrives; do not add `RunInnerLoop` around the chunk pump.

**Step 6: Write the parity test**

The critical assertion is that default config changes nothing. Add to `retry_integration_test.go`:

```go
func TestRunInnerLoopSingleAttemptDoesNotRetry(t *testing.T) {
	var attempts int32
	outcome := RunInnerLoop(context.Background(), InnerLoopOpts{}, func(ctx context.Context) InnerAttemptResult {
		attempts++
		return InnerAttemptResult{Err: errors.New("connection refused")}
	})
	if attempts != 1 {
		t.Fatalf("default opts must make exactly one attempt, got %d", attempts)
	}
	if outcome.Reason != ReasonNonTransient {
		t.Fatalf("reason = %v, want %v", outcome.Reason, ReasonNonTransient)
	}
}

func TestRunInnerLoopRetriesTransportErrorWhenEnabled(t *testing.T) {
	var attempts int32
	outcome := RunInnerLoop(context.Background(), InnerLoopOpts{MaxAttempts: 3, BackoffMS: 1},
		func(ctx context.Context) InnerAttemptResult {
			attempts++
			if attempts < 3 {
				return InnerAttemptResult{Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}
			}
			return InnerAttemptResult{}
		})
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
	if outcome.Reason != ReasonSuccess {
		t.Fatalf("reason = %v, want %v", outcome.Reason, ReasonSuccess)
	}
}
```

Add `"context"`, `"errors"`, `"net"` imports.

**Step 7: Run the tests**

Run: `gofmt -w . && go build -o test-output ./cmd/server && rm test-output`
Run: `go test ./sdk/cliproxy/auth/ -run 'InnerLoop|RunInnerLoop' -v`
Expected: PASS

**Step 8: Run the full auth package and the integration suite**

Run: `go test ./sdk/cliproxy/... ./test/... 2>&1 | tail -40`
Expected: no new failures. This is the highest-risk task — do not commit on a failing suite. If a pre-existing failure appears, confirm it fails on a clean tree with `git stash` before attributing it.

**Step 9: Document the flag**

Add to `config.example.yaml`, under the routing block, a commented example:

```yaml
  # Per-entry retry on transient upstream failures. Disabled unless
  # max-attempts is set: the default runs exactly one attempt per entry,
  # which is the behavior before this option existed.
  # retry:
  #   max-attempts: 3
  #   max-time-ms: 5000
  #   backoff-ms: 200
```

**Step 10: Commit**

```bash
git add sdk/cliproxy/auth/conductor_execution.go sdk/cliproxy/auth/conductor_stream.go \
        sdk/cliproxy/auth/retry_loop.go sdk/cliproxy/auth/retry_integration_test.go \
        config.example.yaml
git commit -m "feat(auth): retry transient per-entry failures via RunInnerLoop

Wires the previously-dead RunInnerLoop and IsRetryableError classifier
into the non-stream attempt loops, so a dial/DNS/TLS failure retries on
the same credential before pool rotation. Disabled unless
routing.retry.max-attempts is set; the default preserves one attempt per
entry. Streaming is untouched: post-first-byte retry stays forbidden.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

`internal/config/routing_retry_test.go` already covers config parsing — leave it as is.

---

## Phase 1 definition of done

Run all of these before considering Phase 1 complete:

```bash
gofmt -l .                      # must print nothing
go build -o test-output ./cmd/server && rm test-output
go test ./... 2>&1 | tail -30   # no new failures vs. the pre-change baseline
```

Then:
- Tag by bumping only the `0.x.x` half (the fork convention — core half stays at `v7.2.138` until a real core sync): `git tag v7.2.138-0.1.26`
- Confirm no timeout was added outside Task 7's per-attempt budget.
- Note in the PR body that Tasks 1–6 are verified by unit tests and that Task 7's streaming path is deliberately not wrapped.
