package auth

import (
	"strings"
	"time"
)

// QuotaResetPattern defines the reset pattern types for provider quotas
type QuotaResetPattern string

const (
	// QuotaResetPatternDaily indicates daily quota reset (e.g., Gemini at midnight UTC)
	QuotaResetPatternDaily QuotaResetPattern = "daily"
	// QuotaResetPatternHourly indicates hourly rolling window (e.g., Anthropic free tier 5-hour window)
	QuotaResetPatternHourly QuotaResetPattern = "hourly"
	// QuotaResetPatternMonthly indicates monthly quota reset (e.g., Kimi on 1st of month)
	QuotaResetPatternMonthly QuotaResetPattern = "monthly"
	// QuotaResetPatternWeekly indicates weekly quota reset
	QuotaResetPatternWeekly QuotaResetPattern = "weekly"
	// QuotaResetPatternUnknown indicates unknown reset pattern (use exponential backoff)
	QuotaResetPatternUnknown QuotaResetPattern = "unknown"
)

// GetProviderResetPattern returns the expected quota reset pattern for a provider
func GetProviderResetPattern(provider string) QuotaResetPattern {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "anthropic", "claude":
		// Free tier: 5 hour rolling window; Pro tier varies
		return QuotaResetPatternHourly
	case "kimi", "minimax":
		// Monthly billing cycle, resets on 1st of month
		return QuotaResetPatternMonthly
	case "gemini":
		// Daily quota, resets at midnight UTC
		return QuotaResetPatternDaily
	case "openrouter":
		// Varies by upstream provider, treat as hourly
		return QuotaResetPatternHourly
	case "glm", "zhipu":
		// GLM has generous free tier limits
		return QuotaResetPatternDaily
	case "openai":
		// Monthly billing for paid tiers, but rate limits are per-minute
		return QuotaResetPatternHourly
	default:
		return QuotaResetPatternUnknown
	}
}

// GetProviderResetTimeZone returns the timezone for provider quota resets
func GetProviderResetTimeZone(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "anthropic", "claude":
		// Anthropic uses rolling windows, no specific timezone needed
		return "UTC"
	case "kimi":
		// Kimi operates on Beijing time
		return "Asia/Shanghai"
	case "gemini":
		// Gemini resets at midnight UTC
		return "UTC"
	case "minimax":
		return "Asia/Shanghai"
	default:
		return "UTC"
	}
}

// PredictQuotaResetTime predicts when quota will reset based on provider patterns
func PredictQuotaResetTime(provider string, fromTime time.Time) time.Time {
	pattern := GetProviderResetPattern(provider)
	tz := GetProviderResetTimeZone(provider)

	// Load timezone location
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}

	switch pattern {
	case QuotaResetPatternHourly:
		// For rolling windows (like Anthropic's 5-hour window)
		// Return time + 5 hours as a reasonable default
		return fromTime.Add(5 * time.Hour)

	case QuotaResetPatternDaily:
		// For daily resets at midnight in the provider's timezone
		localTime := fromTime.In(loc)
		nextDay := localTime.Add(24 * time.Hour)
		// Reset at midnight
		resetTime := time.Date(
			nextDay.Year(), nextDay.Month(), nextDay.Day(),
			0, 0, 0, 0,
			loc,
		)
		return resetTime.In(time.UTC)

	case QuotaResetPatternMonthly:
		// For monthly resets on the 1st of the month
		localTime := fromTime.In(loc)
		// Move to next month
		year := localTime.Year()
		month := localTime.Month() + 1
		if month > 12 {
			month = 1
			year++
		}
		resetTime := time.Date(
			year, month, 1,
			0, 0, 0, 0,
			loc,
		)
		return resetTime.In(time.UTC)

	case QuotaResetPatternWeekly:
		// Weekly reset (e.g., Sunday at midnight)
		localTime := fromTime.In(loc)
		// Calculate days until next Sunday
		daysUntilSunday := (7 - int(localTime.Weekday())) % 7
		if daysUntilSunday == 0 {
			daysUntilSunday = 7 // If today is Sunday, go to next Sunday
		}
		nextSunday := localTime.Add(time.Duration(daysUntilSunday) * 24 * time.Hour)
		resetTime := time.Date(
			nextSunday.Year(), nextSunday.Month(), nextSunday.Day(),
			0, 0, 0, 0,
			loc,
		)
		return resetTime.In(time.UTC)

	default:
		// Unknown pattern - use default 1 hour cooldown
		return fromTime.Add(1 * time.Hour)
	}
}

// QuotaUtilizationRate returns how much of the quota has been used (0.0 to 1.0)
// Returns -1 if max tokens is unknown
func (qs *QuotaState) QuotaUtilizationRate() float64 {
	if qs == nil || qs.MaxTokens <= 0 {
		return -1
	}
	if qs.UsedTokens >= qs.MaxTokens {
		return 1.0
	}
	return float64(qs.UsedTokens) / float64(qs.MaxTokens)
}

// HasRemainingQuota returns true if the auth has remaining quota allowance
func (qs *QuotaState) HasRemainingQuota() bool {
	if qs == nil {
		return true // No quota info means we don't know, assume available
	}
	if !qs.Exceeded {
		return true
	}
	// Check if the quota recovery time has passed
	if qs.NextRecoverAt.IsZero() {
		return false
	}
	return time.Now().After(qs.NextRecoverAt)
}

