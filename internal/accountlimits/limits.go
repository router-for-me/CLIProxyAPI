package accountlimits

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ProviderLimitsObject = "account.limits"
	ProviderAnthropic    = "anthropic"
	ProviderOpenAI       = "openai"
	ProviderZai          = "zai"
)

type RateLimitWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes *int    `json:"window_minutes"`
	ResetsAt      *int64  `json:"resets_at"`
}

type CreditsSnapshot struct {
	HasCredits bool    `json:"has_credits"`
	Unlimited  bool    `json:"unlimited"`
	Balance    *string `json:"balance"`
}

type ProviderLimitSnapshot struct {
	LimitID              string           `json:"limit_id"`
	LimitName            *string          `json:"limit_name"`
	Primary              *RateLimitWindow `json:"primary"`
	Secondary            *RateLimitWindow `json:"secondary"`
	Credits              *CreditsSnapshot `json:"credits"`
	PlanType             *string          `json:"plan_type"`
	RateLimitReachedType *string          `json:"rate_limit_reached_type"`
}

type ProviderLimitsPayload struct {
	Object     string                  `json:"object"`
	AccountID  string                  `json:"account_id"`
	Provider   string                  `json:"provider"`
	Source     string                  `json:"source"`
	CapturedAt *int64                  `json:"captured_at"`
	Snapshots  []ProviderLimitSnapshot `json:"snapshots"`
}

type limitsRecord struct {
	capturedAt int64
	snapshots  []ProviderLimitSnapshot
}

var anthropicLimitsCache = struct {
	sync.RWMutex
	byAccount map[string]limitsRecord
}{byAccount: map[string]limitsRecord{}}

func ProviderLimitsForAccount(accountID string) ProviderLimitsPayload {
	accountID = normalizeAccountID(accountID)

	anthropicLimitsCache.RLock()
	record, ok := anthropicLimitsCache.byAccount[accountID]
	anthropicLimitsCache.RUnlock()
	if !ok {
		return ProviderLimitsPayload{
			Object:     ProviderLimitsObject,
			AccountID:  accountID,
			Provider:   ProviderAnthropic,
			Source:     "unavailable",
			CapturedAt: nil,
			Snapshots:  []ProviderLimitSnapshot{},
		}
	}

	capturedAt := record.capturedAt
	return ProviderLimitsPayload{
		Object:     ProviderLimitsObject,
		AccountID:  accountID,
		Provider:   ProviderAnthropic,
		Source:     "response_headers",
		CapturedAt: &capturedAt,
		Snapshots:  cloneSnapshots(record.snapshots),
	}
}

func OpenAIProviderLimitsFromUsage(accountID string, payload map[string]any, capturedAt int64) ProviderLimitsPayload {
	snapshots := []ProviderLimitSnapshot{openAIRateLimitSnapshot("codex", nil, payload, capturedAt)}
	if additional, ok := payload["additional_rate_limits"].([]any); ok {
		for index, raw := range additional {
			entry := asMap(raw)
			if entry == nil {
				continue
			}
			limitID := firstString(entry, "limit_id", "id", "metered_feature", "limit_name", "name")
			if limitID == "" {
				limitID = fmt.Sprintf("additional_%d", index+1)
			}
			limitName := firstString(entry, "limit_name", "name")
			snapshots = append(snapshots, openAIRateLimitSnapshot(limitID, stringPointer(limitName), entry, capturedAt))
		}
	}
	return ProviderLimitsPayload{
		Object:    ProviderLimitsObject,
		AccountID: normalizeAccountID(accountID),
		Provider:  ProviderOpenAI,
		Source:    "usage_endpoint",
		Snapshots: snapshots,
	}
}

