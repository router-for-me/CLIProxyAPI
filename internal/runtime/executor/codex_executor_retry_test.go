package executor

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseCodexRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	t.Run("resets_in_seconds", func(t *testing.T) {
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":123}}`)
		retryAfter := parseCodexRetryAfter(http.StatusTooManyRequests, body, now)
		if retryAfter == nil {
			t.Fatalf("expected retryAfter, got nil")
		}
		if *retryAfter != 123*time.Second {
			t.Fatalf("retryAfter = %v, want %v", *retryAfter, 123*time.Second)
		}
	})

	t.Run("prefers resets_at", func(t *testing.T) {
		resetAt := now.Add(5 * time.Minute).Unix()
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_at":` + itoa(resetAt) + `,"resets_in_seconds":1}}`)
		retryAfter := parseCodexRetryAfter(http.StatusTooManyRequests, body, now)
		if retryAfter == nil {
			t.Fatalf("expected retryAfter, got nil")
		}
		if *retryAfter != 5*time.Minute {
			t.Fatalf("retryAfter = %v, want %v", *retryAfter, 5*time.Minute)
		}
	})

	t.Run("fallback when resets_at is past", func(t *testing.T) {
		resetAt := now.Add(-1 * time.Minute).Unix()
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_at":` + itoa(resetAt) + `,"resets_in_seconds":77}}`)
		retryAfter := parseCodexRetryAfter(http.StatusTooManyRequests, body, now)
		if retryAfter == nil {
			t.Fatalf("expected retryAfter, got nil")
		}
		if *retryAfter != 77*time.Second {
			t.Fatalf("retryAfter = %v, want %v", *retryAfter, 77*time.Second)
		}
	})

	t.Run("non-429 status code", func(t *testing.T) {
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":30}}`)
		if got := parseCodexRetryAfter(http.StatusBadRequest, body, now); got != nil {
			t.Fatalf("expected nil for non-429, got %v", *got)
		}
	})

	t.Run("non usage_limit_reached error type", func(t *testing.T) {
		body := []byte(`{"error":{"type":"server_error","resets_in_seconds":30}}`)
		if got := parseCodexRetryAfter(http.StatusTooManyRequests, body, now); got != nil {
			t.Fatalf("expected nil for non-usage_limit_reached, got %v", *got)
		}
	})
}

func TestParseCodexSSEError(t *testing.T) {
	t.Run("context_length_exceeded maps to invalid_request_error bad_request", func(t *testing.T) {
		line := []byte(`{"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"},"sequence_number":2}`)
		got, ok := parseCodexSSEError(line)
		if !ok {
			t.Fatalf("expected parser to handle codex SSE error")
		}
		if got.code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", got.code, http.StatusBadRequest)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(got.msg), &payload); err != nil {
			t.Fatalf("unmarshal wrapped message: %v", err)
		}
		errObj, ok := payload["error"].(map[string]any)
		if !ok {
			t.Fatalf("payload['error'] is not of type map[string]any")
		}
		if errObj["type"] != "invalid_request_error" {
			t.Fatalf("error.type = %v, want invalid_request_error", errObj["type"])
		}
		if errObj["code"] != "context_length_exceeded" {
			t.Fatalf("error.code = %v, want context_length_exceeded", errObj["code"])
		}
		msg, _ := errObj["message"].(string)
		if !strings.Contains(strings.ToLower(msg), "context window") {
			t.Fatalf("error.message = %q, want context window wording", msg)
		}
	})

	t.Run("rate_limit keeps 429", func(t *testing.T) {
		line := []byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"rate limited"}}`)
		got, ok := parseCodexSSEError(line)
		if !ok {
			t.Fatalf("expected parser to handle codex SSE rate limit error")
		}
		if got.code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want %d", got.code, http.StatusTooManyRequests)
		}
	})

	t.Run("response.failed with nested response.error is parsed", func(t *testing.T) {
		line := []byte(`{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}}`)
		got, ok := parseCodexSSEError(line)
		if !ok {
			t.Fatalf("expected parser to handle response.failed event")
		}
		if got.code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", got.code, http.StatusBadRequest)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(got.msg), &payload); err != nil {
			t.Fatalf("unmarshal wrapped message: %v", err)
		}
		errObj, ok := payload["error"].(map[string]any)
		if !ok {
			t.Fatalf("payload['error'] is not of type map[string]any")
		}
		if errObj["code"] != "context_length_exceeded" {
			t.Fatalf("error.code = %v, want context_length_exceeded", errObj["code"])
		}
	})

	t.Run("non-error event ignored", func(t *testing.T) {
		line := []byte(`{"type":"response.completed"}`)
		if _, ok := parseCodexSSEError(line); ok {
			t.Fatalf("expected non-error event to be ignored")
		}
	})
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
