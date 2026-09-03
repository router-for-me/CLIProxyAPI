package management

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/cachestats"
)

// cacheStatsResponse is the payload of GET /v0/management/cache-stats.
type cacheStatsResponse struct {
	Enabled bool `json:"enabled"`
	// Provider echoes the applied filter, empty when every provider is included.
	Provider  string                      `json:"provider,omitempty"`
	Global    cachestats.Aggregate        `json:"global"`
	Providers []cachestats.KeyedAggregate `json:"providers"`
	Models    []cachestats.KeyedAggregate `json:"models"`
	Auths     []cachestats.KeyedAggregate `json:"auths"`
	Sessions  []cachestats.SessionSummary `json:"sessions"`
}

// cacheStatsResetResponse is the payload of DELETE /v0/management/cache-stats.
type cacheStatsResetResponse struct {
	Status          string `json:"status"`
	ClearedSessions int    `json:"cleared_sessions"`
}

// GetCacheStats returns the global, per-model and per-auth prompt-cache summary
// together with the retained session list, newest activity first.
func (h *Handler) GetCacheStats(c *gin.Context) {
	provider := strings.TrimSpace(c.Query("provider"))
	snapshot := cachestats.Default().Snapshot(cachestats.Filter{Provider: provider})
	c.JSON(http.StatusOK, cacheStatsResponse{
		Enabled:   snapshot.Enabled,
		Provider:  provider,
		Global:    snapshot.Global,
		Providers: snapshot.Providers,
		Models:    snapshot.Models,
		Auths:     snapshot.Auths,
		Sessions:  snapshot.Sessions,
	})
}

// GetCacheStatsSession returns one session's summary and its retained request
// sequence in order.
func (h *Handler) GetCacheStatsSession(c *gin.Context) {
	// The catch-all param arrives with a leading slash, and the id may itself
	// contain slashes, so only the first one is stripped.
	id := strings.TrimSpace(strings.TrimPrefix(c.Param("id"), "/"))
	if unescaped, errUnescape := url.PathUnescape(id); errUnescape == nil {
		id = unescaped
	}
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
