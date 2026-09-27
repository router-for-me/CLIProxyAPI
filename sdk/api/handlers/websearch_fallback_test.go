package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/websearch"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

type websearchStubDoer struct {
	handle func(*http.Request) (*http.Response, error)
}

func (s websearchStubDoer) Do(req *http.Request) (*http.Response, error) {
	return s.handle(req)
}

func websearchStubSearchConfig() websearch.Config {
	cfg := websearch.Config{Enabled: true, Exclude: []string{"brave", "tavily", "exa", "searxng"}}
	cfg = cfg.WithDefaults()
	return cfg.WithDoer(websearchStubDoer{handle: func(*http.Request) (*http.Response, error) {
		page := `<html><body><a class="result__a" href="https://example.com/a">Title A</a>` +
			`<a class="result__snippet" href="https://example.com/a">snippet a</a></body></html>`
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(page))}, nil
	}})
}

type websearchFakeExecutor struct {
	provider string

	mu       sync.Mutex
	calls    int
	payloads [][]byte
	execute  func(call int, req coreexecutor.Request) ([]byte, error)
}

func (e *websearchFakeExecutor) Identifier() string { return e.provider }

func (e *websearchFakeExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	e.payloads = append(e.payloads, cloneBytes(req.Payload))
	execute := e.execute
	e.mu.Unlock()
	if execute == nil {
		return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "no execute func"}
	}
	body, err := execute(call, req)
	if err != nil {
		return coreexecutor.Response{}, err
	}
	return coreexecutor.Response{Payload: body, Headers: http.Header{}}, nil
}

func (e *websearchFakeExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "stream not implemented"}
}

func (e *websearchFakeExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websearchFakeExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{Payload: []byte("0")}, nil
}

func (e *websearchFakeExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "HttpRequest not implemented"}
}

func newWebsearchHandler(t *testing.T, model, provider string, executor *websearchFakeExecutor, cfg *sdkconfig.SDKConfig) *BaseAPIHandler {
	t.Helper()
	executor.provider = provider
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       "websearch-" + provider + "-" + model,
		Provider: provider,
		Status:   coreauth.StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(): %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	return NewBaseAPIHandlers(cfg, manager)
}

const websearchClaudeRequest = `{
	"model":"%s",
	"tools":[{"type":"web_search_20250305","name":"web_search"}],
	"messages":[{"role":"user","content":"search go"}]
}`

func TestWebsearchFallbackNonStreamLoop(t *testing.T) {
	model := "ws-fallback-model"
	executor := &websearchFakeExecutor{execute: func(call int, req coreexecutor.Request) ([]byte, error) {
		if call == 1 {
			if got := gjson.GetBytes(req.Payload, "tools.0.type").String(); got != "" {
				t.Errorf("first payload keeps server type: %s", req.Payload)
			}
			if got := gjson.GetBytes(req.Payload, "tools.0.input_schema.required.0").String(); got != "query" {
				t.Errorf("first payload not rewritten: %s", req.Payload)
			}
			return []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"` + model + `",
				"content":[{"type":"tool_use","id":"toolu_1","name":"web_search","input":{"query":"golang"}}],
				"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`), nil
		}
		if got := gjson.GetBytes(req.Payload, "messages.2.content.0.type").String(); got != "tool_result" {
			t.Errorf("second payload missing tool result: %s", req.Payload)
		}
		return []byte(`{"id":"msg_2","type":"message","role":"assistant","model":"` + model + `",
			"content":[{"type":"text","text":"Go is great."}],
			"stop_reason":"end_turn","usage":{"input_tokens":50,"output_tokens":8}}`), nil
	}}
	cfg := &sdkconfig.SDKConfig{}
	cfg.WebSearch = websearchStubSearchConfig()
	handler := newWebsearchHandler(t, model, "kimi", executor, cfg)

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "claude", model, []byte(sprintfModel(websearchClaudeRequest, model)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager error = %#v", errMsg)
	}
	if got := gjson.GetBytes(body, "content.0.text").String(); got != "Go is great." {
		t.Fatalf("body = %s", body)
	}
	if executor.calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", executor.calls)
	}
}

