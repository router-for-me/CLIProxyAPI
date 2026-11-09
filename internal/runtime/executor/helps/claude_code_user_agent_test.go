package helps

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestDetectClaudeCodeRequestAcceptsUserAgentFields(t *testing.T) {
	for _, testCase := range []struct {
		name, userAgent, sdkVersion string
	}{
		{name: "cron", userAgent: "claude-cli/2.1.312 (external, cli, workload/cron)"},
		{name: "field before SDK", userAgent: "claude-cli/2.1.312 (external, cli, workload/cron, agent-sdk/0.2.133)", sdkVersion: "0.2.133"},
		{name: "field after SDK", userAgent: "claude-cli/2.1.312 (external, cli, agent-sdk/0.2.133, workload/cron)", sdkVersion: "0.2.133"},
		{name: "unknown fields", userAgent: "claude-cli/2.1.312 (external, cli, future-feature/value, workload/background)"},
	} {
		for _, countTokens := range []bool{false, true} {
			path := "messages"
			if countTokens {
				path = "count_tokens"
			}
			t.Run(testCase.name+"/"+path, func(t *testing.T) {
				headers := confirmedClaudeCodeHeaders()
				headers.Set("User-Agent", testCase.userAgent)
				payload := []byte(`{}`)
				if !countTokens {
					payload = claudeCodeDetectionPayload(validClaudeCodeMetadataUserID)
				}
				detection := DetectClaudeCodeRequest(headers, payload, countTokens)
				if !detection.Confirmed || !detection.StrongSignals || !detection.NativeClient {
					t.Fatalf("detection = %+v, want confirmed native CLI", detection)
				}
				if detection.Entrypoint != "cli" || detection.AgentSDKVersion != testCase.sdkVersion {
					t.Fatalf("identity = %q/%q, want cli/%q", detection.Entrypoint, detection.AgentSDKVersion, testCase.sdkVersion)
				}
			})
		}
	}
}

func TestParseClaudeCodeUserAgent(t *testing.T) {
	for _, testCase := range []struct {
		name, userAgent, entrypoint, sdkVersion, profile string
	}{
		{name: "plain", userAgent: "claude-cli/2.1.312 (external, cli)", entrypoint: "cli"},
		{name: "SDK", userAgent: "claude-cli/2.1.312 (external, sdk-ts, agent-sdk/0.2.133)", entrypoint: "sdk-ts", sdkVersion: "0.2.133"},
		{name: "cron", userAgent: "claude-cli/2.1.312 (external, cli, workload/cron)", entrypoint: "cli", profile: "claude-cli/2.1.312 (external, cli)"},
		{name: "SDK after metadata", userAgent: "claude-cli/2.1.312 (external, sdk-ts, workload/cron, agent-sdk/0.2.133, future-feature/value)", entrypoint: "sdk-ts", sdkVersion: "0.2.133", profile: "claude-cli/2.1.312 (external, sdk-ts, agent-sdk/0.2.133)"},
		{name: "token field name", userAgent: "claude-cli/2.1.312 (external, cli, future_field.v2/value/path)", entrypoint: "cli", profile: "claude-cli/2.1.312 (external, cli)"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			parsed, ok := parseClaudeCodeUserAgent(testCase.userAgent)
			if !ok || parsed.version != (claudeCLIVersion{major: 2, minor: 1, patch: 312}) || parsed.entrypoint != testCase.entrypoint || parsed.agentSDKVersion != testCase.sdkVersion {
				t.Fatalf("parsed = %+v, valid = %v", parsed, ok)
			}
			profile := testCase.profile
			if profile == "" {
				profile = testCase.userAgent
			}
			if parsed.profileUserAgent != profile {
				t.Fatalf("profile UA = %q, want %q", parsed.profileUserAgent, profile)
			}
		})
	}
}

