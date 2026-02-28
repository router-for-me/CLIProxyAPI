package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// GetModelFallback returns the full model-fallback configuration.
func (h *Handler) GetModelFallback(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(200, gin.H{"model-fallback": config.ModelFallback{}})
		return
	}
	c.JSON(200, gin.H{"model-fallback": h.cfg.ModelFallback})
}

// PutModelFallback replaces the entire model-fallback configuration.
func (h *Handler) PutModelFallback(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "configuration not loaded"})
		return
	}
	var body struct {
		Value *config.ModelFallback `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: expected {\"value\": {...}}"})
		return
	}
	h.cfg.ModelFallback = *body.Value
	h.cfg.SanitizeModelFallback()
	h.persist(c)
}

// GetModelFallbackEnabled returns whether model fallback is enabled.
func (h *Handler) GetModelFallbackEnabled(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(200, gin.H{"enabled": false})
		return
	}
	c.JSON(200, gin.H{"enabled": h.cfg.ModelFallback.Enabled})
}

// PutModelFallbackEnabled toggles model fallback on/off.
func (h *Handler) PutModelFallbackEnabled(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "configuration not loaded"})
		return
	}
	h.updateBoolField(c, func(v bool) {
		h.cfg.ModelFallback.Enabled = v
		h.cfg.SanitizeModelFallback()
	})
}

// GetModelFallbackRules returns the model fallback rules.
func (h *Handler) GetModelFallbackRules(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(200, gin.H{"rules": []config.ModelFallbackRule{}})
		return
	}
	rules := h.cfg.ModelFallback.Rules
	if rules == nil {
		rules = []config.ModelFallbackRule{}
	}
	c.JSON(200, gin.H{"rules": rules})
}

// PutModelFallbackRules replaces all model fallback rules.
func (h *Handler) PutModelFallbackRules(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "configuration not loaded"})
		return
	}
	var body struct {
		Value []config.ModelFallbackRule `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.cfg.ModelFallback.Rules = body.Value
	h.cfg.SanitizeModelFallback()
	h.persist(c)
}

// PatchModelFallbackRules adds or updates a single fallback rule.
func (h *Handler) PatchModelFallbackRules(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "configuration not loaded"})
		return
	}
	var body config.ModelFallbackRule
	if err := c.ShouldBindJSON(&body); err != nil || body.From == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: expected {\"from\": \"...\", \"to\": [...]}"})
		return
	}
	// Update existing rule or append new one
	found := false
	for i, rule := range h.cfg.ModelFallback.Rules {
		if strings.EqualFold(rule.From, body.From) {
			h.cfg.ModelFallback.Rules[i] = body
			found = true
			break
		}
	}
	if !found {
		h.cfg.ModelFallback.Rules = append(h.cfg.ModelFallback.Rules, body)
	}
	h.cfg.SanitizeModelFallback()
	h.persist(c)
}

// DeleteModelFallbackRules removes a fallback rule by "from" model name.
func (h *Handler) DeleteModelFallbackRules(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "configuration not loaded"})
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: expected {\"value\": \"model-name\"}"})
		return
	}
	filtered := make([]config.ModelFallbackRule, 0, len(h.cfg.ModelFallback.Rules))
	for _, rule := range h.cfg.ModelFallback.Rules {
		if !strings.EqualFold(rule.From, body.Value) {
			filtered = append(filtered, rule)
		}
	}
	h.cfg.ModelFallback.Rules = filtered
	h.persist(c)
}
