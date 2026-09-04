package helps

import (
	"encoding/json"
	"net/http"
	"strings"
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
		"User-Agent":     {"claude-cli/2.1.258 (external, cli)"},
		"X-App":          {"cli"},
		"Anthropic-Beta": {"claude-code-20250219,interleaved-thinking-2025-05-14"},
	}
}

func measuredClaudeCodeHelperHeaders(betaProfile string, structured bool) http.Header {
	profile := defaultClaudeDeviceProfile(&config.Config{})
	headers := http.Header{
		"Accept":            {"application/json"},
		"Accept-Encoding":   {"gzip"},
		"Content-Type":      {"application/json"},
		"User-Agent":        {profile.UserAgent},
		"X-App":             {"cli"},
		"Anthropic-Beta":    {betaProfile},
		"Anthropic-Version": {"2023-06-01"},
		"Anthropic-Dangerous-Direct-Browser-Access": {"true"},
		"X-Claude-Code-Session-Id":                  {"11111111-2222-4333-8444-555555555555"},
		"X-Client-Request-Id":                       {"66666666-7777-4888-8999-aaaaaaaaaaaa"},
		"X-Stainless-Lang":                          {"js"},
		"X-Stainless-Runtime":                       {"node"},
		"X-Stainless-Package-Version":               {profile.PackageVersion},
		"X-Stainless-Runtime-Version":               {profile.RuntimeVersion},
		"X-Stainless-OS":                            {profile.OS},
		"X-Stainless-Arch":                          {profile.Arch},
		"X-Stainless-Retry-Count":                   {"0"},
		"X-Stainless-Timeout":                       {"600"},
	}
	if structured {
		headers.Set("Accept-Encoding", "gzip, deflate, br, zstd")
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

func measuredClaudeCodeMinimalHelperPayload() []byte {
	encodedUserID, _ := json.Marshal(validClaudeCodeMetadataUserID)
	return []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"helper probe"}],"metadata":{"user_id":` + string(encodedUserID) + `}}`)
}

func measuredClaudeCodeStructuredHelperPayload() []byte {
	encodedUserID, _ := json.Marshal(validClaudeCodeMetadataUserID)
	return []byte(`{"model":"claude-haiku-4-5-20251001","messages":[{"role":"user","content":[{"type":"text","text":"helper probe"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.258; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"Return a short title."}],"tools":[],"metadata":{"user_id":` + string(encodedUserID) + `},"max_tokens":32000,"thinking":{"type":"disabled"},"temperature":1,"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}}},"stream":true}`)
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
		{name: "x-app", headers: http.Header{"User-Agent": {"claude-cli/2.1.258 (external, cli)"}, "Anthropic-Beta": {"claude-code-20250219"}}, body: payload},
		{name: "user-agent", headers: http.Header{"User-Agent": {"curl/8.7.1"}, "X-App": {"cli"}, "Anthropic-Beta": {"claude-code-20250219"}}, body: payload},
		{name: "betas", headers: http.Header{"User-Agent": {"claude-cli/2.1.258 (external, cli)"}, "X-App": {"cli"}}, body: payload},
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
		{name: "cli", userAgent: "claude-cli/2.1.258 (external, cli)", entrypoint: "cli", subclient: "claude-code-cli", native: true},
		{name: "vscode-agent-sdk", userAgent: "claude-cli/2.1.258 (external, claude-vscode, agent-sdk/0.3.220)", entrypoint: "claude-vscode", subclient: "claude-code-vscode", agentSDKVersion: "0.3.220", native: true},
		{name: "sdk-cli", userAgent: "claude-cli/2.1.258 (external, sdk-cli)", entrypoint: "sdk-cli", subclient: "claude-code-cli-sdk", native: true},
		{name: "sdk-ts", userAgent: "claude-cli/2.1.258 (external, sdk-ts, agent-sdk/0.3.220)", entrypoint: "sdk-ts", subclient: "claude-code-sdk-ts", agentSDKVersion: "0.3.220"},
		{name: "sdk-py", userAgent: "claude-cli/2.1.258 (external, sdk-py, agent-sdk/0.1.0)", entrypoint: "sdk-py", subclient: "claude-code-sdk-py", agentSDKVersion: "0.1.0"},
		{name: "desktop", userAgent: "claude-cli/2.1.258 (external, claude-desktop)", entrypoint: "claude-desktop", subclient: "claude-desktop"},
		{name: "desktop-third-party-inference", userAgent: "claude-cli/2.1.258 (external, claude-desktop-3p)", entrypoint: "claude-desktop-3p", subclient: "claude-desktop-3p"},
		{name: "remote", userAgent: "claude-cli/2.1.258 (external, remote)", entrypoint: "remote", subclient: "claude-remote"},
		{name: "github-action", userAgent: "claude-cli/2.1.258 (external, claude-code-github-action)", entrypoint: "claude-code-github-action", subclient: "claude-code-gh-action"},
		{name: "unknown", userAgent: "claude-cli/2.1.258 (external, copied-client)", entrypoint: "copied-client"},
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
	headers.Set("User-Agent", "claude-cli/2.1.258 (external, claude-vscode, agent-sdk/0.3.220)")
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

func TestDetectClaudeCodeRequestRecognizesMeasuredHaikuHelpers(t *testing.T) {
	tests := []struct {
		name       string
		beta       string
		structured bool
		payload    []byte
	}{
		{
			name:    "minimal with redact thinking",
			beta:    claudeCodeHelperBetaProfile(true),
			payload: measuredClaudeCodeMinimalHelperPayload(),
		},
		{
			name:    "minimal without redact thinking",
			beta:    claudeCodeHelperBetaProfile(false),
			payload: measuredClaudeCodeMinimalHelperPayload(),
		},
		{
			name:       "structured title helper with advisor",
			beta:       claudeCodeHelperBetaProfile(true, "advisor-tool-2026-03-01", "structured-outputs-2025-12-15", "cache-diagnosis-2026-04-07"),
			structured: true,
			payload:    measuredClaudeCodeStructuredHelperPayload(),
		},
		{
			name:       "structured title helper with fallback credit",
			beta:       claudeCodeHelperBetaProfile(true, "structured-outputs-2025-12-15", "fallback-credit-2026-06-01"),
			structured: true,
			payload:    measuredClaudeCodeStructuredHelperPayload(),
		},
		{
			name:       "structured title helper with lowercase hex CCH",
			beta:       claudeCodeHelperBetaProfile(true, "structured-outputs-2025-12-15"),
			structured: true,
			payload:    []byte(strings.Replace(string(measuredClaudeCodeStructuredHelperPayload()), "cch=00000", "cch=7ee87", 1)),
		},
		{
			name:       "structured title helper without redact thinking",
			beta:       claudeCodeHelperBetaProfile(false, "structured-outputs-2025-12-15"),
			structured: true,
			payload:    measuredClaudeCodeStructuredHelperPayload(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detection := DetectClaudeCodeRequest(
				measuredClaudeCodeHelperHeaders(test.beta, test.structured),
				test.payload,
				false,
			)
			if !detection.Confirmed || !detection.StrongSignals || !detection.NativeClient || !detection.HelperProfile {
				t.Fatalf("detection = %#v, want confirmed measured helper", detection)
			}
			if detection.BetasPresent {
				t.Fatalf("claude-code beta signal = true, want helper profile to remain separate: %#v", detection)
			}
		})
	}
}

func TestDetectClaudeCodeRequestRejectsMalformedStructuredHaikuHelpers(t *testing.T) {
	basePayload := string(measuredClaudeCodeStructuredHelperPayload())
	beta := claudeCodeHelperBetaProfile(true, "structured-outputs-2025-12-15")
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "non-hex CCH", payload: strings.Replace(basePayload, "cch=00000", "cch=ghijk", 1)},
		{name: "uppercase CCH", payload: strings.Replace(basePayload, "cch=00000", "cch=7EE87", 1)},
		{name: "wrong token cap", payload: strings.Replace(basePayload, `"max_tokens":32000`, `"max_tokens":32001`, 1)},
		{name: "open schema", payload: strings.Replace(basePayload, `"additionalProperties":false`, `"additionalProperties":true`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			detection := DetectClaudeCodeRequest(measuredClaudeCodeHelperHeaders(beta, true), []byte(test.payload), false)
			if detection.Confirmed || detection.HelperProfile {
				t.Fatalf("detection = %#v, want malformed structured helper rejected", detection)
			}
		})
	}
}

func TestDetectClaudeCodeRequestRejectsNearMissHaikuHelpers(t *testing.T) {
	minimalPayload := string(measuredClaudeCodeMinimalHelperPayload())
	tests := []struct {
		name        string
		mutate      func(http.Header)
		payload     string
		countTokens bool
	}{
		{
			name: "unexpected beta profile",
			mutate: func(headers http.Header) {
				headers.Set("Anthropic-Beta", headers.Get("Anthropic-Beta")+",unknown-beta")
			},
			payload: minimalPayload,
		},
		{
			name: "missing stainless package",
			mutate: func(headers http.Header) {
				headers.Del("X-Stainless-Package-Version")
			},
			payload: minimalPayload,
		},
		{
			name: "wrong compression profile",
			mutate: func(headers http.Header) {
				headers.Set("Accept-Encoding", "gzip, deflate, br, zstd")
			},
			payload: minimalPayload,
		},
		{
			name: "mismatched session header",
			mutate: func(headers http.Header) {
				headers.Set("X-Claude-Code-Session-Id", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
			},
			payload: minimalPayload,
		},
		{
			name: "invalid request id",
			mutate: func(headers http.Header) {
				headers.Set("X-Client-Request-Id", "not-a-uuid")
			},
			payload: minimalPayload,
		},
		{
			name: "unexpected async mode",
			mutate: func(headers http.Header) {
				headers.Set("X-Stainless-Async", "async")
			},
			payload: minimalPayload,
		},
		{
			name:    "wrong helper model",
			payload: strings.Replace(minimalPayload, claudeCodeHelperModel, "claude-sonnet-4-6", 1),
		},
		{
			name:    "wrong helper token cap",
			payload: strings.Replace(minimalPayload, `"max_tokens":1`, `"max_tokens":2`, 1),
		},
		{
			name:    "extra root key",
			payload: strings.TrimSuffix(minimalPayload, "}") + `,"tools":[]}`,
		},
		{
			name:    "cache marker content shape",
			payload: strings.Replace(minimalPayload, `"content":"helper probe"`, `"content":[{"type":"text","text":"helper probe","cache_control":{"type":"ephemeral","ttl":"1h"}}]`, 1),
		},
		{
			name:        "count tokens endpoint",
			payload:     minimalPayload,
			countTokens: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			headers := measuredClaudeCodeHelperHeaders(claudeCodeHelperBetaProfile(true), false)
			if test.mutate != nil {
				test.mutate(headers)
			}
			detection := DetectClaudeCodeRequest(headers, []byte(test.payload), test.countTokens)
			if detection.Confirmed || detection.HelperProfile {
				t.Fatalf("detection = %#v, want helper near miss rejected", detection)
			}
		})
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
		{name: "unrelated beta", headers: http.Header{"User-Agent": {"claude-cli/2.1.258 (external, cli)"}, "X-App": {"cli"}, "Anthropic-Beta": {"anything"}}, userID: validClaudeCodeMetadataUserID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if detection := DetectClaudeCodeRequest(test.headers, claudeCodeDetectionPayload(test.userID), false); detection.Confirmed {
				t.Fatalf("detection = %#v, want malformed signal to use local profile", detection)
			}
		})
	}
}

