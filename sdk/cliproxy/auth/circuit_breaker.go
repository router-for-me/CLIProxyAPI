package auth

import (
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Default circuit breaker cooldown durations.
const (
	DefaultHard403CooldownSeconds = 600  // 10 minutes
	DefaultSoft403CooldownSeconds = 1800 // 30 minutes
)

// circuitBreakerConfig holds runtime circuit breaker configuration.
type circuitBreakerConfig struct {
	mu                     sync.RWMutex
	enabled                bool
	hard403CooldownSeconds int
	soft403CooldownSeconds int
	hard403Retry           int
}

var globalCircuitBreakerConfig = &circuitBreakerConfig{
	enabled:                true,
	hard403CooldownSeconds: DefaultHard403CooldownSeconds,
	soft403CooldownSeconds: DefaultSoft403CooldownSeconds,
	hard403Retry:           0,
}

// SetCircuitBreakerConfig updates the global circuit breaker configuration.
func SetCircuitBreakerConfig(enabled bool, hard403CooldownSecs, soft403CooldownSecs, hard403Retry int) {
	if hard403CooldownSecs <= 0 {
		hard403CooldownSecs = DefaultHard403CooldownSeconds
	}
	if soft403CooldownSecs <= 0 {
		soft403CooldownSecs = DefaultSoft403CooldownSeconds
	}
	if hard403Retry < 0 {
		hard403Retry = 0
	}
	globalCircuitBreakerConfig.mu.Lock()
	globalCircuitBreakerConfig.enabled = enabled
	globalCircuitBreakerConfig.hard403CooldownSeconds = hard403CooldownSecs
	globalCircuitBreakerConfig.soft403CooldownSeconds = soft403CooldownSecs
	globalCircuitBreakerConfig.hard403Retry = hard403Retry
	globalCircuitBreakerConfig.mu.Unlock()
}

// ClassifyHard403 analyzes an error to determine if it's a hard 403 that should trigger circuit breaker.
// It parses the error message/code looking for CONSUMER_INVALID, SERVICE_DISABLED, or PERMISSION_DENIED.
func ClassifyHard403(err *Error) Hard403Type {
	if err == nil {
		return Hard403None
	}

	if err.HTTPStatus != 403 {
		return Hard403None
	}

	msg := strings.ToUpper(err.Message)
	code := strings.ToUpper(err.Code)

	if strings.Contains(msg, "CONSUMER_INVALID") || strings.Contains(code, "CONSUMER_INVALID") {
		return Hard403ConsumerInvalid
	}

	if strings.Contains(msg, "SERVICE_DISABLED") || strings.Contains(code, "SERVICE_DISABLED") ||
		strings.Contains(msg, "HAS NOT BEEN USED IN PROJECT") ||
		strings.Contains(msg, "IT IS DISABLED") {
		return Hard403ServiceDisabled
	}

	if strings.Contains(msg, "PERMISSION_DENIED") || strings.Contains(code, "PERMISSION_DENIED") ||
		strings.Contains(msg, "PERMISSION DENIED ON RESOURCE PROJECT") {
		return Hard403PermissionDenied
	}

	return Hard403None
}

// IsHard403 returns true if the error is classified as a hard 403.
func IsHard403(err *Error) bool {
	return ClassifyHard403(err) != Hard403None
}

// OpenCircuitBreaker opens the circuit breaker for an auth credential.
func OpenCircuitBreaker(auth *Auth, reason Hard403Type, now time.Time) {
	if auth == nil || reason == Hard403None {
		return
	}

	globalCircuitBreakerConfig.mu.RLock()
	enabled := globalCircuitBreakerConfig.enabled
	cooldownSecs := globalCircuitBreakerConfig.hard403CooldownSeconds
	globalCircuitBreakerConfig.mu.RUnlock()

	if !enabled {
		return
	}

	cooldownDuration := time.Duration(cooldownSecs) * time.Second

	auth.CircuitBreaker.Open = true
	auth.CircuitBreaker.Reason = reason
	auth.CircuitBreaker.CooldownUntil = now.Add(cooldownDuration)
	auth.CircuitBreaker.OpenedAt = now
	auth.CircuitBreaker.FailureCount++

	log.WithFields(log.Fields{
		"auth_id":        auth.ID,
		"provider":       auth.Provider,
		"reason":         string(reason),
		"cooldown_until": auth.CircuitBreaker.CooldownUntil.Format(time.RFC3339),
		"failure_count":  auth.CircuitBreaker.FailureCount,
	}).Warn("circuit breaker opened for hard 403")
}

// CloseCircuitBreaker closes the circuit breaker for an auth credential.
func CloseCircuitBreaker(auth *Auth) {
	if auth == nil {
		return
	}

	if auth.CircuitBreaker.Open {
		log.WithFields(log.Fields{
			"auth_id":  auth.ID,
			"provider": auth.Provider,
			"reason":   string(auth.CircuitBreaker.Reason),
		}).Info("circuit breaker closed")
	}

	auth.CircuitBreaker.Open = false
	auth.CircuitBreaker.Reason = Hard403None
	auth.CircuitBreaker.CooldownUntil = time.Time{}
	auth.CircuitBreaker.FailureCount = 0
}

// IsCircuitBreakerOpen returns true if the circuit breaker is open and cooldown has not expired.
// Note: This function only reads auth state and does NOT auto-close expired circuit breakers.
// Use CheckAndCloseExpiredCircuitBreaker for auto-close behavior when holding a write lock.
func IsCircuitBreakerOpen(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}

	globalCircuitBreakerConfig.mu.RLock()
	enabled := globalCircuitBreakerConfig.enabled
	globalCircuitBreakerConfig.mu.RUnlock()

	if !enabled {
		return false
	}

	if !auth.CircuitBreaker.Open {
		return false
	}

	if now.After(auth.CircuitBreaker.CooldownUntil) {
		return false
	}

	return true
}

