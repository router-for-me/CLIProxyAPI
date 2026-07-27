package handlers

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/context"
)

type pinnedAuthContextKey struct{}

type selectedAuthCallbackContextKey struct{}

type preparedModelRouteContextKey struct{}

type executionSessionContextKey struct{}

type disallowFreeAuthContextKey struct{}

// WithPinnedAuthID returns a child context that requests execution on a specific auth ID.
func WithPinnedAuthID(ctx context.Context, authID string) context.Context {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, pinnedAuthContextKey{}, authID)
}

// WithSelectedAuthIDCallback returns a child context that receives the selected auth ID.
func WithSelectedAuthIDCallback(ctx context.Context, callback func(string)) context.Context {
	if callback == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, selectedAuthCallbackContextKey{}, callback)
}

// PrepareStreamModelRoute resolves a stream route once and stores it on the returned context for execution.
// The boolean reports whether the route overrides normal model-to-provider resolution.
func (h *BaseAPIHandler) PrepareStreamModelRoute(ctx context.Context, handlerType string, modelName string, rawJSON []byte) (context.Context, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	decision := h.applyModelRouter(ctx, handlerType, modelName, rawJSON, true, modelExecutionOptions{})
	ctx = context.WithValue(ctx, preparedModelRouteContextKey{}, decision)
	hasOverride := strings.TrimSpace(decision.ExecutorPluginID) != "" || strings.TrimSpace(decision.Provider) != ""
	return ctx, hasOverride
}

func preparedModelRouteFromContext(ctx context.Context) (modelRouteDecision, bool) {
	if ctx == nil {
		return modelRouteDecision{}, false
	}
	decision, ok := ctx.Value(preparedModelRouteContextKey{}).(modelRouteDecision)
	return decision, ok
}

// WithExecutionSessionID returns a child context tagged with a long-lived execution session ID.
func WithExecutionSessionID(ctx context.Context, sessionID string) context.Context {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, executionSessionContextKey{}, sessionID)
}

// WithDisallowFreeAuth returns a child context that requests skipping known free-tier credentials.
func WithDisallowFreeAuth(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, disallowFreeAuthContextKey{}, true)
}

// headersFromContext extracts the original HTTP request headers from the gin context
// embedded in the provided context. This allows session affinity selectors to read
// client-provided session headers.
func headersFromContext(ctx context.Context) http.Header {
	if ctx == nil {
		return nil
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		return ginCtx.Request.Header.Clone()
	}
	return nil
}

// queryFromContext extracts the original HTTP request query parameters from the
// gin context embedded in the provided context. Mirrors headersFromContext so
// model routers can observe inbound query parameters for plain HTTP requests,
// where execOptions.Query is not populated by callers.
func queryFromContext(ctx context.Context) url.Values {
	if ctx == nil {
		return nil
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil && ginCtx.Request.URL != nil {
		return ginCtx.Request.URL.Query()
	}
	return nil
}

func pinnedAuthIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(pinnedAuthContextKey{})
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func selectedAuthIDCallbackFromContext(ctx context.Context) func(string) {
	if ctx == nil {
		return nil
	}
	raw := ctx.Value(selectedAuthCallbackContextKey{})
	if callback, ok := raw.(func(string)); ok && callback != nil {
		return callback
	}
	return nil
}

func executionSessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(executionSessionContextKey{})
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func disallowFreeAuthFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	raw, ok := ctx.Value(disallowFreeAuthContextKey{}).(bool)
	return ok && raw
}
