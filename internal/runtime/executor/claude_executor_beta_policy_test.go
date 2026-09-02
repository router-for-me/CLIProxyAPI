package executor

import (
	"bytes"
	"context"
	"errors"
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

const claudeRaceProbeOAuthKey = "sk-ant-oat-beta-policy"

func claudeOAuthAuthForBetaPolicy() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "claude-beta-policy",
		Metadata: map[string]any{"access_token": claudeRaceProbeOAuthKey},
	}
}

// A confirmed native client authenticates to CPA with the user's configured key
// and cannot know CPA will pick an OAuth credential upstream, so its header never
// carries the credential-scoped OAuth and extended-cache betas.
func TestApplyClaudeHeaders_ConfirmedClientKeepsOAuthCredentialBetas(t *testing.T) {
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", claudeCodeBeta+",interleaved-thinking-2025-05-14,"+claudeEffortBeta)

	req := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false, nil,
		[]byte(`{"model":"claude-opus-5"}`), nil, incoming, true); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}

	got := req.Header.Get("Anthropic-Beta")
	parts := strings.Split(got, ",")
	if len(parts) < 2 || parts[0] != claudeCodeBeta || parts[1] != claudeOAuthBeta {
		t.Fatalf("Anthropic-Beta = %q, want %s at position 2", got, claudeOAuthBeta)
	}
	if parts[len(parts)-1] != claudeExtendedCacheTTLBeta {
		t.Fatalf("Anthropic-Beta = %q, want OAuth cache trailer %s", got, claudeExtendedCacheTTLBeta)
	}
	if strings.Contains(got, "advisor-tool-2026-03-01") {
		t.Fatalf("Anthropic-Beta = %q, contains unrequested advisor tool beta", got)
	}
	if strings.Contains(got, claudeCacheDiagnosisBeta) {
		t.Fatalf("Anthropic-Beta = %q, contains %s without a diagnostics body", got, claudeCacheDiagnosisBeta)
	}
	// The caller's own betas survive the restoration.
	for _, want := range []string{"interleaved-thinking-2025-05-14", claudeEffortBeta} {
		if !strings.Contains(got, want) {
			t.Fatalf("Anthropic-Beta = %q, want caller beta %s preserved", got, want)
		}
	}
}

func TestApplyClaudeHeaders_ConfirmedAPIKeyClientKeepsPurePassthrough(t *testing.T) {
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", claudeCodeBeta+","+claudeEffortBeta)

	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-passthrough"}}
	req := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeaders(req, auth, "key-passthrough", false, nil,
		[]byte(`{"model":"claude-opus-5"}`), nil, incoming, true); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	if got, want := req.Header.Get("Anthropic-Beta"), claudeCodeBeta+","+claudeEffortBeta; got != want {
		t.Fatalf("Anthropic-Beta = %q, want untouched passthrough %q", got, want)
	}
}

// Default API-key mode preserves body-lifted betas just like header betas.
func TestApplyClaudeHeaders_UnknownBodyBetaPreservedOnAnthropic(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-body-beta"}}
	req := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeaders(req, auth, "key-body-beta", false, []string{"unknown-body-probe-2099-01-01"},
		[]byte(`{"model":"claude-opus-5"}`), nil, nil, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	if got := req.Header.Get("Anthropic-Beta"); got != "unknown-body-probe-2099-01-01" {
		t.Fatalf("Anthropic-Beta = %q, want the caller body beta preserved", got)
	}
}

func TestApplyClaudeHeaders_KnownBodyBetaStillPlacedOnAnthropic(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-known-body-beta"}}
	req := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeaders(req, auth, "key-known-body-beta", false, []string{claudeContext1MBeta},
		[]byte(`{"model":"claude-opus-5"}`), nil, nil, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	if got := req.Header.Get("Anthropic-Beta"); got != claudeContext1MBeta {
		t.Fatalf("Anthropic-Beta = %q, want caller body beta %s", got, claudeContext1MBeta)
	}
}

// Custom credential headers run after the whole header set is assembled, so they
// could rewrite the reconstructed identity on Anthropic itself.
func TestApplyClaudeHeaders_CustomHeadersCannotOverrideAnthropicIdentity(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":                "key-custom-headers",
		"header:Anthropic-Beta":  "attacker-controlled-2099-01-01",
		"header:Accept-Encoding": "identity",
	}}

	for _, stream := range []bool{false, true} {
		req := newClaudeHeaderTestRequest(t, nil)
		if err := applyClaudeHeaders(req, auth, "key-custom-headers", stream, nil,
			[]byte(`{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":2048,"block_binding":{}}}`), nil, nil, false); err != nil {
			t.Fatalf("applyClaudeHeaders(stream=%v) error = %v", stream, err)
		}
		if got := req.Header.Get("Anthropic-Beta"); got == "attacker-controlled-2099-01-01" {
			t.Fatalf("stream=%v: custom header overrode Anthropic-Beta", stream)
		}
		if tokens := countBetaTokens(req.Header.Get("Anthropic-Beta")); tokens[strings.ToLower(claudeThinkingBindingControlsBeta)] != 1 {
			t.Fatalf("stream=%v: Anthropic-Beta = %q, want %s preserved after custom header check", stream, req.Header.Get("Anthropic-Beta"), claudeThinkingBindingControlsBeta)
		}
		if got := req.Header.Get("Accept-Encoding"); got != "gzip, deflate, br, zstd" {
			t.Fatalf("stream=%v: Accept-Encoding = %q, want the negotiated transport", stream, got)
		}
	}
}

// Kimi rewrites base_url to api.kimi.com and custom gateways set their own host,
// yet both delegate to ClaudeExecutor and are therefore cloaked. Keying the
// context_management injection on the cloaked flag alone leaked a Claude Code
// field into their traffic.
func TestClaudeExecutor_ContextManagementNeverLeaksToOtherUpstreams(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "claude-non-anthropic-upstream",
		Attributes: map[string]string{"api_key": "sk-ant-oat-non-anthropic", "base_url": server.URL},
		Metadata:   claudeOAuthTestMetadata(),
	}
	payload := []byte(`{"model":"claude-opus-5","system":"p","messages":[{"role":"user","content":"hi"}]}`)

	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(upstreamBody, "context_management"); got.Exists() {
		t.Fatalf("non-Anthropic upstream received context_management = %s", got.Raw)
	}
}

func TestIsAnthropicUpstreamBase(t *testing.T) {
	cases := map[string]bool{
		"https://api.anthropic.com":      true,
		"https://API.Anthropic.com":      true,
		"https://api.anthropic.com:443":  true,
		"https://api.anthropic.com:8443": false,
		"https://user@api.anthropic.com": false,
		"https://api.kimi.com":           false,
		"http://api.anthropic.com":       false,
		"https://api.anthropic.com.evil": false,
		"https://gateway.example.com":    false,
		"":                               false,
	}
	for base, want := range cases {
		if got := isAnthropicUpstreamBase(base); got != want {
			t.Fatalf("isAnthropicUpstreamBase(%q) = %v, want %v", base, got, want)
		}
	}
}

