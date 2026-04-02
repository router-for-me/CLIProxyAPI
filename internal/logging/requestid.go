package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"
)

// requestIDKey is the context key for storing/retrieving request IDs.
type requestIDKey struct{}

// upstreamRequestIDKey is the context key for storing/retrieving upstream (e.g. llmgate) request IDs.
type upstreamRequestIDKey struct{}

// ginRequestIDKey is the Gin context key for request IDs.
const ginRequestIDKey = "__request_id__"

// ginUpstreamRequestIDKey is the Gin context key for upstream request IDs.
const ginUpstreamRequestIDKey = "__upstream_request_id__"

// GenerateRequestID creates a new 8-character hex request ID.
func GenerateRequestID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b)
}

// WithRequestID returns a new context with the request ID attached.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

// GetRequestID retrieves the request ID from the context.
// Returns empty string if not found.
func GetRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// SetGinRequestID stores the request ID in the Gin context.
func SetGinRequestID(c *gin.Context, requestID string) {
	if c != nil {
		c.Set(ginRequestIDKey, requestID)
	}
}

// GetGinRequestID retrieves the request ID from the Gin context.
func GetGinRequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if id, exists := c.Get(ginRequestIDKey); exists {
		if s, ok := id.(string); ok {
			return s
		}
	}
	return ""
}

// WithUpstreamRequestID returns a new context with the upstream request ID attached.
func WithUpstreamRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, upstreamRequestIDKey{}, requestID)
}

// GetUpstreamRequestID retrieves the upstream request ID from the context.
// Returns empty string if not found.
func GetUpstreamRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(upstreamRequestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// SetGinUpstreamRequestID stores the upstream request ID in the Gin context.
func SetGinUpstreamRequestID(c *gin.Context, requestID string) {
	if c != nil {
		c.Set(ginUpstreamRequestIDKey, requestID)
	}
}

// GetGinUpstreamRequestID retrieves the upstream request ID from the Gin context.
func GetGinUpstreamRequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if id, exists := c.Get(ginUpstreamRequestIDKey); exists {
		if s, ok := id.(string); ok {
			return s
		}
	}
	return ""
}
