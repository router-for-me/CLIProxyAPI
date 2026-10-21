package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestIsOfficialClaudeCodeRequest(t *testing.T) {
	payload := claudeCodeDetectionPayload(validClaudeCodeMetadataUserID)

	withUserAgent := func(userAgent string) http.Header {
		headers := confirmedClaudeCodeHeaders()
		headers.Set("User-Agent", userAgent)
		return headers
	}
	without := func(headers http.Header, name string) http.Header {
		headers.Del(name)
		return headers
	}

	tests := []struct {
		name        string
		headers     http.Header
		body        []byte
		countTokens bool
		want        bool
	}{
		{name: "genuine claude code", headers: confirmedClaudeCodeHeaders(), body: payload, want: true},
		{name: "vscode extension", headers: withUserAgent("claude-cli/2.1.280 (external, claude-vscode)"), body: payload, want: true},
		{name: "headless sdk-cli", headers: withUserAgent("claude-cli/2.1.280 (external, sdk-cli)"), body: payload, want: true},
		{name: "count tokens without metadata", headers: confirmedClaudeCodeHeaders(), body: []byte(`{"messages":[]}`), countTokens: true, want: true},
		{name: "count tokens flag required for missing metadata", headers: confirmedClaudeCodeHeaders(), body: []byte(`{"messages":[]}`), want: false},
		{name: "curl", headers: http.Header{"User-Agent": {"curl/8.7.1"}}, body: payload, want: false},
		{name: "no headers", headers: nil, body: payload, want: false},
		{name: "copied user agent only", headers: http.Header{"User-Agent": {"claude-cli/2.1.280 (external, cli)"}}, body: []byte(`{}`), want: false},
		{name: "missing x-app", headers: without(confirmedClaudeCodeHeaders(), "X-App"), body: payload, want: false},
		{name: "missing claude code beta", headers: without(confirmedClaudeCodeHeaders(), "Anthropic-Beta"), body: payload, want: false},
		{name: "missing metadata user id", headers: confirmedClaudeCodeHeaders(), body: []byte(`{"messages":[]}`), want: false},
		{name: "invalid metadata user id", headers: confirmedClaudeCodeHeaders(), body: claudeCodeDetectionPayload("not-a-claude-code-id"), want: false},
		{name: "agent sdk entrypoint", headers: withUserAgent("claude-cli/2.1.280 (external, sdk-ts)"), body: payload, want: false},
		{name: "non native user agent shape", headers: withUserAgent("claude-cli/2.1.280"), body: payload, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOfficialClaudeCodeRequest(tc.headers, tc.body, tc.countTokens, nil); got != tc.want {
				t.Fatalf("IsOfficialClaudeCodeRequest() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A Claude Code auto-update to a new minor line must not lock genuine users out,
// even though the cloaking pipeline (Confirmed) pins the minor line to its baseline.
func TestIsOfficialClaudeCodeRequestToleratesNewerMinorLine(t *testing.T) {
	headers := confirmedClaudeCodeHeaders()
	headers.Set("User-Agent", "claude-cli/2.2.0 (external, cli)")
	payload := claudeCodeDetectionPayload(validClaudeCodeMetadataUserID)

	if DetectClaudeCodeRequest(headers, payload, false).Confirmed {
		t.Fatal("precondition: 2.2.0 should not be Confirmed against the default baseline")
	}
	if !IsOfficialClaudeCodeRequest(headers, payload, false, &config.Config{}) {
		t.Fatal("genuine Claude Code on a newer minor line was rejected")
	}
}

func TestOfficialClientVerifierCodex(t *testing.T) {
	verify := NewOfficialClientVerifier(nil)
	tests := []struct {
		userAgent string
		want      bool
	}{
		{userAgent: "codex_cli_rs/0.50.0 (Ubuntu 24.4.0; x86_64) xterm-256color", want: true},
		{userAgent: "codex_cli_rs", want: true},
		{userAgent: "codex-tui/0.120.0 (Linux; x86_64)", want: true},
		{userAgent: "codex_exec/0.120.0 (Linux; x86_64)", want: true},
		{userAgent: "Codex Desktop/1.2.3", want: true},
		{userAgent: "python-requests/2.32.0", want: false},
		{userAgent: "OpenAI/Python 1.40.0", want: false},
		{userAgent: "claude-cli/2.1.280 (external, cli)", want: false},
		{userAgent: "", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.userAgent, func(t *testing.T) {
			opts := cliproxyexecutor.Options{Headers: http.Header{"User-Agent": {tc.userAgent}}}
			if got := verify(context.Background(), "codex", opts); got != tc.want {
				t.Fatalf("verify(codex, %q) = %v, want %v", tc.userAgent, got, tc.want)
			}
		})
	}
}

func TestOfficialClientVerifierClaudeUsesRequestPathForCountTokens(t *testing.T) {
	verify := NewOfficialClientVerifier(func() *config.Config { return &config.Config{} })
	body := []byte(`{"messages":[]}`)

	opts := cliproxyexecutor.Options{
		Headers:         confirmedClaudeCodeHeaders(),
		OriginalRequest: body,
		Metadata:        map[string]any{cliproxyexecutor.RequestPathMetadataKey: "/v1/messages/count_tokens"},
	}
	if !verify(context.Background(), "claude", opts) {
		t.Fatal("count_tokens from Claude Code was rejected")
	}
	opts.Metadata = map[string]any{cliproxyexecutor.RequestPathMetadataKey: "/v1/messages"}
	if verify(context.Background(), "claude", opts) {
		t.Fatal("messages request without metadata.user_id was accepted")
	}

	opts = cliproxyexecutor.Options{
		Headers:         confirmedClaudeCodeHeaders(),
		OriginalRequest: claudeCodeDetectionPayload(validClaudeCodeMetadataUserID),
	}
	if !verify(context.Background(), "claude", opts) {
		t.Fatal("genuine Claude Code messages request was rejected")
	}
}

func TestOfficialClientVerifierFallsBackToGinRequestHeaders(t *testing.T) {
	verify := NewOfficialClientVerifier(nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("User-Agent", "codex_cli_rs/0.50.0 (Linux; x86_64) tty")
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = request
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	if !verify(ctx, "codex", cliproxyexecutor.Options{}) {
		t.Fatal("gin request headers were ignored when options carried none")
	}
	// Headers on the options win over the raw request.
	override := cliproxyexecutor.Options{Headers: http.Header{"User-Agent": {"curl/8.7.1"}}}
	if verify(ctx, "codex", override) {
		t.Fatal("options headers did not take precedence over gin request headers")
	}
}

func TestOfficialClientVerifierRejectsUnknownProvider(t *testing.T) {
	verify := NewOfficialClientVerifier(nil)
	opts := cliproxyexecutor.Options{Headers: http.Header{"User-Agent": {"codex_cli_rs/0.50.0"}}}
	if verify(context.Background(), "gemini", opts) {
		t.Fatal("unsupported provider must never verify")
	}
}
