package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravityQuotaResetDelay(t *testing.T) {
	tests := []struct {
		name string
		body string
		want time.Duration
	}{
		{
			name: "quota reset delay metadata",
			body: `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Individual quota reached.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"quotaResetDelay":"248557s"}}]}}`,
			want: 248557 * time.Second,
		},
		{
			name: "retry info delay",
			body: `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Individual quota reached.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"3600s"}]}}`,
			want: time.Hour,
		},
		{
			name: "human readable message",
			body: `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Individual quota reached for Claude. Resets in 69h2m37s.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`,
			want: 69*time.Hour + 2*time.Minute + 37*time.Second,
		},
		{
			name: "structured hint wins over message",
			body: `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Individual quota reached. Resets in 69h2m37s.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"quotaResetDelay":"120s"}}]}}`,
			want: 2 * time.Minute,
		},
		{
			name: "no reset hint",
			body: `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Individual quota reached.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`,
			want: 0,
		},
		{
			name: "message hint without a duration",
			body: `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Individual quota reached. Resets in soon.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`,
			want: 0,
		},
		{
			name: "message hint with a zero duration",
			body: `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Individual quota reached. Resets in 0h0m0s.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`,
			want: 0,
		},
		{
			// A long-retry RATE_LIMIT_EXCEEDED is classified as full quota exhaustion by
			// decideAntigravity429, but it is a rate limit rather than a quota window, so
			// the parsed retry delay is not surfaced as a reset deadline.
			name: "rate limit exceeded with a long retry",
			body: `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Too many requests. Resets in 69h2m37s.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"248557s"}]}}`,
			want: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := antigravityQuotaResetDelay([]byte(test.body))
			if test.want == 0 {
				if got != nil {
					t.Fatalf("antigravityQuotaResetDelay() = %v, want nil", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("antigravityQuotaResetDelay() = nil, want duration")
			}
			if *got != test.want {
				t.Fatalf("antigravityQuotaResetDelay() = %v, want %v", *got, test.want)
			}
		})
	}
}

// antigravityQuotaExhaustedBody is the upstream payload for an exhausted weekly
// quota window: ErrorInfo carries QUOTA_EXHAUSTED and the message states when the
// window reopens.
const antigravityQuotaExhaustedBody = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Individual quota reached for this model. Resets in 69h2m37s.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","domain":"googleapis.com","metadata":{"quotaResetDelay":"248557s"}}]}}`

// antigravityQuotaExhaustedExecutor drives AntigravityExecutor against a stub upstream
// answering every request with body.
func antigravityQuotaExhaustedExecutor(t *testing.T, id, body string, cfg *config.Config) (*AntigravityExecutor, *cliproxyauth.Auth) {
	t.Helper()
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	exec := NewAntigravityExecutor(cfg)
	auth := &cliproxyauth.Auth{
		ID: id,
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	return exec, auth
}

// antigravityQuotaExhaustedRetryAfter returns the retry delay the executor attached,
// failing when the error carries none.
func antigravityQuotaExhaustedRetryAfter(t *testing.T, err error) time.Duration {
	t.Helper()
	delay := antigravityQuotaExhaustedRetryDelay(t, err)
	if delay == nil {
		t.Fatal("Execute() error RetryAfter() = nil, want quota reset delay")
	}
	return *delay
}

// antigravityQuotaExhaustedRetryDelay returns the retry delay the executor attached, or
// nil when the error exposes none. The interface is declared once here so individual
// tests do not redeclare it.
func antigravityQuotaExhaustedRetryDelay(t *testing.T, err error) *time.Duration {
	t.Helper()
	type retryAfterProvider interface {
		RetryAfter() *time.Duration
	}
	provider, ok := err.(retryAfterProvider)
	if !ok || provider == nil {
		t.Fatalf("Execute() error %T does not expose RetryAfter()", err)
	}
	return provider.RetryAfter()
}

func TestAntigravityExecute_QuotaExhaustedCarriesResetDelay(t *testing.T) {
	exec, auth := antigravityQuotaExhaustedExecutor(t, "auth-quota-exhausted", antigravityQuotaExhaustedBody, &config.Config{})

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3-pro",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want upstream 429")
	}
	if got, want := antigravityQuotaExhaustedRetryAfter(t, err), 248557*time.Second; got != want {
		t.Fatalf("Execute() retry after = %v, want %v", got, want)
	}
}

// TestAntigravityExecute_QuotaExhaustedMessageOnlyResetDelay covers a payload without
// structured reset metadata: the conductor must still learn the reset time from the
// human-readable hint in the message.
func TestAntigravityExecute_QuotaExhaustedMessageOnlyResetDelay(t *testing.T) {
	exec, auth := antigravityQuotaExhaustedExecutor(t, "auth-quota-message-only", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Individual quota reached for this model. Resets in 1h30m.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`, &config.Config{})

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3-pro",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want upstream 429")
	}
	if got, want := antigravityQuotaExhaustedRetryAfter(t, err), 90*time.Minute; got != want {
		t.Fatalf("Execute() retry after = %v, want %v", got, want)
	}
}

// TestAntigravityExecute_QuotaExhaustedWithoutHintLeavesRetryAfterUnset verifies that a
// payload with no reset hint produces no provider hint, leaving the conductor on its
// existing exponential-backoff path.
func TestAntigravityExecute_QuotaExhaustedWithoutHintLeavesRetryAfterUnset(t *testing.T) {
	exec, auth := antigravityQuotaExhaustedExecutor(t, "auth-quota-no-hint", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Individual quota reached.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`, &config.Config{})

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3-pro",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want upstream 429")
	}
	if delay := antigravityQuotaExhaustedRetryDelay(t, err); delay != nil {
		t.Fatalf("Execute() retry after = %v, want nil", *delay)
	}
}

