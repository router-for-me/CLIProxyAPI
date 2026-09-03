# Upstream Entry Routing Strategy Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Give `claude-api-key` and `openai-compatibility` upstream-provider pools a row-level routing strategy plus per-entry priority, and make any entry error (including request-fault-classified ones) rotate to the next entry in the same pool before surfacing to the end user.

**Architecture:** Fields flow through the existing pipeline: PG columns → `store.UpstreamProvider` → `upstreamsync/render.go` fan-out → `internal/config` types → `synthesizer` auth attributes (`pool_strategy`, `priority`) → conductor execution loops, where a non-empty `pool_strategy` suppresses the `isRequestInvalidError` hard-stop so rotation continues within the pool.

**Tech Stack:** Go 1.26 (Gin, lib/pq), React 19 + Vite dashboard, Postgres migrations via `Migrate()`.

**Worktree:** `.worktrees/entry-routing-strategy`, branch `feat/entry-routing-strategy`. All commands below run from that directory.

**Design doc:** `docs/plans/2026-09-03-upstream-entry-routing-strategy-design.md`

**Baseline note:** 3 tests in `internal/runtime/executor` (`TestApplyClaudeHeaders_DisableDeviceProfileStabilization`, `TestApplyClaudeHeaders_LegacyModePreservesConfiguredUserAgentOverrideForClaudeClients`, `TestClaudeExecutor_NonClaudeRequestUsesClaudeCode220CLIFingerprint`) fail on `main` before any of our changes — pre-existing, keep running them to confirm they do not regress further. `TestMountDashboardRoutes` fails only because the worktree has no built `dist/`; Task 11 fixes it via `make dash-embed`.

**Valid strategy values** (shared everywhere — config validation, dashboard select, normalizer): `round-robin`, `weighted-round-robin`, `fill-first`, `priority`, `failover`. Empty string = unset (inherit global, today's behavior). `priority` and `fill-first` are aliases; `failover` and `round-robin` are aliases — normalized to the canonical value at every layer boundary (store read, render, seed, synthesize) so the conductor sees exactly one spelling.

---

### Task 1: PG schema — `routing_strategy` on `upstream_providers`, `priority` on entries

**Files:**
- Modify: `internal/store/postgresstore.go` (~line 718, right after the existing `weight` migration)
- Test: `internal/store/pg_upstream_providers_test.go` (append)

**Step 1: Write the failing test**

Append to `internal/store/pg_upstream_providers_test.go`, modeled on `TestUpstreamProviderStoreAPIKeyEntryWeightRoundTrip` (line ~321):

```go
// TestUpstreamProviderStoreRoutingStrategyAndEntryPriorityRoundTrip pins the
// schema contract for pool routing strategy + per-entry priority: the
// upstream_providers.routing_strategy column is nullable TEXT (nil = unset =
// today's behavior), and the entries priority column is nullable INTEGER
// (nil = inherit the row-level priority). Both must round-trip through
// Create/Update/Get.
func TestUpstreamProviderStoreRoutingStrategyAndEntryPriorityRoundTrip(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_entry_routing")

	// Column contracts.
	var strategyType, strategyNullable string
	err := pg.DB().QueryRowContext(context.Background(), `
		SELECT data_type, is_nullable FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = 'routing_strategy'
	`, pg.cfg.Schema, pg.cfg.UpstreamProvidersTable).Scan(&strategyType, &strategyNullable)
	if err != nil {
		t.Fatalf("query routing_strategy column: %v", err)
	}
	if strategyType != "text" || strategyNullable != "YES" {
		t.Fatalf("routing_strategy column = %s/%s, want text/YES", strategyType, strategyNullable)
	}
	var prioType, prioNullable string
	err = pg.DB().QueryRowContext(context.Background(), `
		SELECT data_type, is_nullable FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = 'priority'
	`, pg.cfg.Schema, pg.cfg.UpstreamProviderEntriesTable).Scan(&prioType, &prioNullable)
	if err != nil {
		t.Fatalf("query entries priority column: %v", err)
	}
	if prioType != "integer" || prioNullable != "YES" {
		t.Fatalf("entries priority column = %s/%s, want integer/YES", prioType, prioNullable)
	}

	created, err := pg.CreateUpstreamProvider(t.Context(), store.UpstreamProvider{
		ProviderType:    storeTypeClaudeAPIKey, // use the literal "claude-api-key" if no const exists in this package
		Name:            "pooled",
		RoutingStrategy: "failover",
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{APIKey: "secret-a", Name: "alpha", Priority: intPtr(10)},
			{APIKey: "secret-b", Name: "beta"}, // nil priority = inherit
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.RoutingStrategy != "failover" {
		t.Fatalf("created strategy = %q, want failover", created.RoutingStrategy)
	}
	got, err := pg.GetUpstreamProvider(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RoutingStrategy != "failover" {
		t.Fatalf("strategy round-trip = %q, want failover", got.RoutingStrategy)
	}
	if len(got.APIKeyEntries) != 2 {
		t.Fatalf("entries = %d, want 2", len(got.APIKeyEntries))
	}
	if got.APIKeyEntries[0].Priority == nil || *got.APIKeyEntries[0].Priority != 10 {
		t.Fatalf("entry 0 priority = %#v, want *10", got.APIKeyEntries[0].Priority)
	}
	if got.APIKeyEntries[1].Priority != nil {
		t.Fatalf("entry 1 priority = %#v, want nil (inherit)", got.APIKeyEntries[1].Priority)
	}

	// Clearing back to unset must round-trip as "".
	got.RoutingStrategy = ""
	if _, err := pg.UpdateUpstreamProvider(t.Context(), *got); err != nil {
		t.Fatalf("Update clearing strategy: %v", err)
	}
	again, err := pg.GetUpstreamProvider(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if again.RoutingStrategy != "" {
		t.Fatalf("strategy after clear = %q, want empty", again.RoutingStrategy)
	}
}
```

Use the store-test helpers this file already uses (check how existing tests call Create/Get — e.g. `st.Create`/`st.Get` on the `UpstreamProviderStore` value returned by `newTestPostgresStore`); mirror their exact receiver names and add a small `intPtr` helper only if none exists in the package's test files.

**Step 2: Run test to verify it fails**

```bash
go test ./internal/store/ -run TestUpstreamProviderStoreRoutingStrategyAndEntryPriorityRoundTrip -v
```

Expected: FAIL — `unknown column` / missing `RoutingStrategy`+`Priority` fields (compile error first: struct fields don't exist yet). The compile error is the failing state; add the struct fields (Task 2) and SQL in this task together only if you prefer one compile unit — otherwise do struct fields first in Task 2 and keep this test red on schema columns.

Simplest sequencing: Task 1 adds **struct fields + migration + SQL columns together** (they are one compile unit), then the test goes green in Task 2's DTO step. Run the test now expecting a **compile failure** (`RoutingStrategy undefined`) — that is the red state.

**Step 3: Add the migration and struct fields**

In `internal/store/pg_upstream_providers.go`:

`store.UpstreamProvider` (after `Disabled`, line ~31):
```go
	// RoutingStrategy is the optional in-pool credential-selection strategy
	// for entry-bearing providers (claude-api-key, openai-compatibility).
	// Empty = unset: entries follow the global routing.strategy and
	// request-fault errors keep today's hard-stop behavior. Any valid value
	// additionally opts the pool into aggressive failover — any entry error
	// rotates to the next entry before surfacing to the client.
	RoutingStrategy string `json:"routing_strategy,omitempty"`
```

`store.UpstreamProviderAPIKey` (after `Weight`, line ~103):
```go
	// Priority is the optional selection tier for this entry within its
	// pool. nil = inherit the provider row's Priority (today's behavior);
	// the scheduler always serves the highest ready tier first and descends
	// when the upper tier cools down. Stored as nullable INTEGER so
	// "inherit" stays distinct from an explicit 0.
	Priority *int `json:"priority,omitempty"`
```

In `internal/store/postgresstore.go`, right after the `weight` migration (~line 718):

```go
	// upstream_providers.routing_strategy column. The optional in-pool
	// selection strategy for entry-bearing providers; NULL = unset = today's
	// behavior. Nullable TEXT with no DEFAULT so legacy rows keep NULL.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS routing_strategy TEXT`, upstreamProvidersTable,
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream providers routing strategy column: %w", err)
	}
	// upstream_provider_api_key_entries priority column. Optional per-entry
	// selection tier; NULL = inherit the provider row priority. Nullable
	// INTEGER with no DEFAULT, mirroring the weight column contract.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS priority INTEGER`, upstreamEntriesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream provider entries priority column: %w", err)
	}
