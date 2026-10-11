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

// ClaudeFiveHourQuota is the five-hour (session) window. UsedFraction is in [0, 1]. ResetsAt is
// zero when no window is open, which the usage endpoint reports as a null reset.
type ClaudeFiveHourQuota struct {
	UsedFraction float64
	ResetsAt     time.Time
}

// FetchClaudeWeeklyQuota fetches and parses one subscription's weekly quota.
// Only caller cancellation controls the established HTTP connection/body read.
//
// The callback owns authentication. This helper never accepts, stores, or logs
// credential material.
func FetchClaudeWeeklyQuota(ctx context.Context, auth *coreauth.Auth, do ClaudeQuotaRequestFunc) (ClaudeWeeklyQuota, bool, error) {
	payload, err := fetchClaudeUsage(ctx, auth, do)
	if err != nil {
		return ClaudeWeeklyQuota{}, false, err
	}
	quota, known, errParse := ParseClaudeWeeklyQuota(payload)
	if errParse != nil {
		return ClaudeWeeklyQuota{}, false, fmt.Errorf("parse Claude quota response: %w", errParse)
	}
	return quota, known, nil
}

// fetchClaudeUsage returns one subscription's usage endpoint body, so one poll can be read for
// every window it reports.
func fetchClaudeUsage(ctx context.Context, auth *coreauth.Auth, do ClaudeQuotaRequestFunc) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("Claude quota request requires a context")
	}
	if auth == nil {
		return nil, errors.New("Claude quota request requires an account")
	}
	if do == nil {
		return nil, errors.New("Claude quota request requires a callback")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ClaudeOAuthUsageEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create Claude quota request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")

	response, err := do(ctx, auth, request)
	if err != nil {
		return nil, fmt.Errorf("fetch Claude quota: %w", err)
	}
	if response == nil || response.Body == nil {
		return nil, errors.New("fetch Claude quota: empty response")
	}

	payload, errRead := io.ReadAll(io.LimitReader(response.Body, claudeQuotaMaxBodyBytes+1))
	errClose := response.Body.Close()
	if errRead != nil {
		return nil, fmt.Errorf("read Claude quota response: %w", errRead)
	}
	if errClose != nil {
		return nil, fmt.Errorf("close Claude quota response: %w", errClose)
	}
	if len(payload) > claudeQuotaMaxBodyBytes {
		return nil, errors.New("Claude quota response exceeds size limit")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, &claudeUsageStatusError{status: response.StatusCode, retryAfter: claudeRetryAfter(response.Header.Get("Retry-After"), time.Now())}
	}
	return payload, nil
}

// claudeUsageStatusError is a usage request Anthropic answered with a non-2xx status, with the wait its
// Retry-After asked for (zero without one).
type claudeUsageStatusError struct {
	status     int
	retryAfter time.Duration
}

func (e *claudeUsageStatusError) Error() string {
	if e.retryAfter > 0 {
		return fmt.Sprintf("fetch Claude quota: unexpected HTTP status %d (retry after %s)", e.status, e.retryAfter.Round(time.Second))
	}
	return fmt.Sprintf("fetch Claude quota: unexpected HTTP status %d", e.status)
}

// claudeRetryAfter reads a Retry-After value, delta-seconds or an HTTP date, as a wait from now.
func claudeRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 0
}

// ParseClaudeFiveHourQuota reads the five-hour window from the same shapes as the weekly one: a
// limits row (top-level, or wrapped in rate_limits) whose kind or name is five_hour or 5h, else the
// top-level five_hour object. Utilization is a percentage. A window with utilization and no reset
// is reported with a zero ResetsAt: no window is open. Unknown valid schemas are not errors.
func ParseClaudeFiveHourQuota(payload []byte) (ClaudeFiveHourQuota, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return ClaudeFiveHourQuota{}, false, err
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return ClaudeFiveHourQuota{}, false, err
	}
	object, ok := root.(map[string]any)
	if !ok {
		return ClaudeFiveHourQuota{}, false, nil
	}
	var rows []any
	if limits, okLimits := quotaMapValue(object, "limits"); okLimits {
		rows = append(rows, limits)
	}
	if wrapped, okWrapped := quotaMapValue(object, "rate_limits"); okWrapped {
		if rateLimits, okMap := wrapped.(map[string]any); okMap {
			if limits, okLimits := quotaMapValue(rateLimits, "limits"); okLimits {
				rows = append(rows, limits)
			}
		}
	}
	for _, limits := range rows {
		if quota, found := fiveHourLimitsRow(limits); found {
			return quota, true, nil
		}
	}
	if legacy, okLegacy := quotaMapValue(object, "five_hour"); okLegacy {
		if quota, okQuota := parseClaudeFiveHourCandidate(legacy); okQuota {
			return quota, true, nil
		}
	}
	return ClaudeFiveHourQuota{}, false, nil
}

