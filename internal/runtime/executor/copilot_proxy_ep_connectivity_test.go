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

// End-to-end: Attributes lacks base_url; access_token carries proxy-ep with scheme+host.
// Executor must derive base from token and hit /chat/completions on that host.
func TestCopilot_DerivedBaseFromToken_Connectivity(t *testing.T) {
    // Fake upstream Copilot endpoint
    mux := http.NewServeMux()
    mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write([]byte(`{"id":"cmpl_test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
    })
    fake := httptest.NewServer(mux)
    defer fake.Close()

    auth := &cliproxyauth.Auth{
        ID:       "copilot:test-derived",
        Provider: "copilot",
        // No attributes.base_url on purpose
        Metadata: map[string]any{
            // Carry proxy-ep inside token so executor can derive base
            "access_token": "tid=abc;proxy-ep=" + fake.URL + ";exp=1",
        },
        Status: cliproxyauth.StatusActive,
    }

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
    var parsed struct {
        Choices []struct {
            Message struct{ Content string `json:"content"` }
        } `json:"choices"`
    }
    if err := json.Unmarshal(resp.Payload, &parsed); err != nil {
        t.Fatalf("invalid json payload: %v", err)
    }
    if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content != "ok" {
        t.Fatalf("unexpected content, want 'ok', got: %+v", parsed)
    }
}

