# Neuralwatt Upstream Provider Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add Neuralwatt as a first-class NixLLM upstream provider that captures its per-request cost, energy, and service-tier metadata into `usage_events`.

**Architecture:** A thin `NeuralwattExecutor` wraps the shared `OpenAICompatExecutor` (the same pattern as `MetaExecutor`) and installs a response sink so Neuralwatt's non-standard billing envelope — cost headers, an SSE `: cost {...}` comment, and the `energy` object — is parsed before the usage record is published. Provider metadata reaches the database through two new `usage_events` columns.

**Tech Stack:** Go 1.26, `gjson`/`sjson` for payload parsing, Postgres (pgx), React + Vite dashboard.

**Design doc:** `docs/plans/2026-09-29-neuralwatt-provider-design.md`

**Baseline note:** Three tests in `internal/runtime/executor` fail on `main` before any change (`TestApplyClaudeHeaders_DisableDeviceProfileStabilization`, `TestApplyClaudeHeaders_LegacyModePreservesConfiguredUserAgentOverrideForClaudeClients`, `TestClaudeExecutor_NonClaudeRequestUsesClaudeCode220CLIFingerprint`). They are pre-existing and out of scope. Every task below must leave the *rest* of the package green.

**Convention reminders (from AGENTS.md):**
- `internal/runtime/executor/` holds executors and their unit tests only — helpers go in `internal/runtime/executor/helps/`.
- No `log.Fatal`/`log.Fatalf`. Use logrus.
- Run `gofmt -w .` after Go edits.
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`.

---

## Phase 1 — Provider identity and executor

### Task 1: Add the provider-type constant

**Files:**
- Modify: `internal/upstreamsync/render.go:28-47`

**Step 1: Add the constant**

In the `const` block that defines `TypeGeminiAPIKey`, `TypeMetaAPIKey`, and friends, add after `TypeMetaAPIKey`:

```go
	TypeNeuralwattAPIKey    = "neuralwatt-api-key"
```

**Step 2: Verify it compiles**

Run: `go build ./internal/upstreamsync/`
Expected: no output (success).

**Step 3: Commit**

```bash
gofmt -w internal/upstreamsync/render.go
git add internal/upstreamsync/render.go
git commit -m "feat(neuralwatt): add neuralwatt-api-key provider type"
```

---

### Task 2: Create the Neuralwatt executor wrapper

**Files:**
- Create: `internal/runtime/executor/neuralwatt_executor.go`
- Test: `internal/runtime/executor/neuralwatt_executor_test.go`

**Step 1: Write the failing test**

```go
package executor

import (
	"context"
	"testing"
)

func TestNeuralwattExecutorIdentifier(t *testing.T) {
	e := NewNeuralwattExecutor(nil)
	if got := e.Identifier(); got != "neuralwatt" {
		t.Fatalf("Identifier() = %q, want %q", got, "neuralwatt")
	}
}

func TestNeuralwattExecutorNilCompatIsSafe(t *testing.T) {
	var e *NeuralwattExecutor
	if _, err := e.Execute(context.Background(), nil, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); err == nil {
		t.Fatal("Execute on nil executor: want error, got nil")
	}
}
```

Add the `cliproxyexecutor` import:

```go
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
```

**Step 2: Run test to verify it fails**

Run: `go test -run TestNeuralwattExecutor ./internal/runtime/executor/`
Expected: FAIL — `undefined: NewNeuralwattExecutor`.

**Step 3: Write the implementation**

Create `internal/runtime/executor/neuralwatt_executor.go`, mirroring `meta_executor.go`:

```go
package executor

import (
	"context"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// NeuralwattExecutor executes inference for the Neuralwatt provider.
//
// Neuralwatt's API is OpenAI-compatible: a static bearer API key authenticates
// inference against https://api.neuralwatt.com/v1. The executor therefore
// delegates all inference to the shared OpenAI-compatible executor bound to
// the "neuralwatt" provider key, and additionally installs a response sink
// (neuralwatt_metadata.go) that captures Neuralwatt's cost headers, SSE cost
// comment, and energy object into the usage record.
type NeuralwattExecutor struct {
	compat *OpenAICompatExecutor
	cfg    *config.Config
}

// NewNeuralwattExecutor creates a Neuralwatt executor. The provider may be the
// bare executor channel; inference is routed by the shared OpenAI-compatible
// path.
func NewNeuralwattExecutor(cfg *config.Config) *NeuralwattExecutor {
	compat := NewOpenAICompatExecutor("neuralwatt", cfg)
	compat.SetResponseSink(neuralwattResponseSink{})
	return &NeuralwattExecutor{compat: compat, cfg: cfg}
}

// Identifier returns the provider identifier.
func (e *NeuralwattExecutor) Identifier() string {
	return "neuralwatt"
}

// Execute delegates to the OpenAI-compatible inference path.
func (e *NeuralwattExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.compat == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("neuralwatt executor: compat executor is nil")
	}
	return e.compat.Execute(ctx, auth, req, opts)
}

// ExecuteStream delegates to the OpenAI-compatible streaming path.
func (e *NeuralwattExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e == nil || e.compat == nil {
		return nil, fmt.Errorf("neuralwatt executor: compat executor is nil")
	}
	return e.compat.ExecuteStream(ctx, auth, req, opts)
}

// CountTokens delegates to the OpenAI-compatible token accounting path.
func (e *NeuralwattExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.compat == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("neuralwatt executor: compat executor is nil")
	}
	return e.compat.CountTokens(ctx, auth, req, opts)
}

// PrepareRequest injects Neuralwatt credentials into the outgoing HTTP request.
func (e *NeuralwattExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if e == nil || e.compat == nil {
		return fmt.Errorf("neuralwatt executor: compat executor is nil")
	}
	return e.compat.PrepareRequest(req, auth)
}

// HttpRequest injects Neuralwatt credentials into the request and executes it.
func (e *NeuralwattExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if e == nil || e.compat == nil {
		return nil, fmt.Errorf("neuralwatt executor: compat executor is nil")
	}
	return e.compat.HttpRequest(ctx, auth, req)
}

// Refresh is a no-op: Neuralwatt credentials are static API keys with no OAuth
// or minted-token refresh. Returning the auth unchanged keeps the conductor's
// refresh loop from erroring on this channel.
func (e *NeuralwattExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}
```

**Step 4: Add the response sink hook to the compat executor**

In `internal/runtime/executor/openai_compat_executor.go`, add to the struct definition (near `provider string`):

```go
	// responseSink, when non-nil, observes the upstream response so a
	// provider wrapper (e.g. Neuralwatt) can capture provider-specific
	// billing/energy metadata into the usage record before it is published.
	// Nil for every bare OpenAI-compatible provider.
	responseSink OpenAICompatResponseSink
