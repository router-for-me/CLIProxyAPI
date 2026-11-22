package handlers

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

// proxxEnsureDeferredAPICapture attaches a request-scoped DeferredAPICapture to
// ctx when none is present yet. Without one, the executor logging helpers fall
// back to creating the capture lazily on the pooled *gin.Context. Executor
// stream goroutines can outlive the handler (a Responses stream returns on
// response.completed while the upstream body is still being drained), and by
// then gin may have reset that context for another request, which is a data
// race. Creating the capture here, on the handler goroutine, and storing it on
// the gin context too keeps the request-log finalizer reading the same capture.
func proxxEnsureDeferredAPICapture(c *gin.Context, ctx context.Context) context.Context {
	if c == nil || ctx == nil {
		return ctx
	}
	if logging.DeferredAPICaptureFromContext(ctx) != nil || logging.DeferredAPIRequestCaptureDisabled(ctx) {
		return ctx
	}
	var capture *logging.DeferredAPICapture
	if value, exists := c.Get(logging.DeferredAPIResponseContextKey); exists {
		capture, _ = value.(*logging.DeferredAPICapture)
	}
	if capture == nil {
		capture = logging.NewDeferredAPICapture()
		c.Set(logging.DeferredAPIResponseContextKey, capture)
	}
	return logging.WithDeferredAPICapture(ctx, capture)
}
