package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	directCodexResponsesPath = "/backend-api/codex/responses"
	directCodexCompactPath   = "/backend-api/codex/responses/compact"
)

func TestCodexDirectPostContinuationUnknownPreviousResponseIDFailsClosed(t *testing.T) {
	resetCodexDirectContinuationsForTest(t)

	model := "gpt-5.4"
	capture := &directContinuationUpstreamCapture{}
	upstream := newDirectContinuationUpstream(t, capture, directContinuationUpstreamResponse{
		path: directCodexUpstreamResponsesPath,
		body: directContinuationSSECompleted("resp-unused", "[]"),
	})
	defer upstream.Close()

	h := newDirectContinuationHandler(t, nil, directContinuationAuthSpec{
		id:       "direct-unknown-auth",
		models:   []string{model},
		baseURL:  upstream.URL,
		provider: "codex",
		apiKey:   "test",
	})

	recorder := performDirectContinuationRequest(t, h, directCodexResponsesPath, directContinuationToolOutputBody(model, true, "resp-unknown"))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	assertDirectContinuationErrorCode(t, recorder.Body.Bytes(), "codex_continuation_auth_unknown")
	if capture.calls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", capture.calls.Load())
	}
}

func TestCodexDirectPostContinuationModelMismatchFailsClosed(t *testing.T) {
	resetCodexDirectContinuationsForTest(t)

	modelA := "gpt-5.4"
	modelB := "gpt-5.4-mini"
	responseID := "resp-model-a"
	capture := &directContinuationUpstreamCapture{}
	upstream := newDirectContinuationUpstream(t, capture, directContinuationUpstreamResponse{
		path: directCodexUpstreamResponsesPath,
		body: directContinuationSSECompleted(responseID, `[{"type":"message","role":"assistant","content":[]}]`),
	})
	defer upstream.Close()

	h := newDirectContinuationHandler(t, nil, directContinuationAuthSpec{
		id:       "direct-model-mismatch-auth",
		models:   []string{modelA, modelB},
		baseURL:  upstream.URL,
		provider: "codex",
		apiKey:   "test",
	})

	firstRecorder := performDirectContinuationRequest(t, h, directCodexResponsesPath, directContinuationUserMessageBody(modelA, true))
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want %d", firstRecorder.Code, http.StatusOK)
	}

	secondRecorder := performDirectContinuationRequest(t, h, directCodexResponsesPath, directContinuationUserMessageBodyWithPreviousID(modelB, true, responseID))
	if secondRecorder.Code != http.StatusConflict {
		t.Fatalf("second request status = %d, want %d", secondRecorder.Code, http.StatusConflict)
	}
	assertDirectContinuationErrorCode(t, secondRecorder.Body.Bytes(), "codex_continuation_auth_unknown")
	if capture.calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", capture.calls.Load())
	}
}