func fiveHourLimitsRow(limits any) (ClaudeFiveHourQuota, bool) {
	matches := func(name string, object map[string]any) bool {
		if isFiveHourName(name) {
			return true
		}
		for _, field := range []string{"kind", "type", "rate_limit_type", "limit_type", "name", "id", "key"} {
			if value, present := quotaMapValue(object, field); present {
				if text, okText := value.(string); okText && isFiveHourName(text) {
					return true
				}
			}
		}
		return false
	}
	switch typed := limits.(type) {
	case []any:
		for _, value := range typed {
			if object, ok := value.(map[string]any); ok && matches("", object) {
				if quota, okQuota := parseClaudeFiveHourCandidate(object); okQuota {
					return quota, true
				}
			}
		}
	case map[string]any:
		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if object, ok := typed[name].(map[string]any); ok && matches(name, object) {
				if quota, okQuota := parseClaudeFiveHourCandidate(object); okQuota {
					return quota, true
				}
			}
		}
	}
	return ClaudeFiveHourQuota{}, false
}

func isFiveHourName(value string) bool {
	switch strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(value))) {
	case "five_hour", "5h":
		return true
	}
	return false
}

func parseClaudeFiveHourCandidate(value any) (ClaudeFiveHourQuota, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return ClaudeFiveHourQuota{}, false
	}
	utilizationValue, okUtilization := quotaMapValueAny(object, "utilization", "used_percent", "usage_percent", "utilization_percent", "percentage", "percent")
	if !okUtilization {
		return ClaudeFiveHourQuota{}, false
	}
	used, okUsed := parseQuotaNumber(utilizationValue)
	if !okUsed || used < 0 {
		return ClaudeFiveHourQuota{}, false
	}
	quota := ClaudeFiveHourQuota{UsedFraction: min(used/100, 1)}
	if resetValue, okReset := quotaMapValueAny(object, "resets_at", "reset_at"); okReset && resetValue != nil {
		reset, okTime := parseQuotaTime(resetValue)
		if !okTime {
			return ClaudeFiveHourQuota{}, false
		}
		quota.ResetsAt = reset
	}
	return quota, true
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
	// A row's explicit meter kind is authoritative. Scoped/model-specific and
	// unknown kinds must not become account-wide weekly quota merely because
	// they also belong to the weekly group or carry a generic display name.
	explicitRank := 0
	hasExplicitKind := false
	for _, field := range []string{"kind", "type", "rate_limit_type", "limit_type"} {
		value, present := quotaMapValue(object, field)
		if !present {
			continue
		}
		hasExplicitKind = true
		text, ok := value.(string)
		if !ok || weeklyQuotaNameRank(text) == 0 {
			return 0
		}
		explicitRank = max(explicitRank, weeklyQuotaNameRank(text))
	}
	if hasExplicitKind {
		return explicitRank
	}

	// Legacy specific identifiers carry the same scope distinction. A weekly
	// group/window is only a fallback when no meter identifier was provided.
	rank := 0
	hasIdentifier := false
	if strings.TrimSpace(name) != "" {
		hasIdentifier = true
		rank = weeklyQuotaNameRank(name)
		if rank == 0 {
			return 0
		}
	}
	for _, field := range []string{"name", "id", "key"} {
		value, present := quotaMapValue(object, field)
		if !present {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return 0
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		hasIdentifier = true
		identifierRank := weeklyQuotaNameRank(text)
		if identifierRank == 0 {
			return 0
		}
		rank = max(rank, identifierRank)
	}
	if hasIdentifier {
		return rank
	}

	for _, field := range []string{"group", "window"} {
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
