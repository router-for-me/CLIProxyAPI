package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

type deleteUsageRequest struct {
	IDs []string `json:"ids"`
}

// GetUsageStatistics returns persisted request usage grouped by API key and model.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	rng, ok := parseUsageRange(c)
	if !ok {
		return
	}

	store := usage.DefaultStore()
	if store == nil {
		c.JSON(http.StatusOK, usage.APIUsage{})
		return
	}

	result, err := store.Query(c.Request.Context(), rng)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query usage"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// DeleteUsageRecords removes persisted usage records by record ID.
func (h *Handler) DeleteUsageRecords(c *gin.Context) {
	store := usage.DefaultStore()
	if store == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage store unavailable"})
		return
	}

	var body deleteUsageRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	ids := make([]string, 0, len(body.IDs))
	seen := make(map[string]struct{}, len(body.IDs))
	for _, id := range body.IDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		ids = append(ids, trimmed)
	}
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids required"})
		return
	}

	result, err := store.Delete(c.Request.Context(), ids)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete usage records"})
		return
	}
	c.JSON(http.StatusOK, result)
}

func parseUsageRange(c *gin.Context) (usage.QueryRange, bool) {
	var rng usage.QueryRange

	if rawStart := strings.TrimSpace(c.Query("start")); rawStart != "" {
		start, err := time.Parse(time.RFC3339, rawStart)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start"})
			return rng, false
		}
		start = start.UTC()
		rng.Start = &start
	}

	if rawEnd := strings.TrimSpace(c.Query("end")); rawEnd != "" {
		end, err := time.Parse(time.RFC3339, rawEnd)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end"})
			return rng, false
		}
		end = end.UTC()
		rng.End = &end
	}

	return rng, true
}
