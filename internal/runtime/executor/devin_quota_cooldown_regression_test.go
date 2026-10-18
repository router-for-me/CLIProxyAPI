package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Tests in this file cover issue #6690: Devin daily/weekly usage quota
// exhaustion must cool down the whole credential until the quota reset
// (when a valid reset timestamp is known), instead of the short
// per-model exponential backoff.

func TestDevinQuotaExhaustedError_StreamWeeklyIsCredentialScopedWithResetRetryAfter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	auth := devinTestAuthWithQuotaResets(now.Add(24*time.Hour), now.Add(6*24*time.Hour))

	err := devinStreamTrailerError(t, auth, "failed_precondition", "Your weekly usage quota has been exhausted.")

	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("stream error type = %T, want statusErr (err=%v)", err, err)
	}
	if se.code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429", se.code)
	}
	if !se.IsCredentialScoped() {
		t.Fatalf("weekly quota exhaustion must be credential-scoped (issue #6690), got model-scoped: %+v", se)
	}
	if se.RetryAfter() == nil {
		t.Fatalf("weekly quota exhaustion must carry reset-based RetryAfter, got nil")
	}
	want := 6 * 24 * time.Hour
	if got := *se.RetryAfter(); got < want-time.Minute || got > want+time.Minute {
		t.Fatalf("RetryAfter = %v, want ~%v (weekly reset)", got, want)
	}
}

func TestDevinQuotaExhaustedError_StreamDailyUsesDailyReset(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	auth := devinTestAuthWithQuotaResets(now.Add(3*time.Hour), now.Add(6*24*time.Hour))

	err := devinStreamTrailerError(t, auth, "failed_precondition", "Your daily usage quota has been exhausted.")

	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("stream error type = %T, want statusErr (err=%v)", err, err)
	}
	if !se.IsCredentialScoped() {
		t.Fatalf("daily quota exhaustion must be credential-scoped (issue #6690), got model-scoped")
	}
	if se.RetryAfter() == nil {
		t.Fatalf("daily quota exhaustion must carry daily-reset-based RetryAfter, got nil")
	}
	if got := *se.RetryAfter(); got < 2*time.Hour || got > 4*time.Hour {
		t.Fatalf("RetryAfter = %v, want ~3h (daily reset, not weekly)", got)
	}
}

func TestDevinQuotaExhaustedError_StreamWithoutSignalsStillCredentialScoped(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "devin-quota-no-signals", Provider: "devin"}

	err := devinStreamTrailerError(t, auth, "failed_precondition", "Your weekly usage quota has been exhausted.")

	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("stream error type = %T, want statusErr (err=%v)", err, err)
	}
	if !se.IsCredentialScoped() {
		t.Fatalf("weekly quota exhaustion without known reset must still be credential-scoped (issue #6690)")
	}
	if se.RetryAfter() != nil {
		t.Fatalf("RetryAfter = %v, want nil (no valid reset signal; conductor credential backoff applies)", *se.RetryAfter())
	}
}

func TestDevinQuotaExhaustedError_StreamPastResetYieldsNoRetryAfter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	auth := devinTestAuthWithQuotaResets(now.Add(-time.Hour), now.Add(-time.Hour))

	err := devinStreamTrailerError(t, auth, "failed_precondition", "Your weekly usage quota has been exhausted.")

	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("stream error type = %T, want statusErr (err=%v)", err, err)
	}
	if !se.IsCredentialScoped() {
		t.Fatalf("weekly quota exhaustion must be credential-scoped even with a stale reset (issue #6690)")
	}
	if se.RetryAfter() != nil {
		t.Fatalf("RetryAfter = %v, want nil for past reset timestamp", *se.RetryAfter())
	}
}

func TestDevinQuotaExhaustedError_StreamFarFutureResetIsRejected(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	// A reset further out than any weekly window (plus slack) is bogus data
	// and must not pin the credential for months.
	auth := devinTestAuthWithQuotaResets(now.Add(90*24*time.Hour), now.Add(90*24*time.Hour))

	err := devinStreamTrailerError(t, auth, "failed_precondition", "Your weekly usage quota has been exhausted.")

	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("stream error type = %T, want statusErr (err=%v)", err, err)
	}
	if !se.IsCredentialScoped() {
		t.Fatalf("weekly quota exhaustion must be credential-scoped (issue #6690)")
	}
	if se.RetryAfter() != nil {
		t.Fatalf("RetryAfter = %v, want nil for implausibly far reset", *se.RetryAfter())
	}
}

