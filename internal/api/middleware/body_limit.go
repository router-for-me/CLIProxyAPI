package middleware

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// BodyLimitMiddleware returns a gin middleware that limits request body size.
// Requests exceeding maxBytes will receive a 413 Payload Too Large response.
// A maxBytes of 0 disables the limit.
func BodyLimitMiddleware(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if maxBytes <= 0 {
			c.Next()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()

		// If the body was too large, MaxBytesReader will have returned an error
		// when the handler tried to read it. The handler's error response takes
		// precedence, but if no response was written yet, we send 413.
		if c.Errors.Last() != nil {
			for _, e := range c.Errors {
				if e.Error() == "http: request body too large" {
					if !c.Writer.Written() {
						c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
							"error": gin.H{
								"type":    "invalid_request_error",
								"message": fmt.Sprintf("Request body too large. Maximum size: %d bytes", maxBytes),
							},
						})
					}
					return
				}
			}
		}
	}
}