func TestCodexDirectPostContinuationPinsOriginalSelectedAuth(t *testing.T) {
	resetCodexDirectContinuationsForTest(t)

	model := "gpt-5.4"
	responseID := "resp-auth-a"
	captureA := &directContinuationUpstreamCapture{}
	upstreamA := newDirectContinuationUpstream(t, captureA,
		directContinuationUpstreamResponse{
			path: directCodexUpstreamResponsesPath,
			body: directContinuationSSECompleted(responseID, `[{"type":"message","role":"assistant","content":[]}]`),
		},
		directContinuationUpstreamResponse{
			path: directCodexUpstreamResponsesPath,
			body: directContinuationSSECompleted("resp-auth-a-2", `[{"type":"message","role":"assistant","content":[]}]`),
		},
	)
	defer upstreamA.Close()

	captureB := &directContinuationUpstreamCapture{}
	upstreamB := newDirectContinuationUpstream(t, captureB, directContinuationUpstreamResponse{
		path: directCodexUpstreamResponsesPath,
		body: directContinuationSSECompleted("resp-auth-b", `[{"type":"message","role":"assistant","content":[]}]`),
	})
	defer upstreamB.Close()

	selector := &directContinuationSequenceSelector{firstID: "direct-pin-auth-a", laterID: "direct-pin-auth-b"}
	h := newDirectContinuationHandler(t, selector,
		directContinuationAuthSpec{id: "direct-pin-auth-a", models: []string{model}, baseURL: upstreamA.URL, provider: "codex", apiKey: "test-a"},
		directContinuationAuthSpec{id: "direct-pin-auth-b", models: []string{model}, baseURL: upstreamB.URL, provider: "codex", apiKey: "test-b"},
	)

	firstRecorder := performDirectContinuationRequest(t, h, directCodexResponsesPath, directContinuationUserMessageBody(model, true))
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want %d", firstRecorder.Code, http.StatusOK)
	}
	secondRecorder := performDirectContinuationRequest(t, h, directCodexResponsesPath, directContinuationUserMessageBodyWithPreviousID(model, true, responseID))
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("second request status = %d, want %d", secondRecorder.Code, http.StatusOK)
	}
	if captureA.calls.Load() != 2 {
		t.Fatalf("auth A upstream calls = %d, want 2", captureA.calls.Load())
	}
	if captureB.calls.Load() != 0 {
		t.Fatalf("auth B upstream calls = %d, want 0", captureB.calls.Load())
	}
	assertDirectContinuationUpstreamInputCounts(t, captureA.lastBody(), map[string]int{"message": 3}, 3)
}

func TestCodexDirectPostContinuationRejectsOrphanToolOutputBeforeUpstream(t *testing.T) {
	resetCodexDirectContinuationsForTest(t)

	model := "gpt-5.4"
	responseID := "resp-missing-tool-call"
	capture := &directContinuationUpstreamCapture{}
	upstream := newDirectContinuationUpstream(t, capture, directContinuationUpstreamResponse{
		path: directCodexUpstreamResponsesPath,
		body: directContinuationSSECompleted(responseID, "[]"),
	})
	defer upstream.Close()

	h := newDirectContinuationHandler(t, nil, directContinuationAuthSpec{
		id:       "direct-orphan-tool-output-auth",
		models:   []string{model},
		baseURL:  upstream.URL,
		provider: "codex",
		apiKey:   "test",
	})

	firstRecorder := performDirectContinuationRequest(t, h, directCodexResponsesPath, directContinuationUserMessageBody(model, true))
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want %d", firstRecorder.Code, http.StatusOK)
	}

	secondRecorder := performDirectContinuationRequest(t, h, directCodexResponsesPath, directContinuationToolOutputBody(model, true, responseID))
	if secondRecorder.Code != http.StatusConflict {
		t.Fatalf("second request status = %d, want %d", secondRecorder.Code, http.StatusConflict)
	}
	assertDirectContinuationErrorCode(t, secondRecorder.Body.Bytes(), "codex_continuation_repair_failed")
	if capture.calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", capture.calls.Load())
	}
}

