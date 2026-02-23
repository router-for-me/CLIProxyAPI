package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"
)

// requestIDKey is the context key for storing/retrieving request IDs.
type requestIDKey struct{}

// ginRequestIDKey is the Gin context key for request IDs.
const ginRequestIDKey = "__request_id__"

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
