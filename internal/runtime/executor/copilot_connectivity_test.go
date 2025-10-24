package executor

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
    cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
    cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
    sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// Minimal connectivity test to ensure requests with model "gpt-5-mini" route via Copilot (CodexExecutor) without unknown-provider errors.
func TestCopilot_GPT5Mini_Connectivity(t *testing.T) {
    // Fake upstream Copilot endpoint returning a minimal non-stream JSON completion
    mux := http.NewServeMux()
    mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write([]byte(`{"id":"cmpl_test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
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

    // Assert returned JSON contains expected assistant content "ok"
    var parsed struct {
        Choices []struct {
            Index   int `json:"index"`
            Message struct {
                Role    string `json:"role"`
                Content string `json:"content"`
            } `json:"message"`
        } `json:"choices"`
    }
    if err := json.Unmarshal(resp.Payload, &parsed); err != nil {
        t.Fatalf("invalid json payload: %v", err)
    }
    if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content != "ok" {
        t.Fatalf("unexpected content, want 'ok', got: %+v", parsed)
    }
}
