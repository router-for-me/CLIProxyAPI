# Provider Entry Test Button Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** A "Test" panel on the upstream provider editor that runs one pinned chat-completion probe (random math question) against a chosen entry or the provider level, through the real execution pipeline, and reports latency/answer/errors.

**Architecture:** New management endpoint `POST /v0/management/upstream-providers/:id/test` resolves the target auth from `authManager.List()` by `provider_key`/`entry_provider_key` attributes, builds an OpenAI-format payload with a server-generated math question, and executes via `authManager.Execute` pinned with `PinnedAuthMetadataKey`. The frontend adds `TestPanel.jsx` rendered last in the editor (edit mode only).

**Tech Stack:** Go (gin, sdk/cliproxy/auth, executor), React (Vite SPA), node:test.

**Design doc:** `docs/plans/2026-09-07-provider-entry-test-design.md`
**Working dir:** `/home/bilfid/projects/nixllm` (main branch, inline execution)

---

## Task 1: Backend — math question generator + unit test

**Files:**
- Create: `internal/api/handlers/management/upstream_providers_testgen.go`
- Test: `internal/api/handlers/management/upstream_providers_testgen_test.go`

**Step 1: Write the failing test**

```go
package management

import (
	"strings"
	"testing"
)

func TestGenerateMathQuestionShape(t *testing.T) {
	for i := 0; i < 200; i++ {
		q := generateMathQuestion()
		if q.Question == "" {
			t.Fatal("question text is empty")
		}
		if !strings.Contains(q.Question, "What is") {
			t.Fatalf("question %q does not contain the prompt prefix", q.Question)
		}
		// Subtraction must never go negative; operands are two-digit.
		if q.Expected < 0 {
			t.Fatalf("expected answer %d is negative", q.Expected)
		}
		// Recompute the answer from the printed question.
		var a, b int
		var op string
		if _, err := fmtSscanf3(q.Question, &a, &op, &b); err != nil {
			t.Fatalf("cannot parse question %q: %v", q.Question, err)
		}
		var want int
		switch op {
		case "+":
			want = a + b
		case "×":
			want = a * b
		case "-":
			want = a - b
		default:
			t.Fatalf("unexpected operator %q", op)
		}
		if q.Expected != want {
			t.Fatalf("question %q: expected answer %d, computed %d", q.Question, q.Expected, want)
		}
	}
}

func TestGenerateMathQuestionVaries(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 50; i++ {
		seen[generateMathQuestion().Question] = struct{}{}
	}
	if len(seen) < 10 {
		t.Fatalf("only %d distinct questions in 50 draws; generator looks constant", len(seen))
	}
}
```

Note for implementer: `fmtSscanf3` doesn't exist — use `fmt.Sscanf(q.Question, "What is %d %s %d? Reply with just the number.", &a, &op, &b)` directly in the test (the `%s` stops at the space; verify the format works — if Sscanf parsing proves brittle, assert arithmetically on the struct fields instead: expose `A, B, Op` in the struct and drop the string parsing).

**Step 2: Run test to verify it fails**

Run: `go test -run TestGenerateMathQuestion ./internal/api/handlers/management/ -v`
Expected: FAIL — `generateMathQuestion` undefined

**Step 3: Write the implementation**

```go
package management

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// mathQuestion is one generated probe question. Expected is the exact
// integer answer so the handler can return it for the dashboard's
// comparison display.
type mathQuestion struct {
	Question string
	Expected int
	A        int
	B        int
	Op       string
}

// generateMathQuestion builds a random arithmetic probe: two 11..99
// operands with +, ×, or a subtraction guaranteed non-negative. Random
// content keeps the probe off upstream prompt caches.
func generateMathQuestion() mathQuestion {
	a := randInt(11, 99)
	b := randInt(11, 99)
	switch randInt(0, 2) {
	case 0:
		return mathQuestion{
			Question: fmt.Sprintf("What is %d + %d? Reply with just the number.", a, b),
			Expected: a + b, A: a, B: b, Op: "+",
		}
	case 1:
		return mathQuestion{
			Question: fmt.Sprintf("What is %d × %d? Reply with just the number.", a, b),
			Expected: a * b, A: a, B: b, Op: "×",
		}
	default:
		if b > a {
			a, b = b, a
		}
		return mathQuestion{
			Question: fmt.Sprintf("What is %d - %d? Reply with just the number.", a, b),
			Expected: a - b, A: a, B: b, Op: "-",
		}
	}
}

func randInt(min, max int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	if err != nil {
		return min
	}
	return min + int(n.Int64())
}
```

**Step 4: Run test to verify it passes**