```

Add the interface and setter immediately above `NewOpenAICompatExecutor`:

```go
// OpenAICompatResponseSink observes upstream responses on behalf of a
// provider-specific wrapper. The compat executor calls it on the success path
// only, and always before the usage record is published.
type OpenAICompatResponseSink interface {
	// CaptureResponse receives the full body of a non-streaming response.
	CaptureResponse(ctx context.Context, headers http.Header, body []byte)
	// CaptureStreamHeaders receives the response headers before any chunk.
	CaptureStreamHeaders(ctx context.Context, headers http.Header)
	// CaptureStreamChunk receives each raw streaming chunk in arrival order.
	CaptureStreamChunk(ctx context.Context, chunk []byte)
}

// SetResponseSink installs a provider-specific response observer. It is called
// once at construction time by the wrapping executor.
func (e *OpenAICompatExecutor) SetResponseSink(sink OpenAICompatResponseSink) {
	if e == nil {
		return
	}
	e.responseSink = sink
}
```

**Step 5: Call the sink from the compat executor**

In `Execute`, immediately after `helps.AppendAPIResponseChunk(ctx, e.cfg, body)` and as the line *before* `reporter.Publish(ctx, helps.ParseOpenAIUsage(body))`, insert:

```go
	if e.responseSink != nil {
		e.responseSink.CaptureResponse(ctx, httpResp.Header, body)
	}
```

In `ExecuteStream`, immediately after the status check succeeds (just before `out := make(chan cliproxyexecutor.StreamChunk)`), insert:

```go
	if e.responseSink != nil {
		e.responseSink.CaptureStreamHeaders(ctx, httpResp.Header.Clone())
	}
```

In the same function's read loop, immediately after `chunk := bytes.Clone(buffer[:n])`, insert:

```go
				if e.responseSink != nil {
					e.responseSink.CaptureStreamChunk(ctx, chunk)
				}
```

**Step 6: Add a temporary stub sink so the package compiles**

`neuralwattResponseSink` is implemented in Task 3. To keep this task independently green, create `internal/runtime/executor/neuralwatt_metadata.go` with the type and empty methods for now:

```go
package executor

import (
	"context"
	"net/http"
)

// neuralwattResponseSink captures Neuralwatt's provider-specific response
// metadata (cost, cache savings, energy, served service tier) into the usage
// record. Implemented fully in a later step.
type neuralwattResponseSink struct{}

func (neuralwattResponseSink) CaptureResponse(context.Context, http.Header, []byte)  {}
func (neuralwattResponseSink) CaptureStreamHeaders(context.Context, http.Header)     {}
func (neuralwattResponseSink) CaptureStreamChunk(context.Context, []byte)           {}
```

**Step 7: Run tests to verify they pass**

Run: `go test -run TestNeuralwattExecutor ./internal/runtime/executor/`
Expected: PASS.

Run: `go build -o test-output ./cmd/server && rm test-output`
Expected: success.

**Step 8: Commit**

```bash
gofmt -w internal/runtime/executor/
git add internal/runtime/executor/neuralwatt_executor.go internal/runtime/executor/neuralwatt_executor_test.go internal/runtime/executor/neuralwatt_metadata.go internal/runtime/executor/openai_compat_executor.go
git commit -m "feat(neuralwatt): add executor wrapper and compat response-sink hook"
```

---

### Task 3: Register the executor and add it to the baseline set

**Files:**
- Modify: `sdk/cliproxy/service_executors.go:201-227` (baseline list) and `:278-325` (dispatch switch)

**Step 1: Add the dispatch case**

In `registerExecutorForAuth`'s `switch strings.ToLower(a.Provider)`, directly after the `case "meta":` arm:

```go
	case "neuralwatt":
		s.coreManager.RegisterExecutor(executor.NewNeuralwattExecutor(cfg))
```

**Step 2: Add to the baseline provider list**

In `baselineExecutorAuths`, add `"neuralwatt"` to the `providers` slice next to `"meta"`, so the executor is registered even before any credential exists.

**Step 3: Run tests**

Run: `go build ./... && go test ./sdk/cliproxy/`
Expected: PASS.

**Step 4: Commit**

```bash
gofmt -w sdk/cliproxy/service_executors.go
git add sdk/cliproxy/service_executors.go
git commit -m "feat(neuralwatt): register the neuralwatt executor channel"
```

---

### Task 4: Make the provider key resolvable everywhere it is derived

**Files:**
- Modify: `sdk/cliproxy/auth/types.go:397-414`
- Modify: `sdk/cliproxy/auth/conductor_execution.go:1879-1900`
- Test: `sdk/cliproxy/auth/` (existing suite)

**Step 1: Add the auth-identity prefix**

In the `apiKey != ""` switch in `auth/types.go`, after `case strings.EqualFold(provider, "meta"):`:

```go
		case strings.EqualFold(provider, "neuralwatt"):
			apiPrefix = "neuralwatt-api-key"
```

**Step 2: Add the channel to the routing-key collapse list**

In `conductor_execution.go`, find the `case "claude", "codex", "gemini", "gemini-interactions", "vertex", "xai", "meta", "opencode-go":` arm (~line 1894) and add `"neuralwatt"` to that list, so a compound `neuralwatt:<rowID>` routing key resolves back to the shared channel executor.

**Step 3: Verify `util.UpstreamProviderKey` already handles it**

`internal/util/upstream_provider_key.go` maps any `*-api-key` suffix to `<channel>` / `<channel>:<rowID>`. `neuralwatt-api-key` therefore yields `neuralwatt` / `neuralwatt:42` with **no change**. Confirm by reading the `strings.HasSuffix(pt, "-api-key")` branch.

**Step 4: Run tests**

Run: `go test ./sdk/cliproxy/auth/ ./internal/util/`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w sdk/cliproxy/auth/
git add sdk/cliproxy/auth/types.go sdk/cliproxy/auth/conductor_execution.go
git commit -m "feat(neuralwatt): resolve neuralwatt auth identity and routing keys"
```

---

## Phase 2 — Config surface and control plane

### Task 5: Add the config field and type

**Files:**
- Modify: `internal/config/config.go:122-123`
- Modify: `internal/config/config_types.go:733-737`
- Modify: `sdk/config/config.go:27,33`
- Modify: `internal/config/weight.go:144-145`

**Step 1: Add the config section field**

In `internal/config/config.go`, after the `MetaKey` field:

```go
	// NeuralwattKey defines Neuralwatt API key configurations using the same
	// structure as Codex API keys. Neuralwatt is OpenAI-compatible and
	// authenticates with a static bearer key.
	NeuralwattKey []NeuralwattKey `yaml:"neuralwatt-api-key" json:"neuralwatt-api-key"`
```

**Step 2: Add the type alias**

In `internal/config/config_types.go`, after `type MetaModel = CodexModel`:

