package ratelimit

import (
	"sync"
	"time"
)

// SlidingWindow implements a sliding window rate limiter.
// It tracks both requests and tokens over configurable time windows.
type SlidingWindow struct {
	mu sync.RWMutex

	// Provider identifier
	provider string

	// Credential identifier (e.g., API key prefix)
	credentialID string

	// Configuration
	config RateLimitConfig

	// Minute window state
	minuteRequests  int64
	minuteTokens    int64
	minuteWindowEnd int64

	// Day window state
	dayRequests  int64
	dayTokens    int64
	dayWindowEnd int64
}

// NewSlidingWindow creates a new sliding window rate limiter.
func NewSlidingWindow(provider, credentialID string, config RateLimitConfig) *SlidingWindow {
	now := time.Now()
	return &SlidingWindow{
		provider:        provider,
		credentialID:    credentialID,
		config:          config,
		minuteWindowEnd: now.Truncate(time.Minute).Add(time.Minute).Unix(),
		dayWindowEnd:    now.Truncate(24 * time.Hour).Add(24 * time.Hour).Unix(),
	}
}

// TryConsume attempts to consume capacity from the rate limiter.
// If allowWait is true and the config allows waiting, it will wait up to maxWait.
// Returns an error if the limit would be exceeded.
func (sw *SlidingWindow) TryConsume(requests int64, tokens int64, allowWait bool) error {
	if sw.config.IsEmpty() {
		return nil
	}

	sw.mu.Lock()
	defer sw.mu.Unlock()

	now := time.Now().Unix()
	sw.resetWindowsIfNeeded(now)

	// Check minute limits
	if sw.config.RPM > 0 && sw.minuteRequests+requests > int64(sw.config.RPM) {
		waitSec := int(sw.minuteWindowEnd - now)
		if sw.config.WaitOnLimit && allowWait && waitSec <= sw.config.GetMaxWaitDuration() {
			sw.mu.Unlock()
			time.Sleep(time.Duration(waitSec) * time.Second)
			sw.mu.Lock()
			sw.resetWindowsIfNeeded(time.Now().Unix())
		} else {
			return &RateLimitError{
				LimitType:   "rpm",
				ResetAt:     sw.minuteWindowEnd,
				WaitSeconds: waitSec,
			}
		}
	}

	if sw.config.TPM > 0 && sw.minuteTokens+tokens > int64(sw.config.TPM) {
		waitSec := int(sw.minuteWindowEnd - now)
		if sw.config.WaitOnLimit && allowWait && waitSec <= sw.config.GetMaxWaitDuration() {
			sw.mu.Unlock()
			time.Sleep(time.Duration(waitSec) * time.Second)
			sw.mu.Lock()
			sw.resetWindowsIfNeeded(time.Now().Unix())
		} else {
			return &RateLimitError{
				LimitType:   "tpm",
				ResetAt:     sw.minuteWindowEnd,
				WaitSeconds: waitSec,
			}
		}
	}

	// Check day limits
	if sw.config.RPD > 0 && sw.dayRequests+requests > int64(sw.config.RPD) {
		waitSec := int(sw.dayWindowEnd - now)
		if sw.config.WaitOnLimit && allowWait && waitSec <= sw.config.GetMaxWaitDuration() {
			sw.mu.Unlock()
			time.Sleep(time.Duration(waitSec) * time.Second)
			sw.mu.Lock()
			sw.resetWindowsIfNeeded(time.Now().Unix())
		} else {
			return &RateLimitError{
				LimitType:   "rpd",
				ResetAt:     sw.dayWindowEnd,
				WaitSeconds: waitSec,
			}
		}
	}

	if sw.config.TPD > 0 && sw.dayTokens+tokens > int64(sw.config.TPD) {
		waitSec := int(sw.dayWindowEnd - now)
		if sw.config.WaitOnLimit && allowWait && waitSec <= sw.config.GetMaxWaitDuration() {
			sw.mu.Unlock()
			time.Sleep(time.Duration(waitSec) * time.Second)
			sw.mu.Lock()
			sw.resetWindowsIfNeeded(time.Now().Unix())
		} else {
			return &RateLimitError{
				LimitType:   "tpd",
				ResetAt:     sw.dayWindowEnd,
				WaitSeconds: waitSec,
			}
		}
	}

	// Consume the capacity
	sw.minuteRequests += requests
	sw.minuteTokens += tokens
	sw.dayRequests += requests
	sw.dayTokens += tokens

	return nil
}

