package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
)

const DefaultSanitizedErrorMessageMaxRunes = 1024

var sensitiveErrorMessagePatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{
		pattern:     regexp.MustCompile(`(?i)(authorization\s*:\s*bearer)\s+[^\s,;]+`),
		replacement: `$1 <redacted>`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]+\b`),
		replacement: `bearer <redacted>`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|token|secret|password)\s*[:=]\s*([^\s,;]+)`),
		replacement: `$1=<redacted>`,
	},
	{
		pattern:     regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|token|key|secret)=([^&\s]+)`),
		replacement: `$1=<redacted>`,
	},
}

// NormalizeCircuitBreakerModelID normalizes model ID for failure tracking.
func NormalizeCircuitBreakerModelID(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return ""
	}
	parsed := thinking.ParseSuffix(trimmed)
	if base := strings.TrimSpace(parsed.ModelName); base != "" {
		return base
	}
	return trimmed
}

// IsModelSupportErrorMessage reports whether message indicates unsupported model.
func IsModelSupportErrorMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	patterns := [...]string{
		"model_not_supported",
		"requested model is not supported",
		"requested model is unsupported",
		"requested model is unavailable",
		"model is not supported",
		"model not supported",
		"unsupported model",
		"model unavailable",
		"not available for your plan",
		"not available for your account",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// IsRequestScopedNotFoundMessage reports request-scoped 404 caused by store=false semantics.
func IsRequestScopedNotFoundMessage(message string) bool {
	if message == "" {
		return false
	}
	lower := strings.ToLower(message)
	return strings.Contains(lower, "item with id") &&
		strings.Contains(lower, "not found") &&
		strings.Contains(lower, "items are not persisted when `store` is set to false")
}

// IsRequestInvalidByStatus reports whether error should be excluded from circuit-breaker counting.
func IsRequestInvalidByStatus(statusCode int, message string) (invalid bool, reason string) {
	if (statusCode == http.StatusBadRequest || statusCode == http.StatusUnprocessableEntity) && IsModelSupportErrorMessage(message) {
		return false, ""
	}
	switch statusCode {
	case http.StatusBadRequest:
		if strings.Contains(strings.ToLower(strings.TrimSpace(message)), "invalid_request_error") {
			return true, "invalid_request"
		}
		return false, ""
	case http.StatusNotFound:
		if IsRequestScopedNotFoundMessage(message) {
			return true, "request_scoped_not_found"
		}
		return false, ""
	case http.StatusUnprocessableEntity:
		return true, "unprocessable_entity"
	default:
		return false, ""
	}
}

// IsCircuitCountableFailure reports whether one failure is eligible for circuit-breaker counting.
func IsCircuitCountableFailure(statusCode int, message string) (countable bool, skipReason string) {
	invalid, reason := IsRequestInvalidByStatus(statusCode, message)
	if invalid {
		return false, reason
	}
	return true, ""
}

// SanitizeErrorMessageForStore removes secrets and returns the masked message
// plus a deterministic hash for aggregation.
func SanitizeErrorMessageForStore(message string, maxRunes int) (masked string, hash string) {
	masked = strings.TrimSpace(message)
	if masked == "" {
		return "", ""
	}
	for _, rule := range sensitiveErrorMessagePatterns {
		masked = rule.pattern.ReplaceAllString(masked, rule.replacement)
	}
	sum := sha256.Sum256([]byte(masked))
	hash = hex.EncodeToString(sum[:])
	if maxRunes <= 0 {
		maxRunes = DefaultSanitizedErrorMessageMaxRunes
	}
	runes := []rune(masked)
	if len(runes) > maxRunes {
		masked = string(runes[:maxRunes])
	}
	return masked, hash
}