```go
// NeuralwattKey uses the Codex API key structure for native Neuralwatt execution.
type NeuralwattKey = CodexKey

// NeuralwattModel uses the Codex model mapping structure for Neuralwatt models.
type NeuralwattModel = CodexModel
```

**Step 3: Add the service-tier selector to `CodexKey`**

Neuralwatt needs a per-credential tier selector. `CodexKey` is shared by Codex/xAI/Meta/Neuralwatt, so add one documented field rather than duplicating the whole struct. In `config_types.go`, inside `CodexKey`, after `AlphaSearch`:

```go
	// ServiceTier selects the upstream billing tier for providers that expose
	// one. Only Neuralwatt reads it ("default" | "flex"); empty leaves the
	// provider default and every other Codex-style provider ignores it.
	ServiceTier string `yaml:"service-tier,omitempty" json:"service-tier,omitempty"`
```

**Step 4: Add the SDK alias**

In `sdk/config/config.go`, alongside the `MetaKey` alias, add:

```go
	NeuralwattKey = internalconfig.NeuralwattKey
```

(Match the exact form of the neighbouring aliases in that file.)

**Step 5: Include Neuralwatt in weight validation**

In `internal/config/weight.go`, in the loop that iterates `cfg.MetaKey`, add an equivalent iteration over `cfg.NeuralwattKey`.

**Step 6: Run tests**

Run: `go build ./... && go test ./internal/config/`
Expected: PASS.

**Step 7: Commit**

```bash
gofmt -w internal/config/ sdk/config/
git add internal/config/config.go internal/config/config_types.go internal/config/weight.go sdk/config/config.go
git commit -m "feat(neuralwatt): add neuralwatt-api-key config section"
```

---

### Task 6: Normalize the new section on config load