```

Check how `upstreamProvidersTable` is spelled near the existing code (grep `upstreamProvidersTable :=` in `Migrate`); if `Migrate` doesn't currently bind it, add `upstreamProvidersTable := s.fullTableName(s.cfg.UpstreamProvidersTable)` above the block.

**Step 4: Thread the columns through the store SQL**

In `pg_upstream_providers.go` — find every SELECT/INSERT/UPDATE that touches `weight` on the parent (`upstream_providers`) and on entries; the entry scan at ~line 500 (`SELECT id, provider_id, api_key, name, proxy_url, sort_order, weight`) gains `priority` (read via `sql.NullInt64` like `weight`), the entry INSERT at ~line 682 and UPDATE at ~line 692 gain `priority` written via the existing `nullableInt` helper, and the parent-row INSERT/UPDATE/SELECT sites gain `routing_strategy` (TEXT — write with the existing null-string helper if present, else `sql.Null[string]`-style; check how `Label` or `CloakMode` columns are written and copy that pattern exactly).

Also update `syncAPIKeyEntriesTx` doc comment: entries now carry priority.

**Step 5: Run test to verify it passes**

```bash
go test ./internal/store/ -run TestUpstreamProviderStoreRoutingStrategy -v
```
Expected: PASS (or SKIP with `PGSTORE_TEST_DSN not set` — acceptable in dev; the compile must be green either way). Then:
```bash
go build ./... && go test ./internal/store/ 2>&1 | grep -E "FAIL|ok" | head
```

**Step 6: Commit**

```bash
git add internal/store/pg_upstream_providers.go internal/store/postgresstore.go internal/store/pg_upstream_providers_test.go
git commit -m "feat(store): persist pool routing strategy and per-entry priority"
```

---

### Task 2: Strategy normalizer + validation in config

**Files:**
- Create: `internal/config/strategy.go`
- Create: `internal/config/strategy_test.go`
- Modify: `internal/config/config_types.go` (`OpenAICompatibility`, `OpenAICompatibilityAPIKey`, `ClaudeKey`)

**Step 1: Write the failing test**

`internal/config/strategy_test.go`:

```go
package config

import "testing"

