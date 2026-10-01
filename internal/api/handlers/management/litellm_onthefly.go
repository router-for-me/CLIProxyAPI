// Package management: on-the-fly LiteLLM API key validation log.
//
//	GET    /litellm/onthefly-log   — newest-first list (limit/outcome/key_prefix)
//	DELETE /litellm/onthefly-log   — purge all rows
package management

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// requireLiteLLMOnTheFly returns the on-the-fly log store or writes a 503.
func (h *Handler) requireLiteLLMOnTheFly(c *gin.Context) (*store.OnTheFlyLogStore, bool) {
	if h == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	h.mu.Lock()
	s := h.litellmOnTheFly
	h.mu.Unlock()
	if s == nil {
		h.pgNotConfigured(c)
		return nil, false
	}
	return s, true
}

// ListLiteLLMOnTheFlyLog handles GET /v0/management/litellm/onthefly-log.
func (h *Handler) ListLiteLLMOnTheFlyLog(c *gin.Context) {
	s, ok := h.requireLiteLLMOnTheFly(c)
	if !ok {
		return
	}
	limit := 100
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	filter := store.OnTheFlyLogFilter{
		Outcome:   strings.TrimSpace(c.Query("outcome")),
		KeyPrefix: strings.TrimSpace(c.Query("key_prefix")),
	}
	entries, err := s.ListOnTheFlyLog(c.Request.Context(), filter, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries})
}

// ClearLiteLLMOnTheFlyLog handles DELETE /v0/management/litellm/onthefly-log.
func (h *Handler) ClearLiteLLMOnTheFlyLog(c *gin.Context) {
	s, ok := h.requireLiteLLMOnTheFly(c)
	if !ok {
		return
	}
	deleted, err := s.PurgeOnTheFlyLog(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
}
