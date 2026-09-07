# OpenCode Go Upstream Provider — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a first-class `opencode-go` Upstream Provider type with per-model wire-format dispatch (openai/anthropic), a seed + manual-refresh model catalog, and a manual per-entry quota check button in the dashboard.

**Architecture:** New provider type flows through the existing normalized-provider pipeline: PG row → `upstreamsync` renderer → `config.Config.OpenCodeGo` section → `ConfigSynthesizer` auths → new `OpenCodeGoExecutor` that composes the existing `OpenAICompatExecutor` (openai wire) and `ClaudeExecutor` (anthropic wire) and dispatches per model via a wire-format map built from config. Quota/seed/refresh are management endpoints following the existing `/upstream-providers/:id/test` pattern. Design doc: `docs/plans/2026-09-07-opencode-go-upstream-provider-design.md`.

**Tech Stack:** Go 1.26 (gin, pgx), React + Vite dashboard SPA.

**Working directory:** all work happens in the worktree `.worktrees/opencode-go-provider` (branch `feature/opencode-go-provider`).

**Baseline known failures (pre-existing on main, NOT ours to fix):**
- `TestApplyClaudeHeaders_DisableDeviceProfileStabilization`, `TestApplyClaudeHeaders_LegacyModePreservesConfiguredUserAgentOverrideForClaudeClients`, `TestClaudeExecutor_NonClaudeRequestUsesClaudeCode220CLIFingerprint` (machine-dependent OS fingerprint).
- Every command below runs from the worktree root unless stated otherwise.

---

### Task 1: Config structs — `OpenCodeGoKey` / `OpenCodeGoModel`

**Files:**
- Modify: `internal/config/config_types.go` (add after `OpenAICompatibilityModel`, ~line 870)
- Modify: `internal/config/config.go` (find the `Config` struct; add the field next to `OpenAICompatibility`)

**Step 1: Add the model + key structs** in `config_types.go`:

```go
// OpenCodeGoModel is one upstream model on an opencode-go provider row,
// carrying the wire format the upstream expects for that model.
type OpenCodeGoModel struct {
	// Name is the actual model name used by the upstream provider.
	Name string `yaml:"name" json:"name"`
	// Alias is the model name alias that clients will use to reference this model.
	Alias string `yaml:"alias" json:"alias"`
	// DisplayName is the optional human-readable name shown in model catalogs.
	DisplayName string `yaml:"display-name,omitempty" json:"display-name,omitempty"`
	// MaxContextLength overrides the context window advertised to Codex clients.
	MaxContextLength int `yaml:"max-context-length,omitempty" json:"max-context-length,omitempty"`
	// ForceMapping rewrites upstream response model fields back to Alias.
	ForceMapping bool `yaml:"force-mapping,omitempty" json:"force-mapping,omitempty"`
	// WireFormat selects the upstream protocol for this model: "openai"
	// (default, /chat/completions + Bearer) or "anthropic"
	// (/v1/messages + x-api-key). Unknown values fall back to "openai".
	WireFormat string `yaml:"wire-format,omitempty" json:"wire-format,omitempty"`
	// Thinking configures the thinking/reasoning capability for this model.
	Thinking *registry.ThinkingSupport `yaml:"thinking,omitempty" json:"thinking,omitempty"`
}

func (m OpenCodeGoModel) GetName() string { return m.Name }
func (m OpenCodeGoModel) GetAlias() string { return m.Alias }
```

Check whether `buildConfigModels[T modelEntry]` in `sdk/cliproxy/service_models.go` requires more getter methods; add `GetDisplayName()`/`GetMaxContextLength()` mirroring `OpenAICompatibilityModel` if the generic constraint demands them.

