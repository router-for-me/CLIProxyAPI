package executor

import (
	"net/http"
	"testing"
	"time"
)

func TestParseClaudeCodeQuotaHeaders_NilHeaders(t *testing.T) {
	result := parseClaudeCodeQuotaHeaders(nil)
	if result != nil {
		t.Fatal("expected nil for nil headers")
	}
}

func TestParseClaudeCodeQuotaHeaders_NoQuotaHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	headers.Set("X-Custom-Header", "value")

	result := parseClaudeCodeQuotaHeaders(headers)
	if result != nil {
		t.Fatal("expected nil when no quota headers present")
	}
}

func TestParseClaudeCodeQuotaHeaders_WithQuotaHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-status", "ok")
	headers.Set("anthropic-ratelimit-unified-5h-status", "available")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "1234567890")
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.25")
	headers.Set("anthropic-ratelimit-unified-7d-status", "available")
	headers.Set("anthropic-ratelimit-unified-7d-reset", "1234567900")
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.50")
	headers.Set("anthropic-ratelimit-unified-overage-status", "available")
	headers.Set("anthropic-ratelimit-unified-overage-reset", "1234567910")
	headers.Set("anthropic-ratelimit-unified-overage-utilization", "0.10")
	headers.Set("anthropic-ratelimit-unified-representative-claim", "5h")
	headers.Set("anthropic-ratelimit-unified-fallback-percentage", "0.75")
	headers.Set("anthropic-ratelimit-unified-fallback", "available")
	headers.Set("anthropic-ratelimit-unified-reset", "1234567920")

	result := parseClaudeCodeQuotaHeaders(headers)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.UnifiedStatus != "ok" {
		t.Fatalf("expected UnifiedStatus 'ok', got %s", result.UnifiedStatus)
	}
	if result.FiveHourStatus != "available" {
		t.Fatalf("expected FiveHourStatus 'available', got %s", result.FiveHourStatus)
	}
	if result.FiveHourReset != 1234567890 {
		t.Fatalf("expected FiveHourReset 1234567890, got %d", result.FiveHourReset)
	}
	if result.FiveHourUtilization != 0.25 {
		t.Fatalf("expected FiveHourUtilization 0.25, got %f", result.FiveHourUtilization)
	}
	if result.SevenDayStatus != "available" {
		t.Fatalf("expected SevenDayStatus 'available', got %s", result.SevenDayStatus)
	}
	if result.SevenDayReset != 1234567900 {
		t.Fatalf("expected SevenDayReset 1234567900, got %d", result.SevenDayReset)
	}
	if result.SevenDayUtilization != 0.50 {
		t.Fatalf("expected SevenDayUtilization 0.50, got %f", result.SevenDayUtilization)
	}
	if result.OverageStatus != "available" {
		t.Fatalf("expected OverageStatus 'available', got %s", result.OverageStatus)
	}
	if result.OverageReset != 1234567910 {
		t.Fatalf("expected OverageReset 1234567910, got %d", result.OverageReset)
	}
	if result.OverageUtilization != 0.10 {
		t.Fatalf("expected OverageUtilization 0.10, got %f", result.OverageUtilization)
	}
	if result.RepresentativeClaim != "5h" {
		t.Fatalf("expected RepresentativeClaim '5h', got %s", result.RepresentativeClaim)
	}
	if result.FallbackPercentage != 0.75 {
		t.Fatalf("expected FallbackPercentage 0.75, got %f", result.FallbackPercentage)
	}
	if result.FallbackAvailable != "available" {
		t.Fatalf("expected FallbackAvailable 'available', got %s", result.FallbackAvailable)
	}
	if result.UnifiedReset != 1234567920 {
		t.Fatalf("expected UnifiedReset 1234567920, got %d", result.UnifiedReset)
	}
	if result.LastUpdated.IsZero() {
		t.Fatal("expected LastUpdated to be set")
	}
}

func TestParseClaudeCodeQuotaHeaders_PartialHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-status", "ok")
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.30")

	result := parseClaudeCodeQuotaHeaders(headers)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.UnifiedStatus != "ok" {
		t.Fatalf("expected UnifiedStatus 'ok', got %s", result.UnifiedStatus)
	}
	if result.FiveHourUtilization != 0.30 {
		t.Fatalf("expected FiveHourUtilization 0.30, got %f", result.FiveHourUtilization)
	}
	// Other fields should be zero values
	if result.FiveHourReset != 0 {
		t.Fatalf("expected FiveHourReset 0, got %d", result.FiveHourReset)
	}
	if result.SevenDayUtilization != 0 {
		t.Fatalf("expected SevenDayUtilization 0, got %f", result.SevenDayUtilization)
	}
}