func TestParseClaudeCodeUserAgentRejectsMalformedFields(t *testing.T) {
	for _, userAgent := range []string{
		"claude-cli/2.1.312(external, cli)",
		"claude-cli/2.1.312-extra (external, cli)",
		"claude-cli/999999999999999999999999.1.0 (external, cli)",
		"claude-cli/2.1.312 (external, )",
		"claude-cli/2.1.312 (external, cli, foo)",
		"claude-cli/2.1.312 (external, cli, /cron)",
		"claude-cli/2.1.312 (external, cli, workload/)",
		"claude-cli/2.1.312 (external, cli, bad name/value)",
		"claude-cli/2.1.312 (external, cli, workload/(cron))",
		"claude-cli/2.1.312 (external, cli, workload/cron, WORKLOAD/background)",
		"claude-cli/2.1.312 (external, cli, agent-sdk/dev)",
		"claude-cli/2.1.312 (external, cli, agent-sdk/0.2.133, agent-sdk/0.2.134)",
		"claude-cli/2.1.312 (external, cli, workload/cron) trailing",
		"claude-cli/2.1.312 (external, cli, workload/cr\non)",
		"claude-cli/2.1.312 (external, cli)\r\n",
	} {
		t.Run(userAgent, func(t *testing.T) {
			if parsed, ok := parseClaudeCodeUserAgent(userAgent); ok {
				t.Fatalf("accepted malformed UA: %+v", parsed)
			}
		})
	}
}

func TestDetectClaudeCodeRequestUserAgentFieldsKeepNativeGuards(t *testing.T) {
	for _, testCase := range []struct {
		name, userAgent, missingHeader                  string
		missingMetadata, allowEntrypoint, wantConfirmed bool
	}{
		{name: "old patch", userAgent: "claude-cli/2.1.279 (external, cli, workload/cron)"},
		{name: "unknown release line", userAgent: "claude-cli/2.2.0 (external, cli, workload/cron)"},
		{name: "unknown entrypoint", userAgent: "claude-cli/2.1.312 (external, other-client, workload/cron)"},
		{name: "missing app", missingHeader: "X-App"},
		{name: "missing beta", missingHeader: "Anthropic-Beta"},
		{name: "missing metadata", missingMetadata: true},
		{name: "configured entrypoint", userAgent: "claude-cli/2.1.312 (external, other-client, workload/cron)", allowEntrypoint: true, wantConfirmed: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			headers := confirmedClaudeCodeHeaders()
			userAgent := testCase.userAgent
			if userAgent == "" {
				userAgent = "claude-cli/2.1.312 (external, cli, workload/cron)"
			}
			headers.Set("User-Agent", userAgent)
			headers.Del(testCase.missingHeader)
			payload := claudeCodeDetectionPayload(validClaudeCodeMetadataUserID)
			if testCase.missingMetadata {
				payload = []byte(`{}`)
			}
			cfg := &config.Config{}
			if testCase.allowEntrypoint {
				cfg.ClaudeHeaderDefaults.NativeEntrypoints = []string{"other-client"}
			}
			detection := DetectClaudeCodeRequest(headers, payload, false, cfg)
			if detection.Confirmed != testCase.wantConfirmed {
				t.Fatalf("detection = %+v, want confirmed = %v", detection, testCase.wantConfirmed)
			}
		})
	}
}

