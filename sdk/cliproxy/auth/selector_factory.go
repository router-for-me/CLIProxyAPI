package auth

import (
	"sync"
	"time"
)

var (
	intelligentSelectorInstance *IntelligentSelector
	intelligentSelectorOnce     sync.Once
)

// GetIntelligentSelector returns the shared intelligent selector instance
func GetIntelligentSelector() *IntelligentSelector {
	intelligentSelectorOnce.Do(func() {
		intelligentSelectorInstance = NewIntelligentSelector()
	})
	return intelligentSelectorInstance
}

// NewSelectorFromConfig creates an appropriate selector based on configuration
func NewSelectorFromConfig(enabled bool) Selector {
	if enabled {
		return GetIntelligentSelector()
	}
	return &RoundRobinSelector{}
}

// StartIntelligentSelectorCleanup starts a background goroutine to clean up old stats
func StartIntelligentSelectorCleanup(selector *IntelligentSelector, retentionHours int, intervalMinutes int) {
	if selector == nil {
		return
	}
	if retentionHours <= 0 {
		retentionHours = 24 // default to 24 hours
	}
	if intervalMinutes <= 0 {
		intervalMinutes = 60 // default to 60 minutes
	}

	retention := time.Duration(retentionHours) * time.Hour
	interval := time.Duration(intervalMinutes) * time.Minute

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			selector.CleanupOldStats(retention)
		}
	}()
}