// Streaming previously never reached the fast-mode derivation, so speed:"fast"
// produced a 400 on every streamed request.
func TestApplyClaudeHeaders_FastModeBetaMatchesAcrossStreamModes(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-fast-parity"}}
	body := []byte(`{"model":"claude-opus-5","speed":"fast"}`)

	var seen []string
	for _, stream := range []bool{false, true} {
		req := newClaudeHeaderTestRequest(t, nil)
		if err := applyClaudeHeaders(req, auth, "key-fast-parity", stream, nil, body, nil, nil, false); err != nil {
			t.Fatalf("applyClaudeHeaders(stream=%v) error = %v", stream, err)
		}
		got := req.Header.Get("Anthropic-Beta")
		if !strings.Contains(got, claudeFastModeBeta) {
			t.Fatalf("stream=%v: Anthropic-Beta = %q, want %s", stream, got, claudeFastModeBeta)
		}
		seen = append(seen, got)
	}
	if seen[0] != seen[1] {
		t.Fatalf("stream and non-stream disagree:\n non-stream %q\n stream     %q", seen[0], seen[1])
	}
}

// The current OAuth CLI profile places fast-mode immediately before the
// extended-cache-ttl trailer.
func TestApplyClaudeHeaders_FastModePrecedesOAuthTrailer(t *testing.T) {
	req := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, true, nil,
		[]byte(`{"model":"claude-opus-5","speed":"fast"}`), nil, nil, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	got := req.Header.Get("Anthropic-Beta")
	parts := strings.Split(got, ",")
	if parts[len(parts)-1] != claudeExtendedCacheTTLBeta {
		t.Fatalf("Anthropic-Beta = %q, want %s last", got, claudeExtendedCacheTTLBeta)
	}
	if parts[len(parts)-2] != claudeFastModeBeta {
		t.Fatalf("Anthropic-Beta = %q, want %s before the OAuth cache trailer", got, claudeFastModeBeta)
	}
	if strings.Contains(got, claudeCacheDiagnosisBeta) {
		t.Fatalf("Anthropic-Beta = %q, contains %s without a diagnostics body", got, claudeCacheDiagnosisBeta)
	}
}

func TestApplyClaudeHeaders_DiagnosticsBetaFollowsBodyInNativeOrder(t *testing.T) {
	for _, stream := range []bool{false, true} {
		req := newClaudeHeaderTestRequest(t, nil)
		body := []byte(`{"model":"claude-opus-5","diagnostics":{"previous_message_id":null}}`)
		if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, stream, nil,
			body, nil, nil, false); err != nil {
			t.Fatalf("applyClaudeHeaders(stream=%v) error = %v", stream, err)
		}
		got := req.Header.Get("Anthropic-Beta")
		wantTrailer := claudeExtendedCacheTTLBeta + "," + claudeCacheDiagnosisBeta
		if !strings.HasSuffix(got, wantTrailer) {
			t.Fatalf("stream=%v: Anthropic-Beta = %q, want native diagnostics trailer %q", stream, got, wantTrailer)
		}
	}
}

// Anthropic refuses a fast-mode request from an account without the matching
// usage credits with 429 rate_limit_error. The generic pipeline reads 429 as
// quota exhaustion, cools the credential down and rotates, so one speed:"fast"
// request would walk the whole Claude pool and disable credentials that are
// perfectly healthy for ordinary traffic.
func TestClassifyClaudeUpstreamError_FastModeCreditsIsRequestScoped(t *testing.T) {
	// Anthropic and the Claude Code CLI word this refusal differently; both must
	// be recognised, and neither may be rewritten on the way back to the caller.
	bodies := [][]byte{
		[]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for fast mode."}}`),
		[]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Fast mode requires usage credits"}}`),
	}
	for _, body := range bodies {
		err := classifyClaudeUpstreamError(http.StatusTooManyRequests, nil, body)

		scoped, ok := err.(cliproxyexecutor.RequestScopedError)
		if !ok || !scoped.IsRequestScoped() {
			t.Fatalf("fast-mode credit refusal = %T, want a request-scoped error: %s", err, body)
		}
		var status cliproxyexecutor.StatusError
		if !errors.As(err, &status) || status.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("status was not preserved for the caller: %v", err)
		}
		// Pass-through must be byte-exact: the upstream body is the caller's
		// only explanation of what to do about it.
		if err.Error() != string(body) {
			t.Fatalf("body was rewritten:\n got  %s\n want %s", err.Error(), body)
		}
	}
}

