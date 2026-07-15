package openai

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

const openAICompatStreamFixtureModel = "openai-sse-fixture-model"

func newOpenAICompatStreamFixtureHandler(t *testing.T, upstreamURL string) *OpenAIAPIHandler {
	t.Helper()

	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	compatExecutor := runtimeexecutor.NewOpenAICompatExecutor("openai-compatibility", &internalconfig.Config{})
	manager.RegisterExecutor(compatExecutor)

	auth := &coreauth.Auth{
		ID:       "openai-sse-fixture-auth",
		Provider: compatExecutor.Identifier(),
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "fixture-key",
			"base_url": upstreamURL + "/v1",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register fixture auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: openAICompatStreamFixtureModel}})
	manager.RefreshSchedulerEntry(auth.ID)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	return NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
}

func runOpenAICompatStreamFixture(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("upstream path = %q, want /v1/chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	gin.SetMode(gin.TestMode)
	handler := newOpenAICompatStreamFixtureHandler(t, upstream.URL)
	router := gin.New()
	router.POST("/v1/chat/completions", handler.ChatCompletions)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"openai-sse-fixture-model","stream":true,"messages":[{"role":"user","content":"fixture"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func TestOpenAICompatChatStreamPreservesNativeSSEFraming(t *testing.T) {
	terminalWithoutDone := "data: {\"id\":\"chatcmpl-native-no-done\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fixture response\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":60,\"completion_tokens\":9,\"total_tokens\":69,\"prompt_tokens_details\":{\"cached_tokens\":20}}}\n"
	malformedEvent := "event: message\ndata: {\"usage\":\n\ndata: [DONE]\n"
	terminalWithDone := ": fixture keepalive\n\ndata: {\"id\":\"chatcmpl-native-terminal\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fixture response\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":101,\"completion_tokens\":17,\"total_tokens\":125,\"prompt_tokens_details\":{\"cached_tokens\":60},\"completion_tokens_details\":{\"reasoning_tokens\":7}}}\n\ndata: {\"id\":\"chatcmpl-native-terminal\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":999,\"completion_tokens\":999,\"total_tokens\":1998}}\n\ndata: [DONE]\n"
	framingFields := "event: message\nid: fixture-42\nretry: 2500\ndata: {\"id\":\"chatcmpl-framing\",\"object\":\"chat.completion.chunk\",\"choices\":[]}\n\n"

	for _, tc := range []struct {
		name      string
		upstream  string
		doneCount int
	}{
		{name: "clean EOF does not add DONE", upstream: terminalWithoutDone, doneCount: 0},
		{name: "malformed event retains event field", upstream: malformedEvent, doneCount: 1},
		{name: "normal terminal usage and DONE remain unchanged", upstream: terminalWithDone, doneCount: 1},
		{name: "event id and retry fields remain unchanged", upstream: framingFields, doneCount: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := runOpenAICompatStreamFixture(t, tc.upstream)
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%q", resp.Code, http.StatusOK, resp.Body.String())
			}
			if got := resp.Body.String(); got != tc.upstream {
				t.Fatalf("stream body = %q, want %q", got, tc.upstream)
			}
			if got := strings.Count(resp.Body.String(), "data: [DONE]"); got != tc.doneCount {
				t.Fatalf("DONE count = %d, want %d; body=%q", got, tc.doneCount, resp.Body.String())
			}
		})
	}
}

func TestOpenAICompatChatStreamKeepsCommitErrorBehavior(t *testing.T) {
	t.Run("pre-commit HTTP failure remains an HTTP error", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"fixture pre-commit failure"}}`))
		}))
		defer upstream.Close()

		gin.SetMode(gin.TestMode)
		handler := newOpenAICompatStreamFixtureHandler(t, upstream.URL)
		router := gin.New()
		router.POST("/v1/chat/completions", handler.ChatCompletions)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"openai-sse-fixture-model","stream":true,"messages":[{"role":"user","content":"fixture"}]}`))
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d; body=%q", resp.Code, http.StatusServiceUnavailable, resp.Body.String())
		}
		if strings.Contains(resp.Body.String(), "[DONE]") {
			t.Fatalf("pre-commit response synthesized DONE: %q", resp.Body.String())
		}
	})

	t.Run("post-commit stream failure remains in stream without DONE", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-partial\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"))
			_, _ = w.Write([]byte("{\"error\":{\"message\":\"fixture post-commit failure\"}}\n"))
		}))
		defer upstream.Close()

		gin.SetMode(gin.TestMode)
		handler := newOpenAICompatStreamFixtureHandler(t, upstream.URL)
		router := gin.New()
		router.POST("/v1/chat/completions", handler.ChatCompletions)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"openai-sse-fixture-model","stream":true,"messages":[{"role":"user","content":"fixture"}]}`))
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%q", resp.Code, http.StatusOK, resp.Body.String())
		}
		if !strings.Contains(resp.Body.String(), "chatcmpl-partial") || !strings.Contains(resp.Body.String(), "fixture post-commit failure") {
			t.Fatalf("post-commit stream body = %q", resp.Body.String())
		}
		if strings.Contains(resp.Body.String(), "[DONE]") {
			t.Fatalf("post-commit stream synthesized DONE: %q", resp.Body.String())
		}
	})
}

func TestOpenAICompatChatStreamCancellationReachesUpstream(t *testing.T) {
	upstreamCanceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-cancel\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
		close(upstreamCanceled)
	}))
	defer upstream.Close()

	gin.SetMode(gin.TestMode)
	handler := newOpenAICompatStreamFixtureHandler(t, upstream.URL)
	router := gin.New()
	router.POST("/v1/chat/completions", handler.ChatCompletions)
	downstream := httptest.NewServer(router)
	defer downstream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, downstream.URL+"/v1/chat/completions", strings.NewReader(`{"model":"openai-sse-fixture-model","stream":true,"messages":[{"role":"user","content":"fixture"}]}`))
	if errRequest != nil {
		t.Fatalf("new request: %v", errRequest)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, errDo := http.DefaultClient.Do(req)
	if errDo != nil {
		t.Fatalf("stream request: %v", errDo)
	}
	defer resp.Body.Close()

	line, errRead := bufio.NewReader(resp.Body).ReadString('\n')
	if errRead != nil {
		t.Fatalf("read first stream line: %v", errRead)
	}
	if !strings.Contains(line, "chatcmpl-cancel") {
		t.Fatalf("first stream line = %q", line)
	}
	cancel()

	select {
	case <-upstreamCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream did not observe client cancellation")
	}
}
