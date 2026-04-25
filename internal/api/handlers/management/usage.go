package management

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

const usageFlushTimeout = 5 * time.Second

type usageExportPayload struct {
	Version    int                            `json:"version"`
	ExportedAt time.Time                      `json:"exported_at"`
	Usage      usage.StatisticsSnapshot       `json:"usage"`
	Aggregated *usage.AggregatedUsageSnapshot `json:"aggregated,omitempty"`
}

type usageImportPayload struct {
	Version    int                            `json:"version"`
	Usage      usage.StatisticsSnapshot       `json:"usage"`
	Aggregated *usage.AggregatedUsageSnapshot `json:"aggregated,omitempty"`
}

// GetUsageStatistics returns a lightweight in-memory statistics snapshot for dashboards.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	if err := flushUsageStatistics(c.Request.Context()); err != nil {
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": "usage statistics are still being processed"})
		return
	}
	snapshot := h.summaryUsageSnapshot()
	c.JSON(http.StatusOK, gin.H{
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
	})
}

// GetDetailedUsageStatistics returns the full in-memory usage snapshot with request details.
func (h *Handler) GetDetailedUsageStatistics(c *gin.Context) {
	if err := flushUsageStatistics(c.Request.Context()); err != nil {
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": "usage statistics are still being processed"})
		return
	}
	snapshot := h.detailedUsageSnapshot()
	c.JSON(http.StatusOK, gin.H{
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
	})
}

// GetAggregatedUsageStatistics returns pre-aggregated usage windows for the management usage page.
func (h *Handler) GetAggregatedUsageStatistics(c *gin.Context) {
	if err := flushUsageStatistics(c.Request.Context()); err != nil {
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": "usage statistics are still being processed"})
		return
	}

	snapshot := h.aggregatedUsageSnapshot(time.Now().UTC())
	failedRequests := int64(0)
	if allWindow, ok := snapshot.Windows["all"]; ok {
		failedRequests = allWindow.FailureCount
	}

	c.JSON(http.StatusOK, gin.H{
		"usage":           snapshot,
		"failed_requests": failedRequests,
	})
}

// ExportUsageStatistics returns an aggregated usage export plus a summary snapshot for import compatibility.
func (h *Handler) ExportUsageStatistics(c *gin.Context) {
	if err := flushUsageStatistics(c.Request.Context()); err != nil {
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": "usage statistics are still being processed"})
		return
	}
	now := time.Now().UTC()
	snapshot := h.summaryUsageSnapshot()
	aggregated := h.aggregatedUsageSnapshot(now)
	c.JSON(http.StatusOK, usageExportPayload{
		Version:    3,
		ExportedAt: now,
		Usage:      snapshot,
		Aggregated: &aggregated,
	})
}

// ExportDetailedUsageStatistics returns the full detailed usage snapshot for backup or forensic analysis.
func (h *Handler) ExportDetailedUsageStatistics(c *gin.Context) {
	if err := flushUsageStatistics(c.Request.Context()); err != nil {
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": "usage statistics are still being processed"})
		return
	}
	c.JSON(http.StatusOK, usageExportPayload{
		Version:    3,
		ExportedAt: time.Now().UTC(),
		Usage:      h.detailedUsageSnapshot(),
	})
}

func flushUsageStatistics(parent context.Context) error {
	ctx := parent
	if ctx == nil {
		ctx = context.Background()
	}
	flushCtx, cancel := context.WithTimeout(ctx, usageFlushTimeout)
	defer cancel()
	return coreusage.FlushDefault(flushCtx)
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
	if payload.Version != 0 && payload.Version != 1 && payload.Version != 2 && payload.Version != 3 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported version"})
		return
	}

	result := h.usageStats.MergeSnapshot(payload.Usage)
	if payload.Aggregated != nil {
		h.usageStats.MergeImportedAggregatedSnapshot(*payload.Aggregated)
	}
	snapshot := h.usageStats.SnapshotSummary()
	c.JSON(http.StatusOK, gin.H{
		"added":           result.Added,
		"skipped":         result.Skipped,
		"total_requests":  snapshot.TotalRequests,
		"failed_requests": snapshot.FailureCount,
	})
}

func (h *Handler) summaryUsageSnapshot() usage.StatisticsSnapshot {
	if h == nil || h.usageStats == nil {
		return usage.StatisticsSnapshot{}
	}
	return h.usageStats.SnapshotSummary()
}

func (h *Handler) detailedUsageSnapshot() usage.StatisticsSnapshot {
	if h == nil || h.usageStats == nil {
		return usage.StatisticsSnapshot{}
	}
	return h.usageStats.Snapshot()
}

func (h *Handler) aggregatedUsageSnapshot(now time.Time) usage.AggregatedUsageSnapshot {
	if h == nil || h.usageStats == nil {
		return usage.AggregatedUsageSnapshot{
			GeneratedAt: now.UTC(),
			Windows:     map[string]usage.AggregatedUsageWindow{},
		}
	}
	return h.usageStats.AggregatedUsageSnapshot(now)
}
