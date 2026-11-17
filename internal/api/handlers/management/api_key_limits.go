package management

import (
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/apikeylimits"
)

type apiKeyModelUsage struct {
	Model           string  `json:"model"`
	Requests        int64   `json:"requests"`
	Failed          int64   `json:"failed"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	Tokens          int64   `json:"tokens"`
	Cost            float64 `json:"cost"`
}

type apiKeyWindowUsage struct {
	Tokens   int64              `json:"tokens"`
	Cost     float64            `json:"cost"`
	Requests int64              `json:"requests"`
	Failed   int64              `json:"failed"`
	ResetAt  *time.Time         `json:"reset_at,omitempty"`
	Models   []apiKeyModelUsage `json:"models"`
}

type apiKeyPolicyUsage struct {
	Key     string                       `json:"key"`
	Name    string                       `json:"name,omitempty"`
	Expired bool                         `json:"expired"`
	Usage   map[string]apiKeyWindowUsage `json:"usage"`
}

// apiKeyModelBreakdown lists a window's models, heaviest token users first.
func apiKeyModelBreakdown(counter apikeylimits.Counter) []apiKeyModelUsage {
	models := make([]apiKeyModelUsage, 0, len(counter.Models))
	for name, m := range counter.Models {
		if m == nil {
			continue
		}
		models = append(models, apiKeyModelUsage{
			Model:           name,
			Requests:        m.Requests,
			Failed:          m.Failed,
			InputTokens:     m.InputTokens,
			OutputTokens:    m.OutputTokens,
			CachedTokens:    m.CachedTokens,
			ReasoningTokens: m.ReasoningTokens,
			Tokens:          m.Tokens,
			Cost:            m.Cost,
		})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Tokens != models[j].Tokens {
			return models[i].Tokens > models[j].Tokens
		}
		if models[i].Requests != models[j].Requests {
			return models[i].Requests > models[j].Requests
		}
		return models[i].Model < models[j].Model
	})
	return models
}

// GetAPIKeyPolicyUsage reports per client key usage in the windows used by
// access.api-key-policies limits (server local time), plus an all-time "total"
// window, each broken down by model.
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
		entry := apiKeyPolicyUsage{Key: key, Usage: make(map[string]apiKeyWindowUsage, 4)}
		if policy, ok := cfg.FindAPIKeyPolicy(key); ok {
			entry.Name = policy.Name
			entry.Expired = policy.ExpiredAt(now)
		}
		for window, counter := range store.Usage(key) {
			usage := apiKeyWindowUsage{
				Tokens:   counter.Tokens,
				Cost:     counter.Cost,
				Requests: counter.Requests,
				Failed:   counter.Failed,
				Models:   apiKeyModelBreakdown(counter),
			}
			if end := apikeylimits.WindowEnd(window, now); !end.IsZero() {
				usage.ResetAt = &end
			}
			entry.Usage[string(window)] = usage
		}
		out = append(out, entry)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"keys": out, "timezone": now.Location().String()})
}