```go
// OpenCodeGoKey is one credential entry of an opencode-go provider row.
type OpenCodeGoKey struct {
	APIKey string `yaml:"api-key" json:"api-key"`
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	UpstreamProviderEntryID int64 `yaml:"upstream-provider-entry-id,omitempty" json:"-"`
	// Weight / Priority / ProxyURL / ProxyPoolID / RelayBaseURL mirror
	// OpenAICompatibilityAPIKey exactly — copy the fields + yaml tags from
	// that struct (including the doc comments).
	...
}
```

Copy the remaining fields verbatim from `config.OpenAICompatibilityAPIKey` (Weight, Priority, Disabled, ProxyURL, ProxyPoolID, RelayBaseURL) — read that struct first and mirror it.

**Step 2: Add the row struct + Config field.** In `config_types.go`, add `OpenCodeGo` struct mirroring `OpenAICompatibility` (Name, Priority, Strategy, Disabled, Prefix, BaseURL, ProxyURL, ProxyPoolID, RelayBaseURL, APIKeyEntries []OpenCodeGoKey, Models []OpenCodeGoModel, Headers, DisableCooling). In the `Config` struct add:

```go
	// OpenCodeGo holds opencode-go upstream provider rows rendered from the
	// upstream_providers table.
	OpenCodeGo []OpenCodeGo `yaml:"opencode-go,omitempty" json:"opencode-go,omitempty"`
```

**Step 3: Verify compile.**

Run: `go build ./... 2>&1 | head` — expect no errors about the new symbols.

**Step 4: Commit.**

```bash
git add internal/config/config_types.go internal/config/config.go
git commit -m "feat(config): OpenCodeGo provider structs with per-model wire format"
```

---

### Task 2: Store — `wire_format` column + model field

**Files:**
- Modify: `internal/store/postgresstore.go:1651-1671` (models table DDL + fork migration block)
- Modify: `internal/store/pg_upstream_providers.go` (UpstreamProviderModel struct + scan/write sites)
- Test: `internal/store/pg_upstream_providers_test.go` (extend an existing round-trip test)

**Step 1: Write the failing test.** Find an existing test that creates a provider with models and reads it back (search `UpstreamProviderModel` in `pg_upstream_providers_test.go`). Add a case asserting `wire_format` round-trips:

```go
// inside the existing round-trip test's model slice:
//   {Name: "minimax-m3", WireFormat: "anthropic"},
// then after re-read:
//   if got := out.Models[0].WireFormat; got != "anthropic" { t.Fatalf(...) }
```

**Step 2: Run to verify failure.**

Run: `go test ./internal/store/ -run TestUpstreamProvider -v 2>&1 | tail -5`
Expected: FAIL — `WireFormat` field does not exist yet (compile error counts as the failure).

**Step 3: Implement.**

In `pg_upstream_providers.go`: add `WireFormat string \`json:"wire_format,omitempty"\`` to the models struct (find `type UpstreamProviderModel` — check exact name with `grep -n "UpstreamProviderModel struct" internal/store/pg_upstream_providers.go` or the shared types file). Update the models INSERT/SELECT/scan sites the same way the `fork` column is threaded (search `fork` in the file to find every site — there are typically 3: create, update, scan).

In `postgresstore.go`, inside the models DDL after `thinking JSONB,` add `wire_format TEXT NOT NULL DEFAULT 'openai',`, and after the fork ALTER block add:

```go
	// Idempotently add wire_format for databases running an older schema.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS wire_format TEXT NOT NULL DEFAULT 'openai'`,
		upstreamModelsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter upstream_provider_models add wire_format column: %w", err)
	}
