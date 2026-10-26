package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func parseIssue5093Config(t *testing.T, raw string) *config.Config {
	t.Helper()
	cfg, errParse := config.ParseConfigBytes([]byte(raw))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	return cfg
}

// Issue #5093: operators need exact strip/append levers applied after the
// rule-derived Anthropic-Beta set. Strip filters without re-sorting, and append
// adds names at the end without duplicating names that are already present.
func TestApplyClaudeHeaders_OperatorBetaStripAndAppend(t *testing.T) {
	cfg := parseIssue5093Config(t, `upstream:
  claude:
    header-defaults:
      anthropic-beta-strip: [" extended-cache-ttl-2025-04-11 ", "not-emitted-2099-01-01"]
      anthropic-beta-append: ["context-1m-2025-08-07", "advisor-tool-2026-03-01", "claude-code-20250219", "context-1m-2025-08-07"]
`)
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	for _, stream := range []bool{false, true} {
		baseline := newClaudeHeaderTestRequest(t, nil)
		if err := applyClaudeHeaders(baseline, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, stream, nil,
			body, nil, nil, false); err != nil {
			t.Fatalf("baseline applyClaudeHeaders(stream=%v) error = %v", stream, err)
		}
		derived := baseline.Header.Get("Anthropic-Beta")
		if !strings.Contains(derived, claudeExtendedCacheTTLBeta) ||
			strings.Contains(derived, claudeContext1MBeta) ||
			strings.Contains(derived, claudeAdvisorToolBeta) {
			t.Fatalf("stream=%v: unexpected derived baseline %q", stream, derived)
		}
		want := withoutClaudeBeta(derived, claudeExtendedCacheTTLBeta) + "," + claudeContext1MBeta + "," + claudeAdvisorToolBeta

		req := newClaudeHeaderTestRequest(t, nil)
		if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, stream, nil,
			body, cfg, nil, false); err != nil {
			t.Fatalf("applyClaudeHeaders(stream=%v) error = %v", stream, err)
		}
		if got := req.Header.Get("Anthropic-Beta"); got != want {
			t.Fatalf("stream=%v: Anthropic-Beta = %q, want %q", stream, got, want)
		}
	}
}

// The header is assembled more than once per request; re-applying overrides must
// not reorder names that are both stripped and appended.
func TestApplyClaudeHeaders_OperatorBetaOverridesStableAcrossReapplication(t *testing.T) {
	cfg := parseIssue5093Config(t, `upstream:
  claude:
    header-defaults:
      anthropic-beta-strip: ["operator-a-2099-01-01"]
      anthropic-beta-append: ["operator-a-2099-01-01", "operator-b-2099-01-01"]
`)
	const wantSuffix = ",operator-a-2099-01-01,operator-b-2099-01-01"
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)

	cliReq := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeaders(cliReq, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false, nil,
		body, cfg, nil, false); err != nil {
		t.Fatalf("CLI applyClaudeHeaders() error = %v", err)
	}
	if got := cliReq.Header.Get("Anthropic-Beta"); !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("CLI Anthropic-Beta = %q, want suffix %q", got, wantSuffix)
	}

	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", claudeCodeBeta)
	callerReq := newClaudeHeaderTestRequest(t, incoming)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-caller-owned"}}
	if err := applyClaudeHeaders(callerReq, auth, "key-caller-owned", false, nil,
		[]byte(`{"model":"claude-opus-5"}`), cfg, incoming, false); err != nil {
		t.Fatalf("caller-owned applyClaudeHeaders() error = %v", err)
	}
	if got, want := callerReq.Header.Get("Anthropic-Beta"), claudeCodeBeta+wantSuffix; got != want {
		t.Fatalf("caller-owned Anthropic-Beta = %q, want %q", got, want)
	}
}

