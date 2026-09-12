package executor

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestXAIStatusErr_FreeUsageExhaustedSets24hRetryAfter(t *testing.T) {
	body := []byte(`{"code":"subscription:free-usage-exhausted","error":"You've used all the included free usage for model grok-4.5-build-free for now. Usage resets over a rolling 24-hour window — tokens (actual/limit): 1065387/1000000."}`)
	err := xaiStatusErr(http.StatusTooManyRequests, body)
	if err.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", err.StatusCode())
	}
	if err.RetryAfter() == nil {
		t.Fatal("expected RetryAfter for free-usage-exhausted")
	}
	if *err.RetryAfter() != 24*time.Hour {
		t.Fatalf("RetryAfter = %v, want 24h", *err.RetryAfter())
	}
}

func TestXAIStatusErr_Generic429HasNoRetryAfter(t *testing.T) {
	body := []byte(`{"code":"rate_limit","error":"too many requests"}`)
	err := xaiStatusErr(http.StatusTooManyRequests, body)
	if err.RetryAfter() != nil {
		t.Fatalf("expected nil RetryAfter for generic 429, got %v", *err.RetryAfter())
	}
}

func TestXAIStatusErr_Non429Unchanged(t *testing.T) {
	body := []byte(`{"error":"nope"}`)
	err := xaiStatusErr(http.StatusBadRequest, body)
	if err.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", err.StatusCode())
	}
	if err.RetryAfter() != nil {
		t.Fatalf("expected nil RetryAfter for 400, got %v", *err.RetryAfter())
	}
}

func TestXAIStatusErr_BadCredentials403RemapsToUnauthorized(t *testing.T) {
	body := []byte(`{"code":"unauthenticated:bad-credentials","error":"The OAuth2 access token could not be validated."}`)
	err := xaiStatusErr(http.StatusForbidden, body)
	if err.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", err.StatusCode())
	}
	if !strings.Contains(err.Error(), "bad-credentials") {
		t.Fatalf("error body should be preserved, got %q", err.Error())
	}
	if err.RetryAfter() != nil {
		t.Fatalf("expected nil RetryAfter for bad-credentials, got %v", *err.RetryAfter())
	}
}

func TestXAIStatusErr_BadCredentialsByMessageOnly(t *testing.T) {
	body := []byte(`{"error":"The OAuth2 access token could not be validated."}`)
	err := xaiStatusErr(http.StatusForbidden, body)
	if err.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", err.StatusCode())
	}
}

func TestXAIStatusErr_BadCredentialsNestedErrorCode(t *testing.T) {
	body := []byte(`{"type":"error","status":403,"error":{"code":"unauthenticated:bad-credentials","message":"The OAuth2 access token could not be validated."}}`)
	err := xaiStatusErr(http.StatusForbidden, body)
	if err.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", err.StatusCode())
	}
}

func TestXAIStatusErr_Generic403Unchanged(t *testing.T) {
	body := []byte(`{"code":"permission_denied","error":"model access is not allowed for this account"}`)
	err := xaiStatusErr(http.StatusForbidden, body)
	if err.StatusCode() != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", err.StatusCode())
	}
	if err.RetryAfter() != nil {
		t.Fatalf("expected nil RetryAfter for generic 403, got %v", *err.RetryAfter())
	}
}

func TestXAIStatusErr_EmptyBodyForbiddenUnchanged(t *testing.T) {
	err := xaiStatusErr(http.StatusForbidden, nil)
	if err.StatusCode() != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", err.StatusCode())
	}
}
