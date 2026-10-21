package usageapi

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const maxLimitsPerReport = 64

// reportFromQuota converts a quota provider response into one normalized
// report. Provider-defined descriptions and summary metrics are intentionally
// not exposed: they may contain account or billing details that a proxy API
// key is not allowed to see.
func reportFromQuota(provider string, quota pluginapi.QuotaFetchResponse, fetchedAt int64) *Report {
	if fetchedAt <= 0 {
		return nil
	}
	limits := make([]Limit, 0)
	seenIDs := make(map[string]int)
	for groupIndex, group := range quota.Groups {
		if len(limits) >= maxLimitsPerReport {
			break
		}
		groupName := sanitizeText(group.DisplayName, 80)
		for bucketIndex, bucket := range group.Buckets {
			if len(limits) >= maxLimitsPerReport {
				break
			}
			remaining := bucket.RemainingFraction
			if math.IsNaN(remaining) || math.IsInf(remaining, 0) {
				continue
			}
			remaining = clampFraction(remaining)
			usedFraction := 1 - remaining

			windowID := normalizeWindowID(bucket.Window)
			label := groupName
			if windowID != "" {
				windowLabel := humanWindowLabel(windowID)
				switch {
				case label == "":
					label = windowLabel
				case windowLabel != "" && windowLabel != "Limit":
					label = label + " (" + windowLabel + ")"
				}
			}
			if label == "" {
				label = fmt.Sprintf("Limit %d", bucketIndex+1)
			}

			baseID := provider
			if slug := compactSlug(groupName); slug != "" {
				baseID += ":" + slug
			} else if groupIndex > 0 {
				baseID += ":" + strconv.Itoa(groupIndex+1)
			}
			windowKey := windowID
			if windowKey == "" {
				windowKey = "limit"
			}
			baseID += ":" + compactSlug(windowKey)
			id := uniqueLimitID(baseID, seenIDs)

			limit := Limit{
				ID:    id,
				Label: label,
				Scope: Scope{Provider: provider, WindowID: windowID},
				Amount: Amount{
					Used:              new(usedFraction * 100),
					Limit:             new(float64(100)),
					Remaining:         new(remaining * 100),
					UsedFraction:      new(usedFraction),
					RemainingFraction: new(remaining),
					Unit:              "percent",
				},
				Status: percentStatus(usedFraction, false),
			}
			if windowID != "" {
				limit.Window = &Window{ID: windowID, Label: humanWindowLabel(windowID)}
				if duration := windowDurationMs(windowID); duration != nil {
					limit.Window.DurationMs = duration
				}
				if resetsAt := parseResetTime(bucket.ResetTime); resetsAt > 0 {
					limit.Window.ResetsAt = resetsAt
				}
			}
			limits = append(limits, limit)
		}
	}
	if len(limits) == 0 {
		return nil
	}

	report := &Report{
		Provider:  provider,
		FetchedAt: fetchedAt,
		Limits:    limits,
		Metadata:  map[string]any{"source": "quota-provider"},
	}
	if subscription := quota.Subscription; subscription != nil {
		plan := sanitizeText(subscription.Plan, 80)
		tier := sanitizeText(firstNonEmpty(subscription.TierName, subscription.TierID), 80)
		if tier != "" {
			report.Metadata["tier"] = tier
		}
		if plan == "" {
			plan = tier
		}
		if plan != "" {
			report.Metadata["planType"] = plan
		}
	}
	return report
}

func clampFraction(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func percentStatus(usedFraction float64, forceExhausted bool) string {
	if forceExhausted || usedFraction >= 1 {
		return "exhausted"
	}
	if usedFraction >= 0.9 {
		return "warning"
	}
	return "ok"
}

func uniqueLimitID(base string, seen map[string]int) string {
	count := seen[base]
	seen[base] = count + 1
	if count == 0 {
		return base
	}
	return base + ":" + strconv.Itoa(count+1)
}

func compactSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			builder.WriteRune(char)
			lastDash = false
		case !lastDash && builder.Len() > 0:
			builder.WriteByte('-')
			lastDash = true
		}
		if builder.Len() >= 40 {
			break
		}
	}
	return strings.Trim(builder.String(), "-")
}

func normalizeWindowID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '-', char == '_':
			builder.WriteRune(char)
		}
		if builder.Len() >= 32 {
			break
		}
	}
	return strings.Trim(builder.String(), "-_")
}

func humanWindowLabel(windowID string) string {
	switch windowID {
	case "":
		return "Limit"
	case "5h":
		return "5 Hour"
	case "7d":
		return "7 Day"
	case "1h":
		return "1 Hour"
	case "24h":
		return "24 Hour"
	case "daily", "day":
		return "Daily"
	case "weekly", "week":
		return "Weekly"
	case "monthly", "month":
		return "Monthly"
	}
	if value, ok := parseWindowCount(windowID); ok {
		label := pluralUnit(value.unit)
		return strconv.FormatInt(value.count, 10) + " " + label
	}
	words := strings.FieldsFunc(windowID, func(char rune) bool { return char == '-' || char == '_' })
	for index, word := range words {
		words[index] = strings.ToUpper(word[:1]) + word[1:]
	}
	if len(words) == 0 {
		return "Limit"
	}
	return strings.Join(words, " ")
}

type windowCount struct {
	count int64
	unit  string
}

func parseWindowCount(windowID string) (windowCount, bool) {
	if len(windowID) < 2 {
		return windowCount{}, false
	}
	unit := windowID[len(windowID)-1:]
	if unit != "h" && unit != "d" && unit != "m" {
		return windowCount{}, false
	}
	count, errParse := strconv.ParseInt(windowID[:len(windowID)-1], 10, 64)
	if errParse != nil || count <= 0 {
		return windowCount{}, false
	}
	return windowCount{count: count, unit: unit}, true
}

func pluralUnit(unit string) string {
	switch unit {
	case "h":
		return "Hour"
	case "d":
		return "Day"
	case "m":
		return "Minute"
	}
	return "Window"
}

func windowDurationMs(windowID string) *int64 {
	const (
		hourMs = int64(60 * 60 * 1000)
		dayMs  = 24 * hourMs
	)
	switch windowID {
	case "daily", "day":
		return new(dayMs)
	case "weekly", "week", "7d":
		return new(7 * dayMs)
	case "monthly", "month":
		return nil
	}
	parsed, ok := parseWindowCount(windowID)
	if !ok {
		return nil
	}
	var value int64
	switch parsed.unit {
	case "h":
		value = parsed.count * hourMs
	case "d":
		value = parsed.count * dayMs
	case "m":
		value = parsed.count * 60 * 1000
	default:
		return nil
	}
	if value <= 0 {
		return nil
	}
	return new(value)
}

func sanitizeText(value string, maxLength int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var builder strings.Builder
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			continue
		}
		builder.WriteRune(char)
		if builder.Len() >= maxLength {
			break
		}
	}
	return strings.TrimSpace(builder.String())
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
