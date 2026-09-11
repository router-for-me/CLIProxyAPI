package util

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type sessionIDContextKey struct{}

// WithSessionID returns a new context annotated with the internal session ID.
// Passing an empty sessionID explicitly clears/overrides any previous session ID in parent contexts.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionIDContextKey{}, strings.TrimSpace(sessionID))
}

// SessionIDFromContext retrieves the internal session ID from the context if present.
func SessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(sessionIDContextKey{}).(string); ok {
		return strings.TrimSpace(id)
	}
	return ""
}

// HasExplicitSessionID returns true if ctx explicitly carries a session ID annotation
// (including an explicit empty string used to clear it).
func HasExplicitSessionID(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(sessionIDContextKey{}).(string)
	return ok
}

// SessionIDResolver is a pluggable resolver for extracting the internal session ID.
var SessionIDResolver func(ctx context.Context, clientHeaders http.Header) string

// ApplyCustomHeadersFromAttrs applies user-defined headers stored in the provided attributes map.
// Custom headers override built-in defaults when conflicts occur.
// If clientHeaders is provided (or if the request context carries a Gin context), any custom header
// whose value starts with "$" (e.g. "$ABC" or "$X-Claude-Code-Session-Id") is dynamically
// resolved from the client's request headers. If the client did not provide that header,
// the custom header is omitted from the outgoing request.
// The magic variable $CPA-SESSION-ID expands to the internal session identifier used for
// session-affinity (regardless of whether session-affinity is enabled).
func ApplyCustomHeadersFromAttrs(r *http.Request, attrs map[string]string, clientHeaders ...http.Header) {
	if r == nil {
		return
	}
	var ch http.Header
	var ctx context.Context
	if r.Context() != nil {
		ctx = r.Context()
	}
	if len(clientHeaders) > 0 && clientHeaders[0] != nil {
		ch = clientHeaders[0]
	} else if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			ch = ginCtx.Request.Header
		} else if ginCtx, ok := ctx.(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			ch = ginCtx.Request.Header
		}
	}
	applyCustomHeaders(r, extractCustomHeaders(attrs, ch, ctx))
}

func resolveCPASessionID(ctx context.Context, clientHeaders http.Header) string {
	if ctx != nil {
		if id := SessionIDFromContext(ctx); id != "" {
			return id
		}
		if HasExplicitSessionID(ctx) {
			return ""
		}
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			if id := SessionIDFromContext(ginCtx.Request.Context()); id != "" {
				return id
			}
			if HasExplicitSessionID(ginCtx.Request.Context()) {
				return ""
			}
			if clientHeaders == nil {
				clientHeaders = ginCtx.Request.Header
			}
		} else if ginCtx, ok := ctx.(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			if id := SessionIDFromContext(ginCtx.Request.Context()); id != "" {
				return id
			}
			if HasExplicitSessionID(ginCtx.Request.Context()) {
				return ""
			}
			if clientHeaders == nil {
				clientHeaders = ginCtx.Request.Header
			}
		}
	}
	if SessionIDResolver != nil {
		if id := SessionIDResolver(ctx, clientHeaders); id != "" {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

func replaceCPASessionID(val, sessionID string) string {
	target := "$CPA-SESSION-ID"
	targetLen := len(target)
	if len(val) < targetLen {
		return val
	}
	var sb strings.Builder
	start := 0
	for i := 0; i <= len(val)-targetLen; {
		if val[i] == '$' && strings.EqualFold(val[i:i+targetLen], target) {
			sb.WriteString(val[start:i])
			sb.WriteString(sessionID)
			i += targetLen
			start = i
		} else {
			i++
		}
	}
	if start == 0 {
		return val
	}
	sb.WriteString(val[start:])
	return sb.String()
}

func extractCustomHeaders(attrs map[string]string, clientHeaders http.Header, ctx context.Context) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	headers := make(map[string]string)
	for k, v := range attrs {
		if !strings.HasPrefix(k, "header:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(k, "header:"))
		if name == "" {
			continue
		}
		val := strings.TrimSpace(v)
		if val == "" {
			continue
		}
		if strings.HasPrefix(val, "$") && strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(val, "$")), "CPA-SESSION-ID") {
			sessionID := resolveCPASessionID(ctx, clientHeaders)
			if sessionID == "" {
				continue
			}
			val = sessionID
		} else if strings.Contains(strings.ToUpper(val), "$CPA-SESSION-ID") {
			sessionID := resolveCPASessionID(ctx, clientHeaders)
			if sessionID == "" {
				continue
			}
			val = replaceCPASessionID(val, sessionID)
		} else if strings.HasPrefix(val, "$") {
			varName := strings.TrimSpace(strings.TrimPrefix(val, "$"))
			if varName == "" {
				continue
			}
			if clientHeaders == nil {
				continue
			}
			clientVal := clientHeaders.Get(varName)
			if clientVal == "" {
				for ck, cv := range clientHeaders {
					if strings.EqualFold(ck, varName) && len(cv) > 0 && cv[0] != "" {
						clientVal = cv[0]
						break
					}
				}
			}
			if clientVal == "" {
				continue
			}
			val = clientVal
		}
		headers[name] = val
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func applyCustomHeaders(r *http.Request, headers map[string]string) {
	if r == nil || len(headers) == 0 {
		return
	}
	for k, v := range headers {
		if k == "" || v == "" {
			continue
		}
		// net/http reads Host from req.Host (not req.Header) when writing
		// a real request, so we must mirror it there. Some callers pass
		// synthetic requests (e.g. &http.Request{Header: ...}) and only
		// consume r.Header afterwards, so keep the value in the header
		// map too.
		if http.CanonicalHeaderKey(k) == "Host" {
			r.Host = v
		}
		r.Header.Set(k, v)
	}
}
