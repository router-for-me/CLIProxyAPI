package executor

import (
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Issue 7017: the Codex upstream rejects per-turn admission with a 429
// turn_rate_limit_exceeded rate_limit_error ("Codex is at capacity") and a
// Retry-After header. It is a transient capacity rejection, not quota
// exhaustion, and must be classified and hinted accordingly.
const issue7017TurnRateLimitBody = `{"error":{"type":"rate_limit_error","code":"turn_rate_limit_exceeded","message":"Codex is at capacity, please try again later"}}`

func issue7017AssertTransient(t *testing.T, err error, wantHint *time.Duration) {
	t.Helper()
	var statusErr interface{ StatusCode() int }
	if !errors.As(err, &statusErr) || statusErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %v, want 429 (err=%v)", statusErr, err)
	}
	var transient cliproxyexecutor.TransientRateLimitError
	if !errors.As(err, &transient) || transient == nil || !transient.IsTransientRateLimit() {
		t.Fatalf("turn_rate_limit_exceeded must be transient rate limit, err=%v", err)
	}
	var scoped interface{ IsCredentialScoped() bool }
	if errors.As(err, &scoped) && scoped.IsCredentialScoped() {
		t.Fatalf("turn rate limit must not be credential scoped, err=%v", err)
	}
	var retry interface{ RetryAfter() *time.Duration }
	if !errors.As(err, &retry) {
		t.Fatalf("error must expose RetryAfter, err=%v", err)
	}
	got := retry.RetryAfter()
	switch {
	case wantHint == nil && got != nil:
		t.Fatalf("RetryAfter = %v, want nil", *got)
	case wantHint != nil && (got == nil || *got != *wantHint):
		t.Fatalf("RetryAfter = %v, want %v", got, *wantHint)
	}
}

func TestIssue7017TurnRateLimitHTTPHonorsRetryAfterHeader(t *testing.T) {
	headers := http.Header{"Retry-After": []string{"256"}}
	err := newCodexStatusErrWithHeaders(http.StatusTooManyRequests, headers, []byte(issue7017TurnRateLimitBody))
	want := 256 * time.Second
	issue7017AssertTransient(t, err, &want)
}

func TestIssue7017TurnRateLimitHeaderFallbacks(t *testing.T) {
	t.Run("zero is explicit immediate retry", func(t *testing.T) {
		err := newCodexStatusErrWithHeaders(http.StatusTooManyRequests, http.Header{"Retry-After": []string{"0"}}, []byte(issue7017TurnRateLimitBody))
		want := time.Duration(0)
		issue7017AssertTransient(t, err, &want)
	})

	t.Run("past http date clamps to zero", func(t *testing.T) {
		past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
		err := newCodexStatusErrWithHeaders(http.StatusTooManyRequests, http.Header{"Retry-After": []string{past}}, []byte(issue7017TurnRateLimitBody))
		want := time.Duration(0)
		issue7017AssertTransient(t, err, &want)
	})

	t.Run("invalid header yields no hint", func(t *testing.T) {
		err := newCodexStatusErrWithHeaders(http.StatusTooManyRequests, http.Header{"Retry-After": []string{"soon"}}, []byte(issue7017TurnRateLimitBody))
		issue7017AssertTransient(t, err, nil)
	})

	t.Run("missing header still transient without hint", func(t *testing.T) {
		err := newCodexStatusErrWithHeaders(http.StatusTooManyRequests, nil, []byte(issue7017TurnRateLimitBody))
		issue7017AssertTransient(t, err, nil)
	})

	t.Run("nested body usage limit hint wins and stays quota", func(t *testing.T) {
		body := `{"body":{"error":{"type":"usage_limit_reached","code":"turn_rate_limit_exceeded","message":"usage","resets_in_seconds":3600}}}`
		err := newCodexStatusErrWithHeaders(http.StatusTooManyRequests, http.Header{"Retry-After": []string{"30"}}, []byte(body))
		retry := err.RetryAfter()
		if retry == nil || *retry != time.Hour {
			t.Fatalf("nested usage_limit_reached reset must win, got %v", retry)
		}
		if !err.IsCredentialScoped() || err.IsTransientRateLimit() {
			t.Fatalf("nested usage_limit_reached must stay credential quota")
		}
	})

	t.Run("huge retry-after seconds clamp", func(t *testing.T) {
		err := newCodexStatusErrWithHeaders(http.StatusTooManyRequests, http.Header{"Retry-After": []string{"18446744074"}}, []byte(issue7017TurnRateLimitBody))
		retry := err.RetryAfter()
		if retry == nil || *retry <= 0 {
			t.Fatalf("huge Retry-After must clamp to a large positive duration, got %v", retry)
		}
	})

	t.Run("usage limit hint wins over header and stays quota", func(t *testing.T) {
		body := `{"error":{"type":"usage_limit_reached","code":"turn_rate_limit_exceeded","message":"usage","resets_in_seconds":3600}}`
		err := newCodexStatusErrWithHeaders(http.StatusTooManyRequests, http.Header{"Retry-After": []string{"30"}}, []byte(body))
		retry := err.RetryAfter()
		if retry == nil || *retry != time.Hour {
			t.Fatalf("usage_limit_reached reset must win over Retry-After header, got %v", retry)
		}
		if !err.IsCredentialScoped() {
			t.Fatalf("usage_limit_reached must stay credential scoped")
		}
		if err.IsTransientRateLimit() {
			t.Fatalf("usage_limit_reached must not be transient rate limit")
		}
	})
}