func TestWebsearchFallbackStreamSynthesizesSSE(t *testing.T) {
	model := "ws-fallback-stream-model"
	executor := &websearchFakeExecutor{execute: func(call int, _ coreexecutor.Request) ([]byte, error) {
		if call == 1 {
			return []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"` + model + `",
				"content":[{"type":"tool_use","id":"toolu_1","name":"web_search","input":{"query":"golang"}}],
				"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`), nil
		}
		return []byte(`{"id":"msg_2","type":"message","role":"assistant","model":"` + model + `",
			"content":[{"type":"text","text":"Streamed answer."}],
			"stop_reason":"end_turn","usage":{"input_tokens":50,"output_tokens":8}}`), nil
	}}
	cfg := &sdkconfig.SDKConfig{}
	cfg.WebSearch = websearchStubSearchConfig()
	handler := newWebsearchHandler(t, model, "kimi", executor, cfg)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "claude", model, []byte(sprintfModel(websearchClaudeRequest, model)), "")
	var chunks []string
	for chunk := range dataChan {
		chunks = append(chunks, string(chunk))
	}
	for errMsg := range errChan {
		t.Fatalf("stream error = %#v", errMsg)
	}
	joined := strings.Join(chunks, "\n")
	for _, want := range []string{"event: message_start", "Streamed answer.", "event: message_stop"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
	if executor.calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", executor.calls)
	}
}

func TestWebsearchFallbackSkippedForNativeRoutes(t *testing.T) {
	model := "ws-native-model"
	executor := &websearchFakeExecutor{execute: func(_ int, req coreexecutor.Request) ([]byte, error) {
		if got := gjson.GetBytes(req.Payload, "tools.0.type").String(); got != "web_search_20250305" {
			t.Errorf("native payload rewritten: %s", req.Payload)
		}
		return []byte(`{"id":"msg_1","content":[{"type":"text","text":"native"}],"stop_reason":"end_turn",
			"usage":{"input_tokens":1,"output_tokens":1}}`), nil
	}}
	cfg := &sdkconfig.SDKConfig{}
	cfg.WebSearch = websearchStubSearchConfig()
	handler := newWebsearchHandler(t, model, "codex", executor, cfg)

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "claude", model, []byte(sprintfModel(websearchClaudeRequest, model)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager error = %#v", errMsg)
	}
	if got := gjson.GetBytes(body, "content.0.text").String(); got != "native" {
		t.Fatalf("body = %s", body)
	}
	if executor.calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", executor.calls)
	}
}

func TestWebsearchFallbackDisabledByDefault(t *testing.T) {
	model := "ws-disabled-model"
	executor := &websearchFakeExecutor{execute: func(_ int, req coreexecutor.Request) ([]byte, error) {
		if got := gjson.GetBytes(req.Payload, "tools.0.type").String(); got != "web_search_20250305" {
			t.Errorf("disabled fallback rewrote payload: %s", req.Payload)
		}
		return []byte(`{"id":"msg_1","content":[{"type":"text","text":"passthrough"}],"stop_reason":"end_turn",
			"usage":{"input_tokens":1,"output_tokens":1}}`), nil
	}}
	handler := newWebsearchHandler(t, model, "kimi", executor, &sdkconfig.SDKConfig{})

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "claude", model, []byte(sprintfModel(websearchClaudeRequest, model)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager error = %#v", errMsg)
	}
	if got := gjson.GetBytes(body, "content.0.text").String(); got != "passthrough" {
		t.Fatalf("body = %s", body)
	}
}

func sprintfModel(format, model string) string {
	return strings.Replace(format, "%s", model, 1)
}