// A genuine rate limit must keep cooling the credential down and rotating.
func TestClassifyClaudeUpstreamError_RealRateLimitStaysCredentialScoped(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Number of requests has exceeded your rate limit."}}`),
		[]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"This organization has exceeded its usage limit."}}`),
	}
	for _, body := range cases {
		err := classifyClaudeUpstreamError(http.StatusTooManyRequests, nil, body)
		if scoped, ok := err.(cliproxyexecutor.RequestScopedError); ok && scoped.IsRequestScoped() {
			t.Fatalf("genuine rate limit was misclassified as request-scoped: %s", body)
		}
	}
}

func TestClassifyClaudeUpstreamError_OtherStatusesUnaffected(t *testing.T) {
	body := []byte(`{"error":{"message":"Usage credits are required for fast mode."}}`)
	// Only 429 carries the entitlement refusal; a 500 mentioning it is still a
	// credential-scoped failure worth rotating away from.
	err := classifyClaudeUpstreamError(http.StatusInternalServerError, nil, body)
	if scoped, ok := err.(cliproxyexecutor.RequestScopedError); ok && scoped.IsRequestScoped() {
		t.Fatal("non-429 status was misclassified as request-scoped")
	}
}

func TestApplyClaudeHeaders_AdvisorToolBetaPreservedWhenRequested(t *testing.T) {
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", "claude-code-20250219,context-1m-2025-08-07,interleaved-thinking-2025-05-14,mid-conversation-system-2026-04-07,advisor-tool-2026-03-01,advanced-tool-use-2025-11-20,effort-2025-11-24")

	body := []byte(`{"model":"claude-opus-5","tools":[{"type":"advisor_20260301","name":"advisor"}]}`)

	for _, stream := range []bool{false, true} {
		req := newClaudeHeaderTestRequest(t, nil)
		if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, stream, nil,
			body, nil, incoming, false); err != nil {
			t.Fatalf("applyClaudeHeaders(stream=%v) error = %v", stream, err)
		}

		got := req.Header.Get("Anthropic-Beta")
		if !strings.Contains(got, "advisor-tool-2026-03-01") {
			t.Fatalf("stream=%v: Anthropic-Beta = %q, want advisor-tool-2026-03-01 preserved", stream, got)
		}

		parts := strings.Split(got, ",")
		advIdx := -1
		midIdx := -1
		toolIdx := -1
		for i, part := range parts {
			switch strings.TrimSpace(part) {
			case "advisor-tool-2026-03-01":
				advIdx = i
			case "mid-conversation-system-2026-04-07":
				midIdx = i
			case "advanced-tool-use-2025-11-20":
				toolIdx = i
			}
		}

		if advIdx == -1 {
			t.Fatalf("stream=%v: advisor-tool-2026-03-01 missing from %q", stream, got)
		}
		if midIdx != -1 && advIdx < midIdx {
			t.Fatalf("stream=%v: advisor-tool at %d should follow mid-conversation-system at %d in %q", stream, advIdx, midIdx, got)
		}
		if toolIdx != -1 && advIdx > toolIdx {
			t.Fatalf("stream=%v: advisor-tool at %d should precede advanced-tool-use at %d in %q", stream, advIdx, toolIdx, got)
		}
	}
}

func TestApplyClaudeHeaders_AdvisorToolBetaInjectedWhenBodyHasTool_ConfirmedClient(t *testing.T) {
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", "claude-code-20250219,interleaved-thinking-2025-05-14,mid-conversation-system-2026-04-07,advanced-tool-use-2025-11-20,effort-2025-11-24")

	body := []byte(`{"model":"claude-opus-5","tools":[{"type":"advisor_20260301","name":"advisor"}]}`)

	for _, stream := range []bool{false, true} {
		req := newClaudeHeaderTestRequest(t, nil)
		if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, stream, nil,
			body, nil, incoming, true); err != nil {
			t.Fatalf("applyClaudeHeaders(stream=%v) error = %v", stream, err)
		}

		got := req.Header.Get("Anthropic-Beta")
		if !strings.Contains(got, "advisor-tool-2026-03-01") {
			t.Fatalf("stream=%v: Anthropic-Beta = %q, want advisor-tool-2026-03-01 injected for confirmed client", stream, got)
		}

		parts := strings.Split(got, ",")
		advIdx := -1
		midIdx := -1
		toolIdx := -1
		for i, part := range parts {
			switch strings.TrimSpace(part) {
			case "advisor-tool-2026-03-01":
				advIdx = i
			case "mid-conversation-system-2026-04-07":
				midIdx = i
			case "advanced-tool-use-2025-11-20":
				toolIdx = i
			}
		}

		if advIdx == -1 {
			t.Fatalf("stream=%v: advisor-tool-2026-03-01 missing from %q", stream, got)
		}
		if midIdx != -1 && advIdx < midIdx {
			t.Fatalf("stream=%v: advisor-tool at %d should follow mid-conversation-system at %d in %q", stream, advIdx, midIdx, got)
		}
		if toolIdx != -1 && advIdx > toolIdx {
			t.Fatalf("stream=%v: advisor-tool at %d should precede advanced-tool-use at %d in %q", stream, advIdx, toolIdx, got)
		}
	}
}

func TestApplyClaudeHeaders_AdvisorToolBetaInjectedWhenBodyHasTool_APIKeyPassthrough(t *testing.T) {
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", claudeCodeBeta+",mid-conversation-system-2026-04-07,advanced-tool-use-2025-11-20,"+claudeEffortBeta)

	body := []byte(`{"model":"claude-opus-5","tools":[{"type":"advisor_20260301","name":"advisor"}]}`)

	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-passthrough"}}
	for _, stream := range []bool{false, true} {
		req := newClaudeHeaderTestRequest(t, nil)
		if err := applyClaudeHeaders(req, auth, "key-passthrough", stream, nil,
			body, nil, incoming, false); err != nil {
			t.Fatalf("applyClaudeHeaders(stream=%v) error = %v", stream, err)
		}

		got := req.Header.Get("Anthropic-Beta")
		if !strings.Contains(got, "advisor-tool-2026-03-01") {
			t.Fatalf("stream=%v: Anthropic-Beta = %q, want advisor-tool-2026-03-01 injected in API key passthrough", stream, got)
		}

		parts := strings.Split(got, ",")
		advIdx := -1
		midIdx := -1
		toolIdx := -1
		for i, part := range parts {
			switch strings.TrimSpace(part) {
			case "advisor-tool-2026-03-01":
				advIdx = i
			case "mid-conversation-system-2026-04-07":
				midIdx = i
			case "advanced-tool-use-2025-11-20":
				toolIdx = i
			}
		}
		if advIdx == -1 || (midIdx != -1 && advIdx < midIdx) || (toolIdx != -1 && advIdx > toolIdx) {
			t.Fatalf("stream=%v: Anthropic-Beta = %q, want advisor-tool-2026-03-01 between mid-conversation-system and advanced-tool-use", stream, got)
		}
	}
}

func TestApplyClaudeHeaders_AdvisorToolBetaPreservedWhenLiftedFromBodyBetas_ConfirmedClient(t *testing.T) {
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", claudeCodeBeta+",interleaved-thinking-2025-05-14,"+claudeEffortBeta)

	body := []byte(`{"model":"claude-opus-5"}`)
	extraBetas := []string{"advisor-tool-2026-03-01"}

	for _, stream := range []bool{false, true} {
		req := newClaudeHeaderTestRequest(t, nil)
		if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, stream, extraBetas,
			body, nil, incoming, true); err != nil {
			t.Fatalf("applyClaudeHeaders(stream=%v) error = %v", stream, err)
		}

		got := req.Header.Get("Anthropic-Beta")
		if !strings.Contains(got, "advisor-tool-2026-03-01") {
			t.Fatalf("stream=%v: Anthropic-Beta = %q, want body-lifted advisor-tool-2026-03-01 preserved for confirmed client", stream, got)
		}
	}
}

func TestApplyClaudeHeaders_AdvisorToolBetaPreservedWhenCountTokens(t *testing.T) {
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,token-counting-2024-11-01")

	body := []byte(`{"model":"claude-opus-5","tools":[{"type":"advisor_20260301","name":"advisor"}]}`)

	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages/count_tokens", bytes.NewReader(body))
	if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false, nil,
		body, nil, incoming, false); err != nil {
		t.Fatalf("applyClaudeHeaders(count_tokens) error = %v", err)
	}

	got := req.Header.Get("Anthropic-Beta")
	if !strings.Contains(got, "advisor-tool-2026-03-01") {
		t.Fatalf("count_tokens Anthropic-Beta = %q, want advisor-tool-2026-03-01 preserved when advisor tools declared", got)
	}
}

func TestApplyClaudeHeaders_AdvisorToolBetaRepositionedWhenOutOfOrder(t *testing.T) {
	// Incoming header has advisor-tool at trailing position after effort
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", "claude-code-20250219,mid-conversation-system-2026-04-07,advanced-tool-use-2025-11-20,effort-2025-11-24,advisor-tool-2026-03-01")

	body := []byte(`{"model":"claude-opus-5","tools":[{"type":"advisor_20260301","name":"advisor"}]}`)

	for _, confirmed := range []bool{false, true} {
		req := newClaudeHeaderTestRequest(t, nil)
		if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false, nil,
			body, nil, incoming, confirmed); err != nil {
			t.Fatalf("confirmed=%v: applyClaudeHeaders() error = %v", confirmed, err)
		}

		got := req.Header.Get("Anthropic-Beta")
		parts := strings.Split(got, ",")
		advIdx := -1
		midIdx := -1
		toolIdx := -1
		for i, part := range parts {
			switch strings.TrimSpace(part) {
			case "advisor-tool-2026-03-01":
				advIdx = i
			case "mid-conversation-system-2026-04-07":
				midIdx = i
			case "advanced-tool-use-2025-11-20":
				toolIdx = i
			}
		}

		if advIdx == -1 {
			t.Fatalf("confirmed=%v: advisor-tool missing from %q", confirmed, got)
		}
		if midIdx != -1 && advIdx < midIdx {
			t.Fatalf("confirmed=%v: advisor-tool at %d should follow mid-conversation-system at %d in %q", confirmed, advIdx, midIdx, got)
		}
		if toolIdx != -1 && advIdx > toolIdx {
			t.Fatalf("confirmed=%v: advisor-tool at %d should precede advanced-tool-use at %d in %q", confirmed, advIdx, toolIdx, got)
		}
	}
}

func TestApplyClaudeHeaders_StructuredHelperBetaOrderPreservedWithAdvisor(t *testing.T) {
	// Exact beta profile from measured structured Haiku helper with advisor
	helperBeta := claudeCodeBeta + ",oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advisor-tool-2026-03-01,structured-outputs-2025-12-15,cache-diagnosis-2026-04-07"
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", helperBeta)

	body := []byte(`{"model":"claude-haiku-4-5-20251001","tools":[]}`)

	req := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeadersWithNativeProfile(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false, nil,
		body, nil, incoming, true, true); err != nil {
		t.Fatalf("applyClaudeHeadersWithNativeProfile() error = %v", err)
	}

	got := req.Header.Get("Anthropic-Beta")
	if got != helperBeta {
		t.Fatalf("helper Anthropic-Beta =\n got:  %q\n want: %q", got, helperBeta)
	}
}

func TestParseAnthropicBetasFromHeader_DeterministicOrder(t *testing.T) {
	incoming := http.Header{
		"x-other":        []string{"foo"},
		"anthropic-beta": []string{"beta-lower-1, beta-lower-2", "beta-lower-3"},
		"Anthropic-Beta": []string{"beta-canonical-1", "beta-canonical-2, beta-canonical-3"},
		"ANTHROPIC-BETA": []string{"beta-upper-1", "beta-upper-2"},
	}
	want := []string{
		"beta-upper-1", "beta-upper-2",
		"beta-canonical-1", "beta-canonical-2", "beta-canonical-3",
		"beta-lower-1", "beta-lower-2", "beta-lower-3",
	}
	got := parseAnthropicBetasFromHeader(incoming)
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if parseAnthropicBetasFromHeader(nil) != nil {
		t.Fatal("nil headers must yield no betas")
	}
	if parseAnthropicBetasFromHeader(http.Header{"Anthropic-Beta": []string{" , "}}) != nil {
		t.Fatal("blank CSV items must be dropped")
	}
}

func countBetaTokens(headerValue string) map[string]int {
	counts := make(map[string]int)
	for _, part := range strings.Split(headerValue, ",") {
		if token := strings.TrimSpace(part); token != "" {
			counts[strings.ToLower(token)]++
		}
	}
	return counts
}

// claudeBetaOccurrences counts the tokens of headerValue that match beta
// case-insensitively, and how many of those use beta's canonical spelling.
func claudeBetaOccurrences(headerValue, beta string) (matches int, canonical int) {
	for _, part := range strings.Split(headerValue, ",") {
		token := strings.TrimSpace(part)
		if token == "" || !strings.EqualFold(token, beta) {
			continue
		}
		matches++
		if token == beta {
			canonical++
		}
	}
	return matches, canonical
}

func assertCanonicalBetaOnce(t *testing.T, headerValue, beta string) {
	t.Helper()
	matches, canonical := claudeBetaOccurrences(headerValue, beta)
	if matches != 1 || canonical != 1 {
		t.Fatalf("Anthropic-Beta = %q, want %s exactly once in canonical spelling (matches=%d canonical=%d)",
			headerValue, beta, matches, canonical)
	}
}

func assertBetaAbsent(t *testing.T, headerValue, beta string) {
	t.Helper()
	if matches, _ := claudeBetaOccurrences(headerValue, beta); matches != 0 {
		t.Fatalf("Anthropic-Beta = %q, want no %s", headerValue, beta)
	}
}

func TestClaudeBodyHasBlockBinding(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"enabled with empty binding object", `{"thinking":{"type":"enabled","budget_tokens":2048,"block_binding":{}}}`, true},
		{"adaptive with populated binding", `{"thinking":{"type":"adaptive","block_binding":{"strategy":"auto"}}}`, true},
		{"binding without an explicit type", `{"thinking":{"block_binding":{"strategy":"auto"}}}`, true},
		{"disabled outranks the binding", `{"thinking":{"type":"disabled","block_binding":{"strategy":"auto"}}}`, false},
		{"disabled is matched case-insensitively", `{"thinking":{"type":"DISABLED","block_binding":{}}}`, false},
		{"disabled with surrounding spaces", `{"thinking":{"type":" disabled ","block_binding":{}}}`, false},
		{"binding null", `{"thinking":{"type":"enabled","block_binding":null}}`, false},
		{"binding boolean", `{"thinking":{"type":"enabled","block_binding":true}}`, false},
		{"binding string", `{"thinking":{"type":"enabled","block_binding":"auto"}}`, false},
		{"binding array", `{"thinking":{"type":"enabled","block_binding":[]}}`, false},
		{"no binding", `{"thinking":{"type":"enabled","budget_tokens":2048}}`, false},
		{"no thinking block", `{"model":"claude-opus-5"}`, false},
		{"thinking is not an object", `{"thinking":"enabled"}`, false},
		{"empty body", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeBodyHasBlockBinding([]byte(tc.body)); got != tc.want {
				t.Fatalf("claudeBodyHasBlockBinding(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestCanonicalizeClaudeBetaToken(t *testing.T) {
	cases := []struct {
		name      string
		betas     string
		canonical string
		want      string
	}{
		{"rewrites in place", "a,THINKING-BINDING-CONTROLS-2026-08-01,b", claudeThinkingBindingControlsBeta, "a," + claudeThinkingBindingControlsBeta + ",b"},
		{"leaves an exact match alone", "a," + claudeThinkingBindingControlsBeta, claudeThinkingBindingControlsBeta, "a," + claudeThinkingBindingControlsBeta},
		{"leaves unrelated tokens alone", "a,b", claudeThinkingBindingControlsBeta, "a,b"},
		{"empty list", "", claudeThinkingBindingControlsBeta, ""},
		// Exactly one occurrence survives, at the first matching position.
		{
			"collapses exact duplicates",
			claudeThinkingBindingControlsBeta + ",a," + claudeThinkingBindingControlsBeta,
			claudeThinkingBindingControlsBeta,
			claudeThinkingBindingControlsBeta + ",a",
		},
		{
			"collapses mixed variants onto the first position",
			"a,THINKING-BINDING-CONTROLS-2026-08-01,b," + claudeThinkingBindingControlsBeta,
			claudeThinkingBindingControlsBeta,
			"a," + claudeThinkingBindingControlsBeta + ",b",
		},
		{
			"keeps the first canonical occurrence and drops later variants",
			"a," + claudeThinkingBindingControlsBeta + ",b,Thinking-Binding-Controls-2026-08-01",
			claudeThinkingBindingControlsBeta,
			"a," + claudeThinkingBindingControlsBeta + ",b",
		},
		{
			"three occurrences collapse to one",
			"THINKING-BINDING-CONTROLS-2026-08-01,a," + claudeThinkingBindingControlsBeta + ",b,Thinking-Binding-Controls-2026-08-01",
			claudeThinkingBindingControlsBeta,
			claudeThinkingBindingControlsBeta + ",a,b",
		},
		{
			"surrounding whitespace does not hide a duplicate",
			"a, THINKING-BINDING-CONTROLS-2026-08-01 ,b, " + claudeThinkingBindingControlsBeta,
			claudeThinkingBindingControlsBeta,
			"a," + claudeThinkingBindingControlsBeta + ",b",
		},
		{
			"duplicates of another owned token",
			claudeFastModeBeta + ",a,FAST-MODE-2026-02-01",
			claudeFastModeBeta,
			claudeFastModeBeta + ",a",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canonicalizeClaudeBetaToken(tc.betas, tc.canonical); got != tc.want {
				t.Fatalf("canonicalizeClaudeBetaToken(%q, %q) = %q, want %q", tc.betas, tc.canonical, got, tc.want)
			}
		})
	}
}

func TestApplyClaudeHeaders_ThinkingBlockBindingInjectsBeta_EnabledAndAdaptive(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{"enabled", []byte(`{"model":"claude-sonnet-5","thinking":{"type":"enabled","budget_tokens":2048,"block_binding":{}}}`)},
		{"adaptive", []byte(`{"model":"claude-opus-5","thinking":{"type":"adaptive","block_binding":{"strategy":"auto"}}}`)},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			req := newClaudeHeaderTestRequest(t, nil)
			if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, stream, nil, tc.body, nil, nil, false); err != nil {
				t.Fatalf("%s stream=%v: %v", tc.name, stream, err)
			}
			assertCanonicalBetaOnce(t, req.Header.Get("Anthropic-Beta"), claudeThinkingBindingControlsBeta)
		}
	}
}

func TestApplyClaudeHeaders_ThinkingBlockBindingAbsentWithoutBlockBinding(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"model":"claude-sonnet-5","thinking":{"type":"enabled","budget_tokens":2048}}`),
		[]byte(`{"model":"claude-sonnet-5","thinking":{"type":"enabled","budget_tokens":2048,"block_binding":null}}`),
		[]byte(`{"model":"claude-sonnet-5","thinking":{"type":"enabled","budget_tokens":2048,"block_binding":"auto"}}`),
		[]byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hello"}]}`),
	}
	for _, body := range cases {
		req := newClaudeHeaderTestRequest(t, nil)
		if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false, nil, body, nil, nil, false); err != nil {
			t.Fatalf("applyClaudeHeaders error = %v", err)
		}
		assertBetaAbsent(t, req.Header.Get("Anthropic-Beta"), claudeThinkingBindingControlsBeta)
	}
}

