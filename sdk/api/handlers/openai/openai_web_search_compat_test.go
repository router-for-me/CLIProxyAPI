package openai

import (
	"bytes"
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

type chatPassthroughCaptureExecutor struct {
	payload []byte
}

func (e *chatPassthroughCaptureExecutor) Identifier() string { return "chat-passthrough-provider" }

func (e *chatPassthroughCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.payload = bytes.Clone(req.Payload)
	return coreexecutor.Response{Payload: []byte(`{"id":"chatcmpl_test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)}, nil
}

func (e *chatPassthroughCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *chatPassthroughCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *chatPassthroughCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *chatPassthroughCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestChatCompletionsPreservesWebSearchOptionsForProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &chatPassthroughCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "chat-passthrough-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "passthrough-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/chat/completions", handler.ChatCompletions)
	requestBody := `{"model":"passthrough-model","messages":[{"role":"user","content":"Search"}],"web_search_options":{"search_context_size":"high","user_location":{"type":"approximate","approximate":{"city":"Shanghai"}}}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if !bytes.Equal(executor.payload, []byte(requestBody)) {
		t.Fatalf("provider payload changed:\n got: %s\nwant: %s", string(executor.payload), requestBody)
	}
	if !gjson.GetBytes(executor.payload, "web_search_options").IsObject() {
		t.Fatalf("web_search_options missing: %s", string(executor.payload))
	}
	if gjson.GetBytes(executor.payload, "tools").Exists() || gjson.GetBytes(executor.payload, "tool_choice").Exists() {
		t.Fatalf("xAI-only search fields leaked into passthrough payload: %s", string(executor.payload))
	}
}

func TestAddChatWebSearchAnnotations(t *testing.T) {
	request := []byte(`{"web_search_options":{"search_context_size":"medium"}}`)
	response := []byte(`{"choices":[{"message":{"role":"assistant","content":"你好 [[1]](https://example.com/source) and [Second](https://example.org/two)"}}]}`)

	out := addChatWebSearchAnnotations(request, response)
	annotations := gjson.GetBytes(out, "choices.0.message.annotations").Array()
	if len(annotations) != 2 {
		t.Fatalf("annotations length = %d, want 2: %s", len(annotations), string(out))
	}
	first := annotations[0].Get("url_citation")
	if got := first.Get("url").String(); got != "https://example.com/source" {
		t.Fatalf("first URL = %q: %s", got, string(out))
	}
	if got := first.Get("title").String(); got != "1" {
		t.Fatalf("first title = %q, want 1: %s", got, string(out))
	}
	if got := first.Get("start_index").Int(); got != 3 {
		t.Fatalf("first start_index = %d, want 3: %s", got, string(out))
	}
	wantEnd := int64(3 + len([]rune(`[[1]](https://example.com/source)`)))
	if got := first.Get("end_index").Int(); got != wantEnd {
		t.Fatalf("first end_index = %d, want %d: %s", got, wantEnd, string(out))
	}
	if got := annotations[1].Get("url_citation.title").String(); got != "Second" {
		t.Fatalf("second title = %q, want Second: %s", got, string(out))
	}
}

func TestAddChatWebSearchAnnotationsLeavesNonSearchAndExistingAnnotationsAlone(t *testing.T) {
	response := []byte(`{"choices":[{"message":{"content":"[Link](https://example.com)"}}]}`)
	if out := addChatWebSearchAnnotations([]byte(`{"messages":[]}`), response); !bytes.Equal(out, response) {
		t.Fatalf("non-search response changed: %s", string(out))
	}

	withAnnotations := []byte(`{"choices":[{"message":{"content":"[Link](https://example.com)","annotations":[]}}]}`)
	if out := addChatWebSearchAnnotations([]byte(`{"tools":[{"type":"web_search"}]}`), withAnnotations); !bytes.Equal(out, withAnnotations) {
		t.Fatalf("existing annotations changed: %s", string(out))
	}
}
