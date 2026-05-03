package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestOpenAICompatExecutor_AliasResolvedThinkingLookup guards against a regression where
// ApplyThinking was called with the conductor-rewritten upstream model name instead of
// the client-requested alias. That caused registry lookups to miss user-defined levels
// registered under the alias, which made "xhigh" budget values be passed through
// unchanged to upstreams that only accept low/medium/high (e.g. vLLM).
//
// The scenario exercised here is a Claude client routed to an OpenAI-compatible upstream
// (cross provider family), which is where the original 400 response was observed.
func TestOpenAICompatExecutor_AliasResolvedThinkingLookup(t *testing.T) {
	// Register the model under the client-visible alias with restricted levels.
	// This mirrors how OpenAI-compatibility entries populate the registry.
	const providerKey = "openai-compatibility-alias-test"
	const alias = "deepseek-v3.1-terminus-slow"
	const upstream = "deepseek-ai/deepseek-v3.1-terminus"

	reg := registry.GetGlobalRegistry()
	clientID := "test-openai-compat-alias-thinking-client"
	reg.RegisterClient(clientID, providerKey, []*registry.ModelInfo{{
		ID:      alias,
		Type:    "openai-compatibility",
		OwnedBy: providerKey,
		Object:  "model",
		Created: time.Now().Unix(),
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	}})
	defer reg.UnregisterClient(clientID)

	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"chat.completion","usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	exec := NewOpenAICompatExecutor(providerKey, &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}

	// Claude client payload with an out-of-range thinking budget. The Claude -> OpenAI
	// translator converts budget_tokens > 24576 into reasoning_effort="xhigh".
	payload := []byte(`{"model":"` + upstream + `","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":32000}}`)
	req := cliproxyexecutor.Request{
		Model:   upstream, // conductor-rewritten upstream name
		Payload: payload,
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       false,
		Metadata: map[string]any{
			// Handlers always populate this with the client's original model name.
			cliproxyexecutor.RequestedModelMetadataKey: alias,
		},
	}

	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	effort := gjson.GetBytes(capturedBody, "reasoning_effort").String()
	if effort != "high" {
		t.Fatalf("reasoning_effort forwarded upstream = %q, want %q (xhigh should clamp to high via alias lookup); body=%s", effort, "high", string(capturedBody))
	}
}
