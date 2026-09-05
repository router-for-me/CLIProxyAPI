package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	claudeNativeHelperSessionID = "11111111-2222-4333-8444-555555555555"
	claudeNativeHelperUserID    = `{"device_id":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","account_uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","session_id":"11111111-2222-4333-8444-555555555555"}`
	claudeNativeHelperCoreBetas = "oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05"
)

func claudeNativeHelperHeaders(betas, compression string, structured bool) http.Header {
	headers := http.Header{
		"Accept":            {"application/json"},
		"Accept-Encoding":   {compression},
		"Content-Type":      {"application/json"},
		"User-Agent":        {"claude-cli/2.1.258 (external, cli)"},
		"X-App":             {"cli"},
		"Anthropic-Beta":    {betas},
		"Anthropic-Version": {"2023-06-01"},
		"Anthropic-Dangerous-Direct-Browser-Access": {"true"},
		"X-Claude-Code-Session-Id":                  {claudeNativeHelperSessionID},
		"X-Client-Request-Id":                       {"66666666-7777-4888-8999-aaaaaaaaaaaa"},
		"X-Stainless-Lang":                          {"js"},
		"X-Stainless-Runtime":                       {"node"},
		"X-Stainless-Package-Version":               {"0.112.1"},
		"X-Stainless-Runtime-Version":               {"v26.3.0"},
		"X-Stainless-OS":                            {"MacOS"},
		"X-Stainless-Arch":                          {"arm64"},
		"X-Stainless-Retry-Count":                   {"0"},
		"X-Stainless-Timeout":                       {"600"},
	}
	if structured {
		headers.Set("X-Stainless-Async", "async")
	}
	canonical := make(http.Header, len(headers))
	for name, values := range headers {
		for _, value := range values {
			canonical.Add(name, value)
		}
	}
	return canonical
}

func claudeNativeHelperOAuthAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID: "native-helper-oauth",
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat-native-helper",
			"base_url": baseURL,
		},
		Metadata: claudeOAuthTestMetadata(),
	}
}

func TestApplyClaudeHeadersPreservesCallerAsyncWithoutFingerprintOptIn(t *testing.T) {
	for _, test := range []struct {
		name      string
		confirmed bool
		profile   bool
		wantAsync string
	}{
		{name: "confirmed native", confirmed: true, wantAsync: "async"},
		{name: "unconfirmed caller default", wantAsync: "async"},
		{name: "unconfirmed caller profile", profile: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, errRequest := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", nil)
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			incoming := http.Header{"X-Stainless-Async": {"async"}}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-api-key"}}
			if test.profile {
				auth.Attributes["fingerprint_profile"] = "claude-code-cli"
			}
			if errHeaders := applyClaudeHeaders(
				request,
				auth,
				"test-api-key",
				true,
				nil,
				[]byte(`{"model":"claude-haiku-4-5-20251001"}`),
				&config.Config{},
				incoming,
				test.confirmed,
				claudeNativeHelperSessionID,
			); errHeaders != nil {
				t.Fatalf("applyClaudeHeaders() error = %v", errHeaders)
			}
			if got := request.Header.Get("X-Stainless-Async"); got != test.wantAsync {
				t.Fatalf("X-Stainless-Async = %q, want %q", got, test.wantAsync)
			}
		})
	}
}

func TestClaudeExecutorMinimalNativeHelperPreservesMarkerlessWire(t *testing.T) {
	var upstreamBody []byte
	var upstreamHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		upstreamHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	payload := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"helper probe"}],"metadata":{"user_id":"` + strings.ReplaceAll(claudeNativeHelperUserID, `"`, `\"`) + `"}}`)
	headers := claudeNativeHelperHeaders(claudeNativeHelperCoreBetas, "gzip", false)
	executor := NewClaudeExecutor(&config.Config{})
	_, errExecute := executor.Execute(context.Background(), claudeNativeHelperOAuthAuth(server.URL), cliproxyexecutor.Request{
		Model:   "claude-haiku-4-5-20251001",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers:         headers,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	for _, path := range []string{"system", "stream", "context_management", "output_config"} {
		if got := gjson.GetBytes(upstreamBody, path); got.Exists() {
			t.Fatalf("helper body unexpectedly contains %s=%s: %s", path, got.Raw, upstreamBody)
		}
	}
	if bytes.Contains(upstreamBody, []byte(`"cache_control"`)) {
		t.Fatalf("helper body unexpectedly contains cache_control: %s", upstreamBody)
	}
	if got := gjson.GetBytes(upstreamBody, "messages.0.content").String(); got != "helper probe" {
		t.Fatalf("messages.0.content = %q, want preserved string", got)
	}
	if !bytes.HasPrefix(upstreamBody, []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":`)) {
		t.Fatalf("helper top-level order changed: %s", upstreamBody)
	}
	assertClaudeNativeHelperHeaders(t, upstreamHeaders, headers)
}