func TestParseUnixTimestamp_EmptyString(t *testing.T) {
	result := parseUnixTimestamp("")
	if result != 0 {
		t.Fatalf("expected 0 for empty string, got %d", result)
	}
}

func TestParseUnixTimestamp_ValidTimestamp(t *testing.T) {
	result := parseUnixTimestamp("1234567890")
	if result != 1234567890 {
		t.Fatalf("expected 1234567890, got %d", result)
	}
}

func TestParseUnixTimestamp_InvalidTimestamp(t *testing.T) {
	result := parseUnixTimestamp("invalid")
	if result != 0 {
		t.Fatalf("expected 0 for invalid timestamp, got %d", result)
	}
}

func TestParseUnixTimestamp_NegativeTimestamp(t *testing.T) {
	result := parseUnixTimestamp("-123")
	if result != -123 {
		t.Fatalf("expected -123, got %d", result)
	}
}

func TestParseFloat_EmptyString(t *testing.T) {
	result := parseFloat("")
	if result != 0 {
		t.Fatalf("expected 0 for empty string, got %f", result)
	}
}

func TestParseFloat_ValidFloat(t *testing.T) {
	result := parseFloat("0.75")
	if result != 0.75 {
		t.Fatalf("expected 0.75, got %f", result)
	}
}

func TestParseFloat_InvalidFloat(t *testing.T) {
	result := parseFloat("invalid")
	if result != 0 {
		t.Fatalf("expected 0 for invalid float, got %f", result)
	}
}

func TestParseFloat_Integer(t *testing.T) {
	result := parseFloat("42")
	if result != 42.0 {
		t.Fatalf("expected 42.0, got %f", result)
	}
}

func TestParseFloat_ScientificNotation(t *testing.T) {
	result := parseFloat("1.5e-2")
	if result != 0.015 {
		t.Fatalf("expected 0.015, got %f", result)
	}
}

func TestClaudeCodeQuotaInfo_Structure(t *testing.T) {
	// Test that the structure can be created and fields are accessible
	now := time.Now()
	quota := &ClaudeCodeQuotaInfo{
		UnifiedStatus:       "ok",
		FiveHourStatus:      "available",
		FiveHourReset:       1234567890,
		FiveHourUtilization: 0.25,
		SevenDayStatus:      "available",
		SevenDayReset:       1234567900,
		SevenDayUtilization: 0.50,
		OverageStatus:       "available",
		OverageReset:        1234567910,
		OverageUtilization:  0.10,
		RepresentativeClaim: "5h",
		FallbackPercentage:  0.75,
		FallbackAvailable:   "available",
		UnifiedReset:        1234567920,
		LastUpdated:         now,
	}

	if quota.UnifiedStatus != "ok" {
		t.Fatalf("expected UnifiedStatus 'ok', got %s", quota.UnifiedStatus)
	}
	if quota.FiveHourUtilization != 0.25 {
		t.Fatalf("expected FiveHourUtilization 0.25, got %f", quota.FiveHourUtilization)
	}
	if quota.LastUpdated != now {
		t.Fatalf("expected LastUpdated to match, got %v", quota.LastUpdated)
	}
}

func TestParseClaudeCodeQuotaHeaders_EmptyValues(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-status", "ok")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "")
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "")

	result := parseClaudeCodeQuotaHeaders(headers)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.UnifiedStatus != "ok" {
		t.Fatalf("expected UnifiedStatus 'ok', got %s", result.UnifiedStatus)
	}
	// Empty values should parse to zero
	if result.FiveHourReset != 0 {
		t.Fatalf("expected FiveHourReset 0, got %d", result.FiveHourReset)
	}
	if result.FiveHourUtilization != 0 {
		t.Fatalf("expected FiveHourUtilization 0, got %f", result.FiveHourUtilization)
	}
}

func TestParseClaudeCodeQuotaHeaders_OnlyUnifiedStatus(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-status", "rate_limited")

	result := parseClaudeCodeQuotaHeaders(headers)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.UnifiedStatus != "rate_limited" {
		t.Fatalf("expected UnifiedStatus 'rate_limited', got %s", result.UnifiedStatus)
	}
	// All other fields should be zero/empty
	if result.FiveHourStatus != "" {
		t.Fatalf("expected empty FiveHourStatus, got %s", result.FiveHourStatus)
	}
	if result.FiveHourUtilization != 0 {
		t.Fatalf("expected FiveHourUtilization 0, got %f", result.FiveHourUtilization)
	}
}
