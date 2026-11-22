package clawmaster

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ClaudeFableQuota is a subscription's weekly Fable allowance: on Max and Ultra, Fable may use up to half
// of the weekly all-models limit, and Anthropic meters that share on its own. UsedFraction is in [0, 1] of
// that share. ResetsAt is zero when the usage endpoint gives no reset (no Fable use this week).
type ClaudeFableQuota struct {
	UsedFraction float64
	ResetsAt     time.Time
}

// The usage endpoint's Fable row is a weekly_scoped meter whose scope names the model; Claude Code tells
// Fable rows apart the same way (a display name starting with the word Fable).
var claudeFableScopeName = regexp.MustCompile(`(?i)^fable\b`)

// ParseClaudeFableQuota reads the Fable row of the usage endpoint's limits (top-level or wrapped in
// rate_limits, a list or a keyed object): kind weekly_scoped, scope.model.display_name Fable, percent
// 0-100, resets_at. A payload without that row is unknown, not an error: Pro and Team seats have none.
func ParseClaudeFableQuota(payload []byte) (ClaudeFableQuota, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return ClaudeFableQuota{}, false, err
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return ClaudeFableQuota{}, false, err
	}
	object, ok := root.(map[string]any)
	if !ok {
		return ClaudeFableQuota{}, false, nil
	}
	var lists []any
	if limits, okLimits := quotaMapValue(object, "limits"); okLimits {
		lists = append(lists, limits)
	}
	if wrapped, okWrapped := quotaMapValue(object, "rate_limits"); okWrapped {
		if rateLimits, okMap := wrapped.(map[string]any); okMap {
			if limits, okLimits := quotaMapValue(rateLimits, "limits"); okLimits {
				lists = append(lists, limits)
			}
		}
	}
	for _, limits := range lists {
		for _, row := range claudeLimitRows(limits) {
			if !isClaudeFableRow(row) {
				continue
			}
			if quota, okQuota := parseClaudeFableCandidate(row); okQuota {
				return quota, true, nil
			}
		}
	}
	return ClaudeFableQuota{}, false, nil
}

// claudeLimitRows lists the row objects of a limits value, a list in order or a keyed object by key.
func claudeLimitRows(limits any) []map[string]any {
	var rows []map[string]any
	switch typed := limits.(type) {
	case []any:
		for _, value := range typed {
			if row, ok := value.(map[string]any); ok {
				rows = append(rows, row)
			}
		}
	case map[string]any:
		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if row, ok := typed[name].(map[string]any); ok {
				rows = append(rows, row)
			}
		}
	}
	return rows
}

func isClaudeFableRow(row map[string]any) bool {
	kind, ok := quotaMapValueAny(row, "kind", "type", "rate_limit_type", "limit_type")
	if text, okText := kind.(string); !ok || !okText || normalizedQuotaKey(text) != normalizedQuotaKey("weekly_scoped") {
		return false
	}
	scope, ok := quotaMapValue(row, "scope")
	scopeObject, okScope := scope.(map[string]any)
	if !ok || !okScope {
		return false
	}
	model, ok := quotaMapValue(scopeObject, "model")
	modelObject, okModel := model.(map[string]any)
	if !ok || !okModel {
		return false
	}
	name, ok := quotaMapValue(modelObject, "display_name")
	text, okText := name.(string)
	return ok && okText && claudeFableScopeName.MatchString(strings.TrimSpace(text))
}

func parseClaudeFableCandidate(row map[string]any) (ClaudeFableQuota, bool) {
	utilizationValue, ok := quotaMapValueAny(row, "percent", "utilization", "used_percent", "usage_percent", "utilization_percent", "percentage")
	if !ok {
		return ClaudeFableQuota{}, false
	}
	used, ok := parseQuotaNumber(utilizationValue)
	if !ok || used < 0 {
		return ClaudeFableQuota{}, false
	}
	quota := ClaudeFableQuota{UsedFraction: min(used/100, 1)}
	if resetValue, okReset := quotaMapValueAny(row, "resets_at", "reset_at"); okReset && resetValue != nil {
		reset, okTime := parseQuotaTime(resetValue)
		if !okTime {
			return ClaudeFableQuota{}, false
		}
		quota.ResetsAt = reset
	}
	return quota, true
}

// ParseClaudeFableQuotaHeaders reads the Fable allowance Anthropic reports on a response (the 7d_oi window:
// utilization as a fraction, reset as epoch seconds). Both are needed.
func ParseClaudeFableQuotaHeaders(headers http.Header) (ClaudeFableQuota, bool) {
	if len(headers) == 0 {
		return ClaudeFableQuota{}, false
	}
	used, okUsed := parseQuotaNumber(headers.Get("Anthropic-Ratelimit-Unified-7d_oi-Utilization"))
	reset, okReset := parseQuotaTime(headers.Get("Anthropic-Ratelimit-Unified-7d_oi-Reset"))
	if !okUsed || !okReset || used < 0 {
		return ClaudeFableQuota{}, false
	}
	return ClaudeFableQuota{UsedFraction: min(used, 1), ResetsAt: reset}, true
}
