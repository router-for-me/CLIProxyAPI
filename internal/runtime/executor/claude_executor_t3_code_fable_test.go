package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeExecutor_ConfirmedT3CodeFablePreservesNativeFingerprintLegacyHeaders(t *testing.T) {
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-fable-5-2","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	const (
		sessionID = "11111111-2222-4333-8444-555555555555"
		t3UA      = "claude-cli/2.1.323 (external, t3-code, agent-sdk/0.3.321)"
		t3Package = "0.128.0"
		t3Runtime = "v26.12.1"
	)
	userID := fmt.Sprintf(`{"device_id":"0000000000000000000000000000000000000000000000000000000000000000","account_uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","session_id":%q}`, sessionID)
	payload := []byte(`{"model":"claude-fable-5-2","messages":[{"role":"user","content":"fable probe"}],"metadata":{"user_id":` + fmt.Sprintf("%q", userID) + `}}`)
	incoming := http.Header{
		"User-Agent":     {t3UA},
		"X-App":          {"cli"},
		"Anthropic-Beta": {"claude-code-20250219,interleaved-thinking-2025-05-14"},
		"Anthropic-Dangerous-Direct-Browser-Access": {"true"},
		"X-Claude-Code-Session-Id":                  {sessionID},
		"X-Stainless-Package-Version":               {t3Package},
		"X-Stainless-Runtime-Version":               {t3Runtime},
		"X-Stainless-Os":                            {"MacOS"},
		"X-Stainless-Arch":                          {"arm64"},
	}

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "auth-t3-fable", Attributes: map[string]string{
		"api_key":  "key-t3-fable",
		"base_url": server.URL,
	}}
	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-fable-5-2",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers:         incoming,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	assertClaudeFingerprint(t, seenHeaders, t3UA, t3Package, t3Runtime, "MacOS", "arm64")
}

func TestClaudeExecutor_ConfirmedT3CodeFablePreservesNativeFingerprintStabilized(t *testing.T) {
	helps.ResetClaudeDeviceProfileCache()
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		seenHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-fable-5-2","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	const (
		sessionID = "11111111-2222-4333-8444-555555555555"
		t3UA      = "claude-cli/2.1.323 (external, t3-code, agent-sdk/0.3.321)"
		t3Package = "0.128.0"
		t3Runtime = "v26.12.1"
	)
	userID := fmt.Sprintf(`{"device_id":"0000000000000000000000000000000000000000000000000000000000000000","account_uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","session_id":%q}`, sessionID)
	payload := []byte(`{"model":"claude-fable-5-2","stream":true,"messages":[{"role":"user","content":"fable stream probe"}],"metadata":{"user_id":` + fmt.Sprintf("%q", userID) + `}}`)
	incoming := http.Header{
		"User-Agent":     {t3UA},
		"X-App":          {"cli"},
		"Anthropic-Beta": {"claude-code-20250219,interleaved-thinking-2025-05-14"},
		"Anthropic-Dangerous-Direct-Browser-Access": {"true"},
		"X-Claude-Code-Session-Id":                  {sessionID},
		"X-Stainless-Package-Version":               {t3Package},
		"X-Stainless-Runtime-Version":               {t3Runtime},
		"X-Stainless-Os":                            {"MacOS"},
		"X-Stainless-Arch":                          {"arm64"},
	}
	stabilize := true
	executor := NewClaudeExecutor(&config.Config{ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{StabilizeDeviceProfile: &stabilize}})
	auth := &cliproxyauth.Auth{ID: "auth-t3-fable-stabilized", Attributes: map[string]string{
		"api_key":  "key-t3-fable-stabilized",
		"base_url": server.URL,
	}}

	_, errStream := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-fable-5-2",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers:         incoming,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	assertClaudeFingerprint(t, seenHeaders, t3UA, t3Package, t3Runtime, "MacOS", "arm64")
}

func TestClaudeExecutor_CopiedT3CodeUserAgentWithoutMetadataStaysCloaked(t *testing.T) {
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-fable-5-2","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	const t3UA = "claude-cli/2.1.323 (external, t3-code, agent-sdk/0.3.321)"
	payload := []byte(`{"model":"claude-fable-5-2","messages":[{"role":"user","content":"probe"}]}`)
	incoming := http.Header{
		"User-Agent":                  {t3UA},
		"X-App":                       {"cli"},
		"Anthropic-Beta":              {"claude-code-20250219"},
		"X-Stainless-Package-Version": {"0.128.0"},
		"X-Stainless-Runtime-Version": {"v26.12.1"},
	}
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":    "key-copied-t3",
		"base_url":   server.URL,
		"cloak_mode": "always",
	}}
	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-fable-5-2",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers:         incoming,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	baseline := helps.DefaultClaudeVersion(nil)
	if got := seenHeaders.Get("User-Agent"); got == t3UA {
		t.Fatalf("User-Agent = %q, want cloaked baseline not copied t3-code", got)
	}
	if got := seenHeaders.Get("User-Agent"); got != fmt.Sprintf("claude-cli/%s (external, cli)", baseline) {
		t.Fatalf("User-Agent = %q, want baseline cloak", got)
	}
}
