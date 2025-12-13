// Package logging provides Gin middleware for HTTP request logging and panic recovery.
// It integrates Gin web framework with logrus for structured logging of HTTP requests,
// responses, and error handling with panic recovery capabilities.
package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const skipGinLogKey = "__gin_skip_request_logging__"

// maxBodyReadSize limits how much of the request body we read for model extraction
// to prevent memory issues with large payloads.
const maxBodyReadSize = 64 * 1024 // 64KB

// GinLogrusLogger returns a Gin middleware handler that logs HTTP requests and responses
// using logrus. It captures request details including method, path, status code, latency,
// client IP, model name (if available), and any error messages, formatting them in a Gin-style log format.
//
// Returns:
//   - gin.HandlerFunc: A middleware handler for request logging
func GinLogrusLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := util.MaskSensitiveQuery(c.Request.URL.RawQuery)

		// Extract model from request body before processing
		model := extractModelFromRequest(c)

		c.Next()

		if shouldSkipGinRequestLogging(c) {
			return
		}

		if raw != "" {
			path = path + "?" + raw
		}

		latency := time.Since(start)
		if latency > time.Minute {
			latency = latency.Truncate(time.Second)
		} else {
			latency = latency.Truncate(time.Millisecond)
		}

		statusCode := c.Writer.Status()
		clientIP := c.ClientIP()
		method := c.Request.Method
		errorMessage := c.Errors.ByType(gin.ErrorTypePrivate).String()
		timestamp := time.Now().Format("2006/01/02 - 15:04:05")
		logLine := fmt.Sprintf("[GIN] %s | %3d | %13v | %15s | %-7s \"%s\"", timestamp, statusCode, latency, clientIP, method, path)
		if model != "" {
			logLine = logLine + " | model=" + model
		}
		if errorMessage != "" {
			logLine = logLine + " | " + errorMessage
		}

		switch {
		case statusCode >= http.StatusInternalServerError:
			log.Error(logLine)
		case statusCode >= http.StatusBadRequest:
			log.Warn(logLine)
		default:
			log.Info(logLine)
		}
	}
}

// GinLogrusRecovery returns a Gin middleware handler that recovers from panics and logs
// them using logrus. When a panic occurs, it captures the panic value, stack trace,
// and request path, then returns a 500 Internal Server Error response to the client.
//
// Returns:
//   - gin.HandlerFunc: A middleware handler for panic recovery
func GinLogrusRecovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		log.WithFields(log.Fields{
			"panic": recovered,
			"stack": string(debug.Stack()),
			"path":  c.Request.URL.Path,
		}).Error("recovered from panic")

		c.AbortWithStatus(http.StatusInternalServerError)
	})
}

// SkipGinRequestLogging marks the provided Gin context so that GinLogrusLogger
// will skip emitting a log line for the associated request.
func SkipGinRequestLogging(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(skipGinLogKey, true)
}

func shouldSkipGinRequestLogging(c *gin.Context) bool {
	if c == nil {
		return false
	}
	val, exists := c.Get(skipGinLogKey)
	if !exists {
		return false
	}
	flag, ok := val.(bool)
	return ok && flag
}

// extractModelFromRequest reads the request body to extract the "model" field
// for logging purposes. It restores the body so downstream handlers can read it.
// Only extracts from POST/PUT/PATCH requests with JSON content-type.
// Returns empty string if model cannot be extracted.
func extractModelFromRequest(c *gin.Context) string {
	// Only extract model for methods that typically have a JSON body
	method := c.Request.Method
	if method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch {
		return ""
	}

	// Only extract if content-type is JSON
	contentType := c.GetHeader("Content-Type")
	if contentType == "" || !strings.HasPrefix(contentType, "application/json") {
		return ""
	}

	if c.Request.Body == nil {
		return ""
	}

	// Read the body with size limit to prevent memory issues
	limitedReader := io.LimitReader(c.Request.Body, maxBodyReadSize)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return ""
	}

	// Restore the body for downstream handlers
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	// Parse JSON to extract model
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return ""
	}

	return payload.Model
}