**Files:**
- Modify: `internal/config/config_normalization.go:148-158`
- Test: `internal/config/config_normalization_test.go` (or the package's existing test file)

**Step 1: Write the failing test**

Add a test asserting that a `NeuralwattKey` entry with a trailing-whitespace API key and an empty base URL is sanitized the same way a `MetaKey` entry is. Follow the existing `SanitizeMetaKeys` test's shape in that package.

**Step 2: Run it to verify it fails**

Run: `go test -run SanitizeNeuralwatt ./internal/config/`
Expected: FAIL — `undefined: SanitizeNeuralwattKeys`.

**Step 3: Implement**

In `config_normalization.go`, add `SanitizeNeuralwattKeys` mirroring `SanitizeMetaKeys` exactly (same field handling, same slicing semantics), then call it from the same two places `SanitizeMetaKeys` is called (`parse.go:102` and `config_load.go:166`).

**Step 4: Run it to verify it passes**

Run: `go test -run SanitizeNeuralwatt ./internal/config/`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/config/
git add internal/config/
git commit -m "feat(neuralwatt): sanitize neuralwatt-api-key on config load"
```

---

### Task 7: Wire the control plane (planner, render, seed, import)

**Files:**
- Modify: `internal/configsnapshot/import_resources.go:23-33, 88, 140-163, 354`
- Modify: `internal/upstreamsync/render.go:147-172`
- Modify: `internal/upstreamsync/seed.go:26-114`
- Modify: `internal/store/pg_normalized_import.go:744-756`
- Modify: `internal/upstreamsync/apply.go:63`
- Modify: `internal/api/handlers/management/handler.go:1142-1149`
- Test: `internal/upstreamsync/` round-trip test mirroring `identity_test.go:70-95`

**Step 1: Write the failing round-trip test**

Mirror `TestMetaAPIKeySeedRenderRoundTrip` in `internal/upstreamsync/identity_test.go`: seed a `config.NeuralwattKey` into a provider row via `SeedFromArtifacts`, render it back with `RenderConfig`, and assert the API key, base URL, and `service-tier` survive.

**Step 2: Run it to verify it fails**

Run: `go test -run Neuralwatt ./internal/upstreamsync/`
Expected: FAIL — nothing seeds or renders the section yet.

**Step 3: Implement the planner**

In `internal/configsnapshot/import_resources.go`:
- Add `ptNeuralwattAPIKey = "neuralwatt-api-key"` to the provider-type const block.
- Write `convertNeuralwattKeys(in []config.CodexKey) []store.UpstreamProvider` by copying `convertMetaKeys` verbatim and changing `ptMetaAPIKey` → `ptNeuralwattAPIKey` and the synthesized name prefix `"meta-%d"` → `"neuralwatt-%d"`.
- Add `appendProviders(&plan.Providers, &plan.Report, convertNeuralwattKeys(cfg.NeuralwattKey)...)` to `BuildResourcePlan`, after the Meta call.
- Add `case ptNeuralwattAPIKey: return "neuralwatt-"` to `sectionPrefix`.

**Step 4: Implement render and seed**

- `render.go`: add `case TypeNeuralwattAPIKey: cfg.NeuralwattKey = append(cfg.NeuralwattKey, codexKeyFromProvider(p))` to `RenderConfigWithPools`.
- `seed.go`: add a `cfg.NeuralwattKey` loop calling `providerFromCodexKey(k, TypeNeuralwattAPIKey)`, mirroring the Meta loop at `:53-57`.

**Step 5: Implement the import name prefix and merge list**

- `pg_normalized_import.go`: in the provider-type → name-prefix switch (~`:744`), add `case "neuralwatt-api-key": return "neuralwatt-"` alongside the meta case.
- `apply.go` (~`:63`): add `merged.NeuralwattKey = rendered.NeuralwattKey` beside the other provider merges.
- `handler.go` (~`:1142-1149`): add `NeuralwattKey` to the same merge/filter list.

**Step 6: Run tests**

Run: `go test ./internal/upstreamsync/ ./internal/configsnapshot/ ./internal/store/`
Expected: PASS, including the new round-trip test.

**Step 7: Commit**

```bash
gofmt -w internal/ internal/store/
git add internal/configsnapshot/import_resources.go internal/upstreamsync/render.go internal/upstreamsync/seed.go internal/upstreamsync/identity_test.go internal/store/pg_normalized_import.go internal/upstreamsync/apply.go internal/api/handlers/management/handler.go
git commit -m "feat(neuralwatt): wire neuralwatt-api-key through the control plane"
```

---

### Task 8: Synthesize live auths for Neuralwatt credentials

Neuralwatt is a **pure API-key** provider, so unlike Meta (which is served by its OAuth login path) it must be synthesized into live auths from config. Without this task the provider has models but no routable credential.

**Files:**
- Modify: `internal/watcher/synthesizer/config.go:143-168` (the `Synthesize` call list)
- Test: `internal/watcher/synthesizer/` (existing suite)

**Step 1: Write the failing test**

Add a test asserting `Synthesize` returns one auth with `Provider == "neuralwatt"` and attributes carrying `api_key` and `base_url` when `Config.NeuralwattKey` has one entry. Mirror the xAI/Codex synthesis test in that package.

**Step 2: Run it to verify it fails**

Run: `go test -run Neuralwatt ./internal/watcher/synthesizer/`
Expected: FAIL — no auths produced.

**Step 3: Implement**

In `Synthesize`, after the xAI line, add:

```go
	// Neuralwatt API Keys
	out = append(out, s.synthesizeNeuralwattKeys(ctx)...)
```

And add the method near `synthesizeXAIKeys`:

```go
func (s *ConfigSynthesizer) synthesizeNeuralwattKeys(ctx *SynthesisContext) []*coreauth.Auth {
	return s.synthesizeCodexStyleKeys(ctx, ctx.Config.NeuralwattKey, "neuralwatt")
}
```

**Step 4: Stamp the service tier onto the auth**

`neuralwatt` needs the tier on the auth so the executor can inject it. In `synthesizeCodexStyleKeys`, alongside the existing `if entry.Websockets` line, add:

```go
		if tier := strings.TrimSpace(entry.ServiceTier); tier != "" {
			attrs["service_tier"] = tier
		}
```

**Step 5: Run tests**

Run: `go test ./internal/watcher/...`
Expected: PASS.

**Step 6: Commit**

```bash
gofmt -w internal/watcher/
git add internal/watcher/synthesizer/config.go
git commit -m "feat(neuralwatt): synthesize live auths from neuralwatt-api-key config"
```

---

### Task 9: Register Neuralwatt models

**Files:**
- Modify: `internal/registry/models/models.json` (add a top-level `"neuralwatt"` key)
- Modify: `internal/registry/model_definitions.go:19-33, 110-146, 330-352, 354-379`
- Modify: `sdk/cliproxy/service_models.go:64-181`
- Modify: `internal/api/handlers/management/upstream_catalog_sync.go:106-118`
- Test: `internal/registry/model_definitions_test.go`

**Step 1: Write the failing test**

Assert `registry.GetNeuralwattModels()` returns at least one model and that `GetModelsByType("neuralwatt")` resolves to the same list.

**Step 2: Run it to verify it fails**

Run: `go test -run Neuralwatt ./internal/registry/`
Expected: FAIL — `undefined: GetNeuralwattModels`.

**Step 3: Add the static catalog**

In `internal/registry/models/models.json`, add a top-level `"neuralwatt"` key holding the known models in the same shape as the `"meta"` entry (see the `muse-code` object for the exact field set):

```json
  "neuralwatt": [
    {
      "id": "deepseek-v4-pro",
      "object": "model",
      "created": 1784592000,
      "owned_by": "neuralwatt",
      "type": "neuralwatt",
      "display_name": "DeepSeek V4 Pro",
      "name": "deepseek-v4-pro",
      "version": "deepseek-v4-pro",
      "description": "DeepSeek V4 Pro served by Neuralwatt (OpenAI-compatible).",
      "context_length": 200000,
      "max_completion_tokens": 65536,
      "supportedInputModalities": ["text"],
      "supportedOutputModalities": ["text"]
    }
  ]
```

Keep the JSON valid — run `python3 -m json.tool internal/registry/models/models.json > /dev/null` to confirm.

**Step 4: Add the accessor**

In `model_definitions.go`:
- Add `Neuralwatt []*ModelInfo \`json:"neuralwatt"\`` to `staticModelsJSON`.
- Add, after `GetMetaModels`:

```go
// GetNeuralwattModels returns the standard Neuralwatt model definitions.
func GetNeuralwattModels() []*ModelInfo {
	return cloneModelInfos(getModels().Neuralwatt)
}
```

- Add `case "neuralwatt": return GetNeuralwattModels()` to the provider-key switch (~`:347`).
- Add `data.Neuralwatt` to the `allModels` slice in `LookupStaticModelInfo` (~`:362-372`).

**Step 5: Add the per-auth model resolution**

In `sdk/cliproxy/service_models.go`, after the `case "meta":` arm, add a `case "neuralwatt":` arm mirroring it:

```go
	case "neuralwatt":
		models = registry.GetNeuralwattModels()
		if entry := s.resolveConfigNeuralwattKey(a); entry != nil {
			if len(entry.Models) > 0 {
				models = buildNeuralwattConfigModels(entry)
			}
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
```

Add the two helpers by copying `resolveConfigMetaKey` and `buildMetaConfigModels` — for `buildNeuralwattConfigModels`, use the same delegate the Meta builder uses (see `buildMetaConfigModels` at `:875-879`) so behavior stays identical.

**Step 6: Register the catalog mirror**

In `upstream_catalog_sync.go`, add `upstreamsync.TypeNeuralwattAPIKey` to the `mirrorsCatalogByUpstreamName` switch so Neuralwatt models mirror into `models_catalog`.

**Step 7: Run tests**

Run: `go test ./internal/registry/ ./sdk/cliproxy/ ./internal/api/handlers/management/`
Expected: PASS.

**Step 8: Commit**

```bash
gofmt -w internal/registry/ sdk/cliproxy/ internal/api/
git add internal/registry/models/models.json internal/registry/model_definitions.go internal/registry/model_definitions_test.go sdk/cliproxy/service_models.go internal/api/handlers/management/upstream_catalog_sync.go
git commit -m "feat(neuralwatt): register the neuralwatt model catalog"
```

---

## Phase 3 — Provider metadata capture

### Task 10: Add the provider-metadata context holder

**Files:**
- Create: `internal/runtime/executor/helps/provider_usage_metadata.go`
- Test: `internal/runtime/executor/helps/provider_usage_metadata_test.go`

**Step 1: Write the failing test**

```go
package helps

import (
	"context"
	"testing"
)

func TestProviderUsageMetadataRoundTrip(t *testing.T) {
	ctx := EnsureProviderUsageMetadata(context.Background())
	SetProviderUsageMetadata(ctx, "neuralwatt", map[string]any{"grid_id": "us-west-2"})
	SetProviderEnergyJoules(ctx, 42.5)
	SetProviderResponseServiceTier(ctx, "flex")

	md := ProviderUsageMetadataFromContext(ctx)
	if md.EnergyJoules != 42.5 {
		t.Fatalf("EnergyJoules = %v, want 42.5", md.EnergyJoules)
	}
	if md.ResponseServiceTier != "flex" {
		t.Fatalf("ResponseServiceTier = %q, want flex", md.ResponseServiceTier)
	}
	if got := md.Metadata["neuralwatt"].(map[string]any)["grid_id"]; got != "us-west-2" {
		t.Fatalf("grid_id = %v, want us-west-2", got)
	}
}

func TestProviderUsageMetadataAbsentIsZero(t *testing.T) {
	md := ProviderUsageMetadataFromContext(context.Background())
	if md.EnergyJoules != 0 || md.Metadata != nil || md.ResponseServiceTier != "" {
		t.Fatalf("absent holder = %+v, want zero value", md)
	}
}
```

**Step 2: Run it to verify it fails**

Run: `go test -run TestProviderUsageMetadata ./internal/runtime/executor/helps/`
Expected: FAIL — undefined symbols.

**Step 3: Implement**

Create the file, modelling the mutable-holder pattern on `internal/logging/requestmeta.go:26-133` (value stored behind a pointer + mutex so a capture that happens after the ctx is derived is still visible):

```go
package helps

import (
	"context"
	"sync"
)

// ProviderUsageMetadata carries provider-specific billing/energy data that a
// provider executor captures from an upstream response and the generic usage
// reporter later folds into the usage record. The holder lives behind a
// pointer in the context so writes performed after the context is derived
// (e.g. a streaming cost comment that arrives mid-stream) remain visible to
// the reporter, which publishes at end-of-stream.
type ProviderUsageMetadata struct {
	EnergyJoules float64
	Metadata     map[string]any
	// ResponseServiceTier is a fallback used only when the response body did
	// not carry a service_tier of its own.
	ResponseServiceTier string
}

type providerUsageMetadataKey struct{}

type providerUsageMetadataHolder struct {
	mu  sync.Mutex
	md  ProviderUsageMetadata
}

// EnsureProviderUsageMetadata returns ctx carrying a mutable metadata holder.
func EnsureProviderUsageMetadata(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	if _, ok := ctx.Value(providerUsageMetadataKey{}).(*providerUsageMetadataHolder); ok {
		return ctx
	}
	return context.WithValue(ctx, providerUsageMetadataKey{}, &providerUsageMetadataHolder{})
}

func providerUsageMetadataHolderFrom(ctx context.Context) *providerUsageMetadataHolder {
	if ctx == nil {
		return nil
	}
	holder, _ := ctx.Value(providerUsageMetadataKey{}).(*providerUsageMetadataHolder)
	return holder
}

// SetProviderUsageMetadata merges md under the provider key. Later calls with
// the same provider overwrite earlier values for the same keys.
func SetProviderUsageMetadata(ctx context.Context, provider string, md map[string]any) {
	holder := providerUsageMetadataHolderFrom(ctx)
	if holder == nil || len(md) == 0 {
		return
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	if holder.md.Metadata == nil {
		holder.md.Metadata = map[string]any{}
	}
	holder.md.Metadata[provider] = md
}

// SetProviderEnergyJoules records measured energy for the request.
func SetProviderEnergyJoules(ctx context.Context, joules float64) {
	holder := providerUsageMetadataHolderFrom(ctx)
	if holder == nil {
		return
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	holder.md.EnergyJoules = joules
}

// SetProviderResponseServiceTier records the tier the upstream reported
// serving, used as a fallback when the body carried none.
func SetProviderResponseServiceTier(ctx context.Context, tier string) {
	holder := providerUsageMetadataHolderFrom(ctx)
	if holder == nil || tier == "" {
		return
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	holder.md.ResponseServiceTier = tier
}

// ProviderUsageMetadataFromContext returns a copy of the captured metadata.
func ProviderUsageMetadataFromContext(ctx context.Context) ProviderUsageMetadata {
	holder := providerUsageMetadataHolderFrom(ctx)
	if holder == nil {
		return ProviderUsageMetadata{}
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	out := holder.md
	if holder.md.Metadata != nil {
		out.Metadata = make(map[string]any, len(holder.md.Metadata))
		for k, v := range holder.md.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}
```

**Step 4: Run tests to verify they pass**

Run: `go test -run TestProviderUsageMetadata ./internal/runtime/executor/helps/`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/runtime/executor/helps/
git add internal/runtime/executor/helps/provider_usage_metadata.go internal/runtime/executor/helps/provider_usage_metadata_test.go
git commit -m "feat(usage): add provider usage metadata context holder"
```

---

### Task 11: Implement the Neuralwatt response sink

**Files:**
- Modify: `internal/runtime/executor/neuralwatt_metadata.go` (replace the stub)
- Test: `internal/runtime/executor/neuralwatt_metadata_test.go`

**Step 1: Write the failing tests**

Cover the three documented behaviours:

```go
package executor

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

func TestNeuralwattSinkCapturesCostHeadersAndEnergy(t *testing.T) {
	ctx := helps.EnsureProviderUsageMetadata(context.Background())
	headers := http.Header{}
	headers.Set("X-Request-Cost-USD", "0.003400")
	headers.Set("X-Cache-Savings-USD", "0.027")
	headers.Set("X-Allowance-Remaining-USD", "47.66")
	headers.Set("X-NW-Service-Tier", "flex")
	body := []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5},"energy":{"energy_joules":42.5,"measurement_available":true,"grid_id":"us-west-2","carbon_g_co2eq":3.2}}`)

	neuralwattResponseSink{}.CaptureResponse(ctx, headers, body)

	md := helps.ProviderUsageMetadataFromContext(ctx)
	if md.EnergyJoules != 42.5 {
		t.Fatalf("EnergyJoules = %v, want 42.5", md.EnergyJoules)
	}
	nw, ok := md.Metadata["neuralwatt"].(map[string]any)
	if !ok {
		t.Fatalf("neuralwatt metadata missing: %+v", md.Metadata)
	}
	if nw["request_cost_usd"] != 0.0034 {
		t.Fatalf("request_cost_usd = %v, want 0.0034", nw["request_cost_usd"])
	}
}

func TestNeuralwattSinkOmitsEnergyWhenUnmeasured(t *testing.T) {
	ctx := helps.EnsureProviderUsageMetadata(context.Background())
	body := []byte(`{"usage":{"prompt_tokens":1},"energy":{"measurement_available":false}}`)

	neuralwattResponseSink{}.CaptureResponse(ctx, http.Header{}, body)

	md := helps.ProviderUsageMetadataFromContext(ctx)
	if md.EnergyJoules != 0 {
		t.Fatalf("EnergyJoules = %v, want 0 for an unmeasured response", md.EnergyJoules)
	}
}

func TestNeuralwattSinkParsesStreamCostComment(t *testing.T) {
	ctx := helps.EnsureProviderUsageMetadata(context.Background())
	sink := neuralwattResponseSink{}
	sink.CaptureStreamHeaders(ctx, http.Header{"X-Nw-Service-Tier": []string{"standard"}})
	sink.CaptureStreamChunk(ctx, []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
	sink.CaptureStreamChunk(ctx, []byte(": cost {\"request_cost_usd\": 0.0034, \"cache_savings_usd\": 0.027, \"allowance_remaining_usd\": 47.66}\n"))
	sink.CaptureStreamChunk(ctx, []byte("data: [DONE]\n\n"))

	md := helps.ProviderUsageMetadataFromContext(ctx)
	nw, ok := md.Metadata["neuralwatt"].(map[string]any)
	if !ok {
		t.Fatalf("neuralwatt metadata missing: %+v", md.Metadata)
	}
	if nw["request_cost_usd"] != 0.0034 {
		t.Fatalf("request_cost_usd = %v, want 0.0034", nw["request_cost_usd"])
	}
}
```

