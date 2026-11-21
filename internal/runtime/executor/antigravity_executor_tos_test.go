package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
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

	auth := &cliproxyauth.Auth{ID: "tos-auth", Provider: "antigravity"}
	other := &cliproxyauth.Auth{ID: "sibling-auth", Provider: "antigravity"}

	// By default (empty TOSPauseWindow or nil config), TOS_VIOLATION must not pause the pool.
	defaultCfg := &config.Config{}
	markAntigravityTOSPause(defaultCfg, auth)
	if antigravityInTOSPause(defaultCfg, auth) || antigravityInTOSPause(defaultCfg, other) {
		t.Fatal("antigravityInTOSPause() = true by default, want no pool-wide pause unless explicitly configured")
	}
	markAntigravityTOSPause(nil, auth)
	if antigravityInTOSPause(nil, auth) || antigravityInTOSPause(nil, other) {
		t.Fatal("antigravityInTOSPause(nil) = true, want no pool-wide pause")
	}

	// When explicitly configured, TOS_VIOLATION pauses the entire pool.
	cfg := &config.Config{}
	cfg.Antigravity.TOSPauseWindow = "6h"
	if antigravityInTOSPause(cfg, auth) {
		t.Fatal("antigravityInTOSPause() = true before any TOS violation")
	}
	markAntigravityTOSPause(cfg, auth)
	if !antigravityInTOSPause(cfg, auth) {
		t.Fatal("antigravityInTOSPause() = false after TOS violation, want pool-wide pause")
	}
	// A different auth in the same pool must also be paused.
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

	auth := &cliproxyauth.Auth{ID: "tos-auth", Provider: "antigravity"}

	// Unset, disabled, zero, negative, and invalid windows must not pause the pool.
	for _, raw := range []string{"", "0", "off", "false", "none", "disabled", "0s", "-1m", "invalid"} {
		resetAntigravityCreditsRetryState()
		cfg := &config.Config{}
		cfg.Antigravity.TOSPauseWindow = raw
		markAntigravityTOSPause(cfg, auth)
		if antigravityInTOSPause(cfg, auth) {
			t.Fatalf("antigravityInTOSPause() = true for TOSPauseWindow=%q, want no pause", raw)
		}
	}

	// A custom positive window must be honored.
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

type countingAntigravityExecutor struct {
	*AntigravityExecutor
	mu    sync.Mutex
	calls []string
}

func (c *countingAntigravityExecutor) record(auth *cliproxyauth.Auth) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := ""
	if auth != nil {
		id = auth.ID
	}
	c.calls = append(c.calls, id)
}

func (c *countingAntigravityExecutor) recordedCalls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.calls))
	copy(out, c.calls)
	return out
}

func (c *countingAntigravityExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	c.record(auth)
	return c.AntigravityExecutor.Execute(ctx, auth, req, opts)
}

func (c *countingAntigravityExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	c.record(auth)
	return c.AntigravityExecutor.ExecuteStream(ctx, auth, req, opts)
}

func (c *countingAntigravityExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	c.record(auth)
	return c.AntigravityExecutor.CountTokens(ctx, auth, req, opts)
}

