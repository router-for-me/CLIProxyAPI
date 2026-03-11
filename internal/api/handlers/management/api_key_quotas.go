package management

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func (h *Handler) GetAPIKeyQuotas(c *gin.Context) {
	c.JSON(200, gin.H{"api-key-quotas": h.cfg.APIKeyQuotas.Snapshot()})
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
	h.cfg.APIKeyQuotas.Update(func(q *config.APIKeyQuotaConfig) {
		q.Enabled = next.Enabled
		q.ExcludeModelPatterns = next.ExcludeModelPatterns
		q.MonthlyTokenLimits = next.MonthlyTokenLimits
	})
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
		h.cfg.APIKeyQuotas.Update(func(q *config.APIKeyQuotaConfig) { q.Enabled = *body.Enabled })
		changed = true
	}
	if body.ExcludeModelPatterns != nil {
		normalized := normalizeStringList(*body.ExcludeModelPatterns)
		h.cfg.APIKeyQuotas.Update(func(q *config.APIKeyQuotaConfig) { q.ExcludeModelPatterns = normalized })
		changed = true
	}
	if body.MonthlyTokenLimits != nil {
		normalized := normalizeAPIKeyMonthlyTokenLimits(*body.MonthlyTokenLimits)
		h.cfg.APIKeyQuotas.Update(func(q *config.APIKeyQuotaConfig) { q.MonthlyTokenLimits = normalized })
		changed = true
	}
	if !changed {
		c.JSON(400, gin.H{"error": "missing fields"})
		return
	}
	h.persist(c)
}

func (h *Handler) GetAPIKeyQuotasEnabled(c *gin.Context) {
	snapshot := h.cfg.APIKeyQuotas.Snapshot()
	c.JSON(200, gin.H{"enabled": snapshot.Enabled})
}

func (h *Handler) PutAPIKeyQuotasEnabled(c *gin.Context) {
	h.updateBoolField(c, func(v bool) { h.cfg.APIKeyQuotas.Update(func(q *config.APIKeyQuotaConfig) { q.Enabled = v }) })
}

func (h *Handler) GetAPIKeyQuotaExcludeModelPatterns(c *gin.Context) {
	snapshot := h.cfg.APIKeyQuotas.Snapshot()
	c.JSON(200, gin.H{"exclude-model-patterns": snapshot.ExcludeModelPatterns})
}

func (h *Handler) PutAPIKeyQuotaExcludeModelPatterns(c *gin.Context) {
	h.putStringList(c, func(v []string) {
		normalized := normalizeStringList(v)
		h.cfg.APIKeyQuotas.Update(func(q *config.APIKeyQuotaConfig) { q.ExcludeModelPatterns = normalized })
	}, nil)
}

func (h *Handler) PatchAPIKeyQuotaExcludeModelPatterns(c *gin.Context) {
	current := h.cfg.APIKeyQuotas.Snapshot().ExcludeModelPatterns
	h.patchStringList(c, &current, func() {
		normalized := normalizeStringList(current)
		h.cfg.APIKeyQuotas.Update(func(q *config.APIKeyQuotaConfig) { q.ExcludeModelPatterns = normalized })
	})
}

func (h *Handler) DeleteAPIKeyQuotaExcludeModelPatterns(c *gin.Context) {
	current := h.cfg.APIKeyQuotas.Snapshot().ExcludeModelPatterns
	h.deleteFromStringList(c, &current, func() {
		normalized := normalizeStringList(current)
		h.cfg.APIKeyQuotas.Update(func(q *config.APIKeyQuotaConfig) { q.ExcludeModelPatterns = normalized })
	})
}

func (h *Handler) GetAPIKeyQuotaMonthlyTokenLimits(c *gin.Context) {
	snapshot := h.cfg.APIKeyQuotas.Snapshot()
	c.JSON(200, gin.H{"monthly-token-limits": snapshot.MonthlyTokenLimits})
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
	normalized := normalizeAPIKeyMonthlyTokenLimits(arr)
	h.cfg.APIKeyQuotas.Update(func(q *config.APIKeyQuotaConfig) { q.MonthlyTokenLimits = normalized })
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

	monthlyTokenLimits := h.cfg.APIKeyQuotas.Snapshot().MonthlyTokenLimits
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(monthlyTokenLimits) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		m := normalizeSingleAPIKeyMonthlyTokenLimit(*body.Match)
		for i := range monthlyTokenLimits {
			entry := normalizeSingleAPIKeyMonthlyTokenLimit(monthlyTokenLimits[i])
			if entry.APIKey == m.APIKey && entry.Model == m.Model {
				targetIndex = i
				break
			}
		}
	}

	normalized := normalizeSingleAPIKeyMonthlyTokenLimit(*body.Value)
	if targetIndex == -1 {
		monthlyTokenLimits = append(monthlyTokenLimits, normalized)
	} else {
		monthlyTokenLimits[targetIndex] = normalized
	}
	monthlyTokenLimits = normalizeAPIKeyMonthlyTokenLimits(monthlyTokenLimits)
	h.cfg.APIKeyQuotas.Update(func(q *config.APIKeyQuotaConfig) { q.MonthlyTokenLimits = monthlyTokenLimits })
	h.persist(c)
}

func (h *Handler) DeleteAPIKeyQuotaMonthlyTokenLimits(c *gin.Context) {
	monthlyTokenLimits := h.cfg.APIKeyQuotas.Snapshot().MonthlyTokenLimits
	if idxStr := strings.TrimSpace(c.Query("index")); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(monthlyTokenLimits) {
			monthlyTokenLimits = append(monthlyTokenLimits[:idx], monthlyTokenLimits[idx+1:]...)
			h.cfg.APIKeyQuotas.Update(func(q *config.APIKeyQuotaConfig) { q.MonthlyTokenLimits = monthlyTokenLimits })
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
	out := make([]config.APIKeyMonthlyModelTokenLimit, 0, len(monthlyTokenLimits))
	for _, entry := range monthlyTokenLimits {
		n := normalizeSingleAPIKeyMonthlyTokenLimit(entry)
		if n.APIKey == needle.APIKey && n.Model == needle.Model {
			continue
		}
		out = append(out, n)
	}
	h.cfg.APIKeyQuotas.Update(func(q *config.APIKeyQuotaConfig) { q.MonthlyTokenLimits = out })
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
