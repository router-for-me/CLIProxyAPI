package management

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// requireAlerts resolves the PG-backed alert store or aborts with 503 when PG
// is not configured (mirrors requireModelHealth / requirePG).
func (h *Handler) requireAlerts(c *gin.Context) (*store.AlertStore, bool) {
	if h == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "pg_not_configured", "message": "PostgreSQL store is not configured",
		}})
		return nil, false
	}
	h.mu.Lock()
	s := h.pgAlerts
	h.mu.Unlock()
	if s == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "pg_not_configured", "message": "alerts are not enabled (PostgreSQL store is not configured)",
		}})
		return nil, false
	}
	return s, true
}

// ListAlerts handles GET /v0/management/alerts.
//
// Returns a page of the alerts feed, newest first. Filters: alert_type,
// severity, dismissed (true/false), read (true/false), from/to (RFC3339).
// Mirrors the { alerts, total, page, page_size } envelope used by the model
// health log so the dashboard can reuse its table+pager.
func (h *Handler) ListAlerts(c *gin.Context) {
	s, ok := h.requireAlerts(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "25"))
	if pageSize <= 0 {
		pageSize = 25
	}

	f := store.AlertFilter{
		AlertType: strings.TrimSpace(c.Query("alert_type")),
		Severity:  strings.TrimSpace(c.Query("severity")),
	}
	if v := strings.TrimSpace(c.Query("dismissed")); v != "" {
		switch strings.ToLower(v) {
		case "true", "1":
			b := true
			f.Dismissed = &b
		case "false", "0":
			b := false
			f.Dismissed = &b
		}
	}
	if v := strings.TrimSpace(c.Query("read")); v != "" {
		switch strings.ToLower(v) {
		case "true", "1":
			b := true
			f.Read = &b
		case "false", "0":
			b := false
			f.Read = &b
		}
	}
	if from, err := time.Parse(time.RFC3339, strings.TrimSpace(c.Query("from"))); err == nil {
		f.From = from
	}
	if to, err := time.Parse(time.RFC3339, strings.TrimSpace(c.Query("to"))); err == nil {
		f.To = to
	}

	alerts, total, err := s.ListPaged(c.Request.Context(), f, page, pageSize)
	if err != nil {
		log.WithError(err).Warn("alerts: list failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"alerts":    alerts,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// ListActiveAlerts handles GET /v0/management/alerts/active.
//
// Returns every non-dismissed alert whose suppression window has not yet
// elapsed (the live "currently firing" feed). Newest first.
func (h *Handler) ListActiveAlerts(c *gin.Context) {
	s, ok := h.requireAlerts(c)
	if !ok {
		return
	}
	alerts, err := s.ListActive(c.Request.Context())
	if err != nil {
		log.WithError(err).Warn("alerts: list active failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"alerts": alerts})
}

// GetUnreadAlertCount handles GET /v0/management/alerts/unread-count.
//
// Returns the count of non-dismissed, unread alerts so the dashboard can render
// a badge on the sidebar bell. A cheap COUNT(*); polled periodically.
func (h *Handler) GetUnreadAlertCount(c *gin.Context) {
	s, ok := h.requireAlerts(c)
	if !ok {
		return
	}
	n, err := s.CountUnread(c.Request.Context())
	if err != nil {
		log.WithError(err).Warn("alerts: count unread failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"unread": n})
}

// MarkAlertRead handles POST /v0/management/alerts/:id/read.
func (h *Handler) MarkAlertRead(c *gin.Context) {
	s, ok := h.requireAlerts(c)
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
	affected, err := s.MarkRead(c.Request.Context(), id)
	if err != nil {
		log.WithError(err).Warn("alerts: mark read failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "affected": affected})
}

// MarkAllAlertsRead handles POST /v0/management/alerts/read-all.
func (h *Handler) MarkAllAlertsRead(c *gin.Context) {
	s, ok := h.requireAlerts(c)
	if !ok {
		return
	}
	affected, err := s.MarkAllRead(c.Request.Context())
	if err != nil {
		log.WithError(err).Warn("alerts: mark all read failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "affected": affected})
}

// DismissAlert handles POST /v0/management/alerts/:id/dismiss.
func (h *Handler) DismissAlert(c *gin.Context) {
	s, ok := h.requireAlerts(c)
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
	affected, err := s.Dismiss(c.Request.Context(), id)
	if err != nil {
		log.WithError(err).Warn("alerts: dismiss failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "affected": affected})
}

// ClearAlerts handles DELETE /v0/management/alerts.
//
// Removes every alert row (history + feed). Returns the number of rows deleted.
func (h *Handler) ClearAlerts(c *gin.Context) {
	s, ok := h.requireAlerts(c)
	if !ok {
		return
	}
	deleted, err := s.ClearAll(c.Request.Context())
	if err != nil {
		log.WithError(err).Warn("alerts: clear failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "deleted": deleted})
}

// GetAlertSettings handles GET /v0/management/alerts/settings.
//
// Returns the singleton operator configuration for the alert sweep (enabled,
// interval_seconds, suppression_minutes, per-category toggles, thresholds).
func (h *Handler) GetAlertSettings(c *gin.Context) {
	s, ok := h.requireAlerts(c)
	if !ok {
		return
	}
	settings, err := s.GetAlertSettings(c.Request.Context())
	if err != nil {
		log.WithError(err).Warn("alerts: load settings failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": settings})
}

// alertSettingsRequest is the JSON body accepted by PutAlertSettings. All
// fields are optional; omitted fields fall back to the existing/defaults.
type alertSettingsRequest struct {
	Enabled                *bool    `json:"enabled,omitempty"`
	IntervalSeconds        *int     `json:"interval_seconds,omitempty"`
	SuppressionMinutes     *int     `json:"suppression_minutes,omitempty"`
	EnableUserBudget       *bool    `json:"enable_user_budget,omitempty"`
	EnableAPIKeyBudget     *bool    `json:"enable_api_key_budget,omitempty"`
	EnableErrorRate        *bool    `json:"enable_error_rate,omitempty"`
	EnableProviderCooldown *bool    `json:"enable_provider_cooldown,omitempty"`
	ErrorRateThreshold     *float64 `json:"error_rate_threshold,omitempty"`
	ErrorWindowMinutes     *int     `json:"error_window_minutes,omitempty"`
}

// PutAlertSettings handles PUT/PATCH /v0/management/alerts/settings.
//
// Updates the singleton operator configuration for the alert sweep. A PATCH
// merges partial updates on top of the current settings. The sweep re-reads
// settings on its next tick so changes take effect without a restart. Returns
// the persisted settings.
func (h *Handler) PutAlertSettings(c *gin.Context) {
	s, ok := h.requireAlerts(c)
	if !ok {
		return
	}
	var body alertSettingsRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "invalid JSON body: " + err.Error(),
		}})
		return
	}
	current, err := s.GetAlertSettings(c.Request.Context())
	if err != nil {
		current = store.AlertSettings{
			Enabled: true, IntervalSeconds: 60, SuppressionMinutes: 60,
			EnableUserBudget: true, EnableAPIKeyBudget: true, EnableErrorRate: true,
			EnableProviderCooldown: true,
			ErrorRateThreshold:     0.5, ErrorWindowMinutes: 5,
		}
	}
	if body.Enabled != nil {
		current.Enabled = *body.Enabled
	}
	if body.IntervalSeconds != nil {
		current.IntervalSeconds = *body.IntervalSeconds
	}
	if body.SuppressionMinutes != nil {
		current.SuppressionMinutes = *body.SuppressionMinutes
	}
	if body.EnableUserBudget != nil {
		current.EnableUserBudget = *body.EnableUserBudget
	}
	if body.EnableAPIKeyBudget != nil {
		current.EnableAPIKeyBudget = *body.EnableAPIKeyBudget
	}
	if body.EnableErrorRate != nil {
		current.EnableErrorRate = *body.EnableErrorRate
	}
	if body.EnableProviderCooldown != nil {
		current.EnableProviderCooldown = *body.EnableProviderCooldown
	}
	if body.ErrorRateThreshold != nil {
		current.ErrorRateThreshold = *body.ErrorRateThreshold
	}
	if body.ErrorWindowMinutes != nil {
		current.ErrorWindowMinutes = *body.ErrorWindowMinutes
	}
	persisted, err := s.UpsertAlertSettings(c.Request.Context(), current)
	if err != nil {
		log.WithError(err).Warn("alerts: upsert settings failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type": "internal_error", "message": err.Error(),
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": persisted})
}
