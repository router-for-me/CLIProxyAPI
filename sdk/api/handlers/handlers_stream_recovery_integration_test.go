package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func newCodexRecoveryIntegrationHandler(t *testing.T, upstreamURL string, recovery sdkconfig.StreamingRecoveryConfig) *BaseAPIHandler {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(&internalconfig.Config{}))
	auth := &coreauth.Auth{ID: "codex-recovery-auth", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": upstreamURL, "api_key": "test"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-test"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)
	return NewBaseAPIHandlers(&sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{Recovery: recovery}}, manager)
}

func TestExecuteStreamWithAuthManagerCodexTerminalServerErrorRecovers(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempt := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if attempt == 1 {
			_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"losing-response\",\"model\":\"gpt-test\"}}\n\n"))
			_, _ = w.Write([]byte("data: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"message\":\"temporary\"}}\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"winning-response\",\"model\":\"gpt-test\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"recovered\",\"item_id\":\"item_1\",\"output_index\":0,\"content_index\":0}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"winning-response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	handler := newCodexRecoveryIntegrationHandler(t, server.URL, sdkconfig.StreamingRecoveryConfig{Attempts: 1, MaxBufferBytes: 1 << 20, MaxRetryWindowSeconds: 5, MaxConcurrent: 1, InitialBackoffMilliseconds: 1, MaxBackoffMilliseconds: 1})
	data, _, errs := handler.ExecuteStreamWithAuthManager(context.Background(), "claude", "gpt-test", []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"stream":true}`), "")
	var output strings.Builder
	for chunk := range data {
		output.Write(chunk)
	}
	for streamErr := range errs {
		if streamErr != nil {
			t.Fatalf("stream error: %+v", streamErr)
		}
	}
	got := output.String()
	if requests.Load() != 2 {
		t.Fatalf("upstream requests = %d, want 2", requests.Load())
	}
	if strings.Contains(got, "losing-response") {
		t.Fatalf("losing attempt leaked: %s", got)
	}
	if strings.Count(got, `"type":"message_start"`) != 1 {
		t.Fatalf("message_start count = %d; output=%s", strings.Count(got, `"type":"message_start"`), got)
	}
	if !strings.Contains(got, "recovered") || !strings.Contains(got, `"type":"message_stop"`) {
		t.Fatalf("winning stream incomplete: %s", got)
	}
}

func TestExecuteStreamWithAuthManagerRecoveryCancellationPreventsSecondRequest(t *testing.T) {
	var requests atomic.Int32
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"cancel-response\",\"model\":\"gpt-test\"}}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	handler := newCodexRecoveryIntegrationHandler(t, server.URL, sdkconfig.StreamingRecoveryConfig{Attempts: 1, MaxBufferBytes: 1 << 20, MaxRetryWindowSeconds: 5, MaxConcurrent: 1, InitialBackoffMilliseconds: 1, MaxBackoffMilliseconds: 1})
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() {
		data, _, errs := handler.ExecuteStreamWithAuthManager(ctx, "claude", "gpt-test", []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}],"stream":true}`), "")
		if data != nil {
			for range data {
			}
		}
		if errs != nil {
			for range errs {
			}
		}
		close(returned)
	}()
	<-started
	cancel()
	<-returned
	if requests.Load() != 1 {
		t.Fatalf("upstream requests = %d, want 1", requests.Load())
	}
}
