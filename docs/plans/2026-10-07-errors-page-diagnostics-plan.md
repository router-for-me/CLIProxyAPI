# Errors Page Diagnostics Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Enrich the dashboard Errors page with server-side error classification, aggregation, and pattern grouping so operators can analyse *why* failures happen, not just inspect one row at a time.

**Architecture:** Two new `usage_errors` columns (`error_class`, `error_fingerprint`) are computed at flush time by a new pure package `internal/store/errorclass` (body → keyword → status resolution; message normalisation before hashing). Three read-only aggregation endpoints under `usage-stats/errors/*` expose summary / groups / timeline, built on the existing `buildWhereClause` filter shape. The dashboard renders them in a new "Error Patterns" tab, an enriched KPI strip, a Class column, and a richer detail modal.

**Tech Stack:** Go 1.26, `gjson` (already a dependency) for body parsing, Postgres (pgx via `database/sql`), React + Vite dashboard.

**Design doc:** `docs/plans/2026-10-07-errors-page-diagnostics-design.md`

**Convention reminders (from AGENTS.md):**
- No `log.Fatal`/`log.Fatalf`; use logrus.
- Run `gofmt -w .` after Go edits.
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`.
- Avoid panics in HTTP handlers; return errors + meaningful status codes.
- `internal/store/runtimeconfig` depends on `store` + `configsnapshot`; never make `store` depend on `configsnapshot`.
- Comments in English; keep user-visible strings in the language the file/area already uses.

**Baseline note:** `internal/store` and `internal/api/handlers/management` build and test green at `cca5c152` in this worktree. Every task below must keep them green.

---

## Phase 1 — Classification package

### Task 1: `errorclass.Classify`

**Files:**
- Create: `internal/store/errorclass/classify.go`
- Test: `internal/store/errorclass/classify_test.go`

**Step 1: Write the failing test**

```go
package errorclass

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"body type rate limit wins over status", 400, `{"error":{"type":"rate_limit_error"}}`, ClassRateLimit},
		{"body code insufficient quota", 429, `{"error":{"code":"insufficient_quota"}}`, ClassQuota},
		{"body auth error", 200, `{"error":{"type":"authentication_error"}}`, ClassAuth},
		{"status 429", 429, ``, ClassRateLimit},
		{"status 401", 401, `not json`, ClassAuth},
		{"status 403", 403, ``, ClassPermission},
		{"status 404", 404, ``, ClassNotFound},
		{"status 400", 400, ``, ClassInvalidRequest},
		{"status 408", 408, ``, ClassTimeout},
		{"status 504", 504, ``, ClassTimeout},
		{"status 503", 503, ``, ClassServerError},
		{"keyword timeout", 500, `upstream connection timeout`, ClassTimeout},
		{"keyword overloaded", 529, `server overloaded`, ClassOverloaded},
		{"keyword content filter", 400, `blocked by content filter policy`, ClassContentFilter},
		{"unknown falls back to other", 418, `teapot`, ClassOther},
		{"empty body empty status", 0, ``, ClassOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.status, tc.body); got != tc.want {
				t.Fatalf("Classify(%d, %q) = %q, want %q", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

func TestClassifyNeverPanics(t *testing.T) {
	for _, body := range []string{"", "{", "[]", "{not json", `{"error":null}`} {
		_ = Classify(500, body) // must not panic
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/store/errorclass/ -run TestClassify -v`
Expected: FAIL — package/identifiers undefined.

**Step 3: Write minimal implementation**

```go
// Package errorclass maps an upstream failure to a stable class slug. It is
// pure: no DB, no globals, no errors — an unparseable body falls through to the
// status/keyword arms and finally to ClassOther so classification can never
// fail a flush or drop a row.
package errorclass

import (
	"strings"

	"github.com/tidwall/gjson"
)

// Stable class slugs. Display labels are resolved in the frontend so the
// persisted values never need to change when wording does.
const (
	ClassRateLimit      = "rate_limit"
	ClassAuth           = "auth"
	ClassInvalidRequest = "invalid_request"
	ClassNotFound       = "not_found"
	ClassPermission     = "permission"
	ClassTimeout        = "timeout"
	ClassConnection     = "connection"
	ClassServerError    = "server_error"
	ClassOverloaded     = "overloaded"
	ClassContentFilter  = "content_filter"
	ClassQuota          = "quota"
	ClassOther          = "other"
)

// Classify maps an upstream failure to a class slug. Resolution order is
// body (structured) → message keywords → status code → ClassOther.
func Classify(statusCode int, body string) string {
	if c := classFromBody(body); c != "" {
		return c
	}
	if c := classFromStatus(statusCode); c != "" {
		return c
	}
	if c := classFromKeywords(body); c != "" {
		return c
	}
	return ClassOther
}

// classFromBody reads error.type / error.code from a JSON body. The mapping
// mirrors the precedent in internal/runtime/executor/codex_executor_terminal.go.
func classFromBody(body string) string {
	if strings.TrimSpace(body) == "" || !gjson.Valid(body) {
		return ""
	}
	t := strings.ToLower(strings.TrimSpace(gjson.Get(body, "error.type").String()))
	code := strings.ToLower(strings.TrimSpace(gjson.Get(body, "error.code").String()))
	switch {
	case t == "rate_limit_error" || code == "rate_limit_exceeded":
		return ClassRateLimit
	case code == "insufficient_quota":
		return ClassQuota
	case t == "authentication_error" || code == "invalid_api_key" || code == "unauthorized":
		return ClassAuth
	case t == "permission_error" || code == "forbidden" || code == "permission_denied":
		return ClassPermission
	case t == "not_found_error" || code == "not_found" || code == "model_not_found":
		return ClassNotFound
	case t == "invalid_request_error" || t == "bad_request_error":
		return ClassInvalidRequest
	case code == "content_filter" || t == "content_filter_error":
		return ClassContentFilter
	case t == "overloaded_error":
		return ClassOverloaded
	case t == "api_error" || t == "server_error":
		return ClassServerError
	}
	return ""
}

func classFromStatus(statusCode int) string {
	switch {
	case statusCode == 429:
		return ClassRateLimit
	case statusCode == 401:
		return ClassAuth
	case statusCode == 403:
		return ClassPermission
	case statusCode == 404:
		return ClassNotFound
	case statusCode == 400 || statusCode == 422:
		return ClassInvalidRequest
	case statusCode == 408 || statusCode == 504:
		return ClassTimeout
	case statusCode >= 500:
		return ClassServerError
	}
	return ""
}

func classFromKeywords(body string) string {
	s := strings.ToLower(body)
	switch {
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded"):
		return ClassTimeout
	case strings.Contains(s, "overloaded"):
		return ClassOverloaded
	case strings.Contains(s, "content filter") || strings.Contains(s, "content_filter"):
		return ClassContentFilter
	case strings.Contains(s, "quota"):
		return ClassQuota
	case strings.Contains(s, "rate limit") || strings.Contains(s, "rate_limit"):
		return ClassRateLimit
	case strings.Contains(s, "connection refused") || strings.Contains(s, "connection reset"):
		return ClassConnection
	case strings.Contains(s, "unauthorized") || strings.Contains(s, "invalid api key"):
		return ClassAuth
	}
	return ""
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/store/errorclass/ -run TestClassify -v`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/store/errorclass/
git add internal/store/errorclass/
git commit -m "feat(errorclass): add Classify with body/status/keyword resolution"
```

---

### Task 2: `errorclass.Fingerprint` + normalisation

**Files:**
- Create: `internal/store/errorclass/fingerprint.go`
- Test: `internal/store/errorclass/fingerprint_test.go`

**Step 1: Write the failing test**

```go
package errorclass

import "testing"

func TestFingerprintCollapsesVariableTokens(t *testing.T) {
	a := Fingerprint(ClassRateLimit, "anthropic", "claude-sonnet-4-5", "rate limit exceeded for request abc-12")
	b := Fingerprint(ClassRateLimit, "anthropic", "claude-sonnet-4-5", "rate limit exceeded for request xyz-98")
	if a != b {
		t.Fatalf("structurally identical messages must share a fingerprint: %q != %q", a, b)
	}
}

func TestFingerprintDistinguishesDifferentCauses(t *testing.T) {
	a := Fingerprint(ClassRateLimit, "anthropic", "m", "rate limit exceeded")
	b := Fingerprint(ClassAuth, "anthropic", "m", "rate limit exceeded")
	if a == b {
		t.Fatal("different classes must not share a fingerprint")
	}
}

func TestFingerprintStableAndBounded(t *testing.T) {
	got := Fingerprint(ClassAuth, "openai", "gpt-4o", "invalid api key")
	if len(got) != 16 {
		t.Fatalf("fingerprint length = %d, want 16", len(got))
	}
	if got != Fingerprint(ClassAuth, "openai", "gpt-4o", "invalid api key") {
		t.Fatal("fingerprint must be deterministic")
	}
}

func TestNormalizeMessage(t *testing.T) {
	if normalizeMessage("Retry 3 of 5") != normalizeMessage("Retry 9 of 12") {
		t.Fatal("digit runs must collapse")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/store/errorclass/ -run TestFingerprint -v`
Expected: FAIL — `Fingerprint` undefined.

**Step 3: Write minimal implementation**

```go
package errorclass

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

var (
	reUUID  = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	reHex   = regexp.MustCompile(`[0-9a-fA-F]{16,}`)
	reQuote = regexp.MustCompile(`"[^"]{24,}"`)
	reDigit = regexp.MustCompile(`\d+`)
	reWS    = regexp.MustCompile(`\s+`)
)

// Fingerprint returns a short stable hash grouping structurally-identical
// errors. The message is normalised first so messages that differ only in
// embedded ids/numbers/tokens collapse to the same fingerprint.
func Fingerprint(class, provider, model, message string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		class, provider, model, normalizeMessage(message),
	}, "\x00")))
	return hex.EncodeToString(h[:])[:16]
}

