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

func TestParseAntigravityRetryDelay_NegativeCases(t *testing.T) {
	testCases := []struct {
		name string
		body []byte
	}{
		{"nil body", nil},
		{"empty body", []byte{}},
		{"no details field", []byte(`{"error": {"code": 429, "message": "Resource exhausted"}}`)},
		{"empty details array", []byte(`{"error": {"details": []}}`)},
		{"wrong @type", []byte(`{"error": {"details": [{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "RATE_LIMIT"}]}}`)},
		{"empty retryDelay", []byte(`{"error": {"details": [{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": ""}]}}`)},
		{"invalid duration format", []byte(`{"error": {"details": [{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "invalid"}]}}`)},
		{"zero duration", []byte(`{"error": {"details": [{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "0s"}]}}`)},
		{"negative duration", []byte(`{"error": {"details": [{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "-5s"}]}}`)},
		{"invalid json", []byte(`{invalid json`)},
		{"details not array", []byte(`{"error": {"details": "not an array"}}`)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := parseAntigravityRetryDelay(tc.body)
			if result != nil {
				t.Errorf("Expected nil, got %v", *result)
			}
		})
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
