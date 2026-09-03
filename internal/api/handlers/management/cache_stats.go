package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/cachestats"
)

// cacheStatsResponse is the payload of GET /v0/management/cache-stats.
type cacheStatsResponse struct {
	Enabled  bool                        `json:"enabled"`
	Global   cachestats.Aggregate        `json:"global"`
	Models   []cachestats.KeyedAggregate `json:"models"`
	Auths    []cachestats.KeyedAggregate `json:"auths"`
	Sessions []cachestats.SessionSummary `json:"sessions"`
}

// cacheStatsResetResponse is the payload of DELETE /v0/management/cache-stats.
type cacheStatsResetResponse struct {
	Status          string `json:"status"`
	ClearedSessions int    `json:"cleared_sessions"`
}

// GetCacheStats returns the global, per-model and per-auth prompt-cache summary
// together with the retained session list, newest activity first.
func (h *Handler) GetCacheStats(c *gin.Context) {
	store := cachestats.Default()
	response := cacheStatsResponse{
		Enabled:  store.Enabled(),
		Global:   store.Global(),
		Models:   store.ByModel(),
		Auths:    store.ByAuth(),
		Sessions: store.Sessions(),
	}
	if response.Models == nil {
		response.Models = []cachestats.KeyedAggregate{}
	}
	if response.Auths == nil {
		response.Auths = []cachestats.KeyedAggregate{}
	}
	if response.Sessions == nil {
		response.Sessions = []cachestats.SessionSummary{}
	}
	c.JSON(http.StatusOK, response)
}

// GetCacheStatsSession returns one session's summary and its retained request
// sequence in order.
func (h *Handler) GetCacheStatsSession(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session id is required"})
		return
	}
	detail, ok := cachestats.Default().Session(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if detail.Requests == nil {
		detail.Requests = []cachestats.Request{}
	}
	c.JSON(http.StatusOK, detail)
}

// DeleteCacheStats drops every retained session.
func (h *Handler) DeleteCacheStats(c *gin.Context) {
	cleared := cachestats.Default().Reset()
	c.JSON(http.StatusOK, cacheStatsResetResponse{Status: "ok", ClearedSessions: cleared})
}