// Recovered from the native metadata builder in 2.1.220, 2.1.221 and 2.1.227:
//
//	{...extraMetadata, device_id, account_uuid, session_id, ...parentSessionId && {parent_session_id}}
//
// parent_session_id is therefore a legitimate optional trailing key that sub-agent
// and forked sessions attach, and it must not disqualify a helper request.
func TestDetectClaudeCodeRequestAcceptsHelperSubagentParentSessionID(t *testing.T) {
	identity := `{"device_id":"0000000000000000000000000000000000000000000000000000000000000000","account_uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","session_id":"11111111-2222-4333-8444-555555555555","parent_session_id":"99999999-8888-4777-8666-555555555555"}`
	encoded, _ := json.Marshal(identity)
	payload := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"helper probe"}],"metadata":{"user_id":` + string(encoded) + `}}`)

	detection := DetectClaudeCodeRequest(
		measuredClaudeCodeHelperHeaders(claudeCodeHelperBetaProfile(true), false),
		payload,
		false,
	)
	if !detection.Confirmed || !detection.HelperProfile {
		t.Fatalf("detection = %#v, want a confirmed sub-agent helper", detection)
	}
}

func TestDetectClaudeCodeRequestRejectsHelperIdentityWithUnknownKeys(t *testing.T) {
	identity := `{"device_id":"0000000000000000000000000000000000000000000000000000000000000000","account_uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","session_id":"11111111-2222-4333-8444-555555555555","spoofed":"x"}`
	encoded, _ := json.Marshal(identity)
	payload := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"helper probe"}],"metadata":{"user_id":` + string(encoded) + `}}`)

	detection := DetectClaudeCodeRequest(
		measuredClaudeCodeHelperHeaders(claudeCodeHelperBetaProfile(true), false),
		payload,
		false,
	)
	if detection.HelperProfile {
		t.Fatalf("detection = %#v, want an unknown identity key to disqualify the helper profile", detection)
	}
}