func TestDevinQuotaExhaustedError_NonStreamConsumeWeeklyIsCredentialScoped(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	auth := devinTestAuthWithQuotaResets(now.Add(24*time.Hour), now.Add(72*time.Hour))

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{"error":{"code":"failed_precondition","message":"Your weekly usage quota has been exhausted."}}`)))

	_, _, errConsume := consumeDevinFramesToInteractionsForAuth(&buf, "devin/claude-opus-5-5", "claude-opus-5-5", auth)
	if errConsume == nil {
		t.Fatal("expected trailer error, got nil")
	}
	se, ok := errConsume.(statusErr)
	if !ok {
		t.Fatalf("consume error type = %T, want statusErr (err=%v)", errConsume, errConsume)
	}
	if se.code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429", se.code)
	}
	if !se.IsCredentialScoped() {
		t.Fatalf("non-stream weekly quota exhaustion must be credential-scoped (issue #6690), err=%+v", se)
	}
	if se.RetryAfter() == nil {
		t.Fatalf("non-stream weekly quota exhaustion must carry reset-based RetryAfter, got nil")
	}
	want := 72 * time.Hour
	if got := *se.RetryAfter(); got < want-time.Minute || got > want+time.Minute {
		t.Fatalf("RetryAfter = %v, want ~%v (weekly reset)", got, want)
	}
}

func TestDevinTransientErrors_StayModelScoped(t *testing.T) {
	cases := []struct {
		name    string
		code    string
		message string
	}{
		{name: "high demand", code: "permission_denied", message: "We are experiencing high demand. Please try again later."},
		{name: "resource exhausted", code: "resource_exhausted", message: "Resource exhausted, please retry shortly."},
		{name: "monthly ACU out of scope", code: "failed_precondition", message: "User monthly ACU quota exhausted (trace ID: 12345)"},
		{name: "generic rate limit", code: "failed_precondition", message: "Rate limit exceeded. Please slow down."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{ID: "devin-transient", Provider: "devin"}
			err := devinStreamTrailerError(t, auth, tc.code, tc.message)
			se, ok := err.(statusErr)
			if !ok {
				t.Fatalf("stream error type = %T, want statusErr (err=%v)", err, err)
			}
			if se.code != http.StatusTooManyRequests {
				t.Fatalf("code = %d, want 429", se.code)
			}
			if se.IsCredentialScoped() {
				t.Fatalf("%s must stay model-scoped, got credential-scoped", tc.name)
			}
			if se.RetryAfter() != nil {
				t.Fatalf("%s RetryAfter = %v, want nil (short backoff path)", tc.name, *se.RetryAfter())
			}
		})
	}
}

func TestDevinStatusError_HTTP429WeeklyQuotaUsesResetWhenAuthKnown(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	auth := devinTestAuthWithQuotaResets(now.Add(24*time.Hour), now.Add(48*time.Hour))

	se := newDevinStatusErrorForAuth(http.StatusTooManyRequests, http.Header{}, []byte("Your weekly usage quota has been exhausted."), auth)
	if !se.IsCredentialScoped() {
		t.Fatalf("HTTP 429 weekly quota exhaustion must be credential-scoped (issue #6690), got %+v", se)
	}
	if se.RetryAfter() == nil {
		t.Fatal("HTTP 429 weekly quota exhaustion must use the known weekly reset when auth is available")
	}
	want := 48 * time.Hour
	if got := *se.RetryAfter(); got < want-time.Minute || got > want+time.Minute {
		t.Fatalf("RetryAfter = %v, want ~%v", got, want)
	}

	// The auth-less helper still guarantees credential scope (nil delay).
	plain := newDevinStatusError(http.StatusTooManyRequests, http.Header{}, []byte("Your weekly usage quota has been exhausted."))
	if !plain.IsCredentialScoped() || plain.RetryAfter() != nil {
		t.Fatalf("auth-less HTTP 429 = %+v, want credential-scoped with nil RetryAfter", plain)
	}

	// An explicit Retry-After header keeps precedence over signal data.
	hdr := http.Header{}
	hdr.Set("Retry-After", "30")
	withHeader := newDevinStatusErrorForAuth(http.StatusTooManyRequests, hdr, []byte("Your weekly usage quota has been exhausted."), auth)
	if withHeader.RetryAfter() == nil || *withHeader.RetryAfter() != 30*time.Second {
		t.Fatalf("explicit Retry-After must win, got %v", withHeader.RetryAfter())
	}
}

func devinTestAuthWithQuotaResets(daily, weekly time.Time) *cliproxyauth.Auth {
	auth := &cliproxyauth.Auth{ID: "devin-quota-auth", Provider: "devin"}
	auth.Quota.Signals = map[string]string{
		"daily_quota_remaining_percent":  "0%",
		"weekly_quota_remaining_percent": "0%",
	}
	if !daily.IsZero() {
		auth.Quota.Signals["daily_quota_reset_at"] = daily.Format(time.RFC3339)
	}
	if !weekly.IsZero() {
		auth.Quota.Signals["weekly_quota_reset_at"] = weekly.Format(time.RFC3339)
	}
	return auth
}

func devinStreamTrailerError(t *testing.T, auth *cliproxyauth.Auth, code, message string) error {
	t.Helper()
	trailer := fmt.Sprintf(`{"error":{"code":%q,"message":%q}}`, code, message)
	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(trailer)))

	e := &DevinExecutor{}
	out := make(chan cliproxyexecutor.StreamChunk, 16)
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: []byte(`{"model":"devin/claude-opus-5-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}
	go func() {
		defer close(out)
		e.streamDevinFramesForAuth(
			context.Background(),
			&buf,
			cliproxyexecutor.Request{Model: "devin/claude-opus-5-5", Payload: opts.OriginalRequest},
			opts,
			"claude-opus-5-5",
			sdktranslator.FormatOpenAI,
			nil,
			out,
			auth,
		)
	}()

	var streamErr error
	for chunk := range out {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil {
		t.Fatal("expected stream trailer error chunk, got none")
	}
	return streamErr
}