func TestNormalizePoolRoutingStrategy(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"round-robin", "round-robin"},
		{" Round-Robin ", "round-robin"},
		{"weighted-round-robin", "weighted-round-robin"},
		{"wrr", "weighted-round-robin"},
		{"fill-first", "fill-first"},
		{"ff", "fill-first"},
		{"priority", "fill-first"},      // alias, canonicalized
		{"failover", "round-robin"},     // alias, canonicalized
		{"bogus", ""},
	}
	for _, tc := range cases {
		if got := NormalizePoolRoutingStrategy(tc.in); got != tc.want {
			t.Fatalf("NormalizePoolRoutingStrategy(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidatePoolRoutingStrategy(t *testing.T) {
	for _, ok := range []string{"", "round-robin", "weighted-round-robin", "fill-first"} {
		if err := ValidatePoolRoutingStrategy(ok); err != nil {
			t.Fatalf("ValidatePoolRoutingStrategy(%q) = %v, want nil", ok, err)
		}
	}
	if err := ValidatePoolRoutingStrategy("priority"); err == nil {
		t.Fatal("ValidatePoolRoutingStrategy(priority) = nil, want error (raw aliases rejected at input boundaries)")
	}
	if err := ValidatePoolRoutingStrategy("nope"); err == nil {
		t.Fatal("ValidatePoolRoutingStrategy(nope) = nil, want error")
	}
}
```

**Step 2: Run to verify failure**

```bash
go test ./internal/config/ -run TestNormalizePoolRoutingStrategy -v
```
Expected: FAIL (undefined `NormalizePoolRoutingStrategy`).

**Step 3: Implement**

`internal/config/strategy.go`:

```go
package config

import "fmt"

// Canonical pool routing strategy values for entry-bearing providers
// (claude-api-key, openai-compatibility). Empty means unset: entries follow
// the global routing.strategy and request-fault errors keep the historical
// hard-stop rotation behavior. "priority"/"failover" are accepted as Model
// Routes-compatible aliases at input boundaries (store read, config parse)
// and canonicalized here so downstream layers see one spelling.
const (
	PoolStrategyRoundRobin         = "round-robin"
	PoolStrategyWeightedRoundRobin = "weighted-round-robin"
	PoolStrategyFillFirst          = "fill-first"
)

// NormalizePoolRoutingStrategy canonicalizes an operator-supplied pool
// strategy. Unknown/blank values return "" (unset) EXCEPT when called via
// ValidatePoolRoutingStrategy, which is the strict gate for HTTP/YAML input.
func NormalizePoolRoutingStrategy(s string) string {
	switch lowerTrim(s) {
	case "round-robin", "failover":
		return PoolStrategyRoundRobin
	case "weighted-round-robin", "wrr":
		return PoolStrategyWeightedRoundRobin
	case "fill-first", "priority", "ff":
		return PoolStrategyFillFirst
	default:
		return ""
	}
}

// ValidatePoolRoutingStrategy accepts only canonical values (plus empty).
// Raw aliases must be rejected at input boundaries so operators see their
// typo; use NormalizePoolRoutingStrategy first when aliases are intended.
func ValidatePoolRoutingStrategy(s string) error {
	switch s {
	case "", PoolStrategyRoundRobin, PoolStrategyWeightedRoundRobin, PoolStrategyFillFirst:
		return nil
	default:
		return fmt.Errorf("invalid routing strategy %q: want one of round-robin, weighted-round-robin, fill-first", s)
	}
}

func lowerTrim(s string) string {
	// avoid importing strings for two calls
	b := []byte(s)
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\t') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t') {
		end--
	}
	out := string(b[start:end])
	// lowercase ASCII
	c := []byte(out)
	for i := range c {
		if c[i] >= 'A' && c[i] <= 'Z' {
			c[i] += 'a' - 'A'
		}
	}
	return string(c)
}
```

(If the package already imports `strings` everywhere, just use `strings.ToLower(strings.TrimSpace(s))` for `lowerTrim` — simpler, preferred.)

**Step 4: Run test to verify pass**

```bash
go test ./internal/config/ -run "TestNormalizePoolRoutingStrategy|TestValidatePoolRoutingStrategy" -v
```
Expected: PASS.

**Step 5: Add config struct fields**

In `internal/config/config_types.go`:

`OpenAICompatibility` (after `Priority`, ~line 709) — named strategy lives on the pool:
```go
	// Strategy selects the in-pool credential selection strategy for this
	// provider's api-key-entries (round-robin, weighted-round-robin,
	// fill-first). Empty = follow the global routing.strategy. Any non-empty
	// value opts the pool into aggressive failover: entry errors rotate to
	// the next entry before surfacing to the client.
	Strategy string `yaml:"strategy,omitempty" json:"strategy,omitempty"`
```

`OpenAICompatibilityAPIKey` (after `Weight`, ~line 749):
```go
	// Priority is the optional selection tier for this entry within the
	// pool. nil = inherit the pool-level Priority. The scheduler serves the
	// highest ready tier first and descends when a tier cools down.
	Priority *int `yaml:"priority,omitempty" json:"priority,omitempty"`
```

`ClaudeKey` (after `UpstreamProviderEntryID`, ~line 462) — carry-through, same pattern as `UpstreamProviderID`:
```go
	// UpstreamProviderStrategy is the canonicalized pool routing strategy of
	// the upstream_providers row this entry was rendered from. The renderer
	// populates it; the synthesizer embeds it in the auth's `pool_strategy`
	// attribute which activates in-pool aggressive failover in the conductor.
	// Empty = legacy single-key behavior. Operators should not set this
	// manually.
	UpstreamProviderStrategy string `yaml:"upstream-provider-strategy,omitempty" json:"-"`
```

**Step 6: Build + commit**

```bash
gofmt -w internal/config/ && go build ./... && go test ./internal/config/ 2>&1 | grep -E "FAIL|ok" | head
git add internal/config/strategy.go internal/config/strategy_test.go internal/config/config_types.go
git commit -m "feat(config): pool routing strategy field + normalizer/validator"
```

---

### Task 3: Management DTO + API round-trip

**Files:**
- Modify: `internal/api/handlers/management/upstream_providers_types.go:12` (`upstreamProviderReq`), `:59` (`upstreamProviderEntryReq`)
- Modify: `internal/api/handlers/management/upstream_providers.go:80` (`toUpstreamProvider`)
- Test: `internal/api/handlers/management/upstream_providers_test.go` (create if missing — check first; else append to the closest existing test file)

**Step 1: Write the failing test**

Add a unit test for `toUpstreamProvider` (it is a pure function — no PG needed):

```go
func TestToUpstreamProviderMapsRoutingStrategyAndEntryPriority(t *testing.T) {
	p := toUpstreamProvider(&upstreamProviderReq{
		ProviderType:    "claude-api-key",
		RoutingStrategy: "failover",
		APIKeyEntries: []upstreamProviderEntryReq{
			{APIKey: "k1", Priority: intPtr(10)},
			{APIKey: "k2"}, // inherit
		},
	})
	if p.RoutingStrategy != "failover" {
		t.Fatalf("RoutingStrategy = %q, want failover", p.RoutingStrategy)
	}
	if p.APIKeyEntries[0].Priority == nil || *p.APIKeyEntries[0].Priority != 10 {
		t.Fatalf("entry 0 priority = %#v, want *10", p.APIKeyEntries[0].Priority)
	}
	if p.APIKeyEntries[1].Priority != nil {
		t.Fatalf("entry 1 priority = %#v, want nil", p.APIKeyEntries[1].Priority)
	}
}
```

(`intPtr` — reuse if present in package tests, else `func intPtr(v int) *int { return &v }` in the test file.)

Also add an invalid-strategy handler test if the handler tests spin up a fake store — check `handlers_test.go` in the same package for the existing pattern for 400 responses; if a full handler harness exists, add:

```go
func TestUpdateUpstreamProviderRejectsInvalidRoutingStrategy(t *testing.T) { ... }
```

asserting HTTP 400 for `routing_strategy: "bogus"`.

**Step 2: Run — expect compile failure** (`RoutingStrategy undefined` on the DTO).

```bash
go test ./internal/api/handlers/management/ -run TestToUpstreamProvider -v
```

**Step 3: Implement**

`upstreamProviderReq` (after `Priority`, line ~15):
```go
	// RoutingStrategy is the optional in-pool selection strategy for
	// entry-bearing providers. Accepts the canonical values plus the Model
	// Routes aliases (priority, failover) — canonicalized on the store side.
	RoutingStrategy string `json:"routing_strategy,omitempty"`
```

`upstreamProviderEntryReq` (after `Weight`):
```go
	// Priority is the optional selection tier for this entry. nil = inherit
	// the row-level priority.
	Priority *int `json:"priority,omitempty"`
```

`toUpstreamProvider` (~line 107, add to the struct literal):
```go
		RoutingStrategy:         body.RoutingStrategy,
```
and in the entries loop (~line 147):
```go
		if e.Priority != nil {
			v := *e.Priority
			entry.Priority = &v
		}
```

**Validation placement:** where the handler validates entry names today (grep `normalizeUpstreamProviderEntryName` callers in this package) add, before the store call:
```go
	if strategy := config.NormalizePoolRoutingStrategy(body.RoutingStrategy); strategy == "" && strings.TrimSpace(body.RoutingStrategy) != "" {
		// 400 invalid_request
	}
```
The store write persists the **normalized** value — set `p.RoutingStrategy = config.NormalizePoolRoutingStrategy(body.RoutingStrategy)` inside `toUpstreamProvider` so `priority`/`failover` aliases are canonicalized once at the boundary. (Check the import path for `internal/config` from the management package — it's already imported elsewhere in the API tree.)

**Step 4: Run tests — PASS; build.**

```bash
gofmt -w internal/api/handlers/management/ && go build ./... && go test ./internal/api/handlers/management/ 2>&1 | grep -E "FAIL|ok" | head
```

**Step 5: Commit**

```bash
git add internal/api/handlers/management/
git commit -m "feat(api): routing_strategy + entry priority in upstream provider DTO"
```

---

### Task 4: Renderer fan-out (render.go + seed.go)

**Files:**
- Modify: `internal/upstreamsync/render.go:161` (`buildClaudeKey`), `:203` (`openAICompatFromProvider`)
- Modify: `internal/upstreamsync/seed.go:180` (`providerFromClaudeKey`), `:231` (`providerFromOpenAICompat`)
- Test: `internal/upstreamsync/render_test.go` (append; check existing test names first)

**Step 1: Write the failing tests**

```go
func TestRenderClaudePoolStrategyAndEntryPriority(t *testing.T) {
	prio10, prio5 := 10, 5
	cfg := RenderConfig([]store.UpstreamProvider{{
		ID:               7,
		ProviderType:     TypeClaudeAPIKey,
		Name:             "pool",
		RoutingStrategy:  "failover",
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{ID: 71, APIKey: "k1", Priority: &prio10},
			{ID: 72, APIKey: "k2", Priority: &prio5},
			{ID: 73, APIKey: "k3"}, // inherit row priority
		},
	}})
	if len(cfg.ClaudeKey) != 3 {
		t.Fatalf("ClaudeKey items = %d, want 3", len(cfg.ClaudeKey))
	}
	if cfg.ClaudeKey[0].UpstreamProviderStrategy != "round-robin" {
		t.Fatalf("strategy carry-through = %q, want round-robin (failover canonicalized)", cfg.ClaudeKey[0].UpstreamProviderStrategy)
	}
	if cfg.ClaudeKey[0].Priority != 10 || cfg.ClaudeKey[1].Priority != 5 {
		t.Fatalf("entry priorities = %d,%d, want 10,5", cfg.ClaudeKey[0].Priority, cfg.ClaudeKey[1].Priority)
	}
	if cfg.ClaudeKey[2].Priority != 0 {
		t.Fatalf("inherit priority = %d, want row default 0", cfg.ClaudeKey[2].Priority)
	}
}

