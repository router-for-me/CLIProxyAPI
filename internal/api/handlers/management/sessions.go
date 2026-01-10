// Package management provides the management API handlers and middleware
// for configuring the server and managing sessions.
package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// ListSessions returns all active sessions.
// GET /v0/management/sessions
func (h *Handler) ListSessions(c *gin.Context) {
	if h.sessionManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "session management not enabled"})
		return
	}

	sessions, err := h.sessionManager.List(c.Request.Context())
	if err != nil {
		log.WithError(err).Error("Failed to list sessions")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve sessions"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"sessions": sessions,
		"count":    len(sessions),
	})
}

// GetSession retrieves a specific session by ID.
// GET /v0/management/sessions/:id
func (h *Handler) GetSession(c *gin.Context) {
	if h.sessionManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "session management not enabled"})
		return
	}

	sessionID := c.Param("id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session ID required"})
		return
	}

	session, err := h.sessionManager.Get(c.Request.Context(), sessionID)
	if err != nil {
		log.WithError(err).WithField("session_id", sessionID).Error("Failed to get session")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve session"})
		return
	}

	if session == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}

	c.JSON(http.StatusOK, session)
}

// DeleteSession removes a session by ID.
// DELETE /v0/management/sessions/:id
func (h *Handler) DeleteSession(c *gin.Context) {
	if h.sessionManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "session management not enabled"})
		return
	}

	sessionID := c.Param("id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session ID required"})
		return
	}

	if err := h.sessionManager.Delete(c.Request.Context(), sessionID); err != nil {
		log.WithError(err).WithField("session_id", sessionID).Error("Failed to delete session")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete session"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// CleanupSessions triggers manual cleanup of expired sessions.
// POST /v0/management/sessions/cleanup
func (h *Handler) CleanupSessions(c *gin.Context) {
	if h.sessionManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "session management not enabled"})
		return
	}

	count, err := h.sessionManager.Cleanup(c.Request.Context())
	if err != nil {
		log.WithError(err).Error("Failed to cleanup sessions")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to cleanup sessions"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "cleaned",
		"removed": count,
	})
}
