package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func newXAITerminalTestSetup(t *testing.T, handler http.HandlerFunc) (*XAIExecutor, *cliproxyauth.Auth) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	return exec, auth
}

func xaiExecuteNonStreamForTest(t *testing.T, exec *XAIExecutor, auth *cliproxyauth.Auth) (cliproxyexecutor.Response, error) {
	t.Helper()
	return exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: []byte(`{"model":"grok-4.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
}

func TestXAIExecutorExecuteSurfacesTerminalStreamError(t *testing.T) {
	exec, auth := newXAITerminalTestSetup(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: error\n"))
		_, _ = w.Write([]byte(`data: {"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"},"sequence_number":2}` + "\n\n"))
	})

	_, err := xaiExecuteNonStreamForTest(t, exec, auth)
	if err == nil {
		t.Fatal("expected terminal stream error, got nil")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, err)
	}
	if !strings.Contains(err.Error(), "Your input exceeds the context window") {
		t.Fatalf("error message missing upstream error text: %v", err)
	}
	if got := ojson.GetBytes([]byte(err.Error()), "error.code").String(); got != "context_length_exceeded" {
		t.Fatalf("error.code = %q, want context_length_exceeded; err=%v", got, err)
	}
	assertNotRequestScopedTestError(t, err)
}

func TestXAIExecutorExecuteSurfacesResponseFailed(t *testing.T) {
	exec, auth := newXAITerminalTestSetup(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.failed","response":{"id":"resp_1","object":"response","status":"failed","error":{"code":"server_error","message":"upstream failed mid-generation"}}}` + "\n\n"))
	})

	_, err := xaiExecuteNonStreamForTest(t, exec, auth)
	if err == nil {
		t.Fatal("expected response.failed error, got nil")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadGateway, err)
	}
	if !strings.Contains(err.Error(), "upstream failed mid-generation") {
		t.Fatalf("error message missing upstream error text: %v", err)
	}
	assertNotRequestScopedTestError(t, err)
}

func TestXAIExecutorExecuteEmptyStreamReturnsBadGateway(t *testing.T) {
	exec, auth := newXAITerminalTestSetup(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	})

	_, err := xaiExecuteNonStreamForTest(t, exec, auth)
	if err == nil {
		t.Fatal("expected empty stream error, got nil")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadGateway, err)
	}
	if !strings.Contains(err.Error(), "empty stream") {
		t.Fatalf("error message should mention empty stream: %v", err)
	}
	assertRequestScopedTestError(t, err)
}

func TestXAIExecutorExecuteStreamDisconnectedWithoutTerminalIsRequestScoped(t *testing.T) {
	exec, auth := newXAITerminalTestSetup(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"grok-4.5"}}` + "\n\n"))
	})

	_, err := xaiExecuteNonStreamForTest(t, exec, auth)
	if err == nil {
		t.Fatal("expected incomplete stream error, got nil")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusRequestTimeout, err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatalf("error message should not say timeout: %v", err)
	}
	assertRequestScopedTestError(t, err)
}

func TestXAIExecutorExecuteTerminalCompletedStillSucceeds(t *testing.T) {
	exec, auth := newXAITerminalTestSetup(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"grok-4.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	})

	resp, err := xaiExecuteNonStreamForTest(t, exec, auth)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := ojson.GetBytes(resp.Payload, "output.0.content.0.text").String(); got != "ok" {
		t.Fatalf("output text = %q, want ok; payload=%s", got, resp.Payload)
	}
}
