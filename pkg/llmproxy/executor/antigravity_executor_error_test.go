package executor

import (
	"net/http"
	"strings"
	"testing"
)

func TestAntigravityErrorMessage_AddsLicenseHintForKnown403(t *testing.T) {
	body := []byte(`{"error":{"code":403,"message":"SUBSCRIPTION_REQUIRED: Gemini Code Assist license missing","status":"PERMISSION_DENIED"}}`)
	msg := antigravityErrorMessage(http.StatusForbidden, body)
	if !strings.Contains(msg, "Hint:") {
		t.Fatalf("expected hint in message, got %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "gemini code assist license") {
		t.Fatalf("expected license text in message, got %q", msg)
	}
}

func TestAntigravityErrorMessage_NoHintForNon403(t *testing.T) {
	body := []byte(`{"error":"bad request"}`)
	msg := antigravityErrorMessage(http.StatusBadRequest, body)
	if strings.Contains(msg, "Hint:") {
		t.Fatalf("did not expect hint for non-403, got %q", msg)
	}
}
<<<<<<< HEAD
=======

func TestAntigravityErrorMessage_DoesNotDuplicateHint(t *testing.T) {
	body := []byte(`{"error":{"code":403,"message":"PERMISSION_DENIED: Gemini Code Assist license missing. Hint: The current Google project/account does not have a Gemini Code Assist license. Re-run --antigravity-login with a licensed account/project, or switch providers.","status":"PERMISSION_DENIED"}}`)
	msg := antigravityErrorMessage(http.StatusForbidden, body)
	if strings.Count(msg, "Hint:") != 1 {
		t.Fatalf("expected one hint marker, got %q", msg)
	}
}

func TestAntigravityShouldRetryNoCapacity_NestedCapacityMarker(t *testing.T) {
	body := []byte(`{"error":{"code":503,"message":"Resource exhausted: no capacity available right now","status":"UNAVAILABLE"}}`)
	if !antigravityShouldRetryNoCapacity(http.StatusServiceUnavailable, body) {
		t.Fatalf("expected retry on nested no-capacity marker")
	}
}

func TestAntigravityShouldRetryNoCapacity_DoesNotRetryUnrelated503(t *testing.T) {
	body := []byte(`{"error":{"code":503,"message":"service unavailable","status":"UNAVAILABLE"}}`)
	if antigravityShouldRetryNoCapacity(http.StatusServiceUnavailable, body) {
		t.Fatalf("did not expect retry for unrelated 503")
	}
}
>>>>>>> archive/pr-234-head-20260223