// The surrounding device-profile pipeline pins OS/Arch to the configured baseline
// rather than rejecting a foreign platform, so a genuine Windows or Linux helper
// must still be recognized instead of being cloaked.
func TestDetectClaudeCodeRequestAcceptsHelperFromNonBaselinePlatform(t *testing.T) {
	for _, platform := range []struct{ os, arch string }{
		{"Windows", "x64"},
		{"Linux", "x64"},
		{"MacOS", "x64"},
	} {
		t.Run(platform.os+"/"+platform.arch, func(t *testing.T) {
			headers := measuredClaudeCodeHelperHeaders(claudeCodeHelperBetaProfile(true), false)
			headers.Set("X-Stainless-OS", platform.os)
			headers.Set("X-Stainless-Arch", platform.arch)

			detection := DetectClaudeCodeRequest(headers, measuredClaudeCodeMinimalHelperPayload(), false)
			if !detection.Confirmed || !detection.HelperProfile {
				t.Fatalf("detection = %#v, want a confirmed helper on a non-baseline platform", detection)
			}
		})
	}
}

func TestDetectClaudeCodeRequestRejectsHelperWithoutPlatformHeaders(t *testing.T) {
	for _, name := range []string{
		"X-Stainless-OS",
		"X-Stainless-Arch",
		"X-Stainless-Package-Version",
		"X-Stainless-Runtime-Version",
	} {
		t.Run("missing "+name, func(t *testing.T) {
			headers := measuredClaudeCodeHelperHeaders(claudeCodeHelperBetaProfile(true), false)
			headers.Del(name)

			detection := DetectClaudeCodeRequest(headers, measuredClaudeCodeMinimalHelperPayload(), false)
			if detection.HelperProfile {
				t.Fatalf("detection = %#v, want a missing %s to disqualify the helper profile", detection, name)
			}
		})
	}
}

