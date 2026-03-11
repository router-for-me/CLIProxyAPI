package management

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func (h *Handler) GetAPIKeyQuotas(c *gin.Context) {
	c.JSON(200, gin.H{"api-key-quotas": h.cfg.APIKeyQuotas})
}

func (h *Handler) PutAPIKeyQuotas(c *gin.Context) {
	var body struct {
		Value *config.APIKeyQuotaConfig `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	next := *body.Value
	next.ExcludeModelPatterns = normalizeStringList(next.ExcludeModelPatterns)
	next.MonthlyTokenLimits = normalizeAPIKeyMonthlyTokenLimits(next.MonthlyTokenLimits)
	h.cfg.APIKeyQuotas = next
	h.persist(c)
}

func (h *Handler) PatchAPIKeyQuotas(c *gin.Context) {
	var body struct {
		Enabled              *bool                                  `json:"enabled"`
		ExcludeModelPatterns *[]string                              `json:"exclude-model-patterns"`
		MonthlyTokenLimits   *[]config.APIKeyMonthlyModelTokenLimit `json:"monthly-token-limits"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	changed := false
	if body.Enabled != nil {
		h.cfg.APIKeyQuotas.Enabled = *body.Enabled
		changed = true
	}
	if body.ExcludeModelPatterns != nil {
		h.cfg.APIKeyQuotas.ExcludeModelPatterns = normalizeStringList(*body.ExcludeModelPatterns)
		changed = true
	}
	if body.MonthlyTokenLimits != nil {
		h.cfg.APIKeyQuotas.MonthlyTokenLimits = normalizeAPIKeyMonthlyTokenLimits(*body.MonthlyTokenLimits)
		changed = true
	}
	if !changed {
		c.JSON(400, gin.H{"error": "missing fields"})
		return
	}
	h.persist(c)
}

func (h *Handler) GetAPIKeyQuotasEnabled(c *gin.Context) {
	c.JSON(200, gin.H{"enabled": h.cfg.APIKeyQuotas.Enabled})
}

func (h *Handler) PutAPIKeyQuotasEnabled(c *gin.Context) {
	h.updateBoolField(c, func(v bool) { h.cfg.APIKeyQuotas.Enabled = v })
}

func (h *Handler) GetAPIKeyQuotaExcludeModelPatterns(c *gin.Context) {
	c.JSON(200, gin.H{"exclude-model-patterns": h.cfg.APIKeyQuotas.ExcludeModelPatterns})
}

func (h *Handler) PutAPIKeyQuotaExcludeModelPatterns(c *gin.Context) {
	h.putStringList(c, func(v []string) {
		h.cfg.APIKeyQuotas.ExcludeModelPatterns = normalizeStringList(v)
	}, nil)
}

func (h *Handler) PatchAPIKeyQuotaExcludeModelPatterns(c *gin.Context) {
	h.patchStringList(c, &h.cfg.APIKeyQuotas.ExcludeModelPatterns, func() {
		h.cfg.APIKeyQuotas.ExcludeModelPatterns = normalizeStringList(h.cfg.APIKeyQuotas.ExcludeModelPatterns)
	})
}

func (h *Handler) DeleteAPIKeyQuotaExcludeModelPatterns(c *gin.Context) {
	h.deleteFromStringList(c, &h.cfg.APIKeyQuotas.ExcludeModelPatterns, func() {
		h.cfg.APIKeyQuotas.ExcludeModelPatterns = normalizeStringList(h.cfg.APIKeyQuotas.ExcludeModelPatterns)
	})
}

func (h *Handler) GetAPIKeyQuotaMonthlyTokenLimits(c *gin.Context) {
	c.JSON(200, gin.H{"monthly-token-limits": h.cfg.APIKeyQuotas.MonthlyTokenLimits})
}

func (h *Handler) PutAPIKeyQuotaMonthlyTokenLimits(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.APIKeyMonthlyModelTokenLimit
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.APIKeyMonthlyModelTokenLimit `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	h.cfg.APIKeyQuotas.MonthlyTokenLimits = normalizeAPIKeyMonthlyTokenLimits(arr)
	h.persist(c)
}

func (h *Handler) PatchAPIKeyQuotaMonthlyTokenLimits(c *gin.Context) {
	var body struct {
		Index *int                                 `json:"index"`
		Match *config.APIKeyMonthlyModelTokenLimit `json:"match"`
		Value *config.APIKeyMonthlyModelTokenLimit `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.APIKeyQuotas.MonthlyTokenLimits) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		m := normalizeSingleAPIKeyMonthlyTokenLimit(*body.Match)
		for i := range h.cfg.APIKeyQuotas.MonthlyTokenLimits {
			entry := normalizeSingleAPIKeyMonthlyTokenLimit(h.cfg.APIKeyQuotas.MonthlyTokenLimits[i])
			if entry.APIKey == m.APIKey && entry.Model == m.Model {
				targetIndex = i
				break
			}
		}
	}

	normalized := normalizeSingleAPIKeyMonthlyTokenLimit(*body.Value)
	if targetIndex == -1 {
		h.cfg.APIKeyQuotas.MonthlyTokenLimits = append(h.cfg.APIKeyQuotas.MonthlyTokenLimits, normalized)
	} else {
		h.cfg.APIKeyQuotas.MonthlyTokenLimits[targetIndex] = normalized
	}
	h.cfg.APIKeyQuotas.MonthlyTokenLimits = normalizeAPIKeyMonthlyTokenLimits(h.cfg.APIKeyQuotas.MonthlyTokenLimits)
	h.persist(c)
}

