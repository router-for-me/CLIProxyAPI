package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

var claudeDateReminderPattern = regexp.MustCompile(`Today's date is (\d{4}-\d{2}-\d{2})`)

// extractClaudeCloakDate pulls the calendar date out of the currentDate
// reminder injected into the first user message of a cloaked payload.
func extractClaudeCloakDate(t *testing.T, payload []byte) string {
	t.Helper()
	content := gjson.GetBytes(payload, "messages.0.content")
	if content.IsArray() {
		for _, block := range content.Array() {
			if match := claudeDateReminderPattern.FindStringSubmatch(block.Get("text").String()); match != nil {
				return match[1]
			}
		}
	} else if match := claudeDateReminderPattern.FindStringSubmatch(content.String()); match != nil {
		return match[1]
	}
	t.Fatalf("no currentDate reminder found in first user message: %s", payload)
	return ""
}

// TestClaudeCloakedDateReminderPinnedToSessionAcrossMidnight verifies that the
// cloaked currentDate reminder stays identical for one session even when the
// local calendar date flips between two requests. Rewriting the date on every
// request would invalidate the prompt-cache prefix in front of the entire
// conversation, including in-flight turns.
func TestClaudeCloakedDateReminderPinnedToSessionAcrossMidnight(t *testing.T) {
	helps.ResetClaudeDiagnosticsForTest()
	t.Cleanup(helps.ResetClaudeDiagnosticsForTest)

	auth := &cliproxyauth.Auth{
		ID:         "test-date-pin-cred",
		Attributes: map[string]string{"timezone": "Asia/Shanghai"},
	}
	headers := http.Header{}
	ctx := helps.WithClaudeSessionID(context.Background(), "sess-midnight")

	payload1 := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"turn one"}]}`)
	prevReq1, promptID1, cCtx1, ok1 := resolveClaudeContinuityTags(ctx, nil, auth, headers, payload1, true, "", "")
	if !ok1 {
		t.Fatal("continuity context not resolved for session request 1")
	}
	if cCtx1.PinnedDate == "" {
		t.Fatal("first request must establish a pinned session date")
	}
	out1 := checkSystemInstructionsWithSigningModeAt(payload1, false, false, "2.1.280", "cli", "", cCtx1.PinnedDate, false, prevReq1, promptID1)
	date1 := extractClaudeCloakDate(t, out1)
	if date1 != cCtx1.PinnedDate {
		t.Fatalf("first request date = %q, want pinned anchor %q", date1, cCtx1.PinnedDate)
	}

	// Second request after local midnight: the per-request candidate would be
	// the flipped date, but the session pin must win. Compute the next calendar day
	// dynamically from the pinned anchor date rather than hardcoding.
	parsedAnchor, errParse := time.Parse("2006-01-02", cCtx1.PinnedDate)
	if errParse != nil {
		t.Fatalf("parse anchor date: %v", errParse)
	}
	nextDay := parsedAnchor.AddDate(0, 0, 1).Format("2006-01-02")
	if nextDay == cCtx1.PinnedDate {
		t.Fatalf("test setup: anchor %q must differ from the simulated next day", cCtx1.PinnedDate)
	}
	resolved := helps.PinClaudeSessionDate(cCtx1.Key, nextDay)
	if resolved != cCtx1.PinnedDate {
		t.Fatalf("second request resolved date = %q, want session-pinned %q (local midnight flip busts the cached prefix)", resolved, cCtx1.PinnedDate)
	}

	payload2 := []byte(`{"model":"claude-opus-5","messages":[` +
		`{"role":"user","content":"turn one"},` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":"turn two"}]}`)
	prevReq2, promptID2, cCtx2, ok2 := resolveClaudeContinuityTags(ctx, nil, auth, headers, payload2, true, "", "")
	if !ok2 {
		t.Fatal("continuity context not resolved for session request 2")
	}
	if cCtx2.PinnedDate != cCtx1.PinnedDate {
		t.Fatalf("second request continuity pin = %q, want anchor %q", cCtx2.PinnedDate, cCtx1.PinnedDate)
	}
	out2 := checkSystemInstructionsWithSigningModeAt(payload2, false, false, "2.1.280", "cli", "", cCtx2.PinnedDate, false, prevReq2, promptID2)
	date2 := extractClaudeCloakDate(t, out2)
	if date2 != date1 {
		t.Fatalf("date reminder changed within one session: %q -> %q (busts prompt cache prefix)", date1, date2)
	}
}