```

**Step 4: Run tests to verify pass.**

Run: `go test ./internal/store/ -run TestUpstreamProvider 2>&1 | tail -3` — expect ok.

**Step 5: Commit.**

```bash
git add internal/store/postgresstore.go internal/store/pg_upstream_providers.go internal/store/pg_upstream_providers_test.go
git commit -m "feat(store): wire_format column on upstream_provider_models"
```

---

### Task 3: Renderer — `upstreamsync` support

**Files:**
- Modify: `internal/upstreamsync/render.go:28-44` (constants), `:147-166` (RenderConfigWithPools switch), new helper near `openAICompatFromProviderWithPools` (~line 310)
- Test: `internal/upstreamsync/render_test.go` or a new `opencode_go_render_test.go`

**Step 1: Write the failing test.**

```go
func TestRenderOpenCodeGo(t *testing.T) {
	p := store.UpstreamProvider{
		ID:           7,
		ProviderType: TypeOpenCodeGo,
		Name:         "ocgo",
		BaseURL:      "https://opencode.ai/zen/go/v1",
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{ID: 3, APIKey: "sk-1", Name: "main"},
		},
		Models: []store.UpstreamProviderModel{
			{Name: "glm-5.2", Alias: "glm-5.2"},
			{Name: "minimax-m3", Alias: "minimax-m3", WireFormat: "anthropic"},
		},
		Headers: map[string]string{"User-Agent": "opencode"},
	}
	cfg := RenderConfig([]store.UpstreamProvider{p})
	if len(cfg.OpenCodeGo) != 1 { t.Fatalf("want 1 opencode-go row, got %d", len(cfg.OpenCodeGo)) }
	row := cfg.OpenCodeGo[0]
	if len(row.APIKeyEntries) != 1 || row.APIKeyEntries[0].APIKey != "sk-1" { t.Fatalf("entries not rendered") }
	if row.APIKeyEntries[0].UpstreamProviderEntryID != 3 { t.Fatalf("entry id not stamped") }
	if row.Models[0].WireFormat != "" || row.Models[0].Name != "glm-5.2" { t.Fatalf("openai model wrong") }
	if row.Models[1].WireFormat != "anthropic" { t.Fatalf("anthropic wire format lost") }
	if row.Headers["User-Agent"] != "opencode" { t.Fatalf("headers lost") }
	// UpstreamProviderID must be stamped for the routing key.
	if row.UpstreamProviderID != 7 { t.Fatalf("row id not stamped") }
}
```

**Step 2: Run to verify failure.** `go test ./internal/upstreamsync/ -run TestRenderOpenCodeGo -v` — FAIL (no TypeOpenCodeGo).

**Step 3: Implement.** In `render.go`:

1. Constant: `TypeOpenCodeGo = "opencode-go"` in the const block.
2. Switch case in `RenderConfigWithPools`: `case TypeOpenCodeGo: cfg.OpenCodeGo = append(cfg.OpenCodeGo, openCodeGoFromProviderWithPools(p, pools))`.
3. Helper modeled directly on `openAICompatFromProviderWithPools` (read it first): resolveBinding for row proxy, iterate entries skipping disabled, stamp `UpstreamProviderEntryID`/Weight/Priority, map models copying `WireFormat`, headers, strategy normalization, disable_cooling from ExtraConfig — same field-for-field shape as the compat helper.

**Step 4: Verify pass** — `go test ./internal/upstreamsync/` — ok. Run `gofmt -w internal/upstreamsync internal/config internal/store`.

**Step 5: Commit** — `feat(upstreamsync): render opencode-go rows into config section`.

---

### Task 4: Routing key — `util.UpstreamProviderKey` case

**Files:**
- Modify: `internal/util/upstream_provider_key.go:33-70` (switch)
- Test: `internal/util/upstream_provider_key_test.go` (create if missing — check with `ls internal/util/`)

**Step 1: Write failing test:**

```go
func TestUpstreamProviderKeyOpenCodeGo(t *testing.T) {
	if got := UpstreamProviderKey("opencode-go", "ocgo", 7); got != "opencode-go:7" {
		t.Fatalf("got %q", got)
	}
	if got := UpstreamProviderKey("opencode-go", "ocgo", 0); got != "opencode-go" {
		t.Fatalf("legacy bare key: got %q", got)
	}
}
```

**Step 2: Verify fail**, then **Step 3: implement** — in the switch, add before the `-api-key` suffix case:

```go
	case pt == "opencode-go":
		// opencode-go rows get per-row routing keys like the built-in
		// api-key channels; rowID==0 keeps the legacy bare channel key.
		if rowID <= 0 {
			return "opencode-go"
		}
		return "opencode-go:" + strconv.FormatInt(rowID, 10)