**Step 2: Run them to verify they fail**

Run: `go test -run TestNeuralwattSink ./internal/runtime/executor/`
Expected: FAIL — the stub captures nothing.

**Step 3: Implement**

Replace `neuralwatt_metadata.go` with a real implementation. Requirements:

- **`CaptureResponse`**: read `X-Request-Cost-USD`, `X-Cache-Savings-USD`, `X-Allowance-Remaining-USD`, `X-NW-Service-Tier`, `X-Flex-Applied` from headers; read `energy.energy_joules` and the carbon fields from the body with `gjson`. Write the header/energy values via `helps.SetProviderUsageMetadata(ctx, "neuralwatt", ...)` and `helps.SetProviderEnergyJoules`. Only set energy when `energy.measurement_available` is true — the API omits the numeric fields entirely when it is false, so a missing `measurement_available`/false must leave `EnergyJoules` at 0 rather than guessing.
- **`CaptureStreamHeaders`**: record `X-NW-Service-Tier` via `helps.SetProviderResponseServiceTier`.
- **`CaptureStreamChunk`**: buffer the chunk, and on each newline-terminated line starting with `: cost `, JSON-parse the remainder into a `map[string]any` and merge it via `helps.SetProviderUsageMetadata`. Keep the buffer bounded — retain only a trailing partial line (cap the retained prefix at 64 KiB and drop it if a single line exceeds that, logging at debug level).
- Parse the numeric fields defensively (`strconv.ParseFloat` on the trimmed string; on error, skip the field rather than storing NaN).

