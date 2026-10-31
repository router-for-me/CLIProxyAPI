package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func newImageGenerationCaptureServer(t *testing.T) (*httptest.Server, *[]byte) {
	t.Helper()
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	t.Cleanup(server.Close)
	return server, &gotBody
}

func hasInjectedImageGenerationTool(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if tool.Get("type").String() == "image_generation" {
			return true
		}
	}
	return false
}

func TestCodexExecutorCredentialDisableImageGenerationSkipsInjection(t *testing.T) {
	server, gotBody := newImageGenerationCaptureServer(t)
	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "test",
			"base_url": server.URL,
			cliproxyauth.AttributeCodexDisableImageGeneration: "true",
		},
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if hasInjectedImageGenerationTool(*gotBody) {
		t.Fatalf("image_generation tool injected despite credential-level disable: %s", gjson.GetBytes(*gotBody, "tools").Raw)
	}
}

func TestCodexExecutorCredentialWithoutDisableAttributeStillInjects(t *testing.T) {
	server, gotBody := newImageGenerationCaptureServer(t)
	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "test",
			"base_url": server.URL,
		},
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !hasInjectedImageGenerationTool(*gotBody) {
		t.Fatalf("image_generation tool missing for credential without disable flag: %s", *gotBody)
	}
}

func TestCodexImageGenerationDisabledForAuth(t *testing.T) {
	enabled := true
	disabled := false
	tests := []struct {
		name string
		cfg  *config.Config
		auth *cliproxyauth.Auth
		want bool
	}{
		{
			name: "attribute true wins",
			cfg:  &config.Config{},
			auth: &cliproxyauth.Auth{Attributes: map[string]string{
				cliproxyauth.AttributeCodexDisableImageGeneration: "true",
			}},
			want: true,
		},
		{
			name: "attribute false wins over entry",
			cfg: &config.Config{CodexKey: []config.CodexKey{{
				APIKey:                 "test",
				DisableImageGeneration: &enabled,
			}}},
			auth: &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key": "test",
				cliproxyauth.AttributeCodexDisableImageGeneration: "false",
			}},
			want: false,
		},
		{
			name: "invalid attribute falls back to entry",
			cfg: &config.Config{CodexKey: []config.CodexKey{{
				APIKey:                 "test",
				DisableImageGeneration: &enabled,
			}}},
			auth: &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key": "test",
				cliproxyauth.AttributeCodexDisableImageGeneration: "not-a-bool",
			}},
			want: true,
		},
		{
			name: "credential entry matches by api key",
			cfg: &config.Config{CodexKey: []config.CodexKey{{
				APIKey:                 "test",
				DisableImageGeneration: &enabled,
			}}},
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}},
			want: true,
		},
		{
			name: "no signal anywhere defaults to false",
			cfg:  &config.Config{},
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}},
			want: false,
		},
		{
			name: "entry flag false keeps injection",
			cfg: &config.Config{CodexKey: []config.CodexKey{{
				APIKey:                 "test",
				DisableImageGeneration: &disabled,
			}}},
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexImageGenerationDisabledForAuth(tt.cfg, tt.auth); got != tt.want {
				t.Fatalf("codexImageGenerationDisabledForAuth() = %v, want %v", got, tt.want)
			}
		})
	}
}
