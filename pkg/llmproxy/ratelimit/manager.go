package ratelimit

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Manager manages rate limiters for all providers and credentials.
type Manager struct {
	mu       sync.RWMutex
	limiters map[string]*SlidingWindow // key: provider:credentialID
}

// globalManager is the singleton rate limit manager.
var globalManager = NewManager()

// NewManager creates a new rate limit manager.
func NewManager() *Manager {
	return &Manager{
		limiters: make(map[string]*SlidingWindow),
	}
}

// GetManager returns the global rate limit manager.
func GetManager() *Manager {
	return globalManager
}

// makeKey creates a unique key for a provider/credential combination.
func makeKey(provider, credentialID string) string {
	return provider + ":" + credentialID
}

// GetLimiter returns the rate limiter for a provider/credential.
// If no limiter exists, it creates one with the given config.
func (m *Manager) GetLimiter(provider, credentialID string, config RateLimitConfig) *SlidingWindow {
	if config.IsEmpty() {
		return nil
	}

	key := makeKey(provider, credentialID)

	m.mu.RLock()
	limiter, exists := m.limiters[key]
	m.mu.RUnlock()

	if exists {
		limiter.UpdateConfig(config)
		return limiter
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double check after acquiring write lock
	if limiter, exists = m.limiters[key]; exists {
		limiter.UpdateConfig(config)
		return limiter
	}

	limiter = NewSlidingWindow(provider, credentialID, config)
	m.limiters[key] = limiter
	return limiter
}

// RemoveLimiter removes a rate limiter for a provider/credential.
func (m *Manager) RemoveLimiter(provider, credentialID string) {
	key := makeKey(provider, credentialID)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.limiters, key)
}

// GetStatus returns the rate limit status for a provider/credential.
func (m *Manager) GetStatus(provider, credentialID string) *RateLimitStatus {
	key := makeKey(provider, credentialID)

	m.mu.RLock()
	limiter, exists := m.limiters[key]
	m.mu.RUnlock()

	if !exists {
		return nil
	}

	status := limiter.GetStatus()
	return &status
}

// GetAllStatuses returns the rate limit status for all tracked limiters.
func (m *Manager) GetAllStatuses() []RateLimitStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	statuses := make([]RateLimitStatus, 0, len(m.limiters))
	for _, limiter := range m.limiters {
		statuses = append(statuses, limiter.GetStatus())
	}
	return statuses
}

// TryConsume attempts to consume from a provider/credential's rate limiter.
// Returns nil if successful, or an error if the limit would be exceeded.
func (m *Manager) TryConsume(provider, credentialID string, config RateLimitConfig, requests, tokens int64) error {
	if config.IsEmpty() {
		return nil
	}

	limiter := m.GetLimiter(provider, credentialID, config)
	if limiter == nil {
		return nil
	}

	return limiter.TryConsume(requests, tokens, config.WaitOnLimit)
}

// RecordUsage records actual usage after a request completes.
func (m *Manager) RecordUsage(provider, credentialID string, config RateLimitConfig, requests, tokens int64) {
	if config.IsEmpty() {
		return
	}

	limiter := m.GetLimiter(provider, credentialID, config)
	if limiter == nil {
		return
	}

	limiter.RecordUsage(requests, tokens)
}

// CleanupStale removes limiters that haven't been used in the specified duration.
func (m *Manager) CleanupStale(maxAge time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().Unix()
	staleThreshold := now - int64(maxAge.Seconds())

	for key, limiter := range m.limiters {
		status := limiter.GetStatus()
		// Remove if both windows are expired and no recent activity
		if status.MinuteWindow.WindowEnd < staleThreshold && status.DayWindow.WindowEnd < staleThreshold {
			delete(m.limiters, key)
		}
	}
}

// MaskCredential masks a credential ID for logging/display purposes.
func MaskCredential(credentialID string) string {
	if len(credentialID) <= 8 {
		return credentialID
	}
	return credentialID[:4] + "..." + credentialID[len(credentialID)-4:]
}

// ParseRateLimitConfigFromMap parses rate limit config from a generic map.
// This is useful for loading from YAML/JSON.
func ParseRateLimitConfigFromMap(m map[string]interface{}) RateLimitConfig {
	var cfg RateLimitConfig

	apply := func(canonical string, value interface{}) {
		parsed, ok := parseIntValue(value)
		if !ok {
			return
		}
		switch canonical {
		case "rpm":
			cfg.RPM = parsed
		case "tpm":
			cfg.TPM = parsed
		case "rpd":
			cfg.RPD = parsed
		case "tpd":
			cfg.TPD = parsed
		}
	}

	for key, value := range m {
		normalized := strings.ToLower(strings.TrimSpace(key))
		switch normalized {
		case "rpm", "requests_per_minute", "requestsperminute":
			apply("rpm", value)
		case "tpm", "tokens_per_minute", "tokensperminute":
			apply("tpm", value)
		case "rpd", "requests_per_day", "requestsperday":
			apply("rpd", value)
		case "tpd", "tokens_per_day", "tokensperday":
			apply("tpd", value)
		}
	}

	if v, ok := m["wait-on-limit"]; ok {
		if val, ok := v.(bool); ok {
			cfg.WaitOnLimit = val
		} else if val, ok := v.(string); ok {
			cfg.WaitOnLimit = strings.ToLower(val) == "true"
		}
	}
	if v, ok := m["max-wait-seconds"]; ok {
		switch val := v.(type) {
		case int:
			cfg.MaxWaitSeconds = val
		case float64:
			cfg.MaxWaitSeconds = int(val)
		}
	}
	return cfg
}

func parseIntValue(v interface{}) (int, bool) {
	switch val := v.(type) {
	case int:
		return val, true
	case int64:
		return int(val), true
	case float64:
		return int(val), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil {
			return 0, false
		}
		return parsed, true
	case json.Number:
		parsed, err := val.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}