func TestRenderOpenAICompatPoolStrategyAndEntryPriority(t *testing.T) {
	prio10 := 10
	cfg := RenderConfig([]store.UpstreamProvider{{
		ID:               8,
		ProviderType:     TypeOpenAICompatibility,
		Name:             "pool",
		RoutingStrategy:  "priority", // raw alias from store — canonicalized in render
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{ID: 81, APIKey: "k1", Priority: &prio10},
		},
	}})
	if len(cfg.OpenAICompatibility) != 1 || cfg.OpenAICompatibility[0].Strategy != "fill-first" {
		t.Fatalf("pool strategy = %#v, want fill-first", cfg.OpenAICompatibility)
	}
	if cfg.OpenAICompatibility[0].APIKeyEntries[0].Priority == nil || *cfg.OpenAICompatibility[0].APIKeyEntries[0].Priority != 10 {
		t.Fatal("openai entry priority not carried")
	}
}
```

And a seed inverse test (mirror the render one): `providerFromOpenAICompat` must map `Strategy` → `RoutingStrategy` (normalized) and entry `Priority` → store entry; `providerFromClaudeKey` must map `UpstreamProviderStrategy` → `RoutingStrategy` (normalized). Since those are unexported, test through `SeedFromArtifacts`-level behavior only if a store stub exists in the package's tests; otherwise export-for-test is NOT allowed — test `providerFrom*` directly in the same package (test file is package `upstreamsync`).

**Step 2: Run — expect FAIL.**

```bash
go test ./internal/upstreamsync/ -run "TestRender.*Strategy|TestRender.*Priority" -v
```

**Step 3: Implement**

`buildClaudeKey` (render.go ~161):
```go
	k := config.ClaudeKey{
		APIKey:                  e.APIKey,
		Weight:                  e.Weight,
		Priority:                p.Priority, // row default; overridden below when the entry sets one
		...
	}
	if e.Priority != nil {
		k.Priority = *e.Priority
	}
	k.UpstreamProviderStrategy = config.NormalizePoolRoutingStrategy(p.RoutingStrategy)
