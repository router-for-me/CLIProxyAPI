package middleware

import (
	"context"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

// Capture caps: a single section may not exceed 1 MiB; the whole row may not
// exceed 4 MiB. Any truncation sets Truncated on the captured record.
const (
	bodyCaptureSectionMaxBytes = 1 << 20
	bodyCaptureTotalMaxBytes   = 4 << 20
)

// BodyCaptureRequest is one assembled request/response capture. Header maps are
// the raw HTTP headers; bodies are raw bytes (already capped by the caller).
type BodyCaptureRequest struct {
	RequestID             string
	Provider              string
	UpstreamProviderID    int64
	ClientRequestHeaders  map[string][]string
	ClientRequestBody     []byte
	ClientResponseHeaders map[string][]string
	ClientResponseBody    []byte
	UpstreamRequest       []byte
	UpstreamResponse      []byte
	Truncated             bool
}

// BodyCaptureSink persists captured pairs. Implementations must be safe for
// concurrent use and must not block the request path for long.
type BodyCaptureSink interface {
	Capture(ctx context.Context, req BodyCaptureRequest)
}

// storeRequestBodiesRequested reports whether the resolved executor asked for
// full body capture on this request.
func storeRequestBodiesRequested(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, ok := c.Get(logging.StoreRequestBodiesContextKey)
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

func storeRequestBodiesProvider(c *gin.Context) string {
	if c == nil {
		return ""
	}
	v, _ := c.Get(logging.StoreRequestBodiesProviderContextKey)
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func storeRequestBodiesUpstreamID(c *gin.Context) int64 {
	if c == nil {
		return 0
	}
	v, _ := c.Get(logging.StoreRequestBodiesUpstreamIDContextKey)
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return parsed
	default:
		return 0
	}
}

// capSection truncates b to at most max bytes, reporting whether it truncated.
func capSection(b []byte, max int) ([]byte, bool) {
	if max <= 0 || len(b) <= max {
		return b, false
	}
	return b[:max], true
}
