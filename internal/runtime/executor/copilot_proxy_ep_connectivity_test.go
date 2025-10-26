package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// End-to-end: Without explicit base_url, executor should fall back to default host.
func TestCopilot_DefaultBaseURL_Connectivity(t *testing.T) {
	var (
		mu        sync.Mutex
		requested bool
	)
	// Fake upstream Copilot endpoint
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("expected flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emitCopilotSSE(w, flusher, "ok")
		mu.Lock()
		requested = true
		mu.Unlock()
	})
	fake := httptest.NewServer(mux)
	defer fake.Close()

	originalDefault := copilotDefaultBaseURL
	copilotDefaultBaseURL = fake.URL
	t.Cleanup(func() { copilotDefaultBaseURL = originalDefault })

	auth := &cliproxyauth.Auth{
		ID:       "copilot:test-default",
		Provider: "copilot",
		Metadata: map[string]any{
			"access_token": "tid=abc;exp=1",
		},
		Status: cliproxyauth.StatusActive,
	}

	payload := []byte(`{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`)
	req := cliproxyexecutor.Request{Model: "gpt-5-mini", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: payload}

	exec := NewCopilotExecutor(&config.Config{})
	resp, err := exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("empty payload")
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			}
		} `json:"choices"`
	}
	if err := json.Unmarshal(resp.Payload, &parsed); err != nil {
		t.Fatalf("invalid json payload: %v", err)
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content != "ok" {
		t.Fatalf("unexpected content, want 'ok', got: %+v", parsed)
	}
	mu.Lock()
	if !requested {
		mu.Unlock()
		t.Fatalf("upstream was not invoked")
	}
	mu.Unlock()
}
