package helps

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const validClaudeCodeMetadataUserID = `{"device_id":"0000000000000000000000000000000000000000000000000000000000000000","account_uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","session_id":"11111111-2222-4333-8444-555555555555"}`

func claudeCodeDetectionPayload(userID string) []byte {
	encodedUserID, _ := json.Marshal(userID)
	return []byte(`{"metadata":{"user_id":` + string(encodedUserID) + `}}`)
}

func confirmedClaudeCodeHeaders() http.Header {
	return http.Header{
		"User-Agent":     {"claude-cli/2.1.220 (external, cli)"},
		"X-App":          {"cli"},
		"Anthropic-Beta": {"claude-code-20250219,interleaved-thinking-2025-05-14"},
	}
}

func TestDetectClaudeCodeRequestRequiresAllFourMessageSignals(t *testing.T) {
	payload := claudeCodeDetectionPayload(validClaudeCodeMetadataUserID)
	detection := DetectClaudeCodeRequest(confirmedClaudeCodeHeaders(), payload, false)

	if !detection.Confirmed || !detection.StrongSignals || !detection.NativeClient {
		t.Fatalf("detection = %#v, want native CLI confirmed", detection)
	}
	if !detection.XAppCLI || !detection.UserAgent || !detection.BetasPresent || !detection.MetadataUserID {
		t.Fatalf("detection signals = %#v, want all present", detection)
	}
}

func TestDetectClaudeCodeRequestAcceptsConfiguredMeasuredBaseline(t *testing.T) {
	headers := confirmedClaudeCodeHeaders()
	headers.Set("User-Agent", "claude-cli/2.2.0 (external, cli)")
	payload := claudeCodeDetectionPayload(validClaudeCodeMetadataUserID)
	if detection := DetectClaudeCodeRequest(headers, payload, false); detection.Confirmed {
		t.Fatalf("default detection = %#v, want unconfigured 2.2.0 rejected", detection)
	}

	cfg := &config.Config{ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
		UserAgent:      "claude-cli/2.2.0 (external, cli)",
		PackageVersion: "0.95.0",
		RuntimeVersion: "v26.4.0",
	}}
	if detection := DetectClaudeCodeRequest(headers, payload, false, cfg); !detection.Confirmed {
		t.Fatalf("configured detection = %#v, want measured baseline confirmed", detection)
	}
}

