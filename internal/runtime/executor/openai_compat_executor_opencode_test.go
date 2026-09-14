package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// The OpenCode gateway rejects requests without x-opencode-session, which an
// OpenAI-compatible client that did not name its provider "opencode*" never sends.
func TestOpenAICompatExecutorForwardsSessionToOpenCode(t *testing.T) {
	t.Parallel()

	var captured []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = append(captured, r.Header.Clone())
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","created":0,"model":"test-model",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	tests := []struct {
		name     string
		provider string
		headers  http.Header
		want     string
	}{
		{
			name:     "synthesizes the header from the client session",
			provider: "opencode-go",
			headers:  http.Header{"X-Session-Affinity": []string{"ses_client"}},
			want:     "ses_client",
		},
		{
			name:     "preserves the client's own opencode session",
			provider: "opencode-go",
			headers:  http.Header{"X-Opencode-Session": []string{"ses_native"}},
			want:     "ses_native",
		},
		{
			name:     "unrelated compatibility providers receive no session header",
			provider: "cpa",
			headers:  http.Header{"X-Session-Affinity": []string{"ses_client"}},
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := NewOpenAICompatExecutor(tt.provider, &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{{Name: tt.provider}},
			})
			auth := &cliproxyauth.Auth{
				Provider: "openai-compatibility",
				Attributes: map[string]string{
					"base_url":     server.URL + "/v1",
					"api_key":      "test",
					"compat_name":  tt.provider,
					"provider_key": tt.provider,
				},
			}
			payload := []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`)

			if _, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "test-model",
				Payload: payload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai"),
				Headers:      tt.headers,
			}); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			got := captured[len(captured)-1]
			if value := got.Get("x-opencode-session"); value != tt.want {
				t.Fatalf("x-opencode-session = %q, want %q", value, tt.want)
			}
			if tt.want == "" && got.Get("x-session-affinity") != "" {
				t.Fatalf("client session header leaked to %q", tt.provider)
			}
		})
	}
}

// A session identity annotated on the context (extracted before protocol
// translation on the original request) must win over re-extracting from the
// translated body, so that translation dropping non-OpenAI session signals
// (such as Gemini cachedContent) does not cause the session header to diverge.
func TestOpenAICompatExecutorPrefersContextRoutingSessionOverTranslatedBody(t *testing.T) {
	t.Parallel()

	var captured []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = append(captured, r.Header.Clone())
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","created":0,"model":"test-model",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	provider := "opencode-go"
	exec := NewOpenAICompatExecutor(provider, &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: provider}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test",
			"compat_name":  provider,
			"provider_key": provider,
		},
	}
	payload := []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`)

	ctx := util.WithSessionID(context.Background(), "affinity:ses_context_routing")
	if _, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "test-model",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := captured[len(captured)-1]
	if value := got.Get("x-opencode-session"); value != "ses_context_routing" {
		t.Fatalf("x-opencode-session = %q, want context routing session %q", value, "ses_context_routing")
	}
}