Because chunks arrive per-read and a line may straddle reads, the sink needs per-request buffer state. Make it a pointer type with its own mutex and construct it per request in `NewNeuralwattExecutor` — pass a **new** sink instance into `Execute`/`ExecuteStream`'s compat executor rather than sharing one across requests. Concretely: give `OpenAICompatExecutor` the sink per call via `ctx` rather than a struct field, **or** keep the field but have `NeuralwattExecutor.Execute*` set a fresh sink on a locally-created compat executor for that call.

Choose the simplest correct option and state your choice in the commit message.

**Step 4: Run tests to verify they pass**

Run: `go test -run TestNeuralwatt ./internal/runtime/executor/`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/runtime/executor/
git add internal/runtime/executor/neuralwatt_metadata.go internal/runtime/executor/neuralwatt_metadata_test.go internal/runtime/executor/neuralwatt_executor.go
git commit -m "feat(neuralwatt): capture cost, energy, and service tier into usage metadata"
```

---

### Task 12: Thread the captured metadata into the usage record

**Files:**
- Modify: `sdk/cliproxy/usage/manager.go:33-123` (the `Record` struct)
- Modify: `internal/runtime/executor/helps/usage_helpers.go:432-484` (`buildRecordForModel`)
- Test: `internal/runtime/executor/helps/usage_helpers_test.go`

**Step 1: Write the failing test**

Assert that when the context carries provider metadata, the record built by the reporter exposes it:

```go
func TestBuildRecordCarriesProviderUsageMetadata(t *testing.T) {
	ctx := EnsureProviderUsageMetadata(context.Background())
	SetProviderEnergyJoules(ctx, 42.5)
	SetProviderUsageMetadata(ctx, "neuralwatt", map[string]any{"request_cost_usd": 0.0034})

	reporter := NewUsageReporter(ctx, "neuralwatt", "deepseek-v4-pro", nil)
	record := reporter.buildRecord(usage.Detail{}, false)

	if record.EnergyJoules != 42.5 {
		t.Fatalf("EnergyJoules = %v, want 42.5", record.EnergyJoules)
	}
	if record.ProviderMetadata["neuralwatt"] == nil {
		t.Fatalf("ProviderMetadata missing neuralwatt: %+v", record.ProviderMetadata)
	}
}
```

**Step 2: Run it to verify it fails**

Run: `go test -run TestBuildRecordCarriesProviderUsageMetadata ./internal/runtime/executor/helps/`
Expected: FAIL — `record.EnergyJoules` undefined.

**Step 3: Add the fields to `Record`**

In `sdk/cliproxy/usage/manager.go`, add to `Record`:

```go
	// EnergyJoules is the upstream-reported energy consumption for this
	// request, when the provider measures it. Zero means unmeasured.
	EnergyJoules float64
	// ProviderMetadata carries provider-specific billing/attribution data
	// (e.g. Neuralwatt cost, cache savings, grid/carbon attribution) keyed by
	// provider. Persisted to usage_events.provider_metadata.
	ProviderMetadata map[string]any
```

**Step 4: Populate them when building the record**

In `buildRecordForModel`, read `helps.ProviderUsageMetadataFromContext(ctx)`. `buildRecordForModel` currently has no `ctx` parameter — add one and update its two call sites (`buildRecord` and `buildAdditionalModelRecord`) plus any test callers. Then set:

```go
		EnergyJoules:        providerMeta.EnergyJoules,
		ProviderMetadata:    providerMeta.Metadata,
```

and, where `ResponseServiceTier` is currently set from `detail.ResponseServiceTier`, fall back to the captured tier:

```go
	ResponseServiceTier:      firstNonEmptyTier(strings.TrimSpace(detail.ResponseServiceTier), providerMeta.ResponseServiceTier),
