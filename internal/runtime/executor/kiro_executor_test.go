package executor

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestKiroExecutor_Identifier(t *testing.T) {
	e := NewKiroExecutor(&config.Config{})
	if id := e.Identifier(); id != "kiro" {
		t.Fatalf("expected id 'kiro', got %s", id)
	}
	if !e.SupportsApplyPatch() {
		t.Fatalf("expected SupportsApplyPatch true")
	}
	if f := e.RequestToFormat(cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); f != sdktranslator.FormatCodex {
		t.Fatalf("expected FormatCodex, got %v", f)
	}
}

func TestKiroExecutor_PrepareRequest(t *testing.T) {
	e := NewKiroExecutor(&config.Config{})
	req, _ := http.NewRequest(http.MethodPost, "https://runtime.us-east-1.kiro.dev/v1/responses", nil)

	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"access_token": "aoa-test-token",
			"profile_arn":  "arn:aws:codewhisperer:profile/123",
		},
	}

	if err := e.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest failed: %v", err)
	}

	if got := req.Header.Get("Authorization"); got != "Bearer aoa-test-token" {
		t.Fatalf("expected Bearer aoa-test-token, got %s", got)
	}
	if got := req.Header.Get("x-amzn-kiro-profile"); got != "arn:aws:codewhisperer:profile/123" {
		t.Fatalf("expected profile arn, got %s", got)
	}
	if got := req.Header.Get("x-amzn-kiro-origin"); got != "KIRO_CLI" {
		t.Fatalf("expected KIRO_CLI, got %s", got)
	}
}

func TestNormalizeKiroModel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"kiro/claude-sonnet-4.5", "claude-sonnet-4.5"},
		{"claude-3-5-sonnet", "claude-sonnet-4.5"},
		{"claude-3-5-haiku", "claude-haiku-4.5"},
		{"deepseek-chat", "deepseek-3.2"},
		{"minimax-m2.5", "minimax-m2.5"},
		{"auto", "auto"},
	}

	for _, tt := range tests {
		if got := normalizeKiroModel(tt.input); got != tt.want {
			t.Errorf("normalizeKiroModel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestKiroExecutor_CountTokens(t *testing.T) {
	e := NewKiroExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   "claude-sonnet-4.5",
		Payload: []byte(`{"messages":[{"role":"user","content":"Hello world"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	}

	resp, err := e.CountTokens(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("CountTokens failed: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("expected non-empty payload")
	}
}

func TestKiroExecutor_PrepareKiroRequest(t *testing.T) {
	e := NewKiroExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   "claude-sonnet-4.5",
		Payload: []byte(`{"messages":[{"role":"user","content":"Hello world"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	}
	model, body, _, _, err := e.prepareKiroRequest(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("prepareKiroRequest failed: %v", err)
	}
	t.Logf("model: %s", model)
	t.Logf("body: %s", string(body))
}
