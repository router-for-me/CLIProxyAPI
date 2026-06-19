package helps

import (
	"net/http"
	"testing"
)

func TestDetectUpstreamErrorBody_InsufficientQuota(t *testing.T) {
	body := []byte(`{"error":{"message":"Insufficient quota.","type":"insufficient_quota"}}`)
	err := DetectUpstreamErrorBody(http.StatusOK, body)
	if err == nil {
		t.Fatal("expected error for insufficient_quota body on HTTP 200")
	}
	if err.Code != http.StatusPaymentRequired {
		t.Fatalf("expected status 402, got %d", err.Code)
	}
	if err.StatusCode() != http.StatusPaymentRequired {
		t.Fatalf("expected StatusCode()=402, got %d", err.StatusCode())
	}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestDetectUpstreamErrorBody_RateLimit(t *testing.T) {
	body := []byte(`{"error":{"message":"Too many requests.","type":"rate_limit_exceeded"}}`)
	err := DetectUpstreamErrorBody(http.StatusOK, body)
	if err == nil {
		t.Fatal("expected error for rate_limit body")
	}
	if err.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d", err.Code)
	}
}

func TestDetectUpstreamErrorBody_StringError(t *testing.T) {
	body := []byte(`{"error":"Insufficient quota."}`)
	err := DetectUpstreamErrorBody(http.StatusOK, body)
	if err == nil {
		t.Fatal("expected error for string error body")
	}
	if err.Code != http.StatusPaymentRequired {
		t.Fatalf("expected status 402, got %d", err.Code)
	}
}

func TestDetectUpstreamErrorBody_NoError(t *testing.T) {
	body := []byte(`{"id":"resp_1","choices":[{"message":{"content":"ok"}}]}`)
	err := DetectUpstreamErrorBody(http.StatusOK, body)
	if err != nil {
		t.Fatalf("expected nil error for success body, got %v", err)
	}
}

func TestDetectUpstreamErrorBody_NonJSON(t *testing.T) {
	body := []byte("data: {\"type\":\"response.completed\"}\n\n")
	err := DetectUpstreamErrorBody(http.StatusOK, body)
	if err != nil {
		t.Fatalf("expected nil error for SSE body, got %v", err)
	}
}

func TestDetectUpstreamErrorBody_EmptyBody(t *testing.T) {
	if err := DetectUpstreamErrorBody(http.StatusOK, nil); err != nil {
		t.Fatalf("expected nil for empty body, got %v", err)
	}
}

func TestDetectUpstreamErrorBody_EmptyErrorObject(t *testing.T) {
	body := []byte(`{"error":{}}`)
	if err := DetectUpstreamErrorBody(http.StatusOK, body); err != nil {
		t.Fatalf("expected nil for empty error object, got %v", err)
	}
}

func TestDetectUpstreamErrorBody_UnknownFallsBackToBadGateway(t *testing.T) {
	body := []byte(`{"error":{"message":"something broke","type":"weird_error"}}`)
	err := DetectUpstreamErrorBody(http.StatusOK, body)
	if err == nil {
		t.Fatal("expected error for unknown error type on HTTP 200")
	}
	if err.Code != http.StatusBadGateway {
		t.Fatalf("expected fallback status 502, got %d", err.Code)
	}
}

func TestDetectUpstreamErrorBody_PreservesNon2xxStatus(t *testing.T) {
	body := []byte(`{"error":{"message":"something broke","type":"weird_error"}}`)
	err := DetectUpstreamErrorBody(http.StatusInternalServerError, body)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Code != http.StatusInternalServerError {
		t.Fatalf("expected preserved status 500, got %d", err.Code)
	}
}
