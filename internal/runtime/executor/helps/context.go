package helps

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// HeaderFromContext extracts the HTTP request headers from a gin context stored
// in ctx under the "gin" key. Returns nil when no gin context is present or when
// the request is nil, allowing callers to treat nil as an empty header map.
func HeaderFromContext(ctx context.Context) http.Header {
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil || ginCtx.Request == nil {
		return nil
	}
	return ginCtx.Request.Header
}
