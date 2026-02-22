// Package ratelimit provides configurable rate limiting for API providers.
// Supports RPM (Requests Per Minute), TPM (Tokens Per Minute), 
// RPD (Requests Per Day), and TPD (Tokens Per Day) limits.
package ratelimit

// RateLimitConfig defines rate limit settings for a provider/credential.
// All limits are optional - set to 0 to disable a specific limit.
type RateLimitConfig struct {
	// RPM is the maximum requests per minute. 0 means no limit.
	RPM int `yaml:"rpm" json:"rpm"`

	// TPM is the maximum tokens per minute. 0 means no limit.
	TPM int `yaml:"tpm" json:"tpm"`

	// RPD is the maximum requests per day. 0 means no limit.
	RPD int `yaml:"rpd" json:"rpd"`

	// TPD is the maximum tokens per day. 0 means no limit.
	TPD int `yaml:"tpd" json:"tpd"`

	// WaitOnLimit controls behavior when a limit is exceeded.
	// If true, the request will wait until the limit resets.
	// If false (default), the request is rejected immediately with HTTP 429.
	WaitOnLimit bool `yaml:"wait-on-limit" json:"wait-on-limit"`

	// MaxWaitSeconds is the maximum time to wait when WaitOnLimit is true.
	// 0 means wait indefinitely (not recommended). Default: 30.
	MaxWaitSeconds int `yaml:"max-wait-seconds" json:"max-wait-seconds"`
}

// IsEmpty returns true if no rate limits are configured.
func (c *RateLimitConfig) IsEmpty() bool {
	return c == nil || (c.RPM == 0 && c.TPM == 0 && c.RPD == 0 && c.TPD == 0)
}

// HasRequestLimit returns true if any request-based limit is configured.
func (c *RateLimitConfig) HasRequestLimit() bool {
	return c != nil && (c.RPM > 0 || c.RPD > 0)
}

// HasTokenLimit returns true if any token-based limit is configured.
func (c *RateLimitConfig) HasTokenLimit() bool {
	return c != nil && (c.TPM > 0 || c.TPD > 0)
}

// GetMaxWaitDuration returns the maximum wait time as a duration in seconds.
func (c *RateLimitConfig) GetMaxWaitDuration() int {
	if c == nil || c.MaxWaitSeconds <= 0 {
		return 30 // default 30 seconds
	}
	return c.MaxWaitSeconds
}

// RateLimitStatus represents the current status of rate limits for a credential.
type RateLimitStatus struct {
	// Provider is the provider name (e.g., "gemini", "claude").
	Provider string `json:"provider"`

	// CredentialID is the identifier for this credential (e.g., API key prefix).
	CredentialID string `json:"credential_id"`

	// MinuteWindow contains the current minute window usage.
	MinuteWindow WindowStatus `json:"minute_window"`

	// DayWindow contains the current day window usage.
	DayWindow WindowStatus `json:"day_window"`

	// IsLimited is true if any limit is currently exceeded.
	IsLimited bool `json:"is_limited"`

	// LimitType describes which limit is hit, if any.
	LimitType string `json:"limit_type,omitempty"`

	// ResetAt is the time when the current limit will reset (Unix timestamp).
	ResetAt int64 `json:"reset_at,omitempty"`

	// WaitSeconds is the estimated wait time in seconds (if limited).
	WaitSeconds int `json:"wait_seconds,omitempty"`
}

// WindowStatus contains usage statistics for a time window.
type WindowStatus struct {
	// Requests is the number of requests in the current window.
	Requests int64 `json:"requests"`

	// Tokens is the number of tokens in the current window.
	Tokens int64 `json:"tokens"`

	// RequestLimit is the configured request limit (0 if unlimited).
	RequestLimit int `json:"request_limit"`

	// TokenLimit is the configured token limit (0 if unlimited).
	TokenLimit int `json:"token_limit"`

	// WindowStart is the start time of the window (Unix timestamp).
	WindowStart int64 `json:"window_start"`

	// WindowEnd is the end time of the window (Unix timestamp).
	WindowEnd int64 `json:"window_end"`
}

// RateLimitError represents an error when a rate limit is exceeded.
type RateLimitError struct {
	LimitType   string
	ResetAt     int64
	WaitSeconds int
}

func (e *RateLimitError) Error() string {
	return "rate limit exceeded: " + e.LimitType
}

// IsRateLimitError checks if an error is a rate limit error.
func IsRateLimitError(err error) bool {
	_, ok := err.(*RateLimitError)
	return ok
}