```

**Step 4: pass**, **Step 5: commit** — `feat(util): opencode-go routing key`.

---

### Task 5: Synthesizer — auths from `cfg.OpenCodeGo`

**Files:**
- Modify: `internal/watcher/synthesizer/config.go:110-130` (Synthesize list), new `synthesizeOpenCodeGoKeys` after `synthesizeClaudeKeys` (~line 300)
- Test: `internal/watcher/synthesizer/config_test.go`

**Step 1: Write failing test** modeled on an existing key-synthesizer test (find one with `grep -n "synthesizeClaudeKeys\|synthesizeXAI" internal/watcher/synthesizer/config_test.go | head -3` and copy its fixture shape). Assert: auth.Provider == "opencode-go", api_key + base_url attrs present, `provider_key` attr == "opencode-go:<id>" when UpstreamProviderID set, entry_provider_key compound key when UpstreamProviderEntryID set, models_hash computed.

**Step 2: verify fail. Step 3: implement** — copy `synthesizeClaudeKeys` (read it: lines 202-297) and adjust:
- iterate `cfg.OpenCodeGo` fan-out style (note: ClaudeKey fans out one row per entry via the renderer; OpenCodeGo renders ONE row with N entries — so iterate `row.APIKeyEntries` inside, building one auth per entry, mirroring how `synthesizeOpenAICompat` handles entries — read that function first and follow IT, since its shape (row + entries) matches ours).
- `addUpstreamProviderKey(attrs, "opencode-go", row.UpstreamProviderID)`; entry key via the same helper pattern used for compat entries (`addOpenAICompatEntryProviderKey`-style).
- `id, token := idGen.Next("opencode-go:apikey", key, base)`, Label "opencode-go-apikey", Provider "opencode-go".
- Stamp `models_hash` via the models-hash helper (check `diff.Compute*ModelsHash` — if there is no OpenCodeGo variant, hash the alias list the same way `ComputeGeminiModelsHash` does; add `ComputeOpenCodeGoModelsHash` in the diff package only if trivially mirrorable, else compute inline identically).
- headers, priority, weight, relay/proxy, disable_cooling: same treatment as claude/compat.

**Step 4: pass. Step 5: commit** — `feat(synthesizer): opencode-go config auths`.

---

### Task 6: Executor — `OpenCodeGoExecutor` with per-model dispatch

**Files:**
- Create: `internal/runtime/executor/opencode_go_executor.go`
- Create: `internal/runtime/executor/opencode_go_executor_test.go`
- Modify: `sdk/cliproxy/service_executors.go:264-296` (registration switch — add `case "opencode-go":`)

**Design (follow XAIAutoExecutor's composition pattern, read it first — xai_websockets_executor.go:1586-1660):**

```go
// OpenCodeGoExecutor routes opencode-go traffic to the right wire format per
// model: models whose config carries wire_format "anthropic" execute through
// the shared Claude executor path (/v1/messages, x-api-key); everything else
// (the default) goes through the OpenAI-compat path (/chat/completions,
// Bearer). Both delegate executors read base_url/api_key from the auth's
// attributes, so no credential handling lives here.
type OpenCodeGoExecutor struct {
	openaiExec *OpenAICompatExecutor
	claudeExec *ClaudeExecutor
	cfg        *config.Config
}

func NewOpenCodeGoExecutor(cfg *config.Config) *OpenCodeGoExecutor {
	return &OpenCodeGoExecutor{
		openaiExec: NewOpenAICompatExecutor("opencode-go", cfg),
		claudeExec: NewClaudeExecutor(cfg),
		cfg:        cfg,
	}
}