```

with a small local helper `firstNonEmptyTier(a, b string) string`.

**Step 5: Run tests**

Run: `go test ./internal/runtime/executor/helps/ ./sdk/cliproxy/usage/`
Expected: PASS.

**Step 6: Commit**

```bash
gofmt -w sdk/cliproxy/usage/ internal/runtime/executor/helps/
git add sdk/cliproxy/usage/manager.go internal/runtime/executor/helps/usage_helpers.go internal/runtime/executor/helps/usage_helpers_test.go
git commit -m "feat(usage): carry provider energy and metadata on the usage record"
```

---

### Task 13: Inject the metadata holder on the Neuralwatt execution paths

**Files:**
- Modify: `internal/runtime/executor/neuralwatt_executor.go`

**Step 1: Write the failing test**

Assert that `Execute` returns an error from the compat path but that the ctx passed downstream has a holder. The simplest observable assertion: pass a context without a holder and confirm `helps.ProviderUsageMetadataFromContext` on a child context is non-nil after `EnsureProviderUsageMetadata`. If a direct behavioural test is impractical, assert the wrapper calls `EnsureProviderUsageMetadata` by extracting the body of the call into a tiny helper and testing that helper.

**Step 2: Implement**

In `NeuralwattExecutor.Execute`, `ExecuteStream`, and `HttpRequest`, derive `ctx = helps.EnsureProviderUsageMetadata(ctx)` as the first statement before delegating to `e.compat`. This guarantees the holder exists for the sink to write into regardless of which entry point the conductor uses.

**Step 3: Run tests**

Run: `go test ./internal/runtime/executor/ -run TestNeuralwatt`
Expected: PASS.

**Step 4: Commit**

```bash
gofmt -w internal/runtime/executor/
git add internal/runtime/executor/neuralwatt_executor.go
git commit -m "feat(neuralwatt): ensure the metadata holder on every entry point"
```

---

## Phase 4 — Persistence

### Task 14: Add the two `usage_events` columns

**Files:**
- Modify: `internal/store/postgresstore.go` (the `usage_events` CREATE TABLE ~`:1187-1222`, plus an idempotent ALTER for existing databases)
- Test: `internal/store/pg_migrations_test.go`

**Step 1: Write the failing test**

Add a test asserting the `usage_events` table has `energy_joules` (numeric) and `provider_metadata` (jsonb) columns after `EnsureSchema`. Follow the existing column-assertion tests in `pg_migrations_test.go`.

**Step 2: Run it to verify it fails**

Run: `go test -run UsageEventsColumns ./internal/store/`
Expected: FAIL — columns missing.

**Step 3: Implement**

Add both columns to the `CREATE TABLE` block, after `flushed_at`:

```sql
			energy_joules           NUMERIC(12,6),
			provider_metadata       JSONB NOT NULL DEFAULT '{}'::jsonb
```

And, immediately after the CREATE (matching the file's existing `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` idempotency pattern used for `api_key_policies`):

```go
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS energy_joules NUMERIC(12,6)`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter usage_events add energy_joules: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS provider_metadata JSONB NOT NULL DEFAULT '{}'::jsonb`, usageEventsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: alter usage_events add provider_metadata: %w", err)
	}
```

**Step 4: Run tests**

Run: `go test ./internal/store/ -run UsageEvents`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/store/postgresstore.go
git add internal/store/postgresstore.go internal/store/pg_migrations_test.go
git commit -m "feat(store): add energy_joules and provider_metadata to usage_events"
```

---

### Task 15: Read and write the new columns

**Files:**
- Modify: `internal/store/pg_usage.go:26-97` (`UsageEvent` struct), `:564-572` (column list + count), `:577-613` (`InsertEvent`), `:618-680` (`BatchInsertEvents`)
- Test: `internal/store/pg_usage_test.go`

**Step 1: Write the failing test**

Assert an `InsertEvent` round-trips `EnergyJoules` and `ProviderMetadata` — mirror the existing insert/select round-trip tests in `pg_usage_test.go`.

**Step 2: Run it to verify it fails**

Run: `go test -run ProviderMetadata ./internal/store/`
Expected: FAIL — fields undefined.

**Step 3: Add the struct fields**

In `UsageEvent`, after `OriginalCostUSD`:

```go
	// EnergyJoules is the upstream-reported energy consumption, when measured.
	// NULL for providers that do not report it (and for unmeasured responses).
	EnergyJoules *float64 `json:"energy_joules,omitempty"`
	// ProviderMetadata carries provider-specific billing/attribution data as a
	// JSON object keyed by provider (e.g. {"neuralwatt": {...}}).
	ProviderMetadata map[string]any `json:"provider_metadata,omitempty"`
```

`EnergyJoules` is a pointer so an unmeasured response stores SQL NULL rather than a misleading `0`.

**Step 4: Extend the column list and the binder**

Add `energy_joules, provider_metadata` to `usageEventColumnList` and bump `usageEventColumnCount` from 41 to 43. Add the two bind arguments in `InsertEvent` and in the per-row loop of `BatchInsertEvents`. Marshal `ProviderMetadata` to JSON with a helper that returns `[]byte("{}")` for an empty map, and bind it so the `NOT NULL DEFAULT` column is satisfied.

Also check whether the read paths (`SelectEvents` and friends, ~`:444-490`) use an explicit column list; if so leave them unchanged — the new columns are write-only for now — but confirm with `go test ./internal/store/`.

**Step 5: Run tests**

Run: `go test ./internal/store/`
Expected: PASS.

**Step 6: Commit**

```bash
gofmt -w internal/store/
git add internal/store/pg_usage.go internal/store/pg_usage_test.go
git commit -m "feat(store): persist energy_joules and provider_metadata on usage events"
```

---

### Task 16: Map the record onto the event in the flusher

**Files:**
- Modify: `internal/store/pg_usage_flusher.go:347-388` (`toEvent`)
- Test: `internal/store/pg_usage_flusher_test.go`

**Step 1: Write the failing test**

Assert `toEvent` copies `record.EnergyJoules` and `record.ProviderMetadata` into the `UsageEvent`, and that a zero `EnergyJoules` on the record yields a nil pointer (SQL NULL).

**Step 2: Run it to verify it fails**

Run: `go test -run TestToEventCarriesProviderMetadata ./internal/store/`
Expected: FAIL.

**Step 3: Implement**

In `toEvent`'s returned `UsageEvent` literal, add:

```go
		EnergyJoules:        energyJoulesPtr(record.EnergyJoules),
		ProviderMetadata:    record.ProviderMetadata,
```

with:

```go
// energyJoulesPtr returns nil for an unmeasured (zero) reading so the column
// stores SQL NULL rather than a misleading zero.
func energyJoulesPtr(joules float64) *float64 {
	if joules == 0 {
		return nil
	}
	return &joules
}
```

**Step 4: Run tests**

Run: `go test ./internal/store/`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/store/
git add internal/store/pg_usage_flusher.go internal/store/pg_usage_flusher_test.go
git commit -m "feat(store): flush provider energy and metadata into usage events"
```

---

## Phase 5 — Flex tier

### Task 17: Inject `service_tier` into Neuralwatt requests

**Files:**
- Modify: `internal/runtime/executor/neuralwatt_executor.go` (or a new `neuralwatt_request.go`)
- Test: `internal/runtime/executor/neuralwatt_request_test.go`

**Step 1: Write the failing tests**

```go
func TestNeuralwattRequestInjectsServiceTier(t *testing.T) {
	payload := []byte(`{"model":"deepseek-v4-pro","messages":[]}`)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"service_tier": "flex"}}

	got := applyNeuralwattServiceTier(payload, auth)
	if gjson.GetBytes(got, "service_tier").String() != "flex" {
		t.Fatalf("service_tier = %q, want flex", gjson.GetBytes(got, "service_tier").String())
	}
}

