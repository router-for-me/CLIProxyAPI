package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// TestClaudeExecutorSessionBetaHeaderStableAcrossTurns replays the issue #7470
// scenario: within one conversation the executor injects
// diagnostics.previous_message_id, so every later round continues the session
// the first round opened. Upstream pins the declared beta feature set for the
// whole session and rejects a later round whose Anthropic-Beta differs from the
// first. A per-turn body difference that would rebuild a different header (a
// speed:"fast" turn after a plain first turn) must therefore not change the
// emitted value: the second round reuses the first round's header verbatim.
// Synthetic replay built from the issue #7470 report and the existing
// TestClaudeExecutorDiagnosticsAdvancesAfterSuccessfulResponse harness; no
// network access and no real credentials are involved.
func TestClaudeExecutorSessionBetaHeaderStableAcrossTurns(t *testing.T) {
	helps.ResetClaudeDiagnosticsForTest()
	t.Cleanup(helps.ResetClaudeDiagnosticsForTest)

	var betaValues []string
	var previousValues []ojson.Result
	call := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		previousValues = append(previousValues, ojson.GetBytes(body, "diagnostics.previous_message_id"))
		betas := req.Header.Get("Anthropic-Beta")
		if betas == "" {
			betas = strings.Join(req.Header["anthropic-beta"], ",")
		}
		betaValues = append(betaValues, betas)
		call++
		response := `{"id":"msg_issue7470_` + string(rune('0'+call)) + `","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	deviceIDs := []string{"0000000000000000000000000000000000000000000000000000000000000000"}
	testID := uuid.NewString()
	auth := &cliproxyauth.Auth{
		ID:         "issue7470-stable-" + testID,
		Attributes: map[string]string{"api_key": "sk-ant-oat-issue7470-stable", "cloak_mode": "always"},
		Metadata: map[string]any{
			"account_uuid":                        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			claudeauth.ClaudeDeviceIDsMetadataKey: deviceIDs,
		},
	}
	executor := NewClaudeExecutor(&config.Config{})
	options := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Metadata:     map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "issue7470-conversation-" + testID},
	}

	first := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"first turn"}],"max_tokens":64}`)}
	if _, errExecute := executor.Execute(ctx, auth, first, options); errExecute != nil {
		t.Fatalf("first Execute() error = %v", errExecute)
	}
	// The second turn of the same conversation is a speed:"fast" request; the
	// per-request rebuild would add fast-mode-2026-02-01 to its header.
	second := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"first turn"},{"role":"assistant","content":"ok"},{"role":"user","content":"second turn"}],"max_tokens":64,"speed":"fast"}`)}
	if _, errExecute := executor.Execute(ctx, auth, second, options); errExecute != nil {
		t.Fatalf("second Execute() error = %v", errExecute)
	}

	if len(previousValues) != 2 || previousValues[0].Type != ojson.Null || previousValues[0].Raw != "null" {
		t.Fatalf("first diagnostics value = %#v, want explicit null", previousValues)
	}
	if got := previousValues[1].String(); got != "msg_issue7470_1" {
		t.Fatalf("second diagnostics previous_message_id = %q, want first upstream response ID", got)
	}
	if len(betaValues) != 2 {
		t.Fatalf("captured %d beta headers, want 2", len(betaValues))
	}
	if betaValues[0] == "" {
		t.Fatal("first round emitted an empty Anthropic-Beta")
	}
	if betaValues[0] != betaValues[1] {
		t.Fatalf("same-session Anthropic-Beta changed between rounds:\n first  %q\n second %q", betaValues[0], betaValues[1])
	}
	if strings.Contains(betaValues[1], claudeFastModeBeta) {
		t.Fatalf("second round rebuilt its own beta set (%q); the first-round header must stay stable for the session", betaValues[1])
	}
}

// TestApplyClaudeHeaders_ThreadContinuationForwardsCallerBetas covers the
// caller-owned continuation path of issue #7470: a request that already carries
// a session continuation identifier (thread.previous_message_id) forwards the
// caller-provided Anthropic-Beta verbatim instead of rebuilding the profile,
// because the caller's header is the one the thread was started with.
func TestApplyClaudeHeaders_ThreadContinuationForwardsCallerBetas(t *testing.T) {
	helps.ResetClaudeDiagnosticsForTest()
	t.Cleanup(helps.ResetClaudeDiagnosticsForTest)

	callerBetas := "message-threads-2026-08-12,custom-caller-beta-2027-01-01"
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", callerBetas)

	body := []byte(`{"model":"claude-opus-5","thread":{"type":"continue","previous_message_id":"msg_01Issue7470CallerThread"},"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"ok"}]}]}`)
	req := newClaudeHeaderTestRequest(t, incoming)
	if errHeaders := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false, nil, body, &config.Config{}, incoming, false, "11111111-2222-4333-8444-555555555555"); errHeaders != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", errHeaders)
	}
	if got := req.Header.Get("Anthropic-Beta"); got != callerBetas {
		t.Fatalf("Anthropic-Beta = %q, want the caller header forwarded verbatim (%q)", got, callerBetas)
	}
}

// TestApplyClaudeHeaders_ContinuationMarkerScopesSessionBetaPin proves the pin
// only engages for requests that carry a session continuation marker: within
// one session, a marker-carrying round reuses the pinned first-round header,
// while a round without the marker keeps rebuilding from its own body.
func TestApplyClaudeHeaders_ContinuationMarkerScopesSessionBetaPin(t *testing.T) {
	helps.ResetClaudeDiagnosticsForTest()
	t.Cleanup(helps.ResetClaudeDiagnosticsForTest)

	auth := claudeOAuthAuthForBetaPolicy()
	const sessionID = "33333333-2222-4333-8444-555555555555"
	// Create the session's continuity entry so the beta pin has a live session
	// to bind to, exactly like a cloaked first round would have.
	helps.BeginClaudeContinuity(claudeDiagnosticsCredentialIdentity(auth), sessionID, false, "")

	run := func(body []byte) string {
		req := newClaudeHeaderTestRequest(t, nil)
		if errHeaders := applyClaudeHeaders(req, auth, claudeRaceProbeOAuthKey, false, nil, body, &config.Config{}, nil, false, sessionID); errHeaders != nil {
			t.Fatalf("applyClaudeHeaders() error = %v", errHeaders)
		}
		return req.Header.Get("Anthropic-Beta")
	}

	plain := []byte(`{"model":"claude-opus-5","diagnostics":{"previous_message_id":null},"messages":[{"role":"user","content":"hi"}],"max_tokens":64}`)
	firstRound := run(plain)
	if firstRound == "" {
		t.Fatal("first round emitted an empty Anthropic-Beta")
	}

	// Same session, no continuation marker: the per-request rebuild keeps
	// following the body (fast mode adds its beta).
	unmarkedFast := run([]byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"max_tokens":64,"speed":"fast"}`))
	if !strings.Contains(unmarkedFast, claudeFastModeBeta) {
		t.Fatalf("unmarked fast round = %q, want the per-body rebuild with %s", unmarkedFast, claudeFastModeBeta)
	}

	// Same session, continuation marker present: the pinned first-round header
	// wins even though this round's body would rebuild a different set.
	continuedFast := run([]byte(`{"model":"claude-opus-5","diagnostics":{"previous_message_id":"msg_01Issue7470Pinned"},"messages":[{"role":"user","content":"hi"}],"max_tokens":64,"speed":"fast"}`))
	if continuedFast != firstRound {
		t.Fatalf("continued round = %q, want the pinned first-round header %q", continuedFast, firstRound)
	}
}
