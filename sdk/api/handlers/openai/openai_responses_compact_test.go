package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

type compactCaptureExecutor struct {
	alt          string
	sourceFormat string
	calls        int
	payload      []byte
}

func (e *compactCaptureExecutor) Identifier() string { return "test-provider" }

func (e *compactCaptureExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.alt = opts.Alt
	e.sourceFormat = opts.SourceFormat.String()
	payload := e.payload
	if len(payload) == 0 {
		payload = []byte(`{"ok":true}`)
	}
	return coreexecutor.Response{Payload: payload}, nil
}

func (e *compactCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *compactCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *compactCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *compactCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestOpenAIResponsesCompactRejectsStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &compactCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth1", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses/compact", h.Compact)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"test-model","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.calls)
	}
}

func TestOpenAIResponsesCompactExecute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &compactCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth2", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses/compact", h.Compact)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"test-model","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if executor.alt != "responses/compact" {
		t.Fatalf("alt = %q, want %q", executor.alt, "responses/compact")
	}
	if executor.sourceFormat != "openai-response" {
		t.Fatalf("source format = %q, want %q", executor.sourceFormat, "openai-response")
	}
	if strings.TrimSpace(resp.Body.String()) != `{"ok":true}` {
		t.Fatalf("body = %s", resp.Body.String())
	}
}

func TestOpenAIResponsesCompactAugmentsHiddenOnlyOutputWithSameTurnEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &compactCaptureExecutor{
		payload: []byte(`{"id":"resp-compact","output":[{"type":"compaction_summary","summary":"hidden"}]}`),
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth-compact-evidence", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses/compact", h.Compact)

	body := `{
		"model":"test-model",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"old turn"}]},
			{"type":"message","role":"assistant","id":"stale-assistant","content":[{"type":"output_text","text":"stale assistant"}]},
			{"type":"function_call","call_id":"stale-call","name":"shell","arguments":"{}"},
			{"type":"function_call_output","call_id":"stale-call","output":"stale"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"please inspect"}]},
			{"type":"message","role":"assistant","id":"latest-assistant","content":[{"type":"output_text","text":"I will inspect the logs."}]},
			{"type":"function_call","call_id":"latest-call","name":"shell","arguments":"{}"},
			{"type":"function_call_output","call_id":"latest-call","output":"ok"}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	output := gjson.GetBytes(resp.Body.Bytes(), "output")
	if got, want := len(output.Array()), 4; got != want {
		t.Fatalf("output item count = %d, want %d; body=%s", got, want, resp.Body.String())
	}
	if got := output.Array()[1].Get("role").String(); got != "assistant" {
		t.Fatalf("merged assistant role = %q, want assistant", got)
	}
	if got := output.Array()[1].Get("id").String(); got != "latest-assistant" {
		t.Fatalf("merged assistant id = %q, want latest-assistant", got)
	}
	if got := output.Array()[2].Get("type").String(); got != "function_call" {
		t.Fatalf("merged tool call type = %q, want function_call", got)
	}
	if got := output.Array()[2].Get("call_id").String(); got != "latest-call" {
		t.Fatalf("merged tool call id = %q, want latest-call", got)
	}
	if got := output.Array()[3].Get("type").String(); got != "function_call_output" {
		t.Fatalf("merged tool output type = %q, want function_call_output", got)
	}
	if got := output.Array()[3].Get("call_id").String(); got != "latest-call" {
		t.Fatalf("merged tool output id = %q, want latest-call", got)
	}
	if strings.Contains(resp.Body.String(), "stale-call") || strings.Contains(resp.Body.String(), "stale-assistant") {
		t.Fatalf("response injected stale evidence: %s", resp.Body.String())
	}
}

func TestOpenAIResponsesCompactSkipsOrphanToolOutputEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &compactCaptureExecutor{
		payload: []byte(`{"id":"resp-compact","output":[{"type":"compaction_summary","summary":"hidden"}]}`),
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth-compact-orphan", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses/compact", h.Compact)

	body := `{
		"model":"test-model",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]},
			{"type":"function_call_output","call_id":"missing-call","output":"orphan"}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	output := gjson.GetBytes(resp.Body.Bytes(), "output")
	if got, want := len(output.Array()), 1; got != want {
		t.Fatalf("output item count = %d, want %d; body=%s", got, want, resp.Body.String())
	}
	if got := output.Array()[0].Get("type").String(); got != "compaction_summary" {
		t.Fatalf("output[0].type = %q, want compaction_summary", got)
	}
	if strings.Contains(resp.Body.String(), "missing-call") || strings.Contains(resp.Body.String(), "orphan") {
		t.Fatalf("response injected orphan tool output: %s", resp.Body.String())
	}
}

