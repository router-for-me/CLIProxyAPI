package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func (h *Handler) getConfigInternal() *config.Config {
	return h.cfg
}

// GetEffectiveRateLimit returns the effective rate limit for a provider.
// Priority: 1. Runtime override (if set), 2. Config file value, 3. nil
func (h *Handler) GetEffectiveRateLimit(provider string) *config.ProviderRateLimit {
	provider = strings.ToLower(strings.TrimSpace(provider))

	h.selfRateLimitMu.RLock()
	if h.selfRateLimitOverrides != nil {
		if override, ok := h.selfRateLimitOverrides[provider]; ok {
			h.selfRateLimitMu.RUnlock()
			return override // May be nil if explicitly cleared
		}
	}
	h.selfRateLimitMu.RUnlock()

	cfg := h.getConfigInternal()
	if cfg != nil && cfg.SelfRateLimit != nil {
		if limit, ok := cfg.SelfRateLimit[provider]; ok {
			return &limit
		}
	}
	return nil
}

// GetAllSelfRateLimits returns GET /v0/management/self-rate-limit
func (h *Handler) GetAllSelfRateLimits(c *gin.Context) {
	result := make(map[string]config.ProviderRateLimit)

	// Start with config values
	cfg := h.getConfigInternal()
	if cfg != nil && cfg.SelfRateLimit != nil {
		for provider, limit := range cfg.SelfRateLimit {
			result[provider] = limit
		}
	}

	// Apply overrides (replace entire entry, nil means deleted)
	h.selfRateLimitMu.RLock()
	if h.selfRateLimitOverrides != nil {
		for provider, override := range h.selfRateLimitOverrides {
			if override == nil {
				delete(result, provider)
			} else {
				result[provider] = *override
			}
		}
	}
	h.selfRateLimitMu.RUnlock()

	c.JSON(http.StatusOK, result)
}

// GetSelfRateLimit returns GET /v0/management/self-rate-limit/:provider
func (h *Handler) GetSelfRateLimit(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Param("provider")))

	limit := h.GetEffectiveRateLimit(provider)
	if limit == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not configured"})
		return
	}

	c.JSON(http.StatusOK, limit)
}

// PutSelfRateLimit handles PUT /v0/management/self-rate-limit/:provider
func (h *Handler) PutSelfRateLimit(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	if provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider name required"})
		return
	}

	var limit config.ProviderRateLimit
	if err := c.ShouldBindJSON(&limit); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON: " + err.Error()})
		return
	}

	// Validate
	if limit.MinDelayMs < 0 || limit.MaxDelayMs < 0 || limit.ChunkDelayMs < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "delay values must be non-negative"})
		return
	}
	if limit.MinDelayMs > limit.MaxDelayMs {
		c.JSON(http.StatusBadRequest, gin.H{"error": "min-delay-ms must be <= max-delay-ms"})
		return
	}

	h.selfRateLimitMu.Lock()
	if h.selfRateLimitOverrides == nil {
		h.selfRateLimitOverrides = make(map[string]*config.ProviderRateLimit)
	}
	h.selfRateLimitOverrides[provider] = &limit
	h.selfRateLimitMu.Unlock()

	c.JSON(http.StatusOK, limit)
}

// DeleteSelfRateLimit handles DELETE /v0/management/self-rate-limit/:provider
func (h *Handler) DeleteSelfRateLimit(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Param("provider")))

	// Check if provider exists in config or overrides
	h.selfRateLimitMu.RLock()
	hasOverride := h.selfRateLimitOverrides != nil && h.selfRateLimitOverrides[provider] != nil
	h.selfRateLimitMu.RUnlock()

	cfg := h.getConfigInternal()
	hasConfig := cfg != nil && cfg.SelfRateLimit != nil
	if hasConfig {
		_, hasConfig = cfg.SelfRateLimit[provider]
	}

	if !hasOverride && !hasConfig {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not configured"})
		return
	}

	// Set override to nil to clear (overrides config value with "no delay")
	h.selfRateLimitMu.Lock()
	if h.selfRateLimitOverrides == nil {
		h.selfRateLimitOverrides = make(map[string]*config.ProviderRateLimit)
	}
	h.selfRateLimitOverrides[provider] = nil
	h.selfRateLimitMu.Unlock()

	c.AbortWithStatus(http.StatusNoContent)
}
