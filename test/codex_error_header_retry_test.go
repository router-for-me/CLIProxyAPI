package test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Exercise the actual HTTP executor and retry scheduler, not a synthetic
// RetryAfter-bearing error: losing the upstream header must fail this contract.
func TestCodexHTTPRetryAfterExceedingWaitCapPreventsRedispatch(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			testCodexHTTPRetryAfterWaitCap(t, stream)
		})
	}
}

func testCodexHTTPRetryAfterWaitCap(t *testing.T, stream bool) {
	var mu sync.Mutex
	var attempts []time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts = append(attempts, time.Now())
		mu.Unlock()
		w.Header().Set("Retry-After", "38")
		w.Header().Set("X-Request-Id", "retry-header-reproduction")
		w.WriteHeader(http.StatusTooManyRequests)
		if _, errWrite := fmt.Fprint(w, `{"detail":"Rate limit exceeded"}`); errWrite != nil {
			t.Errorf("write upstream error: %v", errWrite)
		}
	}))
	t.Cleanup(server.Close)

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(3, 30*time.Second, 0)
	manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(&config.Config{}))
	id := "codex-retry-header-" + uuid.NewString()
	const model = "gpt-5.4"
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
		ID: id, Provider: "codex", Status: cliproxyauth.StatusActive,
		Attributes: map[string]string{"base_url": server.URL, "api_key": "synthetic-test-key"},
		Metadata:   map[string]any{"disable_cooling": false},
	}); errRegister != nil {
		t.Fatal(errRegister)
	}
	start := time.Now()
	request := cliproxyexecutor.Request{
		Model: model, Payload: []byte(`{"model":"gpt-5.4","input":"synthetic retry reproduction"}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: stream}
	var errExecute error
	if stream {
		_, errExecute = manager.ExecuteStream(context.Background(), []string{"codex"}, request, opts)
	} else {
		_, errExecute = manager.Execute(context.Background(), []string{"codex"}, request, opts)
	}
	if errExecute == nil {
		t.Fatal("expected upstream 429")
	}
	mu.Lock()
	observed := append([]time.Time(nil), attempts...)
	mu.Unlock()
	delays := make([]time.Duration, len(observed))
	for i, at := range observed {
		delays[i] = at.Sub(start).Round(time.Millisecond)
	}
	var timed interface{ RetryAfter() *time.Duration }
	var hint *time.Duration
	if errors.As(errExecute, &timed) {
		hint = timed.RetryAfter()
	}
	t.Logf("upstream attempts=%d elapsed=%v retry_after=%v", len(observed), delays, hint)
	if len(observed) != 1 {
		t.Errorf("upstream attempts=%d, want 1: 38-second hint exceeds the 30-second wait cap", len(observed))
	}
	if hint == nil || *hint != 38*time.Second {
		t.Errorf("retry hint=%v, want 38 seconds from real upstream headers", hint)
	}
	var status interface{ StatusCode() int }
	if !errors.As(errExecute, &status) || status.StatusCode() != http.StatusTooManyRequests {
		t.Errorf("upstream status lost: %T %v", errExecute, errExecute)
	}
}
