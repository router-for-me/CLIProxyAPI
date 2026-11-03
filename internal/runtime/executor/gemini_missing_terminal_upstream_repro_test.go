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

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// TestGeminiMissingTerminalUpstreamRepro is the regression test for issue #7005:
// an HTTP 200 Gemini stream that ends by clean EOF without finishReason must stay
// a failure, but it must not cool the credential/model pair or replay the already
// submitted generation on another credential. A genuine HTTP 502 keeps existing
// failover and cooldown behavior.
func TestGeminiMissingTerminalUpstreamRepro(t *testing.T) {
	const model = "gemini-3.1-pro-preview"
	const partial = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"partial answer"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"totalTokenCount":13}}` + "\n\n"
	const complete = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"complete answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"totalTokenCount":13}}` + "\n\n"
	cases := []struct {
		name          string
		status        int
		body          string
		wantError     bool
		wantCalls     int32
		wantCooled    int
		wantErrorText string
	}{
		{name: "normal_terminal", status: 200, body: complete, wantCalls: 1},
		{name: "partial_then_missing_terminal", status: 200, body: partial, wantError: true, wantCalls: 1, wantErrorText: "before finishReason"},
		{name: "empty_200_missing_terminal", status: 200, wantError: true, wantCalls: 1, wantErrorText: "before finishReason"},
		{name: "real_http_502_control", status: 502, body: `{"error":{"code":502,"message":"mock upstream rejection","status":"UNAVAILABLE"}}`, wantError: true, wantCalls: 2, wantCooled: 2, wantErrorText: "mock upstream rejection"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
					t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(tc.status)
				if _, errWrite := fmt.Fprint(w, tc.body); errWrite != nil {
					t.Errorf("write mock response: %v", errWrite)
				}
			}))
			defer server.Close()

			cfg := &config.Config{}
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(NewGeminiExecutor(cfg))
			for i := 0; i < 2; i++ {
				id := fmt.Sprintf("missing-terminal-%s-%d", tc.name, i)
				registry.GetGlobalRegistry().RegisterClient(id, "gemini", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				_, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
					ID: id, Provider: "gemini", Status: cliproxyauth.StatusActive,
					Attributes: map[string]string{"api_key": fmt.Sprintf("synthetic-key-%d", i), "base_url": server.URL},
				})
				if errRegister != nil {
					t.Fatal(errRegister)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, errStream := manager.ExecuteStream(ctx, []string{"gemini"}, cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(`{"model":"gemini-3.1-pro-preview","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Say hello"}]}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: true})
			var payloadChunks int
			var observedError error = errStream
			if result != nil {
				for chunk := range result.Chunks {
					if len(chunk.Payload) != 0 {
						payloadChunks++
					}
					if chunk.Err != nil {
						observedError = chunk.Err
					}
				}
			}
			cooled := 0
			for _, auth := range manager.List() {
				if state := auth.ModelStates[model]; state != nil && state.Unavailable && state.NextRetryAfter.After(time.Now()) {
					cooled++
					t.Logf("auth=%s cooldown=%s last_error=%+v", auth.ID, time.Until(state.NextRetryAfter).Round(time.Second), state.LastError)
				}
			}
			t.Logf("upstream_status=%d generation_POSTs=%d payload_chunks=%d cooled_auth_models=%d error=%v", tc.status, calls.Load(), payloadChunks, cooled, observedError)
			if (observedError != nil) != tc.wantError {
				t.Errorf("error=%v, wantError=%t", observedError, tc.wantError)
			}
			if tc.wantErrorText != "" && (observedError == nil || !strings.Contains(observedError.Error(), tc.wantErrorText)) {
				t.Errorf("error=%v, want text %q", observedError, tc.wantErrorText)
			}
			if calls.Load() != tc.wantCalls {
				t.Errorf("generation POSTs=%d, want %d", calls.Load(), tc.wantCalls)
			}
			if cooled != tc.wantCooled {
				t.Errorf("cooled auth/model pairs=%d, want %d", cooled, tc.wantCooled)
			}
		})
	}
}
