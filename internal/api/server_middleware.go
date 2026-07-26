package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/safemode"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	log "github.com/sirupsen/logrus"
)

var corsExposedResponseHeaders = []string{
	logging.CPATraceIDHeader,
	"X-CPA-VERSION",
	"X-CPA-COMMIT",
	"X-CPA-BUILD-DATE",
	"X-CPA-SUPPORT-PLUGIN",
	"X-CPA-HOME-VERSION",
	"X-CPA-HOME-BUILD-DATE",
	"X-SERVER-VERSION",
	"X-SERVER-BUILD-DATE",
}

var corsExposedResponseHeadersJoined = strings.Join(corsExposedResponseHeaders, ", ")

const (
	exampleAPIKeyManagementPath = "/management.html"
	exampleAPIKeyManagementURL  = "/management.html?safe-mode=configure"
)

func (s *Server) homeHeartbeatMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil || s.cfg == nil || !s.cfg.Home.Enabled {
			c.Next()
			return
		}
		if c != nil && c.Request != nil {
			path := c.Request.URL.Path
			if strings.HasPrefix(path, "/v0/management/") || path == "/v0/management" || strings.HasPrefix(path, "/v0/resource/plugins/") || path == "/management.html" {
				c.Next()
				return
			}
		}
		client := home.Current()
		if client == nil || !client.HeartbeatOK() {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		c.Next()
	}
}

func (s *Server) exampleAPIKeySafeModeRequired(cfg *config.Config) bool {
	return s != nil && s.exampleAPIKeySafeModeEnabled && cfg != nil && safemode.HasExampleAPIKeys(cfg.APIKeys)
}

func (s *Server) exampleAPIKeySafeModeMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil || !s.exampleAPIKeySafeModeActive.Load() || c == nil || c.Request == nil || c.Request.URL == nil {
			c.Next()
			return
		}

		path := c.Request.URL.Path
		if path == exampleAPIKeyManagementPath && c.Query("safe-mode") == "configure" {
			c.Next()
			return
		}
		if (path == "/" || path == exampleAPIKeyManagementPath) && (c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead) {
			s.serveExampleAPIKeyWarningPage(c)
			return
		}
		if !isExampleAPIKeySafeModeProxyPath(path) {
			c.Next()
			return
		}

		c.Header("X-CPA-SAFE-MODE", "example-api-key")
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "unsafe_example_api_key",
			"message": "Proxy API endpoints are disabled because api-keys contains template values. Open /management.html?safe-mode=configure, update api-keys in Management, then retry.",
		})
	}
}

func (s *Server) serveExampleAPIKeyWarningPage(c *gin.Context) {
	cfg := s.cfg
	var keys []string
	if cfg != nil {
		keys = safemode.ExampleAPIKeys(cfg.APIKeys)
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		c.Abort()
		return
	}
	c.String(http.StatusOK, safemode.ExampleAPIKeyWarningPageHTML(keys, exampleAPIKeyManagementURL))
	c.Abort()
}

func isExampleAPIKeySafeModeProxyPath(path string) bool {
	switch {
	case path == "/v1" || strings.HasPrefix(path, "/v1/"):
		return true
	case path == "/v1beta" || strings.HasPrefix(path, "/v1beta/"):
		return true
	case path == "/openai/v1" || strings.HasPrefix(path, "/openai/v1/"):
		return true
	case path == "/backend-api/codex" || strings.HasPrefix(path, "/backend-api/codex/"):
		return true
	default:
		return false
	}
}

// corsMiddleware returns a Gin middleware handler that adds CORS headers
// to every response, allowing cross-origin requests.
//
// Returns:
//   - gin.HandlerFunc: The CORS middleware handler
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "*")
		c.Header("Access-Control-Expose-Headers", corsExposedResponseHeadersJoined)

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// AuthMiddleware returns a Gin middleware handler that authenticates requests
// using the configured authentication providers. When no providers are available,
// it allows all requests (legacy behaviour).
func AuthMiddleware(manager *sdkaccess.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if manager == nil {
			c.Next()
			return
		}

		result, err := manager.Authenticate(c.Request.Context(), c.Request)
		if err == nil {
			if result != nil {
				c.Set("userApiKey", result.Principal)
				c.Set("accessProvider", result.Provider)
				if len(result.Metadata) > 0 {
					c.Set("accessMetadata", result.Metadata)
				}
			}
			c.Next()
			return
		}

		statusCode := err.HTTPStatusCode()
		if statusCode >= http.StatusInternalServerError {
			log.Errorf("authentication middleware error: %v", err)
		}
		c.AbortWithStatusJSON(statusCode, gin.H{"error": err.Message})
	}
}
