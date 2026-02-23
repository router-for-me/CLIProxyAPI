package executor

import (
	"testing"
	"time"
)

func TestParseRetryDelay_MessageDuration(t *testing.T) {
	t.Parallel()

	body := []byte(`{"error":{"message":"Quota exceeded. Your quota will reset after 1.5s."}}`)
	got, err := parseRetryDelay(body)
	if err != nil {
		t.Fatalf("parseRetryDelay returned error: %v", err)
	}
	if got == nil {
		t.Fatal("parseRetryDelay returned nil duration")
	}
	if *got != 1500*time.Millisecond {
		t.Fatalf("parseRetryDelay = %v, want %v", *got, 1500*time.Millisecond)
	}
}

func TestParseRetryDelay_MessageMilliseconds(t *testing.T) {
	t.Parallel()

	body := []byte(`{"error":{"message":"Please retry after 250ms."}}`)
	got, err := parseRetryDelay(body)
	if err != nil {
		t.Fatalf("parseRetryDelay returned error: %v", err)
	}
	if got == nil {
		t.Fatal("parseRetryDelay returned nil duration")
	}
	if *got != 250*time.Millisecond {
		t.Fatalf("parseRetryDelay = %v, want %v", *got, 250*time.Millisecond)
	}
}

func TestParseRetryDelay_PrefersRetryInfo(t *testing.T) {
	t.Parallel()

	body := []byte(`{"error":{"message":"Your quota will reset after 99s.","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"2s"}]}}`)
	got, err := parseRetryDelay(body)
	if err != nil {
		t.Fatalf("parseRetryDelay returned error: %v", err)
	}
	if got == nil {
		t.Fatal("parseRetryDelay returned nil duration")
	}
	if *got != 2*time.Second {
		t.Fatalf("parseRetryDelay = %v, want %v", *got, 2*time.Second)
	}
}
