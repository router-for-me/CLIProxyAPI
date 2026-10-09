package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const preserveNativeIdentitySessionID = "11111111-2222-4333-8444-555555555555"

const preserveNativeIdentityUserID = `{"device_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","account_uuid":"","session_id":"` + preserveNativeIdentitySessionID + `"}`

// runPreserveNativeIdentityRequest sends one confirmed native Claude Code request
// through an OAuth (setup-token) credential and returns what reached upstream.
func runPreserveNativeIdentityRequest(t *testing.T, cfg *config.Config, stream bool, sessionHeader string) ([]byte, http.Header) {
	t.Helper()
	payload := []byte(`{"model":"claude-opus-4-6","stream":` + fmt.Sprint(stream) + `,"system":[{"type":"text","text":"interactive-system","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"x"}],"metadata":{"user_id":` + fmt.Sprintf("%q", preserveNativeIdentityUserID) + `}}`)
	body, headers, errRun := runPreserveNativeIdentityPayload(t, cfg, stream, sessionHeader, payload)
	if errRun != nil {
		t.Fatal(errRun)
	}
	return body, headers
}

func runPreserveNativeIdentityPayload(t *testing.T, cfg *config.Config, stream bool, sessionHeader string, payload []byte) ([]byte, http.Header, error) {
	t.Helper()
	var seenBody []byte
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenHeaders = r.Header.Clone()
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	incoming := http.Header{
		"User-Agent":                  {"claude-cli/2.1.295 (external, cli)"},
		"X-App":                       {"cli"},
		"Anthropic-Beta":              {"claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,effort-2025-11-24"},
		"X-Claude-Code-Session-Id":    {sessionHeader},
		"X-Stainless-Package-Version": {"0.128.0"},
		"X-Stainless-Runtime-Version": {"v26.3.0"},
		"X-Stainless-Os":              {"Linux"},
		"X-Stainless-Arch":            {"x64"},
	}
	executor := NewClaudeExecutor(cfg)
	auth := &cliproxyauth.Auth{
		ID: "claude-preserve-native-identity",
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat01-preserve-native-identity",
			"base_url": server.URL,
		},
		Metadata: claudeOAuthTestMetadata(),
	}
	req := cliproxyexecutor.Request{Model: "claude-opus-4-6", Payload: payload}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers:         incoming,
		Stream:          stream,
	}
	if stream {
		result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
		if errStream != nil {
			return nil, nil, fmt.Errorf("ExecuteStream() error = %w", errStream)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				return nil, nil, fmt.Errorf("stream chunk error = %w", chunk.Err)
			}
		}
	} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
		return nil, nil, fmt.Errorf("Execute() error = %w", errExecute)
	}
	return seenBody, seenHeaders, nil
}

func preserveNativeIdentityConfig(enabled bool) *config.Config {
	return &config.Config{ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{PreserveNativeIdentity: enabled}}
}

func TestClaudeExecutor_PreserveNativeIdentityKeepsCallerIdentity(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			body, headers := runPreserveNativeIdentityRequest(t, preserveNativeIdentityConfig(true), stream, preserveNativeIdentitySessionID)

			if got := gjson.GetBytes(body, "metadata.user_id").String(); got != preserveNativeIdentityUserID {
				t.Fatalf("metadata.user_id = %q, want caller identity %q", got, preserveNativeIdentityUserID)
			}
			if got := headers.Get("User-Agent"); got != "claude-cli/2.1.295 (external, cli)" {
				t.Fatalf("User-Agent = %q, want caller value", got)
			}
			if got := headers.Get("X-Stainless-Package-Version"); got != "0.128.0" {
				t.Fatalf("X-Stainless-Package-Version = %q, want caller value coherent with its User-Agent", got)
			}
			if got := headers.Get("Authorization"); got != "Bearer sk-ant-oat01-preserve-native-identity" {
				t.Fatalf("Authorization = %q, want selected credential", got)
			}
		})
	}
}

func TestClaudeExecutor_PreserveNativeIdentityDisabledRewritesIdentity(t *testing.T) {
	body, headers := runPreserveNativeIdentityRequest(t, preserveNativeIdentityConfig(false), false, preserveNativeIdentitySessionID)

	userID := gjson.GetBytes(body, "metadata.user_id").String()
	if userID == preserveNativeIdentityUserID {
		t.Fatalf("metadata.user_id = %q, want per-credential identity by default", userID)
	}
	if got := gjson.Get(userID, "session_id").String(); got != preserveNativeIdentitySessionID {
		t.Fatalf("session_id = %q, want %q", got, preserveNativeIdentitySessionID)
	}
	if got := headers.Get("X-Stainless-Package-Version"); got == "0.128.0" {
		t.Fatalf("X-Stainless-Package-Version = %q, want baseline by default", got)
	}
}

func TestClaudeExecutor_PreserveNativeIdentityRequiresSessionBoundIdentity(t *testing.T) {
	body, _ := runPreserveNativeIdentityRequest(t, preserveNativeIdentityConfig(true), false, "99999999-2222-4333-8444-555555555555")

	if got := gjson.GetBytes(body, "metadata.user_id").String(); got == preserveNativeIdentityUserID {
		t.Fatalf("metadata.user_id = %q, want rewrite when the identity does not match the session header", got)
	}
}

func TestClaudeExecutor_PreserveNativeIdentityRejectsDuplicateMetadata(t *testing.T) {
	const otherUserID = `{"device_id":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","account_uuid":"","session_id":"99999999-2222-4333-8444-555555555555"}`
	payload := []byte(`{"model":"claude-opus-4-6","system":[{"type":"text","text":"interactive-system"}],"messages":[{"role":"user","content":"x"}],"metadata":{"user_id":` + fmt.Sprintf("%q", preserveNativeIdentityUserID) + `},"metadata":{"user_id":` + fmt.Sprintf("%q", otherUserID) + `}}`)

	body, _, errRun := runPreserveNativeIdentityPayload(t, preserveNativeIdentityConfig(true), false, preserveNativeIdentitySessionID, payload)

	// The per-credential path rejects duplicated metadata, so the request must
	// either fail or at least never forward one of the caller identities.
	if errRun == nil && (strings.Contains(string(body), strings.Repeat("a", 64)) || strings.Contains(string(body), strings.Repeat("c", 64))) {
		t.Fatalf("upstream body forwarded a caller identity from a duplicated metadata member: %s", body)
	}
}