// normalizeMessage replaces variable tokens with placeholders, lowercases, and
// collapses whitespace so two messages with the same structure compare equal.
// Order matters: UUIDs and long hex/quoted strings are replaced before the
// generic digit run.
func normalizeMessage(msg string) string {
	s := msg
	s = reUUID.ReplaceAllString(s, "<uuid>")
	s = reQuote.ReplaceAllString(s, "<str>")
	s = reHex.ReplaceAllString(s, "<hex>")
	s = reDigit.ReplaceAllString(s, "#")
	s = strings.ToLower(s)
	s = reWS.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/store/errorclass/ -v`
Expected: PASS (all tests in the package).

**Step 5: Commit**

```bash
gofmt -w internal/store/errorclass/
git add internal/store/errorclass/
git commit -m "feat(errorclass): add Fingerprint with message normalisation"
```

---

## Phase 2 — Persistence

### Task 3: Migration for the two new columns + index

**Files:**
- Modify: `internal/store/postgresstore.go` (inside `Migrate`, after the existing `usage_errors` ALTERs around line 1650)
- Test: `internal/store/pg_migrations_test.go` (extend)

**Step 1: Write the failing test**

Add to `internal/store/pg_migrations_test.go` a check that both columns exist after `Migrate`, following the existing column-existence assertion helper used in that file:

```go
func TestMigrateAddsUsageErrorClassColumns(t *testing.T) {
	pg := newTestPostgresStore(t, "test_migrate_error_class_cols")
	ctx := context.Background()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Idempotent: a second run must be a no-op.
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (second): %v", err)
	}
	for _, col := range []string{"error_class", "error_fingerprint"} {
		var exists bool
		if err := pg.db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = $1 AND column_name = $2
			)`, pg.fullTableName(pg.cfg.UsageErrorsTable), col).Scan(&exists); err != nil {
			t.Fatalf("query column %s: %v", col, err)
		}
		if !exists {
			t.Fatalf("column %s missing after Migrate", col)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestMigrateAddsUsageErrorClassColumns -v`
Expected: FAIL — column missing.

**Step 3: Write minimal implementation**

In `Migrate`, alongside the other `usage_errors` ALTERs, add:

```go
	// error_class / error_fingerprint back the Errors page diagnostics: a stable
	// failure category and a structural hash used to group identical errors.
	// Both nullable (pre-existing rows carry NULL) and added with IF NOT EXISTS
	// so the migration is idempotent. See internal/store/errorclass.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		ALTER TABLE %s
			ADD COLUMN IF NOT EXISTS error_class TEXT,
			ADD COLUMN IF NOT EXISTS error_fingerprint TEXT`,
		usageErrorsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: add usage_errors error_class/fingerprint columns: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE INDEX IF NOT EXISTS idx_usage_errors_class ON %s(requested_at DESC, error_class)`,
		usageErrorsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create usage_errors class index: %w", err)
	}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestMigrateAddsUsageErrorClassColumns -v`
Expected: PASS. (Requires a PG test harness, consistent with the other tests in this file.)

**Step 5: Commit**

```bash
gofmt -w internal/store/postgresstore.go internal/store/pg_migrations_test.go
git add internal/store/postgresstore.go internal/store/pg_migrations_test.go
git commit -m "feat(store): add usage_errors error_class + fingerprint columns"
```

---

### Task 4: Persist the columns on `UsageError` (struct, insert, scan)

**Files:**
- Modify: `internal/store/pg_usage_errors.go` (`UsageError`, `UsageErrorRow`, `usageErrorColumnList`, `usageErrorColumnCount`, `errorRowSelectColumns`, `scanErrorRow`, `InsertError`, `BatchInsertErrors`, `ImportLiteLLMErrors`)
- Test: `internal/store/pg_usage_errors_test.go` (extend)

**Step 1: Write the failing test**

Add a round-trip test mirroring the existing insert/select tests in `pg_usage_errors_test.go`:

```go
func TestInsertErrorRoundTripsClassAndFingerprint(t *testing.T) {
	pg := newTestPostgresStore(t, "test_error_class_roundtrip")
	requireMigrated(t, pg)
	us := newTestUsageStore(t, pg)
	ctx := context.Background()

	errRow := UsageError{
		RequestID:      "req-class-1",
		Provider:       "anthropic",
		Model:          "claude-sonnet-4-5",
		FailStatusCode: 429,
		ErrorMessage:   "rate limit exceeded",
		ErrorClass:     errorclass.ClassRateLimit,
		ErrorFingerprint: "deadbeefdeadbeef",
		RequestedAt:    time.Now().UTC(),
	}
	if err := us.InsertError(ctx, errRow); err != nil {
		t.Fatalf("InsertError: %v", err)
	}
	rows, _, err := us.SelectErrors(ctx, UsageFilter{RequestID: "req-class-1"}, 1, 10)
	if err != nil {
		t.Fatalf("SelectErrors: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].ErrorClass != errorclass.ClassRateLimit {
		t.Fatalf("class = %q, want %q", rows[0].ErrorClass, errorclass.ClassRateLimit)
	}
	if rows[0].ErrorFingerprint != "deadbeefdeadbeef" {
		t.Fatalf("fingerprint = %q", rows[0].ErrorFingerprint)
	}
}
```

(Adjust the store constructor helper names to the ones the file already uses.)

**Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestInsertErrorRoundTripsClassAndFingerprint -v`
Expected: FAIL — `ErrorClass` field undefined.

**Step 3: Write minimal implementation**

1. Add to `UsageError` (after `ErrorMessage`):
```go
	// ErrorClass is the stable failure category (see internal/store/errorclass).
	ErrorClass string `json:"error_class,omitempty"`
	// ErrorFingerprint groups structurally-identical errors (see errorclass).
	ErrorFingerprint string `json:"error_fingerprint,omitempty"`
```

2. Add the same two fields to `UsageErrorRow` with the same json tags.

3. Extend `usageErrorColumnList` with `error_class, error_fingerprint` at the end (before `generate`), bump `usageErrorColumnCount` from 35 to 37, add the two columns to `errorRowSelectColumns` (`e.error_class, e.error_fingerprint`), and add `&r.ErrorClass, &r.ErrorFingerprint` to the `scanErrorRow` dest list in matching order.

4. Add `$36, $37` and the two args (`e.ErrorClass, e.ErrorFingerprint`) to **all three** inserts: `InsertError`, `BatchInsertErrors`, `ImportLiteLLMErrors`.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestInsertError -v`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/store/pg_usage_errors.go internal/store/pg_usage_errors_test.go
git add internal/store/pg_usage_errors.go internal/store/pg_usage_errors_test.go
git commit -m "feat(store): persist error_class + fingerprint on usage_errors"
```

---

### Task 5: Populate the columns in the flusher

**Files:**
- Modify: `internal/store/pg_usage_flusher.go` (the `UsageError{...}` block around line 455)
- Test: `internal/store/pg_usage_flusher_test.go` (extend, or the closest existing flusher test)

**Step 1: Write the failing test**

```go
func TestFlusherPopulatesErrorClass(t *testing.T) {
	// Build a failed record the same way the existing flusher tests do, with
	// Fail.Body = `{"error":{"type":"rate_limit_error"}}` and a 429 status.
	uerr := usageErrorFromRecord(record) // the internal builder under test
	if uerr.ErrorClass != errorclass.ClassRateLimit {
		t.Fatalf("class = %q, want %q", uerr.ErrorClass, errorclass.ClassRateLimit)
	}
	if uerr.ErrorFingerprint == "" {
		t.Fatal("fingerprint must be populated")
	}
}
```

(Use the actual unexported builder name found in the file.)

**Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestFlusherPopulatesErrorClass -v`
Expected: FAIL — fields zero.

**Step 3: Write minimal implementation**

In the builder that constructs the `UsageError`, before the returned literal:

```go
	errorClass := errorclass.Classify(failStatus, record.Fail.Body)
	errorFingerprint := errorclass.Fingerprint(errorClass, record.Provider, model, record.Fail.Body)
```

and set `ErrorClass: errorClass, ErrorFingerprint: errorFingerprint` in the `UsageError{...}` literal. Add the `internal/store/errorclass` import.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestFlusher -v`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/store/pg_usage_flusher.go internal/store/pg_usage_flusher_test.go
git add internal/store/pg_usage_flusher.go internal/store/pg_usage_flusher_test.go
git commit -m "feat(store): classify + fingerprint errors at flush time"
```

---

## Phase 3 — Aggregation queries

### Task 6: `SelectErrorSummary`

**Files:**
- Modify: `internal/store/pg_usage_errors.go` (new func + result types)
- Test: `internal/store/pg_usage_errors_test.go` (extend)

**Step 1: Write the failing test**

```go
func TestSelectErrorSummary(t *testing.T) {
	// Insert 3 rate_limit (429) and 1 auth (401) row in a window, then:
	got, err := us.SelectErrorSummary(ctx, UsageFilter{From: since})
	if err != nil { t.Fatalf("SelectErrorSummary: %v", err) }
	if got.Total != 4 { t.Fatalf("total = %d, want 4", got.Total) }
	if got.ByClass[0].ErrorClass != errorclass.ClassRateLimit || got.ByClass[0].Count != 3 {
		t.Fatalf("by_class = %+v", got.ByClass)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestSelectErrorSummary -v`
Expected: FAIL — undefined.

**Step 3: Write minimal implementation**

Result types and the query, built on `buildWhereClause` (the `e` alias). Class/status/provider groupings come from `GROUP BY`; `top_models` reuses `ORDER BY count DESC LIMIT 10`.

```go
type ErrorClassCount struct {
	ErrorClass string `json:"error_class"`
	Count      int64  `json:"count"`
}
type ErrorStatusCount struct {
	Status int   `json:"status"`
	Count  int64 `json:"count"`
}
type ErrorProviderCount struct {
	Provider string `json:"provider"`
	Count    int64  `json:"count"`
}
type ErrorModelCount struct {
	Model string `json:"model"`
	Count int64  `json:"count"`
}
type ErrorSummary struct {
	Total      int64                `json:"total"`
	ByClass    []ErrorClassCount    `json:"by_class"`
	ByStatus   []ErrorStatusCount   `json:"by_status"`
	ByProvider []ErrorProviderCount `json:"by_provider"`
	TopModels  []ErrorModelCount    `json:"top_models"`
}
```

Implement `SelectErrorSummary(ctx, filter) (ErrorSummary, error)` running five small `GROUP BY` queries (total, by class, by status, by provider, top models) — each `errorsTable e` + `buildWhereClause`. Guard `nil` store → error like the other methods.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestSelectErrorSummary -v`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/store/pg_usage_errors.go internal/store/pg_usage_errors_test.go
git add internal/store/pg_usage_errors.go internal/store/pg_usage_errors_test.go
git commit -m "feat(store): add SelectErrorSummary aggregation"
```

---

### Task 7: `SelectErrorGroups`

**Files:**
- Modify: `internal/store/pg_usage_errors.go`
- Test: `internal/store/pg_usage_errors_test.go` (extend)

**Step 1: Write the failing test**

```go
func TestSelectErrorGroupsByFingerprint(t *testing.T) {
	// Insert two rows sharing a fingerprint, one different.
	groups, err := us.SelectErrorGroups(ctx, UsageFilter{From: since}, "fingerprint", 20)
	if err != nil { t.Fatalf("SelectErrorGroups: %v", err) }
	if groups[0].Count != 2 { t.Fatalf("top group count = %d, want 2", groups[0].Count) }
	if groups[0].SampleMessage == "" { t.Fatal("sample_message must be populated") }
}

func TestSelectErrorGroupsRejectsUnknownDimension(t *testing.T) {
	if _, err := us.SelectErrorGroups(ctx, UsageFilter{}, "bogus", 20); err == nil {
		t.Fatal("unknown group_by must error")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestSelectErrorGroups -v`
Expected: FAIL — undefined.

**Step 3: Write minimal implementation**

A whitelist mapper `errorGroupColumn(groupBy string) (expr string, err error)` returning the column expression for `class|fingerprint|provider|model|status` and an error otherwise (mirrors `dimensionColumn`). Then a query grouping by that expr, selecting `COUNT(*)`, `MIN(e.error_message)` as `sample_message`, `MIN(e.requested_at)`, `MAX(e.requested_at)`, `array_agg(DISTINCT e.provider)`, `array_agg(DISTINCT e.model)`, and the latest `request_id`. Result type:

```go
type ErrorGroup struct {
	Key           string   `json:"key"`
	ErrorClass    string   `json:"error_class,omitempty"`
	SampleMessage string   `json:"sample_message"`
	Count         int64    `json:"count"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	Providers     []string `json:"providers"`
	Models        []string `json:"models"`
	LastRequestID string   `json:"last_request_id,omitempty"`
}
```

Use `pqStringArray`/array scanning consistent with the existing helpers in the package.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestSelectErrorGroups -v`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/store/pg_usage_errors.go internal/store/pg_usage_errors_test.go
git add internal/store/pg_usage_errors.go internal/store/pg_usage_errors_test.go
git commit -m "feat(store): add SelectErrorGroups leaderboard"
```

---

### Task 8: `SelectErrorTimeline`

**Files:**
- Modify: `internal/store/pg_usage_errors.go`
- Test: `internal/store/pg_usage_errors_test.go` (extend)

**Step 1: Write the failing test**

```go
func TestSelectErrorTimeline(t *testing.T) {
	series, err := us.SelectErrorTimeline(ctx, UsageFilter{From: since}, "hour")
	if err != nil { t.Fatalf("SelectErrorTimeline: %v", err) }
	// Two classes over one hour bucket => one series per class.
	if len(series) == 0 { t.Fatal("expected at least one series") }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestSelectErrorTimeline -v`
Expected: FAIL — undefined.

**Step 3: Write minimal implementation**

Group by `intervalExpr(interval)` and `error_class`, reusing `intervalExpr`. Result type:

```go
type ErrorTimelinePoint struct {
	Bucket string `json:"bucket"`
	Count  int64  `json:"count"`
}
type ErrorClassSeries struct {
	ErrorClass string               `json:"error_class"`
	Points     []ErrorTimelinePoint `json:"points"`
}
```

Fold rows into per-class series in Go after the scan.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestSelectErrorTimeline -v`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/store/pg_usage_errors.go internal/store/pg_usage_errors_test.go
git add internal/store/pg_usage_errors.go internal/store/pg_usage_errors_test.go
git commit -m "feat(store): add SelectErrorTimeline per-class series"
```

---

## Phase 4 — HTTP handlers & routes

### Task 9: Summary / groups / timeline handlers

**Files:**
- Modify: `internal/api/handlers/management/usage_errors.go`
- Modify: `internal/api/server_management.go` (route registration)
- Test: `internal/api/handlers/management/usage_errors_test.go` (create if absent)

**Step 1: Write the failing test**

Follow the existing handler test harness (PG-backed `requirePG`). Assert:
- invalid `group_by` → 400
- no PG → 503
- valid request → 200 with the expected top-level keys.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/api/handlers/management/ -run TestGetErrorGroups -v`
Expected: FAIL — route/handler undefined.

**Step 3: Write minimal implementation**

Three handlers mirroring `GetUsageErrors`: `requirePG` → parse query with `parseUsageStatsQuery` → `filterFromQuery` → call the store method → JSON. Register routes in `server_management.go` next to the existing `/usage-stats/errors` routes:

```go
	r.GET("/usage-stats/errors/summary", h.GetErrorSummary)
	r.GET("/usage-stats/errors/groups", h.GetErrorGroups)
	r.GET("/usage-stats/errors/timeline", h.GetErrorTimeline)
```

`GetErrorGroups` reads `group_by` (default `class`) and `limit` (default 20, cap 200) and returns 400 on an unknown dimension (the store error maps to 400, matching `GetUsageErrors`).

**Step 4: Run test to verify it passes**

Run: `go test ./internal/api/handlers/management/ -run TestGetError -v`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/api/handlers/management/usage_errors.go internal/api/server_management.go
git add internal/api/handlers/management/usage_errors.go internal/api/server_management.go internal/api/handlers/management/usage_errors_test.go
git commit -m "feat(api): add error summary/groups/timeline endpoints"
```

---

## Phase 5 — Dashboard

### Task 10: API client functions

**Files:**
- Modify: `web/dashboard/src/api/client.js`

**Step 1: Add the functions** next to `getUsageErrors`:

```js
export async function getErrorSummary(params = {}) {
  const qs = buildQuery(params);
  return fetchJSON(`/usage-stats/errors/summary${qs}`);
}

export async function getErrorGroups(params = {}) {
  const qs = buildQuery(params);
  return fetchJSON(`/usage-stats/errors/groups${qs}`);
}

export async function getErrorTimeline(params = {}, interval = 'hour') {
  const qs = buildQuery({ ...params, interval });
  return fetchJSON(`/usage-stats/errors/timeline${qs}`);
}
```

**Step 2: Verify** — `cd web/dashboard && npm run build` succeeds.

**Step 3: Commit**

```bash
git add web/dashboard/src/api/client.js
git commit -m "feat(dashboard): add error aggregation API client"
```

---

### Task 11: Class label/badge helper + enriched KPI strip

**Files:**
- Create: `web/dashboard/src/pages/errorClass.js` (slug → label + colour)
- Modify: `web/dashboard/src/pages/ErrorsPage.jsx`
- Test: `web/dashboard/src/pages/errorClass.test.js`

**Step 1: Write the failing test** for the slug→label map and unknown-slug fallback.

**Step 2–4:** Implement `errorClassLabel(slug)` and `errorClassColorVar(slug)`, then render the enriched KPI strip (total/failed/rate + class chips with counts + mini timeline). Clicking a chip sets a `error_class` filter in page state.

**Step 5: Commit**

```bash
git add web/dashboard/src/pages/errorClass.js web/dashboard/src/pages/errorClass.test.js web/dashboard/src/pages/ErrorsPage.jsx
git commit -m "feat(dashboard): class-aware KPI strip on Errors page"
```

---

### Task 12: "Error Patterns" tab (`ErrorGroupsPanel`)

**Files:**
- Create: `web/dashboard/src/pages/ErrorGroupsPanel.jsx`
- Modify: `web/dashboard/src/pages/ErrorsPage.jsx`
- Test: `web/dashboard/src/pages/ErrorGroupsPanel.test.jsx`

**Step 1: Write the failing test** — renders groups from data; `EmptyState` when empty; clicking a group calls the onSelect callback with the group key.

**Step 2–4:** Build the segmented dimension control (`class|fingerprint|provider|model|status`), the leaderboard table (badge, sample_message + copy, count, first/last seen, providers, models), and wire row click to set the table filter and scroll to the detail table.

**Step 5: Commit**

```bash
git add web/dashboard/src/pages/ErrorGroupsPanel.jsx web/dashboard/src/pages/ErrorGroupsPanel.test.jsx web/dashboard/src/pages/ErrorsPage.jsx
git commit -m "feat(dashboard): Error Patterns tab with grouped leaderboard"
```

---

### Task 13: Table Class column + detail modal enrichment

**Files:**
- Modify: `web/dashboard/src/pages/ErrorsPage.jsx`

**Step 1–4:** Add the Class badge column after Status (row tint by class); in `ErrorDetailModal` add the class badge to the header, surface `entry_provider_key`, `network_rtt_ms`, `user_id`, add the "N errors with the same pattern" link, and the action buttons (Copy request ID / View logs / Check provider health) using the existing router navigation.

**Step 5: Commit**

```bash
git add web/dashboard/src/pages/ErrorsPage.jsx
git commit -m "feat(dashboard): class column + richer error detail modal"
```

---

## Phase 6 — Verification

### Task 14: Full verification

**Step 1: Format & build**

```bash
gofmt -w .
go build -o test-output ./cmd/server && rm test-output
```

**Step 2: Test**

```bash
go test ./internal/store/... ./internal/api/handlers/management/...
go test ./...
cd web/dashboard && npm run build
```

Expected: new tests pass; no new failures beyond the known pre-existing ones.

**Step 3: Embed the dashboard**

```bash
cd ../.. && make dash-embed
```

**Step 4: Commit any embed artifacts**

```bash
git add -A && git commit -m "chore: rebuild embedded dashboard"
```

---

## Out of Scope

- Client-side aggregation/classification, query-time classification, storing raw bodies in `usage_errors`, class-based alerting — see the design doc §8.