// Per-credential custom headers are more specific than the global header-defaults
// overrides: they keep the last word on custom gateways, while api.anthropic.com
// still reclaims Anthropic-Beta and applies the overrides there.
func TestApplyClaudeHeaders_CredentialHeadersKeepPrecedenceOverOperatorDefaults(t *testing.T) {
	cfg := parseIssue5093Config(t, `upstream:
  claude:
    header-defaults:
      anthropic-beta-strip: ["credential-beta-2099-01-01"]
      anthropic-beta-append: ["operator-beta-2099-01-01"]
      client-request-id: "off"
`)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":                    "key-credential-headers",
		"header:Anthropic-Beta":      "credential-beta-2099-01-01",
		"header:X-Client-Request-Id": "credential-request-id",
	}}
	for _, tc := range []struct {
		name      string
		url       string
		wantBetas string
	}{
		{name: "custom gateway", url: "https://gateway.example.com/v1/messages", wantBetas: "credential-beta-2099-01-01"},
		{name: "anthropic", url: "https://api.anthropic.com/v1/messages", wantBetas: "operator-beta-2099-01-01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, errNew := http.NewRequest(http.MethodPost, tc.url, nil)
			if errNew != nil {
				t.Fatalf("http.NewRequest() error = %v", errNew)
			}
			if err := applyClaudeHeaders(req, auth, "key-credential-headers", false, nil,
				[]byte(`{"model":"claude-opus-5"}`), cfg, http.Header{}, false); err != nil {
				t.Fatalf("applyClaudeHeaders() error = %v", err)
			}
			if got := req.Header.Get("Anthropic-Beta"); got != tc.wantBetas {
				t.Fatalf("Anthropic-Beta = %q, want %q", got, tc.wantBetas)
			}
			if got := req.Header.Get("X-Client-Request-Id"); got != "credential-request-id" {
				t.Fatalf("X-Client-Request-Id = %q, want credential header value", got)
			}
		})
	}
}

// Strip also filters caller-supplied betas in caller-owned passthrough mode,
// which is the gateway use case referenced by the issue (#4750).
func TestApplyClaudeHeaders_OperatorBetaStripAppliesToCallerOwnedBetas(t *testing.T) {
	cfg := parseIssue5093Config(t, `upstream:
  claude:
    header-defaults:
      anthropic-beta-strip: ["fine-grained-tool-streaming-2025-05-14"]
`)
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", claudeCodeBeta+",fine-grained-tool-streaming-2025-05-14")
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-caller-owned"}}
	req := newClaudeHeaderTestRequest(t, incoming)
	if err := applyClaudeHeaders(req, auth, "key-caller-owned", false, nil,
		[]byte(`{"model":"claude-opus-5"}`), cfg, incoming, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	if got := req.Header.Get("Anthropic-Beta"); got != claudeCodeBeta {
		t.Fatalf("Anthropic-Beta = %q, want %q", got, claudeCodeBeta)
	}
}

// Stripping every derived beta must remove the header instead of sending an
// empty Anthropic-Beta value to api.anthropic.com.
func TestApplyClaudeHeaders_OperatorBetaStripAllRemovesHeader(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	baseline := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeaders(baseline, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false, nil,
		body, nil, nil, false); err != nil {
		t.Fatalf("baseline applyClaudeHeaders() error = %v", err)
	}
	stripAll, errMarshal := json.Marshal(strings.Split(baseline.Header.Get("Anthropic-Beta"), ","))
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	cfg := parseIssue5093Config(t, "upstream: {claude: {header-defaults: {anthropic-beta-strip: "+string(stripAll)+"}}}\n")

	req := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false, nil,
		body, cfg, nil, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	if values, ok := req.Header["Anthropic-Beta"]; ok {
		t.Fatalf("Anthropic-Beta = %q, want header removed", values)
	}
}

// inject-diagnostics=false keeps the cloaked body free of the CPA-injected
// diagnostics field, so cache-diagnosis-2026-04-07 is no longer derived from it.
func TestClaudeExecutor_InjectDiagnosticsDisabledOmitsDiagnosticsAndCacheDiagnosis(t *testing.T) {
	disabled := parseIssue5093Config(t, "upstream: {claude: {header-defaults: {inject-diagnostics: false}}}\n")
	for _, tc := range []struct {
		name   string
		cfg    *config.Config
		stream bool
		inject bool
	}{
		{name: "default execute", cfg: &config.Config{}, stream: false, inject: true},
		{name: "default stream", cfg: &config.Config{}, stream: true, inject: true},
		{name: "disabled execute", cfg: disabled, stream: false, inject: false},
		{name: "disabled stream", cfg: disabled, stream: true, inject: false},
	} {
		cfg, stream := tc.cfg, tc.stream
		var upstreamBody []byte
		var upstreamBetas string
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			body, errRead := io.ReadAll(req.Body)
			if errRead != nil {
				t.Fatal(errRead)
			}
			upstreamBody = body
			upstreamBetas = req.Header.Get("Anthropic-Beta")
			if upstreamBetas == "" {
				upstreamBetas = strings.Join(req.Header["anthropic-beta"], ",")
			}
			if stream {
				sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_5093\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
					"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse)), Request: req}, nil
			}
			response := `{"id":"msg_5093","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
		testID := uuid.NewString()
		auth := &cliproxyauth.Auth{
			ID:         "diagnostics-disabled-" + testID,
			Attributes: map[string]string{"api_key": "sk-ant-oat-diagnostics-disabled", "cloak_mode": "always"},
			Metadata: map[string]any{
				"account_uuid":                        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
				claudeauth.ClaudeDeviceIDsMetadataKey: []string{"0000000000000000000000000000000000000000000000000000000000000000"},
			},
		}
		executor := NewClaudeExecutor(cfg)
		request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"x"}],"max_tokens":16}`)}
		options := cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FormatClaude,
			Stream:       stream,
			Metadata:     map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "diagnostics-disabled-" + testID},
		}
		if stream {
			result, errStream := executor.ExecuteStream(ctx, auth, request, options)
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}
			for range result.Chunks {
			}
		} else if _, errExecute := executor.Execute(ctx, auth, request, options); errExecute != nil {
			t.Fatalf("Execute() error = %v", errExecute)
		}

		if len(upstreamBody) == 0 {
			t.Fatalf("%s: upstream request was not captured", tc.name)
		}
		if got := gjson.GetBytes(upstreamBody, "diagnostics").Exists(); got != tc.inject {
			t.Fatalf("%s: diagnostics present = %v, want %v; body = %s", tc.name, got, tc.inject, upstreamBody)
		}
		if got := strings.Contains(upstreamBetas, claudeCacheDiagnosisBeta); got != tc.inject {
			t.Fatalf("%s: Anthropic-Beta = %q, %s present = %v, want %v", tc.name, upstreamBetas, claudeCacheDiagnosisBeta, got, tc.inject)
		}
	}
	if resolveClaudeFingerprintPolicy(disabled, nil, claudeRaceProbeOAuthKey).InjectDiagnostics {
		t.Fatal("InjectDiagnostics = true, want false when inject-diagnostics is disabled")
	}
}