func TestCodexDirectCompactContinuationPreservesAssistantAndToolEvidence(t *testing.T) {
	resetCodexDirectContinuationsForTest(t)
	hook := logtest.NewGlobal()
	defer hook.Reset()

	model := "gpt-5.4"
	compactResponseID := "resp-compact-evidence"
	compactOutput := `[` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"summary"}]},` +
		`{"type":"function_call","call_id":"call_fn","name":"shell","arguments":"{}"},` +
		`{"type":"custom_tool_call","call_id":"call_custom","name":"apply_patch","input":"*** Begin Patch"}` +
		`]`
	capture := &directContinuationUpstreamCapture{}
	upstream := newDirectContinuationUpstream(t, capture,
		directContinuationUpstreamResponse{
			path: directCodexUpstreamCompactPath,
			body: []byte(`{"id":"` + compactResponseID + `","object":"response","status":"completed","output":` + compactOutput + `}`),
		},
		directContinuationUpstreamResponse{
			path: directCodexUpstreamResponsesPath,
			body: directContinuationSSECompleted("resp-after-compact", `[{"type":"message","role":"assistant","content":[]}]`),
		},
	)
	defer upstream.Close()

	h := newDirectContinuationHandler(t, nil, directContinuationAuthSpec{
		id:       "direct-compact-auth",
		models:   []string{model},
		baseURL:  upstream.URL,
		provider: "codex",
		apiKey:   "test",
	})

	compactRecorder := performDirectContinuationRequest(t, h, directCodexCompactPath, directContinuationCompactBody(model))
	if compactRecorder.Code != http.StatusOK {
		t.Fatalf("compact request status = %d, want %d", compactRecorder.Code, http.StatusOK)
	}

	nextRecorder := performDirectContinuationRequest(t, h, directCodexResponsesPath, directContinuationToolOutputsBody(model, true, compactResponseID))
	if nextRecorder.Code != http.StatusOK {
		t.Fatalf("next request status = %d, want %d", nextRecorder.Code, http.StatusOK)
	}
	if capture.calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", capture.calls.Load())
	}
	assertDirectContinuationUpstreamInputCounts(t, capture.lastBody(), map[string]int{
		"message":                 2,
		"function_call":           1,
		"custom_tool_call":        1,
		"function_call_output":    1,
		"custom_tool_call_output": 1,
	}, 6)
	entry := assertDirectContinuationLogEntry(t, hook, "codex direct http continuation diagnostic", map[string]any{
		"route_kind":                 "responses",
		"compact_request":            false,
		"has_previous_response_id":   true,
		"scope_present":              false,
		"binding_result":             "hit",
		"repair_result":              "repaired",
		"input_item_count":           6,
		"assistant_message_count":    1,
		"function_call_count":        2,
		"function_call_output_count": 2,
		"fail_reason":                "none",
	})
	assertDirectContinuationLogEntryRedacted(t, entry, compactResponseID, "summary", "*** Begin Patch", "ok")
}

func TestCodexDirectHiddenOnlyCompactAugmentsRecentAssistantAndToolEvidence(t *testing.T) {
	resetCodexDirectContinuationsForTest(t)
	hook := logtest.NewGlobal()
	defer hook.Reset()

	model := "gpt-5.4"
	promptCacheKey := "hidden-compact-scope"
	beforeCompactResponseID := "resp-before-hidden-compact"
	compactResponseID := "resp-hidden-compact"
	beforeCompactOutput := `[` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ready"}]},` +
		`{"type":"function_call","call_id":"call_fn","name":"shell","arguments":"{}"},` +
		`{"type":"custom_tool_call","call_id":"call_custom","name":"apply_patch","input":"*** Begin Patch"}` +
		`]`
	hiddenOnlyCompactOutput := `[{"type":"compaction_summary","summary":[{"type":"summary_text","text":"hidden compact state"}]}]`
	capture := &directContinuationUpstreamCapture{}
	upstream := newDirectContinuationUpstream(t, capture,
		directContinuationUpstreamResponse{
			path: directCodexUpstreamResponsesPath,
			body: directContinuationSSECompleted(beforeCompactResponseID, beforeCompactOutput),
		},
		directContinuationUpstreamResponse{
			path: directCodexUpstreamCompactPath,
			body: []byte(`{"id":"` + compactResponseID + `","object":"response","status":"completed","output":` + hiddenOnlyCompactOutput + `}`),
		},
		directContinuationUpstreamResponse{
			path: directCodexUpstreamResponsesPath,
			body: directContinuationSSECompleted("resp-after-hidden-compact", `[{"type":"message","role":"assistant","content":[]}]`),
		},
	)
	defer upstream.Close()

	h := newDirectContinuationHandler(t, nil, directContinuationAuthSpec{
		id:       "direct-hidden-compact-auth",
		models:   []string{model},
		baseURL:  upstream.URL,
		provider: "codex",
		apiKey:   "test",
	})

	firstRecorder := performDirectContinuationRequest(t, h, directCodexResponsesPath, directContinuationWithPromptCacheKey(directContinuationUserMessageBody(model, true), promptCacheKey))
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want %d", firstRecorder.Code, http.StatusOK)
	}
	compactRecorder := performDirectContinuationRequest(t, h, directCodexCompactPath, directContinuationWithPromptCacheKey(directContinuationCompactBody(model), promptCacheKey))
	if compactRecorder.Code != http.StatusOK {
		t.Fatalf("compact request status = %d, want %d", compactRecorder.Code, http.StatusOK)
	}

	nextRecorder := performDirectContinuationRequest(t, h, directCodexResponsesPath, directContinuationWithPromptCacheKey(directContinuationToolOutputsBody(model, true, compactResponseID), promptCacheKey))
	if nextRecorder.Code != http.StatusOK {
		t.Fatalf("next request status = %d, want %d", nextRecorder.Code, http.StatusOK)
	}
	if capture.calls.Load() != 3 {
		t.Fatalf("upstream calls = %d, want 3", capture.calls.Load())
	}
	assertDirectContinuationUpstreamInputCounts(t, capture.lastBody(), map[string]int{
		"message":                 2,
		"function_call":           1,
		"custom_tool_call":        1,
		"function_call_output":    1,
		"custom_tool_call_output": 1,
	}, 6)
	entry := assertDirectContinuationLogEntry(t, hook, "codex direct http compact evidence diagnostic", map[string]any{
		"route_kind":                     "compact",
		"compact_request":                true,
		"has_previous_response_id":       false,
		"scope_present":                  true,
		"binding_result":                 "none",
		"repair_result":                  "none",
		"input_item_count":               1,
		"assistant_message_count":        0,
		"function_call_count":            0,
		"function_call_output_count":     0,
		"compact_output_has_evidence":    false,
		"recent_evidence_hit":            true,
		"compact_evidence_augmented":     true,
		"bound_output_item_count":        3,
		"bound_output_assistant_count":   1,
		"bound_output_tool_call_count":   2,
		"bound_output_tool_output_count": 0,
		"fail_reason":                    "none",
	})
	assertDirectContinuationLogEntryRedacted(t, entry, promptCacheKey, beforeCompactResponseID, compactResponseID, "ready", "*** Begin Patch")
}