func TestIssue7017CapacityClassificationBoundaries(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "exact turn code", body: issue7017TurnRateLimitBody, want: true},
		{name: "code only", body: `{"error":{"code":"turn_rate_limit_exceeded"}}`, want: true},
		{name: "top level code", body: `{"code":"turn_rate_limit_exceeded"}`, want: true},
		{name: "wrapped body code", body: `{"body":{"error":{"code":"turn_rate_limit_exceeded"}}}`, want: true},
		{name: "code case and whitespace", body: `{"error":{"code":" Turn_Rate_Limit_Exceeded "}}`, want: true},
		{name: "code in message text only", body: `{"error":{"type":"server_error","message":"docs mention turn_rate_limit_exceeded"}}`, want: false},
		{name: "generic rate limit exceeded", body: `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Too many requests"}}`, want: false},
		{name: "rate limit type with capacity message", body: `{"error":{"type":"rate_limit_error","message":"Codex is at capacity, please try again later"}}`, want: true},
		{name: "other explicit code blocks message fallback", body: `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Codex is at capacity"}}`, want: false},
		{name: "other code blocks legacy model capacity via raw body", body: `{"model":"gpt-6.1-sol","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Codex is at capacity"}}`, want: false},
		{name: "explicit model capacity code", body: `{"error":{"code":"model_at_capacity","message":"busy"}}`, want: true},
		{name: "capacity message without rate type", body: `{"error":{"type":"server_error","message":"Codex is at capacity"}}`, want: false},
		{name: "message in nested param", body: `{"error":{"type":"rate_limit_error","param":"Codex is at capacity"}}`, want: false},
		{name: "selected model at capacity stays model capacity", body: `{"error":{"message":"Selected model is at capacity. Please try a different model."}}`, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := newCodexStatusErr(http.StatusTooManyRequests, []byte(tc.body))
			if got := err.IsTransientRateLimit(); got != tc.want {
				t.Fatalf("IsTransientRateLimit = %t, want %t", got, tc.want)
			}
			if tc.want && err.StatusCode() != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429", err.StatusCode())
			}
		})
	}
}

func TestIssue7017TurnRateLimitTerminalAndBootstrap(t *testing.T) {
	for _, payload := range []string{
		`{"type":"error","error":{"type":"rate_limit_error","code":"turn_rate_limit_exceeded","message":"Codex is at capacity, please try again later"}}`,
		`{"type":"response.failed","response":{"error":{"type":"rate_limit_error","code":"turn_rate_limit_exceeded","message":"Codex is at capacity, please try again later"}}}`,
	} {
		errTerminal, body, ok := codexTerminalStreamErr([]byte(payload))
		if !ok {
			t.Fatalf("terminal turn rate limit must be handled: %s", payload)
		}
		issue7017AssertTransient(t, errTerminal, nil)
		if !isCodexOverloadBootstrapFailure(body) {
			t.Fatalf("terminal turn rate limit must remain a bootstrap overload failure")
		}
	}
}

func TestIssue7017TurnRateLimitWebsocket(t *testing.T) {
	t.Run("error event with header", func(t *testing.T) {
		payload := `{"type":"error","status":429,"headers":{"Retry-After":"120"},"error":{"type":"rate_limit_error","code":"turn_rate_limit_exceeded","message":"Codex is at capacity, please try again later"}}`
		errParsed, ok := parseCodexWebsocketError([]byte(payload))
		if !ok {
			t.Fatal("websocket error was not recognized")
		}
		want := 120 * time.Second
		issue7017AssertTransient(t, errParsed, &want)
	})

	t.Run("nested body error", func(t *testing.T) {
		payload := `{"type":"error","status":429,"body":{"error":{"type":"rate_limit_error","code":"turn_rate_limit_exceeded","message":"Codex is at capacity, please try again later"}}}`
		errParsed, ok := parseCodexWebsocketError([]byte(payload))
		if !ok {
			t.Fatal("websocket error was not recognized")
		}
		issue7017AssertTransient(t, errParsed, nil)
	})
}
