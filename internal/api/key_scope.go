package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// keyScopeMiddleware applies the parts of client.key-scopes that model
// resolution cannot. It attaches the key's account pools to the request and
// refuses a pooled key on an endpoint whose credential selection has not been
// checked against pools, so new endpoints fail closed. Codex web search and
// voice always use a Codex credential without resolving a model, so a key
// whose scope excludes codex is refused there before a credential is selected.
// Unscoped keys pass through unchanged.
func (s *Server) keyScopeMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.handlers == nil {
			c.Next()
			return
		}
		ctx, errMsg := s.handlers.AccountPoolContext(c.Request.Context(), c)
		if errMsg != nil {
			c.AbortWithStatusJSON(errMsg.StatusCode, gin.H{"error": gin.H{"message": errMsg.Error.Error(), "code": "account_pool_unavailable"}})
			return
		}
		method, path := c.Request.Method, c.FullPath()
		if coreauth.HasAccountPools(ctx) && !accountPoolRouteSupported(method, path) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{"message": "This endpoint does not support account-pool keys", "code": "account_pool_endpoint_unsupported"}})
			return
		}
		if codexOnlyRoute(method, path) && !s.handlers.KeyAllowsProvider(c, "codex") {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{
				"message": "Codex is not available to this API key",
				"type":    "invalid_request_error",
				"param":   nil,
				"code":    "provider_not_allowed",
			}})
			return
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// codexOnlyRoutes always use a Codex credential: Codex web search and voice.
// A Realtime client secret authenticates as the key that minted it, so the
// minting key's scope and pools apply to it.
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