func (e *OpenCodeGoExecutor) Identifier() string { return "opencode-go" }

// wireFormat resolves the upstream protocol for a model: "anthropic" only
// when a matching configured model (by alias or name) says so; anything else
// falls back to the openai path (safe majority default).
func (e *OpenCodeGoExecutor) wireFormat(model string) string {
	base := thinking.ParseSuffix(model).ModelName // same suffix-strip the executors do
	for i := range e.cfg.OpenCodeGo {
		for _, m := range e.cfg.OpenCodeGo[i].Models {
			if m.Alias == base || m.Name == base {
				return m.WireFormat
			}
		}
	}
	return ""
}
```

Dispatch in Execute/ExecuteStream/CountTokens:

```go
func (e *OpenCodeGoExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.wireFormat(req.Model) == "anthropic" {
		return e.claudeExec.Execute(ctx, auth, req, opts)
	}
	return e.openaiExec.Execute(ctx, auth, req, opts)
}
```

Mirror the same dispatch for `ExecuteStream` and `CountTokens` (check which methods the executor interface requires — copy the exact method set XAIAutoExecutor implements). Do NOT implement websocket/session methods beyond what the interface demands.

**Registration** in `service_executors.go` switch: add

```go
	case "opencode-go":
		s.coreManager.RegisterExecutor(executor.NewOpenCodeGoExecutor(cfg))