func TestClaudeExecutorStructuredNativeHelperPreservesStreamProfile(t *testing.T) {
	var upstreamBody []byte
	var upstreamHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		upstreamHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-4-5-20251001\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	betas := claudeNativeHelperCoreBetas + ",structured-outputs-2025-12-15"
	payload := []byte(`{"model":"claude-haiku-4-5-20251001","messages":[{"role":"user","content":[{"type":"text","text":"helper probe"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.258; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"Return a short title."}],"tools":[],"metadata":{"user_id":"` + strings.ReplaceAll(claudeNativeHelperUserID, `"`, `\"`) + `"},"max_tokens":32000,"thinking":{"type":"disabled"},"temperature":1,"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}}},"stream":true}`)
	headers := claudeNativeHelperHeaders(betas, "gzip, deflate, br, zstd", true)
	executor := NewClaudeExecutor(&config.Config{})
	result, errStream := executor.ExecuteStream(context.Background(), claudeNativeHelperOAuthAuth(server.URL), cliproxyexecutor.Request{
		Model:   "claude-haiku-4-5-20251001",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers:         headers,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	if got := gjson.GetBytes(upstreamBody, "system.#").Int(); got != 3 {
		t.Fatalf("system block count = %d, want native 3: %s", got, upstreamBody)
	}
	if !bytes.HasPrefix(upstreamBody, []byte(`{"model":"claude-haiku-4-5-20251001","messages":`)) {
		t.Fatalf("structured helper top-level order changed: %s", upstreamBody)
	}
	for _, path := range []string{"context_management", "output_config.effort"} {
		if got := gjson.GetBytes(upstreamBody, path); got.Exists() {
			t.Fatalf("structured helper unexpectedly contains %s=%s: %s", path, got.Raw, upstreamBody)
		}
	}
	if bytes.Contains(upstreamBody, []byte(`"cache_control"`)) {
		t.Fatalf("structured helper unexpectedly contains cache_control: %s", upstreamBody)
	}
	if got := gjson.GetBytes(upstreamBody, "stream").Bool(); !got {
		t.Fatalf("structured helper stream = false, want true: %s", upstreamBody)
	}
	if got := gjson.GetBytes(upstreamBody, "system.0.text").String(); strings.Contains(got, "cch=00000") || !strings.Contains(got, " cch=") {
		t.Fatalf("structured helper billing CCH was not re-signed: %q", got)
	}
	assertClaudeNativeHelperHeaders(t, upstreamHeaders, headers)
}

func assertClaudeNativeHelperHeaders(t *testing.T, got, incoming http.Header) {
	t.Helper()
	if got.Get("Anthropic-Beta") != incoming.Get("Anthropic-Beta") {
		t.Fatalf("Anthropic-Beta = %q, want exact native helper profile %q", got.Get("Anthropic-Beta"), incoming.Get("Anthropic-Beta"))
	}
	if strings.Contains(got.Get("Anthropic-Beta"), claudeExtendedCacheTTLBeta) || strings.Contains(got.Get("Anthropic-Beta"), claudeCodeBeta) {
		t.Fatalf("Anthropic-Beta gained standard Claude Code cache betas: %q", got.Get("Anthropic-Beta"))
	}
	for _, name := range []string{
		"Accept",
		"Accept-Encoding",
		"Content-Type",
		"User-Agent",
		"X-App",
		"Anthropic-Version",
		"Anthropic-Dangerous-Direct-Browser-Access",
		"X-Claude-Code-Session-Id",
		"X-Client-Request-Id",
		"X-Stainless-Async",
		"X-Stainless-Lang",
		"X-Stainless-Runtime",
		"X-Stainless-Package-Version",
		"X-Stainless-Runtime-Version",
		"X-Stainless-OS",
		"X-Stainless-Arch",
		"X-Stainless-Retry-Count",
		"X-Stainless-Timeout",
	} {
		gotValue := claudeNativeHelperHeaderValue(got, name)
		wantValue := claudeNativeHelperHeaderValue(incoming, name)
		if gotValue != wantValue {
			t.Fatalf("%s = %q, want preserved %q", name, gotValue, wantValue)
		}
	}
}

func claudeNativeHelperHeaderValue(headers http.Header, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			return strings.Join(values, ",")
		}
	}
	return ""
}

// The measured minimal helper has no system field at all, so injecting a billing
// header would itself be the deviation. Keying the fallback on system presence means
// that if a payload rule later attaches a system prompt, the billing header and its
// CCH come back instead of shipping a system block native would never send unsigned.
func TestClaudeBodyNeedsBillingFallbackTracksSystemPresence(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "measured minimal helper has no system",
			body: `{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"probe"}]}`,
			want: false,
		},
		{
			name: "structured helper carries its own billing header",
			body: `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.258; cc_entrypoint=cli; cch=00000;"}]}`,
			want: true,
		},
		{
			name: "pipeline attached a system prompt without a billing header",
			body: `{"system":[{"type":"text","text":"injected by a payload rule"}]}`,
			want: true,
		},
		{
			name: "string system prompt also needs the fallback",
			body: `{"system":"injected by a payload rule"}`,
			want: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := claudeBodyNeedsBillingFallback([]byte(test.body)); got != test.want {
				t.Fatalf("claudeBodyNeedsBillingFallback() = %v, want %v", got, test.want)
			}
		})
	}
}
