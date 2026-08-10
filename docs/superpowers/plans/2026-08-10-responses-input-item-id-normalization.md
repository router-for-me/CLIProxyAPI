# Responses Input Item ID Normalization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite the two observed Codex Desktop `item_` IDs to the prefixes required by the Responses API before CLIProxyAPI forwards an HTTP `/v1/responses` request.

**Architecture:** Add one deterministic helper in the OpenAI handler package and invoke it once at the HTTP Responses boundary. The helper changes only `function_call` and `message` items whose IDs begin with `item_`; all valid IDs, unknown item types, ordering, and `call_id` values remain untouched.

**Tech Stack:** Go 1.26, Gin, `gjson`, `sjson`, Docker-based test runner, code-review-graph.

---

## Baseline and File Map

The branch starts at `6e92e3e6`. `go test ./sdk/api/handlers/openai -count=1` passes. The full `go test ./...` baseline has exactly three unrelated container-platform fingerprint failures:

- `TestApplyClaudeHeaders_DisableDeviceProfileStabilization`
- `TestApplyClaudeHeaders_LegacyModePreservesConfiguredUserAgentOverrideForClaudeClients`
- `TestClaudeExecutor_NonClaudeRequestUsesClaudeCode220CLIFingerprint`

Files:

- Create: `sdk/api/handlers/openai/openai_responses_input_item_ids.go` - deterministic ID-prefix normalization only.
- Create: `sdk/api/handlers/openai/openai_responses_input_item_ids_test.go` - direct helper tests and HTTP boundary regression test.
- Modify: `sdk/api/handlers/openai/openai_responses_handlers.go:402-425` - call the helper after request-body decoding.
- Reuse test fixture: `sdk/api/handlers/openai/openai_responses_multi_agent_test.go:90-130` - capture executor and handler constructor; do not modify it.

### Task 1: Add and prove the deterministic normalizer

**Files:**

- Create: `sdk/api/handlers/openai/openai_responses_input_item_ids_test.go`
- Create: `sdk/api/handlers/openai/openai_responses_input_item_ids.go`

- [ ] **Step 1: Write the direct failing tests**

Create `sdk/api/handlers/openai/openai_responses_input_item_ids_test.go` with:

~~~go
package openai

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeResponsesInputItemIDsRewritesObservedLocalIDs(t *testing.T) {
	payload := []byte(`{"model":"test-model","input":[{"type":"function_call","id":"item_call123","call_id":"call_1","name":"wait"},{"type":"function_call_output","id":"fco_1","call_id":"call_1","output":"done"},{"type":"message","id":"item_msg456","role":"assistant","content":[]}]}`)

	got := normalizeResponsesInputItemIDs(payload)

	checks := map[string]string{
		"input.0.id":      "fc_call123",
		"input.0.call_id": "call_1",
		"input.1.id":      "fco_1",
		"input.1.call_id": "call_1",
		"input.2.id":      "msg_msg456",
		"input.2.role":    "assistant",
	}
	for path, want := range checks {
		if actual := gjson.GetBytes(got, path).String(); actual != want {
			t.Fatalf("%s = %q, want %q; payload=%s", path, actual, want, got)
		}
	}
}

func TestNormalizeResponsesInputItemIDsPreservesValidAndUnknownItems(t *testing.T) {
	payload := []byte(`{"input":[{"type":"function_call","id":"fc_valid","call_id":"call_1"},{"type":"message","id":"msg_valid","role":"assistant"},{"type":"custom_tool_call","id":"item_custom","call_id":"call_2"}]}`)

	got := normalizeResponsesInputItemIDs(payload)

	if !bytes.Equal(got, payload) {
		t.Fatalf("payload changed unexpectedly: %s", got)
	}
}
~~~

- [ ] **Step 2: Run the direct tests and verify RED**

Run:

~~~bash
docker run --rm -v /home/elsen_xu/CLIProxyAPI/.worktrees/fix-responses-item-id:/src -w /src golang:1.26 go test ./sdk/api/handlers/openai -run '^TestNormalizeResponsesInputItemIDs' -count=1
~~~

Expected: build failure containing `undefined: normalizeResponsesInputItemIDs`. This is the missing behavior, not a test setup failure.

