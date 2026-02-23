package kiro

import (
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

var (
	globalRateLimiter     *RateLimiter
	globalRateLimiterOnce sync.Once

	globalCooldownManager     *CooldownManager
	globalCooldownManagerOnce sync.Once
	cooldownStopCh            chan struct{}
)

// GetGlobalRateLimiter returns the singleton RateLimiter instance.
func GetGlobalRateLimiter() *RateLimiter {
	globalRateLimiterOnce.Do(func() {
		globalRateLimiter = NewRateLimiter()
		log.Info("kiro: global RateLimiter initialized")
	})
	return globalRateLimiter
}

// GetGlobalCooldownManager returns the singleton CooldownManager instance.
func GetGlobalCooldownManager() *CooldownManager {
	globalCooldownManagerOnce.Do(func() {
		globalCooldownManager = NewCooldownManager()
		cooldownStopCh = make(chan struct{})
		go globalCooldownManager.StartCleanupRoutine(5*time.Minute, cooldownStopCh)
		log.Info("kiro: global CooldownManager initialized with cleanup routine")
	})
	return globalCooldownManager
}

// ShutdownRateLimiters stops the cooldown cleanup routine.
// Should be called during application shutdown.
func ShutdownRateLimiters() {
	if cooldownStopCh != nil {
		close(cooldownStopCh)
		log.Info("kiro: rate limiter cleanup routine stopped")
	}
}