// CheckAndCloseExpiredCircuitBreaker checks if the circuit breaker cooldown expired and closes it.
// Returns true if the circuit breaker is still open, false if closed or expired.
// Call this only when holding a write lock on the auth.
func CheckAndCloseExpiredCircuitBreaker(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}

	globalCircuitBreakerConfig.mu.RLock()
	enabled := globalCircuitBreakerConfig.enabled
	globalCircuitBreakerConfig.mu.RUnlock()

	if !enabled {
		return false
	}

	if !auth.CircuitBreaker.Open {
		return false
	}

	if now.After(auth.CircuitBreaker.CooldownUntil) {
		CloseCircuitBreaker(auth)
		return false
	}

	return true
}

// ShouldRetryHard403 returns true if hard 403 retries are allowed.
func ShouldRetryHard403() bool {
	globalCircuitBreakerConfig.mu.RLock()
	retry := globalCircuitBreakerConfig.hard403Retry
	globalCircuitBreakerConfig.mu.RUnlock()
	return retry > 0
}

// GetHard403MaxRetries returns the maximum number of retries for hard 403 errors.
func GetHard403MaxRetries() int {
	globalCircuitBreakerConfig.mu.RLock()
	retry := globalCircuitBreakerConfig.hard403Retry
	globalCircuitBreakerConfig.mu.RUnlock()
	return retry
}

// GetSoft403Cooldown returns the cooldown duration for soft 403 errors.
func GetSoft403Cooldown() time.Duration {
	globalCircuitBreakerConfig.mu.RLock()
	secs := globalCircuitBreakerConfig.soft403CooldownSeconds
	globalCircuitBreakerConfig.mu.RUnlock()
	return time.Duration(secs) * time.Second
}

// GetHard403Cooldown returns the cooldown duration for hard 403 errors.
func GetHard403Cooldown() time.Duration {
	globalCircuitBreakerConfig.mu.RLock()
	secs := globalCircuitBreakerConfig.hard403CooldownSeconds
	globalCircuitBreakerConfig.mu.RUnlock()
	return time.Duration(secs) * time.Second
}
