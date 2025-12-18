package ratelimit

import (
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNewRateLimitHandler(t *testing.T) {
	handler := NewRateLimitHandler()
	if handler == nil {
		t.Fatal("NewRateLimitHandler returned nil")
	}
}

func TestHandleRateLimitDetects429(t *testing.T) {
	handler := NewRateLimitHandler()
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{},
	}

	retryAfter, shouldRetry := handler.HandleRateLimit(resp)
	if !shouldRetry {
		t.Error("should retry on 429 status")
	}
	if retryAfter <= 0 {
		t.Error("retryAfter should be positive")
	}
}

func TestHandleRateLimitIgnoresNon429(t *testing.T) {
	handler := NewRateLimitHandler()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
	}

	_, shouldRetry := handler.HandleRateLimit(resp)
	if shouldRetry {
		t.Error("should not retry on 200 status")
	}
}

func TestCalculateBackoffFollowsFormula(t *testing.T) {
	handler := NewRateLimitHandler()

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 16 * time.Second},
		{5, 32 * time.Second},
		{6, 60 * time.Second}, // capped at max
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			backoff := handler.CalculateBackoff(tt.attempt)
			base := backoff - (backoff % time.Millisecond)
			expectedBase := tt.expected

			if base < expectedBase || base > expectedBase+1*time.Second {
				t.Errorf("attempt %d: expected backoff around %v (with 0-1s jitter), got %v", tt.attempt, expectedBase, backoff)
			}
		})
	}
}

func TestCalculateBackoffCappedAtMax(t *testing.T) {
	handler := NewRateLimitHandler()

	for attempt := 10; attempt < 20; attempt++ {
		backoff := handler.CalculateBackoff(attempt)
		if backoff > DefaultMaxBackoff+DefaultMaxJitter {
			t.Errorf("attempt %d: backoff %v exceeds max %v + jitter %v", attempt, backoff, DefaultMaxBackoff, DefaultMaxJitter)
		}
	}
}

func TestCalculateBackoffIncludesJitter(t *testing.T) {
	handler := NewRateLimitHandler()
	seen := make(map[time.Duration]bool)

	for i := 0; i < 100; i++ {
		backoff := handler.CalculateBackoff(0)
		seen[backoff] = true
	}

	if len(seen) < 2 {
		t.Error("jitter should cause variation in backoff values")
	}
}

func TestJitterRange(t *testing.T) {
	handler := NewRateLimitHandler()

	for i := 0; i < 100; i++ {
		backoff := handler.CalculateBackoff(0)
		if backoff < DefaultBaseBackoff {
			t.Errorf("backoff %v is less than base %v", backoff, DefaultBaseBackoff)
		}
		if backoff > DefaultBaseBackoff+DefaultMaxJitter {
			t.Errorf("backoff %v exceeds base + max jitter (%v)", backoff, DefaultBaseBackoff+DefaultMaxJitter)
		}
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	handler := NewRateLimitHandler()

	tests := []struct {
		header   string
		expected time.Duration
	}{
		{"5", 5 * time.Second},
		{"30", 30 * time.Second},
		{"60", 60 * time.Second},
		{"120", 120 * time.Second},
		{"0", 0},
	}

	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			result := handler.ParseRetryAfter(tt.header)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	handler := NewRateLimitHandler()

	futureTime := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	result := handler.ParseRetryAfter(futureTime)

	if result < 29*time.Second || result > 31*time.Second {
		t.Errorf("expected ~30s, got %v", result)
	}
}

func TestParseRetryAfterInvalid(t *testing.T) {
	handler := NewRateLimitHandler()

	tests := []string{"", "invalid", "-5", "abc123"}

	for _, header := range tests {
		t.Run(header, func(t *testing.T) {
			result := handler.ParseRetryAfter(header)
			if result != 0 {
				t.Errorf("expected 0 for invalid header %q, got %v", header, result)
			}
		})
	}
}

func TestHandleRateLimitParsesRetryAfterHeader(t *testing.T) {
	handler := NewRateLimitHandler()
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"15"}},
	}

	retryAfter, shouldRetry := handler.HandleRateLimit(resp)
	if !shouldRetry {
		t.Error("should retry on 429 with Retry-After")
	}
	if retryAfter != 15*time.Second {
		t.Errorf("expected 15s from Retry-After header, got %v", retryAfter)
	}
}

