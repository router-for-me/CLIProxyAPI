package accountlimits

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseAnthropicRateLimitHeaders(t *testing.T) {
	headers := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Status":      []string{"Allowed"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       []string{"1777477800"},
		"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.8"},
		"Anthropic-Ratelimit-Unified-7d-Status":      []string{"warning"},
		"Anthropic-Ratelimit-Unified-7d-Reset":       []string{"1777755600"},
		"Anthropic-Ratelimit-Unified-7d-Utilization": []string{"0.56"},
	}

	snapshots := ParseAnthropicRateLimitHeaders(headers)

	if len(snapshots) != 2 {
		t.Fatalf("snapshots length = %d, want 2", len(snapshots))
	}
	if snapshots[0].LimitID != "five_hour" {
		t.Fatalf("first limit_id = %q, want five_hour", snapshots[0].LimitID)
	}
	if snapshots[0].Primary == nil || snapshots[0].Primary.UsedPercent != 80 {
		t.Fatalf("five_hour used_percent = %+v, want 80", snapshots[0].Primary)
	}
	if snapshots[0].Primary.ResetsAt == nil || *snapshots[0].Primary.ResetsAt != 1777477800 {
		t.Fatalf("five_hour resets_at = %+v, want 1777477800", snapshots[0].Primary.ResetsAt)
	}
	if snapshots[0].Primary.WindowMinutes == nil || *snapshots[0].Primary.WindowMinutes != 300 {
		t.Fatalf("five_hour window_minutes = %+v, want 300", snapshots[0].Primary.WindowMinutes)
	}
	if snapshots[0].RateLimitReachedType != nil {
		t.Fatalf("five_hour status = %q, want nil", *snapshots[0].RateLimitReachedType)
	}
	if snapshots[1].LimitID != "seven_day" {
		t.Fatalf("second limit_id = %q, want seven_day", snapshots[1].LimitID)
	}
	if snapshots[1].Primary == nil || snapshots[1].Primary.UsedPercent < 55.999 || snapshots[1].Primary.UsedPercent > 56.001 {
		t.Fatalf("seven_day used_percent = %+v, want 56", snapshots[1].Primary)
	}
	if snapshots[1].Primary.WindowMinutes == nil || *snapshots[1].Primary.WindowMinutes != 10080 {
		t.Fatalf("seven_day window_minutes = %+v, want 10080", snapshots[1].Primary.WindowMinutes)
	}
	if snapshots[1].RateLimitReachedType == nil || *snapshots[1].RateLimitReachedType != "warning" {
		t.Fatalf("seven_day status = %+v, want warning", snapshots[1].RateLimitReachedType)
	}
}

func TestParseAnthropicRateLimitHeadersReturnsEmptyWithoutUtilization(t *testing.T) {
	snapshots := ParseAnthropicRateLimitHeaders(http.Header{"Content-Type": []string{"application/json"}})

	if len(snapshots) != 0 {
		t.Fatalf("snapshots length = %d, want 0", len(snapshots))
	}
}

func TestParseAnthropicRateLimitHeadersClampsUtilization(t *testing.T) {
	snapshots := ParseAnthropicRateLimitHeaders(http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"1.5"},
		"Anthropic-Ratelimit-Unified-7d-Utilization": []string{"-0.1"},
	})

	if len(snapshots) != 2 {
		t.Fatalf("snapshots length = %d, want 2", len(snapshots))
	}
	if snapshots[0].Primary == nil || snapshots[0].Primary.UsedPercent != 100 {
		t.Fatalf("five_hour used_percent = %+v, want 100", snapshots[0].Primary)
	}
	if snapshots[1].Primary == nil || snapshots[1].Primary.UsedPercent != 0 {
		t.Fatalf("seven_day used_percent = %+v, want 0", snapshots[1].Primary)
	}
}

