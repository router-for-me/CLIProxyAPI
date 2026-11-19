package executor

import (
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Issue 7691: a Codex upstream 429 with body {"detail":"Rate limit exceeded"}
// (no code field, no quota signal) is recorded as a quota failure, cooling
// the whole credential so every following request fails locally with an
// instant 429. It must be classified as a transient per-model rate limit,
// like the turn_rate_limit_exceeded fix for issue 7017, not as quota.
const issue7691DetailRateLimitBody = `{"detail":"Rate limit exceeded"}`

func TestIssue7691DetailRateLimitHTTPIsTransient(t *testing.T) {
	err := newCodexStatusErr(http.StatusTooManyRequests, []byte(issue7691DetailRateLimitBody))
	if err.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", err.StatusCode())
	}
	if !err.IsTransientRateLimit() {
		t.Fatalf("detail rate limit must be transient rate limit, err=%v", err)
	}
	if err.IsCredentialScoped() {
		t.Fatalf("detail rate limit must not be credential scoped, err=%v", err)
	}
	// No Retry-After header was present, so no hint is expected.
	if got := err.RetryAfter(); got != nil {
		t.Fatalf("RetryAfter = %v, want nil without a header", *got)
	}
}

func TestIssue7691DetailRateLimitHTTPHonorsRetryAfterHeader(t *testing.T) {
	headers := http.Header{"Retry-After": []string{"45"}}
	err := newCodexStatusErrWithHeaders(http.StatusTooManyRequests, headers, []byte(issue7691DetailRateLimitBody))
	if !err.IsTransientRateLimit() {
		t.Fatalf("detail rate limit must be transient rate limit, err=%v", err)
	}
	if err.IsCredentialScoped() {
		t.Fatalf("detail rate limit must not be credential scoped, err=%v", err)
	}
	retry := err.RetryAfter()
	if retry == nil || *retry != 45*time.Second {
		t.Fatalf("RetryAfter = %v, want 45s", retry)
	}
}

func TestIssue7691DetailRateLimitClassificationBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		transient bool
		scoped    bool
	}{
		{name: "exact detail body", body: issue7691DetailRateLimitBody, transient: true},
		{name: "detail case insensitive", body: `{"detail":"rate limit exceeded"}`, transient: true},
		{name: "detail nested under error", body: `{"error":{"detail":"Rate limit exceeded"}}`, transient: true},
		{name: "detail is usage wording", body: `{"detail":"You've hit your usage limit."}`, transient: false},
		{name: "detail with usage limit type wins as quota", body: `{"detail":"Rate limit exceeded","error":{"type":"usage_limit_reached","resets_in_seconds":3600}}`, transient: false, scoped: true},
		{name: "detail with reset signal stays quota", body: `{"detail":"Rate limit exceeded","resets_in_seconds":3600}`, transient: false},
		{name: "explicit other code blocks detail fallback", body: `{"detail":"Rate limit exceeded","code":"rate_limit_exceeded"}`, transient: false},
		{name: "generic rate limit code stays generic", body: `{"error":{"code":"rate_limit_exceeded","message":"Rate limit exceeded"}}`, transient: false},
		{name: "quota body stays credential quota", body: `{"error":{"type":"usage_limit_reached","message":"You've hit your usage limit.","resets_in_seconds":3600}}`, transient: false, scoped: true},
		{name: "turn rate limit still transient", body: issue7017TurnRateLimitBody, transient: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := newCodexStatusErr(http.StatusTooManyRequests, []byte(tc.body))
			if got := err.IsTransientRateLimit(); got != tc.transient {
				t.Fatalf("IsTransientRateLimit = %t, want %t (err=%v)", got, tc.transient, err)
			}
			if got := err.IsCredentialScoped(); got != tc.scoped {
				t.Fatalf("IsCredentialScoped = %t, want %t (err=%v)", got, tc.scoped, err)
			}
			if err.StatusCode() != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429", err.StatusCode())
			}
		})
	}
}

func TestIssue7691DetailRateLimitTerminalAndWebsocket(t *testing.T) {
	t.Run("terminal error event with detail under error", func(t *testing.T) {
		payload := `{"type":"error","error":{"detail":"Rate limit exceeded"}}`
		errTerminal, body, ok := codexTerminalStreamErr([]byte(payload))
		if !ok {
			t.Fatal("terminal detail rate limit must be handled")
		}
		if !errTerminal.IsTransientRateLimit() {
			t.Fatalf("terminal detail rate limit must be transient, err=%v", errTerminal)
		}
		if errTerminal.IsCredentialScoped() {
			t.Fatalf("terminal detail rate limit must not be credential scoped, err=%v", errTerminal)
		}
		// Like the #7017 turn-rate rejection, the transient classification
		// inherits bootstrap failover: the conductor may transparently
		// retry on another credential instead of returning the 429.
		if !isCodexOverloadBootstrapFailure(body) {
			t.Fatalf("terminal detail rate limit must remain a bootstrap overload failure")
		}
	})

	t.Run("terminal error event with top level detail", func(t *testing.T) {
		payload := `{"type":"error","detail":"Rate limit exceeded"}`
		errTerminal, body, ok := codexTerminalStreamErr([]byte(payload))
		if !ok {
			t.Fatal("terminal top-level detail rate limit must be handled")
		}
		if !errTerminal.IsTransientRateLimit() {
			t.Fatalf("terminal top-level detail rate limit must be transient, err=%v", errTerminal)
		}
		if !isCodexOverloadBootstrapFailure(body) {
			t.Fatalf("terminal top-level detail rate limit must remain a bootstrap overload failure")
		}
	})

	t.Run("websocket error event with top level detail", func(t *testing.T) {
		payload := `{"type":"error","status":429,"detail":"Rate limit exceeded"}`
		errParsed, ok := parseCodexWebsocketError([]byte(payload))
		if !ok {
			t.Fatal("websocket detail rate limit was not recognized")
		}
		var transient cliproxyexecutor.TransientRateLimitError
		if !errors.As(errParsed, &transient) || transient == nil || !transient.IsTransientRateLimit() {
			t.Fatalf("websocket detail rate limit must be transient, err=%v", errParsed)
		}
		var scoped interface{ IsCredentialScoped() bool }
		if errors.As(errParsed, &scoped) && scoped.IsCredentialScoped() {
			t.Fatalf("websocket detail rate limit must not be credential scoped, err=%v", errParsed)
		}
	})
}

func TestIssue7691DetailRateLimitReachesConductorAsTransient(t *testing.T) {
	err := newCodexStatusErr(http.StatusTooManyRequests, []byte(issue7691DetailRateLimitBody))
	resultErr := cliproxyauth.ResultErrorFromError(err)
	if resultErr == nil || resultErr.Code != cliproxyauth.ErrorCodeTransientRateLimit {
		t.Fatalf("conductor result error code = %v, want %q", resultErr, cliproxyauth.ErrorCodeTransientRateLimit)
	}
	if resultErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("conductor result error status = %d, want 429", resultErr.HTTPStatus)
	}
}