```

**TDD steps:**

1. **Write failing tests** in `opencode_go_executor_test.go`. Pattern: build `config.Config{OpenCodeGo: []config.OpenCodeGo{{BaseURL: srv.URL, Models: ...}}}`, an `auth.Attributes` map with `base_url`/`api_key` pointing at an `httptest.Server` that records the request path + headers, and call Execute. Three cases:
   - openai model → upstream receives `POST /chat/completions` with `Authorization: Bearer sk-x`; respond `{"id":"1","choices":[{"message":{"role":"assistant","content":"hi"}}]}`.
   - anthropic model (`WireFormat: "anthropic"`) → upstream receives path ending `/messages` with `x-api-key: sk-x` and `anthropic-version` header; respond a minimal Claude messages JSON `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`.
   - unknown model (not in config) → falls to openai path.
   NOTE: check how existing claude/openai-compat executor tests stub auth+cfg (claude_executor_test.go, openai_compat_executor*.go tests) and reuse their fixtures — ClaudeExecutor may demand more auth attributes (e.g. api_key attr) than the bare minimum; satisfy whatever `resolveCredentials` reads.
2. Verify fail (NewOpenCodeGoExecutor undefined).
3. Implement.
4. Verify pass: `go test ./internal/runtime/executor/ -run TestOpenCodeGo -v`.
5. Commit — `feat(executor): opencode-go executor with per-model wire-format dispatch`.

---

### Task 7: Management — seed catalog + `POST /:id/seed-models`

**Files:**
- Create: `internal/api/handlers/management/upstream_providers_seedmodels.go`
- Create: `internal/api/handlers/management/upstream_providers_seedmodels_test.go`
- Modify: `internal/api/server_management.go:356` (add route under the test route)

**Step 1: Catalog.** Static slice (data from OmniRoute's go-tier registry):

```go
// openCodeGoSeedModels is the initial opencode-go model catalog, ported from
// OmniRoute's opencode-go registry. WireFormat "" means openai (the default).
var openCodeGoSeedModels = []store.UpstreamProviderModel{
	{Name: "glm-5.2"}, {Name: "glm-5.1"}, {Name: "glm-5"},
	{Name: "kimi-k2.7-code"}, {Name: "kimi-k2.6"}, {Name: "kimi-k2.5"},
	{Name: "kimi-k3"}, {Name: "kimi-k3-max"},
	{Name: "mimo-v2.5-pro"}, {Name: "mimo-v2.5"},
	{Name: "minimax-m2.7"}, {Name: "minimax-m2.5"},
	{Name: "minimax-m3", WireFormat: "anthropic"},
	{Name: "qwen3.7-max", WireFormat: "anthropic"},
	{Name: "qwen3.7-plus", WireFormat: "anthropic"},
	{Name: "qwen3.6-plus-high", WireFormat: "anthropic"},
	{Name: "qwen3.6-plus-max", WireFormat: "anthropic"},
	{Name: "hy3"}, {Name: "hy3-preview"},
	{Name: "muse-spark-1.2-contributor"},
	{Name: "grok-4.5"},
	{Name: "deepseek-v4-pro"}, {Name: "deepseek-v4-flash"},
	{Name: "ox-alpha-free"},
}
```

**Step 2: Handler.** `POST /upstream-providers/:id/seed-models` — no body. Load row (store Get), skip seed names already present (exact match on `Name`), append the rest via store Update (read how UpdateUpstreamProvider persists models — reuse `toUpstreamProvider`/store Update; keep models list = existing + new). Respond `{"added": N, "total": len(models)}`. Idempotent: second call → `added: 0`.

**Step 3: TDD** — test first (seed into empty row → 24 added; seed again → 0 added; pre-existing custom model untouched). Use the same fake in-memory store the existing upstream_providers tests use (find it: `grep -n "fake\|stub\|mock" internal/api/handlers/management/upstream_providers_test.go | head -5` — reuse that fixture).

**Step 4: route + verify + commit** — `feat(management): opencode-go seed-models endpoint`.

---

### Task 8: Management — `POST /:id/refresh-models`

**Files:**
- Create: `internal/api/handlers/management/upstream_providers_refreshmodels.go` (+ test file)

Deviates slightly from the design doc (single server-side merge instead of client merge) — fewer round-trips, keys never leave the server.

**Step 1: Failing test** — httptest upstream serving `{"object":"list","data":[{"id":"new-model-a"},{"id":"glm-5.2"}]}` as `GET /v1/models`; call handler; assert `{"added":1,"models":[...]}` and the store row now contains `new-model-a` with `WireFormat: ""` while `glm-5.2` (pre-seeded) is not duplicated.

**Step 2: Implement.** Handler flow: load row → pick key: entry from body `{entry_id}` or first non-disabled entry with non-empty key, else row.APIKey → GET `<base_url>/models` (base_url default `https://opencode.ai/zen/go/v1`; strip trailing slash, append `/models`) with `Authorization: Bearer <key>`; 8s `context.WithTimeout` (management probe precedent: `modelHealthProbeTimeout` in api_tools.go) → parse `{data:[{id}]}` → merge-by-name (add missing only, never remove) → store Update → respond `{"added":N,"total":M}`. Errors (401/404/network/timeout/malformed) → `502 {"error": ...}` without touching the store. Log no keys.

**Step 3: verify + commit** — `feat(management): opencode-go refresh-models endpoint`.

---

### Task 9: Management — `POST /:id/quota` (per-entry quota check)

**Files:**
- Create: `internal/api/handlers/management/upstream_providers_quota.go`
- Create: `internal/api/handlers/management/upstream_providers_quota_test.go`
- Modify: `internal/api/server_management.go` (route next to `/test`)
- Modify: `AGENTS.md` (timeouts exception list — append "the opencode-go quota fetch timeout in `internal/api/handlers/management/upstream_providers_quota.go`")

**Response shape:**