```

`openAICompatFromProvider` (render.go ~203): add `Strategy: config.NormalizePoolRoutingStrategy(p.RoutingStrategy)` to the literal and `Priority: e.Priority` to each entry literal.

`providerFromClaudeKey` (seed.go ~180): add `RoutingStrategy: config.NormalizePoolRoutingStrategy(k.UpstreamProviderStrategy)`. Note the existing long comment block (~line 194) explaining dropped IDs — extend it: strategy, like the IDs, is renderer metadata, but unlike IDs it CAN round-trip through the parent row so we keep it.

`providerFromOpenAICompat` (seed.go ~231): add `RoutingStrategy: config.NormalizePoolRoutingStrategy(k.Strategy)` and `Priority: e.Priority` in the entry loop.

**Step 4: Run — PASS; package build.**

```bash
gofmt -w internal/upstreamsync/ && go test ./internal/upstreamsync/ 2>&1 | grep -E "FAIL|ok" | head
```

**Step 5: Commit**

```bash
git add internal/upstreamsync/
git commit -m "feat(upstreamsync): fan out pool strategy and per-entry priority"
```

---

### Task 5: Synthesizer — `pool_strategy` + per-entry priority attributes

**Files:**
- Modify: `sdk/cliproxy/auth/classification.go:16-26` (attribute constant)
- Modify: `internal/watcher/synthesizer/config.go` (`synthesizeClaudeKeys` ~line 203, `synthesizeOpenAICompat` ~line 347)
- Test: `internal/watcher/synthesizer/config_test.go` (append; find existing Claude entry test names first)

**Step 1: Write the failing test**

Follow the existing synthesizer test patterns (grep `TestSynthesizeClaude` in the package). Core assertions:

```go
// Pool with strategy → every auth of the pool carries pool_strategy.
// Entries with priority → priority attribute per entry; nil → row priority.
// No strategy → attribute absent (legacy behavior).
```

Register three Claude auths (one row, entries with priorities 10/5/inherit) via a `config.Config{ClaudeKey: [...]}` with `UpstreamProviderID: 7`, `UpstreamProviderStrategy: "round-robin"`, entries carrying `UpstreamProviderEntryID` 71/72/73 and `Priority`/`Weight`. Assert `attrs["pool_strategy"] == "round-robin"` on each, `attrs["priority"]` mapping, and — with strategy empty — `_, ok := attrs["pool_strategy"]; !ok`.

Same for OpenAI-compat: `OpenAICompatibility{Strategy: "fill-first", APIKeyEntries: [{Priority: &prio10}, ...]}` → auths with `pool_strategy = "fill-first"` and `priority = "10"`.

**Step 2: Run — expect FAIL** (`pool_strategy` attribute never set).

**Step 3: Implement**

`sdk/cliproxy/auth/classification.go` — add to the const block:
```go
	AttributePoolStrategy = "pool_strategy"
```

`internal/watcher/synthesizer/config.go`:

Claude (`synthesizeClaudeKeys`, after `addClaudeEntryProviderKey(...)` ~line 250):
```go
		if s := config.NormalizePoolRoutingStrategy(ck.UpstreamProviderStrategy); s != "" {
			attrs[coreauth.AttributePoolStrategy] = s
		}
```

OpenAI-compat (`synthesizeOpenAICompat`, inside the entries loop after `addWeightToAttrs(entry.Weight, attrs)` ~line 389):
```go
		if entry.Priority != nil {
			attrs["priority"] = strconv.Itoa(*entry.Priority)
		} else if compat.Priority != 0 {
			attrs["priority"] = strconv.Itoa(compat.Priority)
		}
		if s := config.NormalizePoolRoutingStrategy(compat.Strategy); s != "" {
			attrs[coreauth.AttributePoolStrategy] = s
		}
