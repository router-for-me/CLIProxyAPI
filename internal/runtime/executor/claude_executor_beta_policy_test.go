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
			[]byte(`{"model":"claude-opus-5"}`), nil, nil, false); err != nil {
			t.Fatalf("applyClaudeHeaders(stream=%v) error = %v", stream, err)
		}
		if got := req.Header.Get("Anthropic-Beta"); got == "attacker-controlled-2099-01-01" {
			t.Fatalf("stream=%v: custom header overrode Anthropic-Beta", stream)
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
