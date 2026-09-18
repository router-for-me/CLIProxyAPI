# Zero-Downtime Routing Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add four operator-facing capabilities on top of the existing routing strategy:
per-entry auto retry (transient only) with bounded wall-time, server-side force
failover when the budget is exhausted, a model-routing picker that filters LIVE
only and assigns priorities atomically, and a cleaner upstream-provider UI with
inline live status.

**Architecture:** Inner retry loop wraps the existing single-attempt path in
`sdk/cliproxy/auth/conductor_execution.go`. Helpers (`EntryBudget`, `IsRetryable`,
`NextBackoff`) live in `sdk/cliproxy/auth/` alongside `conductor_cooldown.go` so the
conductor stays a single coherent package. New PG columns land in
`upstream_provider_api_key_entries` (not `api_key_entries` — corrected from the
design doc) via a migration block in `internal/store/postgresstore.go`. The
picker endpoints project from `model_routing.priorities` (JSONB) — no new
table. The dashboard reuses the existing `providerKeyIsLive` helper; a new
shared poller drives the live dot everywhere.

**Tech Stack:** Go 1.26+, React + Vite, Postgres (PGSTORE), TanStack Query,
Tailwind tokens, gqlgen-style hand-rolled SQL, logrus.

**Reference design:** `docs/plans/2026-09-18-zero-downtime-routing-design.md`
(committed as `528a0039`). Reference code points (verify before each task —
`grep` may show them at slightly different line numbers after prior edits):

- `sdk/cliproxy/auth/conductor_execution.go` — inner loop insertion point.
- `sdk/cliproxy/auth/conductor_cooldown.go:1851` — `isRequestInvalidError`
  (inverse predicate to our new `IsRetryable`).
- `sdk/cliproxy/auth/conductor_cooldown.go:1370` — `isRequestScopedError`
  (related; do not generalize).
- `internal/store/postgresstore.go:1891` — entries table CREATE block (mirror
  shape when adding ALTER block).
- `internal/store/postgresstore.go:1563` — model_routing CREATE block (already
  has `priorities JSONB`).
- `internal/config/config_types.go:290` — `RoutingConfig` struct (add `Retry`
  next to `CooldownWait`).
- `internal/api/handlers/management/models_catalog_global.go:69` —
  `GlobalModelRoutingRequest` (mirror shape for picker DTOs).
- `web/dashboard/src/components/modelRouteProvider.js:17` —
  `providerKeyIsLive` (extend, do not duplicate).

**Working branch:** main. No worktree (per user instruction).

**Conventions:**

- `gofmt -w .` after every Go change (AGENTS.md).
- `go build -o test-output ./cmd/server && rm test-output` after any change
  (AGENTS.md — required for compile verification).
- Wrap `defer` errors with logrus; no `log.Fatal`.
- New tests go alongside the file under test (`*_test.go`).
- Dashboard tests use `node:test` (matches existing `*.test.js[x]` files).
- `docs/*` is gitignored; tracked plan files use `git add -f`.

**Known baseline failures (NOT regressions of this work):**

- 3 Claude header-fingerprint tests in `internal/runtime/executor/`.
- `TestInPlaceByteWritesAreReviewed` (`internal/util`).
- `TestBackupRoundTrip`, `TestUsageStoreBatchInsert` on `PGSTORE_TEST_DSN`.
  See memory `upstream-pool-routing-strategy` for stash-test verification.

---

## Phase A — Per-entry retry helpers + classifier

### Task A1: `ExitReason` type and helpers skeleton

**Files:**

- Create: `sdk/cliproxy/auth/retry_budget.go`
- Test: `sdk/cliproxy/auth/retry_budget_test.go`

**Step 1: Write failing tests**

```go
// retry_budget_test.go
package auth

import (
    "context"
    "testing"
    "time"
)

func TestExitReasonStringValues(t *testing.T) {
    cases := map[ExitReason]string{
        ReasonSuccess:         "success",
        ReasonNonTransient:    "non_transient",
        ReasonAttemptsOut:     "attempts_exhausted",
        ReasonBudgetOut:       "budget_exhausted",
        ReasonStreamStarted:   "stream_started",
        ReasonParentCtxDone:   "parent_ctx_done",
    }
    for r, want := range cases {
        if string(r) != want {
            t.Fatalf("ExitReason(%d) = %q, want %q", r, string(r), want)
        }
    }
}

func TestEntryBudgetReturnsSubContext(t *testing.T) {
    parent := context.Background()
    ctx, cancel, deadline, reason := EntryBudget(parent, EntryBudgetOpts{
        MaxTimeMS: 1000,
    })
    defer cancel()
    if ctx == nil || ctx == parent {
        t.Fatalf("expected derived context")
    }
    if reason != nil {
        t.Fatalf("unexpected exit reason on create: %v", reason)
    }
    if time.Until(deadline) > 1100*time.Millisecond {
        t.Fatalf("deadline too far in future: %v", time.Until(deadline))
    }
}

func TestEntryBudgetRespectsParentDeadline(t *testing.T) {
    parent, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
    defer cancel()
    _, c2, _, _ := EntryBudget(parent, EntryBudgetOpts{MaxTimeMS: 5000})
    defer c2()
    // deadline should be clamped to the parent deadline (~50ms), not 5s.
    time.Sleep(30 * time.Millisecond)
    if parent.Err() == nil {
        t.Fatalf("expected parent to expire soon")
    }
}
```

**Step 2: Run, expect compile failure**

```
cd /home/bilfid/projects/nixllm && go test ./sdk/cliproxy/auth/ -run TestExitReason -v
```

Expected: `undefined: ExitReason`, `undefined: EntryBudget`, `undefined: EntryBudgetOpts`.

**Step 3: Implement skeleton**

```go
// retry_budget.go
package auth

import (
    "context"
    "time"
)

type ExitReason string

const (
    ReasonSuccess       ExitReason = "success"
    ReasonNonTransient  ExitReason = "non_transient"
    ReasonAttemptsOut   ExitReason = "attempts_exhausted"
    ReasonBudgetOut     ExitReason = "budget_exhausted"
    ReasonStreamStarted ExitReason = "stream_started"
    ReasonParentCtxDone ExitReason = "parent_ctx_done"
)

type EntryBudgetOpts struct {
    MaxTimeMS uint32
}

func EntryBudget(parent context.Context, opts EntryBudgetOpts) (context.Context, context.CancelFunc, time.Time, *ExitReason) {
    if opts.MaxTimeMS == 0 {
        opts.MaxTimeMS = 5000
    }
    deadline := time.Now().Add(time.Duration(opts.MaxTimeMS) * time.Millisecond)
    if d, ok := parent.Deadline(); ok && d.Before(deadline) {
        deadline = d
    }
    ctx, cancel := context.WithDeadline(parent, deadline)
    return ctx, cancel, deadline, nil
}

// NextBackoff returns min(base * 2^(attempt-1), time.Until(deadline)/2).
func NextBackoff(deadline time.Time, baseMS uint32, attempt int) time.Duration {
    base := time.Duration(baseMS) * time.Millisecond
    if attempt < 1 {
        attempt = 1
    }
    b := base << (attempt - 1)
    cap := time.Until(deadline) / 2
    if cap < 0 {
        cap = 0
    }
    if b > cap {
        return cap
    }
    return b
}
```