```
(Keep the existing `if compat.Priority != 0` block — the entry check must come first.)

**Step 4: Run — PASS; commit**

```bash
gofmt -w sdk/cliproxy/auth/classification.go internal/watcher/synthesizer/ && go test ./internal/watcher/synthesizer/ 2>&1 | grep -E "FAIL|ok" | head
git add sdk/cliproxy/auth/classification.go internal/watcher/synthesizer/
git commit -m "feat(synthesizer): stamp pool_strategy and per-entry priority attributes"
```

---

### Task 6: Conductor — aggressive failover (the zero-downtime core) — TDD

**Files:**
- Modify: `sdk/cliproxy/auth/conductor_execution.go` (6 call sites of `isRequestInvalidError`: lines ~378, ~390, ~513, ~525, ~692; plus pool-scope narrowing)
- Create: `sdk/cliproxy/auth/pool_failover_test.go`
- Test harness reuse: `claudeCancellationTestExecutor` (conductor_claude_cancellation_test.go:15), `newClaudeCancellationTestManager` (:85), `requestScopedStatusError` (conductor_overrides_test.go:284)

**Step 1: Write the failing tests**

`sdk/cliproxy/auth/pool_failover_test.go`:

```go
package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// poolFailoverExecutor fails the first entry's Execute with a 400
// invalid_request_error body (request-fault classified) and succeeds on any
// other auth.
type poolFailoverExecutor struct {
	*claudeCancellationTestExecutor
	failAuthIDs map[string]struct{}
}

// newPoolFailoverManager builds a Manager with one claude executor, a model
// registered, and three same-pool auths (provider_key claude:7,
// entry_provider_key claude:7:key-71..73). poolStrategy stamps
// AttributePoolStrategy on every auth when non-empty.
func newPoolFailoverManager(t *testing.T, poolStrategy string, failAuthIDs map[string]struct{}) (*Manager, []*Auth, string) {
	t.Helper()
	// 1. Build executor whose executeFn fails request-scoped 400 + body
	//    `{"error":{"code":"invalid_request_error"}}` for auths in failAuthIDs,
	//    else returns a valid response payload.
	// 2. Register 3 auths with Attributes: provider_key/entry_provider_key
	//    (+ pool_strategy when non-empty), auth_kind apikey.
	// 3. Register model in the global registry like
	//    newClaudeCancellationTestManager does; return manager, auths, model.
}

func TestPoolStrategyRotatesOnRequestInvalidError(t *testing.T) {
	// fail auth[0] with 400 invalid_request_error; strategy "round-robin".
	// Manager.Execute(ctx, []string{"claude"}, req{model}, opts) must return
	// SUCCESS (the second entry served it), and executor calls must be 2
	// (auth0 failed, auth1 succeeded).
}

func TestPoolWithoutStrategyKeepsHardStop(t *testing.T) {
	// Same failure, NO pool_strategy attribute → Execute returns the 400
	// error directly; executor calls == 1.
}

func TestPoolStrategyExhaustedReturnsLastError(t *testing.T) {
	// ALL three entries fail with distinct 400 bodies; strategy set.
	// Execute returns the LAST attempted entry's error (not "no auth
	// available"), executor calls == 3.
}

func TestPoolStrategyRotationStaysInsidePool(t *testing.T) {
	// Register an outside auth (different provider_key claude:99, no pool
	// strategy) that would succeed. All pool entries fail. The outside auth
	// must NOT be picked: Execute still ends with the pool's last error and
	// the outside executor path is never invoked (track calls per auth ID).
}
```

Implement the helper fully (follow `newClaudeCancellationTestManager` for registry setup and `SetRetryConfig(0,0,0)`). The failing error: reuse `newFastDirectResponseTestError`-style `RequestTerminatedError{HTTPStatus: 400, Body: []byte(`{"error":{"code":"invalid_request_error"}}`)}` from conductor_fast_error_test.go — check `isRequestInvalidError` classifies it (it should via `hasRequestFaultBody`).

**Step 2: Run — expect FAIL on the rotation test**

```bash
go test ./sdk/cliproxy/auth/ -run TestPoolStrategy -v
```
Expected: `TestPoolStrategyRotatesOnRequestInvalidError` FAIL (error returned to caller, calls == 1). `TestPoolWithoutStrategyKeepsHardStop` should PASS already (today's behavior).

**Step 3: Implement the suppression**

In `conductor_execution.go` add a helper near `routeStrategyFromMetadata`:

```go
// poolStrategyFromAuth returns the canonical in-pool routing strategy stamped
// by the synthesizer for entry-bearing provider pools. Non-empty means the
// auth belongs to a pool that opted into aggressive failover: any entry
// error — including request-fault-classified ones — rotates to the next
// entry of the same pool before the error surfaces to the client.
func poolStrategyFromAuth(a *Auth) string {
	if a == nil {
		return ""
	}
	return strings.TrimSpace(a.Attributes[AttributePoolStrategy])
}
```

Replace each `isRequestInvalidError(errX)` hard-stop with:

```go
if isRequestInvalidError(errExec) && poolStrategyFromAuth(auth) == "" {
	return cliproxyexecutor.Response{}, errExec
}
```

at all 5 sites (378, 390, 513, 525, 692 — stream returns `nil, errStream`). Keep `MarkResult` untouched (cooldowns still recorded per existing classification).

**Pool scoping (Task 6b, same commit):** when the first suppression triggers — i.e. rotation continues past a request-fault on a pool auth — the `providers` list used by the next `pickNextMixed` must narrow to the pool. Read the failed auth's `provider_key`/`entry_provider_key`; the existing `authMatchesProvider` already matches pool keys, so the cleanest minimal mechanism: at loop top, if the last failure came from a pool auth (track `lastPoolProviderKey string`), set `providers = []string{poolKey}` for the remainder of this request. Only assign when `isCompoundRoutingKey(poolKey)` is true (guard `claude:7` style keys; a bare-channel pool like `openai-compatible-foo` passes through unchanged). Verify with the `TestPoolStrategyRotationStaysInsidePool` test.

**Step 4: Run all conductor tests**

```bash
go test ./sdk/cliproxy/auth/ 2>&1 | grep -E "FAIL|ok" | head
```
Expected: all PASS (existing suites must stay green — they cover the no-strategy path which is unchanged).

**Step 5: Commit**

```bash
gofmt -w sdk/cliproxy/auth/ && go test ./sdk/cliproxy/auth/ >/dev/null 2>&1 && \
git add sdk/cliproxy/auth/conductor_execution.go sdk/cliproxy/auth/pool_failover_test.go && \
git commit -m "feat(conductor): in-pool aggressive failover on any entry error"
```

---

### Task 7: Route default — pool strategy as fallback for pinned routes

**Files:**
- Modify: `sdk/api/handlers/handlers_routing.go:116` (`applyPinnedRoute`)
- Test: `sdk/api/handlers/handlers_provider_intersect_test.go` (append; it already tests pinned-route ordering)

**Step 1: Write the failing test**

Model on the existing pinned-route tests in `handlers_provider_intersect_test.go` — find how they build a `store.ModelRoute` and gin context, then add:

```go
// A route pinning a pool provider WITHOUT its own strategy falls back to the
// pool's row strategy (stashed as route_strategy) — but only when every
// pinned provider belongs to pools carrying the SAME strategy; mixed pools
// keep the global default. A route with an explicit strategy always wins.
```

Cases: (a) route {providers: ["claude:7"], strategy: ""} + pool strategy "failover" → `RouteStrategyFor(ginCtx) == "failover"`; (b) route strategy "priority" + pool "round-robin" → stashed "priority" (route wins); (c) no route at all → nothing stashed.

**Step 2: Run — expect FAIL on case (a).**

**Step 3: Implement**

`applyPinnedRoute` needs the pool strategies of the pinned providers. The handler layer cannot read auth attributes directly (they live on the auth manager) — use the resolver the handlers already hold: check how `LiveProviderKeysForModel`/`authManager` is reachable from `BaseAPIHandler` (grep `AuthManager` in `sdk/api/handlers/handlers.go`), and add a small method on the auth manager surface (`func (m *Manager) PoolStrategyForProviderKey(key string) string` — scans auths whose routing key matches `key` and returns the shared non-empty `pool_strategy`, "" when none/mixed). Put the method in `sdk/cliproxy/auth/conductor_selection.go` next to the routing-key helpers. `applyPinnedRoute` is a free function — pass the resolved strategy in from `getRequestDetailsWithOptions` via a new parameter or make it a method on the handler; follow whichever keeps the diff smaller (a method on `BaseAPIHandler` is fine — it has `h.AuthManager`).

Logic in `applyPinnedRoute`:
```go
	strategy := strings.ToLower(strings.TrimSpace(route.Strategy))
	if strategy == "" {
		// Pool-row strategy becomes the route default when every pinned
		// provider carries the same one; mixed pools keep the global.
		strategy = h.poolStrategyForProviders(filtered) // "" when unset/mixed
	}
	if strategy == "priority" || strategy == "failover" || strategy == "round-robin" || strategy == "fill-first" || strategy == "weighted-round-robin" {
		filtered = orderProvidersByPriority(filtered, route.Priorities)
		stashRouteStrategy(ctx, strategy)
	}