func TestCodexDirectPostContinuationRepairsFromStreamOutputItemDoneWhenCompletedOutputEmpty(t *testing.T) {
	resetCodexDirectContinuationsForTest(t)

	model := "gpt-5.4"
	responseID := "resp-stream-output-item-done"
	capture := &directContinuationUpstreamCapture{}
	upstream := newDirectContinuationUpstream(t, capture,
		directContinuationUpstreamResponse{
			path: directCodexUpstreamResponsesPath,
			body: []byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_fn","name":"tool","arguments":"{}"},"output_index":0}` + "\n" +
				`data: {"type":"response.completed","response":{"id":"` + responseID + `","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":[]}}` + "\n\n"),
		},
		directContinuationUpstreamResponse{
			path: directCodexUpstreamResponsesPath,
			body: directContinuationSSECompleted("resp-after-stream-repair", `[{"type":"message","role":"assistant","content":[]}]`),
		},
	)
	defer upstream.Close()

	h := newDirectContinuationHandler(t, nil, directContinuationAuthSpec{
		id:       "direct-stream-repair-auth",
		models:   []string{model},
		baseURL:  upstream.URL,
		provider: "codex",
		apiKey:   "test",
	})

	firstRecorder := performDirectContinuationRequest(t, h, directCodexResponsesPath, directContinuationUserMessageBody(model, true))
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want %d", firstRecorder.Code, http.StatusOK)
	}

	secondRecorder := performDirectContinuationRequest(t, h, directCodexResponsesPath, directContinuationFunctionToolOutputBody(model, true, responseID, "call_fn"))
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("second request status = %d, want %d", secondRecorder.Code, http.StatusOK)
	}
	if capture.calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", capture.calls.Load())
	}
	assertDirectContinuationUpstreamInputCounts(t, capture.lastBody(), map[string]int{
		"message":              1,
		"function_call":        1,
		"function_call_output": 1,
	}, 3)
}

type directContinuationAuthSpec struct {
	id       string
	models   []string
	baseURL  string
	provider string
	apiKey   string
}

func newDirectContinuationHandler(t *testing.T, selector cliproxyauth.Selector, specs ...directContinuationAuthSpec) *OpenAIResponsesAPIHandler {
	t.Helper()

	manager := cliproxyauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(&config.Config{}))
	for _, spec := range specs {
		registerDirectContinuationAuth(t, manager, spec)
	}

	return NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
}

