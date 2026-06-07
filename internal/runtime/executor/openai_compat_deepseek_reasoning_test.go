package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func registerThinkingModelForProvider(t *testing.T, clientID, provider, modelID string, levels []string) {
	t.Helper()

	var support *registry.ThinkingSupport
	if levels != nil {
		support = &registry.ThinkingSupport{Levels: append([]string(nil), levels...)}
	}

	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(clientID)
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{{
		ID:       modelID,
		Thinking: support,
	}})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})
}

func TestOpenAICompatExecutorDeepSeekOfficialAllowsMaxReasoningEffort(t *testing.T) {
	registerThinkingModelForProvider(t, "deepseek-official-openai", "deepseek", "deepseek-v4-pro", []string{"low", "medium", "high", "xhigh", "max"})

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "deepseek-official",
			Kind: "deepseek",
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test",
			"compat_name":  "deepseek-official",
			"compat_kind":  "deepseek",
			"provider_key": "deepseek",
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "deepseek-v4-pro",
		Payload: []byte(`{
			"model":"deepseek-v4-pro",
			"messages":[{"role":"user","content":"hi"}],
			"thinking":{"type":"enabled"},
			"reasoning_effort":"max"
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(gotBody, "reasoning_effort").String(); got != "max" {
		t.Fatalf("reasoning_effort = %q, want max; body=%s", got, string(gotBody))
	}
}

func TestOpenAICompatExecutorDeepSeekOfficialClaudeSourceAllowsMaxEffort(t *testing.T) {
	registerThinkingModelForProvider(t, "deepseek-official-claude", "deepseek", "deepseek-v4-pro", []string{"low", "medium", "high", "xhigh", "max"})

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "deepseek-official",
			Kind: "deepseek",
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/anthropic",
			"api_key":      "test",
			"compat_name":  "deepseek-official",
			"compat_kind":  "deepseek",
			"provider_key": "deepseek",
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "deepseek-v4-pro",
		Payload: []byte(`{
			"model":"deepseek-v4-pro",
			"messages":[{"role":"user","content":"hi"}],
			"thinking":{"type":"adaptive"},
			"output_config":{"effort":"max"}
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(gotBody, "reasoning_effort").String(); got != "max" {
		t.Fatalf("reasoning_effort = %q, want max; body=%s", got, string(gotBody))
	}
}

func TestOpenAICompatExecutorDeepSeekIntentRemapClampsMaxToFinalSupport(t *testing.T) {
	registerThinkingModelForProvider(t, "generic-openai-compat-remap", "openai-compatibility", "generic-openai-model", []string{"low", "medium", "high"})

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"generic-openai-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url": server.URL + "/v1",
			"api_key":  "test",
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "generic-openai-model",
		Payload: []byte(`{
			"model":"generic-openai-model",
			"messages":[{"role":"user","content":"hi"}],
			"reasoning_effort":"max"
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey:          "deepseek-v4-pro[1m]",
			cliproxyexecutor.ReasoningEffortOriginalMetadataKey: "max",
			cliproxyexecutor.ClientProfileMetadataKey:           "claude_code",
		},
	})
	if err == nil {
		if got := gjson.GetBytes(gotBody, "reasoning_effort").String(); got != "high" {
			t.Fatalf("reasoning_effort = %q, want high; body=%s", got, string(gotBody))
		}
		return
	}
	t.Fatalf("Execute error: %v", err)
}

func TestOpenAICompatExecutorDeepSeekIntentStripsReasoningWhenFinalModelHasNoThinkingSupport(t *testing.T) {
	registerThinkingModelForProvider(t, "generic-openai-compat-no-thinking", "openai-compatibility", "plain-openai-model", nil)

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"plain-openai-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url": server.URL + "/v1",
			"api_key":  "test",
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "plain-openai-model",
		Payload: []byte(`{
			"model":"plain-openai-model",
			"messages":[{"role":"user","content":"hi"}],
			"reasoning_effort":"max"
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey:          "deepseek-v4-pro",
			cliproxyexecutor.ReasoningEffortOriginalMetadataKey: "max",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gjson.GetBytes(gotBody, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be stripped; body=%s", string(gotBody))
	}
}

func TestOpenAICompatExecutorGenericProviderDoesNotGloballyAllowMax(t *testing.T) {
	registerThinkingModelForProvider(t, "generic-openai-compat", "openai-compatibility", "generic-openai-model", []string{"low", "medium", "high"})

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url": serverURLForUnsupportedDeepSeekTest(),
			"api_key":  "test",
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "generic-openai-model",
		Payload: []byte(`{
			"model":"generic-openai-model",
			"messages":[{"role":"user","content":"hi"}],
			"reasoning_effort":"max"
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "generic-openai-model",
		},
	})
	if err == nil {
		t.Fatal("expected validation error for unrelated generic openai-compat max effort")
	}
	if !strings.Contains(err.Error(), `level "max" not supported`) {
		t.Fatalf("error = %v, want level not supported", err)
	}
}

func serverURLForUnsupportedDeepSeekTest() string {
	return "https://example.com/v1"
}
