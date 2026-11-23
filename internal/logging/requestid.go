package logging

import (
	"context"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// requestIDKey is the context key for storing/retrieving request IDs.
type requestIDKey struct{}

// ginRequestIDKey is the Gin context key for request IDs.
const ginRequestIDKey = "__request_id__"

// GenerateRequestID creates a new UUIDv7 request ID string using the default random source.
// It returns an error if the underlying random source fails.
func GenerateRequestID() (string, error) {
	return GenerateRequestIDFromReader(nil)
}

// GenerateRequestIDFromReader creates a new UUIDv7 request ID string using the provided reader.
// If r is nil, the default crypto/rand source is used.
func GenerateRequestIDFromReader(r io.Reader) (string, error) {
	var id uuid.UUID
	var errNewV7 error
	if r == nil {
		id, errNewV7 = uuid.NewV7()
	} else {
		id, errNewV7 = uuid.NewV7FromReader(r)
	}
	if errNewV7 != nil {
		return "", errNewV7
	}
	return id.String(), nil
}

// ShortRequestID returns the trailing 8 characters of a request ID.
// If requestID is 8 characters or shorter, it returns it unchanged.
func ShortRequestID(requestID string) string {
	requestID = strings.TrimSpace(requestID)
	if len(requestID) > 8 {
		return requestID[len(requestID)-8:]
	}
	return requestID
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