// roundTripperFunc mirrors the helper used by the cache repro tests.
type datePinRoundTripperFunc func(req *http.Request) (*http.Response, error)

func (f datePinRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestClaudeCloakedDateReminderSessionByteStability locks the executor-level
// behaviour: two requests of the same pinned session produce an identical
// messages[0] (which carries the currentDate reminder) even across simulated
// local midnight, and a fresh session re-anchors to the new date.
func TestClaudeCloakedDateReminderSessionByteStability(t *testing.T) {
	helps.ResetClaudeDiagnosticsForTest()
	t.Cleanup(helps.ResetClaudeDiagnosticsForTest)

	var mockTime time.Time
	claudeCodeCurrentTimeFunc = func(cfg *config.Config, auth *cliproxyauth.Auth) time.Time {
		return mockTime
	}
	t.Cleanup(func() {
		claudeCodeCurrentTimeFunc = nil
	})

	var capturedBodies [][]byte
	call := 0
	transport := datePinRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body := make([]byte, 0)
		if req.Body != nil {
			buf, errRead := io.ReadAll(req.Body)
			if errRead != nil {
				t.Fatal(errRead)
			}
			body = buf
		}
		capturedBodies = append(capturedBodies, body)
		call++
		response := fmt.Sprintf(`{"id":"msg_%d","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":10,"output_tokens":2}}`, call)
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		header.Set("request-id", fmt.Sprintf("req_upstream_%d", call))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    req,
		}, nil
	})

	cfg := &config.Config{}
	auth := &cliproxyauth.Auth{
		ID:         "test-date-pin-exec-oauth",
		Attributes: map[string]string{"api_key": "sk-ant-oat-test-oauth-key-date-pin", "cloak_mode": "always", "timezone": "Asia/Shanghai"},
		Metadata: map[string]any{
			"account_uuid": "11111111-2222-4333-8444-555555555555",
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{
				"00000000000000000000000000000000000000000000000000000000000000d1",
			},
		},
	}
	exec := NewClaudeExecutor(cfg)

	payload := `{
		"model": "claude-opus-5",
		"system": [{"type": "text", "text": "You are an expert software engineer.", "cache_control": {"type": "ephemeral"}}],
		"messages": [{"role": "user", "content": "Say OK."}],
		"max_tokens": 100
	}`
	options := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers:      http.Header{"X-Session-ID": []string{"date-pin-session-1"}},
	}

	// Request 1: before midnight
	mockTime = time.Date(2026, 8, 1, 23, 59, 0, 0, time.UTC)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	req := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(payload)}
	if _, err := exec.Execute(ctx, auth, req, options); err != nil {
		t.Fatalf("request 1 failed: %v", err)
	}

	// Request 2: after midnight in the same session
	mockTime = time.Date(2026, 8, 2, 0, 1, 0, 0, time.UTC)
	if _, err := exec.Execute(ctx, auth, req, options); err != nil {
		t.Fatalf("request 2 failed: %v", err)
	}

	// Request 2b: after midnight in the same session via ExecuteStream
	if _, err := exec.ExecuteStream(ctx, auth, req, options); err != nil {
		t.Fatalf("request 2b (stream) failed: %v", err)
	}

	if len(capturedBodies) != 3 {
		t.Fatalf("expected 3 captured bodies, got %d", len(capturedBodies))
	}

	msg0First := gjson.GetBytes(capturedBodies[0], "messages.0").Raw
	msg0Second := gjson.GetBytes(capturedBodies[1], "messages.0").Raw
	msg0SecondStream := gjson.GetBytes(capturedBodies[2], "messages.0").Raw
	if msg0First != msg0Second {
		t.Fatalf("messages[0] changed between requests of one session:\nfirst:  %s\nsecond: %s", msg0First, msg0Second)
	}
	if msg0First != msg0SecondStream {
		t.Fatalf("messages[0] changed between requests of one session in stream:\nfirst:  %s\nstream: %s", msg0First, msg0SecondStream)
	}

	if got := extractClaudeCloakDate(t, capturedBodies[0]); got != "2026-08-01" {
		t.Fatalf("first session date = %q, want anchored 2026-08-01", got)
	}
	if got := extractClaudeCloakDate(t, capturedBodies[1]); got != "2026-08-01" {
		t.Fatalf("second session date across midnight = %q, want pinned 2026-08-01", got)
	}
	if got := extractClaudeCloakDate(t, capturedBodies[2]); got != "2026-08-01" {
		t.Fatalf("stream session date across midnight = %q, want pinned 2026-08-01", got)
	}

	// Request 3: a fresh session re-anchors to the new date
	options.Headers = http.Header{"X-Session-ID": []string{"date-pin-session-2"}}
	if _, err := exec.Execute(ctx, auth, req, options); err != nil {
		t.Fatalf("fresh session request failed: %v", err)
	}
	if len(capturedBodies) != 4 {
		t.Fatalf("expected 4 captured bodies, got %d", len(capturedBodies))
	}
	if got := extractClaudeCloakDate(t, capturedBodies[3]); got != "2026-08-02" {
		t.Fatalf("fresh session date = %q, want current date 2026-08-02", got)
	}
}

