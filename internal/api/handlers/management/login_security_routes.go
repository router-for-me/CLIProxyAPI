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

// requireLoginSecurity resolves the PG-backed management-login store or aborts
// with 503 when PG is not configured (mirrors requireAlerts).
func (h *Handler) requireLoginSecurity(c *gin.Context) (loginSecurityStore, bool) {
	if h == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "pg_not_configured", "message": "PostgreSQL store is not configured",
		}})
		return nil, false
	}
	h.mu.Lock()
	s := h.pgLogin
	h.mu.Unlock()
	if s == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "pg_not_configured", "message": "management login security is not enabled (PostgreSQL store is not configured)",
		}})
		return nil, false
	}
	return s, true
}

// GetLoginSecuritySettings handles GET /v0/management/management-login/settings.
func (h *Handler) GetLoginSecuritySettings(c *gin.Context) {
	s, ok := h.requireLoginSecurity(c)
	if !ok {
		return
	}
	set, err := s.GetLoginSettings(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "db_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": set})
}

// loginSecuritySettingsRequest is the all-pointer PATCH body accepted by
// PutLoginSecuritySettings. Only non-nil fields are applied.
type loginSecuritySettingsRequest struct {
	Enabled                *bool `json:"enabled"`
	MaxFailedAttempts      *int  `json:"max_failed_attempts"`
	BanDurationSeconds     *int  `json:"ban_duration_seconds"`
	FailureWindowSeconds   *int  `json:"failure_window_seconds"`
	CleanupIntervalSeconds *int  `json:"cleanup_interval_seconds"`
	IdleTimeoutSeconds     *int  `json:"idle_timeout_seconds"`
	LogSuccesses           *bool `json:"log_successes"`
	RetentionDays          *int  `json:"retention_days"`
}

// PutLoginSecuritySettings handles PUT/PATCH
// /v0/management/management-login/settings. Applies only the provided fields,
// clamps server-side, persists, and refreshes the in-process cache.
func (h *Handler) PutLoginSecuritySettings(c *gin.Context) {
	s, ok := h.requireLoginSecurity(c)
	if !ok {
		return
	}
	var body loginSecuritySettingsRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	current, err := s.GetLoginSettings(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "db_error", "message": err.Error()}})
		return
	}
	if body.Enabled != nil {
		current.Enabled = *body.Enabled
	}
	if body.MaxFailedAttempts != nil {
		current.MaxFailedAttempts = *body.MaxFailedAttempts
	}
	if body.BanDurationSeconds != nil {
		current.BanDurationSeconds = *body.BanDurationSeconds
	}
	if body.FailureWindowSeconds != nil {
		current.FailureWindowSeconds = *body.FailureWindowSeconds
	}
	if body.CleanupIntervalSeconds != nil {
		current.CleanupIntervalSeconds = *body.CleanupIntervalSeconds
	}
	if body.IdleTimeoutSeconds != nil {
		current.IdleTimeoutSeconds = *body.IdleTimeoutSeconds
	}
	if body.LogSuccesses != nil {
		current.LogSuccesses = *body.LogSuccesses
	}
	if body.RetentionDays != nil {
		current.RetentionDays = *body.RetentionDays
	}

	saved, err := s.UpsertLoginSettings(c.Request.Context(), current)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "db_error", "message": err.Error()}})
		return
	}
	h.setLoginSettingsCache(saved)
	c.JSON(http.StatusOK, gin.H{"settings": saved})
}

// ListLoginSecurityEvents handles GET /v0/management/management-login/events.
func (h *Handler) ListLoginSecurityEvents(c *gin.Context) {
	s, ok := h.requireLoginSecurity(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	f := store.LoginEventFilter{
		IP:      strings.TrimSpace(c.Query("ip")),
		Outcome: strings.TrimSpace(c.Query("outcome")),
	}
	if v := strings.TrimSpace(c.Query("from")); v != "" {
		if ts, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = ts
		}
	}
	if v := strings.TrimSpace(c.Query("to")); v != "" {
		if ts, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = ts
		}
	}
	events, total, err := s.ListLoginEvents(c.Request.Context(), f, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "db_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"events":    events,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// ClearLoginSecurityEvents handles DELETE /v0/management/management-login/events.
func (h *Handler) ClearLoginSecurityEvents(c *gin.Context) {
	s, ok := h.requireLoginSecurity(c)
	if !ok {
		return
	}
	deleted, err := s.ClearLoginEvents(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "db_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
}

// loginBan is the API projection of one in-memory ban/attempt entry.
type loginBan struct {
	IP           string    `json:"ip"`
	BlockedUntil time.Time `json:"blocked_until"`
	Count        int       `json:"count"`
	LastActivity time.Time `json:"last_activity"`
}

// ListLoginSecurityBans handles GET /v0/management/management-login/bans.
// Returns a snapshot of the in-memory ban/attempt map.
func (h *Handler) ListLoginSecurityBans(c *gin.Context) {
	if _, ok := h.requireLoginSecurity(c); !ok {
		return
	}
	now := time.Now()
	h.attemptsMu.Lock()
	bans := make([]loginBan, 0, len(h.failedAttempts))
	for ip, ai := range h.failedAttempts {
		if ai.blockedUntil.IsZero() || !now.Before(ai.blockedUntil) {
			continue
		}
		bans = append(bans, loginBan{
			IP: ip, BlockedUntil: ai.blockedUntil, Count: ai.count, LastActivity: ai.lastActivity,
		})
	}
	h.attemptsMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"bans": bans})
}

// DeleteLoginSecurityBan handles DELETE
// /v0/management/management-login/bans/:ip.
func (h *Handler) DeleteLoginSecurityBan(c *gin.Context) {
	if _, ok := h.requireLoginSecurity(c); !ok {
		return
	}
	ip := strings.TrimSpace(c.Param("ip"))
	if ip == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "ip is required"}})
		return
	}
	h.attemptsMu.Lock()
	_, existed := h.failedAttempts[ip]
	delete(h.failedAttempts, ip)
	h.attemptsMu.Unlock()
	log.WithField("ip", ip).Info("management login: ban lifted by operator")
	c.JSON(http.StatusOK, gin.H{"removed": existed})
}

// ClearLoginSecurityBans handles DELETE /v0/management/management-login/bans.
func (h *Handler) ClearLoginSecurityBans(c *gin.Context) {
	if _, ok := h.requireLoginSecurity(c); !ok {
		return
	}
	h.attemptsMu.Lock()
	n := len(h.failedAttempts)
	h.failedAttempts = make(map[string]*attemptInfo)
	h.attemptsMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"removed": n})
}
