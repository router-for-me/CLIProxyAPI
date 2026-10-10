package executor

import (
	"net/http"
	"testing"
	"time"
)

func TestParseClaudeRetryAfter_DelaySeconds(t *testing.T) {
	header := http.Header{}
	header.Set("Retry-After", "30")

	d := parseClaudeRetryAfter(header)
	if d == nil {
		t.Fatal("expected non-nil duration")
	}
	if *d != 30*time.Second {
		t.Errorf("expected 30s, got %v", *d)
	}
}

func TestParseClaudeRetryAfter_DecimalSeconds(t *testing.T) {
	header := http.Header{}
	header.Set("Retry-After", "1.5")

	d := parseClaudeRetryAfter(header)
	if d == nil {
		t.Fatal("expected non-nil duration")
	}
	if *d != 1500*time.Millisecond {
		t.Errorf("expected 1.5s, got %v", *d)
	}
}

func TestParseClaudeRetryAfter_HTTPDate(t *testing.T) {
	future := time.Now().Add(60 * time.Second).UTC()
	header := http.Header{}
	header.Set("Retry-After", future.Format(http.TimeFormat))

	d := parseClaudeRetryAfter(header)
	if d == nil {
		t.Fatal("expected non-nil duration for HTTP-date")
	}
	if *d < 55*time.Second || *d > 65*time.Second {
		t.Errorf("expected ~60s, got %v", *d)
	}
}

func TestParseClaudeRetryAfter_EmptyHeader(t *testing.T) {
	header := http.Header{}

	d := parseClaudeRetryAfter(header)
	if d != nil {
		t.Errorf("expected nil for missing header, got %v", *d)
	}
}

func TestParseClaudeRetryAfter_NegativeValue(t *testing.T) {
	header := http.Header{}
	header.Set("Retry-After", "-5")

	d := parseClaudeRetryAfter(header)
	if d != nil {
		t.Errorf("expected nil for negative value, got %v", *d)
	}
}

func TestParseClaudeRetryAfter_ZeroValue(t *testing.T) {
	header := http.Header{}
	header.Set("Retry-After", "0")

	d := parseClaudeRetryAfter(header)
	if d != nil {
		t.Errorf("expected nil for zero value, got %v", *d)
	}
}

func TestParseClaudeRetryAfter_InvalidValue(t *testing.T) {
	header := http.Header{}
	header.Set("Retry-After", "not-a-number")

	d := parseClaudeRetryAfter(header)
	if d != nil {
		t.Errorf("expected nil for invalid value, got %v", *d)
	}
}

func TestParseClaudeRetryAfter_PastHTTPDate(t *testing.T) {
	past := time.Now().Add(-60 * time.Second).UTC()
	header := http.Header{}
	header.Set("Retry-After", past.Format(http.TimeFormat))

	d := parseClaudeRetryAfter(header)
	if d != nil {
		t.Errorf("expected nil for past HTTP-date, got %v", *d)
	}
}