- [ ] **Step 3: Implement the minimal helper**

Create `sdk/api/handlers/openai/openai_responses_input_item_ids.go` with:

~~~go
package openai

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func normalizeResponsesInputItemIDs(payload []byte) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.Exists() || !input.IsArray() {
		return payload
	}

	normalized := payload
	changed := false
	for index, item := range input.Array() {
		suffix, localID := strings.CutPrefix(item.Get("id").String(), "item_")
		if !localID || suffix == "" {
			continue
		}

		var prefix string
		switch strings.TrimSpace(item.Get("type").String()) {
		case "function_call":
			prefix = "fc_"
		case "message":
			prefix = "msg_"
		default:
			continue
		}

		updated, err := sjson.SetBytes(normalized, fmt.Sprintf("input.%d.id", index), prefix+suffix)
		if err != nil {
			return payload
		}
		normalized = updated
		changed = true
	}
	if !changed {
		return payload
	}
	return normalized
}
~~~

- [ ] **Step 4: Run the direct tests and verify GREEN**

Run the command from Step 2 again.

Expected: `ok github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai`.

- [ ] **Step 5: Format and commit the helper**

Run:

~~~bash
docker run --rm -v /home/elsen_xu/CLIProxyAPI/.worktrees/fix-responses-item-id:/src -w /src golang:1.26 gofmt -w sdk/api/handlers/openai/openai_responses_input_item_ids.go sdk/api/handlers/openai/openai_responses_input_item_ids_test.go
git add sdk/api/handlers/openai/openai_responses_input_item_ids.go sdk/api/handlers/openai/openai_responses_input_item_ids_test.go
git commit -m "fix(openai): normalize local responses item ids"
~~~

### Task 2: Wire normalization into HTTP `/v1/responses`

**Files:**

- Modify: `sdk/api/handlers/openai/openai_responses_input_item_ids_test.go`
- Modify: `sdk/api/handlers/openai/openai_responses_handlers.go:402-425`

- [ ] **Step 1: Add the HTTP boundary regression test**

Extend the test file import block to:

~~~go
import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)
~~~

Append this test. It intentionally reuses `responsesMultiAgentCaptureExecutor` and `newResponsesMultiAgentTestHandler` from `openai_responses_multi_agent_test.go` so it exercises the real HTTP handler without new executor boilerplate:

~~~go
func TestResponsesNormalizesLocalInputItemIDsBeforeExecution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &responsesMultiAgentCaptureExecutor{}
	handler, modelID := newResponsesMultiAgentTestHandler(t, executor)
	router := gin.New()
	router.POST("/v1/responses", handler.Responses)

	payload := fmt.Sprintf(`{"model":%q,"input":[{"type":"function_call","id":"item_call123","call_id":"call_1","name":"wait"},{"type":"function_call_output","id":"fco_1","call_id":"call_1","output":"done"},{"type":"message","id":"item_msg456","role":"assistant","content":[]}],"stream":false}`, modelID)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(payload))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	payloads := executor.Payloads()
	if len(payloads) != 1 {
		t.Fatalf("captured payload count = %d, want 1", len(payloads))
	}
	captured := payloads[0]
	if actual := gjson.GetBytes(captured, "input.0.id").String(); actual != "fc_call123" {
		t.Fatalf("function_call id = %q, want fc_call123; payload=%s", actual, captured)
	}
	if actual := gjson.GetBytes(captured, "input.2.id").String(); actual != "msg_msg456" {
		t.Fatalf("message id = %q, want msg_msg456; payload=%s", actual, captured)
	}
	if actual := gjson.GetBytes(captured, "input.0.call_id").String(); actual != "call_1" {
		t.Fatalf("call_id = %q, want call_1; payload=%s", actual, captured)
	}
}
~~~

- [ ] **Step 2: Run the boundary test and verify RED**

Run:

~~~bash
docker run --rm -v /home/elsen_xu/CLIProxyAPI/.worktrees/fix-responses-item-id:/src -w /src golang:1.26 go test ./sdk/api/handlers/openai -run '^TestResponsesNormalizesLocalInputItemIDsBeforeExecution$' -count=1
~~~

