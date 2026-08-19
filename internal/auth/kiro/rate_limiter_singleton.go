package kiro

import (
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

var (
	globalRateLimiter     *RateLimiter
	globalRateLimiterOnce sync.Once
	globalRateLimiterCfg  *RateLimiterConfig

	globalCooldownManager     *CooldownManager
	globalCooldownManagerOnce sync.Once
	cooldownStopCh            chan struct{}
)

// SetGlobalRateLimiterConfig sets the configuration for the global rate limiter.
// If the singleton already exists, it is updated in place.
func SetGlobalRateLimiterConfig(cfg *RateLimiterConfig) {
	globalRateLimiterCfg = cfg
	if globalRateLimiter == nil {
		return
	}

	if cfg != nil {
		globalRateLimiter.ApplyConfig(*cfg)
	} else {
		globalRateLimiter.ApplyConfig(RateLimiterConfig{})
	}

	status := "enabled"
	if !globalRateLimiter.enabled {
		status = "disabled"
	}
	source := "defaults"
	if cfg != nil {
		source = "custom config"
	}
	log.Infof("kiro: global RateLimiter reconfigured (%s) with %s", status, source)
}

// GetGlobalRateLimiter returns the singleton RateLimiter instance.
func GetGlobalRateLimiter() *RateLimiter {
	globalRateLimiterOnce.Do(func() {
		if globalRateLimiterCfg != nil {
			globalRateLimiter = NewRateLimiterWithConfig(*globalRateLimiterCfg)
		} else {
			globalRateLimiter = NewRateLimiter()
		}
		status := "enabled"
		if !globalRateLimiter.enabled {
			status = "disabled"
		}
		source := "defaults"
		if globalRateLimiterCfg != nil {
			source = "custom config"
		}
		log.Infof("kiro: global RateLimiter initialized (%s) with %s", status, source)
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
