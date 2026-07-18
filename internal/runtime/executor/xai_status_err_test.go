package executor

import (
	"net/http"
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
	if err.RetryAfter() != nil {
		t.Fatalf("expected nil RetryAfter for 400, got %v", *err.RetryAfter())
	}
}
