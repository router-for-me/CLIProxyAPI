package executor

import (
	"testing"
	"time"
)

func TestParseAntigravityRetryDelay_Valid429Response(t *testing.T) {
	body := []byte(`{
		"error": {
			"code": 429,
			"message": "Resource exhausted",
			"details": [
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "10.5s"}
			]
		}
	}`)

	result := parseAntigravityRetryDelay(body)
	if result == nil {
		t.Fatal("Expected non-nil duration")
	}
	expected := 10*time.Second + 500*time.Millisecond
	if *result != expected {
		t.Errorf("Expected %v, got %v", expected, *result)
	}
}

func TestParseAntigravityRetryDelay_LargeDuration(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "10627.493230411s"}
			]
		}
	}`)

	result := parseAntigravityRetryDelay(body)
	if result == nil {
		t.Fatal("Expected non-nil duration")
	}
	// Verify it's roughly 2.95 hours
	if result.Hours() < 2.9 || result.Hours() > 3.0 {
		t.Errorf("Expected ~2.95 hours, got %v", *result)
	}
}

func TestParseAntigravityRetryDelay_EmptyBody(t *testing.T) {
	result := parseAntigravityRetryDelay(nil)
	if result != nil {
		t.Errorf("Expected nil for empty body, got %v", *result)
	}

	result = parseAntigravityRetryDelay([]byte{})
	if result != nil {
		t.Errorf("Expected nil for empty byte slice, got %v", *result)
	}
}

func TestParseAntigravityRetryDelay_NoErrorDetails(t *testing.T) {
	body := []byte(`{
		"error": {
			"code": 429,
			"message": "Resource exhausted"
		}
	}`)

	result := parseAntigravityRetryDelay(body)
	if result != nil {
		t.Errorf("Expected nil when no details field, got %v", *result)
	}
}

func TestParseAntigravityRetryDelay_EmptyDetails(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": []
		}
	}`)

	result := parseAntigravityRetryDelay(body)
	if result != nil {
		t.Errorf("Expected nil for empty details array, got %v", *result)
	}
}

func TestParseAntigravityRetryDelay_WrongType(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "RATE_LIMIT"}
			]
		}
	}`)

	result := parseAntigravityRetryDelay(body)
	if result != nil {
		t.Errorf("Expected nil when @type doesn't match RetryInfo, got %v", *result)
	}
}

func TestParseAntigravityRetryDelay_EmptyRetryDelay(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": ""}
			]
		}
	}`)

	result := parseAntigravityRetryDelay(body)
	if result != nil {
		t.Errorf("Expected nil for empty retryDelay, got %v", *result)
	}
}

func TestParseAntigravityRetryDelay_InvalidDurationFormat(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "invalid"}
			]
		}
	}`)

	result := parseAntigravityRetryDelay(body)
	if result != nil {
		t.Errorf("Expected nil for invalid duration format, got %v", *result)
	}
}

func TestParseAntigravityRetryDelay_ZeroDuration(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "0s"}
			]
		}
	}`)

	result := parseAntigravityRetryDelay(body)
	if result != nil {
		t.Errorf("Expected nil for zero duration, got %v", *result)
	}
}

func TestParseAntigravityRetryDelay_NegativeDuration(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "-5s"}
			]
		}
	}`)

	result := parseAntigravityRetryDelay(body)
	if result != nil {
		t.Errorf("Expected nil for negative duration, got %v", *result)
	}
}

func TestParseAntigravityRetryDelay_MultipleDetails(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "RATE_LIMIT"},
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "30s"},
				{"@type": "type.googleapis.com/google.rpc.Help", "links": []}
			]
		}
	}`)

	result := parseAntigravityRetryDelay(body)
	if result == nil {
		t.Fatal("Expected non-nil duration")
	}
	expected := 30 * time.Second
	if *result != expected {
		t.Errorf("Expected %v, got %v", expected, *result)
	}
}

func TestParseAntigravityRetryDelay_InvalidJSON(t *testing.T) {
	body := []byte(`{invalid json`)

	result := parseAntigravityRetryDelay(body)
	if result != nil {
		t.Errorf("Expected nil for invalid JSON, got %v", *result)
	}
}

func TestParseAntigravityRetryDelay_DetailsNotArray(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": "not an array"
		}
	}`)

	result := parseAntigravityRetryDelay(body)
	if result != nil {
		t.Errorf("Expected nil when details is not an array, got %v", *result)
	}
}

func TestNewAntigravityStatusErr_429WithRetryDelay(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "60s"}
			]
		}
	}`)

	err := newAntigravityStatusErr(429, body)
	if err.code != 429 {
		t.Errorf("Expected code 429, got %d", err.code)
	}
	if err.retryAfter == nil {
		t.Fatal("Expected non-nil retryAfter for 429")
	}
	expected := 60 * time.Second
	if *err.retryAfter != expected {
		t.Errorf("Expected retryAfter %v, got %v", expected, *err.retryAfter)
	}
}

func TestNewAntigravityStatusErr_Non429(t *testing.T) {
	body := []byte(`{"error": {"message": "Internal error"}}`)

	err := newAntigravityStatusErr(500, body)
	if err.code != 500 {
		t.Errorf("Expected code 500, got %d", err.code)
	}
	if err.retryAfter != nil {
		t.Errorf("Expected nil retryAfter for non-429, got %v", *err.retryAfter)
	}
}

func TestNewAntigravityStatusErr_429WithoutRetryInfo(t *testing.T) {
	body := []byte(`{"error": {"message": "Rate limit exceeded"}}`)

	err := newAntigravityStatusErr(429, body)
	if err.code != 429 {
		t.Errorf("Expected code 429, got %d", err.code)
	}
	if err.retryAfter != nil {
		t.Errorf("Expected nil retryAfter when no RetryInfo, got %v", *err.retryAfter)
	}
}
