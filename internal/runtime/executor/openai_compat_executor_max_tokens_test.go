package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutor_MaxTokensNormalization(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hello"}}]}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "test-compat",
			Models: []config.OpenAICompatibilityModel{
				{
					Name:                   "upstream-new",
					Alias:                  "alias-new",
					UseMaxCompletionTokens: true,
				},
				{
					Name:                   "upstream-legacy",
					Alias:                  "alias-legacy",
					UseMaxCompletionTokens: false,
				},
			},
		}},
	}

	executor := NewOpenAICompatExecutor("openai-compatibility", cfg)
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test-key",
			"compat_name":  "test-compat",
			"provider_key": "test-compat",
		},
	}

	t.Run("Execute responses request with use-max-completion-tokens=true sets max_completion_tokens", func(t *testing.T) {
		gotBody = nil
		payload := []byte(`{"model":"alias-new","input":[{"role":"user","content":"hi"}],"max_output_tokens":1024}`)
		_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "alias-new",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Stream:       false,
		})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if got := gjson.GetBytes(gotBody, "max_completion_tokens").Int(); got != 1024 {
			t.Fatalf("max_completion_tokens = %d, want 1024; body=%s", got, string(gotBody))
		}
		if gjson.GetBytes(gotBody, "max_tokens").Exists() {
			t.Fatalf("max_tokens should be absent; body=%s", string(gotBody))
		}
	})

	t.Run("Execute chat request with max_tokens and use-max-completion-tokens=true converts to max_completion_tokens", func(t *testing.T) {
		gotBody = nil
		payload := []byte(`{"model":"alias-new","messages":[{"role":"user","content":"hi"}],"max_tokens":512}`)
		_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "alias-new",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai"),
			Stream:       false,
		})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if got := gjson.GetBytes(gotBody, "max_completion_tokens").Int(); got != 512 {
			t.Fatalf("max_completion_tokens = %d, want 512; body=%s", got, string(gotBody))
		}
		if gjson.GetBytes(gotBody, "max_tokens").Exists() {
			t.Fatalf("max_tokens should be absent; body=%s", string(gotBody))
		}
	})

	t.Run("Execute responses request with use-max-completion-tokens=false sets max_tokens", func(t *testing.T) {
		gotBody = nil
		payload := []byte(`{"model":"alias-legacy","input":[{"role":"user","content":"hi"}],"max_output_tokens":1024}`)
		_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "alias-legacy",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Stream:       false,
		})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if got := gjson.GetBytes(gotBody, "max_tokens").Int(); got != 1024 {
			t.Fatalf("max_tokens = %d, want 1024; body=%s", got, string(gotBody))
		}
		if gjson.GetBytes(gotBody, "max_completion_tokens").Exists() {
			t.Fatalf("max_completion_tokens should be absent; body=%s", string(gotBody))
		}
	})

	t.Run("Execute chat request with max_completion_tokens and use-max-completion-tokens=false converts to max_tokens", func(t *testing.T) {
		gotBody = nil
		payload := []byte(`{"model":"alias-legacy","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":512}`)
		_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "alias-legacy",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai"),
			Stream:       false,
		})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if got := gjson.GetBytes(gotBody, "max_tokens").Int(); got != 512 {
			t.Fatalf("max_tokens = %d, want 512; body=%s", got, string(gotBody))
		}
		if gjson.GetBytes(gotBody, "max_completion_tokens").Exists() {
			t.Fatalf("max_completion_tokens should be absent; body=%s", string(gotBody))
		}
	})

	t.Run("Execute responses request with max_output_tokens=null and use-max-completion-tokens=true preserves null", func(t *testing.T) {
		gotBody = nil
		payload := []byte(`{"model":"alias-new","input":[{"role":"user","content":"hi"}],"max_output_tokens":null}`)
		_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "alias-new",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Stream:       false,
		})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if got := gjson.GetBytes(gotBody, "max_completion_tokens"); !got.Exists() || got.Type != gjson.Null {
			t.Fatalf("max_completion_tokens = %v, want null; body=%s", got, string(gotBody))
		}
		if gjson.GetBytes(gotBody, "max_tokens").Exists() {
			t.Fatalf("max_tokens should be absent; body=%s", string(gotBody))
		}
	})

	t.Run("Execute responses request with max_output_tokens=null and use-max-completion-tokens=false preserves null", func(t *testing.T) {
		gotBody = nil
		payload := []byte(`{"model":"alias-legacy","input":[{"role":"user","content":"hi"}],"max_output_tokens":null}`)
		_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "alias-legacy",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Stream:       false,
		})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if got := gjson.GetBytes(gotBody, "max_tokens"); !got.Exists() || got.Type != gjson.Null {
			t.Fatalf("max_tokens = %v, want null; body=%s", got, string(gotBody))
		}
		if gjson.GetBytes(gotBody, "max_completion_tokens").Exists() {
			t.Fatalf("max_completion_tokens should be absent; body=%s", string(gotBody))
		}
	})

	t.Run("Execute chat request with max_tokens=null and use-max-completion-tokens=true preserves null", func(t *testing.T) {
		gotBody = nil
		payload := []byte(`{"model":"alias-new","messages":[{"role":"user","content":"hi"}],"max_tokens":null}`)
		_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "alias-new",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai"),
			Stream:       false,
		})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if got := gjson.GetBytes(gotBody, "max_completion_tokens"); !got.Exists() || got.Type != gjson.Null {
			t.Fatalf("max_completion_tokens = %v, want null; body=%s", got, string(gotBody))
		}
		if gjson.GetBytes(gotBody, "max_tokens").Exists() {
			t.Fatalf("max_tokens should be absent; body=%s", string(gotBody))
		}
	})

	t.Run("Execute chat request with max_completion_tokens=null and use-max-completion-tokens=false preserves null", func(t *testing.T) {
		gotBody = nil
		payload := []byte(`{"model":"alias-legacy","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":null}`)
		_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "alias-legacy",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai"),
			Stream:       false,
		})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}

		if got := gjson.GetBytes(gotBody, "max_tokens"); !got.Exists() || got.Type != gjson.Null {
			t.Fatalf("max_tokens = %v, want null; body=%s", got, string(gotBody))
		}
		if gjson.GetBytes(gotBody, "max_completion_tokens").Exists() {
			t.Fatalf("max_completion_tokens should be absent; body=%s", string(gotBody))
		}
	})
}