func (h *Handler) DeleteAPIKeyQuotaMonthlyTokenLimits(c *gin.Context) {
	if idxStr := strings.TrimSpace(c.Query("index")); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(h.cfg.APIKeyQuotas.MonthlyTokenLimits) {
			h.cfg.APIKeyQuotas.MonthlyTokenLimits = append(h.cfg.APIKeyQuotas.MonthlyTokenLimits[:idx], h.cfg.APIKeyQuotas.MonthlyTokenLimits[idx+1:]...)
			h.persist(c)
			return
		}
	}
	var body struct {
		Value *config.APIKeyMonthlyModelTokenLimit `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "missing index or value"})
		return
	}
	needle := normalizeSingleAPIKeyMonthlyTokenLimit(*body.Value)
	out := make([]config.APIKeyMonthlyModelTokenLimit, 0, len(h.cfg.APIKeyQuotas.MonthlyTokenLimits))
	for _, entry := range h.cfg.APIKeyQuotas.MonthlyTokenLimits {
		n := normalizeSingleAPIKeyMonthlyTokenLimit(entry)
		if n.APIKey == needle.APIKey && n.Model == needle.Model {
			continue
		}
		out = append(out, n)
	}
	h.cfg.APIKeyQuotas.MonthlyTokenLimits = out
	h.persist(c)
}

func normalizeStringList(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func normalizeAPIKeyMonthlyTokenLimits(items []config.APIKeyMonthlyModelTokenLimit) []config.APIKeyMonthlyModelTokenLimit {
	out := make([]config.APIKeyMonthlyModelTokenLimit, 0, len(items))
	for _, item := range items {
		normalized := normalizeSingleAPIKeyMonthlyTokenLimit(item)
		if normalized.Model == "" || normalized.Limit <= 0 {
			continue
		}
		out = append(out, normalized)
	}
	return out
}

func normalizeSingleAPIKeyMonthlyTokenLimit(item config.APIKeyMonthlyModelTokenLimit) config.APIKeyMonthlyModelTokenLimit {
	item.APIKey = strings.TrimSpace(item.APIKey)
	if item.APIKey == "" {
		item.APIKey = "*"
	}
	item.Model = strings.TrimSpace(item.Model)
	return item
}
