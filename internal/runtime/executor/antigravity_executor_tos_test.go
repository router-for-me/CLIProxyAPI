package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// antigravityTOSBody reproduces the verbatim 403 PERMISSION_DENIED signature from
// issue #7296/#7515: a service-scoped Terms-of-Service suspension carrying an
// ErrorInfo reason=TOS_VIOLATION plus appeal metadata.
const antigravityTOSBody = `{
	"error": {
		"code": 403,
		"message": "This service has been disabled in this account for violation of Terms of Service. Please submit an appeal to continue using this product.",
		"status": "PERMISSION_DENIED",
		"details": [
			{
				"@type": "type.googleapis.com/google.rpc.ErrorInfo",
				"reason": "TOS_VIOLATION",
				"domain": "cloudcode-pa.googleapis.com",
				"metadata": {
					"uiMessage": "true",
					"appeal_url": "https://forms.gle/hGzM9MEUv2azZsrb9",
					"appeal_url_link_text": "Submit Appeal"
				}
			}
		]
	}
}`

func TestAntigravityTOSViolationParsing(t *testing.T) {
	t.Run("matches verbatim tos signature and extracts appeal url", func(t *testing.T) {
		violation := parseAntigravityTOSViolation([]byte(antigravityTOSBody))
		if violation == nil {
			t.Fatal("parseAntigravityTOSViolation() = nil, want TOS violation match")
		}
		if violation.AppealURL != "https://forms.gle/hGzM9MEUv2azZsrb9" {
			t.Fatalf("AppealURL = %q, want %q", violation.AppealURL, "https://forms.gle/hGzM9MEUv2azZsrb9")
		}
		if violation.AppealURLText != "Submit Appeal" {
			t.Fatalf("AppealURLText = %q, want %q", violation.AppealURLText, "Submit Appeal")
		}
	})

	t.Run("does not match plain 403 permission denied without tos reason", func(t *testing.T) {
		body := `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SOME_OTHER_REASON"}]}}`
		if violation := parseAntigravityTOSViolation([]byte(body)); violation != nil {
			t.Fatalf("parseAntigravityTOSViolation() = %+v, want nil for non-TOS 403", violation)
		}
	})

	t.Run("does not match 429 resource exhausted", func(t *testing.T) {
		body := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`
		if violation := parseAntigravityTOSViolation([]byte(body)); violation != nil {
			t.Fatalf("parseAntigravityTOSViolation() = %+v, want nil for 429", violation)
		}
	})

	t.Run("does not match empty or invalid body", func(t *testing.T) {
		if violation := parseAntigravityTOSViolation(nil); violation != nil {
			t.Fatalf("parseAntigravityTOSViolation(nil) = %+v, want nil", violation)
		}
		if violation := parseAntigravityTOSViolation([]byte("not json")); violation != nil {
			t.Fatalf("parseAntigravityTOSViolation(invalid) = %+v, want nil", violation)
		}
	})
}

func TestAntigravityTOSStatusErrTerminal(t *testing.T) {
	err := newAntigravityStatusErr(403, []byte(antigravityTOSBody))
	if !err.credentialScoped {
		t.Fatal("newAntigravityStatusErr(TOS).credentialScoped = false, want true (terminal credential state)")
	}
}

func TestAntigravityTOSPoolPause(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	cfg := &config.Config{}
	// default 6h window
	auth := &cliproxyauth.Auth{ID: "tos-auth", Provider: "antigravity"}

	if antigravityInTOSPause(cfg, auth) {
		t.Fatal("antigravityInTOSPause() = true before any TOS violation")
	}
	markAntigravityTOSPause(cfg, auth)
	if !antigravityInTOSPause(cfg, auth) {
		t.Fatal("antigravityInTOSPause() = false after TOS violation, want pool-wide pause")
	}
	// A different auth in the same pool must also be paused.
	other := &cliproxyauth.Auth{ID: "sibling-auth", Provider: "antigravity"}
	if !antigravityInTOSPause(cfg, other) {
		t.Fatal("antigravityInTOSPause() = false for sibling credential, want pool-wide pause")
	}

	// A 429 quota result must never create or extend the pause.
	antigravityTOSPauseUntil.Clear()
	decision := decideAntigravity429([]byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0.5s"}]}}`))
	if decision.kind == antigravity429DecisionFullQuotaExhausted {
		t.Fatalf("decideAntigravity429() = %q, want transient rate-limit decision", decision.kind)
	}
	if antigravityInTOSPause(cfg, auth) {
		t.Fatal("antigravityInTOSPause() = true after 429-only handling, want no pause")
	}
}

