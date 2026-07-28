package management

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// syncLogPageSize caps the page size accepted by the list endpoint.
const syncLogDefaultPageSize = 50

// requireSyncLog resolves the PG-backed sync log store or aborts with 503 when
// PG is not configured (mirrors requireMgmtTokens / requirePG).
func (h *Handler) requireSyncLog(c *gin.Context) (*store.SyncLogStore, bool) {
	if h == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "pg_not_configured", "message": "PostgreSQL store is not configured",
		}})
		return nil, false
	}
	h.mu.Lock()
	s := h.pgSyncLog
	h.mu.Unlock()
	if s == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "pg_not_configured", "message": "upstream sync log is not enabled (PostgreSQL store is not configured)",
		}})
		return nil, false
	}
	return s, true
}

// ListSyncLog handles GET /v0/management/upstream-sync-log.
//
// Returns a page of upstream OAuth/auth token refresh outcomes recorded by the
// auth manager's RefreshSink, newest first. Filters: provider, trigger
// (auto/on_demand/unauthorized_retry), success (true/false), and from/to
// (RFC3339). Mirrors the { events, total, page, page_size } envelope used by
// the Usage Stats events endpoint so the dashboard can reuse its table+pager.
func (h *Handler) ListSyncLog(c *gin.Context) {
	s, ok := h.requireSyncLog(c)
	if !ok {
		return
	}

	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))
	if pageSize <= 0 {
		pageSize = syncLogDefaultPageSize
	}

	f := store.SyncLogFilter{
		Provider: strings.TrimSpace(c.Query("provider")),
		Trigger:  strings.TrimSpace(c.Query("trigger")),
	}
	if v := strings.TrimSpace(c.Query("success")); v != "" {
		switch strings.ToLower(v) {
		case "true", "1":
			b := true
			f.Success = &b
		case "false", "0":
			b := false
			f.Success = &b
		}
	}
	if from, err := time.Parse(time.RFC3339, strings.TrimSpace(c.Query("from"))); err == nil {
		f.From = from
	}
	if to, err := time.Parse(time.RFC3339, strings.TrimSpace(c.Query("to"))); err == nil {
		f.To = to
	}

	events, total, err := s.ListSyncLogPaged(c.Request.Context(), f, page, pageSize)
	if err != nil {
		log.WithError(err).Warn("upstream-sync-log: list failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"events":    events,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// GetSyncLog handles GET /v0/management/upstream-sync-log/:id.
//
// Returns a single refresh outcome by ID, with the error_message unsealed.
func (h *Handler) GetSyncLog(c *gin.Context) {
	s, ok := h.requireSyncLog(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "id must be a positive integer",
		}})
		return
	}
	e, err := s.GetSyncLog(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrSyncLogNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
				"type": "not_found", "message": "sync log entry not found",
			}})
			return
		}
		log.WithError(err).Warn("upstream-sync-log: get failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, e)
}

// ClearSyncLog handles DELETE /v0/management/upstream-sync-log.
//
// Removes all sync-log rows. Auto-sweep (30-day retention) handles routine
// pruning; this is an explicit operator action. Returns the number of rows
// deleted.
func (h *Handler) ClearSyncLog(c *gin.Context) {
	s, ok := h.requireSyncLog(c)
	if !ok {
		return
	}
	deleted, err := s.ClearSyncLog(c.Request.Context())
	if err != nil {
		log.WithError(err).Warn("upstream-sync-log: clear failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "deleted": deleted})
}

// ListSyncLogProviders handles GET /v0/management/upstream-sync-log/providers.
//
// Returns the distinct provider values currently present in the sync log, used
// to populate the dashboard provider filter dropdown.
func (h *Handler) ListSyncLogProviders(c *gin.Context) {
	s, ok := h.requireSyncLog(c)
	if !ok {
		return
	}
	providers, err := s.SyncLogDistinctProviders(c.Request.Context())
	if err != nil {
		log.WithError(err).Warn("upstream-sync-log: distinct providers failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"providers": providers})
}
