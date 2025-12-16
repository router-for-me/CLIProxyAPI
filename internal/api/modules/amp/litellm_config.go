// litellm_config.go - Thread-safe LiteLLM configuration with hot-reload support.
// This file is part of our fork-specific features and should never conflict with upstream.
// See FORK_MAINTENANCE.md for architecture details.
package amp

import (
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// LiteLLMConfig holds runtime LiteLLM configuration with thread-safe access
type LiteLLMConfig struct {
	mu              sync.RWMutex
	enabled         bool
	passthroughMode bool
	fallbackEnabled bool
	models          map[string]bool
	baseURL         string
	apiKey          string
	cfg             *config.Config // Store reference for path rewriting
}

// NewLiteLLMConfig creates a new LiteLLM configuration from app config
func NewLiteLLMConfig(cfg *config.Config) *LiteLLMConfig {
	lc := &LiteLLMConfig{
		models: make(map[string]bool),
	}
	lc.Update(cfg)
	return lc
}

// Update refreshes the configuration (hot-reload support)
func (lc *LiteLLMConfig) Update(cfg *config.Config) {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	lc.cfg = cfg
	lc.baseURL = strings.TrimSpace(cfg.LiteLLMBaseURL)
	lc.apiKey = cfg.LiteLLMAPIKey
	lc.enabled = cfg.LiteLLMHybridMode && lc.baseURL != ""
	lc.passthroughMode = cfg.LiteLLMPassthroughMode
	lc.fallbackEnabled = cfg.LiteLLMFallbackEnabled && lc.baseURL != ""

	// Rebuild models map
	lc.models = make(map[string]bool)
	for _, model := range cfg.LiteLLMModels {
		lc.models[strings.ToLower(strings.TrimSpace(model))] = true
	}
}

// IsEnabled returns whether LiteLLM routing is enabled
func (lc *LiteLLMConfig) IsEnabled() bool {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.enabled
}

// IsPassthroughMode returns whether all traffic should go to LiteLLM
func (lc *LiteLLMConfig) IsPassthroughMode() bool {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.passthroughMode
}

// IsFallbackEnabled returns whether fallback to LiteLLM on quota errors is enabled
func (lc *LiteLLMConfig) IsFallbackEnabled() bool {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.fallbackEnabled
}

// GetBaseURL returns the LiteLLM base URL
func (lc *LiteLLMConfig) GetBaseURL() string {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.baseURL
}

// GetAPIKey returns the LiteLLM API key
func (lc *LiteLLMConfig) GetAPIKey() string {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.apiKey
}

// ShouldRouteToLiteLLM checks if a model should be routed to LiteLLM
func (lc *LiteLLMConfig) ShouldRouteToLiteLLM(model string) bool {
	lc.mu.RLock()
	defer lc.mu.RUnlock()

	if !lc.enabled {
		return false
	}

	// Passthrough mode routes everything to LiteLLM
	if lc.passthroughMode {
		return true
	}

	// Check explicit model list
	return lc.models[strings.ToLower(strings.TrimSpace(model))]
}

// GetModelCount returns the number of models configured for LiteLLM routing
func (lc *LiteLLMConfig) GetModelCount() int {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return len(lc.models)
}

// GetConfig returns the underlying config for path rewriting
func (lc *LiteLLMConfig) GetConfig() *config.Config {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.cfg
}