// TestAntigravityTOSPauseErrorIsRequestScoped verifies that the pool-wide
// TOS_VIOLATION pause 503 returned across all Antigravity entrypoints is marked
// as a RequestScopedError, while ordinary 503 statusErr values remain
// non-request-scoped.
func TestAntigravityTOSPauseErrorIsRequestScoped(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	cfg := &config.Config{}
	cfg.Antigravity.TOSPauseWindow = "1h"
	exec := NewAntigravityExecutor(cfg)
	auth := &cliproxyauth.Auth{
		ID:       "tos-pause-unit-auth",
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	markAntigravityTOSPause(cfg, auth)

	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}

	cases := []struct {
		name string
		call func() error
	}{
		{
			name: "Execute",
			call: func() error {
				_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "gemini-2.5-pro",
					Payload: payload,
				}, opts)
				return errExec
			},
		},
		{
			name: "executeClaudeNonStream",
			call: func() error {
				_, errExec := exec.executeClaudeNonStream(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "claude-sonnet-4-6",
					Payload: payload,
				}, opts)
				return errExec
			},
		},
		{
			name: "ExecuteStream",
			call: func() error {
				_, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "claude-sonnet-4-6",
					Payload: payload,
				}, opts)
				return errStream
			},
		},
		{
			name: "CountTokens",
			call: func() error {
				_, errCount := exec.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "gemini-2.5-pro",
					Payload: payload,
				}, opts)
				return errCount
			},
		},
		{
			name: "HttpRequest",
			call: func() error {
				httpReq, errReq := http.NewRequest(http.MethodPost, antigravityBaseURLDaily+"/v1internal:fetchUserInfo", nil)
				if errReq != nil {
					return errReq
				}
				callCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(*http.Request) (*http.Response, error) {
					t.Error("HttpRequest() dialed upstream during active TOS pause")
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       http.NoBody,
					}, nil
				}))
				resp, errDo := exec.HttpRequest(callCtx, auth, httpReq)
				if resp != nil && resp.Body != nil {
					if errClose := resp.Body.Close(); errClose != nil {
						t.Errorf("close response body error: %v", errClose)
					}
				}
				return errDo
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errCall := tc.call()
			if errCall == nil {
				t.Fatalf("%s() error = nil, want 503 TOS pause error", tc.name)
			}
			var statusErrVal cliproxyexecutor.StatusError
			if !errors.As(errCall, &statusErrVal) || statusErrVal.StatusCode() != http.StatusServiceUnavailable {
				t.Fatalf("%s() status = %v, want %d", tc.name, errCall, http.StatusServiceUnavailable)
			}
			if !strings.Contains(errCall.Error(), "antigravity pool paused after TOS_VIOLATION") {
				t.Fatalf("%s() error = %q, want TOS pause message", tc.name, errCall.Error())
			}
			var requestErr cliproxyexecutor.RequestScopedError
			if !errors.As(errCall, &requestErr) || !requestErr.IsRequestScoped() {
				t.Fatalf("%s() error %T is not marked RequestScopedError (IsRequestScoped() == true): %v", tc.name, errCall, errCall)
			}
		})
	}

	t.Run("ordinary_503_not_request_scoped", func(t *testing.T) {
		plain := statusErr{code: http.StatusServiceUnavailable, msg: "upstream overloaded"}
		var requestErr cliproxyexecutor.RequestScopedError
		if errors.As(plain, &requestErr) && requestErr.IsRequestScoped() {
			t.Fatalf("plain statusErr unexpectedly reported IsRequestScoped() == true")
		}
	})
}

