package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// coverageMiddleware records operations whose handlers have no token reporter.
// Control-plane calls and opaque WebRTC media are explicitly unmeasured; token
// estimates (for example count_tokens) are never recorded as consumed tokens.
func usageCoverageMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		covered := strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/v1beta/") || strings.HasPrefix(path, "/backend-api/codex/") || strings.HasPrefix(path, "/openai/v1/")
		if !covered || strings.HasSuffix(path, "/models") {
			c.Next()
			return
		}
		ctx, scope := usage.WithAccountingScope(c.Request.Context())
		ctx = usage.WithNewGeneration(ctx)
		c.Request = c.Request.WithContext(ctx)
		started := time.Now()
		c.Next()
		if scope.Published() {
			return
		}
		// Do not turn rejected credentials or unknown routes into unexpired
		// journal files. Coverage is for accepted, registered API operations.
		status := c.Writer.Status()
		if c.FullPath() == "" || c.IsAborted() && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
			return
		}
		usage.PublishRecord(context.WithValue(ctx, "gin", c), usage.Record{Provider: "unknown", Model: "unknown", ExecutorType: "EndpointCoverage", Endpoint: c.Request.Method + " " + path, Kind: "unmeasured", Generate: usage.GenerateFlag(false), RequestedAt: started, Latency: time.Since(started), Failed: c.Writer.Status() >= http.StatusBadRequest, Fail: usage.Failure{StatusCode: c.Writer.Status()}})
	}
}