func TestNeuralwattRequestLeavesTierUnsetWhenNotConfigured(t *testing.T) {
	payload := []byte(`{"model":"deepseek-v4-pro","messages":[]}`)
	got := applyNeuralwattServiceTier(payload, &cliproxyauth.Auth{})
	if gjson.GetBytes(got, "service_tier").Exists() {
		t.Fatal("service_tier must not be injected when the credential sets no tier")
	}
}
```

**Step 2: Run them to verify they fail**

Run: `go test -run TestNeuralwattRequest ./internal/runtime/executor/`
Expected: FAIL — `undefined: applyNeuralwattServiceTier`.

**Step 3: Implement**

Add a small helper that reads `auth.Attributes["service_tier"]`, accepts only `"flex"` or `"default"` (ignoring anything else, so a typo cannot send a value the API rejects with a 400), and stamps it onto the translated payload with `sjson.SetBytes`. Call it from `NeuralwattExecutor.Execute` and `ExecuteStream` on `req.Payload` before delegating to the compat executor.

**Step 4: Run tests to verify they pass**

Run: `go test -run TestNeuralwattRequest ./internal/runtime/executor/`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/runtime/executor/
git add internal/runtime/executor/neuralwatt_executor.go internal/runtime/executor/neuralwatt_request_test.go
git commit -m "feat(neuralwatt): inject the configured service tier into requests"
```

---

### Task 18: Retry a flex 503 once on the default tier

**Files:**
- Modify: `internal/runtime/executor/neuralwatt_executor.go`
- Test: `internal/runtime/executor/neuralwatt_flex_retry_test.go`

**Step 1: Write the failing test**

Using `httptest`, stand up a fake Neuralwatt server that returns `503` when the request body contains `"service_tier":"flex"` and `200` otherwise. Assert that a single `Execute` call with a flex credential succeeds and that the server observed exactly two requests — the flex attempt, then the default retry.

**Step 2: Run it to verify it fails**

Run: `go test -run TestNeuralwattFlexRetry ./internal/runtime/executor/`
Expected: FAIL — the 503 propagates.

**Step 3: Implement**

In `NeuralwattExecutor.Execute` and `ExecuteStream`, when the credential's tier is `flex` and the compat call fails with a `503` status error, retry **once** with `service_tier: "default"` on the same auth, and mark the retried request's provider metadata with `"flex_downgraded": true` so the dashboard can show the shed happened. Do not retry any other status, and do not loop.

Determine the status with the existing `StatusError` interface (`sdk/cliproxy/executor/types.go:209-215`) via a type assertion, not string matching on the error text.

**Per the AGENTS.md timeout rule:** do not add any timeout to the retry — the upstream connection already exists.

**Step 4: Run tests to verify they pass**

Run: `go test -run TestNeuralwatt ./internal/runtime/executor/`
Expected: PASS.

**Step 5: Commit**

```bash
gofmt -w internal/runtime/executor/
git add internal/runtime/executor/neuralwatt_executor.go internal/runtime/executor/neuralwatt_flex_retry_test.go
git commit -m "feat(neuralwatt): retry a flex 503 once on the default tier"
```

---

## Phase 6 — Dashboard, examples, and verification

### Task 19: Surface Neuralwatt in the dashboard

**Files:**
- Modify: `web/dashboard/src/pages/upstream-provider-editor/schemas.js:15-25, 122-397`
- Modify: `web/dashboard/src/api/client.js:1562-1575`
- Modify: `web/dashboard/src/pages/upstream-provider-editor/CreateMode.jsx:72-75`
- Modify: `web/dashboard/src/pages/upstream-provider-editor/OverviewTab.jsx:67-70`
- Test: `web/dashboard/src/pages/upstream-provider-editor/*.test.js`

**Step 1: Add the type to the catalog**

In `schemas.js`, add `'neuralwatt-api-key'` to `API_KEY_TYPES` and add a `neuralwatt-api-key` entry to `buildSchemas()` mirroring the `meta-api-key` schema, extended with the `service-tier` select (`default` | `flex`).

**Step 2: Register the list endpoint**

In `client.js`, add `neuralwatt: 'neuralwatt-api-key'` to the `providerListEndpoint` map.

**Step 3: Allow the models probe**

Add `'neuralwatt-api-key'` to `FETCHABLE_TYPES` in both `CreateMode.jsx` and `OverviewTab.jsx`.

**Step 4: Run the dashboard tests**

Run: `cd web/dashboard && npm test -- --run`
Expected: PASS. If the suite needs a build first: `npm install && npm test -- --run`.

**Step 5: Commit**

```bash
git add web/dashboard/src/
git commit -m "feat(dashboard): add the neuralwatt-api-key provider type"
```

---

### Task 20: Document the provider and verify end to end

**Files:**
- Modify: `config.example.yaml` (add a `neuralwatt-api-key:` block near the `xai-api-key:` example at `:468`)
- Test: full suite

**Step 1: Add the example config**

```yaml
# Neuralwatt (OpenAI-compatible, static bearer key).
# service-tier: default (standard) | flex (discounted, may be capacity-shed).
neuralwatt-api-key:
  - api-key: "sk-neuralwatt-..."
    base-url: "https://api.neuralwatt.com/v1"
    service-tier: "default"
    models:
      - name: "deepseek-v4-pro"
        alias: "deepseek-v4-pro"
```

**Step 2: Format and build**

Run:
```bash
gofmt -w .
go build -o test-output ./cmd/server && rm test-output
```
Expected: success.

**Step 3: Run the full test suite**

Run: `go test ./... 2>&1 | grep -E "^(FAIL|ok .*FAIL)" | head -30`
Expected: only the three pre-existing `internal/runtime/executor` Claude-header failures documented at the top of this plan. Investigate anything else.

**Step 4: Commit**

```bash
git add config.example.yaml
git commit -m "docs(config): add the neuralwatt-api-key example"
```

---

## Verification checklist

Before declaring the work complete, confirm each of the design's success criteria:

- [ ] A Neuralwatt credential configured under `neuralwatt-api-key` is synthesized into a live auth and appears as a routable provider.
- [ ] A non-streaming request records `energy_joules` and `provider_metadata.neuralwatt.request_cost_usd`.
- [ ] A streaming request records the cost parsed from the SSE `: cost {...}` comment.
- [ ] A response with `energy.measurement_available: false` stores NULL energy, not `0`.
- [ ] `cost_usd` still comes from the NixLLM price table (Neuralwatt's reported cost lives only in `provider_metadata`).
- [ ] A flex credential that receives a 503 retries once on `default` and succeeds.
- [ ] `response_service_tier` reflects the tier Neuralwatt actually served.
- [ ] Only the three documented pre-existing test failures remain.