// RecordUsage records actual usage after a request completes.
// This is used to update token counts based on actual response data.
func (sw *SlidingWindow) RecordUsage(requests int64, tokens int64) {
	if sw.config.IsEmpty() {
		return
	}

	sw.mu.Lock()
	defer sw.mu.Unlock()

	now := time.Now().Unix()
	sw.resetWindowsIfNeeded(now)

	sw.minuteRequests += requests
	sw.minuteTokens += tokens
	sw.dayRequests += requests
	sw.dayTokens += tokens
}

// GetStatus returns the current rate limit status.
func (sw *SlidingWindow) GetStatus() RateLimitStatus {
	sw.mu.RLock()
	defer sw.mu.RUnlock()

	now := time.Now().Unix()
	sw.resetWindowsIfNeeded(now)

	status := RateLimitStatus{
		Provider:     sw.provider,
		CredentialID: sw.credentialID,
		MinuteWindow: WindowStatus{
			Requests:     sw.minuteRequests,
			Tokens:       sw.minuteTokens,
			RequestLimit: sw.config.RPM,
			TokenLimit:   sw.config.TPM,
			WindowStart:  sw.minuteWindowEnd - 60,
			WindowEnd:    sw.minuteWindowEnd,
		},
		DayWindow: WindowStatus{
			Requests:     sw.dayRequests,
			Tokens:       sw.dayTokens,
			RequestLimit: sw.config.RPD,
			TokenLimit:   sw.config.TPD,
			WindowStart:  sw.dayWindowEnd - 86400,
			WindowEnd:    sw.dayWindowEnd,
		},
	}

	// Check if any limit is exceeded
	if sw.config.RPM > 0 && sw.minuteRequests >= int64(sw.config.RPM) {
		status.IsLimited = true
		status.LimitType = "rpm"
		status.ResetAt = sw.minuteWindowEnd
		status.WaitSeconds = int(sw.minuteWindowEnd - now)
	} else if sw.config.TPM > 0 && sw.minuteTokens >= int64(sw.config.TPM) {
		status.IsLimited = true
		status.LimitType = "tpm"
		status.ResetAt = sw.minuteWindowEnd
		status.WaitSeconds = int(sw.minuteWindowEnd - now)
	} else if sw.config.RPD > 0 && sw.dayRequests >= int64(sw.config.RPD) {
		status.IsLimited = true
		status.LimitType = "rpd"
		status.ResetAt = sw.dayWindowEnd
		status.WaitSeconds = int(sw.dayWindowEnd - now)
	} else if sw.config.TPD > 0 && sw.dayTokens >= int64(sw.config.TPD) {
		status.IsLimited = true
		status.LimitType = "tpd"
		status.ResetAt = sw.dayWindowEnd
		status.WaitSeconds = int(sw.dayWindowEnd - now)
	}

	return status
}

// UpdateConfig updates the rate limit configuration.
func (sw *SlidingWindow) UpdateConfig(config RateLimitConfig) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.config = config
}

// resetWindowsIfNeeded resets window counters when the window expires.
// Must be called with the lock held.
func (sw *SlidingWindow) resetWindowsIfNeeded(now int64) {
	// Reset minute window if expired
	if now >= sw.minuteWindowEnd {
		sw.minuteRequests = 0
		sw.minuteTokens = 0
		// Align to minute boundary
		sw.minuteWindowEnd = (now/60 + 1) * 60
	}

	// Reset day window if expired
	if now >= sw.dayWindowEnd {
		sw.dayRequests = 0
		sw.dayTokens = 0
		// Align to day boundary (midnight UTC)
		sw.dayWindowEnd = (now/86400 + 1) * 86400
	}
}
