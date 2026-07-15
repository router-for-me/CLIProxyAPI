package claude

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	responsesBridgeClientModel   = "claude-fable-5-dd-los-6.5-tpg"
	responsesBridgeUpstreamModel = "gpt-5.6-sol"
)

type responsesBridgeCaptureExecutor struct {
	mu           sync.Mutex
	request      coreexecutor.Request
	options      coreexecutor.Options
	executeCalls int
	streamCalls  int
}

func (e *responsesBridgeCaptureExecutor) Identifier() string { return constant.Codex }

func (e *responsesBridgeCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.capture(req, opts)
	e.mu.Lock()
	e.executeCalls++
	e.mu.Unlock()
	if opts.Alt == constant.ClaudeResponsesCompactBridgeAlt {
		return coreexecutor.Response{
			Payload: []byte(`{"id":"resp_compact_1","object":"response.compaction","output":[{"id":"msg_1","type":"message","status":"completed","role":"user","content":[{"type":"input_text","text":"hello"}]},{"id":"cmp_1","type":"compaction_summary","encrypted_content":"encrypted-state"}],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}`),
			Headers: http.Header{"X-Upstream": []string{"compact"}},
		}, nil
	}
	return coreexecutor.Response{
		Payload: []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"gpt-5.6-sol","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`),
		Headers: http.Header{"X-Upstream": []string{"responses"}},
	}, nil
}

func (e *responsesBridgeCaptureExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.capture(req, opts)
	e.mu.Lock()
	e.streamCalls++
	e.mu.Unlock()
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"gpt-5.6-sol\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Headers: http.Header{"X-Upstream": []string{"responses"}}, Chunks: chunks}, nil
}

func (e *responsesBridgeCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *responsesBridgeCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *responsesBridgeCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *responsesBridgeCaptureExecutor) capture(req coreexecutor.Request, opts coreexecutor.Options) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.request = req
	e.options = opts
}

func (e *responsesBridgeCaptureExecutor) captured() (coreexecutor.Request, coreexecutor.Options) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.request, e.options
}

func (e *responsesBridgeCaptureExecutor) calls() (int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.executeCalls, e.streamCalls
}

func newResponsesBridgeHandler(t *testing.T) (*ClaudeCodeAPIHandler, *responsesBridgeCaptureExecutor) {
	t.Helper()
	executor := &responsesBridgeCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	oauth := &coreauth.Auth{
		ID:       "responses-bridge-auth",
		Provider: constant.Codex,
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"access_token": "oauth-token", "account_id": "account-id"},
	}
	if _, errRegister := manager.Register(context.Background(), oauth); errRegister != nil {
		t.Fatalf("register OAuth auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(oauth.ID, oauth.Provider, []*registry.ModelInfo{{ID: responsesBridgeUpstreamModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(oauth.ID)
	})
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, manager)
	return NewClaudeCodeAPIHandler(base), executor
}

func TestShouldUseClaudeResponsesBridge(t *testing.T) {
	tests := []struct {
		name          string
		clientModel   string
		upstreamModel string
		wantResponses bool
	}{
		{name: "encoded GPT model", clientModel: responsesBridgeClientModel, upstreamModel: responsesBridgeUpstreamModel, wantResponses: true},
		{name: "plain GPT model", clientModel: responsesBridgeUpstreamModel, upstreamModel: responsesBridgeUpstreamModel, wantResponses: false},
		{name: "native Claude model", clientModel: "claude-sonnet-4-6", upstreamModel: "claude-sonnet-4-6", wantResponses: false},
		{name: "encoded non-GPT model", clientModel: "claude-fable-5-dd-orp-5.2-inimeg", upstreamModel: "gemini-2.5-pro", wantResponses: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldUseClaudeResponsesBridge(tt.clientModel, tt.upstreamModel); got != tt.wantResponses {
				t.Fatalf("shouldUseClaudeResponsesBridge(%q, %q) = %v, want %v", tt.clientModel, tt.upstreamModel, got, tt.wantResponses)
			}
		})
	}
}