func TestAntigravityTOSPauseWindowConfigurable(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	// A disabled (zero) window must not pause the pool.
	disabled := &config.Config{}
	disabled.Antigravity.TOSPauseWindow = "0"
	auth := &cliproxyauth.Auth{ID: "tos-auth", Provider: "antigravity"}
	markAntigravityTOSPause(disabled, auth)
	if antigravityInTOSPause(disabled, auth) {
		t.Fatal("antigravityInTOSPause() = true with window disabled, want no pause")
	}

	// A custom window must be honored.
	custom := &config.Config{}
	custom.Antigravity.TOSPauseWindow = "2h"
	markAntigravityTOSPause(custom, auth)
	if !antigravityInTOSPause(custom, auth) {
		t.Fatal("antigravityInTOSPause() = false with custom 2h window, want pause")
	}
}

func TestAntigravityTOSErrorCarriesTerminalMarker(t *testing.T) {
	err := newAntigravityStatusErr(403, []byte(antigravityTOSBody))
	if !err.IsTerminalTOSViolation() {
		t.Fatal("newAntigravityStatusErr(TOS).IsTerminalTOSViolation() = false, want true")
	}
	// Non-TOS 403 must not be marked terminal.
	plain := newAntigravityStatusErr(403, []byte(`{"error":{"code":403,"status":"PERMISSION_DENIED"}}`))
	if plain.IsTerminalTOSViolation() {
		t.Fatal("newAntigravityStatusErr(plain 403).IsTerminalTOSViolation() = true, want false")
	}
}

// TestAntigravityClaudeNonStream_SSEErrorFrameIsTerminal verifies that a TOS
// error frame arriving inside an HTTP 200 SSE stream is surfaced as the
// terminal tos_violation error (so the credential is deactivated), not
// swallowed by the aggregation loop into a generic empty-output failure.
func TestAntigravityClaudeNonStream_SSEErrorFrameIsTerminal(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// One normal data frame followed by the TOS error frame.
		_, _ = w.Write([]byte("data: " + `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]}}]}}` + "\n\n"))
		tosLine := strings.Join(strings.Fields(antigravityTOSBody), " ")
		_, _ = w.Write([]byte("data: " + tosLine + "\n\n"))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "tos-sse",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}

	_, err := exec.executeClaudeNonStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity})
	if err == nil {
		t.Fatal("executeClaudeNonStream() = nil, want terminal TOS error")
	}
	sErr, ok := err.(statusErr)
	if !ok {
		t.Fatalf("error type = %T, want statusErr", err)
	}
	if !sErr.IsTerminalTOSViolation() {
		t.Fatalf("IsTerminalTOSViolation() = false; error was swallowed by aggregation: %v", err)
	}
	if sErr.ErrorCode() != "tos_violation" {
		t.Fatalf("ErrorCode() = %q, want tos_violation", sErr.ErrorCode())
	}
}

// TestAntigravityClaudeNonStream_MultiLineSSEErrorIsTerminal verifies that a
// TOS error event split across multiple SSE data: lines is reassembled and
// surfaced as a terminal error rather than being dropped by line-by-line
// classification.
func TestAntigravityClaudeNonStream_MultiLineSSEErrorIsTerminal(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]}}]}}` + "\n\n"))
		// Split the TOS error object across two data: lines at a field boundary
		// (real SSE framers break between JSON tokens, not inside a string).
		tosLine := strings.Join(strings.Fields(antigravityTOSBody), " ")
		mid := strings.Index(tosLine, `"details"`) - 1
		_, _ = w.Write([]byte("data: " + tosLine[:mid] + "\n"))
		_, _ = w.Write([]byte("data: " + tosLine[mid:] + "\n\n"))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "tos-sse-multiline",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	_, err := exec.executeClaudeNonStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity})
	if err == nil {
		t.Fatal("executeClaudeNonStream() = nil, want terminal TOS error for multi-line event")
	}
	sErr, ok := err.(statusErr)
	if !ok || !sErr.IsTerminalTOSViolation() {
		t.Fatalf("err = %v (%T), want terminal tos_violation statusErr", err, err)
	}
}
