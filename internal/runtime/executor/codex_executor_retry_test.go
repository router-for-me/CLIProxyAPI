package executor

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestParseCodexRetryAfter_ResetsInSeconds(t *testing.T) {
	body := []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":123}}`)
	retryAfter := parseCodexRetryAfter(http.StatusTooManyRequests, body)
	if retryAfter == nil {
		t.Fatalf("expected retryAfter, got nil")
	}
	if *retryAfter != 123*time.Second {
		t.Fatalf("retryAfter = %v, want %v", *retryAfter, 123*time.Second)
	}
}

func TestParseCodexRetryAfter_PrefersResetsAt(t *testing.T) {
	resetAt := time.Now().Add(5 * time.Minute).Unix()
	body := []byte(`{"error":{"type":"usage_limit_reached","resets_at":` + itoa(resetAt) + `,"resets_in_seconds":1}}`)
	retryAfter := parseCodexRetryAfter(http.StatusTooManyRequests, body)
	if retryAfter == nil {
		t.Fatalf("expected retryAfter, got nil")
	}
	if *retryAfter < 4*time.Minute || *retryAfter > 6*time.Minute {
		t.Fatalf("retryAfter = %v, want around 5m", *retryAfter)
	}
}

func TestParseCodexRetryAfter_FallbackWhenResetsAtPast(t *testing.T) {
	resetAt := time.Now().Add(-1 * time.Minute).Unix()
	body := []byte(`{"error":{"type":"usage_limit_reached","resets_at":` + itoa(resetAt) + `,"resets_in_seconds":77}}`)
	retryAfter := parseCodexRetryAfter(http.StatusTooManyRequests, body)
	if retryAfter == nil {
		t.Fatalf("expected retryAfter, got nil")
	}
	if *retryAfter != 77*time.Second {
		t.Fatalf("retryAfter = %v, want %v", *retryAfter, 77*time.Second)
	}
}

func TestParseCodexRetryAfter_NonApplicableReturnsNil(t *testing.T) {
	body := []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":30}}`)
	if got := parseCodexRetryAfter(http.StatusBadRequest, body); got != nil {
		t.Fatalf("expected nil for non-429, got %v", *got)
	}
	body = []byte(`{"error":{"type":"server_error","resets_in_seconds":30}}`)
	if got := parseCodexRetryAfter(http.StatusTooManyRequests, body); got != nil {
		t.Fatalf("expected nil for non-usage_limit_reached, got %v", *got)
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
