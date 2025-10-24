package executor

import (
    "net/http"
    "net/http/httptest"
    "testing"

    cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
    cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
    sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
    "context"
)

// Minimal connectivity test to ensure requests with model "gpt-5-mini" route via Copilot (CodexExecutor) without unknown-provider errors.
func TestCopilot_GPT5Mini_Connectivity(t *testing.T) {
    // Fake upstream Codex endpoint returning a single SSE completion event
    mux := http.NewServeMux()
    mux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/event-stream")
        _, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"output_text\":\"ok\"}}\n\n"))
    })
    fake := httptest.NewServer(mux)
    defer fake.Close()

    // Copilot auth with access token and custom base_url to hit our fake upstream
    auth := &cliproxyauth.Auth{
        ID:       "copilot:test-connect",
        Provider: "copilot",
        Attributes: map[string]string{
            "base_url": fake.URL,
        },
        Metadata: map[string]any{
            "access_token": "atk",
        },
        Status: cliproxyauth.StatusActive,
    }

    // Build an OpenAI-compatible chat request targeting gpt-5-mini
    payload := []byte(`{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`)
    req := cliproxyexecutor.Request{Model: "gpt-5-mini", Payload: payload}
    opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: payload}

    exec := NewCodexExecutorWithID(&config.Config{}, "copilot")
    resp, err := exec.Execute(context.Background(), auth, req, opts)
    if err != nil {
        t.Fatalf("execute error: %v", err)
    }
    if len(resp.Payload) == 0 {
        t.Fatalf("empty payload")
    }
}

