package management

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// modelHealthDefaultPageSize caps the page size accepted by the log list endpoint.
const modelHealthDefaultPageSize = 50

// requireModelHealth resolves the PG-backed model health store or aborts with
// 503 when PG is not configured (mirrors requireSyncLog / requirePG).
func (h *Handler) requireModelHealth(c *gin.Context) (*store.ModelHealthStore, bool) {
	if h == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "pg_not_configured", "message": "PostgreSQL store is not configured",
		}})
		return nil, false
	}
	h.mu.Lock()
	s := h.pgModelHealth
	h.mu.Unlock()
	if s == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "pg_not_configured", "message": "model health is not enabled (PostgreSQL store is not configured)",
		}})
		return nil, false
	}
	return s, true
}

// GetModelHealth handles GET /v0/management/model-health.
//
// Returns the latest health-check snapshot for every model id plus the current
// operator settings and the most recent sweep timestamp. Surfaced on the
// dashboard under Analysis → Model Health. Returns 503 when the PG store is not
// configured.
func (h *Handler) GetModelHealth(c *gin.Context) {
	s, ok := h.requireModelHealth(c)
	if !ok {
		return
	}
	snapshots, err := s.ListSnapshots(c.Request.Context())
	if err != nil {
		log.WithError(err).Warn("model-health: list snapshots failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	settings, err := s.GetSettings(c.Request.Context())
	if err != nil {
		// Non-fatal: GetSettings already falls back to defaults on a missing
		// row. Only a hard DB error reaches here; surface it but keep the
		// snapshots so the page still renders.
		log.WithError(err).Warn("model-health: load settings failed")
		settings = store.ModelHealthSettings{Enabled: true, ExcludedModels: []string{}, MaxTokens: 1, IntervalSeconds: 900}
	}
	lastRunAt, lastRunSummary := ModelHealthLastRun()
	c.JSON(http.StatusOK, gin.H{
		"snapshots":        snapshots,
		"settings":         settings,
		"last_run_at":      lastRunAt,
		"last_run_summary": lastRunSummary,
	})
}

// ListModelHealthLog handles GET /v0/management/model-health/log.
//
// Returns a page of model health-check history rows, newest first. Filters:
// model_id, status (operational/degraded/unavailable), success (true/false),
// from/to (RFC3339). Mirrors the { events, total, page, page_size } envelope
// used by the upstream sync log so the dashboard can reuse its table+pager.
func (h *Handler) ListModelHealthLog(c *gin.Context) {
	s, ok := h.requireModelHealth(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))
	if pageSize <= 0 {
		pageSize = modelHealthDefaultPageSize
	}

	f := store.ModelHealthLogFilter{
		ModelID: strings.TrimSpace(c.Query("model_id")),
		Status:  strings.TrimSpace(c.Query("status")),
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

	entries, total, err := s.ListLogPaged(c.Request.Context(), f, page, pageSize)
	if err != nil {
		log.WithError(err).Warn("model-health: list log failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"events":    entries,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// GetModelHealthLogEntry handles GET /v0/management/model-health/log/:id.
//
// Returns a single model health-check history row by ID, with the error_message
// unsealed.
func (h *Handler) GetModelHealthLogEntry(c *gin.Context) {
	s, ok := h.requireModelHealth(c)
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
	e, err := s.GetLogEntry(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrModelHealthLogNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
				"type": "not_found", "message": "model health log entry not found",
			}})
			return
		}
		log.WithError(err).Warn("model-health: get log entry failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, e)
}

// ClearModelHealthLog handles DELETE /v0/management/model-health/log.
//
// Removes all model_health_log history rows (the latest snapshots in
// model_health are preserved). Auto-sweep handles routine retention; this is
// an explicit operator action. Returns the number of rows deleted.
func (h *Handler) ClearModelHealthLog(c *gin.Context) {
	s, ok := h.requireModelHealth(c)
	if !ok {
		return
	}
	deleted, err := s.ClearLog(c.Request.Context())
	if err != nil {
		log.WithError(err).Warn("model-health: clear log failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "deleted": deleted})
}

// ListModelHealthModels handles GET /v0/management/model-health/models.
//
// Returns the distinct model_id values currently present in the model health
// log, ordered alphabetically. Used to populate the dashboard model filter
// dropdown.
func (h *Handler) ListModelHealthModels(c *gin.Context) {
	s, ok := h.requireModelHealth(c)
	if !ok {
		return
	}
	models, err := s.ListLogModels(c.Request.Context())
	if err != nil {
		log.WithError(err).Warn("model-health: distinct models failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"models": models})
}

// GetModelHealthSettings handles GET /v0/management/model-health/settings.
//
// Returns the singleton operator configuration for the model health check
// sweep (enabled, interval_seconds, excluded_models, max_tokens).
func (h *Handler) GetModelHealthSettings(c *gin.Context) {
	s, ok := h.requireModelHealth(c)
	if !ok {
		return
	}
	settings, err := s.GetSettings(c.Request.Context())
	if err != nil {
		log.WithError(err).Warn("model-health: load settings failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": settings})
}

// modelHealthSettingsRequest is the JSON body accepted by PutModelHealthSettings.
// All fields are optional; omitted fields fall back to the existing/defaults.
type modelHealthSettingsRequest struct {
	Enabled         *bool    `json:"enabled,omitempty"`
	IntervalSeconds *int     `json:"interval_seconds,omitempty"`
	ExcludedModels  []string `json:"excluded_models,omitempty"`
	MaxTokens       *int     `json:"max_tokens,omitempty"`
	// RetentionDays bounds the model_health_log age (0 = no time-based
	// pruning; else 1..MaxModelHealthRetentionDays).
	RetentionDays *int `json:"retention_days,omitempty"`
	// MaxLogRows is the per-model history row cap (0 = unlimited; else
	// MinModelHealthMaxLogRows..MaxModelHealthMaxLogRows).
	MaxLogRows *int `json:"max_log_rows,omitempty"`
}

// PutModelHealthSettings handles PUT/PATCH /v0/management/model-health/settings.
//
// Updates the singleton operator configuration for the model health check
// sweep. interval_seconds is rejected below store.MinModelHealthInterval (5 min)
// with a 400; max_tokens is rejected outside [1, store.MaxModelHealthTokens]
// with a 400; retention_days is rejected outside [0, store.MaxModelHealthRetentionDays]
// (0 disables time-based pruning; non-zero below the floor is rejected) with a
// 400; max_log_rows is rejected outside [0, store.MaxModelHealthMaxLogRows] (0
// = unlimited; non-zero below the floor is rejected) with a 400; excluded_models
// is normalized (trimmed, empties dropped). The sweep re-reads settings on its
// next tick so changes take effect without a restart. Returns the persisted
// settings.
func (h *Handler) PutModelHealthSettings(c *gin.Context) {
	s, ok := h.requireModelHealth(c)
	if !ok {
		return
	}
	var body modelHealthSettingsRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "invalid JSON body: " + err.Error(),
		}})
		return
	}
	// Load current settings so a PATCH (partial update) merges on top.
	current, err := s.GetSettings(c.Request.Context())
	if err != nil {
		current = store.ModelHealthSettings{Enabled: true, ExcludedModels: []string{}, MaxTokens: 1, IntervalSeconds: 900}
	}
	if body.Enabled != nil {
		current.Enabled = *body.Enabled
	}
	if body.IntervalSeconds != nil {
		if *body.IntervalSeconds < int(store.MinModelHealthInterval.Seconds()) {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"type":    "invalid_request",
				"message": "interval_seconds must be at least " + strconv.Itoa(int(store.MinModelHealthInterval.Seconds())),
			}})
			return
		}
		current.IntervalSeconds = *body.IntervalSeconds
	}
	if body.ExcludedModels != nil {
		current.ExcludedModels = body.ExcludedModels
	}
	if body.MaxTokens != nil {
		if *body.MaxTokens < 1 || *body.MaxTokens > store.MaxModelHealthTokens {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"type":    "invalid_request",
				"message": "max_tokens must be between 1 and " + strconv.Itoa(store.MaxModelHealthTokens),
			}})
			return
		}
		current.MaxTokens = *body.MaxTokens
	}
	if body.RetentionDays != nil {
		// 0 disables time-based pruning; non-zero must fall in [Min, Max].
		if *body.RetentionDays < 0 ||
			(*body.RetentionDays > 0 && (*body.RetentionDays < store.MinModelHealthRetentionDays || *body.RetentionDays > store.MaxModelHealthRetentionDays)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"type":    "invalid_request",
				"message": "retention_days must be 0 (disabled) or between " + strconv.Itoa(store.MinModelHealthRetentionDays) + " and " + strconv.Itoa(store.MaxModelHealthRetentionDays),
			}})
			return
		}
		current.RetentionDays = *body.RetentionDays
	}
	if body.MaxLogRows != nil {
		// 0 means unlimited; non-zero must fall in [Min, Max].
		if *body.MaxLogRows < 0 ||
			(*body.MaxLogRows > 0 && (*body.MaxLogRows < store.MinModelHealthMaxLogRows || *body.MaxLogRows > store.MaxModelHealthMaxLogRows)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"type":    "invalid_request",
				"message": "max_log_rows must be 0 (unlimited) or between " + strconv.Itoa(store.MinModelHealthMaxLogRows) + " and " + strconv.Itoa(store.MaxModelHealthMaxLogRows),
			}})
			return
		}
		current.MaxLogRows = *body.MaxLogRows
	}
	persisted, err := s.UpsertSettings(c.Request.Context(), current)
	if err != nil {
		log.WithError(err).Warn("model-health: upsert settings failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": persisted})
}

// RunModelHealthCheckNow handles POST /v0/management/model-health/run.
//
// Triggers an immediate model health sweep in the background (the same probe
// path the scheduled sweep uses). Returns 202 Accepted immediately so the
// dashboard does not block on a potentially long probe loop. The handler
// responds with the previous sweep timestamp so the UI can show "running…"
// state; results appear on the next refresh once the sweep completes.
func (h *Handler) RunModelHealthCheckNow(c *gin.Context) {
	if _, ok := h.requireModelHealth(c); !ok {
		return
	}
	prevAt, prevSummary := ModelHealthLastRun()
	go func() {
		// Use a detached context so the probe loop is not cancelled when the
		// HTTP request that triggered it returns (202 returns immediately).
		h.runHealthChecks(context.Background())
	}()
	c.JSON(http.StatusAccepted, gin.H{
		"status":           "accepted",
		"message":          "health check triggered",
		"last_run_at":      prevAt,
		"last_run_summary": prevSummary,
	})
}
