package usageapi

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const observedSignalsNote = "Measured from recent upstream response headers; values may lag live limits."

// reportFromObservedSignals turns the passive quota snapshot captured from
// upstream response headers into a report. It is the fallback when no quota
// provider handles the credential, and it only covers providers whose header
// formats the core auth package understands.
func reportFromObservedSignals(auth *coreauth.Auth, provider string) *Report {
	if auth == nil || auth.Quota.ObservedAt.IsZero() || len(auth.Quota.Signals) == 0 {
		return nil
	}
	fetchedAt := auth.Quota.ObservedAt.UnixMilli()
	if fetchedAt <= 0 {
		return nil
	}
	signals := lowerSignalMap(auth.Quota.Signals)
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		return observedClaudeReport(provider, signals, fetchedAt)
	case "codex":
		return observedCodexReport(provider, signals, fetchedAt)
	case "devin":
		return observedDevinReport(provider, signals, fetchedAt)
	default:
		return nil
	}
}

func lowerSignalMap(signals map[string]string) map[string]string {
	out := make(map[string]string, len(signals))
	for key, value := range signals {
		out[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return out
}

func observedClaudeReport(provider string, signals map[string]string, fetchedAt int64) *Report {
	windows := []struct {
		key      string
		id       string
		label    string
		windowID string
	}{
		{key: "5h", id: "claude:5h", label: "5 Hour", windowID: "5h"},
		{key: "7d", id: "claude:7d", label: "7 Day", windowID: "7d"},
		{key: "7d_oi", id: "claude:7d:fable", label: "7 Day (Fable)", windowID: "7d"},
	}
	limits := make([]Limit, 0, len(windows))
	for _, window := range windows {
		prefix := "anthropic-ratelimit-unified-" + window.key + "-"
		usedPercent, hasUsed := parseUtilizationPercent(signals[prefix+"utilization"])
		resetsAt := parseEpochMillis(signals[prefix+"reset"])
		if !hasUsed && resetsAt == 0 {
			continue
		}
		status := strings.ToLower(signals[prefix+"status"])
		limits = append(limits, newPercentLimit(
			provider,
			window.id,
			window.label,
			window.windowID,
			usedPercent,
			resetsAt,
			status == "rejected",
		))
	}
	if len(limits) == 0 {
		return nil
	}
	return &Report{
		Provider:  provider,
		FetchedAt: fetchedAt,
		Limits:    limits,
		Notes:     []string{observedSignalsNote},
		Metadata:  map[string]any{"source": "observed-signals"},
	}
}

func observedCodexReport(provider string, signals map[string]string, fetchedAt int64) *Report {
	limits := make([]Limit, 0, 4)
	for _, key := range []string{"primary", "secondary"} {
		prefix := "x-codex-" + key + "-"
		usedPercent, ok := parseSignalFloat(signals[prefix+"used-percent"])
		if !ok {
			continue
		}
		minutes, _ := parseSignalFloat(signals[prefix+"window-minutes"])
		windowID, windowLabel := codexWindow(minutes, key)
		status := codexStatus(
			clampPercent(usedPercent)/100,
			parseSignalBool(signals["x-codex-limit-reached"]),
			parseSignalBool(signals["x-codex-allowed"]),
		)
		limit := newPercentLimit(provider, "codex:"+key, windowLabel, windowID, usedPercent, parseEpochMillis(signals[prefix+"reset-at"]), false)
		limit.Status = status
		limits = append(limits, limit)
	}

	for _, additional := range codexAdditionalPrefixes(signals) {
		for _, key := range []string{"primary", "secondary"} {
			prefix := additional.prefix + "-" + key + "-"
			usedPercent, ok := parseSignalFloat(signals[prefix+"used-percent"])
			if !ok {
				continue
			}
			minutes, _ := parseSignalFloat(signals[prefix+"window-minutes"])
			windowID, windowLabel := codexWindow(minutes, key)
			displayName := humanizeCodexLimitName(additional.name)
			label := windowLabel
			if displayName != "" {
				label += " (" + displayName + ")"
			}
			status := codexStatus(
				clampPercent(usedPercent)/100,
				parseSignalBool(signals[additional.prefix+"-limit-reached"]),
				parseSignalBool(signals[additional.prefix+"-allowed"]),
			)
			limit := newPercentLimit(
				provider,
				"codex:"+compactSlug(additional.name)+":"+key,
				label,
				windowID,
				usedPercent,
				parseEpochMillis(signals[prefix+"reset-at"]),
				false,
			)
			limit.Status = status
			limits = append(limits, limit)
		}
	}

	if len(limits) == 0 {
		return nil
	}
	metadata := map[string]any{"source": "observed-signals"}
	if plan := sanitizeText(signals["x-codex-plan-type"], 80); plan != "" {
		metadata["planType"] = plan
	}
	return &Report{
		Provider:  provider,
		FetchedAt: fetchedAt,
		Limits:    limits,
		Notes:     []string{observedSignalsNote},
		Metadata:  metadata,
	}
}

type codexAdditionalLimit struct {
	prefix string
	name   string
}

func codexAdditionalPrefixes(signals map[string]string) []codexAdditionalLimit {
	seen := make(map[string]string)
	for key := range signals {
		for _, suffix := range []string{"-primary-used-percent", "-secondary-used-percent"} {
			if !strings.HasSuffix(key, suffix) {
				continue
			}
			prefix := strings.TrimSuffix(key, suffix)
			if prefix == "x-codex" || strings.HasPrefix(prefix, "x-codex-code-review") {
				continue
			}
			name := strings.TrimPrefix(prefix, "x-codex-additional-")
			if name == prefix {
				name = strings.TrimPrefix(prefix, "x-codex-")
			}
			name = strings.TrimSpace(name)
			if name != "" {
				seen[prefix] = name
			}
		}
	}
	out := make([]codexAdditionalLimit, 0, len(seen))
	for prefix, name := range seen {
		out = append(out, codexAdditionalLimit{prefix: prefix, name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func codexWindow(minutes float64, fallback string) (string, string) {
	if fallback == "" {
		fallback = "window"
	}
	if minutes <= 0 || math.IsNaN(minutes) || math.IsInf(minutes, 0) {
		return fallback, strings.ToUpper(fallback[:1]) + fallback[1:] + " window"
	}
	durationMs := int64(minutes * 60 * 1000)
	if windowID := windowIDFromDuration(durationMs); windowID != "" {
		return windowID, humanWindowLabel(windowID)
	}
	return fallback, strings.ToUpper(fallback[:1]) + fallback[1:] + " window"
}

func windowIDFromDuration(durationMs int64) string {
	const (
		minuteMs = int64(60 * 1000)
		hourMs   = 60 * minuteMs
		dayMs    = 24 * hourMs
	)
	if durationMs <= 0 {
		return ""
	}
	switch {
	case durationMs%dayMs == 0:
		return fmt.Sprintf("%dd", durationMs/dayMs)
	case durationMs%hourMs == 0:
		return fmt.Sprintf("%dh", durationMs/hourMs)
	case durationMs%minuteMs == 0:
		return fmt.Sprintf("%dm", durationMs/minuteMs)
	default:
		return ""
	}
}

func codexStatus(usedFraction float64, limitReached, allowed bool) string {
	if limitReached {
		return "exhausted"
	}
	if usedFraction >= 1 && allowed {
		return "warning"
	}
	return percentStatus(usedFraction, false)
}

func humanizeCodexLimitName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.Contains(strings.ToLower(name), "spark") {
		return "Spark"
	}
	words := strings.FieldsFunc(name, func(char rune) bool { return char == '-' || char == '_' })
	for index, word := range words {
		words[index] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

func observedDevinReport(provider string, signals map[string]string, fetchedAt int64) *Report {
	windows := []struct {
		key      string
		label    string
		windowID string
	}{
		{key: "daily", label: "Daily", windowID: "daily"},
		{key: "weekly", label: "Weekly", windowID: "weekly"},
	}
	limits := make([]Limit, 0, len(windows))
	for _, window := range windows {
		remaining, hasRemaining := parseSignalPercent(signals[window.key+"_quota_remaining_percent"])
		resetsAt := parseResetTime(signals[window.key+"_quota_reset_at"])
		if !hasRemaining && resetsAt == 0 {
			continue
		}
		limits = append(limits, newPercentLimit(
			provider,
			"devin:"+window.key,
			window.label,
			window.windowID,
			100-remaining,
			resetsAt,
			false,
		))
	}
	if len(limits) == 0 {
		return nil
	}
	metadata := map[string]any{"source": "observed-signals"}
	if plan := sanitizeText(signals["plan"], 80); plan != "" {
		metadata["planType"] = plan
	}
	return &Report{
		Provider:  provider,
		FetchedAt: fetchedAt,
		Limits:    limits,
		Notes:     []string{observedSignalsNote},
		Metadata:  metadata,
	}
}

func newPercentLimit(provider, id, label, windowID string, usedPercent float64, resetsAt int64, forceExhausted bool) Limit {
	used := clampPercent(usedPercent)
	fraction := used / 100
	limit := Limit{
		ID:    id,
		Label: label,
		Scope: Scope{Provider: provider, WindowID: windowID},
		Amount: Amount{
			Used:              new(used),
			Limit:             new(float64(100)),
			Remaining:         new(100 - used),
			UsedFraction:      new(fraction),
			RemainingFraction: new(1 - fraction),
			Unit:              "percent",
		},
		Status: percentStatus(fraction, forceExhausted),
	}
	if windowID != "" {
		window := &Window{ID: windowID, Label: humanWindowLabel(windowID)}
		if duration := windowDurationMs(windowID); duration != nil {
			window.DurationMs = duration
		}
		if resetsAt > 0 {
			window.ResetsAt = resetsAt
		}
		limit.Window = window
	}
	return limit
}

func clampPercent(value float64) float64 {
	if math.IsNaN(value) || value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func parseUtilizationPercent(raw string) (float64, bool) {
	value, errParse := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if errParse != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	// Anthropic reports utilization as a 0..1 fraction. Tolerate a
	// percent-scaled value so a header format change cannot silently zero it.
	if value > 1 {
		return clampPercent(value), true
	}
	return clampPercent(value * 100), true
}

func parseSignalPercent(raw string) (float64, bool) {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "%")
	value, errParse := strconv.ParseFloat(raw, 64)
	if errParse != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return clampPercent(value), true
}

func parseSignalFloat(raw string) (float64, bool) {
	value, errParse := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if errParse != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

func parseSignalBool(raw string) bool {
	value, errParse := strconv.ParseBool(strings.TrimSpace(raw))
	return errParse == nil && value
}

func parseEpochMillis(raw string) int64 {
	value, errParse := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if errParse != nil {
		return 0
	}
	return epochMillis(value)
}

func epochMillis(value float64) int64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	switch {
	case value >= 1e17:
		return int64(value / 1e6)
	case value >= 1e14:
		return int64(value / 1e3)
	case value >= 1e12:
		return int64(value)
	default:
		return int64(value * 1000)
	}
}

func parseResetTime(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if value, errParse := strconv.ParseFloat(raw, 64); errParse == nil {
		return epochMillis(value)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if parsed, errParse := time.Parse(layout, raw); errParse == nil {
			return parsed.UnixMilli()
		}
	}
	return 0
}
