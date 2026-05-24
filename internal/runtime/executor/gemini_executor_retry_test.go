package executor

import (
	"testing"
	"time"
)

func TestParseRetryDelay_RetryInfo(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{
					"@type": "type.googleapis.com/google.rpc.RetryInfo",
					"retryDelay": "5s"
				}
			]
		}
	}`)

	d, err := parseRetryDelay(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *d != 5*time.Second {
		t.Fatalf("expected 5s, got %v", *d)
	}
}

func TestParseRetryDelay_RetryInfoDecimal(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{
					"@type": "type.googleapis.com/google.rpc.RetryInfo",
					"retryDelay": "0.847655010s"
				}
			]
		}
	}`)

	d, err := parseRetryDelay(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *d == 0 {
		t.Fatal("expected non-zero duration")
	}
}

func TestParseRetryDelay_QuotaResetDelay(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{
					"@type": "type.googleapis.com/google.rpc.ErrorInfo",
					"metadata": {
						"quotaResetDelay": "373ms"
					}
				}
			]
		}
	}`)

	d, err := parseRetryDelay(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *d != 373*time.Millisecond {
		t.Fatalf("expected 373ms, got %v", *d)
	}
}

func TestParseRetryDelay_MessageFallback(t *testing.T) {
	body := []byte(`{
		"error": {
			"message": "Your quota will reset after 60s."
		}
	}`)

	d, err := parseRetryDelay(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *d != 60*time.Second {
		t.Fatalf("expected 60s, got %v", *d)
	}
}

func TestParseRetryDelay_NoRetryInfo(t *testing.T) {
	body := []byte(`{"error": {"message": "some other error"}}`)

	d, err := parseRetryDelay(body)
	if err == nil {
		t.Fatal("expected error for missing RetryInfo")
	}
	if d != nil {
		t.Fatalf("expected nil duration, got %v", *d)
	}
}

func TestParseRetryDelay_EmptyBody(t *testing.T) {
	body := []byte(`{}`)

	d, err := parseRetryDelay(body)
	if err == nil {
		t.Fatal("expected error for empty body")
	}
	if d != nil {
		t.Fatalf("expected nil duration, got %v", *d)
	}
}