**Step 4: Run, expect PASS**

```
cd /home/bilfid/projects/nixllm && go test ./sdk/cliproxy/auth/ -run 'TestExitReason|TestEntryBudget' -v
```

**Step 5: Verify compile**

```
cd /home/bilfid/projects/nixllm && gofmt -w sdk/cliproxy/auth/retry_budget.go sdk/cliproxy/auth/retry_budget_test.go && go build -o test-output ./cmd/server && rm test-output
```

**Step 6: Commit**

```
git add sdk/cliproxy/auth/retry_budget.go sdk/cliproxy/auth/retry_budget_test.go
git commit -m "feat(retry): add EntryBudget + NextBackoff helpers"
```

---

### Task A2: `IsRetryable` classifier

**Files:**

- Create: `sdk/cliproxy/auth/retry_classify.go`
- Test: `sdk/cliproxy/auth/retry_classify_test.go`

**Step 1: Write failing tests**

```go
// retry_classify_test.go
package auth

import (
    "errors"
    "fmt"
    "io"
    "net/http"
    "net/url"
    "strings"
    "testing"
)

type fakeResp struct {
    status int
    header http.Header
    body   string
}

func (f *fakeResp) StatusCode() int              { return f.status }
func (f *fakeResp) HeaderGet(k string) string    { return f.header.Get(k) }
func (f *fakeResp) BodyString() string           { return f.body }

func TestIsRetryableTransientStatus(t *testing.T) {
    for _, code := range []int{500, 502, 503, 504, 408, 429} {
        if !IsRetryableStatus(code) {
            t.Fatalf("status %d should be retryable", code)
        }
    }
}

func TestIsRetryableNonTransientStatus(t *testing.T) {
    for _, code := range []int{200, 201, 400, 401, 403, 404, 422} {
        if IsRetryableStatus(code) {
            t.Fatalf("status %d should NOT be retryable", code)
        }
    }
}

func TestIsRetryableNetworkErrors(t *testing.T) {
    cases := []error{
        io.EOF,
        io.ErrUnexpectedEOF,
        &net.OpError{Op: "dial", Err: errors.New("connection reset")},
        &url.Error{Op: "Post", Err: errors.New("EOF")},
    }
    for _, e := range cases {
        if !IsRetryableError(e) {
            t.Fatalf("expected retryable: %v", e)
        }
    }
}

func TestIsRetryableApplicationErrors(t *testing.T) {
    if IsRetryableError(errors.New("invalid_request_error: model not supported")) {
        t.Fatalf("application errors should not be retryable")
    }
}

func TestClassifyRetryHonorsRetryAfter(t *testing.T) {
    r := &fakeResp{status: 429, header: http.Header{"Retry-After": []string{"2"}}}
    if d := RetryAfterDuration(r, time.Now().Add(10*time.Second)); d < time.Second || d > 3*time.Second {
        t.Fatalf("Retry-After parse wrong: %v", d)
    }
}
```

**Step 2: Run, expect undefined**

```
cd /home/bilfid/projects/nixllm && go test ./sdk/cliproxy/auth/ -run TestIsRetryable -v
```

**Step 3: Implement**

```go
// retry_classify.go
package auth

import (
    "errors"
    "io"
    "net"
    "net/url"
    "strconv"
    "strings"
    "time"
)

// StatusResponder is the minimal surface needed from an HTTP response for
// retry classification. Implemented by *http.Response and by the executor's
// thin wrappers.
type StatusResponder interface {
    StatusCode() int
    HeaderGet(string) string
}

// DefaultRetryStatuses are the HTTP status codes that trigger a per-entry retry
// before pool failover.
var DefaultRetryStatuses = map[int]struct{}{
    408: {}, 429: {}, 500: {}, 502: {}, 503: {}, 504: {},
}

func IsRetryableStatus(code int) bool {
    _, ok := DefaultRetryStatuses[code]
    return ok
}

func IsRetryableError(err error) bool {
    if err == nil {
        return false
    }
    if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
        return true
    }
    var urlErr *url.Error
    if errors.As(err, &urlErr) {
        // url.Error wraps connection refused, DNS failures, TLS handshakes,
        // and EOF during body read — all transient.
        return true
    }
    var opErr *net.OpError
    if errors.As(err, &opErr) {
        return true
    }
    // Anything else (typed application errors, model-not-supported, etc.)
    // is application-level and must hard-failover.
    return false
}

// IsRetryable is the unified predicate. The caller passes the response (if
// any) and the error (if any). If either says retryable, retry.
func IsRetryable(resp StatusResponder, err error) bool {
    if err != nil && IsRetryableError(err) {
        return true
    }
    if resp != nil && IsRetryableStatus(resp.StatusCode()) {
        return true
    }
    return false
}

// RetryAfterDuration parses a Retry-After header (seconds or HTTP-date) and
// returns the duration to wait, capped to the supplied deadline.
func RetryAfterDuration(r StatusResponder, deadline time.Time) time.Duration {
    if r == nil {
        return 0
    }
    v := strings.TrimSpace(r.HeaderGet("Retry-After"))
    if v == "" {
        return 0
    }
    if secs, err := strconv.Atoi(v); err == nil {
        d := time.Duration(secs) * time.Second
        if remaining := time.Until(deadline); remaining > 0 && d > remaining {
            return remaining
        }
        return d
    }
    if t, err := http.ParseTime(v); err == nil {
        d := time.Until(t)
        if remaining := time.Until(deadline); remaining > 0 && d > remaining {
            return remaining
        }
        return d
    }
    _ = fmtError("unparseable Retry-After: %q", v)
    return 0
}

// fmtError exists to satisfy lint without importing fmt just for an
// unreachable branch; replaced with logrus in the conductor task.
func fmtError(format string, a ...interface{}) error {
    return fmtErr(format, a...)
}
```

