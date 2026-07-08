package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/openai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorUsesPrefixedRequestedModelForThinkingLookup(t *testing.T) {
	const provider = "free-provider"
	const requestedModel = "free-provider/gemini-3.1-pro-preview"
	const upstreamModel = "gemini-3.1-pro-preview"

	reg := registry.GetGlobalRegistry()
	clientID := fmt.Sprintf("openai-compat-prefixed-thinking-%d", time.Now().UnixNano())
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{
		{
			ID:      requestedModel,
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: provider,
			Type:    "openai-compatibility",
			Thinking: &registry.ThinkingSupport{
				Levels: []string{"max", "xhigh", "high", "medium", "low", "minimal", "none", "auto"},
			},
		},
	})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor(provider, &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindAPIKey,
		cliproxyauth.AttributeSource:   "config:openai-compatibility[test]",
		"base_url":                     server.URL + "/v1",
		"api_key":                      "test",
	}}
	payload := []byte(`{"model":"free-provider/gemini-3.1-pro-preview","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   upstreamModel,
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
		Metadata: map[string]any{
			cliproxyexecutor.ThinkingLookupModelMetadataKey: requestedModel,
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != upstreamModel {
		t.Fatalf("model = %q, want %q; body=%s", got, upstreamModel, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "reasoning_effort").String(); got != "xhigh" {
		t.Fatalf("reasoning_effort = %q, want xhigh; body=%s", got, string(gotBody))
	}
}
