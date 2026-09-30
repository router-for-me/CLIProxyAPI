package claudemaster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	// ClaudeOAuthUsageEndpoint reports the limits for the authenticated Claude
	// subscription. Authentication is deliberately left to the request callback.
	ClaudeOAuthUsageEndpoint = "https://api.anthropic.com/api/oauth/usage"

	claudeQuotaDefaultTimeout = 10 * time.Second
	claudeQuotaMaxBodyBytes   = 64 << 10
)

// ClaudeWeeklyQuota is the bounded weekly quota state used for account
// selection. UsedFraction is always in the range [0, 1].
type ClaudeWeeklyQuota struct {
	UsedFraction float64
	ResetsAt     time.Time
}

// ClaudeQuotaRequestFunc executes a usage request for one registered account.
// It matches Manager.HttpRequest, which injects the selected credential before
// sending the request.
type ClaudeQuotaRequestFunc func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error)

// FetchClaudeWeeklyQuota fetches and parses one subscription's weekly quota. A
// non-positive timeout selects the ten-second credential-acquisition default.
//
// The callback owns authentication. This helper never accepts, stores, or logs
// credential material.
func FetchClaudeWeeklyQuota(ctx context.Context, auth *coreauth.Auth, timeout time.Duration, do ClaudeQuotaRequestFunc) (ClaudeWeeklyQuota, bool, error) {
	if ctx == nil {
		return ClaudeWeeklyQuota{}, false, errors.New("Claude quota request requires a context")
	}
	if auth == nil {
		return ClaudeWeeklyQuota{}, false, errors.New("Claude quota request requires an account")
	}
	if do == nil {
		return ClaudeWeeklyQuota{}, false, errors.New("Claude quota request requires a callback")
	}
	if timeout <= 0 {
		timeout = claudeQuotaDefaultTimeout
	}

	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, ClaudeOAuthUsageEndpoint, nil)
	if err != nil {
		return ClaudeWeeklyQuota{}, false, fmt.Errorf("create Claude quota request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")

	response, err := do(requestCtx, auth, request)
	if err != nil {
		return ClaudeWeeklyQuota{}, false, fmt.Errorf("fetch Claude quota: %w", err)
	}
	if response == nil || response.Body == nil {
		return ClaudeWeeklyQuota{}, false, errors.New("fetch Claude quota: empty response")
	}

	payload, errRead := io.ReadAll(io.LimitReader(response.Body, claudeQuotaMaxBodyBytes+1))
	errClose := response.Body.Close()
	if errRead != nil {
		return ClaudeWeeklyQuota{}, false, fmt.Errorf("read Claude quota response: %w", errRead)
	}
	if errClose != nil {
		return ClaudeWeeklyQuota{}, false, fmt.Errorf("close Claude quota response: %w", errClose)
	}
	if len(payload) > claudeQuotaMaxBodyBytes {
		return ClaudeWeeklyQuota{}, false, errors.New("Claude quota response exceeds size limit")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ClaudeWeeklyQuota{}, false, fmt.Errorf("fetch Claude quota: unexpected HTTP status %d", response.StatusCode)
	}

	quota, known, errParse := ParseClaudeWeeklyQuota(payload)
	if errParse != nil {
		return ClaudeWeeklyQuota{}, false, fmt.Errorf("parse Claude quota response: %w", errParse)
	}
	return quota, known, nil
}

// ParseClaudeWeeklyQuota parses the current top-level limits rows, the legacy
// top-level seven_day shape, and the wrapped rate_limits.limits variant used by
// compatible gateways. Unknown valid JSON schemas are reported as unknown
// rather than as errors.
func ParseClaudeWeeklyQuota(payload []byte) (ClaudeWeeklyQuota, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return ClaudeWeeklyQuota{}, false, err
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return ClaudeWeeklyQuota{}, false, err
	}
	object, ok := root.(map[string]any)
	if !ok {
		return ClaudeWeeklyQuota{}, false, nil
	}

	// Current Claude Code treats limits[] as the server-authoritative list of
	// applicable meters. Prefer its weekly_all row when both representations
	// are present, then retain seven_day as a compatibility fallback.
	if limits, okLimits := quotaMapValue(object, "limits"); okLimits {
		if quota, known, errLimits := parseClaudeWeeklyLimits(limits); errLimits != nil || known {
			return quota, known, errLimits
		}
	}

	if legacy, okLegacy := quotaMapValue(object, "seven_day"); okLegacy {
		if quota, okQuota := parseClaudeQuotaCandidate(legacy, true); okQuota {
			return quota, true, nil
		}
	}

	rateLimitsValue, okRateLimits := quotaMapValue(object, "rate_limits")
	rateLimits, okRateLimits := rateLimitsValue.(map[string]any)
	if !okRateLimits {
		return ClaudeWeeklyQuota{}, false, nil
	}
	limits, okLimits := quotaMapValue(rateLimits, "limits")
	if !okLimits {
		return ClaudeWeeklyQuota{}, false, nil
	}
	return parseClaudeWeeklyLimits(limits)
}

