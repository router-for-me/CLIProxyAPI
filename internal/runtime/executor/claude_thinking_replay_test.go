package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const claudeReplayResolvedModelInfoKey = "cliproxy.resolved_api_key_model_info"

func claudeReplayTestAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "claude-replay-auth",
		Provider: "claude",
		Attributes: map[string]string{
			cliproxyauth.AttributeAPIKey:   "key-claude-replay",
			cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindAPIKey,
			"base_url":                     baseURL,
		},
	}
}

func claudeReplayTestRequest(payload []byte, sessionID string, isCompat bool, source sdktranslator.Format) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	return cliproxyexecutor.Request{
			Model:   "claude-synthetic-4772",
			Payload: payload,
			Metadata: map[string]any{
				claudeReplayResolvedModelInfoKey: &registry.ModelInfo{IsCompat: isCompat},
			},
		}, cliproxyexecutor.Options{
			SourceFormat: source,
			Metadata: map[string]any{
				cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
			},
		}
}

func TestClaudeThinkingReplayEnabledRequiresCompatClaudeAPIKey(t *testing.T) {
	baseRequest, baseOptions := claudeReplayTestRequest([]byte(`{"messages":[]}`), "scope", true, sdktranslator.FormatClaude)
	baseAuth := claudeReplayTestAuth("http://127.0.0.1")

	tests := []struct {
		name       string
		auth       *cliproxyauth.Auth
		request    cliproxyexecutor.Request
		options    cliproxyexecutor.Options
		wantEnable bool
	}{
		{
			name:       "compat Claude API key",
			auth:       baseAuth,
			request:    baseRequest,
			options:    baseOptions,
			wantEnable: true,
		},
		{
			name: "non compat model",
			auth: baseAuth,
			request: func() cliproxyexecutor.Request {
				request, _ := claudeReplayTestRequest([]byte(`{"messages":[]}`), "scope-non-compat", false, sdktranslator.FormatClaude)
				return request
			}(),
			options:    baseOptions,
			wantEnable: false,
		},
		{
			name: "OAuth credential",
			auth: func() *cliproxyauth.Auth {
				auth := baseAuth.Clone()
				auth.Attributes[cliproxyauth.AttributeAuthKind] = cliproxyauth.AuthKindOAuth
				auth.Attributes[cliproxyauth.AttributeAPIKey] = "sk-ant-oat-replay"
				return auth
			}(),
			request:    baseRequest,
			options:    baseOptions,
			wantEnable: false,
		},
		{
			name: "other provider",
			auth: func() *cliproxyauth.Auth {
				auth := baseAuth.Clone()
				auth.Provider = "kimi"
				return auth
			}(),
			request:    baseRequest,
			options:    baseOptions,
			wantEnable: false,
		},
		{
			name:    "OpenAI source format",
			auth:    baseAuth,
			request: baseRequest,
			options: func() cliproxyexecutor.Options {
				options := baseOptions
				options.SourceFormat = sdktranslator.FormatOpenAI
				return options
			}(),
			wantEnable: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := claudeThinkingReplayEnabled(test.auth, test.request, test.options); got != test.wantEnable {
				t.Fatalf("claudeThinkingReplayEnabled() = %v, want %v", got, test.wantEnable)
			}
		})
	}
}

