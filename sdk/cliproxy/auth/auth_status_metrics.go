package auth

import (
	"net/http"
	"strings"
	"unicode"
)

const (
	// MetricReasonUnauthorized is a dead refresh token, invalid grant, or 401.
	MetricReasonUnauthorized = "unauthorized"
	// MetricReasonQuota is an auth-wide Quota.Exceeded cooldown.
	MetricReasonQuota = "quota"
	// MetricReasonOther is the catch-all for healthy and unclassified entries.
	MetricReasonOther = "other"

	metricProviderUnknown = "unknown"
	metricProviderMaxLen  = 64
)

// MetricAuthProvider returns a low-cardinality provider label for /metrics.
// It never returns an account, email, file name, or credential identifier.
func MetricAuthProvider(a *Auth) string {
	if a == nil {
		return metricProviderUnknown
	}
	return sanitizeMetricProvider(a.Provider)
}

// MetricAuthReason returns a coarse, bounded failure reason for /metrics.
// Values are unauthorized (refresh rejected / invalid grant), quota
// (Quota.Exceeded cooldown), and other.
func MetricAuthReason(a *Auth) string {
	if a == nil {
		return MetricReasonOther
	}
	if isUnauthorizedMetricReason(a) {
		return MetricReasonUnauthorized
	}
	if isQuotaMetricReason(a) {
		return MetricReasonQuota
	}
	return MetricReasonOther
}

func sanitizeMetricProvider(raw string) string {
	provider := strings.ToLower(strings.TrimSpace(raw))
	if provider == "" {
		return metricProviderUnknown
	}
	// Fold OpenAI-compat aliases so custom names cannot leak identity or cardinality.
	if provider == "openai-compatibility" || strings.HasPrefix(provider, "openai-compatible-") {
		return "openai-compatibility"
	}
	if !isSafeMetricProvider(provider) {
		return metricProviderUnknown
	}
	return provider
}

func isSafeMetricProvider(provider string) bool {
	if provider == "" || len(provider) > metricProviderMaxLen {
		return false
	}
	if strings.ContainsAny(provider, "@/\\") || strings.Contains(provider, ".json") {
		return false
	}
	for _, r := range provider {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func isUnauthorizedMetricReason(a *Auth) bool {
	if hasUnauthorizedAuthFailure(a) || hasDisabledInvalidGrantFailure(a) {
		return true
	}
	if isInvalidGrantResultError(a.LastError) || isInvalidGrantErrorMessage(a.StatusMessage) {
		return true
	}
	if a.LastError != nil {
		if a.LastError.StatusCode() == http.StatusUnauthorized || strings.EqualFold(a.LastError.Code, "unauthorized") {
			return true
		}
		if isUnauthorizedError(a.LastError) {
			return true
		}
	}
	lower := strings.ToLower(a.StatusMessage)
	return strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "invalid grant") ||
		strings.Contains(lower, "refresh token invalid")
}

func isQuotaMetricReason(a *Auth) bool {
	if a.Quota.Exceeded {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(a.Quota.Reason)) {
	case "quota", "credential_quota":
		return true
	default:
		return false
	}
}