```go
// upstreamProviderQuotaResponse reports one manual quota probe for a single
// api_key_entry. Fail-open: any failure yields ok=false + error, never
// mutates entry state or cooldowns.
type upstreamProviderQuotaResponse struct {
	OK      bool                       `json:"ok"`
	EntryID *int64                     `json:"entry_id,omitempty"`
	Windows []upstreamProviderQuotaWin `json:"windows,omitempty"`
	Raw     string                     `json:"raw,omitempty"`
	Error   string                     `json:"error,omitempty"`
}
type upstreamProviderQuotaWin struct {
	Key      string  `json:"key"` // "5h" | "weekly" | "monthly"
	Used     float64 `json:"used"`
	Limit    float64 `json:"limit"`
	Percent  float64 `json:"percent_used"` // 0..100, clamped; -1 when limit<=0
	ResetAt  string  `json:"reset_at,omitempty"` // RFC3339, epoch s or ms accepted
}
```

**Handler flow:** load row → resolve entry (body `{entry_id}` required; error 400 when missing or not found) → key := entry.APIKey (404 clear message when empty) → url := `extra_config["quota_url"]` if set, else `https://opencode.ai/zen/go/v1/quota` → GET with Bearer, 8s timeout →
- 200: parse body. Window wrapper key: `quota` | `data` | `usage` (top-level object). Window keys + aliases: `5h|hourly|short`, `weekly|week|wk`, `monthly|month|mo`; each `{used, limit, reset_at}` (reset_at epoch: <1e12 = seconds, else ms; also accept `reset_after_seconds` → now+that). Percent = used/limit*100 clamped [0,100], limit<=0 → -1. Respond ok:true + windows + raw (truncate raw to 8KB).
- 404/405: ok:false, error `"quota endpoint not available upstream (HTTP 404) — set extra_config.quota_url when OpenCode publishes an official endpoint"`.
- 401/403: ok:false, error `"upstream rejected the API key (HTTP <code>)"`.
- timeout/network/malformed: ok:false with `err.Error()`.

Never log the key. Respond HTTP 200 even when ok:false (the probe itself succeeded; the *upstream* failed) — mirrors the /test handler.

**TDD:** stub server cases: happy 3-window, aliases (`hourly`/`wk`/`mo`), 404, 401, timeout (stub sleeps 20ms, handler timeout 8s — use a 1ns timeout override? No: make the timeout a package var `openCodeQuotaFetchTimeout = 8 * time.Second` so the test can shrink it), malformed JSON, epoch-ms reset_at, quota_url override from extra_config.

**Commit** — `feat(management): per-entry opencode-go quota probe endpoint` + AGENTS.md line.

---

### Task 10: Dashboard — provider type, wire_format editor, quota + refresh buttons

**Files:**
- Modify: `web/dashboard/src/pages/UpstreamProvidersPage.jsx` (type dropdown, base_url prefill, seed call after create, refresh button)
- Modify: the upstream provider editor component (find it: `grep -rn "api_key_entries\|TestUpstreamProvider\|/test" web/dashboard/src/components/*.jsx web/dashboard/src/api/*.js* | head -10`)
- Modify: `web/dashboard/src/api/` client module (add `seedModels`, `refreshModels`, `quota` calls — follow the existing `testProvider` API helper)
- Test: `web/dashboard/src/pages/UpstreamProvidersPage.test.js` or component test file (follow `ProxyPoolsPage.test.js` pattern)

**Step 1: API client** — three helpers next to the existing test-panel helpers:
- `seedOpenCodeGoModels(id)` → POST `/v0/management/upstream-providers/${id}/seed-models`
- `refreshOpenCodeGoModels(id, entryId)` → POST `.../${id}/refresh-models` `{entry_id}`
- `fetchOpenCodeGoQuota(id, entryId)` → POST `.../${id}/quota` `{entry_id}`

**Step 2: Provider create form** — add "OpenCode Go" option to the provider-type dropdown (find the options array — search `openai-compatibility` in the page). When selected: prefill `base_url = 'https://opencode.ai/zen/go/v1'`; after a successful create, call seed + then normal reload; surface `added` count as a toast/alert consistent with the page's existing feedback style.