func TestIsClaudeCompactRequest(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "full conversation with custom instruction",
			body: `{"messages":[{"role":"user","content":"hello"},{"role":"user","content":"Your task is to create a detailed summary of the conversation so far. Do not use tools. Preserve the API details."}]}`,
			want: true,
		},
		{
			name: "recent portion array content",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"Your task is to create a detailed summary of the recent portion of the conversation."}]}]}`,
			want: true,
		},
		{
			name: "ordinary summary request",
			body: `{"messages":[{"role":"user","content":"Please create a detailed summary of this conversation."}]}`,
			want: false,
		},
		{
			name: "compact text not final",
			body: `{"messages":[{"role":"user","content":"Your task is to create a detailed summary of the conversation so far."},{"role":"user","content":"continue"}]}`,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isClaudeCompactRequest([]byte(tt.body)); got != tt.want {
				t.Fatalf("isClaudeCompactRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClaudeMessagesResponsesBridgeNonStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, executor := newResponsesBridgeHandler(t)
	router := gin.New()
	router.POST("/v1/messages", handler.ClaudeMessages)

	body := `{"model":"claude-fable-5-dd-los-6.5-tpg","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages?source=localhost", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := gjson.Get(recorder.Body.String(), "model").String(); got != responsesBridgeClientModel {
		t.Fatalf("response model = %q, want %q; body=%s", got, responsesBridgeClientModel, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Upstream"); got != "responses" {
		t.Fatalf("X-Upstream = %q, want responses", got)
	}

	gotReq, gotOpts := executor.captured()
	if gotReq.Model != responsesBridgeUpstreamModel {
		t.Fatalf("executor model = %q, want %q", gotReq.Model, responsesBridgeUpstreamModel)
	}
	if got := gjson.GetBytes(gotReq.Payload, "model").String(); got != responsesBridgeUpstreamModel {
		t.Fatalf("payload model = %q, want %q; payload=%s", got, responsesBridgeUpstreamModel, gotReq.Payload)
	}
	if gotOpts.Alt != constant.ClaudeResponsesBridgeAlt {
		t.Fatalf("Alt = %q, want %q", gotOpts.Alt, constant.ClaudeResponsesBridgeAlt)
	}
	if gotOpts.SourceFormat != sdktranslator.FormatClaude || gotOpts.ResponseFormat != sdktranslator.FormatClaude {
		t.Fatalf("formats = %q -> %q, want claude -> claude", gotOpts.SourceFormat, gotOpts.ResponseFormat)
	}
	if got := gotOpts.Query.Get("source"); got != "localhost" {
		t.Fatalf("query source = %q, want localhost", got)
	}
	if _, pinned := gotOpts.Metadata[coreexecutor.PinnedAuthMetadataKey]; pinned {
		t.Fatalf("bridge unexpectedly pinned an auth: %#v", gotOpts.Metadata)
	}
}

func TestClaudeMessagesResponsesBridgeStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, executor := newResponsesBridgeHandler(t)
	router := gin.New()
	router.POST("/v1/messages", handler.ClaudeMessages)

	body := `{"model":"claude-fable-5-dd-los-6.5-tpg","stream":true,"max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"model":"`+responsesBridgeClientModel+`"`) {
		t.Fatalf("stream did not restore client model; body=%s", recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	_, gotOpts := executor.captured()
	if !gotOpts.Stream {
		t.Fatal("executor stream option = false, want true")
	}
	if _, pinned := gotOpts.Metadata[coreexecutor.PinnedAuthMetadataKey]; pinned {
		t.Fatalf("stream bridge unexpectedly pinned an auth: %#v", gotOpts.Metadata)
	}
}

func TestClaudeMessagesResponsesBridgeCompactNonStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, executor := newResponsesBridgeHandler(t)
	router := gin.New()
	router.POST("/v1/messages", handler.ClaudeMessages)

	body := `{"model":"claude-fable-5-dd-los-6.5-tpg","max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"Your task is to create a detailed summary of the conversation so far. Do not use tools. Preserve the API details."}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	marker := gjson.Get(recorder.Body.String(), "content.0.text").String()
	_, capsule, found, errStrip := stripClaudeCompactionCapsule(marker)
	if errStrip != nil || !found {
		t.Fatalf("decode compact marker: found=%v err=%v marker=%q", found, errStrip, marker)
	}
	if capsule.Model != responsesBridgeUpstreamModel {
		t.Fatalf("capsule model = %q, want %q", capsule.Model, responsesBridgeUpstreamModel)
	}
	if got := gjson.Get(recorder.Body.String(), "model").String(); got != responsesBridgeClientModel {
		t.Fatalf("response model = %q, want %q", got, responsesBridgeClientModel)
	}

	gotReq, gotOpts := executor.captured()
	if gotOpts.Alt != constant.ClaudeResponsesCompactBridgeAlt {
		t.Fatalf("Alt = %q, want %q", gotOpts.Alt, constant.ClaudeResponsesCompactBridgeAlt)
	}
	if gotOpts.ResponseFormat != sdktranslator.FormatOpenAIResponse {
		t.Fatalf("ResponseFormat = %q, want %q", gotOpts.ResponseFormat, sdktranslator.FormatOpenAIResponse)
	}
	if got := gjson.GetBytes(gotReq.Payload, "messages.2.content").String(); !strings.Contains(got, "Preserve the API details") {
		t.Fatalf("custom compact instruction was not preserved; payload=%s", gotReq.Payload)
	}
}

func TestClaudeMessagesResponsesBridgeCompactStreamingUsesBufferedCompactEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, executor := newResponsesBridgeHandler(t)
	router := gin.New()
	router.POST("/v1/messages", handler.ClaudeMessages)

	body := `{"model":"claude-fable-5-dd-los-6.5-tpg","stream":true,"max_tokens":128,"messages":[{"role":"user","content":"Your task is to create a detailed summary of the conversation so far."}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "event: message_start") || !strings.Contains(recorder.Body.String(), "cpa-responses-compaction") {
		t.Fatalf("unexpected compact SSE: %s", recorder.Body.String())
	}
	executeCalls, streamCalls := executor.calls()
	if executeCalls != 1 || streamCalls != 0 {
		t.Fatalf("executor calls = execute:%d stream:%d, want 1/0", executeCalls, streamCalls)
	}
}

func TestClaudeMessagesResponsesBridgeRehydratesCompactCapsule(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, executor := newResponsesBridgeHandler(t)
	router := gin.New()
	router.POST("/v1/messages", handler.ClaudeMessages)

	compactBody := `{"model":"claude-fable-5-dd-los-6.5-tpg","max_tokens":128,"messages":[{"role":"user","content":"Your task is to create a detailed summary of the conversation so far."}]}`
	compactReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(compactBody))
	compactReq.Header.Set("Content-Type", "application/json")
	compactRecorder := httptest.NewRecorder()
	router.ServeHTTP(compactRecorder, compactReq)
	marker := gjson.Get(compactRecorder.Body.String(), "content.0.text").String()
	if marker == "" {
		t.Fatalf("compact response has no marker: %s", compactRecorder.Body.String())
	}

	followupBody := `{"model":"claude-fable-5-dd-los-6.5-tpg","max_tokens":128,"messages":[{"role":"assistant","content":` + string(mustJSONMarshalForTest(t, marker)) + `},{"role":"user","content":"continue"}]}`
	followupReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(followupBody))
	followupReq.Header.Set("Content-Type", "application/json")
	followupRecorder := httptest.NewRecorder()
	router.ServeHTTP(followupRecorder, followupReq)

	if followupRecorder.Code != http.StatusOK {
		t.Fatalf("follow-up status = %d; body=%s", followupRecorder.Code, followupRecorder.Body.String())
	}
	gotReq, gotOpts := executor.captured()
	if gotOpts.Alt != constant.ClaudeResponsesBridgeAlt {
		t.Fatalf("follow-up Alt = %q, want %q", gotOpts.Alt, constant.ClaudeResponsesBridgeAlt)
	}
	if _, pinned := gotOpts.Metadata[coreexecutor.PinnedAuthMetadataKey]; pinned {
		t.Fatalf("compaction replay unexpectedly pinned an auth: %#v", gotOpts.Metadata)
	}
	if got := gjson.GetBytes(gotReq.Payload, constant.ClaudeResponsesCompactionField+".output.1.type").String(); got != "compaction_summary" {
		t.Fatalf("replay compaction item type = %q; payload=%s", got, gotReq.Payload)
	}
	if got := gjson.GetBytes(gotReq.Payload, "messages.0.content").String(); got != "continue" {
		t.Fatalf("capsule message was not removed; payload=%s", gotReq.Payload)
	}
}

func mustJSONMarshalForTest(t *testing.T, value any) []byte {
	t.Helper()
	encoded, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatalf("marshal test value: %v", errMarshal)
	}
	return encoded
}

func TestRewriteClaudeBridgeResponseModelLeavesOtherEventsAlone(t *testing.T) {
	input := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
	if got := rewriteClaudeBridgeResponseModel(input, responsesBridgeClientModel); string(got) != string(input) {
		t.Fatalf("non-message_start event changed:\ngot=%s\nwant=%s", got, input)
	}
}
