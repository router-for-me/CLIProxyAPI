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

func TestOpenAIProviderLimitsIncludesAdditionalBuckets(t *testing.T) {
	capturedAt := int64(1_800_000_000)
	payload := OpenAIProviderLimitsFromUsage("codex-local", map[string]any{
		"rate_limit": map[string]any{
			"primary_window":   map[string]any{"used_percent": 10.0, "reset_after_seconds": 120.0},
			"secondary_window": map[string]any{"used_percent": 20.0, "reset_at": 1_900_000_000.0, "reset_after_seconds": 999.0},
		},
		"rate_limit_reached_type": map[string]any{"type": "primary"},
		"additional_rate_limits": []any{
			map[string]any{
				"limit_id":   "spark",
				"limit_name": "Spark",
				"rate_limit": map[string]any{
					"primary_window":   map[string]any{"used_percent": 25.0},
					"secondary_window": map[string]any{"used_percent": 50.0},
				},
			},
			map[string]any{
				"metered_feature": "code_review",
				"name":            "Code review",
				"primary_window":  map[string]any{"used_percent": 75.0},
			},
		},
	}, capturedAt)

	if len(payload.Snapshots) != 3 {
		t.Fatalf("snapshots length = %d, want 3", len(payload.Snapshots))
	}
	if reached := payload.Snapshots[0].RateLimitReachedType; reached == nil || *reached != "primary" {
		t.Fatalf("reached type = %v, want primary", reached)
	}
	if primary := payload.Snapshots[0].Primary; primary == nil || primary.ResetsAt == nil || *primary.ResetsAt != capturedAt+120 {
		t.Fatalf("primary resets_at = %+v, want %d", primary, capturedAt+120)
	}
	if secondary := payload.Snapshots[0].Secondary; secondary == nil || secondary.ResetsAt == nil || *secondary.ResetsAt != 1_900_000_000 {
		t.Fatalf("secondary resets_at = %+v, want 1900000000", secondary)
	}
	if snapshot := payload.Snapshots[1]; snapshot.LimitID != "spark" || snapshot.LimitName == nil || *snapshot.LimitName != "Spark" || snapshot.Primary == nil || snapshot.Primary.UsedPercent != 25 || snapshot.Secondary == nil || snapshot.Secondary.UsedPercent != 50 {
		t.Fatalf("spark snapshot = %+v", snapshot)
	}
	if snapshot := payload.Snapshots[2]; snapshot.LimitID != "code_review" || snapshot.LimitName == nil || *snapshot.LimitName != "Code review" || snapshot.Primary == nil || snapshot.Primary.UsedPercent != 75 {
		t.Fatalf("code review snapshot = %+v", snapshot)
	}
}

func TestCloneSnapshotsDoesNotSharePointers(t *testing.T) {
	name := "limit"
	windowMinutes := 60
	resetsAt := int64(100)
	balance := "10"
	planType := "plus"
	status := "limited"
	original := []ProviderLimitSnapshot{{
		LimitName: &name,
		Primary: &RateLimitWindow{
			WindowMinutes: &windowMinutes,
			ResetsAt:      &resetsAt,
		},
		Secondary: &RateLimitWindow{
			WindowMinutes: &windowMinutes,
			ResetsAt:      &resetsAt,
		},
		Credits:              &CreditsSnapshot{Balance: &balance},
		PlanType:             &planType,
		RateLimitReachedType: &status,
	}}

	cloned := cloneSnapshots(original)
	*cloned[0].LimitName = "changed"
	*cloned[0].Primary.WindowMinutes = 120
	*cloned[0].Primary.ResetsAt = 200
	*cloned[0].Secondary.WindowMinutes = 180
	*cloned[0].Secondary.ResetsAt = 300
	*cloned[0].Credits.Balance = "0"
	*cloned[0].PlanType = "free"
	*cloned[0].RateLimitReachedType = "allowed"

	if *original[0].LimitName != "limit" || *original[0].Primary.WindowMinutes != 60 || *original[0].Primary.ResetsAt != 100 ||
		*original[0].Secondary.WindowMinutes != 60 || *original[0].Secondary.ResetsAt != 100 || *original[0].Credits.Balance != "10" ||
		*original[0].PlanType != "plus" || *original[0].RateLimitReachedType != "limited" {
		t.Fatalf("clone mutated original snapshot: %+v", original[0])
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