// ParseClaudeWeeklyQuotaHeaders parses Claude's passive unified 7-day quota
// headers. Utilization is already a fraction in this representation.
func ParseClaudeWeeklyQuotaHeaders(headers http.Header) (ClaudeWeeklyQuota, bool) {
	if len(headers) == 0 {
		return ClaudeWeeklyQuota{}, false
	}
	used, okUsed := parseQuotaNumber(headers.Get("Anthropic-Ratelimit-Unified-7d-Utilization"))
	reset, okReset := parseQuotaTime(headers.Get("Anthropic-Ratelimit-Unified-7d-Reset"))
	if !okUsed || !okReset || used < 0 {
		return ClaudeWeeklyQuota{}, false
	}
	return ClaudeWeeklyQuota{UsedFraction: min(used, 1), ResetsAt: reset}, true
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

func parseClaudeWeeklyLimits(limits any) (ClaudeWeeklyQuota, bool, error) {
	type rankedQuota struct {
		quota ClaudeWeeklyQuota
		rank  int
		order int
	}
	candidates := make([]rankedQuota, 0)
	addCandidate := func(name string, value any, order int) {
		object, ok := value.(map[string]any)
		if !ok {
			return
		}
		rank := weeklyQuotaRank(name, object)
		if rank == 0 {
			return
		}
		quota, ok := parseClaudeQuotaCandidate(object, true)
		if ok {
			candidates = append(candidates, rankedQuota{quota: quota, rank: rank, order: order})
		}
	}

	switch typed := limits.(type) {
	case []any:
		for index, value := range typed {
			addCandidate("", value, index)
		}
	case map[string]any:
		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		for index, name := range names {
			addCandidate(name, typed[name], index)
		}
	default:
		return ClaudeWeeklyQuota{}, false, nil
	}

	if len(candidates) == 0 {
		return ClaudeWeeklyQuota{}, false, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank > candidates[j].rank
		}
		return candidates[i].order < candidates[j].order
	})
	return candidates[0].quota, true, nil
}

func weeklyQuotaRank(name string, object map[string]any) int {
	rank := weeklyQuotaNameRank(name)
	for _, field := range []string{"kind", "name", "type", "group", "id", "key", "rate_limit_type", "limit_type", "window"} {
		value, ok := quotaMapValue(object, field)
		if !ok {
			continue
		}
		text, ok := value.(string)
		if ok {
			rank = max(rank, weeklyQuotaNameRank(text))
		}
	}
	return rank
}

func weeklyQuotaNameRank(value string) int {
	normalized := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(value)))
	switch normalized {
	case "weekly_all", "all_weekly", "weekly_all_models":
		return 3
	case "weekly", "week":
		return 2
	case "seven_day", "7d":
		return 1
	default:
		return 0
	}
}

func parseClaudeQuotaCandidate(value any, utilizationIsPercent bool) (ClaudeWeeklyQuota, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return ClaudeWeeklyQuota{}, false
	}
	utilizationValue, okUtilization := quotaMapValueAny(object, "utilization", "used_percent", "usage_percent", "utilization_percent", "percentage", "percent")
	resetValue, okResetValue := quotaMapValueAny(object, "resets_at", "reset_at")
	if !okUtilization || !okResetValue {
		return ClaudeWeeklyQuota{}, false
	}
	used, okUsed := parseQuotaNumber(utilizationValue)
	reset, okReset := parseQuotaTime(resetValue)
	if !okUsed || !okReset || used < 0 {
		return ClaudeWeeklyQuota{}, false
	}
	if utilizationIsPercent {
		used /= 100
	}
	return ClaudeWeeklyQuota{UsedFraction: min(used, 1), ResetsAt: reset}, true
}

func quotaMapValueAny(object map[string]any, names ...string) (any, bool) {
	for _, name := range names {
		if value, ok := quotaMapValue(object, name); ok {
			return value, true
		}
	}
	return nil, false
}

func quotaMapValue(object map[string]any, name string) (any, bool) {
	if value, ok := object[name]; ok {
		return value, true
	}
	for key, value := range object {
		if normalizedQuotaKey(key) == normalizedQuotaKey(name) {
			return value, true
		}
	}
	return nil, false
}

func normalizedQuotaKey(value string) string {
	return strings.Map(func(char rune) rune {
		if char >= 'A' && char <= 'Z' {
			return char + ('a' - 'A')
		}
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			return char
		}
		return -1
	}, strings.TrimSpace(value))
}

func parseQuotaNumber(value any) (float64, bool) {
	var number float64
	var err error
	switch typed := value.(type) {
	case json.Number:
		number, err = typed.Float64()
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case string:
		number, err = strconv.ParseFloat(strings.TrimSpace(typed), 64)
	default:
		return 0, false
	}
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}

func parseQuotaTime(value any) (time.Time, bool) {
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return time.Time{}, false
		}
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed, true
		}
		value = text
	}

	epoch, ok := parseQuotaNumber(value)
	if !ok || epoch <= 0 {
		return time.Time{}, false
	}
	absEpoch := math.Abs(epoch)
	switch {
	case absEpoch >= 1e18:
		epoch /= 1e9
	case absEpoch >= 1e15:
		epoch /= 1e6
	case absEpoch >= 1e12:
		epoch /= 1e3
	}
	seconds, fraction := math.Modf(epoch)
	if seconds > math.MaxInt64 || seconds < math.MinInt64 {
		return time.Time{}, false
	}
	return time.Unix(int64(seconds), int64(fraction*float64(time.Second))).UTC(), true
}