func openAIRateLimitSnapshot(limitID string, limitName *string, payload map[string]any, capturedAt int64) ProviderLimitSnapshot {
	rateLimit := asMap(payload["rate_limit"])
	if rateLimit == nil {
		rateLimit = payload
	}
	reachedType := openAIRateLimitReachedType(payload["rate_limit_reached_type"])
	if reachedType == nil {
		reachedType = openAIRateLimitReachedType(rateLimit["rate_limit_reached_type"])
	}
	return ProviderLimitSnapshot{
		LimitID:              limitID,
		LimitName:            limitName,
		Primary:              openAIRateLimitWindow(asMap(rateLimit["primary_window"]), capturedAt),
		Secondary:            openAIRateLimitWindow(asMap(rateLimit["secondary_window"]), capturedAt),
		Credits:              openAICreditsSnapshot(asMap(payload["credits"])),
		PlanType:             stringOrNil(payload["plan_type"]),
		RateLimitReachedType: reachedType,
	}
}

// Z.AI (GLM coding plan) quota unit codes seen on
// GET https://api.z.ai/api/monitor/usage/quota/limit:
//
//	unit 3 -> 5-hour token window, unit 6 -> weekly token window.
const (
	zaiUnitFiveHour = 3
	zaiUnitWeekly   = 6
)

// ZaiProviderLimitsFromQuota parses the Z.AI monitor quota payload
// (the `data` object) into 5h/weekly snapshots. Windows are classified by
// unit code first, then by appearance order among TOKENS_LIMIT entries.
func ZaiProviderLimitsFromQuota(accountID string, data map[string]any) ProviderLimitsPayload {
	snapshots := zaiQuotaSnapshots(data)
	return ProviderLimitsPayload{
		Object:    ProviderLimitsObject,
		AccountID: normalizeAccountID(accountID),
		Provider:  ProviderZai,
		Source:    "quota_endpoint",
		Snapshots: snapshots,
	}
}

func zaiQuotaSnapshots(data map[string]any) []ProviderLimitSnapshot {
	limits, ok := data["limits"].([]any)
	if !ok {
		return []ProviderLimitSnapshot{}
	}
	planType := stringOrNil(data["level"])

	snapshots := make([]ProviderLimitSnapshot, 0, 2)
	tokenLimitSeen := 0
	fiveHourDone := false
	weeklyDone := false
	for _, raw := range limits {
		entry := asMap(raw)
		if entry == nil {
			continue
		}
		limitType, _ := entry["type"].(string)
		if !strings.EqualFold(strings.TrimSpace(limitType), "TOKENS_LIMIT") {
			continue
		}
		unit, _ := numericFloat(entry["unit"])

		// Classify by unit, then fall back to appearance order.
		isFiveHour := int(unit) == zaiUnitFiveHour
		isWeekly := int(unit) == zaiUnitWeekly
		if !isFiveHour && !isWeekly {
			if tokenLimitSeen == 0 {
				isFiveHour = true
			} else if tokenLimitSeen == 1 {
				isWeekly = true
			}
		}
		tokenLimitSeen++

		if isFiveHour && !fiveHourDone {
			snapshots = append(snapshots, zaiSnapshot("five_hour", "five hour", 5*60, entry, planType))
			fiveHourDone = true
		} else if isWeekly && !weeklyDone {
			snapshots = append(snapshots, zaiSnapshot("seven_day", "seven day", 7*24*60, entry, planType))
			weeklyDone = true
		}
	}
	return snapshots
}

func zaiSnapshot(limitID, limitName string, windowMinutes int, entry map[string]any, planType *string) ProviderLimitSnapshot {
	usedPercent, _ := numericFloat(entry["percentage"])
	minutes := windowMinutes
	window := &RateLimitWindow{
		UsedPercent:   clampPercent(usedPercent),
		WindowMinutes: &minutes,
	}
	if reset, ok := numericFloat(entry["nextResetTime"]); ok {
		// nextResetTime is a Unix timestamp in milliseconds.
		resetSeconds := int64(reset / 1000)
		window.ResetsAt = &resetSeconds
	}
	name := limitName
	return ProviderLimitSnapshot{
		LimitID:   limitID,
		LimitName: &name,
		Primary:   window,
		PlanType:  planType,
	}
}