// thinking.type=disabled turns extended thinking off upstream, so a block_binding
// left beside it configures nothing and must not pull the beta in.
func TestApplyClaudeHeaders_ThinkingBlockBindingAbsentWhenThinkingDisabled(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{"model":"claude-opus-5","thinking":{"type":"disabled","block_binding":{"strategy":"auto"}}}`),
		[]byte(`{"model":"claude-opus-5","thinking":{"type":"DISABLED","block_binding":{}}}`),
	}
	for _, body := range bodies {
		for _, stream := range []bool{false, true} {
			req := newClaudeHeaderTestRequest(t, nil)
			if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, stream, nil, body, nil, nil, false); err != nil {
				t.Fatalf("applyClaudeHeaders(stream=%v) error = %v", stream, err)
			}
			assertBetaAbsent(t, req.Header.Get("Anthropic-Beta"), claudeThinkingBindingControlsBeta)
		}
	}
}

// Beta dedup is case-insensitive, so a caller casing variant would otherwise
// swallow the append and ship a spelling api.anthropic.com does not recognise.
// The canonical token has to replace the variant in place: same position, same
// count, canonical spelling.
func TestApplyClaudeHeaders_ThinkingBlockBindingCanonicalizesCallerCasing(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":2048,"block_binding":{}}}`)
	upperVariant := strings.ToUpper(claudeThinkingBindingControlsBeta)

	cases := []struct {
		name                string
		incoming            string
		confirmedClaudeCode bool
		want                string
	}{
		{
			name:     "caller owned passthrough, sole token",
			incoming: upperVariant,
			want:     claudeThinkingBindingControlsBeta,
		},
		{
			name:     "caller owned passthrough keeps the caller position",
			incoming: claudeCodeBeta + ",Thinking-Binding-Controls-2026-08-01," + claudeEffortBeta,
			want:     claudeCodeBeta + "," + claudeThinkingBindingControlsBeta + "," + claudeEffortBeta,
		},
		{
			name:                "confirmed native client",
			incoming:            claudeCodeBeta + "," + upperVariant,
			confirmedClaudeCode: true,
			want:                claudeCodeBeta + "," + claudeThinkingBindingControlsBeta,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			incoming := http.Header{}
			incoming.Set("Anthropic-Beta", tc.incoming)
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-binding-casing"}}
			req := newClaudeHeaderTestRequest(t, incoming)
			if err := applyClaudeHeaders(req, auth, "key-binding-casing", false, nil, body, nil, incoming, tc.confirmedClaudeCode); err != nil {
				t.Fatalf("applyClaudeHeaders() error = %v", err)
			}
			got := req.Header.Get("Anthropic-Beta")
			if got != tc.want {
				t.Fatalf("Anthropic-Beta = %q, want %q", got, tc.want)
			}
			assertCanonicalBetaOnce(t, got, claudeThinkingBindingControlsBeta)
		})
	}
}