func TestFormatUserMessage(t *testing.T) {
	handler := NewRateLimitHandler()

	tests := []struct {
		duration time.Duration
		contains string
	}{
		{5 * time.Second, "5s"},
		{30 * time.Second, "30s"},
		{1 * time.Minute, "60s"},
	}

	for _, tt := range tests {
		t.Run(tt.contains, func(t *testing.T) {
			msg := handler.FormatUserMessage(tt.duration)
			if !strings.Contains(msg, "Provider rate limited") {
				t.Errorf("message should contain 'Provider rate limited': %s", msg)
			}
			if !strings.Contains(msg, "Retrying in") {
				t.Errorf("message should contain 'Retrying in': %s", msg)
			}
			if !strings.Contains(msg, tt.contains) {
				t.Errorf("message should contain duration %s: %s", tt.contains, msg)
			}
		})
	}
}

func TestProviderStatusRateLimited(t *testing.T) {
	status := ProviderStatus{
		ProviderID:  "claude",
		Status:      StatusRateLimited,
		RateLimited: true,
		RetryAfter:  time.Now().Add(30 * time.Second),
	}

	if status.Status != StatusRateLimited {
		t.Errorf("expected status %s, got %s", StatusRateLimited, status.Status)
	}
	if !status.RateLimited {
		t.Error("RateLimited should be true")
	}
}

func TestProviderStatusHealthy(t *testing.T) {
	status := ProviderStatus{
		ProviderID:  "claude",
		Status:      StatusHealthy,
		RateLimited: false,
	}

	if status.Status != StatusHealthy {
		t.Errorf("expected status %s, got %s", StatusHealthy, status.Status)
	}
	if status.RateLimited {
		t.Error("RateLimited should be false")
	}
}

func TestHandlerRecordsProviderStatus(t *testing.T) {
	handler := NewRateLimitHandler()
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"30"}},
	}

	handler.HandleRateLimitForProvider("claude", resp)
	status := handler.GetProviderStatus("claude")

	if status.Status != StatusRateLimited {
		t.Errorf("expected status %s, got %s", StatusRateLimited, status.Status)
	}
	if !status.RateLimited {
		t.Error("RateLimited should be true")
	}
}

func TestHandlerClearsRateLimitStatus(t *testing.T) {
	handler := NewRateLimitHandler()
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{},
	}

	handler.HandleRateLimitForProvider("claude", resp)
	handler.ClearRateLimit("claude")

	status := handler.GetProviderStatus("claude")
	if status.RateLimited {
		t.Error("RateLimited should be false after clear")
	}
	if status.Status == StatusRateLimited {
		t.Error("status should not be rate_limited after clear")
	}
}

func TestDefaultBackoffConstants(t *testing.T) {
	if DefaultBaseBackoff != 1*time.Second {
		t.Errorf("expected DefaultBaseBackoff=1s, got %v", DefaultBaseBackoff)
	}
	if DefaultMaxBackoff != 60*time.Second {
		t.Errorf("expected DefaultMaxBackoff=60s, got %v", DefaultMaxBackoff)
	}
	if DefaultMaxJitter != 1*time.Second {
		t.Errorf("expected DefaultMaxJitter=1s, got %v", DefaultMaxJitter)
	}
}

func TestExponentialBackoffFormula(t *testing.T) {
	for attempt := 0; attempt < 6; attempt++ {
		expected := math.Min(float64(1*math.Pow(2, float64(attempt))), 60)
		expectedDuration := time.Duration(expected) * time.Second

		handler := NewRateLimitHandler()
		backoff := handler.CalculateBackoff(attempt)
		backoffBase := backoff - (backoff % time.Second)

		if backoffBase != expectedDuration {
			t.Errorf("attempt %d: expected base %v, got base %v", attempt, expectedDuration, backoffBase)
		}
	}
}

func TestGetAllProviderStatuses(t *testing.T) {
	handler := NewRateLimitHandler()
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{},
	}

	handler.HandleRateLimitForProvider("claude", resp)
	handler.HandleRateLimitForProvider("openai", resp)

	statuses := handler.GetAllProviderStatuses()
	if len(statuses) != 2 {
		t.Errorf("expected 2 statuses, got %d", len(statuses))
	}
}