Run: `go test -run TestGenerateMathQuestion ./internal/api/handlers/management/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/api/handlers/management/upstream_providers_testgen.go internal/api/handlers/management/upstream_providers_testgen_test.go
git commit -m "feat(management): random math probe question generator"
```

## Task 2: Backend — test endpoint + auth resolution + route

**Files:**
- Create: `internal/api/handlers/management/upstream_providers_test.go` (handler — note: this is a NEW file; the existing `upstream_providers_test.go` in management does NOT exist, check first; if a file with that name exists, name it `upstream_providers_testrun.go`)
- Modify: `internal/api/server_management.go:355` (add route after DELETE)
- Test: same new file's test functions or a separate `_test.go`

**Step 1: Write the failing handler test** (HTTP-level, no real execution — the auth manager will be nil/empty so resolution fails before Execute):

```go
func TestTestUpstreamProviderValidationAndNotFound(t *testing.T) {
	// Build a minimal Handler with no stores/authManager set the same way
	// other management handler tests do (see existing handler test setup in
	// the package — reuse the established fixture; if none exists, construct
	// gin + Handler{} directly).
	gin.SetMode(gin.TestMode)
	h := &Handler{}
	w := gin.CreateTestContext(httptest.NewRecorder())
	// ... POST body missing model -> 400
	// ... unknown provider id -> 404 (store unavailable path or missing row)
}
```

Implementer note: check how existing management tests construct `Handler` + gin (grep `gin.CreateTestContext` in the package). If the package has no HTTP-test fixture at all, test only the pure resolution helper (`resolveTestTargetAuth`) and the body parsing function, and validate the route by build + manual verification. Do not invent a heavy fixture.

**Step 2: Implement the handler**

```go
// upstreamProviderTestRequest is the JSON body for POST
// /upstream-providers/:id/test. EntryID nil tests the provider level
// (single-key/OAuth); a positive ID pins one api_key_entries child row.
type upstreamProviderTestRequest struct {
	EntryID *int64 `json:"entry_id"`
	Model   string `json:"model"`
}

type upstreamProviderTestResponse struct {
	OK         bool   `json:"ok"`
	LatencyMs  int64  `json:"latency_ms"`
	Question   string `json:"question"`
	Expected   int    `json:"expected_answer"`
	Answer     string `json:"answer,omitempty"`
	Model      string `json:"model"`
	EntryID    *int64 `json:"entry_id,omitempty"`
	Error      string `json:"error,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
}

// channelForProviderRow maps an upstream provider_type to the runtime
// channel name used in provider_key attributes. Mirrors the synthesizer
// call sites in internal/watcher/synthesizer/config.go.
func channelForProviderRow(p store.UpstreamProvider) string {
	switch p.ProviderType {
	case upstreamsync.TypeGeminiAPIKey:
		return "gemini"
	case upstreamsync.TypeInteractionsAPIKey:
		return "interactions"
	case upstreamsync.TypeCodexAPIKey:
		return "codex"
	case upstreamsync.TypeXAIAPIKey:
		return "xai"
	case upstreamsync.TypeClaudeAPIKey:
		return "claude"
	case upstreamsync.TypeVertexAPIKey:
		return "vertex"
	case upstreamsync.TypeOpenAICompatibility:
		return strings.TrimSpace(p.Name)
	default:
		return upstreamsync.OAuthChannel(p.ProviderType)
	}
}

// resolveTestTargetAuth finds the live auth for a provider row / entry by
// the provider_key / entry_provider_key attributes stamped by the
// synthesizer. Returns nil when the credential is not live in the registry.
func (h *Handler) resolveTestTargetAuth(rowID int64, channel string, entryID *int64) *coreauth.Auth {
	if h == nil || h.authManager == nil || rowID <= 0 || channel == "" {
		return nil
	}
	parentKey := channel + ":" + strconv.FormatInt(rowID, 10)
	wantEntry := ""
	if entryID != nil && *entryID > 0 {
		wantEntry = parentKey + ":key-" + strconv.FormatInt(*entryID, 10)
	}
	for _, auth := range h.authManager.List() {
		if auth == nil || auth.Disabled {
			continue
		}
		attrs := auth.Attributes
		if wantEntry != "" {
			if attrs[coreauth.AttributeEntryProviderKey] == wantEntry {
				return auth
			}
			continue
		}
		// Provider-level probe: match the parent key, but prefer an auth
		// WITHOUT an entry key (legacy single-key rows).
		if attrs["provider_key"] == parentKey && attrs[coreauth.AttributeEntryProviderKey] == "" {
			return auth
		}
	}
	if wantEntry == "" {
		// Fall back to any auth under the parent key (e.g. openai-compat
		// entries whose parent auth carries no entry distinction).
		for _, auth := range h.authManager.List() {
			if auth != nil && !auth.Disabled && auth.Attributes["provider_key"] == parentKey {
				return auth
			}
		}
	}
	return nil
}