// A caller sending the token more than once - identically or in mixed spellings,
// in one CSV value or across repeated header values - must not put it on the wire
// twice. The first position wins, canonically spelled, and every later occurrence
// disappears.
func TestApplyClaudeHeaders_ThinkingBindingBetaCollapsesCallerDuplicates(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":2048,"block_binding":{}}}`)
	upperVariant := strings.ToUpper(claudeThinkingBindingControlsBeta)
	mixedVariant := "Thinking-Binding-Controls-2026-08-01"

	cases := []struct {
		name                string
		incoming            []string
		confirmedClaudeCode bool
		want                string
	}{
		{
			name:     "exact duplicates in one caller value",
			incoming: []string{claudeThinkingBindingControlsBeta + "," + claudeCodeBeta + "," + claudeThinkingBindingControlsBeta},
			want:     claudeThinkingBindingControlsBeta + "," + claudeCodeBeta,
		},
		{
			name:     "mixed variants in one caller value",
			incoming: []string{upperVariant + "," + claudeEffortBeta + "," + mixedVariant + "," + claudeThinkingBindingControlsBeta},
			want:     claudeThinkingBindingControlsBeta + "," + claudeEffortBeta,
		},
		{
			name:     "duplicates across repeated header values",
			incoming: []string{claudeCodeBeta + "," + upperVariant, mixedVariant, claudeThinkingBindingControlsBeta},
			want:     claudeCodeBeta + "," + claudeThinkingBindingControlsBeta,
		},
		{
			name:                "mixed variants for a confirmed native client",
			incoming:            []string{claudeCodeBeta + "," + upperVariant + "," + claudeEffortBeta + "," + mixedVariant},
			confirmedClaudeCode: true,
			want:                claudeCodeBeta + "," + claudeThinkingBindingControlsBeta + "," + claudeEffortBeta,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			incoming := http.Header{}
			for _, value := range tc.incoming {
				incoming.Add("Anthropic-Beta", value)
			}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-binding-duplicates"}}
			req := newClaudeHeaderTestRequest(t, incoming)
			if err := applyClaudeHeaders(req, auth, "key-binding-duplicates", false, nil, body, nil, incoming, tc.confirmedClaudeCode); err != nil {
				t.Fatalf("applyClaudeHeaders() error = %v", err)
			}
			got := req.Header.Get("Anthropic-Beta")
			if got != tc.want {
				t.Fatalf("Anthropic-Beta = %q, want %q", got, tc.want)
			}
			assertCanonicalBetaOnce(t, got, claudeThinkingBindingControlsBeta)
		})
	}
}