Expected: assertion failure showing `function_call id = "item_call123", want fc_call123`. The helper exists, but the handler has not invoked it yet.

- [ ] **Step 3: Wire the helper at the HTTP boundary**

In `OpenAIResponsesAPIHandler.Responses`, immediately after the successful `handlers.ReadRequestBody` error check and before `prepareCodexMultiAgentV2Tools`, add:

~~~go
	rawJSON = normalizeResponsesInputItemIDs(rawJSON)
	rawJSON = h.prepareCodexMultiAgentV2Tools(c, rawJSON)
~~~

The resulting handler must normalize both streaming and non-streaming HTTP requests because the branch decision occurs afterward.

- [ ] **Step 4: Format and verify GREEN**

Run:

~~~bash
docker run --rm -v /home/elsen_xu/CLIProxyAPI/.worktrees/fix-responses-item-id:/src -w /src golang:1.26 gofmt -w sdk/api/handlers/openai/openai_responses_handlers.go sdk/api/handlers/openai/openai_responses_input_item_ids_test.go
docker run --rm -v /home/elsen_xu/CLIProxyAPI/.worktrees/fix-responses-item-id:/src -w /src golang:1.26 go test ./sdk/api/handlers/openai -run '^(TestNormalizeResponsesInputItemIDs|TestResponsesNormalizesLocalInputItemIDsBeforeExecution)' -count=1
~~~

Expected: both direct and boundary tests pass.

- [ ] **Step 5: Commit the boundary wiring**

Run:

~~~bash
git add sdk/api/handlers/openai/openai_responses_handlers.go sdk/api/handlers/openai/openai_responses_input_item_ids_test.go
git commit -m "fix(openai): repair local item ids at responses boundary"
~~~

### Task 3: Verify scope, regressions, and graph impact

**Files:**

- Verify: `sdk/api/handlers/openai/openai_responses_input_item_ids.go`
- Verify: `sdk/api/handlers/openai/openai_responses_input_item_ids_test.go`
- Verify: `sdk/api/handlers/openai/openai_responses_handlers.go`

- [ ] **Step 1: Run the complete focused package**

Run:

~~~bash
docker run --rm -v /home/elsen_xu/CLIProxyAPI/.worktrees/fix-responses-item-id:/src -w /src golang:1.26 go test ./sdk/api/handlers/openai -count=1
~~~

Expected: package passes with no failures.

- [ ] **Step 2: Re-run the wider suite and compare with baseline**

Run:

~~~bash
docker run --rm -v /home/elsen_xu/CLIProxyAPI/.worktrees/fix-responses-item-id:/src -w /src golang:1.26 go test ./...
~~~

Expected: either a clean pass or only the same three `internal/runtime/executor` platform-fingerprint failures listed above. Stop if any additional test fails.

- [ ] **Step 3: Refresh and review with code-review-graph**

Run:

~~~bash
/home/elsen_xu/.local/share/pipx/venvs/code-review-graph/bin/python -m code_review_graph update --brief --base 6e92e3e6 --repo /home/elsen_xu/CLIProxyAPI/.worktrees/fix-responses-item-id --data-dir /home/elsen_xu/CLIProxyAPI/.code-review-graph
/home/elsen_xu/.local/share/pipx/venvs/code-review-graph/bin/python -m code_review_graph detect-changes --brief --base 6e92e3e6 --repo /home/elsen_xu/CLIProxyAPI/.worktrees/fix-responses-item-id
~~~

Expected: the risk report is limited to the HTTP Responses handler, the new helper, and their tests; no websocket or translator execution flow should be marked as changed.

- [ ] **Step 4: Perform final Git checks**

Run:

~~~bash
git diff --check 6e92e3e6..HEAD
git status --short --branch
git log --oneline --decorate 6e92e3e6..HEAD
~~~

Expected: no whitespace errors, a clean `codex/fix-responses-item-id` worktree, and the design plus two implementation commits.

- [ ] **Step 5: Request code review before deployment**

Use the `requesting-code-review` skill against `6e92e3e6..HEAD`. Resolve every Critical or Important finding and rerun the focused package test. Do not build or restart the production Compose service until the user explicitly approves deployment.