func openAIRateLimitWindow(window map[string]any, capturedAt int64) *RateLimitWindow {
	if window == nil {
		return nil
	}
	usedPercent, ok := numericFloat(window["used_percent"])
	if !ok {
		return nil
	}
	var windowMinutes *int
	if seconds, ok := numericFloat(window["limit_window_seconds"]); ok {
		minutes := int(seconds / 60)
		windowMinutes = &minutes
	}
	var resetsAt *int64
	if reset, ok := numericFloat(window["reset_at"]); ok {
		resetValue := int64(reset)
		resetsAt = &resetValue
	} else if resetAfter, ok := numericFloat(window["reset_after_seconds"]); ok && capturedAt > 0 {
		resetValue := capturedAt + int64(resetAfter)
		resetsAt = &resetValue
	}
	return &RateLimitWindow{
		UsedPercent:   usedPercent,
		WindowMinutes: windowMinutes,
		ResetsAt:      resetsAt,
	}
}

func openAICreditsSnapshot(credits map[string]any) *CreditsSnapshot {
	if credits == nil {
		return nil
	}
	hasCredits, okHasCredits := credits["has_credits"].(bool)
	unlimited, okUnlimited := credits["unlimited"].(bool)
	if !okHasCredits || !okUnlimited {
		return nil
	}
	return &CreditsSnapshot{
		HasCredits: hasCredits,
		Unlimited:  unlimited,
		Balance:    stringOrNil(credits["balance"]),
	}
}

