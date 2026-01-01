// Package management provides the management API handlers and middleware
// for configuring the server and managing sessions.
package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "cleaned",
		"removed": count,
	})
}
