package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

type deleteUsageRequest struct {
	IDs []string `json:"ids"`
}

type usageQueueRecord []byte

func (r usageQueueRecord) MarshalJSON() ([]byte, error) {
	if json.Valid(r) {
		return append([]byte(nil), r...), nil
	}
	return json.Marshal(string(r))
}

// GetUsageStatistics returns persisted request usage grouped by API key and model.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	rng, ok := parseUsageRange(c)
	if !ok {
		return
	}

	store := h.currentUsageStore()
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
	store := h.currentUsageStore()
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

// GetUsageQueue pops queued usage records from the usage queue.
func (h *Handler) GetUsageQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	count, errCount := parseUsageQueueCount(c.Query("count"))
	if errCount != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCount.Error()})
		return
	}

	items := redisqueue.PopOldest(count)
	records := make([]usageQueueRecord, 0, len(items))
	for _, item := range items {
		records = append(records, usageQueueRecord(append([]byte(nil), item...)))
	}

	c.JSON(http.StatusOK, records)
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

func parseUsageQueueCount(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 1, nil
	}
	count, errCount := strconv.Atoi(value)
	if errCount != nil || count <= 0 {
		return 0, errors.New("count must be a positive integer")
	}
	return count, nil
}