**Step 3: Entry actions** — in the api_key_entries editor, next to the existing Test button, render a Quota button only when `provider_type === 'opencode-go'`. On click: call quota API, render inline panel below the entry:
- three window rows: label (5h / Weekly / Monthly), `used / limit`, percent bar (reuse the page's existing bar/progress styling if any; else a simple div with width%), reset time formatted via the page's date helper.
- percent -1 → show "—".
- ok:false → error text; 404 message maps to badge text "Quota API belum tersedia di upstream" plus hint `extra_config.quota_url`.
Loading + error states mirror the existing Test button panel (find its state handling and copy the shape).

**Step 4: Refresh models button** — row-level (next to seed/save), shown only for opencode-go rows: dropdown of entries (default first active) → call refresh → alert `added N models`.

**Step 5: Model editor** — in the models table of the editor, when provider_type is opencode-go add a `wire_format` select (`openai` default, `anthropic`), sent as `wire_format` on the model object (API already round-trips it via Task 2/3 once `upstreamProviderModelReq` gets the field — check Task 2 note below).

**IMPORTANT — Task 2 gap:** add `WireFormat string \`json:"wire_format,omitempty"\`` to `upstreamProviderModelReq` in `internal/api/handlers/management/upstream_providers_types.go:54` and thread it through `toUpstreamProvider` (find where model reqs map to store models in `upstream_providers.go`) so the REST API round-trips it. Do this as part of Task 2's commit scope (store field) or a follow-up commit in Task 2 — verify with a management test extension (create row with wire_format via PUT, GET returns it).

**Step 6: Dashboard test** — helper test: quota response → three window rows render, 404 error → badge text, percent -1 → "—". Follow ProxyPoolsPage.test.js structure.

**Step 7: Verify.** `cd web/dashboard && npm test -- --run 2>&1 | tail -5` (or the repo's test script — check package.json) and `npm run build`. Then from worktree root: `make dash-embed` (copies dist into internal/dashboardasset + rebuilds the Go binary).

**Step 8: Commit** — `feat(dashboard): opencode-go provider type with per-entry quota + refresh`.

---

### Task 11: Final verification

**Step 1:** `gofmt -w .` (or `gofmt -l .` first, fix listed files).
**Step 2:** `go build -o /tmp/ocgo-verify ./cmd/server && rm /tmp/ocgo-verify` — must succeed.
**Step 3:** `go test ./... 2>&1 | grep -E "^FAIL|^--- FAIL"` — only the 3 pre-existing Claude fingerprint failures allowed; anything else is ours to fix.
**Step 4:** `go vet ./internal/... ./sdk/... 2>&1 | head` — clean.
**Step 5:** Update the design doc status line to "Implemented". Commit any leftovers:
`git add -A && git commit -m "chore: opencode-go provider final verification"`.
**Step 6:** Report ready for `superpowers:finishing-a-development-branch`.

---

## Notes for the implementer

- **Read before writing:** every "modeled on X" instruction above means: read the referenced function/file FIRST, copy its field handling and comment style. The repo's convention comments are dense and load-bearing (routing keys, pool bindings) — do not paraphrase them away.
- **Secrets:** never `log` api keys; response payloads must not echo keys.
- **No new timeouts** beyond the one quota fetch (8s) — AGENTS.md exception list must be updated (Task 9).
- **KISS:** no caching of quota, no background jobs, no alert integration, no openai-responses support (muse-spark rides the openai path).
- If `ClaudeExecutor` delegation proves to require OAuth-shaped auths (check `isClaudeOAuthToken` / claude auth resolution during Task 6), fall back to: the anthropic dispatch path builds the request inline following `claude_executor_execute.go` lines 30-110 (translate to "claude", POST `{base}/v1/messages`, `x-api-key` + `anthropic-version` headers, Bearer only when token looks like an OAuth token) and reuses `helps` response translation. Keep the delegation if it works — it is strictly simpler.