// IsQuotaNearExhaustion returns true if quota is approaching exhaustion (>80% used)
func (qs *QuotaState) IsQuotaNearExhaustion() bool {
	rate := qs.QuotaUtilizationRate()
	if rate < 0 {
		return false // Unknown
	}
	return rate > 0.8
}

// GetQuotaPriorityScore returns a priority score for quota-based selection
// Higher score = higher priority (more quota remaining, sooner reset if exhausted)
func (qs *QuotaState) GetQuotaPriorityScore(now time.Time) int {
	if qs == nil {
		return 100 // Default medium-high priority when no quota info
	}

	// If not exceeded, calculate priority based on remaining quota
	if !qs.Exceeded {
		utilRate := qs.QuotaUtilizationRate()
		if utilRate < 0 {
			return 100 // Unknown, medium-high priority
		}
		// Higher remaining quota = higher priority (max 200, min 0)
		return int((1.0 - utilRate) * 200)
	}

	// Quota exceeded - priority based on how soon it recovers
	if qs.NextRecoverAt.IsZero() {
		return -1000 // Very low priority if no recovery time set
	}

	// Calculate time until recovery
	untilRecovery := qs.NextRecoverAt.Sub(now)
	if untilRecovery <= 0 {
		return 50 // Should be recovered now
	}

	// Longer wait = lower priority (negative score proportional to wait time)
	// Subtract 1 point per minute of wait time, capped at -1000
	minutes := int(untilRecovery.Minutes())
	if minutes > 1000 {
		minutes = 1000
	}
	return -minutes
}

// RecordUsage adds to the used tokens count
func (qs *QuotaState) RecordUsage(tokens int64) {
	if qs == nil {
		return
	}
	qs.UsedTokens += tokens
}

// ResetUsage resets the used tokens count (called when quota resets)
func (qs *QuotaState) ResetUsage() {
	if qs == nil {
		return
	}
	qs.UsedTokens = 0
	qs.Exceeded = false
	qs.Reason = ""
	qs.NextRecoverAt = time.Time{}
	qs.BackoffLevel = 0
}

// IsRecoveringSoon returns true if the quota will recover within the given duration
func (qs *QuotaState) IsRecoveringSoon(within time.Duration, now time.Time) bool {
	if qs == nil || !qs.Exceeded {
		return true
	}
	if qs.NextRecoverAt.IsZero() {
		return false
	}
	return qs.NextRecoverAt.Sub(now) <= within
}

// HasAllowance returns true if the auth has quota allowance for the given model
// This is the primary API for checking if an auth can be used
func (a *Auth) HasAllowance(model string) bool {
	if a == nil {
		return false
	}

	// Check model-specific quota first
	if model != "" && len(a.ModelStates) > 0 {
		if state, ok := a.ModelStates[model]; ok && state != nil {
			return state.Quota.HasRemainingQuota()
		}
		// Try canonical model key
		baseModel := canonicalModelKey(model)
		if baseModel != "" && baseModel != model {
			if state, ok := a.ModelStates[baseModel]; ok && state != nil {
				return state.Quota.HasRemainingQuota()
			}
		}
	}

	// Fall back to auth-level quota
	return a.Quota.HasRemainingQuota()
}

// AllowancePriority returns a priority score for quota-based selection
// Higher score = better candidate (more quota remaining)
func (a *Auth) AllowancePriority(model string) int {
	if a == nil {
		return -10000
	}
	return getQuotaPriorityScore(a, model, time.Now())
}

// UpdateUsage records token usage for this auth
func (a *Auth) UpdateUsage(model string, tokens int64) {
	if a == nil || tokens <= 0 {
		return
	}

	// Update model-specific quota if exists
	if model != "" && len(a.ModelStates) > 0 {
		if state, ok := a.ModelStates[model]; ok && state != nil {
			state.Quota.RecordUsage(tokens)
		}
	}

	// Also update auth-level quota
	a.Quota.RecordUsage(tokens)
}

// GetQuotaInfo returns quota information for display/debugging
func (a *Auth) GetQuotaInfo(model string) map[string]interface{} {
	if a == nil {
		return nil
	}

	var qs *QuotaState
	if model != "" && len(a.ModelStates) > 0 {
		if state, ok := a.ModelStates[model]; ok && state != nil {
			qs = &state.Quota
		}
	}
	if qs == nil {
		qs = &a.Quota
	}

	return map[string]interface{}{
		"exceeded":       qs.Exceeded,
		"reason":         qs.Reason,
		"next_recover_at": qs.NextRecoverAt,
		"backoff_level":  qs.BackoffLevel,
		"reset_pattern":  qs.ResetPattern,
		"reset_timezone": qs.ResetTimeZone,
		"used_tokens":    qs.UsedTokens,
		"max_tokens":     qs.MaxTokens,
		"utilization":    qs.QuotaUtilizationRate(),
		"has_remaining":  qs.HasRemainingQuota(),
		"near_exhaustion": qs.IsQuotaNearExhaustion(),
	}
}