func TestClaudeDeviceProfileUserAgentFieldsStayRequestScoped(t *testing.T) {
	for _, homeMode := range []bool{false, true} {
		name := "local"
		if homeMode {
			name = "home"
		}
		t.Run(name, func(t *testing.T) {
			ResetClaudeDeviceProfileCache()
			t.Cleanup(ResetClaudeDeviceProfileCache)
			client := newFakeClaudeDeviceProfileKVClient()
			useFakeClaudeDeviceProfileKVClient(t, client, homeMode, nil)
			auth := &cliproxyauth.Auth{ID: "request-fields-" + name}
			baseUA := "claude-cli/2.1.280 (external, cli, agent-sdk/0.2.133)"
			cronUA := "claude-cli/2.1.280 (external, cli, workload/cron, agent-sdk/0.2.133, future-feature/value)"
			for _, testCase := range []struct{ userAgent, want string }{
				{userAgent: cronUA, want: "claude-cli/2.1.280 (external, cli, agent-sdk/0.2.133, workload/cron, future-feature/value)"},
				{userAgent: baseUA, want: baseUA},
				{userAgent: "claude-cli/2.1.280 (external, cli, future-feature/next)", want: "claude-cli/2.1.280 (external, cli, agent-sdk/0.2.133, future-feature/next)"},
				{want: baseUA},
			} {
				headers := claudeDeviceHeaders(testCase.userAgent)
				profile, errProfile := ResolveClaudeDeviceProfileRequired(context.Background(), auth, "dummy-key", headers, nil)
				if errProfile != nil {
					t.Fatal(errProfile)
				}
				if profile.UserAgent != baseUA {
					t.Fatalf("profile UA = %q after %q, want %q", profile.UserAgent, testCase.userAgent, baseUA)
				}
				request := httptest.NewRequest("POST", "https://example.invalid/messages", nil)
				ApplyClaudeDeviceProfileHeaders(request, profile, testCase.userAgent)
				if got := request.Header.Get("User-Agent"); got != testCase.want {
					t.Fatalf("request UA = %q, want only current fields in %q", got, testCase.want)
				}
			}
			if homeMode && client.setCount != 1 {
				t.Fatalf("stored %d profiles, want one shared stable identity", client.setCount)
			}
		})
	}
}

func TestApplyClaudeDeviceProfileHeadersKeepsCurrentUserAgentFields(t *testing.T) {
	profile := defaultClaudeDeviceProfile(nil)
	profile.UserAgent = "claude-cli/2.1.280 (external, cli, agent-sdk/0.3.220)"
	for _, testCase := range []struct {
		name, userAgent, want string
	}{
		{name: "current fields", userAgent: "claude-cli/2.1.312 (external, cli, workload/cron, agent-sdk/0.2.133, future-feature/value)", want: "claude-cli/2.1.280 (external, cli, agent-sdk/0.3.220, workload/cron, future-feature/value)"},
		{name: "next request fields", userAgent: "claude-cli/2.1.312 (external, cli, future-feature/next)", want: "claude-cli/2.1.280 (external, cli, agent-sdk/0.3.220, future-feature/next)"},
		{name: "ordinary request", userAgent: "claude-cli/2.1.312 (external, cli)", want: profile.UserAgent},
		{name: "malformed request", userAgent: "claude-cli/2.1.312 (external, cli, foo)", want: profile.UserAgent},
		{name: "no request", want: profile.UserAgent},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "https://example.invalid/messages", nil)
			ApplyClaudeDeviceProfileHeaders(request, profile, testCase.userAgent)
			if got := request.Header.Get("User-Agent"); got != testCase.want {
				t.Fatalf("upstream UA = %q, want %q", got, testCase.want)
			}
			for name, want := range map[string]string{
				"X-Stainless-Package-Version": profile.PackageVersion,
				"X-Stainless-Runtime-Version": profile.RuntimeVersion,
				"X-Stainless-Os":              profile.OS,
				"X-Stainless-Arch":            profile.Arch,
			} {
				if got := request.Header.Get(name); got != want {
					t.Fatalf("%s = %q, want stabilized %q", name, got, want)
				}
			}
		})
	}
}

func FuzzParseClaudeCodeUserAgent(f *testing.F) {
	for _, seed := range []string{
		"claude-cli/2.1.312 (external, cli)",
		"claude-cli/2.1.312 (external, cli, workload/cron, agent-sdk/0.2.133)",
		"claude-cli/2.1.312 (external, cli, agent-sdk/dev)",
		"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, userAgent string) {
		parsed, ok := parseClaudeCodeUserAgent(userAgent)
		if !ok {
			return
		}
		stable, okStable := parseClaudeCodeUserAgent(parsed.profileUserAgent)
		if !okStable || stable.version != parsed.version || stable.entrypoint != parsed.entrypoint || stable.agentSDKVersion != parsed.agentSDKVersion || stable.profileUserAgent != parsed.profileUserAgent || len(stable.requestFields) != 0 {
			t.Fatalf("unstable parsed identity: %+v -> %+v (valid = %v)", parsed, stable, okStable)
		}
	})
}
