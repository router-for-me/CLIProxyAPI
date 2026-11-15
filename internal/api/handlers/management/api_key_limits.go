package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/apikeylimits"
)

type apiKeyWindowUsage struct {
	Tokens   int64     `json:"tokens"`
	Cost     float64   `json:"cost"`
	Requests int64     `json:"requests"`
	ResetAt  time.Time `json:"reset_at"`
}

type apiKeyPolicyUsage struct {
	Key     string                       `json:"key"`
	Expired bool                         `json:"expired"`
	Usage   map[string]apiKeyWindowUsage `json:"usage"`
}

// GetAPIKeyPolicyUsage reports per client key usage in the windows used by
// access.api-key-policies limits (server local time).
func (h *Handler) GetAPIKeyPolicyUsage(c *gin.Context) {
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()
	if cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}
	now := time.Now()
	store := apikeylimits.Default()
	out := make([]apiKeyPolicyUsage, 0, len(cfg.APIKeys))
	for _, key := range cfg.APIKeys {
		if key == "" {
			continue
		}
		entry := apiKeyPolicyUsage{Key: key, Usage: make(map[string]apiKeyWindowUsage, 3)}
		if policy, ok := cfg.FindAPIKeyPolicy(key); ok {
			entry.Expired = policy.ExpiredAt(now)
		}
		for window, counter := range store.Usage(key) {
			entry.Usage[string(window)] = apiKeyWindowUsage{
				Tokens:   counter.Tokens,
				Cost:     counter.Cost,
				Requests: counter.Requests,
				ResetAt:  apikeylimits.WindowEnd(window, now),
			}
		}
		out = append(out, entry)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"keys": out, "timezone": now.Location().String()})
}