func TestOpenAICompatExecutor_MaxTokensNormalizationStream(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "test-compat",
			Models: []config.OpenAICompatibilityModel{
				{
					Name:                   "upstream-new",
					Alias:                  "alias-new",
					UseMaxCompletionTokens: true,
				},
				{
					Name:                   "upstream-legacy",
					Alias:                  "alias-legacy",
					UseMaxCompletionTokens: false,
				},
			},
		}},
	}

	executor := NewOpenAICompatExecutor("openai-compatibility", cfg)
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test-key",
			"compat_name":  "test-compat",
			"provider_key": "test-compat",
		},
	}

	t.Run("ExecuteStream with use-max-completion-tokens=true emits max_completion_tokens", func(t *testing.T) {
		gotBody = nil
		payload := []byte(`{"model":"alias-new","messages":[{"role":"user","content":"hi"}],"max_tokens":256}`)
		res, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "alias-new",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai"),
			Stream:       true,
		})
		if err != nil {
			t.Fatalf("ExecuteStream error: %v", err)
		}
		for range res.Chunks {
		}

		if got := gjson.GetBytes(gotBody, "max_completion_tokens").Int(); got != 256 {
			t.Fatalf("max_completion_tokens = %d, want 256; body=%s", got, string(gotBody))
		}
		if gjson.GetBytes(gotBody, "max_tokens").Exists() {
			t.Fatalf("max_tokens should be absent; body=%s", string(gotBody))
		}
	})

	t.Run("ExecuteStream with use-max-completion-tokens=false emits max_tokens", func(t *testing.T) {
		gotBody = nil
		payload := []byte(`{"model":"alias-legacy","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":256}`)
		res, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "alias-legacy",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai"),
			Stream:       true,
		})
		if err != nil {
			t.Fatalf("ExecuteStream error: %v", err)
		}
		for range res.Chunks {
		}

		if got := gjson.GetBytes(gotBody, "max_tokens").Int(); got != 256 {
			t.Fatalf("max_tokens = %d, want 256; body=%s", got, string(gotBody))
		}
		if gjson.GetBytes(gotBody, "max_completion_tokens").Exists() {
			t.Fatalf("max_completion_tokens should be absent; body=%s", string(gotBody))
		}
	})

	t.Run("ExecuteStream responses request with max_output_tokens=null and use-max-completion-tokens=true preserves null", func(t *testing.T) {
		gotBody = nil
		payload := []byte(`{"model":"alias-new","input":[{"role":"user","content":"hi"}],"max_output_tokens":null}`)
		res, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "alias-new",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Stream:       true,
		})
		if err != nil {
			t.Fatalf("ExecuteStream error: %v", err)
		}
		for range res.Chunks {
		}

		if got := gjson.GetBytes(gotBody, "max_completion_tokens"); !got.Exists() || got.Type != gjson.Null {
			t.Fatalf("max_completion_tokens = %v, want null; body=%s", got, string(gotBody))
		}
		if gjson.GetBytes(gotBody, "max_tokens").Exists() {
			t.Fatalf("max_tokens should be absent; body=%s", string(gotBody))
		}
	})

	t.Run("ExecuteStream responses request with max_output_tokens=null and use-max-completion-tokens=false preserves null", func(t *testing.T) {
		gotBody = nil
		payload := []byte(`{"model":"alias-legacy","input":[{"role":"user","content":"hi"}],"max_output_tokens":null}`)
		res, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "alias-legacy",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Stream:       true,
		})
		if err != nil {
			t.Fatalf("ExecuteStream error: %v", err)
		}
		for range res.Chunks {
		}

		if got := gjson.GetBytes(gotBody, "max_tokens"); !got.Exists() || got.Type != gjson.Null {
			t.Fatalf("max_tokens = %v, want null; body=%s", got, string(gotBody))
		}
		if gjson.GetBytes(gotBody, "max_completion_tokens").Exists() {
			t.Fatalf("max_completion_tokens should be absent; body=%s", string(gotBody))
		}
	})

	t.Run("ExecuteStream chat request with max_tokens=null and use-max-completion-tokens=true preserves null", func(t *testing.T) {
		gotBody = nil
		payload := []byte(`{"model":"alias-new","messages":[{"role":"user","content":"hi"}],"max_tokens":null}`)
		res, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "alias-new",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai"),
			Stream:       true,
		})
		if err != nil {
			t.Fatalf("ExecuteStream error: %v", err)
		}
		for range res.Chunks {
		}

		if got := gjson.GetBytes(gotBody, "max_completion_tokens"); !got.Exists() || got.Type != gjson.Null {
			t.Fatalf("max_completion_tokens = %v, want null; body=%s", got, string(gotBody))
		}
		if gjson.GetBytes(gotBody, "max_tokens").Exists() {
			t.Fatalf("max_tokens should be absent; body=%s", string(gotBody))
		}
	})

	t.Run("ExecuteStream chat request with max_completion_tokens=null and use-max-completion-tokens=false preserves null", func(t *testing.T) {
		gotBody = nil
		payload := []byte(`{"model":"alias-legacy","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":null}`)
		res, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "alias-legacy",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai"),
			Stream:       true,
		})
		if err != nil {
			t.Fatalf("ExecuteStream error: %v", err)
		}
		for range res.Chunks {
		}

		if got := gjson.GetBytes(gotBody, "max_tokens"); !got.Exists() || got.Type != gjson.Null {
			t.Fatalf("max_tokens = %v, want null; body=%s", got, string(gotBody))
		}
		if gjson.GetBytes(gotBody, "max_completion_tokens").Exists() {
			t.Fatalf("max_completion_tokens should be absent; body=%s", string(gotBody))
		}
	})
}