(`retry_classify.go` imports `fmt` for the unparseable warning — drop the
placeholder `fmtError` and use `logrus.WithField(...).Errorf("...")` directly;
the test file doesn't import `fmt`, so keep `retry_classify.go` self-contained.)

**Step 4: Run, expect PASS**

```
cd /home/bilfid/projects/nixllm && go test ./sdk/cliproxy/auth/ -run 'TestIsRetryable|TestClassifyRetry' -v
```

**Step 5: Verify compile + format**

```
cd /home/bilfid/projects/nixllm && gofmt -w sdk/cliproxy/auth/retry_classify.go sdk/cliproxy/auth/retry_classify_test.go && go build -o test-output ./cmd/server && rm test-output
```

**Step 6: Commit**

```
git add sdk/cliproxy/auth/retry_classify.go sdk/cliproxy/auth/retry_classify_test.go
git commit -m "feat(retry): IsRetryable status+network classifier + Retry-After parser"
```

---

## Phase B — Inner retry loop in conductor

### Task B1: Extract single-attempt path to a helper

**Files:**

- Modify: `sdk/cliproxy/auth/conductor_execution.go`
- Test: `sdk/cliproxy/auth/conductor_execution_test.go` (existing — read first,
  no new tests this task)

**Step 1: Identify the single-attempt block**

Read `conductor_execution.go` and locate the existing `ExecuteOnce` (or
equivalent) call inside the per-entry path. The AGENTS.md rule says executor
helpers go in `helps/`, but the conductor lives in `sdk/cliproxy/auth/` — its
helpers stay alongside it in the same package (consistent with
`conductor_cooldown.go`). Skip extracting; **rewrite in place** using a small
inner closure.

**Step 2: Add the inner loop**

In `conductor_execution.go` `Execute`/`ExecuteStream`, wrap the existing
single-attempt call. Pseudo-code (match the existing signature):

```go
deadline, maxAttempts, baseBackoff := resolveRetryBudget(ctx, entry)
attempt := 0
for {
    attempt++
    if time.Until(deadline) <= 0 {
        return exit(ReasonBudgetOut, ...)
    }
    if attempt > maxAttempts {
        return exit(ReasonAttemptsOut, ...)
    }
    subCtx, cancel := context.WithDeadline(ctx, deadline)
    resp, err, firstByteSent := executeOnce(subCtx, entry, ...)
    cancel()
    if firstByteSent {
        return resp, err // post-stream — never retry
    }
    if !IsRetryable(resp, err) {
        return resp, err // ReasonNonTransient
    }
    if attempt >= maxAttempts {
        return exit(ReasonAttemptsOut, ...)
    }
    sleep := RetryAfterDuration(resp, deadline)
    if sleep == 0 {
        sleep = NextBackoff(deadline, baseBackoff, attempt)
    }
    select {
    case <-time.After(sleep):
    case <-ctx.Done():
        return exit(ReasonParentCtxDone, ...)
    case <-time.After(time.Until(deadline)):
        return exit(ReasonBudgetOut, ...)
    }
}
```

`resolveRetryBudget` reads the entry's per-entry overrides with the global
default as fallback:

```go
func resolveRetryBudget(ctx context.Context, e *entryRef) (time.Time, uint16, uint32) {
    cfg := getRetryConfig(ctx) // from sdk/cliproxy.Options or a getter
    maxMs := cfg.MaxTimeMS
    maxAttempts := cfg.MaxAttempts
    base := cfg.BackoffMS
    if e.RetryMaxTimeMS != nil {
        maxMs = *e.RetryMaxTimeMS
    }
    if e.RetryMaxAttempts != nil {
        maxAttempts = *e.RetryMaxAttempts
    }
    if e.RetryBackoffMS != nil {
        base = *e.RetryBackoffMS
    }
    return time.Now().Add(time.Duration(maxMs) * time.Millisecond), maxAttempts, base
}
```

(`entryRef` is whatever the conductor already uses to identify the candidate
entry — verify by reading `conductor_execution.go` before writing.)

**Step 3: Verify compile**

```
cd /home/bilfid/projects/nixllm && gofmt -w sdk/cliproxy/auth/conductor_execution.go && go build -o test-output ./cmd/server && rm test-output
```

**Step 4: Run existing conductor tests**

```
cd /home/bilfid/projects/nixllm && go test ./sdk/cliproxy/auth/ -run 'TestConductor' -count=1
```

Expected: existing conductor tests still pass (no behavior change at default
`MaxAttempts=0` or with budget large enough not to trigger).

**Step 5: Commit**

```
git add sdk/cliproxy/auth/conductor_execution.go
git commit -m "refactor(conductor): wrap single-attempt path in inner retry loop with budget enforcement"
```

---

### Task B2: Tests for inner loop reasons

**Files:**

- Create: `sdk/cliproxy/auth/conductor_retry_loop_test.go`

**Step 1: Write failing tests**

```go
package auth

import (
    "context"
    "errors"
    "net/http"
    "testing"
    "time"
)

type fakeResult struct {
    status int
    err    error
}

func TestInnerLoopBudgetExceededBeforeFirstAttempt(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
    defer cancel()
    time.Sleep(15 * time.Millisecond) // already past budget
    _, reason := runInnerLoopForTest(ctx, InnerLoopOpts{
        MaxAttempts: 3, MaxTimeMS: 1000, BackoffMS: 10,
        Attempt: func(ctx context.Context) (int, error) {
            return 200, nil
        },
    })
    if reason != ReasonBudgetOut {
        t.Fatalf("want budget_exhausted, got %v", reason)
    }
}

func TestInnerLoopAttemptsExhaustedOnTransient5xx(t *testing.T) {
    attempts := 0
    _, reason := runInnerLoopForTest(context.Background(), InnerLoopOpts{
        MaxAttempts: 3, MaxTimeMS: 10000, BackoffMS: 1,
        Attempt: func(ctx context.Context) (int, error) {
            attempts++
            return 503, nil
        },
    })
    if reason != ReasonAttemptsOut {
        t.Fatalf("want attempts_exhausted, got %v (attempts=%d)", reason, attempts)
    }
    if attempts != 3 {
        t.Fatalf("want 3 attempts, got %d", attempts)
    }
}

func TestInnerLoopStopsOnNonTransient4xx(t *testing.T) {
    attempts := 0
    _, reason := runInnerLoopForTest(context.Background(), InnerLoopOpts{
        MaxAttempts: 3, MaxTimeMS: 10000, BackoffMS: 1,
        Attempt: func(ctx context.Context) (int, error) {
            attempts++
            return 404, nil
        },
    })
    if reason != ReasonNonTransient {
        t.Fatalf("want non_transient, got %v", reason)
    }
    if attempts != 1 {
        t.Fatalf("want 1 attempt (no retry on 404), got %d", attempts)
    }
}

func TestInnerLoopHonorsRetryAfter(t *testing.T) {
    // Retry-After=0 means no sleep; verify attempts advance.
    attempts := 0
    _, reason := runInnerLoopForTest(context.Background(), InnerLoopOpts{
        MaxAttempts: 3, MaxTimeMS: 10000, BackoffMS: 1000, // base backoff big
        Attempt: func(ctx context.Context) (int, error) {
            attempts++
            if attempts == 1 {
                return 429, &http.Response{Header: http.Header{"Retry-After": []string{"0"}}}
            }
            return 200, nil
        },
    })
    if reason != ReasonSuccess {
        t.Fatalf("want success after 1 retry, got %v", reason)
    }
    if attempts != 2 {
        t.Fatalf("want 2 attempts, got %d", attempts)
    }
}

func TestInnerLoopSuccessFirstTry(t *testing.T) {
    attempts := 0
    _, reason := runInnerLoopForTest(context.Background(), InnerLoopOpts{
        MaxAttempts: 3, MaxTimeMS: 10000, BackoffMS: 1,
        Attempt: func(ctx context.Context) (int, error) {
            attempts++
            return 200, nil
        },
    })
    if reason != ReasonSuccess || attempts != 1 {
        t.Fatalf("want 1 attempt + success, got attempts=%d reason=%v", attempts, reason)
    }
}
```

`runInnerLoopForTest` is a thin test helper that wraps the inner loop logic
without touching real HTTP. Extract it to `conductor_retry_loop.go`
(test-only helper can live there, or in a `_test.go`-internal function).

**Step 2: Run, expect compile/undefined errors**

```
cd /home/bilfid/projects/nixllm && go test ./sdk/cliproxy/auth/ -run TestInnerLoop -v
```

**Step 3: Extract the inner loop into a testable function**

In `sdk/cliproxy/auth/retry_loop.go`:

```go
package auth

import (
    "context"
    "time"
)

type AttemptFn func(ctx context.Context) (status int, err error, firstByteSent bool)

type InnerLoopOpts struct {
    MaxAttempts uint16
    MaxTimeMS   uint32
    BackoffMS   uint32
    StatusFn    func(int) StatusResponder // optional; nil => no header reads
}

func RunInnerLoop(parent context.Context, opts InnerLoopOpts, attempt AttemptFn) (lastStatus int, reason *ExitReason) {
    deadline := time.Now().Add(time.Duration(opts.MaxTimeMS) * time.Millisecond)
    if d, ok := parent.Deadline(); ok && d.Before(deadline) {
        deadline = d
    }
    successes := 0
    var lastSt int
    var lastErr error
    for i := uint16(1); ; i++ {
        if time.Until(deadline) <= 0 {
            r := ReasonBudgetOut
            return lastSt, &r
        }
        if i > opts.MaxAttempts {
            r := ReasonAttemptsOut
            return lastSt, &r
        }
        subCtx, cancel := context.WithDeadline(parent, deadline)
        status, err, firstByte := attempt(subCtx)
        cancel()
        lastSt = status
        lastErr = err
        if firstByte {
            r := ReasonStreamStarted
            return status, &r
        }
        if !IsRetryable(asResponder(status, opts.StatusFn), err) {
            r := ReasonNonTransient
            return status, &r
        }
        if err == nil && status >= 200 && status < 300 {
            r := ReasonSuccess
            return status, &r
        }
        if i == opts.MaxAttempts {
            r := ReasonAttemptsOut
            return status, &r
        }
        sleep := NextBackoff(deadline, opts.BackoffMS, int(i))
        select {
        case <-time.After(sleep):
        case <-parent.Done():
            r := ReasonParentCtxDone
            return status, &r
        case <-time.After(time.Until(deadline)):
            r := ReasonBudgetOut
            return status, &r
        }
        _ = successes
    }
    _ = lastErr
}
```

(Final `_ = successes` / `_ = lastErr` are dead-store silencers — drop them
once the function is wired into the real conductor and they become useful.)

**Step 4: Make `conductor_execution.go` call `RunInnerLoop`**

Replace the inline pseudo-code from Task B1 with a call to `RunInnerLoop`,
mapping the conductor's `entry` to `InnerLoopOpts` and the per-attempt body
to `AttemptFn`. The conductor keeps its existing return shape; `RunInnerLoop`
returns the last status + reason, and the conductor maps reason → existing
return contract.

**Step 5: Run, expect PASS**

```
cd /home/bilfid/projects/nixllm && gofmt -w sdk/cliproxy/auth/ && go test ./sdk/cliproxy/auth/ -run TestInnerLoop -v && go build -o test-output ./cmd/server && rm test-output
```

**Step 6: Commit**

```
git add sdk/cliproxy/auth/retry_loop.go sdk/cliproxy/auth/conductor_retry_loop_test.go sdk/cliproxy/auth/conductor_execution.go
git commit -m "feat(conductor): inner retry loop with budget + tests for all exit reasons"
```

---

## Phase C — PG migration + entry column projection + config round-trip

### Task C1: Migration for retry columns on `upstream_provider_api_key_entries`

**Files:**

- Modify: `internal/store/postgresstore.go` (add ALTER block after the
  entries table CREATE block at line ~1891)
- Test: `internal/store/pg_upstream_providers_test.go` (extend existing
  `TestUpstreamProviderStoreRoutingStrategyAndEntryPriorityRoundTrip` — read
  first to mirror its shape)

**Step 1: Write failing test**

```go
// In pg_upstream_providers_test.go — add at end of file
func TestUpstreamProviderStoreRetryColumnsRoundTrip(t *testing.T) {
    // Mirror the existing routing-strategy round-trip test shape; create a
    // provider with an entry that sets all three retry columns, fetch, mutate,
    // unset, refetch.
    ctx := context.Background()
    s, cleanup := setupTestPGStore(t)
    defer cleanup()

    maxAttempts := uint16(5)
    maxTime := uint32(8000)
    backoff := uint32(300)
    created, err := s.CreateUpstreamProvider(ctx, &store.UpstreamProvider{
        ProviderType: "claude",
        Name:         ptr("retry-rt"),
        APIKeyEntries: []store.UpstreamProviderAPIKey{{
            KeyPreview:    "sk-retry-1",
            RetryMaxAttempts: &maxAttempts,
            RetryMaxTimeMS:   &maxTime,
            RetryBackoffMS:   &backoff,
        }},
    })
    // ...assertions...
    _ = created
}
```

(Read the existing test file to mirror its exact setup/teardown style — likely
`newTestPGStoreWithSchema` or similar. Use `t.Skip` when PG is unavailable so
this isn't a `go test ./...` blocker.)

**Step 2: Run, expect compile failure (no `RetryMaxAttempts` field)**

```
cd /home/bilfid/projects/nixllm && go test ./internal/store/ -run TestUpstreamProviderStoreRetry -v
```

**Step 3: Add `Retry*` fields to `UpstreamProviderAPIKey` struct**

In `internal/store/pg_upstream_providers.go` (struct definition near the
`APIKeyEntries` field), add:

```go
type UpstreamProviderAPIKey struct {
    // ...existing fields...
    RetryMaxAttempts *uint16 `json:"retry_max_attempts,omitempty"`
    RetryMaxTimeMS   *uint32 `json:"retry_max_time_ms,omitempty"`
    RetryBackoffMS   *uint32 `json:"retry_backoff_ms,omitempty"`
}
```

**Step 4: Add the migration ALTER block in `postgresstore.go`**

Right after the existing entries table CREATE block:

```go
if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
    `ALTER TABLE %s
        ADD COLUMN IF NOT EXISTS retry_max_attempts SMALLINT NULL,
        ADD COLUMN IF NOT EXISTS retry_max_time_ms INTEGER NULL,
        ADD COLUMN IF NOT EXISTS retry_backoff_ms INTEGER NULL`,
    s.fullTableName(s.cfg.UpstreamProviderEntriesTable),
)); err != nil {
    return fmt.Errorf("postgres store: alter upstream_provider_api_key_entries retry columns: %w", err)
}
```

**Step 5: Update INSERT/UPDATE/SELECT for the new columns**

Read the existing `CreateUpstreamProvider` + `UpdateUpstreamProvider` +
`scanUpstreamProviderEntry` in `pg_upstream_providers.go` and add the three
columns. Use `nullableUint16`/`nullableUint32` helpers if they exist; if not,
add a small helper:

```go
func nullableUint16Ptr(p *uint16) any {
    if p == nil { return nil }
    return int(*p)
}
```

(`SELECT` reads via `pq.NullInt64` or the existing pattern in this file.)

**Step 6: Verify**

```
cd /home/bilfid/projects/nixllm && gofmt -w internal/store/ && go test ./internal/store/ -run TestUpstreamProviderStore -count=1 -v && go build -o test-output ./cmd/server && rm test-output
```

**Step 7: Commit**

```
git add internal/store/postgresstore.go internal/store/pg_upstream_providers.go internal/store/pg_upstream_providers_test.go
git commit -m "feat(store): retry_max_attempts/retry_max_time_ms/retry_backoff_ms on upstream_provider_api_key_entries"
```

---

### Task C2: Config types + YAML round-trip

**Files:**

- Modify: `internal/config/config_types.go`
- Modify: `internal/config/strategy.go` (or new file `internal/config/retry.go`
  — pick whichever has the `NormalizePoolRoutingStrategy` siblings)
- Test: `internal/config/retry_test.go` (mirror `routing_cooldown_wait_test.go`)

**Step 1: Write failing test**

```go
// retry_test.go
package config

import (
    "testing"
)

func TestRetryConfigDefaults(t *testing.T) {
    var cfg RetryConfig
    if got := cfg.MaxAttemptsOrDefault(); got != 3 {
        t.Fatalf("default MaxAttempts = %d, want 3", got)
    }
    if got := cfg.MaxTimeMSOrDefault(); got != 5000 {
        t.Fatalf("default MaxTimeMS = %d, want 5000", got)
    }
}

func TestRetryConfigYAMLRoundTrip(t *testing.T) {
    yamlIn := `routing:
  retry:
    max-attempts: 5
    max-time-ms: 8000
    backoff-ms: 250
    retry-on: [500, 502, 429]
`
    cfg, err := ParseConfigBytes([]byte(yamlIn))
    if err != nil {
        t.Fatal(err)
    }
    if cfg.Routing.Retry == nil {
        t.Fatal("Retry is nil")
    }
    if cfg.Routing.Retry.MaxAttempts != 5 { ... }
    // ...assert all fields...
}
```

**Step 2: Implement**

```go
// config_types.go — add to RoutingConfig
type RetryConfig struct {
    MaxAttempts uint16 `yaml:"max-attempts,omitempty"`
    MaxTimeMS   uint32 `yaml:"max-time-ms,omitempty"`
    BackoffMS   uint32 `yaml:"backoff-ms,omitempty"`
    RetryOn     []int  `yaml:"retry-on,omitempty"`
}

func (r *RetryConfig) MaxAttemptsOrDefault() uint16 {
    if r == nil || r.MaxAttempts == 0 { return 3 }
    return r.MaxAttempts
}
// ...similarly for MaxTimeMSOrDefault (5000), BackoffMSOrDefault (200)...
```

**Step 3: Run, expect PASS**

```
cd /home/bilfid/projects/nixllm && gofmt -w internal/config/ && go test ./internal/config/ -run TestRetry -v && go build -o test-output ./cmd/server && rm test-output
```

**Step 4: Commit**

```
git add internal/config/config_types.go internal/config/retry.go internal/config/retry_test.go
git commit -m "feat(config): RoutingConfig.Retry with sane defaults"
```

---

### Task C3: Snapshot projection round-trip + normalized-import projection

**Files:**

- Modify: `internal/configsnapshot/`
- Modify: `internal/store/pg_normalized_import.go` (project retry columns)

**Step 1: Read existing snapshot YAML round-trip**

`internal/configsnapshot/MarshalYAML`/`UnmarshalYAML` — extend to emit
`retry: { max-attempts, max-time-ms, backoff-ms }` per entry. Tests in
`internal/configsnapshot/` should cover.

**Step 2: Extend `pg_normalized_import.go` projection**

The same INSERT statement that projects `priority`, `disabled`,
`routing_strategy`, `circuit_breaker`, `proxy_pool_id` should also project
the three retry columns from `UpstreamProviderAPIKey`.

**Step 3: Verify**

```
cd /home/bilfid/projects/nixllm && go test ./internal/configsnapshot/ ./internal/store/ -count=1 -v && go build -o test-output ./cmd/server && rm test-output
```

**Step 4: Commit**

```
git add internal/configsnapshot/ internal/store/pg_normalized_import.go
git commit -m "feat(snapshot): round-trip retry config + project retry columns in normalized import"
```

---

## Phase D — Picker endpoints

### Task D1: `isProviderRowLive` server helper

**Files:**

- Create: `internal/api/handlers/management/routing_models.go`
- Test: `internal/api/handlers/management/routing_models_test.go`

**Step 1: Write failing test**

```go
// routing_models_test.go
package management

import (
    "testing"
)

func TestIsProviderRowLive(t *testing.T) {
    cases := []struct {
        name string
        row  string
        live []string
        cooldown map[string]bool
        want bool
    }{
        {"compound match", "claude:42:key-7", []string{"claude:42:key-7"}, nil, true},
        {"bare channel match", "claude:42", []string{"claude"}, nil, true},
        {"cooldown blocks", "claude:42", []string{"claude"}, map[string]bool{"claude:42": true}, false},
        {"no evidence", "claude:42", nil, nil, false},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            got := IsProviderRowLive(tc.row, tc.live, tc.cooldown)
            if got != tc.want {
                t.Fatalf("got %v want %v", got, tc.want)
            }
        })
    }
}
```

**Step 2: Implement**

```go
// routing_models.go
package management

import "strings"

// IsProviderRowLive is the single source of truth for picker LIVE filter.
// Mirrors web/dashboard/src/components/modelRouteProvider.js:providerKeyIsLive.
// Keep the two in sync; if you change one, change both + extend both test suites.
func IsProviderRowLive(row string, liveEvidence []string, cooldown map[string]bool) bool {
    if row == "" { return false }
    if cooldown[row] { return false }
    row = strings.ToLower(strings.TrimSpace(row))
    for _, k := range liveEvidence {
        if strings.EqualFold(strings.TrimSpace(k), row) {
            return true
        }
    }
    // Bare channel fallback: "claude:42" is live if "claude" is in live evidence.
    if idx := strings.LastIndex(row, ":"); idx > 0 {
        bare := row[:idx]
        for _, k := range liveEvidence {
            if strings.EqualFold(strings.TrimSpace(k), bare) {
                return true
            }
        }
    }
    return false
}
```

**Step 3: Run + commit**

```
cd /home/bilfid/projects/nixllm && gofmt -w internal/api/handlers/management/ && go test ./internal/api/handlers/management/ -run TestIsProviderRowLive -v && go build -o test-output ./cmd/server && rm test-output
git add internal/api/handlers/management/routing_models.go internal/api/handlers/management/routing_models_test.go
git commit -m "feat(management): isProviderRowLive single-source-of-truth for picker filter"
```

---

### Task D2: `GET /v0/management/model-routing/picker?model=X`

**Files:**

- Modify: `internal/api/handlers/management/routing_models.go`
- Modify: `internal/api/handlers/management/handler.go` (route registration —
  read first)
- Test: `internal/api/handlers/management/routing_models_test.go`

**Step 1: Write failing test**

```go
func TestPickerEndpointReturnsLiveAndStale(t *testing.T) {
    // Spin up the management Handler with a fake auth manager + a fake
    // model_routing row. Read existing endpoint tests to mirror setup.
    // Assert: response.live excludes stale rows; response.pinned includes
    // pinned entries with their priorities; response.live candidates include
    // suggested_priority = max(existing pinned) + 1.
}
```

**Step 2: Implement endpoint**

```go
// routing_models.go
type PickerResponse struct {
    Model   string                 `json:"model"`
    Live    []PickerCandidate      `json:"live"`
    Pinned  []PickerPinned         `json:"pinned"`
    Stale   []PickerCandidate      `json:"stale"`
}

type PickerCandidate struct {
    ProviderKey      string `json:"provider_key"`
    Name             string `json:"name"`
    SuggestedPriority int   `json:"suggested_priority"`
    Models           []string `json:"models,omitempty"`
}

type PickerPinned struct {
    ProviderKey    string `json:"provider_key"`
    Name           string `json:"name"`
    Priority       int    `json:"priority"`
    IsLive         bool   `json:"is_live"`
    CooldownUntil  *time.Time `json:"cooldown_until,omitempty"`
}

func (h *Handler) GetModelRoutingPicker(c *gin.Context) {
    model := c.Query("model")
    if model == "" {
        c.JSON(400, gin.H{"error": "model required"})
        return
    }
    // 1. Load model_routing row for this model (already exists).
    row, err := h.store.GetModelRouting(c, model)
    if err != nil { /* 503 without PG, 500 otherwise */ }
    // 2. Load live evidence from authManager (existing helper).
    live := h.authManager.LiveProviderKeys()
    cooldown := h.authManager.CooldownStateSnapshot()
    // 3. Load all upstream providers, partition by IsProviderRowLive.
    providers := h.store.ListProviders(c) // existing method
    // 4. Compute suggested priority for each live candidate.
    maxPinned := 0
    for _, p := range row.Priorities { if p.Priority > maxPinned { maxPinned = p.Priority } }
    suggested := maxPinned + 1
    if suggested < 10 { suggested = 10 } // floor at 10 when no pins
    // 5. Build response.
    // ...
}
```

(Wire the live-evidence and cooldown helpers per their existing APIs — read
`internal/api/handlers/management/upstream_providers_*.go` for the existing
pattern before writing.)

**Step 3: Register the route**

In `handler.go` `RegisterRoutes` (or wherever `routing_models.go` style routes
register), add:

```go
mgmt.GET("/model-routing/picker", h.GetModelRoutingPicker)
```

**Step 4: Run + commit**

```
cd /home/bilfid/projects/nixllm && gofmt -w internal/api/handlers/management/ && go test ./internal/api/handlers/management/ -run TestPicker -v && go build -o test-output ./cmd/server && rm test-output
git add internal/api/handlers/management/
git commit -m "feat(management): GET /v0/management/model-routing/picker with LIVE filter + suggested_priority"
```

---

### Task D3: `POST /v0/management/model-routing/pin`

**Files:**

- Modify: `internal/api/handlers/management/routing_models.go`
- Test: `internal/api/handlers/management/routing_models_test.go`

**Step 1: Write failing test**

```go
func TestPinAssignsMaxPlusOne(t *testing.T) {
    // Seed model_routing with priorities=[10]. Pin another entry. Expect 11.
}

func TestPinDefaultsToTenWhenEmpty(t *testing.T) {
    // Seed model_routing with priorities=[]. Pin. Expect 10.
}

func TestPinConcurrentAtomic(t *testing.T) {
    // Two concurrent pins against the same model with empty priorities.
    // Expect: one gets 10, the other gets 11. (Use sync.WaitGroup + real DB.)
}
```

**Step 2: Implement endpoint**

```go
type PinRequest struct {
    Model       string `json:"model"`
    ProviderKey string `json:"provider_key"`
    Force       bool   `json:"force"`
}

type PinResponse struct {
    Priority    int    `json:"priority"`
    EntryID     string `json:"entry_id"`
    WasExisting bool   `json:"was_existing"`
}

func (h *Handler) PostModelRoutingPin(c *gin.Context) {
    var req PinRequest
    if err := c.BindJSON(&req); err != nil { c.JSON(400, gin.H{"error": err.Error()}); return }
    // LIVE filter unless req.Force.
    if !req.Force && !IsProviderRowLive(req.ProviderKey, h.authManager.LiveProviderKeys(), h.authManager.CooldownStateSnapshot()) {
        c.JSON(409, gin.H{"error": "provider not LIVE; pass force=true to pin anyway"})
        return
    }
    // Atomic MAX(priority)+1 in a single tx:
    result, err := h.store.PinModelRoutingEntry(c, req.Model, req.ProviderKey)
    if err != nil { c.JSON(500, gin.H{"error": err.Error()}); return }
    c.JSON(200, result)
}
```

The SQL (in `internal/store/` — add a new method `PinModelRoutingEntry`):

```sql
-- Inside a single tx with SERIALIZABLE or row lock on model_routing.id
SELECT priorities FROM model_routing WHERE id = $1 FOR UPDATE;
-- compute max+1 (default 10) in Go
UPDATE model_routing
   SET priorities = priorities || jsonb_build_array(jsonb_build_object('provider_key', $2, 'priority', $3)),
       updated_at = NOW()
 WHERE id = $1;
-- Return $3 as Priority, generated entry_id, was_existing = (already in priorities).
```

**Step 3: Run + commit**

```
cd /home/bilfid/projects/nixllm && gofmt -w internal/api/handlers/management/ internal/store/ && go test ./internal/api/handlers/management/ ./internal/store/ -run 'TestPin' -v && go build -o test-output ./cmd/server && rm test-output
git add internal/api/handlers/management/ internal/store/
git commit -m "feat(management): POST /v0/management/model-routing/pin with atomic MAX(priority)+1"
```

---

## Phase E — React Picker + liveStatus helpers

### Task E1: Shared `liveStatus` API + TanStack Query hook

**Files:**

- Create: `web/dashboard/src/api/liveStatus.js`
- Create: `web/dashboard/src/api/liveStatus.test.js`
- Create: `web/dashboard/src/api/modelRouting.js`

**Step 1: Write failing tests**

```js
// liveStatus.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { providerKeyIsLive } from './liveStatus.js';

test('providerKeyIsLive exact compound match', () => {
  assert.equal(providerKeyIsLive('claude:42:key-7', ['claude:42:key-7']), true);
});
test('providerKeyIsLive bare channel fallback', () => {
  assert.equal(providerKeyIsLive('claude:42', ['claude']), true);
});
test('providerKeyIsLive cooldown blocks', () => {
  assert.equal(providerKeyIsLive('claude:42', ['claude'], { 'claude:42': true }), false);
});
test('providerKeyIsLive no evidence', () => {
  assert.equal(providerKeyIsLive('claude:42', []), false);
});
```

**Step 2: Implement**

```js
// liveStatus.js — re-exports the existing helper for back-compat and adds the
// cooldown layer. Keep semantics in sync with the Go isProviderRowLive.
import { providerKeyIsLive as bareMatch } from '../components/modelRouteProvider.js';

export function providerKeyIsLive(row, liveEvidence, cooldown) {
  if (!row) return false;
  if (cooldown && cooldown[row]) return false;
  return bareMatch(row, liveEvidence || []);
}

export async function fetchLiveStatus() {
  const r = await fetch('/v0/management/upstream-providers/live-status', { credentials: 'include' });
  if (!r.ok) throw new Error(`live-status ${r.status}`);
  return r.json();
}
```

(For TanStack Query hook, follow the existing pattern in
`web/dashboard/src/api/` — likely already a queryClient helper.)

**Step 3: Run + commit**

```bash
cd /home/bilfid/projects/nixllm/web/dashboard && npm test -- --test-only liveStatus.test.js
cd /home/bilfid/projects/nixllm && git add web/dashboard/src/api/liveStatus.js web/dashboard/src/api/liveStatus.test.js
git commit -m "feat(dashboard): shared liveStatus helper + fetchLiveStatus client"
```

---

### Task E2: `Picker.jsx` four-quadrant component

**Files:**

- Create: `web/dashboard/src/pages/model-routing/Picker.jsx`
- Create: `web/dashboard/src/pages/model-routing/Picker.test.jsx`

**Step 1: Write failing test**

```jsx
// Picker.test.jsx — use @testing-library/react + node:test (existing pattern).
import { render, screen } from '@testing-library/react';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Picker } from './Picker.jsx';

test('Picker renders four quadrants', () => {
  render(<Picker model="gpt-4o"
    live={[{ provider_key: 'openai:1', suggested_priority: 11 }]}
    pinned={[{ provider_key: 'openai:2', priority: 10, is_live: true }]}
    stale={[]}
  />);
  assert(screen.getByTestId('picker-pinned'));
  assert(screen.getByTestId('picker-live'));
  assert(screen.getByTestId('picker-stale'));
  assert(screen.getByTestId('picker-hidden-toggle'));
});
```

**Step 2: Implement**

Four-quadrant layout: Pinned / Live / Stale / Hidden. `Pin → 11` button per
candidate. After pin, optimistic update + invalidate picker query. Use the
existing dashboard primitives (button, badge, tooltip).

**Step 3: Run + commit**

```bash
cd /home/bilfid/projects/nixllm/web/dashboard && npm test -- --test-only Picker.test.jsx
cd /home/bilfid/projects/nixllm && git add web/dashboard/src/pages/model-routing/
git commit -m "feat(dashboard): model-routing Picker with LIVE filter + auto-incremental priority"
```

---

## Phase F — Upstream Provider UI cleanup

### Task F1: `StatusDot` shared component

**Files:**

- Create: `web/dashboard/src/pages/upstream-providers/components/StatusDot.jsx`
- Create: `web/dashboard/src/pages/upstream-providers/components/StatusDot.test.jsx`

**Step 1: Test + implement**

Single source of truth for the dot. Reads `providerKeyIsLive` + a `reason`
string from the live-status payload. Colors: `emerald-500` LIVE,
`amber-500` COOLDOWN, `rose-500` BREAKER_OPEN, `zinc-400` STALE/UNKNOWN.

**Step 2: Commit**

```bash
git add web/dashboard/src/pages/upstream-providers/components/StatusDot.jsx web/dashboard/src/pages/upstream-providers/components/StatusDot.test.jsx
git commit -m "feat(dashboard): shared StatusDot for upstream-provider surfaces"
```

---

### Task F2: `EntryRow` compact row + integration into editor

**Files:**

- Create: `web/dashboard/src/pages/upstream-providers/components/EntryRow.jsx`
- Create: `web/dashboard/src/pages/upstream-providers/components/EntryRow.test.jsx`
- Modify: `web/dashboard/src/pages/upstream-provider-editor/EntriesEditor.jsx`
  (feature-flagged via `VITE_FEATURE_COMPACT_ENTRY_ROW`)

**Step 1: Test + implement**

Dense row columns: `key preview · live dot · last_used · cooldown_until ·
retry_attempts/used · priority · actions`. Retry fields default to
"use global default" toggle (off by default).

**Step 2: Feature flag integration**

In `EntriesEditor.jsx`, branch on `import.meta.env.VITE_FEATURE_COMPACT_ENTRY_ROW`:

```jsx
{import.meta.env.VITE_FEATURE_COMPACT_ENTRY_ROW
  ? <EntryRow ... />
  : <LegacyTabs ... />}
```

**Step 3: Run editor regression**

```bash
cd /home/bilfid/projects/nixllm/web/dashboard && npm test -- --test-only editor.test.js
```

Expected: existing 160 tests still pass.

**Step 4: Commit**

```bash
git add web/dashboard/src/pages/upstream-providers/ web/dashboard/src/pages/upstream-provider-editor/
git commit -m "feat(dashboard): compact EntryRow behind feature flag for upstream-provider editor"
```

---

### Task F3: Shared live-status poller + list-page filters + bulk actions + sticky footer

**Files:**

- Modify: `web/dashboard/src/pages/upstream-providers/UpstreamProvidersPage.jsx`
- Create: `web/dashboard/src/pages/upstream-providers/components/BulkActions.jsx`
- Modify: `web/dashboard/src/pages/upstream-providers/index.jsx` (read first —
  may be the export entry)

**Step 1: Implement**

- Replace per-page polls with `useLiveStatus()` (Phase E1 hook).
- Add filter bar (LIVE/COOLDOWN/STALE), search input (key preview/model/proxy
  pool).
- Add multi-select + `BulkActions` component: "Pin all LIVE to model X" /
  "Set retry budget on selected".
- Sticky footer with save state ("Saved · Ns ago" / "Unsaved changes").

**Step 2: Test**

Add `UpstreamProvidersPage.test.jsx` (new) — render with stubbed data, assert
filter chips render + bulk action button is disabled when no rows selected.

**Step 3: Commit**

```bash
git add web/dashboard/src/pages/upstream-providers/
git commit -m "feat(dashboard): provider list filters, bulk actions, shared live-status poller, sticky save footer"
```

---

## Phase G — Documentation

### Task G1: Update developer docs + cross-reference

**Files:**

- Modify: `web/dashboard/src/api/developerDocs.js` (add the two new endpoints)

**Step 1: Add entries**

```js
{
  method: 'GET',
  path: '/v0/management/model-routing/picker',
  summary: 'List picker candidates for a model with LIVE filter.',
  params: [{ name: 'model', in: 'query', required: true }],
  response: PickerResponseShape,
},
{
  method: 'POST',
  path: '/v0/management/model-routing/pin',
  summary: 'Pin a provider to a model. Priority auto-assigned as MAX(existing)+1 (default 10).',
  body: PinRequestShape,
  response: PinResponseShape,
},
```

**Step 2: Cross-reference 2026-09-11 plan**

Append a "Supersedes" note to `docs/plans/2026-09-11-omniroute-incremental-routing-design.md`:

```markdown
## Superseded by 2026-09-18-zero-downtime-routing-design.md

G5's cooldown_wait budget is reused as the global default for the new
`routing.retry.*` block; per-entry overrides live on the api_key_entries
table. G2/G3/G4/G6 remain orthogonal.
```

**Step 3: Commit**

```bash
git add -f docs/plans/2026-09-11-omniroute-incremental-routing-design.md web/dashboard/src/api/developerDocs.js
git commit -m "docs: cross-reference zero-downtime design from 2026-09-11 plan + add picker endpoints to developer docs"
```

---

## Final verification

After all phases:

```bash
cd /home/bilfid/projects/nixllm
gofmt -w .
go build -o test-output ./cmd/server && rm test-output
go test ./... -count=1
cd web/dashboard && npm test && npm run build
make dash-embed
```

Expected:

- `gofmt` clean.
- `go build` clean.
- `go test ./...` — pre-existing baseline failures unchanged
  (3 Claude header-fingerprint, `TestInPlaceByteWritesAreReviewed`,
  `TestBackupRoundTrip`, `TestUsageStoreBatchInsert`); no new failures.
- `npm test` — all new + existing React tests pass.
- `npm run build` — clean Vite build.
- `make dash-embed` — embeds the new SPA into `internal/dashboardasset/`.

## Risk notes

- **Phase B (conductor loop)** is the highest-risk change. It modifies the
  hot path. Mitigation: extract to a testable `RunInnerLoop` helper in Task
  B2 first; the existing 494-test auth suite (per memory
  `upstream-pool-routing-strategy`) is the regression net.
- **Phase C1 (migration)** is additive (`ADD COLUMN IF NOT EXISTS`) — safe to
  ship to production. Backfill is unnecessary because the columns are
  nullable; the existing code path reads the global default when the column
  is NULL.
- **Phase D3 (concurrent pin)** relies on the `SELECT ... FOR UPDATE` row
  lock. If the existing `model_routing` access path does not use row locks,
  add them in this task — do not assume the wider store is concurrency-safe.
- **Phase F2 (entry row feature flag)** is the only UI change that touches
  production layouts without a flag. The "Advanced (tabs)" fallback keeps
  power users functional while parity tests run.

## Reference

- Design: `docs/plans/2026-09-18-zero-downtime-routing-design.md`
- Memory: `upstream-pool-routing-strategy`, `routing-picker-key-alignment`,
  `proxy-pools-design-status`, `pg-first-control-plane-status`.