```
Extend `routeStrategyFromMetadata` (`conductor_execution.go:1091`) to map the two extra canonical values:
```go
	case "fill-first":
		return schedulerStrategyFillFirst
	case "round-robin":
		return schedulerStrategyRoundRobin
```
(`weighted-round-robin` maps to `schedulerStrategyWeightedRoundRobin` — add it too.)

**Step 4: Run — PASS; handlers suites green.**

```bash
gofmt -w sdk/api/handlers/ sdk/cliproxy/auth/ && go test ./sdk/api/handlers/ ./sdk/cliproxy/auth/ 2>&1 | grep -E "FAIL|ok"
```

**Step 5: Commit**

```bash
git add sdk/api/handlers/ sdk/cliproxy/auth/
git commit -m "feat(routing): pool row strategy defaults for routes pinning the pool"
```

---

### Task 8: Dashboard — schema, editor, form, payload

**Files:**
- Modify: `web/dashboard/src/pages/UpstreamProvidersPage.jsx` — `buildSchemas` (~1359), `APIKeyEntriesEditor` (~3133), `validateAPIKeyEntries` (~3241), `hydrateEntries` (~3298), `buildForm` (~3317), `buildPayload` (~3394)
- Test: `web/dashboard/src/pages/UpstreamProvidersPage.test.jsx` (check existing test file name in the directory first)

**Step 1: Write the failing tests** (follow the file's existing test style — they test `buildForm`/`buildPayload`/`validateAPIKeyEntries` as pure exports):

- `validateAPIKeyEntries`: priority blank = ok; `"10"` = ok; `"-3"` = ok (negative tiers are legal — `min: 0` on the row-level number input is a UI choice for rows; per-entry priority mirrors the Go `*int` with no sign constraint — but keep parity with the row-level UI: accept any integer, including 0 and negatives); `"abc"`, `"1.5"` = error.
- `buildForm`: `routing_strategy: 'failover'` hydrates; entry `priority: 10` hydrates as `10`, null → `''`.
- `buildPayload`: strategy only emitted when non-empty; entry priority emitted only when filled and integer.

**Step 2: Run — expect FAIL.**

```bash
cd web/dashboard && npx vitest run src/pages/UpstreamProvidersPage.test.jsx 2>&1 | tail -20
```
(If the project uses `npm test --` or `npx jest`, match whatever `package.json` scripts exist.)

**Step 3: Implement**

`buildSchemas` — add to BOTH `claude-api-key` Identity section and `openai-compatibility` (Endpoint or Identity section) the strategy select:

```js
const ROUTING_STRATEGY_OPTIONS = [
  { value: '', label: 'Default' },
  { value: 'round-robin', label: 'Round-robin' },
  { value: 'weighted-round-robin', label: 'Weighted round-robin' },
  { value: 'fill-first', label: 'Fill-first (priority)' },
  { value: 'failover', label: 'Failover' },
];
// field:
{ name: 'routing_strategy', label: 'Routing strategy', type: 'select',
  options: ROUTING_STRATEGY_OPTIONS,
  hint: 'Empty = global routing strategy. Any value enables aggressive in-pool failover: on any entry error the next entry is tried first; errors surface only after the whole pool is exhausted. fill-first ≈ priority, failover ≈ round-robin within a priority tier.' },
