package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// TestClaudeExecutor_SoonestResetRoutesByObservedUpstreamHeaders exercises the path the
// server uses: the real ClaudeExecutor sends HTTP to an upstream whose responses carry
// anthropic-ratelimit-unified-* headers, the Manager observes them into each credential's
// quota snapshot, and soonest-reset routes by them. It covers API-key and OAuth
// credentials and both the non-streaming and streaming paths.
func TestClaudeExecutor_SoonestResetRoutesByObservedUpstreamHeaders(t *testing.T) {
	for _, kind := range []string{"api-key", "oauth"} {
		for _, stream := range []bool{false, true} {
			name := kind + "/execute"
			if stream {
				name = kind + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				testClaudeExecutorSoonestReset(t, kind, stream)
			})
		}
	}
}

func testClaudeExecutorSoonestReset(t *testing.T, kind string, stream bool) {
	now := time.Now()
	// Weekly resets by token: B resets first, then C, then A.
	resets := map[string]time.Time{
		"token-a": now.Add(72 * time.Hour),
		"token-b": now.Add(24 * time.Hour),
		"token-c": now.Add(48 * time.Hour),
	}
	var mu sync.Mutex
	var calls []string
	rejected := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		token := r.Header.Get("X-Api-Key")
		if token == "" {
			token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		mu.Lock()
		calls = append(calls, token)
		reset, known := resets[token]
		isRejected := rejected[token]
		mu.Unlock()
		if !known {
			http.Error(w, "unknown token", http.StatusUnauthorized)
			return
		}
		status := "allowed"
		if isRejected {
			status = "rejected"
		}
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", status)
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.9")
		w.Header().Set("Anthropic-Ratelimit-Unified-Overage-Status", "rejected")
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-soonest-reset\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
			_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg","type":"message","role":"assistant","model":"claude-soonest-reset","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()

	const model = "claude-soonest-reset"
	manager := cliproxyauth.NewManager(nil, &cliproxyauth.SoonestResetSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(NewClaudeExecutor(&config.Config{}))
	idPrefix := uuid.NewString() + "-"
	tokenByID := map[string]string{}
	for _, suffix := range []string{"a", "b", "c"} {
		auth := &cliproxyauth.Auth{
			ID:         idPrefix + suffix,
			Provider:   "claude",
			Status:     cliproxyauth.StatusActive,
			Attributes: map[string]string{"base_url": server.URL},
		}
		if kind == "api-key" {
			auth.Attributes["api_key"] = "token-" + suffix
		} else {
			auth.Attributes[cliproxyauth.AttributeAuthKind] = cliproxyauth.AuthKindOAuth
			auth.Metadata = map[string]any{
				"type":         "claude",
				"access_token": "token-" + suffix,
				"expired":      now.Add(48 * time.Hour).Format(time.RFC3339),
			}
		}
		tokenByID[auth.ID] = "token-" + suffix
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}

	send := func() {
		t.Helper()
		req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"` + model + `","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)}
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}
		if !stream {
			if _, errExecute := manager.Execute(context.Background(), []string{"claude"}, req, opts); errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}
			return
		}
		result, errStream := manager.ExecuteStream(context.Background(), []string{"claude"}, req, opts)
		if errStream != nil {
			t.Fatalf("ExecuteStream() error = %v", errStream)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error = %v", chunk.Err)
			}
		}
	}
	callsSince := func(start int) []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls[start:]...)
	}

	// Three probes in ID order, then the earliest weekly reset.
	for i := 0; i < 6; i++ {
		send()
	}
	if got, want := strings.Join(callsSince(0), " "), "token-a token-b token-c token-b token-b token-b"; got != want {
		t.Fatalf("upstream calls = %q, want %q", got, want)
	}
	for id, token := range tokenByID {
		observed, ok := manager.GetByID(id)
		if !ok || observed == nil || observed.Quota.ObservedAt.IsZero() {
			t.Fatalf("auth %s (%s) has no observed quota snapshot", id, token)
		}
		if got := observed.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Reset"]; got != strconv.FormatInt(resets[token].Unix(), 10) {
			t.Fatalf("auth %s (%s) 7d reset signal = %q, want %d", id, token, got, resets[token].Unix())
		}
	}

	// B's weekly window is used up: its next answer says so, then traffic moves to C.
	mu.Lock()
	rejected["token-b"] = true
	mu.Unlock()
	start := len(callsSince(0))
	for i := 0; i < 4; i++ {
		send()
	}
	if got, want := strings.Join(callsSince(start), " "), "token-b token-c token-c token-c"; got != want {
		t.Fatalf("upstream calls after b reports rejected = %q, want %q", got, want)
	}
}