func TestDetectClaudeCodeRequestRejectsHelperWithForeignSoftwareTuple(t *testing.T) {
	for name, value := range map[string]string{
		"X-Stainless-Package-Version": "0.0.1",
		"X-Stainless-Runtime-Version": "v0.0.1",
	} {
		t.Run(name, func(t *testing.T) {
			headers := measuredClaudeCodeHelperHeaders(claudeCodeHelperBetaProfile(true), false)
			headers.Set(name, value)

			detection := DetectClaudeCodeRequest(headers, measuredClaudeCodeMinimalHelperPayload(), false)
			if detection.HelperProfile {
				t.Fatalf("detection = %#v, want a foreign %s to disqualify the helper profile", detection, name)
			}
		})
	}
}

func TestNormalizedClaudeBetaHeaderIsDeterministic(t *testing.T) {
	canonical := http.Header{}
	canonical.Add("Anthropic-Beta", "oauth-2025-04-20")
	canonical.Add("Anthropic-Beta", "interleaved-thinking-2025-05-14")
	if got, want := normalizedClaudeBetaHeader(canonical), "oauth-2025-04-20,interleaved-thinking-2025-05-14"; got != want {
		t.Fatalf("canonical join = %q, want %q", got, want)
	}

	// Two non-canonical spellings in one map used to be joined in Go map order.
	nonCanonical := http.Header{
		"anthropic-beta": {"oauth-2025-04-20"},
		"ANTHROPIC-BETA": {"interleaved-thinking-2025-05-14"},
	}
	first := normalizedClaudeBetaHeader(nonCanonical)
	for i := 0; i < 50; i++ {
		if got := normalizedClaudeBetaHeader(nonCanonical); got != first {
			t.Fatalf("non-canonical join is order-dependent: %q then %q", first, got)
		}
	}
	if !strings.Contains(first, "oauth-2025-04-20") || !strings.Contains(first, "interleaved-thinking-2025-05-14") {
		t.Fatalf("non-canonical join lost values: %q", first)
	}

	if got := normalizedClaudeBetaHeader(nil); got != "" {
		t.Fatalf("nil header join = %q, want empty", got)
	}
}

