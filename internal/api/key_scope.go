package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// keyScopeMiddleware applies client.key-scopes to the routes that choose a
// credential without resolving a model. Codex web search and voice always use a
// Codex credential, so a key whose scope excludes codex is refused there before
// a credential is selected. Requests that name a model are scoped when the
// model is resolved, and every other route passes through unchanged.
func (s *Server) keyScopeMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if codexOnlyRoute(c.Request.Method, c.FullPath()) && s.handlers != nil && !s.handlers.KeyAllowsProvider(c, "codex") {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{
				"message": "Codex is not available to this API key",
				"type":    "invalid_request_error",
				"param":   nil,
				"code":    "provider_not_allowed",
			}})
			return
		}
		c.Next()
	}
}

// codexOnlyRoutes always use a Codex credential: Codex web search and voice.
// A Realtime client secret authenticates as the key that minted it, so the
// minting key's scope applies to it.
var codexOnlyRoutes = map[string]bool{
	"POST /v1/alpha/search":                         true,
	"POST /backend-api/codex/alpha/search":          true,
	"POST /v1/live":                                 true,
	"GET /v1/live/:call_id":                         true,
	"GET /v1/realtime":                              true,
	"POST /v1/realtime":                             true,
	"POST /v1/realtime/calls":                       true,
	"GET /v1/realtime/calls/:call_id":               true,
	"POST /v1/realtime/calls/:call_id/hangup":       true,
	"POST /v1/realtime/calls/:call_id/accept":       true,
	"POST /v1/realtime/calls/:call_id/reject":       true,
	"POST /v1/realtime/calls/:call_id/refer":        true,
	"POST /v1/realtime/client_secrets":              true,
	"POST /v1/realtime/sessions":                    true,
	"POST /v1/realtime/transcription_sessions":      true,
	"GET /v1/realtime/translations":                 true,
	"POST /v1/realtime/translations":                true,
	"POST /v1/realtime/translations/client_secrets": true,
}

func codexOnlyRoute(method, path string) bool { return codexOnlyRoutes[method+" "+path] }