func TestCompactSameTurnEvidenceRequiresTailMarker(t *testing.T) {
	evidence, err := compactSameTurnEvidenceJSON([]byte(`{
		"input":[
			{"type":"message","role":"assistant","id":"assistant-without-marker"},
			{"type":"function_call","call_id":"call-without-marker","name":"shell"},
			{"type":"function_call_output","call_id":"call-without-marker","output":"ok"}
		]
	}`))
	if err != nil {
		t.Fatalf("compactSameTurnEvidenceJSON: %v", err)
	}
	if evidence.hit {
		t.Fatalf("evidence hit = true, want false; evidence=%s", evidence.rawJSON)
	}
	if evidence.skipped {
		t.Fatalf("evidence skipped = true, want false; reason=%s", evidence.skipReason)
	}
}

func TestCompactSameTurnEvidenceUsesCompactionMarkerTail(t *testing.T) {
	evidenceResult, err := compactSameTurnEvidenceJSON([]byte(`{
		"input":[
			{"type":"message","role":"assistant","id":"stale-assistant"},
			{"type":"function_call","call_id":"stale-call","name":"shell"},
			{"type":"function_call_output","call_id":"stale-call","output":"stale"},
			{"type":"compaction_summary","summary":"hidden"},
			{"type":"message","role":"assistant","id":"latest-assistant"},
			{"type":"custom_tool_call","call_id":"latest-call","name":"apply_patch"},
			{"type":"custom_tool_call_output","call_id":"latest-call","output":"ok"},
			{"type":"custom_tool_call_output","call_id":"orphan-call","output":"skip"}
		]
	}`))
	if err != nil {
		t.Fatalf("compactSameTurnEvidenceJSON: %v", err)
	}
	if !evidenceResult.hit {
		t.Fatalf("evidence hit = false, want true")
	}
	if !evidenceResult.skipped {
		t.Fatalf("evidence skipped = false, want true for orphan output")
	}
	if evidenceResult.skipReason != compactEvidenceSkipToolOutputBeforeCall {
		t.Fatalf("skip reason = %q, want %q", evidenceResult.skipReason, compactEvidenceSkipToolOutputBeforeCall)
	}
	evidence := gjson.Parse(evidenceResult.rawJSON)
	if got, want := len(evidence.Array()), 3; got != want {
		t.Fatalf("evidence item count = %d, want %d; evidence=%s", got, want, evidenceResult.rawJSON)
	}
	if strings.Contains(evidenceResult.rawJSON, "stale-call") || strings.Contains(evidenceResult.rawJSON, "orphan-call") {
		t.Fatalf("evidence contains stale or orphan item: %s", evidenceResult.rawJSON)
	}
}

func TestCompactSameTurnEvidenceRequiresCallBeforeOutput(t *testing.T) {
	evidenceResult, err := compactSameTurnEvidenceJSON([]byte(`{
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]},
			{"type":"function_call_output","call_id":"late-call","output":"skip"},
			{"type":"function_call","call_id":"late-call","name":"shell","arguments":"{}"}
		]
	}`))
	if err != nil {
		t.Fatalf("compactSameTurnEvidenceJSON: %v", err)
	}
	if !evidenceResult.hit {
		t.Fatalf("evidence hit = false, want true for later tool call")
	}
	if !evidenceResult.skipped {
		t.Fatalf("evidence skipped = false, want true for output before call")
	}
	if evidenceResult.skipReason != compactEvidenceSkipToolOutputBeforeCall {
		t.Fatalf("skip reason = %q, want %q", evidenceResult.skipReason, compactEvidenceSkipToolOutputBeforeCall)
	}
	evidence := gjson.Parse(evidenceResult.rawJSON)
	if got, want := len(evidence.Array()), 1; got != want {
		t.Fatalf("evidence item count = %d, want %d; evidence=%s", got, want, evidenceResult.rawJSON)
	}
	if got := evidence.Array()[0].Get("type").String(); got != "function_call" {
		t.Fatalf("evidence[0].type = %q, want function_call; evidence=%s", got, evidenceResult.rawJSON)
	}
	if strings.Contains(evidenceResult.rawJSON, "skip") || strings.Contains(evidenceResult.rawJSON, "function_call_output") {
		t.Fatalf("evidence injected out-of-order tool output: %s", evidenceResult.rawJSON)
	}
}
