package auth

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func cooldownErrorBody(t *testing.T, err error) map[string]any {
	t.Helper()
	var payload map[string]any
	if errUnmarshal := json.Unmarshal([]byte(err.Error()), &payload); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal(Error()) error = %v; body=%s", errUnmarshal, err.Error())
	}
	body, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("Error() payload missing error object: %v", payload)
	}
	return body
}

func TestModelCooldownErrorShortCooldownStaysRetryable(t *testing.T) {
	err := NewModelCooldownError("gemini-4-argon-high", "antigravity", QuotaExhaustedCooldownThreshold)
	if IsQuotaExhaustedError(err) {
		t.Fatal("cooldown equal to the threshold must stay retryable")
	}
	mce := err.(*modelCooldownError)
	headers := mce.Headers()
	if got := headers.Get("Retry-After"); got != "300" {
		t.Fatalf("Retry-After = %q, want 300", got)
	}
	if got := headers.Get("X-Should-Retry"); got != "" {
		t.Fatalf("X-Should-Retry = %q, want empty for a retryable cooldown", got)
	}
	body := cooldownErrorBody(t, err)
	if got := body["code"]; got != "model_cooldown" {
		t.Fatalf("error.code = %v, want model_cooldown", got)
	}
	if _, ok := body["type"]; ok {
		t.Fatalf("error.type = %v, want absent for a retryable cooldown", body["type"])
	}
	if _, ok := body["retryable"]; ok {
		t.Fatalf("error.retryable = %v, want absent for a retryable cooldown", body["retryable"])
	}
}

func TestModelCooldownErrorLongCooldownReportsUsageLimit(t *testing.T) {
	resetIn := 140 * time.Hour
	before := time.Now().Add(resetIn).Unix()
	err := NewModelCooldownError("gemini-4-argon-high", "antigravity", resetIn)
	after := time.Now().Add(resetIn).Unix()

	if !IsQuotaExhaustedError(err) {
		t.Fatal("IsQuotaExhaustedError() = false for a 140h cooldown")
	}
	mce := err.(*modelCooldownError)
	if mce.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("StatusCode() = %d, want 429", mce.StatusCode())
	}
	headers := mce.Headers()
	if got := headers.Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q, want absent: clients would sleep through the whole window", got)
	}
	if got := headers.Get("X-Should-Retry"); got != "false" {
		t.Fatalf("X-Should-Retry = %q, want false", got)
	}
	if got := SafeResponseHeaders(err).Get("X-Should-Retry"); got != "false" {
		t.Fatalf("SafeResponseHeaders X-Should-Retry = %q, want false", got)
	}

	body := cooldownErrorBody(t, err)
	if got := body["type"]; got != "usage_limit_reached" {
		t.Fatalf("error.type = %v, want usage_limit_reached", got)
	}
	if got := body["code"]; got != "model_cooldown" {
		t.Fatalf("error.code = %v, want model_cooldown for existing consumers", got)
	}
	if got := body["retryable"]; got != false {
		t.Fatalf("error.retryable = %v, want false", got)
	}
	resetsAt, ok := body["resets_at"].(float64)
	if !ok || int64(resetsAt) < before || int64(resetsAt) > after {
		t.Fatalf("error.resets_at = %v, want unix time in [%d, %d]", body["resets_at"], before, after)
	}
	if got := body["reset_seconds"]; got != float64(resetIn/time.Second) {
		t.Fatalf("error.reset_seconds = %v, want %d", got, int(resetIn/time.Second))
	}
	message, _ := body["message"].(string)
	if message == "" || message == "All credentials for model gemini-4-argon-high are cooling down via provider antigravity" {
		t.Fatalf("error.message = %q, want a usage-limit message", message)
	}
}

func TestIsQuotaExhaustedErrorRejectsOtherErrors(t *testing.T) {
	if IsQuotaExhaustedError(nil) {
		t.Fatal("IsQuotaExhaustedError(nil) = true")
	}
	if IsQuotaExhaustedError(&Error{Code: "auth_unavailable", HTTPStatus: http.StatusServiceUnavailable}) {
		t.Fatal("IsQuotaExhaustedError(auth_unavailable) = true")
	}
}