// Explicit max_tokens overrides must not rerun the built-in probe date policy.
func TestClaudeCloakedDateReminderProbeOverrideDoesNotReinitializeSessionDate(t *testing.T) {
	helps.ResetClaudeDiagnosticsForTest()
	t.Cleanup(helps.ResetClaudeDiagnosticsForTest)

	var mockTime time.Time
	claudeCodeCurrentTimeFunc = func(cfg *config.Config, auth *cliproxyauth.Auth) time.Time {
		return mockTime
	}
	t.Cleanup(func() {
		claudeCodeCurrentTimeFunc = nil
	})

	var capturedBodies [][]byte
	call := 0
	transport := datePinRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body := make([]byte, 0)
		if req.Body != nil {
			buf, errRead := io.ReadAll(req.Body)
			if errRead != nil {
				t.Fatal(errRead)
			}
			body = buf
		}
		capturedBodies = append(capturedBodies, body)
		call++
		response := fmt.Sprintf(`{"id":"msg_%d","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":10,"output_tokens":2}}`, call)
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		header.Set("request-id", fmt.Sprintf("req_upstream_%d", call))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    req,
		}, nil
	})

	// Override max_tokens without reclassifying the original probe.
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "claude-opus-5", Protocol: "claude"}},
					Params: map[string]any{"max_tokens": 100},
				},
			},
		},
	}
	auth := &cliproxyauth.Auth{
		ID:         "test-date-pin-exec-oauth-declass",
		Attributes: map[string]string{"api_key": "sk-ant-oat-test-oauth-key-date-pin", "cloak_mode": "always", "timezone": "Asia/Shanghai"},
		Metadata: map[string]any{
			"account_uuid": "11111111-2222-4333-8444-555555555555",
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{
				"00000000000000000000000000000000000000000000000000000000000000d1",
			},
		},
	}
	exec := NewClaudeExecutor(cfg)

	// Normal request establishing the session on day 1
	mockTime = time.Date(2026, 8, 1, 23, 59, 0, 0, time.UTC)
	normalPayload := `{
		"model": "claude-opus-5",
		"messages": [{"role": "user", "content": "normal request"}],
		"max_tokens": 100
	}`
	options := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers:      http.Header{"X-Session-ID": []string{"declass-session-1"}},
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	req1 := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(normalPayload)}
	if _, err := exec.Execute(ctx, auth, req1, options); err != nil {
		t.Fatalf("request 1 failed: %v", err)
	}

	// Request 2: Across midnight, request initially has max_tokens: 1 and content: "probe",
	// and PayloadOverride changes only the final max_tokens field.
	mockTime = time.Date(2026, 8, 2, 0, 1, 0, 0, time.UTC)
	probePayload := `{
		"model": "claude-opus-5",
		"messages": [{"role": "user", "content": "probe"}],
		"max_tokens": 1
	}`
	req2 := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(probePayload)}
	if _, err := exec.Execute(ctx, auth, req2, options); err != nil {
		t.Fatalf("request 2 (Execute) failed: %v", err)
	}

	// Request 3: Same thing for ExecuteStream
	req3 := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(probePayload)}
	if _, err := exec.ExecuteStream(ctx, auth, req3, options); err != nil {
		t.Fatalf("request 3 (ExecuteStream) failed: %v", err)
	}

	if len(capturedBodies) != 3 {
		t.Fatalf("expected 3 captured bodies, got %d", len(capturedBodies))
	}

	// Payload does not reclassify probes or replay the prior session date.
	if got := extractClaudeCloakDate(t, capturedBodies[1]); got != "2026-08-02" {
		t.Fatalf("configured Execute request date = %q, want original probe date 2026-08-02", got)
	}
	if got := extractClaudeCloakDate(t, capturedBodies[2]); got != "2026-08-02" {
		t.Fatalf("configured ExecuteStream request date = %q, want original probe date 2026-08-02", got)
	}
}