```

Note: the UI offers `failover` (an input-boundary alias) per the approved design; the backend canonicalizes it. Keep `priority` out of the UI select (it's an alias of fill-first already listed).

`APIKeyEntriesEditor` — add a priority input next to weight in each row (mirror the weight input; `aria-label="API key entry priority"`, `data-testid={`api-key-entry-priority-${idx}`}`, placeholder `"priority"`, title `"Selection tier within this pool. Blank = inherit row priority."`), plus the per-row error slot `rowErr.priority`. `add()` gains `priority: ''`.

`validateAPIKeyEntries` — add next to the weight block:
```js
    const prioRaw = e && e.priority;
    if (prioRaw !== undefined && prioRaw !== null && String(prioRaw).trim() !== '') {
      const s = String(prioRaw).trim();
      const n = Number(s);
      if (!Number.isFinite(n) || !/^-?\d+$/.test(s) || Math.floor(n) !== n) {
        errs.priority = 'Priority must be a whole number (blank = inherit the row priority).';
      }
    }
```

`hydrateEntries` — add `priority: e && e.priority !== undefined && e.priority !== null ? e.priority : ''`.
`buildForm` — `routing_strategy: src.routing_strategy ?? ''` in `base`; also thread through `carry` if the file's carry-over pattern requires it (check how `priority` row-level is carried).
`buildPayload` — in the `openai || claude-api-key` branch:
```js
    const strategy = (form.routing_strategy || '').trim();
    if (strategy) payload.routing_strategy = strategy;
```
and in the entry mapping, after the weight block:
```js
        const prioRaw = e.priority;
        if (prioRaw !== undefined && prioRaw !== null && String(prioRaw).trim() !== '') {
          const s = String(prioRaw).trim();
          const n = Number(s);
          if (Number.isFinite(n) && /^-?\d+$/.test(s) && Math.floor(n) === n) {
            entry.priority = Math.trunc(n);
          }
        }
```

**Step 4: Run tests + build**

```bash
cd web/dashboard && npx vitest run src/pages/ 2>&1 | tail -10 && npm run build 2>&1 | tail -5
```

**Step 5: Commit**

```bash
git add web/dashboard/
git commit -m "feat(dashboard): routing strategy select + per-entry priority editor"
```

---

### Task 9: Full Go regression + dash-embed

**Step 1:** Full build + tests from the worktree root:

```bash
gofmt -w . && go build -o test-output ./cmd/server && rm test-output && go test ./... 2>&1 | grep -E "^(FAIL|--- FAIL)" ; go test ./... 2>&1 | grep -c "^ok"
```

Acceptance: **zero new FAILs** vs the recorded baseline: the 3 executor header tests + `TestMountDashboardRoutes` (dist not built yet) are the known pre-existing failures; nothing else.

**Step 2:** Embed the dashboard so `TestMountDashboardRoutes` goes green:

```bash
make dash-embed && go test ./internal/api/ -run TestMountDashboardRoutes -v
```

**Step 3:** Manual sanity (optional but recommended): boot the dev server, save a Claude provider with `failover` + two entries with priorities 10/1, re-edit (values hydrate), confirm `GET /v0/management/upstream-providers` returns `routing_strategy` and per-entry `priority`.

**Step 4: Commit**

```bash
git add -A && git commit -m "chore: embed dashboard build for entry routing strategy"
```

---

### Task 10: Docs + config example

**Files:**
- Modify: `config.example.yaml` — `openai-compatibility` block (~line 545): add `strategy:` under the pool with a comment; `claude-api-key` block (~line 464): document `upstream-provider-strategy` as renderer-managed; entries: document `priority`.
- Modify: `docs/plans/2026-09-03-upstream-entry-routing-strategy-design.md` — add "Status: Implemented" if the team convention is to stamp it.

**Step 1:** Add to `config.example.yaml` inside the openai-compatibility sample:

```yaml
#     strategy: "failover" # optional: in-pool selection strategy for this provider's api-key-entries.
#                          # round-robin | weighted-round-robin | fill-first (priority/failover are accepted aliases).
#                          # Empty = follow routing.strategy. Any value enables aggressive in-pool failover:
#                          # on any entry error the next entry is tried before the error reaches the client.
```
and on an entry line: `#         priority: 10 # optional: selection tier within the pool; higher served first; blank = inherit pool priority`

For `claude-api-key`, add one commented example item showing `priority: 10` per key and a note that `upstream-provider-strategy` is stamped by the renderer.

**Step 2:** Commit

```bash
git add config.example.yaml docs/plans/2026-09-03-upstream-entry-routing-strategy-design.md
git commit -m "docs: document pool routing strategy and entry priority"
```

---

## Execution notes

- **Do not break the no-strategy path.** Every conductor change is guarded by `poolStrategyFromAuth(auth) == ""` → old behavior. The existing test suites are the regression net for this.
- **PG tests skip without `PGSTORE_TEST_DSN`.** That's acceptable in dev; ensure compile stays green. If a DSN is available locally, run them for real.
- **Aliases canonicalize at boundaries** (store read/parse → `NormalizePoolRoutingStrategy`), and every downstream comparison uses canonical values only.
- **Task 6's pool-scope narrowing** is the only behavioral subtlety: re-read `pickNextMixed`'s provider filtering (`conductor_selection.go:1761`) before implementing; `tried` already excludes failed entries, and the narrowing only prevents *cross-pool* picks after the first pool failure.