func registerDirectContinuationAuth(t *testing.T, manager *cliproxyauth.Manager, spec directContinuationAuthSpec) {
	t.Helper()

	provider := spec.provider
	if provider == "" {
		provider = "codex"
	}
	apiKey := spec.apiKey
	if apiKey == "" {
		apiKey = "test"
	}
	models := make([]*registry.ModelInfo, 0, len(spec.models))
	for _, model := range spec.models {
		models = append(models, &registry.ModelInfo{
			ID:      model,
			Object:  "model",
			OwnedBy: provider,
			Type:    provider,
		})
	}
	registry.GetGlobalRegistry().RegisterClient(spec.id, provider, models)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(spec.id)
	})

	if _, err := manager.Register(context.Background(), &cliproxyauth.Auth{
		ID:       spec.id,
		Provider: provider,
		Status:   cliproxyauth.StatusActive,
		Index:    spec.id,
		Attributes: map[string]string{
			"api_key":      apiKey,
			"base_url":     spec.baseURL,
			"runtime_only": "true",
		},
	}); err != nil {
		t.Fatalf("register auth %s: %v", spec.id, err)
	}
	manager.RefreshSchedulerEntry(spec.id)
}

type directContinuationSequenceSelector struct {
	firstID string
	laterID string
	calls   atomic.Int32
}

func (s *directContinuationSequenceSelector) Pick(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if len(auths) == 0 {
		return nil, errors.New("no auths")
	}
	call := s.calls.Add(1)
	wanted := s.laterID
	if call == 1 {
		wanted = s.firstID
	}
	for _, auth := range auths {
		if auth != nil && auth.ID == wanted {
			return auth, nil
		}
	}
	return auths[0], nil
}

func performDirectContinuationRequest(t *testing.T, h *OpenAIResponsesAPIHandler, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	switch path {
	case directCodexCompactPath:
		h.Compact(c)
	default:
		h.Responses(c)
	}
	return recorder
}

func directContinuationUserMessageBody(model string, stream bool) []byte {
	return directContinuationUserMessageBodyWithText(model, stream, "hello")
}

func directContinuationUserMessageBodyWithPreviousID(model string, stream bool, previousResponseID string) []byte {
	raw := directContinuationUserMessageBodyWithText(model, stream, "next")
	updated, err := sjson.SetBytes(raw, "previous_response_id", previousResponseID)
	if err != nil {
		panic(err)
	}
	return updated
}