// Same contract for the other beta whose spelling CPA owns.
func TestApplyClaudeHeaders_FastModeBetaCollapsesCallerDuplicates(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","speed":"fast"}`)
	for _, incomingValue := range []string{
		claudeFastModeBeta + "," + claudeFastModeBeta,
		strings.ToUpper(claudeFastModeBeta) + "," + claudeEffortBeta + "," + claudeFastModeBeta,
	} {
		t.Run(incomingValue, func(t *testing.T) {
			incoming := http.Header{}
			incoming.Set("Anthropic-Beta", incomingValue)
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-fast-duplicates"}}

			req := newClaudeHeaderTestRequest(t, incoming)
			if err := applyClaudeHeaders(req, auth, "key-fast-duplicates", false, nil, body, nil, incoming, false); err != nil {
				t.Fatalf("applyClaudeHeaders() error = %v", err)
			}
			assertCanonicalBetaOnce(t, req.Header.Get("Anthropic-Beta"), claudeFastModeBeta)
		})
	}
}

// No wire capture pins where a native client would place the binding beta, so its
// position is CPA's own decision. The policy is the minimal one: append at the end
// of the assembled baseline and never move a beta that is already there. Comparing
// the same request with and without thinking.block_binding pins exactly that: one
// token added at the end, every historical position byte-identical.
func TestApplyClaudeHeaders_ThinkingBindingBetaAppendedAfterExistingBetas(t *testing.T) {
	withBinding := []byte(`{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":2048,"block_binding":{}}}`)
	withoutBinding := []byte(`{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":2048}}`)

	betasFor := func(t *testing.T, body []byte, auth *cliproxyauth.Auth, apiKey string, incoming http.Header, confirmed bool) string {
		t.Helper()
		req := newClaudeHeaderTestRequest(t, incoming)
		if err := applyClaudeHeaders(req, auth, apiKey, false, nil, body, nil, incoming, confirmed); err != nil {
			t.Fatalf("applyClaudeHeaders() error = %v", err)
		}
		return req.Header.Get("Anthropic-Beta")
	}
	assertAppended := func(t *testing.T, baseline, got string) {
		t.Helper()
		if baseline == "" {
			t.Fatal("baseline is empty: the ordering assertion would be vacuous")
		}
		assertBetaAbsent(t, baseline, claudeThinkingBindingControlsBeta)
		if want := baseline + "," + claudeThinkingBindingControlsBeta; got != want {
			t.Fatalf("Anthropic-Beta =\n got:  %q\n want: %q", got, want)
		}
		assertCanonicalBetaOnce(t, got, claudeThinkingBindingControlsBeta)
	}

	t.Run("cloaked cli baseline", func(t *testing.T) {
		auth, key := claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey
		baseline := betasFor(t, withoutBinding, auth, key, nil, false)
		// The full measured baseline stays intact, credential trailer included.
		if !strings.HasPrefix(baseline, claudeCodeBeta+","+claudeOAuthBeta+",") ||
			!strings.HasSuffix(baseline, ","+claudeExtendedCacheTTLBeta) {
			t.Fatalf("baseline = %q, want the measured CLI list", baseline)
		}
		assertAppended(t, baseline, betasFor(t, withBinding, auth, key, nil, false))
	})

	t.Run("caller owned passthrough keeps the caller list untouched", func(t *testing.T) {
		callerBetas := claudeCodeBeta + "," + claudeOAuthBeta + "," + claudeEffortBeta
		incoming := http.Header{}
		incoming.Set("Anthropic-Beta", callerBetas)
		auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-binding-order"}}

		baseline := betasFor(t, withoutBinding, auth, "key-binding-order", incoming, false)
		if baseline != callerBetas {
			t.Fatalf("baseline = %q, want the caller list forwarded as sent %q", baseline, callerBetas)
		}
		assertAppended(t, baseline, betasFor(t, withBinding, auth, "key-binding-order", incoming, false))
	})

	t.Run("confirmed native client lands after the credential trailer", func(t *testing.T) {
		incoming := http.Header{}
		incoming.Set("Anthropic-Beta", claudeCodeBeta+","+claudeEffortBeta)
		auth, key := claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey

		baseline := betasFor(t, withoutBinding, auth, key, incoming, true)
		if want := claudeCodeBeta + "," + claudeOAuthBeta + "," + claudeEffortBeta + "," + claudeExtendedCacheTTLBeta; baseline != want {
			t.Fatalf("baseline = %q, want %q", baseline, want)
		}
		assertAppended(t, baseline, betasFor(t, withBinding, auth, key, incoming, true))
	})
}