func TestDetectClaudeCodeRequestRejectsEachMissingMessageSignal(t *testing.T) {
	payload := claudeCodeDetectionPayload(validClaudeCodeMetadataUserID)
	for _, test := range []struct {
		name    string
		headers http.Header
		body    []byte
	}{
		{name: "x-app", headers: http.Header{"User-Agent": {"claude-cli/2.1.220 (external, cli)"}, "Anthropic-Beta": {"claude-code-20250219"}}, body: payload},
		{name: "user-agent", headers: http.Header{"User-Agent": {"curl/8.7.1"}, "X-App": {"cli"}, "Anthropic-Beta": {"claude-code-20250219"}}, body: payload},
		{name: "betas", headers: http.Header{"User-Agent": {"claude-cli/2.1.220 (external, cli)"}, "X-App": {"cli"}}, body: payload},
		{name: "metadata", headers: confirmedClaudeCodeHeaders(), body: []byte(`{"messages":[]}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if detection := DetectClaudeCodeRequest(test.headers, test.body, false); detection.Confirmed {
				t.Fatalf("detection = %#v, want unconfirmed", detection)
			}
		})
	}
}

func TestDetectClaudeCodeRequestClassifiesEntrypoints(t *testing.T) {
	payload := claudeCodeDetectionPayload(validClaudeCodeMetadataUserID)
	for _, test := range []struct {
		name            string
		userAgent       string
		entrypoint      string
		subclient       string
		agentSDKVersion string
		native          bool
	}{
		{name: "cli", userAgent: "claude-cli/2.1.220 (external, cli)", entrypoint: "cli", subclient: "claude-code-cli", native: true},
		{name: "vscode-agent-sdk", userAgent: "claude-cli/2.1.220 (external, claude-vscode, agent-sdk/0.3.220)", entrypoint: "claude-vscode", subclient: "claude-code-vscode", agentSDKVersion: "0.3.220", native: true},
		{name: "sdk-cli", userAgent: "claude-cli/2.1.220 (external, sdk-cli)", entrypoint: "sdk-cli", subclient: "claude-code-cli-sdk", native: true},
		{name: "sdk-ts", userAgent: "claude-cli/2.1.220 (external, sdk-ts, agent-sdk/0.3.220)", entrypoint: "sdk-ts", subclient: "claude-code-sdk-ts", agentSDKVersion: "0.3.220"},
		{name: "sdk-py", userAgent: "claude-cli/2.1.220 (external, sdk-py, agent-sdk/0.1.0)", entrypoint: "sdk-py", subclient: "claude-code-sdk-py", agentSDKVersion: "0.1.0"},
		{name: "desktop", userAgent: "claude-cli/2.1.220 (external, claude-desktop)", entrypoint: "claude-desktop", subclient: "claude-desktop"},
		{name: "desktop-third-party-inference", userAgent: "claude-cli/2.1.220 (external, claude-desktop-3p)", entrypoint: "claude-desktop-3p", subclient: "claude-desktop-3p"},
		{name: "remote", userAgent: "claude-cli/2.1.220 (external, remote)", entrypoint: "remote", subclient: "claude-remote"},
		{name: "github-action", userAgent: "claude-cli/2.1.220 (external, claude-code-github-action)", entrypoint: "claude-code-github-action", subclient: "claude-code-gh-action"},
		{name: "unknown", userAgent: "claude-cli/2.1.220 (external, copied-client)", entrypoint: "copied-client"},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := confirmedClaudeCodeHeaders()
			headers.Set("User-Agent", test.userAgent)
			detection := DetectClaudeCodeRequest(headers, payload, false)
			if !detection.StrongSignals {
				t.Fatalf("detection = %#v, want all CCH strong signals", detection)
			}
			if detection.Confirmed != test.native || detection.NativeClient != test.native {
				t.Fatalf("detection = %#v, want native/confirmed %t", detection, test.native)
			}
			if detection.Entrypoint != test.entrypoint || detection.Subclient != test.subclient || detection.AgentSDKVersion != test.agentSDKVersion {
				t.Fatalf("detection identity = %#v, want entrypoint %q subclient %q agent SDK %q", detection, test.entrypoint, test.subclient, test.agentSDKVersion)
			}
		})
	}
}

func TestDetectClaudeCodeCountTokensAllowsMissingMetadata(t *testing.T) {
	headers := confirmedClaudeCodeHeaders()
	headers.Set("User-Agent", "claude-cli/2.1.220 (external, claude-vscode, agent-sdk/0.3.220)")
	detection := DetectClaudeCodeRequest(headers, []byte(`{"messages":[]}`), true)
	if !detection.Confirmed {
		t.Fatalf("detection = %#v, want confirmed", detection)
	}
	if detection.MetadataUserID {
		t.Fatalf("metadata signal = true, want false: %#v", detection)
	}
	if detection.Subclient != "claude-code-vscode" || detection.AgentSDKVersion != "0.3.220" {
		t.Fatalf("count_tokens identity = %#v, want VSCode Agent SDK", detection)
	}
}

func TestDetectClaudeCodeRequestRejectsMalformedNativeSignals(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
		userID  string
	}{
		{name: "legacy metadata", headers: confirmedClaudeCodeHeaders(), userID: "user_abc_account__session_session"},
		{name: "short device", headers: confirmedClaudeCodeHeaders(), userID: `{"device_id":"abc","account_uuid":"","session_id":"11111111-2222-4333-8444-555555555555"}`},
		{name: "uppercase device", headers: confirmedClaudeCodeHeaders(), userID: `{"device_id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","account_uuid":"","session_id":"11111111-2222-4333-8444-555555555555"}`},
		{name: "invalid session", headers: confirmedClaudeCodeHeaders(), userID: `{"device_id":"0000000000000000000000000000000000000000000000000000000000000000","account_uuid":"","session_id":"session"}`},
		{name: "malformed user agent", headers: http.Header{"User-Agent": {"claude-cli/not-a-version (external, cli)"}, "X-App": {"cli"}, "Anthropic-Beta": {"claude-code-20250219"}}, userID: validClaudeCodeMetadataUserID},
		{name: "unmeasured next-minor user agent", headers: http.Header{"User-Agent": {"claude-cli/2.2.0 (external, cli)"}, "X-App": {"cli"}, "Anthropic-Beta": {"claude-code-20250219"}}, userID: validClaudeCodeMetadataUserID},
		{name: "implausible future user agent", headers: http.Header{"User-Agent": {"claude-cli/999.0.0 (external, cli)"}, "X-App": {"cli"}, "Anthropic-Beta": {"claude-code-20250219"}}, userID: validClaudeCodeMetadataUserID},
		{name: "unrelated beta", headers: http.Header{"User-Agent": {"claude-cli/2.1.220 (external, cli)"}, "X-App": {"cli"}, "Anthropic-Beta": {"anything"}}, userID: validClaudeCodeMetadataUserID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if detection := DetectClaudeCodeRequest(test.headers, claudeCodeDetectionPayload(test.userID), false); detection.Confirmed {
				t.Fatalf("detection = %#v, want malformed signal to use local profile", detection)
			}
		})
	}
}