func openAIRateLimitReachedType(value any) *string {
	reached := asMap(value)
	if reached == nil {
		return stringOrNil(value)
	}
	return stringPointer(firstString(reached, "type", "kind"))
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func CaptureAnthropicRateLimits(accountID string, headers http.Header, capturedAt time.Time) bool {
	accountID = normalizeAccountID(accountID)
	snapshots := ParseAnthropicRateLimitHeaders(headers)
	if len(snapshots) == 0 {
		return false
	}
	if capturedAt.IsZero() {
		capturedAt = time.Now()
	}

	anthropicLimitsCache.Lock()
	if previous, ok := anthropicLimitsCache.byAccount[accountID]; ok {
		snapshots = mergeSnapshots(previous.snapshots, snapshots)
	}
	anthropicLimitsCache.byAccount[accountID] = limitsRecord{
		capturedAt: capturedAt.Unix(),
		snapshots:  cloneSnapshots(snapshots),
	}
	anthropicLimitsCache.Unlock()
	return true
}

func mergeSnapshots(previous, current []ProviderLimitSnapshot) []ProviderLimitSnapshot {
	merged := cloneSnapshots(previous)
	positions := make(map[string]int, len(merged))
	for index := range merged {
		positions[merged[index].LimitID] = index
	}
	for _, snapshot := range current {
		if index, ok := positions[snapshot.LimitID]; ok {
			merged[index] = snapshot
			continue
		}
		positions[snapshot.LimitID] = len(merged)
		merged = append(merged, snapshot)
	}
	return merged
}

func ParseAnthropicRateLimitHeaders(headers http.Header) []ProviderLimitSnapshot {
	if headers == nil {
		return nil
	}

	snapshots := make([]ProviderLimitSnapshot, 0, 2)
	for _, item := range []struct {
		id   string
		name string
	}{
		{id: "five_hour", name: "five hour"},
		{id: "seven_day", name: "seven day"},
	} {
		window, ok := parseAnthropicWindow(headers, item.id)
		if !ok {
			continue
		}
		name := item.name
		snapshots = append(snapshots, ProviderLimitSnapshot{
			LimitID:              item.id,
			LimitName:            &name,
			Primary:              window,
			Secondary:            nil,
			Credits:              nil,
			PlanType:             nil,
			RateLimitReachedType: rateLimitStatus(headers, item.id),
		})
	}
	return snapshots
}

func parseAnthropicWindow(headers http.Header, limitID string) (*RateLimitWindow, bool) {
	headerPart := strings.ReplaceAll(limitID, "_", "")
	var windowMinutes int
	if limitID == "five_hour" {
		headerPart = "5h"
		windowMinutes = 5 * 60
	}
	if limitID == "seven_day" {
		headerPart = "7d"
		windowMinutes = 7 * 24 * 60
	}

	utilization := strings.TrimSpace(headers.Get("anthropic-ratelimit-unified-" + headerPart + "-utilization"))
	if utilization == "" {
		return nil, false
	}
	used, err := strconv.ParseFloat(utilization, 64)
	if err != nil {
		return nil, false
	}
	usedPercent := clampPercent(used * 100)

	var resetsAt *int64
	if resetRaw := strings.TrimSpace(headers.Get("anthropic-ratelimit-unified-" + headerPart + "-reset")); resetRaw != "" {
		if reset, err := strconv.ParseInt(resetRaw, 10, 64); err == nil {
			resetsAt = &reset
		}
	}

	return &RateLimitWindow{
		UsedPercent:   usedPercent,
		WindowMinutes: &windowMinutes,
		ResetsAt:      resetsAt,
	}, true
}

func rateLimitStatus(headers http.Header, limitID string) *string {
	headerPart := strings.ReplaceAll(limitID, "_", "")
	if limitID == "five_hour" {
		headerPart = "5h"
	}
	if limitID == "seven_day" {
		headerPart = "7d"
	}
	status := strings.TrimSpace(headers.Get("anthropic-ratelimit-unified-" + headerPart + "-status"))
	if status == "" || strings.EqualFold(status, "allowed") {
		return nil
	}
	return &status
}

func normalizeAccountID(accountID string) string {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return "default"
	}
	return accountID
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func cloneSnapshots(snapshots []ProviderLimitSnapshot) []ProviderLimitSnapshot {
	if len(snapshots) == 0 {
		return []ProviderLimitSnapshot{}
	}
	cloned := make([]ProviderLimitSnapshot, len(snapshots))
	copy(cloned, snapshots)
	for i := range cloned {
		if cloned[i].LimitName != nil {
			name := *cloned[i].LimitName
			cloned[i].LimitName = &name
		}
		if cloned[i].Primary != nil {
			primary := *cloned[i].Primary
			if primary.WindowMinutes != nil {
				windowMinutes := *primary.WindowMinutes
				primary.WindowMinutes = &windowMinutes
			}
			if primary.ResetsAt != nil {
				resetsAt := *primary.ResetsAt
				primary.ResetsAt = &resetsAt
			}
			cloned[i].Primary = &primary
		}
		if cloned[i].Secondary != nil {
			secondary := *cloned[i].Secondary
			if secondary.WindowMinutes != nil {
				windowMinutes := *secondary.WindowMinutes
				secondary.WindowMinutes = &windowMinutes
			}
			if secondary.ResetsAt != nil {
				resetsAt := *secondary.ResetsAt
				secondary.ResetsAt = &resetsAt
			}
			cloned[i].Secondary = &secondary
		}
		if cloned[i].Credits != nil {
			credits := *cloned[i].Credits
			if credits.Balance != nil {
				balance := *credits.Balance
				credits.Balance = &balance
			}
			cloned[i].Credits = &credits
		}
		if cloned[i].PlanType != nil {
			planType := *cloned[i].PlanType
			cloned[i].PlanType = &planType
		}
		if cloned[i].RateLimitReachedType != nil {
			status := *cloned[i].RateLimitReachedType
			cloned[i].RateLimitReachedType = &status
		}
	}
	return cloned
}

func asMap(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return nil
}

func numericFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func stringOrNil(value any) *string {
	if typed, ok := value.(string); ok {
		return &typed
	}
	return nil
}
