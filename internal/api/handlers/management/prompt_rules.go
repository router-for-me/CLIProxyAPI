package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
)

// GetPromptRules returns the current configured prompt rules.
func (h *Handler) GetPromptRules(c *gin.Context) {
	h.mu.Lock()
	out := append([]config.PromptRule(nil), h.cfg.PromptRules...)
	h.mu.Unlock()
	if out == nil {
		out = []config.PromptRule{}
	}
	c.JSON(http.StatusOK, gin.H{"prompt-rules": out})
}

// PutPromptRules replaces the entire prompt-rules list. Validation is strict —
// any invalid rule causes a 400 with the offending rule's name and reason.
// This is the primary write path used by the management UI.
func (h *Handler) PutPromptRules(c *gin.Context) {
	rules, ok := readPromptRulesBody(c)
	if !ok {
		return
	}
	candidate := config.Config{PromptRules: rules}
	candidate.NormalizePromptRules()
	if err := candidate.ValidatePromptRules(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.PromptRules = candidate.PromptRules
	helps.UpdatePromptRulesSnapshot(h.cfg.PromptRules)
	h.persistLocked(c)
}

// PatchPromptRule upserts a single rule. The body shape mirrors other list
// PATCH endpoints in this package:
//
//	{"index": <int>?, "match": "<name>"?, "value": <PromptRule>}
//
// If both index and match are absent (or no match found), the rule is appended.
func (h *Handler) PatchPromptRule(c *gin.Context) {
	var body struct {
		Index *int               `json:"index"`
		Match *string            `json:"match"`
		Value *config.PromptRule `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := append([]config.PromptRule(nil), h.cfg.PromptRules...)
	targetIdx := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(cur) {
		targetIdx = *body.Index
	} else if body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		if match != "" {
			for i := range cur {
				if cur[i].Name == match {
					targetIdx = i
					break
				}
			}
		}
	}
	if targetIdx >= 0 {
		cur[targetIdx] = *body.Value
	} else {
		cur = append(cur, *body.Value)
	}
	candidate := config.Config{PromptRules: cur}
	candidate.NormalizePromptRules()
	if err := candidate.ValidatePromptRules(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.cfg.PromptRules = candidate.PromptRules
	helps.UpdatePromptRulesSnapshot(h.cfg.PromptRules)
	h.persistLocked(c)
}

// DeletePromptRule removes a rule by ?name= or ?index=.
func (h *Handler) DeletePromptRule(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if name := strings.TrimSpace(c.Query("name")); name != "" {
		out := make([]config.PromptRule, 0, len(h.cfg.PromptRules))
		removed := false
		for _, r := range h.cfg.PromptRules {
			if r.Name == name {
				removed = true
				continue
			}
			out = append(out, r)
		}
		if !removed {
			c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
			return
		}
		h.cfg.PromptRules = out
		helps.UpdatePromptRulesSnapshot(h.cfg.PromptRules)
		h.persistLocked(c)
		return
	}
	if idxStr := strings.TrimSpace(c.Query("index")); idxStr != "" {
		var idx int
		if _, err := fmt.Sscanf(idxStr, "%d", &idx); err == nil && idx >= 0 && idx < len(h.cfg.PromptRules) {
			h.cfg.PromptRules = append(h.cfg.PromptRules[:idx], h.cfg.PromptRules[idx+1:]...)
			helps.UpdatePromptRulesSnapshot(h.cfg.PromptRules)
			h.persistLocked(c)
			return
		}
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "missing name or index"})
}

func readPromptRulesBody(c *gin.Context) ([]config.PromptRule, bool) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return nil, false
	}
	if len(data) == 0 {
		// Empty body explicitly clears the list.
		return []config.PromptRule{}, true
	}
	var arr []config.PromptRule
	if err := json.Unmarshal(data, &arr); err == nil {
		return arr, true
	}
	var obj struct {
		Items       []config.PromptRule `json:"items"`
		PromptRules []config.PromptRule `json:"prompt-rules"`
	}
	if err := json.Unmarshal(data, &obj); err == nil {
		if obj.PromptRules != nil {
			return obj.PromptRules, true
		}
		if obj.Items != nil {
			return obj.Items, true
		}
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
	return nil, false
}