// TestUpstreamProvider handles POST /v0/management/upstream-providers/:id/test.
func (h *Handler) TestUpstreamProvider(c *gin.Context) {
	id, errID := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if errID != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid provider id"})
		return
	}
	var body upstreamProviderTestRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing model"})
		return
	}

	srcs := h.upstreamProvidersStore(c)
	if srcs == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "upstream provider store unavailable"})
		return
	}
	row, errGet := srcs.Get(c.Request.Context(), id)
	if errGet != nil || row == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
		return
	}

	channel := channelForProviderRow(*row)
	auth := h.resolveTestTargetAuth(id, channel, body.EntryID)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not live in registry — save and wait for reload, then retry"})
		return
	}

	q := generateMathQuestion()
	payload := []byte(fmt.Sprintf(
		`{"model":%q,"messages":[{"role":"user","content":%q}],"max_tokens":200,"stream":false}`,
		model, q.Question,
	))
	req := cliproxyexecutor.Request{
		Model:   model,
		Payload: payload,
		Format:  sdktranslator.Format(""), // manager infers from SourceFormat/registry
	}
	opts := cliproxyexecutor.Options{
		Stream:       false,
		SourceFormat: sdktranslator.FromString("openai"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: model,
			cliproxyexecutor.PinnedAuthMetadataKey:     auth.ID,
		},
	}

	probeCtx, cancel := context.WithTimeout(c.Request.Context(), modelHealthProbeTimeout)
	defer cancel()
	start := time.Now()
	resp, errExec := h.authManager.Execute(probeCtx, []string{channel}, req, opts)
	elapsed := time.Since(start)

	out := upstreamProviderTestResponse{
		LatencyMs: elapsed.Milliseconds(),
		Question:  q.Question,
		Expected:  q.Expected,
		Model:     model,
		EntryID:   body.EntryID,
	}
	if errExec != nil {
		out.OK = false
		out.Error = errExec.Error()
		c.JSON(http.StatusOK, out)
		return
	}
	out.OK = true
	out.Answer = extractOpenAICompletionText(resp.Payload)
	_, completionTokens := parseOpenAIUsage(resp.Payload)
	out.StatusCode = completionTokens // placeholder — remove; see note
	c.JSON(http.StatusOK, out)
}
```

Implementer notes:
- The `out.StatusCode` line above is a placeholder mistake to avoid — drop it. Do not set StatusCode.
- Imports: `context`, `fmt`, `net/http`, `strconv`, `strings`, `time`, gin, coreauth, store, upstreamsync, sdktranslator, cliproxyexecutor. Reuse `modelHealthProbeTimeout`, `extractOpenAICompletionText`, `parseOpenAIUsage` from `model_health_runner.go` (same package).
- Check `upstreamProvidersStore(c)` signature in `upstream_providers.go:40` — it returns `(store.UpstreamProviderStore, bool)`; adapt the nil check accordingly.
- Route registration in `server_management.go` after line 355:
  `mgmt.POST("/upstream-providers/:id/test", s.mgmt.TestUpstreamProvider)`

**Step 3: Build + run tests**

Run: `go build ./... && go test ./internal/api/handlers/management/`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/api/handlers/management/ internal/api/server_management.go
git commit -m "feat(management): pinned per-entry test probe endpoint for upstream providers"
```

## Task 3: Frontend — client function + TestPanel component

**Files:**
- Modify: `web/dashboard/src/api/client.js` (near `updateUpstreamProvider` ~line 1798)
- Create: `web/dashboard/src/pages/upstream-provider-editor/TestPanel.jsx`
- Modify: `web/dashboard/src/pages/upstream-provider-editor/index.jsx` (render panel last, edit mode only)

**Step 1: Client function**

```js
export async function testUpstreamProvider(id, { entryId, model }) {
  return fetchJSON(`/upstream-providers/${encodeURIComponent(id)}/test`, {
    method: 'POST',
    body: JSON.stringify({ entry_id: entryId ?? null, model }),
  });
}
```

**Step 2: TestPanel.jsx** — props: `{ provider, form }`. Pure helpers exported for tests:

```js
// entryLabel(e): e.name || `key-${e.id}`; suffix ' (disabled)' when e.disabled
// entryOptions(form): [{value:'',label:'(provider-level)'}, ...entries.filter(e=>e.id>0).map(...)]
// modelOptions(form): from form.models (r.name||r.alias, deduped, non-empty)
// describeResult(res): { ok:bool, title, detail } — title '✓ 200 OK · 842ms' or '✗ <error>';
//   when res.ok && trimmed answer !== String(res.expected_answer): flag 'wrong answer' ⚠
```

Component state: `entryId` ('' default), `model` (prefilled from first model option), `modelFree` (text input shown when models list empty or operator wants custom), `running`, `result`, `error`. Run button disabled while running or when no model resolved. Render as `form-section` with title "Test entry" and hint "Sends one small chat-completion probe (a random math question) pinned to this credential."

**Step 3: Wire into index.jsx** — inside the editor body, after the schema sections map (still inside the `<div>` that wraps them), gated:

```jsx
{isEdit && provider && (
  <TestPanel provider={provider} form={form} />
)}
```

**Step 4: Verify**

Run (from `web/dashboard/`): `npm run build`
Expected: build succeeds

**Step 5: Commit**

```bash
git add web/dashboard/src/api/client.js web/dashboard/src/pages/upstream-provider-editor/TestPanel.jsx web/dashboard/src/pages/upstream-provider-editor/index.jsx
git commit -m "feat(dashboard): test panel on upstream provider editor"
```

## Task 4: Frontend tests

**Files:**
- Modify: `web/dashboard/src/pages/upstream-provider-editor/editor.test.js` (pure helper tests)
- Modify: `web/dashboard/src/pages/upstream-provider-editor/editor-render.test.jsx` (panel render)

**Step 1: Pure tests** — import from `TestPanel.jsx`:

```js
test('TestPanel helpers: entry labels, options, result mapping', () => {
  // entryLabel: identity, key-<id> fallback, disabled suffix
  // entryOptions: filters id=0, includes provider-level '' option
  // modelOptions: name||alias, dedupe, drop empties
  // describeResult: ok+match → ok; ok+mismatch → 'wrong answer' flag;
  //                 !ok → error title
});
```

**Step 2: Render tests**:

```jsx
test('TestPanel renders in edit mode for entry-bearing providers', () => {
  const provider = { id: 5, provider_type: 'openai-compatibility', name: 'compat',
    api_key_entries: [{ id: 1, api_key: 'FAKE', name: 'live' }] };
  // render ProviderEditorForm with form containing models + entries —
  // note: TestPanel reads form state, which the render harness builds via
  // buildForm; render and assert 'Test entry' section + entry dropdown.
});
test('TestPanel absent in create mode', () => {
  // render page /new route; assert no 'Test entry' text.
});
```

**Step 3: Run suite**

Run (from `web/dashboard/`): `npm test`
Expected: all pass (baseline 208 + new)

**Step 4: Commit**

```bash
git add web/dashboard/src/pages/upstream-provider-editor/editor.test.js web/dashboard/src/pages/upstream-provider-editor/editor-render.test.jsx
git commit -m "test(dashboard): provider test panel helpers and render"
```

## Task 5: Full verification

**Step 1:** `gofmt -l .` — expect empty (fix with `gofmt -w` if listed).
**Step 2:** `go test ./internal/api/handlers/management/ ./internal/upstreamsync/` — expect PASS.
**Step 3:** `go build -o test-output ./cmd/server && rm test-output` — expect success.
**Step 4:** `make dash-embed` — expect success.
**Step 5:** Manual smoke (operator): run `bin/nixllm`, edit an openai-compat provider, run test on one entry with a known model, verify latency + expected answer; repeat at provider level for a gemini-api-key row.
**Step 6:** Commit any remaining embed changes if tracked files changed.

## Notes for the implementer

- `PinnedAuthMetadataKey` lives in `sdk/cliproxy/executor/types.go:44`; imported as `cliproxyexecutor` (same import as model_health_runner uses for `Request`/`Options`).
- `coreauth.AttributeEntryProviderKey` is in `sdk/cliproxy/auth/classification.go:20`.
- `entry_provider_key` format: `<channel>:<rowID>:key-<entryID>` (both claude and openai-compat); parent `provider_key` is `<channel>:<rowID>`. For openai-compat the channel is the row's `name` (lowercased by the runtime) — match case-insensitively if needed.
- Do NOT set HTTP timeouts on the probe path beyond `modelHealthProbeTimeout` (allowed category: management probe).
- `upstreamProvidersStore` returns `(store.UpstreamProviderStore, bool)` — use the bool, not a nil check on the interface.
- The probe uses the OpenAI request format for ALL providers — the manager translates; verified by model_health_runner doing the same for every provider type.
