// Package fallback provides model error auto-fallback logic for the CLI Proxy API.
// When a requested model returns specific errors, this package determines whether to
// retry with alternative model IDs from a configured fallback chain.
package fallback

import (
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// AttemptContext holds context for a single fallback attempt.
type AttemptContext struct {
	RequestedModel string
	AttemptModel   string
	AttemptIndex   int
	Providers      []string
}

// TriggerDecision describes whether an error should trigger model fallback.
type TriggerDecision struct {
	ShouldFallback bool
	Reason         string
	StatusCode     int
}

// ChainResult summarizes the outcome of a fallback chain execution.
type ChainResult struct {
	FinalModel   string
	AttemptCount int
	Exhausted    bool
	ElapsedMS    int64
}

// ResponseHeaders contains the header names used for fallback observability.
const (
	HeaderActualModel      = "X-Actual-Model"
	HeaderRequestedModel   = "X-Requested-Model"
	HeaderFallbackAttempts = "X-Model-Fallback-Attempts"
)

// defaultTriggerCodes are HTTP status codes that trigger fallback by default.
var defaultTriggerCodes = map[int]struct{}{
	http.StatusNotFound:            {},
	http.StatusRequestTimeout:      {},
	http.StatusTooManyRequests:     {},
	http.StatusInternalServerError: {},
	http.StatusBadGateway:          {},
	http.StatusServiceUnavailable:  {},
	http.StatusGatewayTimeout:      {},
}

// defaultNoFallbackCodes are HTTP status codes that never trigger fallback.
var defaultNoFallbackCodes = map[int]struct{}{
	http.StatusBadRequest:   {},
	http.StatusUnauthorized: {},
	http.StatusForbidden:    {},
}

// IsEnabled returns whether fallback is enabled for the given config.
func IsEnabled(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	return cfg.ModelFallback.Enabled
}

// IsStreamEnabled returns whether fallback is enabled for streaming requests.
func IsStreamEnabled(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	fb := cfg.ModelFallback
	if !fb.Enabled {
		return false
	}
	if fb.Stream.Enabled == nil {
		return true // nil inherits from parent Enabled
	}
	return *fb.Stream.Enabled
}

// Deadline returns the fallback chain deadline based on config timeout.
// Zero means no deadline. Negative values (e.g., -1) also mean no deadline (explicit opt-out).
func Deadline(cfg *config.Config) time.Time {
	if cfg == nil {
		return time.Time{}
	}
	ms := cfg.ModelFallback.MaxTotalTimeoutMS
	if ms <= 0 {
		return time.Time{}
	}
	return time.Now().Add(time.Duration(ms) * time.Millisecond)
}

// MaxAttempts returns the effective max attempts for a model, considering overrides.
func MaxAttempts(cfg *config.Config, model string) int {
	if cfg == nil {
		return 1
	}
	fb := cfg.ModelFallback
	key := strings.ToLower(strings.TrimSpace(model))
	if override, ok := fb.ModelOverrides[key]; ok && override.MaxAttempts > 0 {
		return override.MaxAttempts
	}
	if fb.MaxAttempts > 0 {
		return fb.MaxAttempts
	}
	return 3
}

// TriggerAfterRetries returns how many same-model fallback-eligible retries should occur
// before switching to the next fallback model.
//
// Resolution order:
// 1) model-fallback.trigger-after-retries (if set)
// 2) top-level request-retry
//
// The value is clamped to [0, 10].
func TriggerAfterRetries(cfg *config.Config) int {
	if cfg == nil {
		return 0
	}
	if cfg.ModelFallback.TriggerAfterRetries != nil {
		v := *cfg.ModelFallback.TriggerAfterRetries
		if v < 0 {
			return 0
		}
		if v > 10 {
			return 10
		}
		return v
	}
	v := cfg.RequestRetry
	if v < 0 {
		return 0
	}
	if v > 10 {
		return 10
	}
	return v
}