func TestClaudeExecutorCompatThinkingReplayRestoresOmittedBlock(t *testing.T) {
	internalcacheClearClaudeThinkingReplay(t)

	var mu sync.Mutex
	var requestBodies [][]byte
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		mu.Lock()
		requestBodies = append(requestBodies, bytes.Clone(body))
		callCount++
		call := callCount
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_, _ = w.Write([]byte(`{"id":"msg-1","type":"message","role":"assistant","model":"claude-synthetic-4772","content":[{"type":"thinking","thinking":"provider reasoning","signature":"EgI="},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}],"stop_reason":"tool_use"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg-2","type":"message","role":"assistant","model":"claude-synthetic-4772","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(nil)
	auth := claudeReplayTestAuth(server.URL)
	firstPayload := []byte(`{"messages":[{"role":"user","content":"inspect"}]}`)
	firstRequest, firstOptions := claudeReplayTestRequest(firstPayload, "nonstream-replay", true, sdktranslator.FormatClaude)
	if _, errExecute := executor.Execute(context.Background(), auth, firstRequest, firstOptions); errExecute != nil {
		t.Fatalf("first Execute() error = %v", errExecute)
	}

	secondPayload := []byte(`{"messages":[{"role":"user","content":"inspect"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]}`)
	secondRequest, secondOptions := claudeReplayTestRequest(secondPayload, "nonstream-replay", true, sdktranslator.FormatClaude)
	if _, errExecute := executor.Execute(context.Background(), auth, secondRequest, secondOptions); errExecute != nil {
		t.Fatalf("second Execute() error = %v", errExecute)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requestBodies) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(requestBodies))
	}
	content := gjson.GetBytes(requestBodies[1], "messages.1.content").Array()
	if len(content) != 2 {
		t.Fatalf("second assistant content = %s, want thinking and tool_use", gjson.GetBytes(requestBodies[1], "messages.1.content").Raw)
	}
	if got := content[0].Get("type").String(); got != "thinking" {
		t.Fatalf("restored first content type = %q, want thinking", got)
	}
	if got := content[0].Get("signature").String(); got != "EgI=" {
		t.Fatalf("restored signature = %q, want EgI=", got)
	}
}

func TestClaudeExecutorCompatThinkingReplayRestoresOmittedBlockInStream(t *testing.T) {
	internalcacheClearClaudeThinkingReplay(t)

	var mu sync.Mutex
	var requestBodies [][]byte
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		mu.Lock()
		requestBodies = append(requestBodies, bytes.Clone(body))
		callCount++
		call := callCount
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if call == 1 {
			_, _ = w.Write([]byte(claudeReplayThinkingStream()))
			return
		}
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-2\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(nil)
	auth := claudeReplayTestAuth(server.URL)
	firstPayload := []byte(`{"messages":[{"role":"user","content":"inspect"}]}`)
	firstRequest, firstOptions := claudeReplayTestRequest(firstPayload, "stream-replay", true, sdktranslator.FormatClaude)
	firstResult, errExecute := executor.ExecuteStream(context.Background(), auth, firstRequest, firstOptions)
	if errExecute != nil {
		t.Fatalf("first ExecuteStream() error = %v", errExecute)
	}
	for chunk := range firstResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("first stream error: %v", chunk.Err)
		}
	}

	secondPayload := []byte(`{"messages":[{"role":"user","content":"inspect"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]}`)
	secondRequest, secondOptions := claudeReplayTestRequest(secondPayload, "stream-replay", true, sdktranslator.FormatClaude)
	secondResult, errExecute := executor.ExecuteStream(context.Background(), auth, secondRequest, secondOptions)
	if errExecute != nil {
		t.Fatalf("second ExecuteStream() error = %v", errExecute)
	}
	for chunk := range secondResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("second stream error: %v", chunk.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requestBodies) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(requestBodies))
	}
	content := gjson.GetBytes(requestBodies[1], "messages.1.content").Array()
	if len(content) != 2 || content[0].Get("type").String() != "thinking" {
		t.Fatalf("second streamed assistant content = %s, want restored thinking and tool_use", gjson.GetBytes(requestBodies[1], "messages.1.content").Raw)
	}
	if got := content[0].Get("signature").String(); got != "EgI=" {
		t.Fatalf("restored streamed signature = %q, want EgI=", got)
	}
}

func claudeReplayThinkingStream() string {
	return "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"provider reasoning\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"EgI=\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"Read\",\"input\":{}}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"README.md\\\"}\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
}

func TestClaudeExecutorCompatThinkingReplayClearsAfterUpstreamBadRequest(t *testing.T) {
	internalcacheClearClaudeThinkingReplay(t)

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg-1","type":"message","role":"assistant","model":"claude-synthetic-4772","content":[{"type":"thinking","thinking":"reasoning","signature":"EgI="},{"type":"tool_use","id":"toolu-1","name":"Read","input":{"path":"README.md"}}],"stop_reason":"tool_use"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"invalid thinking signature"}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(nil)
	auth := claudeReplayTestAuth(server.URL)
	firstRequest, firstOptions := claudeReplayTestRequest([]byte(`{"messages":[{"role":"user","content":"inspect"}]}`), "bad-request-replay", true, sdktranslator.FormatClaude)
	if _, errExecute := executor.Execute(context.Background(), auth, firstRequest, firstOptions); errExecute != nil {
		t.Fatalf("first Execute() error = %v", errExecute)
	}

	secondPayload := []byte(`{"messages":[{"role":"user","content":"inspect"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu-1","name":"Read","input":{"path":"README.md"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu-1","content":"ok"}]}]}`)
	secondRequest, secondOptions := claudeReplayTestRequest(secondPayload, "bad-request-replay", true, sdktranslator.FormatClaude)
	if _, errExecute := executor.Execute(context.Background(), auth, secondRequest, secondOptions); errExecute == nil {
		t.Fatal("second Execute() error = nil, want upstream bad request")
	}

	scope := claudeThinkingReplayScopeFromRequest(context.Background(), auth, firstRequest, firstOptions)
	_, found, errGet := internalcache.GetClaudeThinkingReplayRequired(context.Background(), scope.modelFamily, scope.sessionKey)
	if errGet != nil || found {
		t.Fatalf("replay after upstream bad request = found %v, error %v; want cleared state", found, errGet)
	}
}

func TestClaudeExecutorCompatThinkingReplayRestoresMultipleOmittedBlocks(t *testing.T) {
	internalcacheClearClaudeThinkingReplay(t)

	var mu sync.Mutex
	var requestBodies [][]byte
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		mu.Lock()
		requestBodies = append(requestBodies, bytes.Clone(body))
		callCount++
		call := callCount
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch call {
		case 1:
			_, _ = w.Write([]byte(`{"id":"msg-1","type":"message","role":"assistant","model":"claude-synthetic-4772","content":[{"type":"thinking","thinking":"first","signature":"EgI="},{"type":"tool_use","id":"toolu-1","name":"Read","input":{"path":"one"}}],"stop_reason":"tool_use"}`))
		case 2:
			_, _ = w.Write([]byte(`{"id":"msg-2","type":"message","role":"assistant","model":"claude-synthetic-4772","content":[{"type":"thinking","thinking":"second","signature":"EgM="},{"type":"tool_use","id":"toolu-2","name":"Read","input":{"path":"two"}}],"stop_reason":"tool_use"}`))
		default:
			_, _ = w.Write([]byte(`{"id":"msg-3","type":"message","role":"assistant","model":"claude-synthetic-4772","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`))
		}
	}))
	defer server.Close()

	executor := NewClaudeExecutor(nil)
	auth := claudeReplayTestAuth(server.URL)
	firstRequest, firstOptions := claudeReplayTestRequest([]byte(`{"messages":[{"role":"user","content":"inspect"}]}`), "multi-turn-replay", true, sdktranslator.FormatClaude)
	if _, errExecute := executor.Execute(context.Background(), auth, firstRequest, firstOptions); errExecute != nil {
		t.Fatalf("first Execute() error = %v", errExecute)
	}

	secondPayload := []byte(`{"messages":[{"role":"user","content":"inspect"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu-1","name":"Read","input":{"path":"one"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu-1","content":"one result"}]}]}`)
	secondRequest, secondOptions := claudeReplayTestRequest(secondPayload, "multi-turn-replay", true, sdktranslator.FormatClaude)
	if _, errExecute := executor.Execute(context.Background(), auth, secondRequest, secondOptions); errExecute != nil {
		t.Fatalf("second Execute() error = %v", errExecute)
	}

	thirdPayload := []byte(`{"messages":[{"role":"user","content":"inspect"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu-1","name":"Read","input":{"path":"one"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu-1","content":"one result"}]},{"role":"assistant","content":[{"type":"tool_use","id":"toolu-2","name":"Read","input":{"path":"two"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu-2","content":"two result"}]}]}`)
	thirdRequest, thirdOptions := claudeReplayTestRequest(thirdPayload, "multi-turn-replay", true, sdktranslator.FormatClaude)
	if _, errExecute := executor.Execute(context.Background(), auth, thirdRequest, thirdOptions); errExecute != nil {
		t.Fatalf("third Execute() error = %v", errExecute)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requestBodies) != 3 {
		t.Fatalf("upstream request count = %d, want 3", len(requestBodies))
	}
	firstContent := gjson.GetBytes(requestBodies[2], "messages.1.content").Array()
	secondContent := gjson.GetBytes(requestBodies[2], "messages.3.content").Array()
	if len(firstContent) != 2 || firstContent[0].Get("type").String() != "thinking" || firstContent[0].Get("signature").String() != "EgI=" {
		t.Fatalf("first omitted turn was not restored: %s", gjson.GetBytes(requestBodies[2], "messages.1.content").Raw)
	}
	if len(secondContent) != 2 || secondContent[0].Get("type").String() != "thinking" || secondContent[0].Get("signature").String() != "EgM=" {
		t.Fatalf("second omitted turn was not restored: %s", gjson.GetBytes(requestBodies[2], "messages.3.content").Raw)
	}
}

func internalcacheClearClaudeThinkingReplay(t *testing.T) {
	t.Helper()
	internalcache.ClearClaudeThinkingReplayCache()
	t.Cleanup(internalcache.ClearClaudeThinkingReplayCache)
}