// The same rewrite must keep working for the other beta whose spelling CPA owns.
func TestApplyClaudeHeaders_FastModeBetaCanonicalizesCallerCasing(t *testing.T) {
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", strings.ToUpper(claudeFastModeBeta))
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-fast-casing"}}

	req := newClaudeHeaderTestRequest(t, incoming)
	if err := applyClaudeHeaders(req, auth, "key-fast-casing", false, nil,
		[]byte(`{"model":"claude-opus-5","speed":"fast"}`), nil, incoming, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	if got := req.Header.Get("Anthropic-Beta"); got != claudeFastModeBeta {
		t.Fatalf("Anthropic-Beta = %q, want canonical %q", got, claudeFastModeBeta)
	}
}

// The lowercased requested map is what lets a caller casing variant still select
// the measured baseline's conditional betas, which are always emitted canonically.
func TestApplyClaudeHeaders_KnownCallerBetaSelectsCanonicalBaselineToken(t *testing.T) {
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", strings.ToUpper(claudeContext1MBeta))

	req := newClaudeHeaderTestRequest(t, incoming)
	if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false, nil,
		[]byte(`{"model":"claude-opus-5"}`), nil, incoming, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	assertCanonicalBetaOnce(t, req.Header.Get("Anthropic-Beta"), claudeContext1MBeta)
}

// The beta is derived from the finished upstream body and from nothing else. A
// caller asking for it through the header or through body "betas" must not get it
// onto a first-party request whose body carries no thinking.block_binding.
func TestApplyClaudeHeaders_ThinkingBindingBetaNeverInjectedFromCallerRequestAlone(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":2048}}`)

	t.Run("header only", func(t *testing.T) {
		incoming := http.Header{}
		incoming.Set("Anthropic-Beta", claudeThinkingBindingControlsBeta)
		req := newClaudeHeaderTestRequest(t, incoming)
		if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false, nil, body, nil, incoming, false); err != nil {
			t.Fatalf("applyClaudeHeaders() error = %v", err)
		}
		assertBetaAbsent(t, req.Header.Get("Anthropic-Beta"), claudeThinkingBindingControlsBeta)
	})

	t.Run("header casing variant only", func(t *testing.T) {
		incoming := http.Header{}
		incoming.Set("Anthropic-Beta", strings.ToUpper(claudeThinkingBindingControlsBeta))
		req := newClaudeHeaderTestRequest(t, incoming)
		if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false, nil, body, nil, incoming, false); err != nil {
			t.Fatalf("applyClaudeHeaders() error = %v", err)
		}
		assertBetaAbsent(t, req.Header.Get("Anthropic-Beta"), claudeThinkingBindingControlsBeta)
	})

	t.Run("body betas only", func(t *testing.T) {
		req := newClaudeHeaderTestRequest(t, nil)
		if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false,
			[]string{claudeThinkingBindingControlsBeta}, body, nil, nil, false); err != nil {
			t.Fatalf("applyClaudeHeaders() error = %v", err)
		}
		assertBetaAbsent(t, req.Header.Get("Anthropic-Beta"), claudeThinkingBindingControlsBeta)
	})

	// Caller-owned passthrough forwards every caller beta verbatim, and this one is
	// not special-cased (see TestApplyClaudeHeaders_UnknownBodyBetaPreservedOnAnthropic).
	// What must never happen is CPA adding a beta the body never asked for.
	t.Run("caller owned passthrough adds nothing", func(t *testing.T) {
		auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-binding-passthrough"}}
		req := newClaudeHeaderTestRequest(t, nil)
		if err := applyClaudeHeaders(req, auth, "key-binding-passthrough", false, nil, body, nil, nil, false); err != nil {
			t.Fatalf("applyClaudeHeaders() error = %v", err)
		}
		if got := req.Header.Get("Anthropic-Beta"); got != "" {
			t.Fatalf("Anthropic-Beta = %q, want empty: nothing in this request asks for a beta", got)
		}
	})
}

func TestApplyClaudeHeaders_NonPropagationOfUnrecognizedBetasOnFirstParty(t *testing.T) {
	incoming := http.Header{"Anthropic-Beta": []string{"custom-header-beta"}}
	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":2048,"block_binding":{}}}`)
	extra := []string{"custom-extra-beta"}

	req := newClaudeHeaderTestRequest(t, nil)
	if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false, extra, body, nil, incoming, false); err != nil {
		t.Fatalf("applyClaudeHeaders error = %v", err)
	}
	got := req.Header.Get("Anthropic-Beta")
	assertCanonicalBetaOnce(t, got, claudeThinkingBindingControlsBeta)
	tokens := countBetaTokens(got)
	for _, bad := range []string{"custom-header-beta", "custom-extra-beta"} {
		if tokens[bad] > 0 {
			t.Fatalf("unexpected leaked beta %q on first-party", bad)
		}
	}
}

// Non-Anthropic upstreams have their own beta vocabulary: the token CPA derives
// for api.anthropic.com must not reach them, and their caller betas must survive
// with CSV, multi-value and case-insensitive dedup intact.
func TestApplyClaudeHeaders_CustomNonAnthropicRoutePreservesCallerBetas(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://api.kimi.com/v1/messages", nil)
	body := []byte(`{"model":"kimi-k2.5","thinking":{"type":"enabled","block_binding":{}}}`)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "k", "base_url": "https://api.kimi.com"}}
	incoming := http.Header{"Anthropic-Beta": []string{"caller-custom-1, duplicate-beta"}}

	if err := applyClaudeHeaders(req, auth, "k", false, []string{"DUPLICATE-BETA, caller-extra-2"}, body, nil, incoming, false); err != nil {
		t.Fatalf("applyClaudeHeaders error = %v", err)
	}
	got := req.Header.Get("Anthropic-Beta")
	assertBetaAbsent(t, got, claudeThinkingBindingControlsBeta)
	tokens := countBetaTokens(got)
	if tokens["caller-custom-1"] != 1 || tokens["caller-extra-2"] != 1 || tokens["duplicate-beta"] != 1 {
		t.Fatalf("caller betas not preserved or deduplicated: %v", tokens)
	}
	// An unknown caller token keeps the caller's own casing: canonicalisation is
	// reserved for the betas CPA owns the spelling of.
	if !strings.Contains(got, "duplicate-beta") {
		t.Fatalf("Anthropic-Beta = %q, want the caller's first spelling preserved", got)
	}
}

func claudeBlockBindingTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID: "official-oauth",
		Attributes: map[string]string{
			"api_key":             "sk-ant-oat-test",
			"fingerprint_profile": "claude-code-cli",
		},
		Metadata: claudeOAuthTestMetadata(),
	}
}

func TestClaudeExecutor_StreamingFirstPartyCapturesBlockBinding(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header

	transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenBody, _ = io.ReadAll(req.Body)
		seenHeaders = req.Header.Clone()
		sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	executor := NewClaudeExecutor(&config.Config{})

	payload := []byte(`{"model":"claude-opus-5","max_tokens":4096,"stream":true,"thinking":{"type":"enabled","budget_tokens":2048,"block_binding":{"strict":true}},"messages":[{"role":"user","content":"stream test"}]}`)
	opts := cliproxyexecutor.Options{
		Stream:       true,
		SourceFormat: sdktranslator.FormatClaude,
		Headers: http.Header{
			"Anthropic-Beta": []string{"unrecognized-caller-beta-2099"},
		},
	}

	res, err := executor.ExecuteStream(ctx, claudeBlockBindingTestAuth(), cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: payload,
	}, opts)
	if err != nil {
		t.Fatalf("ExecuteStream error = %v", err)
	}
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error = %v", chunk.Err)
		}
	}

	if got := gjson.GetBytes(seenBody, "thinking.block_binding"); !got.Exists() || !got.IsObject() {
		t.Fatalf("upstream body thinking.block_binding missing: %s", string(seenBody))
	}
	if !gjson.GetBytes(seenBody, "thinking.block_binding.strict").Bool() {
		t.Fatalf("upstream body thinking.block_binding.strict != true: %s", string(seenBody))
	}

	betas := claudeFingerprintHeaderValue(seenHeaders, "Anthropic-Beta")
	assertCanonicalBetaOnce(t, betas, claudeThinkingBindingControlsBeta)
	if countBetaTokens(betas)["unrecognized-caller-beta-2099"] != 0 {
		t.Fatalf("Anthropic-Beta = %q, leaked unrecognized beta on first-party", betas)
	}
}

