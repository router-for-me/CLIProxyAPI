package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexWebsocketsExecuteStreamUnlocksSessionAfterHandshakeFallback(t *testing.T) {
	var websocketRequests atomic.Int32
	var httpRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			websocketRequests.Add(1)
			http.Error(w, "websocket unavailable", http.StatusUpgradeRequired)
			return
		}

		responseID := httpRequests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-http-%d\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n", responseID)
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{ID: "test-auth", Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"type":"response.create","model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "sess-handshake-fallback",
		},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	for turn := 1; turn <= 2; turn++ {
		resultCh := make(chan *cliproxyexecutor.StreamResult, 1)
		errCh := make(chan error, 1)
		go func() {
			result, err := exec.ExecuteStream(ctx, auth, req, opts)
			resultCh <- result
			errCh <- err
		}()

		var result *cliproxyexecutor.StreamResult
		select {
		case result = <-resultCh:
		case <-time.After(time.Second):
			t.Fatalf("turn %d blocked after websocket handshake fallback", turn)
		}
		if err := <-errCh; err != nil {
			t.Fatalf("turn %d ExecuteStream() error = %v", turn, err)
		}
		assertCodexWebsocketCompletedChunk(t, result, fmt.Sprintf("resp-http-%d", turn))
	}

	if got := websocketRequests.Load(); got != 2 {
		t.Fatalf("websocket request count = %d, want 2", got)
	}
	if got := httpRequests.Load(); got != 2 {
		t.Fatalf("HTTP fallback request count = %d, want 2", got)
	}
}

func TestCodexWebsocketsExecuteStreamUnlocksSessionAfterHandshakeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{ID: "test-auth", Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"model":"gpt-5-codex","input":[]}`)}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "sess-handshake-error",
		},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	for turn := 1; turn <= 2; turn++ {
		errCh := make(chan error, 1)
		go func() {
			_, err := exec.ExecuteStream(ctx, auth, req, opts)
			errCh <- err
		}()
		select {
		case err := <-errCh:
			if err == nil {
				t.Fatalf("turn %d ExecuteStream() error = nil", turn)
			}
		case <-time.After(time.Second):
			t.Fatalf("turn %d blocked after websocket handshake error", turn)
		}
	}
}