func TestProviderLimitsForAccountUsesCapturedHeaders(t *testing.T) {
	capturedAt := time.Unix(1779695597, 0)
	ok := CaptureAnthropicRateLimits("anthropic_acc_b", http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.14"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       []string{"1779706800"},
	}, capturedAt)
	if !ok {
		t.Fatal("CaptureAnthropicRateLimits returned false")
	}

	payload := ProviderLimitsForAccount("anthropic_acc_b")

	if payload.Object != ProviderLimitsObject {
		t.Fatalf("object = %q, want %q", payload.Object, ProviderLimitsObject)
	}
	if payload.Source != "response_headers" {
		t.Fatalf("source = %q, want response_headers", payload.Source)
	}
	if payload.CapturedAt == nil || *payload.CapturedAt != 1779695597 {
		t.Fatalf("captured_at = %+v, want 1779695597", payload.CapturedAt)
	}
	if len(payload.Snapshots) != 1 {
		t.Fatalf("snapshots length = %d, want 1", len(payload.Snapshots))
	}
	if payload.Snapshots[0].Primary == nil || payload.Snapshots[0].Primary.UsedPercent < 13.999 || payload.Snapshots[0].Primary.UsedPercent > 14.001 {
		t.Fatalf("used_percent = %+v, want 14", payload.Snapshots[0].Primary)
	}
}

func TestCaptureAnthropicRateLimitsMergesPartialWindows(t *testing.T) {
	accountID := "anthropic_partial_windows"
	CaptureAnthropicRateLimits(accountID, http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.2"},
	}, time.Unix(1, 0))
	CaptureAnthropicRateLimits(accountID, http.Header{
		"Anthropic-Ratelimit-Unified-7d-Utilization": []string{"0.4"},
	}, time.Unix(2, 0))

	payload := ProviderLimitsForAccount(accountID)
	if len(payload.Snapshots) != 2 {
		t.Fatalf("snapshots length = %d, want 2", len(payload.Snapshots))
	}
	if payload.Snapshots[0].LimitID != "five_hour" || payload.Snapshots[1].LimitID != "seven_day" {
		t.Fatalf("unexpected snapshots: %+v", payload.Snapshots)
	}
}

func TestZaiProviderLimitsFromQuota(t *testing.T) {
	// Real response shape from GET https://api.z.ai/api/monitor/usage/quota/limit.
	// First TOKENS_LIMIT (unit 3) = 5h, second (unit 6) = weekly; the TIME_LIMIT
	// entry (MCP quota) must be ignored.
	raw := `{
		"code": 200,
		"data": {
			"limits": [
				{"type": "TOKENS_LIMIT", "unit": 3, "percentage": 12},
				{"type": "TOKENS_LIMIT", "unit": 6, "percentage": 34, "nextResetTime": 1783682427974},
				{"type": "TIME_LIMIT", "unit": 5, "percentage": 0}
			],
			"level": "max"
		}
	}`

	var parsed struct {
		Data map[string]any `json:"data"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		t.Fatalf("failed to decode zai payload: %v", err)
	}

	payload := ZaiProviderLimitsFromQuota("zai_acc_a", parsed.Data)

	if payload.Provider != ProviderZai || payload.AccountID != "zai_acc_a" || payload.Source != "quota_endpoint" {
		t.Fatalf("unexpected metadata: %+v", payload)
	}
	if len(payload.Snapshots) != 2 {
		t.Fatalf("snapshots length = %d, want 2", len(payload.Snapshots))
	}

	fiveHour := payload.Snapshots[0]
	if fiveHour.LimitID != "five_hour" || fiveHour.Primary == nil {
		t.Fatalf("five_hour snapshot = %+v", fiveHour)
	}
	if fiveHour.Primary.UsedPercent != 12 || fiveHour.Primary.WindowMinutes == nil || *fiveHour.Primary.WindowMinutes != 300 {
		t.Fatalf("five_hour window = %+v", fiveHour.Primary)
	}
	planType := fiveHour.PlanType
	if planType == nil || *planType != "max" {
		t.Fatalf("five_hour plan_type = %v, want max", fiveHour.PlanType)
	}

	weekly := payload.Snapshots[1]
	if weekly.LimitID != "seven_day" || weekly.Primary == nil {
		t.Fatalf("seven_day snapshot = %+v", weekly)
	}
	if weekly.Primary.UsedPercent != 34 || weekly.Primary.WindowMinutes == nil || *weekly.Primary.WindowMinutes != 10080 {
		t.Fatalf("seven_day window = %+v", weekly.Primary)
	}
	// nextResetTime 1783682427974 ms -> 1783682427 s
	if weekly.Primary.ResetsAt == nil || *weekly.Primary.ResetsAt != 1783682427 {
		t.Fatalf("seven_day resets_at = %+v, want 1783682427", weekly.Primary.ResetsAt)
	}
}