// Non-streaming symmetry of the streaming capture above: both request paths build
// their header from the same finished body, so neither may drift.
func TestClaudeExecutor_NonStreamingFirstPartyCapturesBlockBinding(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header

	transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenBody, _ = io.ReadAll(req.Body)
		seenHeaders = req.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"msg_1","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
			)),
		}, nil
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	payload := []byte(`{"model":"claude-opus-5","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":2048,"block_binding":{"strict":true}},"messages":[{"role":"user","content":"non stream test"}]}`)

	if _, err := NewClaudeExecutor(&config.Config{}).Execute(ctx, claudeBlockBindingTestAuth(), cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers: http.Header{
			"Anthropic-Beta": []string{"unrecognized-caller-beta-2099"},
		},
	}); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	if got := gjson.GetBytes(seenBody, "thinking.block_binding"); !got.Exists() || !got.IsObject() {
		t.Fatalf("upstream body thinking.block_binding missing: %s", string(seenBody))
	}
	if !gjson.GetBytes(seenBody, "thinking.block_binding.strict").Bool() {
		t.Fatalf("upstream body thinking.block_binding.strict != true: %s", string(seenBody))
	}

	betas := claudeFingerprintHeaderValue(seenHeaders, "Anthropic-Beta")
	assertCanonicalBetaOnce(t, betas, claudeThinkingBindingControlsBeta)
	if countBetaTokens(betas)["unrecognized-caller-beta-2099"] != 0 {
		t.Fatalf("Anthropic-Beta = %q, leaked unrecognized beta on first-party", betas)
	}
}

// tool_choice forcing tool use makes disableThinkingIfToolChoiceForced delete the
// whole thinking block from the body that is actually sent. Because the beta is
// keyed on that finished body, the header follows and stops declaring a feature
// the request no longer uses.
func TestClaudeExecutor_ToolChoiceForcedDropsThinkingAndBindingBeta(t *testing.T) {
	payload := []byte(`{"model":"claude-opus-5","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":2048,"block_binding":{"strict":true}},"tool_choice":{"type":"any"},"tools":[{"name":"read_file","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"forced tool"}]}`)

	assertDropped := func(t *testing.T, seenBody []byte, seenHeaders http.Header) {
		t.Helper()
		if len(seenBody) == 0 {
			t.Fatal("expected an upstream request")
		}
		if got := gjson.GetBytes(seenBody, "thinking"); got.Exists() {
			t.Fatalf("thinking = %s, want deleted when tool_choice forces tool use", got.Raw)
		}
		assertBetaAbsent(t, claudeFingerprintHeaderValue(seenHeaders, "Anthropic-Beta"), claudeThinkingBindingControlsBeta)
	}

	t.Run("execute", func(t *testing.T) {
		var seenBody []byte
		var seenHeaders http.Header
		transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			seenBody, _ = io.ReadAll(req.Body)
			seenHeaders = req.Header.Clone()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"id":"msg_1","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
				)),
			}, nil
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
		if _, err := NewClaudeExecutor(&config.Config{}).Execute(ctx, claudeBlockBindingTestAuth(), cliproxyexecutor.Request{
			Model:   "claude-opus-5",
			Payload: payload,
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); err != nil {
			t.Fatalf("Execute error = %v", err)
		}
		assertDropped(t, seenBody, seenHeaders)
	})

	t.Run("execute stream", func(t *testing.T) {
		var seenBody []byte
		var seenHeaders http.Header
		transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			seenBody, _ = io.ReadAll(req.Body)
			seenHeaders = req.Header.Clone()
			sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(sse)),
			}, nil
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
		res, err := NewClaudeExecutor(&config.Config{}).ExecuteStream(ctx, claudeBlockBindingTestAuth(), cliproxyexecutor.Request{
			Model:   "claude-opus-5",
			Payload: payload,
		}, cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatClaude})
		if err != nil {
			t.Fatalf("ExecuteStream error = %v", err)
		}
		for chunk := range res.Chunks {
			if chunk.Err != nil {
				t.Fatalf("chunk error = %v", chunk.Err)
			}
		}
		assertDropped(t, seenBody, seenHeaders)
	})
}

// count_tokens follows the same rule as /v1/messages because the rule is a
// property of the JSON that goes on the wire, not of the endpoint: if the
// count_tokens body still carries thinking.block_binding, Anthropic rejects it
// without the beta, so the beta has to be declared there too. This pins the
// derived header against the observed body only; no wire capture is claimed for
// the count_tokens beta profile itself.
func TestClaudeExecutor_CountTokensFollowsFinalBodyForBlockBinding(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "binding present",
			payload: `{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":2048,"block_binding":{"strict":true}},"messages":[{"role":"user","content":"hello"}]}`,
			want:    true,
		},
		{
			name:    "thinking disabled",
			payload: `{"model":"claude-opus-5","thinking":{"type":"disabled","block_binding":{"strict":true}},"messages":[{"role":"user","content":"hello"}]}`,
			want:    false,
		},
		{
			name:    "no binding",
			payload: `{"model":"claude-opus-5","thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":"hello"}]}`,
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seenBody []byte
			var seenHeaders http.Header
			var seenPath string
			transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				seenBody, _ = io.ReadAll(req.Body)
				seenHeaders = req.Header.Clone()
				seenPath = req.URL.Path
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"input_tokens":7}`)),
					Request:    req,
				}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			payload := []byte(tc.payload)
			if _, err := NewClaudeExecutor(&config.Config{}).CountTokens(ctx, claudeBlockBindingTestAuth(),
				cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload},
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload}); err != nil {
				t.Fatalf("CountTokens error = %v", err)
			}
			if !strings.HasSuffix(seenPath, "/count_tokens") {
				t.Fatalf("upstream path = %q, want the count_tokens endpoint", seenPath)
			}
			betas := claudeFingerprintHeaderValue(seenHeaders, "Anthropic-Beta")
			// Positive pin first, so the beta assertion cannot pass vacuously.
			if got := gjson.GetBytes(seenBody, "messages.#").Int(); got != 1 {
				t.Fatalf("upstream messages length = %d, want 1: %s", got, seenBody)
			}
			hasBinding := claudeBodyHasBlockBinding(seenBody)
			if hasBinding != tc.want {
				t.Fatalf("count_tokens body block binding = %v, want %v: %s", hasBinding, tc.want, seenBody)
			}
			if tc.want {
				assertCanonicalBetaOnce(t, betas, claudeThinkingBindingControlsBeta)
			} else {
				assertBetaAbsent(t, betas, claudeThinkingBindingControlsBeta)
			}
		})
	}
}
