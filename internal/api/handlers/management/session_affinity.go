package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coresession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
)

// sessionAffinityInvalidator is implemented by selectors that can release the
// affinity bindings of a single session. It is declared here rather than
// referencing the concrete selector so that pools configured with a plain
// round-robin selector simply report the capability as unavailable.
type sessionAffinityInvalidator interface {
	InvalidateSession(sessionID string) coreauth.SessionInvalidation
}

// DeleteSessionAffinity releases the affinity bindings of one session so its
// next request is re-selected from the pool.
//
// Operators otherwise have only two ways to move a single caller onto another
// credential: give the caller a new session id, which throws away the upstream
// prompt cache for that conversation, or restart the proxy, which throws away
// every binding for every caller. This endpoint is the narrow option.
//
// The response is 200 whether or not a binding existed. A caller cannot tell an
// unknown session from one whose TTL just lapsed, and the request is a pure
// removal, so making the no-op case an error would only break retries and
// fire-and-forget scripts. The "removed" field carries that distinction.
func (h *Handler) DeleteSessionAffinity(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "core auth manager unavailable"})
		return
	}

	sessionID := coresession.NormalizeExplicitID(c.Query("session_id"))
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "missing or invalid session_id"})
		return
	}

	invalidator, ok := h.authManager.Selector().(sessionAffinityInvalidator)
	if !ok || invalidator == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "session affinity is not enabled"})
		return
	}

	result := invalidator.InvalidateSession(sessionID)
	c.JSON(http.StatusOK, gin.H{
		"status":        "ok",
		"session_id":    sessionID,
		"removed":       result.Removed(),
		"cache_groups":  result.CacheGroups,
		"cache_aliases": result.CacheAliases,
		"lcp_groups":    result.LCPGroups,
	})
}