// A confirmed helper is routed through misc.EnsureHeader, so CPA forwards the
// helper's own X-Stainless-Timeout and never the operator default. Keying the
// detector on claude-header-defaults.timeout instead of the measured constant
// therefore rejected every genuine helper whenever that value was customized.
func TestMeasuredHelperProfileIgnoresConfiguredStainlessTimeout(t *testing.T) {
	headers := measuredClaudeCodeHelperHeaders(claudeCodeHelperBetaProfile(true), false)
	payload := measuredClaudeCodeMinimalHelperPayload()
	if got := headers.Get("X-Stainless-Timeout"); got != claudeDefaultStainlessTimeout {
		t.Fatalf("measured helper timeout = %q, want %q", got, claudeDefaultStainlessTimeout)
	}

	withTimeout := func(timeout string) *config.Config {
		cfg := &config.Config{}
		cfg.ClaudeHeaderDefaults.Timeout = timeout
		return cfg
	}
	for _, test := range []struct {
		name string
		cfg  *config.Config
	}{
		{name: "nil config"},
		{name: "unset", cfg: &config.Config{}},
		{name: "measured default", cfg: withTimeout(claudeDefaultStainlessTimeout)},
		{name: "shorter operator default", cfg: withTimeout("300")},
		{name: "longer operator default", cfg: withTimeout("900")},
	} {
		t.Run(test.name, func(t *testing.T) {
			detection := DetectClaudeCodeRequest(headers, payload, false, test.cfg)
			if !detection.HelperProfile || !detection.Confirmed {
				t.Fatalf("detection = %#v, want confirmed helper regardless of configured timeout", detection)
			}
		})
	}

	// The measured constant stays the only accepted value, so a caller that does not
	// send it is still disqualified even when the operator default happens to match.
	t.Run("foreign timeout stays rejected", func(t *testing.T) {
		foreign := measuredClaudeCodeHelperHeaders(claudeCodeHelperBetaProfile(true), false)
		foreign.Set("X-Stainless-Timeout", "900")
		if detection := DetectClaudeCodeRequest(foreign, payload, false, withTimeout("900")); detection.HelperProfile {
			t.Fatalf("detection = %#v, want a non-measured timeout to disqualify the helper profile", detection)
		}
	})
}