func directContinuationUserMessageBodyWithText(model string, stream bool, text string) []byte {
	raw, err := json.Marshal(map[string]any{
		"model":  model,
		"stream": stream,
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": text},
				},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func directContinuationCompactBody(model string) []byte {
	raw, err := json.Marshal(map[string]any{
		"model": model,
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "compact source"},
				},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func directContinuationToolOutputBody(model string, stream bool, previousResponseID string) []byte {
	return directContinuationFunctionToolOutputBody(model, stream, previousResponseID, "call_1")
}

func directContinuationFunctionToolOutputBody(model string, stream bool, previousResponseID string, callID string) []byte {
	raw, err := json.Marshal(map[string]any{
		"model":                model,
		"stream":               stream,
		"previous_response_id": previousResponseID,
		"input": []any{
			map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  "ok",
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func directContinuationToolOutputsBody(model string, stream bool, previousResponseID string) []byte {
	raw, err := json.Marshal(map[string]any{
		"model":                model,
		"stream":               stream,
		"previous_response_id": previousResponseID,
		"input": []any{
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call_fn",
				"output":  "ok",
			},
			map[string]any{
				"type":    "custom_tool_call_output",
				"call_id": "call_custom",
				"output":  "ok",
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func directContinuationWithPromptCacheKey(raw []byte, promptCacheKey string) []byte {
	updated, err := sjson.SetBytes(raw, "prompt_cache_key", promptCacheKey)
	if err != nil {
		panic(err)
	}
	return updated
}

type directContinuationUpstreamResponse struct {
	path        string
	body        []byte
	contentType string
}

const (
	directCodexUpstreamResponsesPath = "/responses"
	directCodexUpstreamCompactPath   = "/responses/compact"
)

type directContinuationUpstreamCapture struct {
	calls atomic.Int32
	mu    sync.Mutex
	paths []string
	body  []byte
}

func (c *directContinuationUpstreamCapture) record(path string, body []byte) {
	c.calls.Add(1)
	c.mu.Lock()
	c.paths = append(c.paths, path)
	c.body = append([]byte(nil), body...)
	c.mu.Unlock()
}

func (c *directContinuationUpstreamCapture) lastBody() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.body...)
}

func newDirectContinuationUpstream(t *testing.T, capture *directContinuationUpstreamCapture, responses ...directContinuationUpstreamResponse) *httptest.Server {
	t.Helper()

	var index atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		capture.record(r.URL.Path, body)

		i := int(index.Add(1)) - 1
		if i >= len(responses) {
			http.Error(w, "unexpected extra call", http.StatusInternalServerError)
			return
		}
		resp := responses[i]
		if resp.path != "" && r.URL.Path != resp.path {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		contentType := resp.contentType
		if contentType == "" {
			contentType = "text/event-stream"
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(resp.body)
	}))
}

func directContinuationSSECompleted(responseID string, outputJSON string) []byte {
	return []byte(`data: {"type":"response.completed","response":{"id":"` + responseID + `","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":` + outputJSON + `}}` + "\n\n")
}

func assertDirectContinuationUpstreamInputCounts(t *testing.T, body []byte, expected map[string]int, expectedLen int) {
	t.Helper()

	if gjson.GetBytes(body, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id leaked to upstream body")
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		t.Fatalf("upstream input is not an array: %s", input.Raw)
	}
	items := input.Array()
	if len(items) != expectedLen {
		t.Fatalf("upstream input item count = %d, want %d; input=%s", len(items), expectedLen, input.Raw)
	}

	counts := map[string]int{}
	for _, item := range items {
		counts[item.Get("type").String()]++
	}
	for itemType, want := range expected {
		if counts[itemType] != want {
			t.Fatalf("upstream %s item count = %d, want %d; input=%s", itemType, counts[itemType], want, input.Raw)
		}
	}
}

func assertDirectContinuationErrorCode(t *testing.T, body []byte, expected string) {
	t.Helper()

	if got := gjson.GetBytes(body, "error.code").String(); got != expected {
		t.Fatalf("error.code = %q, want %q; body=%s", got, expected, string(body))
	}
}

func assertDirectContinuationLogEntry(t *testing.T, hook *logtest.Hook, message string, expected map[string]any) *log.Entry {
	t.Helper()

	for _, entry := range hook.AllEntries() {
		if entry.Message != message {
			continue
		}
		matched := true
		for key, want := range expected {
			if got := entry.Data[key]; got != want {
				matched = false
				break
			}
		}
		if matched {
			return entry
		}
	}
	t.Fatalf("missing log entry %q with fields %v; got %v", message, expected, directContinuationLogFields(hook.AllEntries()))
	return nil
}

func assertDirectContinuationLogEntryRedacted(t *testing.T, entry *log.Entry, forbidden ...string) {
	t.Helper()

	for key, value := range entry.Data {
		text := fmt.Sprint(value)
		for _, needle := range forbidden {
			if needle != "" && strings.Contains(text, needle) {
				t.Fatalf("log field %q leaked forbidden value %q in %q", key, needle, text)
			}
		}
	}
}

func directContinuationLogFields(entries []*log.Entry) []log.Fields {
	fields := make([]log.Fields, 0, len(entries))
	for _, entry := range entries {
		fields = append(fields, entry.Data)
	}
	return fields
}

func resetCodexDirectContinuationsForTest(t *testing.T) {
	t.Helper()

	reset := func() {
		codexDirectContinuations.mu.Lock()
		codexDirectContinuations.bindings = make(map[string]codexDirectContinuationBinding)
		codexDirectContinuations.recentEvidence = make(map[string]codexDirectRecentEvidence)
		codexDirectContinuations.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}
