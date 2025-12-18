package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	CorrelationContextKey   = "correlation_id"
	HeaderXCorrelationID    = "X-Correlation-ID"
	HeaderXRequestID        = "X-Request-ID"
)

func CorrelationMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		correlationID := c.GetHeader(HeaderXCorrelationID)
		if correlationID == "" {
			correlationID = c.GetHeader(HeaderXRequestID)
		}
		if correlationID == "" {
			correlationID = uuid.New().String()
		}

		c.Set(CorrelationContextKey, correlationID)

		c.Header(HeaderXCorrelationID, correlationID)
		c.Header(HeaderXRequestID, correlationID)

		c.Next()
	}
}

func GetCorrelationID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if val, exists := c.Get(CorrelationContextKey); exists {
		if id, ok := val.(string); ok {
			return id
		}
	}
	return ""
}
