package fallback

import (
	"net"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// Classify determines whether an error should trigger model fallback.
func Classify(err error, attemptIndex int, cfg *config.Config) TriggerDecision {
	if err == nil {
		return TriggerDecision{ShouldFallback: false, Reason: "no error"}
	}
	if cfg == nil || !cfg.ModelFallback.Enabled {
		return TriggerDecision{ShouldFallback: false, Reason: "fallback disabled"}
	}
	fb := cfg.ModelFallback

	// Extract status code from error
	statusCode := statusFromError(err)

	// Check for model_not_found in error message first (always fallback, regardless of status code)
	errMsg := strings.ToLower(err.Error())
	if strings.Contains(errMsg, "model_not_found") || strings.Contains(errMsg, "model not found") {
		return TriggerDecision{
			ShouldFallback: true,
			Reason:         "model_not_found",
			StatusCode:     statusCode,
		}
	}

	// Check no-fallback codes (takes precedence over trigger codes)
	if statusCode > 0 {
		noFallbackSet := buildCodeSet(fb.NoFallbackStatusCodes)
		if _, blocked := noFallbackSet[statusCode]; blocked {
			return TriggerDecision{
				ShouldFallback: false,
				Reason:         "status code in no-fallback list",
				StatusCode:     statusCode,
			}
		}
	}

	// Check trigger status codes
	if statusCode > 0 {
		triggerSet := buildCodeSet(fb.TriggerStatusCodes)
		if _, triggered := triggerSet[statusCode]; triggered {
			return TriggerDecision{
				ShouldFallback: true,
				Reason:         "trigger status code",
				StatusCode:     statusCode,
			}
		}
	}

	// Check network errors
	if fb.AllowNetworkError && isNetworkError(err) {
		return TriggerDecision{
			ShouldFallback: true,
			Reason:         "network error",
			StatusCode:     0,
		}
	}

	// Status code 0 with no network error match — not fallback-eligible
	if statusCode == 0 {
		return TriggerDecision{
			ShouldFallback: false,
			Reason:         "unknown error without status code",
			StatusCode:     0,
		}
	}

	return TriggerDecision{
		ShouldFallback: false,
		Reason:         "status code not in trigger list",
		StatusCode:     statusCode,
	}
}

// statusFromError extracts HTTP status code from an error via StatusCode() interface.
func statusFromError(err error) int {
	if err == nil {
		return 0
	}
	if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
		if code := se.StatusCode(); code > 0 {
			return code
		}
	}
	return 0
}

// isNetworkError checks if the error is a network-level error.
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	// Check for net.Error (timeout, connection refused, etc.)
	if _, ok := err.(net.Error); ok {
		return true
	}
	// Check for wrapped net errors
	errMsg := strings.ToLower(err.Error())
	return strings.Contains(errMsg, "connection refused") ||
		strings.Contains(errMsg, "connection reset") ||
		strings.Contains(errMsg, "no such host") ||
		strings.Contains(errMsg, "i/o timeout")
}

// buildCodeSet converts a slice of status codes to a map for O(1) lookup.
func buildCodeSet(codes []int) map[int]struct{} {
	m := make(map[int]struct{}, len(codes))
	for _, c := range codes {
		m[c] = struct{}{}
	}
	return m
}