// client-request-id selects whether first-party requests synthesize a fresh
// x-client-request-id (on, the default), forward only the caller's value
// (passthrough), or never send one (off).
func TestApplyClaudeHeaders_ClientRequestIDModes(t *testing.T) {
	const callerRequestID = "caller-request-id-5093"
	withCallerID := http.Header{}
	withCallerID.Set("X-Client-Request-Id", callerRequestID)
	apiKeyAuth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-caller-owned"}}

	tests := []struct {
		name     string
		mode     string
		auth     *cliproxyauth.Auth
		apiKey   string
		incoming http.Header
		check    func(t *testing.T, got string, present bool)
	}{
		{
			name: "default synthesizes", mode: "", auth: claudeOAuthAuthForBetaPolicy(), apiKey: claudeRaceProbeOAuthKey, incoming: withCallerID,
			check: func(t *testing.T, got string, present bool) {
				if _, errParse := uuid.Parse(got); !present || errParse != nil {
					t.Fatalf("x-client-request-id = %q, want a synthesized UUID", got)
				}
			},
		},
		{
			name: "off omits header", mode: "off", auth: claudeOAuthAuthForBetaPolicy(), apiKey: claudeRaceProbeOAuthKey, incoming: withCallerID,
			check: func(t *testing.T, got string, present bool) {
				if present {
					t.Fatalf("x-client-request-id = %q, want header omitted", got)
				}
			},
		},
		{
			name: "passthrough forwards caller value", mode: "passthrough", auth: claudeOAuthAuthForBetaPolicy(), apiKey: claudeRaceProbeOAuthKey, incoming: withCallerID,
			check: func(t *testing.T, got string, present bool) {
				if !present || got != callerRequestID {
					t.Fatalf("x-client-request-id = %q, want caller value %q", got, callerRequestID)
				}
			},
		},
		{
			name: "passthrough never synthesizes", mode: "passthrough", auth: claudeOAuthAuthForBetaPolicy(), apiKey: claudeRaceProbeOAuthKey, incoming: nil,
			check: func(t *testing.T, got string, present bool) {
				if present {
					t.Fatalf("x-client-request-id = %q, want header omitted without a caller value", got)
				}
			},
		},
		{
			name: "off removes caller-owned value", mode: "off", auth: apiKeyAuth, apiKey: "key-caller-owned", incoming: withCallerID,
			check: func(t *testing.T, got string, present bool) {
				if present {
					t.Fatalf("x-client-request-id = %q, want caller-owned value removed", got)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cfg *config.Config
			if tc.mode != "" {
				cfg = parseIssue5093Config(t, "upstream: {claude: {header-defaults: {client-request-id: "+tc.mode+"}}}\n")
			}
			req := newClaudeHeaderTestRequest(t, tc.incoming)
			if err := applyClaudeHeaders(req, tc.auth, tc.apiKey, false, nil,
				[]byte(`{"model":"claude-opus-5"}`), cfg, tc.incoming, false); err != nil {
				t.Fatalf("applyClaudeHeaders() error = %v", err)
			}
			values, present := req.Header["X-Client-Request-Id"]
			got := strings.Join(values, ",")
			tc.check(t, got, present)
		})
	}
}