// TestAntigravityManagerTOSPauseStopsImmediatelyWithoutCoolingCredentials
// verifies that when the Antigravity pool is paused after a TOS_VIOLATION,
// Execute, ExecuteStream, and ExecuteCount fail immediately after a single
// executor call without rotating across sibling credentials, retrying outer
// request-retry rounds, or marking healthy credentials as StatusError/cooled.
func TestAntigravityManagerTOSPauseStopsImmediatelyWithoutCoolingCredentials(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	const (
		geminiModel = "gemini-2.5-pro-tos-pause-7748"
		claudeModel = "claude-sonnet-4-6-tos-pause-7748"
	)

	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}

	cases := []struct {
		name  string
		model string
		run   func(m *cliproxyauth.Manager, model string) error
	}{
		{
			name:  "execute_gemini",
			model: geminiModel,
			run: func(m *cliproxyauth.Manager, model string) error {
				_, errRun := m.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
					Model:   model,
					Payload: payload,
				}, opts)
				return errRun
			},
		},
		{
			name:  "execute_claude",
			model: claudeModel,
			run: func(m *cliproxyauth.Manager, model string) error {
				_, errRun := m.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
					Model:   model,
					Payload: payload,
				}, opts)
				return errRun
			},
		},
		{
			name:  "stream",
			model: claudeModel,
			run: func(m *cliproxyauth.Manager, model string) error {
				_, errRun := m.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
					Model:   model,
					Payload: payload,
				}, opts)
				return errRun
			},
		},
		{
			name:  "count_tokens",
			model: geminiModel,
			run: func(m *cliproxyauth.Manager, model string) error {
				_, errRun := m.ExecuteCount(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
					Model:   model,
					Payload: payload,
				}, opts)
				return errRun
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			cfg := &config.Config{}
			cfg.Antigravity.TOSPauseWindow = "1h"

			exec := &countingAntigravityExecutor{
				AntigravityExecutor: NewAntigravityExecutor(cfg),
			}
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
			manager.SetRetryConfig(2, 0, 0)
			manager.RegisterExecutor(exec)

			reg := registry.GetGlobalRegistry()
			authIDs := make([]string, 0, 3)
			var firstAuth *cliproxyauth.Auth
			for i := 1; i <= 3; i++ {
				id := fmt.Sprintf("ag-tos-pause-7748-%s-cred-%d", tc.name, i)
				auth := &cliproxyauth.Auth{
					ID:       id,
					Provider: "antigravity",
					Status:   cliproxyauth.StatusActive,
					Metadata: map[string]any{
						"access_token": fmt.Sprintf("token-%d", i),
						"project_id":   fmt.Sprintf("project-%d", i),
						"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
					},
				}
				if i == 1 {
					firstAuth = auth
				}
				reg.RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: tc.model}})
				authID := id
				t.Cleanup(func() { reg.UnregisterClient(authID) })
				if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
					t.Fatalf("Register(%s) error = %v", id, errRegister)
				}
				authIDs = append(authIDs, id)
			}

			markAntigravityTOSPause(cfg, firstAuth)

			errRun := tc.run(manager, tc.model)
			if errRun == nil {
				t.Fatalf("%s error = nil, want HTTP 503 TOS pause error", tc.name)
			}
			var statusErrVal cliproxyexecutor.StatusError
			if !errors.As(errRun, &statusErrVal) || statusErrVal.StatusCode() != http.StatusServiceUnavailable {
				t.Fatalf("%s status = %v, want %d", tc.name, errRun, http.StatusServiceUnavailable)
			}
			if !strings.Contains(errRun.Error(), "antigravity pool paused after TOS_VIOLATION") {
				t.Fatalf("%s error = %q, want TOS pause message", tc.name, errRun.Error())
			}

			calls := exec.recordedCalls()
			if len(calls) != 1 {
				t.Fatalf("%s executor calls = %v (count=%d), want 1 call on a single credential", tc.name, calls, len(calls))
			}

			calledID := calls[0]
			now := time.Now()
			for _, id := range authIDs {
				updated, ok := manager.GetByID(id)
				if !ok || updated == nil {
					t.Fatalf("auth %s not found", id)
				}
				if updated.Status != cliproxyauth.StatusActive {
					t.Fatalf("auth %s status = %q, want %q", id, updated.Status, cliproxyauth.StatusActive)
				}
				if updated.Unavailable || updated.NextRetryAfter.After(now) || !updated.NextRetryAfter.IsZero() {
					t.Fatalf("auth %s was cooled: unavailable=%v next_retry_after=%v", id, updated.Unavailable, updated.NextRetryAfter)
				}
				if updated.LastError != nil {
					t.Fatalf("auth %s LastError = %+v, want nil", id, updated.LastError)
				}
				if state := updated.ModelStates[tc.model]; state != nil && (state.Unavailable || state.Status == cliproxyauth.StatusError || !state.NextRetryAfter.IsZero()) {
					t.Fatalf("auth %s model state was cooled/failed: %+v", id, state)
				}
				if reg.IsModelSuspendedForClient(id, tc.model) {
					t.Fatalf("auth %s model %s suspended in registry", id, tc.model)
				}
				if id != calledID && updated.Failed != 0 {
					t.Fatalf("sibling auth %s Failed = %d, want 0 (never touched)", id, updated.Failed)
				}
			}
		})
	}
}