// TestAntigravityExecute_RateLimitExceededKeepsGenericRetryAfter pins the narrowed
// scope: a long-retry RATE_LIMIT_EXCEEDED is still classified as full quota exhaustion,
// but the reset hint is not parsed out of the message, so the error keeps the generic
// retry delay the status error already carried before the fix.
func TestAntigravityExecute_RateLimitExceededKeepsGenericRetryAfter(t *testing.T) {
	exec, auth := antigravityQuotaExhaustedExecutor(t, "auth-rate-limit-exceeded", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Too many requests. Resets in 1h30m.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"248557s"}]}}`, &config.Config{})

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3-pro",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want upstream 429")
	}
	if got, want := antigravityQuotaExhaustedRetryAfter(t, err), 248557*time.Second; got != want {
		t.Fatalf("Execute() retry after = %v, want the pre-existing %v", got, want)
	}
}

// TestAntigravityExecute_QuotaExhaustedCreditsModeKeepsPermanentDisable verifies the
// credits path is unchanged: an explicit credits balance failure still disables the
// credential permanently and carries no reset hint.
func TestAntigravityExecute_QuotaExhaustedCreditsModeKeepsPermanentDisable(t *testing.T) {
	exec, auth := antigravityQuotaExhaustedExecutor(t, "auth-quota-credits-mode", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Insufficient credits.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"},{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"INSUFFICIENT_G1_CREDITS_BALANCE"}]}}`, &config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	})

	_, err := exec.Execute(cliproxyauth.WithAntigravityCredits(context.Background()), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want upstream 429")
	}
	if _, disabled := antigravityCreditsFailureByAuth.Load(auth.ID); !disabled {
		t.Fatal("credits balance exhaustion did not permanently disable the credential")
	}
	if delay := antigravityQuotaExhaustedRetryDelay(t, err); delay != nil {
		t.Fatalf("Execute() retry after = %v, want nil in credits mode", *delay)
	}
}

// TestAntigravityExecute_QuotaExhaustedCarriesQuotaGroup pins the group hint that makes
// the conductor cool the whole Antigravity quota window instead of one model.
func TestAntigravityExecute_QuotaExhaustedCarriesQuotaGroup(t *testing.T) {
	exec, auth := antigravityQuotaExhaustedExecutor(t, "auth-quota-group", antigravityQuotaExhaustedBody, &config.Config{})

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash-high",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want upstream 429")
	}
	group, ok := err.(interface{ QuotaGroup() string })
	if !ok || group == nil {
		t.Fatalf("Execute() error %T does not expose QuotaGroup()", err)
	}
	if got, want := group.QuotaGroup(), cliproxyauth.AntigravityQuotaGroupGemini; got != want {
		t.Fatalf("Execute() quota group = %q, want %q", got, want)
	}
}

// TestAntigravityExecute_QuotaExhaustedUnknownModelHasNoQuotaGroup keeps the group hint
// scoped to known groups so an unrecognized model name falls back to a per-model cooldown.
func TestAntigravityExecute_QuotaExhaustedUnknownModelHasNoQuotaGroup(t *testing.T) {
	exec, auth := antigravityQuotaExhaustedExecutor(t, "auth-quota-group-unknown", antigravityQuotaExhaustedBody, &config.Config{})

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "mystery-model-1",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want upstream 429")
	}
	if antigravityQuotaExhaustedRetryDelay(t, err) == nil {
		t.Fatal("Execute() error RetryAfter() = nil, want quota reset delay")
	}
	if got := antigravityQuotaExhaustedGroup("mystery-model-1", false); got != "" {
		t.Fatalf("antigravityQuotaExhaustedGroup() = %q, want empty for an unknown group", got)
	}
}

// TestAntigravityExecute_QuotaExhaustedMarksResetDeadline pins the property the
// conductor depends on: the retry hint on a quota-exhausted 429 is a hard upstream
// window reset, not a speculative backoff. Asserting RetryAfter alone is not enough -
// the conductor must be able to tell the two apart to honor the deadline when global
// cooldown scheduling is disabled.
func TestAntigravityExecute_QuotaExhaustedMarksResetDeadline(t *testing.T) {
	exec, auth := antigravityQuotaExhaustedExecutor(t, "auth-quota-deadline", antigravityQuotaExhaustedBody, &config.Config{})

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3-pro",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity})
	if err == nil {
		t.Fatal("Execute() error = nil, want upstream 429")
	}
	deadline, ok := err.(interface{ IsQuotaResetDeadline() bool })
	if !ok {
		t.Fatalf("Execute() error %T does not expose IsQuotaResetDeadline()", err)
	}
	if !deadline.IsQuotaResetDeadline() {
		t.Fatal("IsQuotaResetDeadline() = false, want true for a QUOTA_EXHAUSTED window")
	}
}
