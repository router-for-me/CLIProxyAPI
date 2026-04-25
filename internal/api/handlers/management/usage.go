package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

type usageExportPayload struct {
	Version    int                      `json:"version"`
	ExportedAt time.Time                `json:"exported_at"`
	Usage      usage.StatisticsSnapshot `json:"usage"`
}

type usageImportPayload struct {
	Version int                      `json:"version"`
	Usage   usage.StatisticsSnapshot `json:"usage"`
}

type requestEventsStreamResetPayload struct {
	Reason string `json:"reason"`
}

// GetUsageStatistics returns the in-memory request statistics snapshot.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		snapshot = h.usageStats.Snapshot()
	}
	c.JSON(http.StatusOK, gin.H{
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
	})
}

// ExportUsageStatistics returns a complete usage snapshot for backup/migration.
func (h *Handler) ExportUsageStatistics(c *gin.Context) {
	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		snapshot = h.usageStats.Snapshot()
	}
	c.JSON(http.StatusOK, usageExportPayload{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Usage:      snapshot,
	})
}

// ImportUsageStatistics merges a previously exported usage snapshot into memory.
func (h *Handler) ImportUsageStatistics(c *gin.Context) {
	if h == nil || h.usageStats == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage statistics unavailable"})
		return
	}

	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	var payload usageImportPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	if payload.Version != 0 && payload.Version != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported version"})
		return
	}

	result := h.usageStats.MergeSnapshot(payload.Usage)
	snapshot := h.usageStats.Snapshot()
	c.JSON(http.StatusOK, gin.H{
		"added":           result.Added,
		"skipped":         result.Skipped,
		"total_requests":  snapshot.TotalRequests,
		"failed_requests": snapshot.FailureCount,
	})
}

// ListUsageRequestEvents returns a flattened request-event snapshot for the management UI.
func (h *Handler) ListUsageRequestEvents(c *gin.Context) {
	page := usage.BuildRequestEventPage(h.usageStats, h.requestEventHub, usage.RequestEventQuery{
		TimeRange: c.DefaultQuery("time_range", "24h"),
		Limit:     parseRequestEventLimit(c.Query("limit")),
	}, time.Now().UTC())
	c.JSON(http.StatusOK, page)
}

// StreamUsageRequestEvents streams newly recorded request events as SSE.
func (h *Handler) StreamUsageRequestEvents(c *gin.Context) {
	if h == nil || h.requestEventHub == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "request event stream unavailable"})
		return
	}

	sinceID, err := parseRequestEventID(c.Query("since_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid since_id"})
		return
	}

	sub, backlog, resetRequired := h.requestEventHub.Subscribe(sinceID)
	if resetRequired {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		writeSSEFrame(c.Writer, "reset-required", requestEventsStreamResetPayload{Reason: "cursor_too_old"})
		if flusher, ok := c.Writer.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	if sub == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "failed to subscribe request event stream"})
		return
	}
	defer h.requestEventHub.Unsubscribe(sub)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming unsupported"})
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	for _, event := range backlog {
		if err := writeSSEFrame(c.Writer, "request-event", event); err != nil {
			return
		}
	}
	flusher.Flush()

	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-heartbeatTicker.C:
			if sub.Overflowed() {
				_ = writeSSEFrame(c.Writer, "reset-required", requestEventsStreamResetPayload{Reason: "buffer_overflow"})
				flusher.Flush()
				return
			}
			if _, err := fmt.Fprint(c.Writer, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event, ok := <-sub.C():
			if !ok {
				return
			}
			if err := writeSSEFrame(c.Writer, "request-event", event); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func parseRequestEventLimit(raw string) int {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}

func parseRequestEventID(raw string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	return strconv.ParseUint(raw, 10, 64)
}

func writeSSEFrame(w http.ResponseWriter, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	return nil
}
