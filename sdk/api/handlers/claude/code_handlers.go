// Package claude provides HTTP handlers for Claude API code-related functionality.
// This package implements Claude-compatible streaming chat completions with sophisticated
// client rotation and quota management systems to ensure high availability and optimal
// resource utilization across multiple backend clients. It handles request translation
// between Claude API format and the underlying Gemini backend, providing seamless
// API compatibility while maintaining robust error handling and connection management.
package claude

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// ClaudeCodeAPIHandler contains the handlers for Claude API endpoints.
// It holds a pool of clients to interact with the backend service.
type ClaudeCodeAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewClaudeCodeAPIHandler creates a new Claude API handlers instance.
// It takes an BaseAPIHandler instance as input and returns a ClaudeCodeAPIHandler.
//
// Parameters:
//   - apiHandlers: The base API handler instance.
//
// Returns:
//   - *ClaudeCodeAPIHandler: A new Claude code API handler instance.
func NewClaudeCodeAPIHandler(apiHandlers *handlers.BaseAPIHandler) *ClaudeCodeAPIHandler {
	return &ClaudeCodeAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *ClaudeCodeAPIHandler) HandlerType() string {
	return Claude
}

// Models returns a list of models supported by this handler.
func (h *ClaudeCodeAPIHandler) Models() []map[string]any {
	// Get dynamic models from the global registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("claude")
}

// ClaudeMessages handles Claude-compatible streaming chat completions.
// This function implements a sophisticated client rotation and quota management system
// to ensure high availability and optimal resource utilization across multiple backend clients.
//
// Parameters:
//   - c: The Gin context for the request.
func (h *ClaudeCodeAPIHandler) ClaudeMessages(c *gin.Context) {
	// Extract raw JSON data from the incoming request
	rawJSON, err := c.GetRawData()
	// If data retrieval fails, return a 400 Bad Request error.
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Type: "error",
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Check if the client requested a streaming response.
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if !streamResult.Exists() || streamResult.Type == gjson.False {
		h.handleNonStreamingResponse(c, rawJSON)
	} else {
		h.handleStreamingResponse(c, rawJSON)
	}
}

// ClaudeMessages handles Claude-compatible streaming chat completions.
// This function implements a sophisticated client rotation and quota management system
// to ensure high availability and optimal resource utilization across multiple backend clients.
//
// Parameters:
//   - c: The Gin context for the request.
func (h *ClaudeCodeAPIHandler) ClaudeCountTokens(c *gin.Context) {
	// Extract raw JSON data from the incoming request
	rawJSON, err := c.GetRawData()
	// If data retrieval fails, return a 400 Bad Request error.
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Type: "error",
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	c.Header("Content-Type", "application/json")

	alt := h.GetAlt(c)
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	modelName := gjson.GetBytes(rawJSON, "model").String()

	resp, errMsg := h.ExecuteCountWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// ClaudeModels handles the Claude models listing endpoint.
// It returns a JSON response containing available Claude models and their specifications.
//
// Parameters:
//   - c: The Gin context for the request.
func (h *ClaudeCodeAPIHandler) ClaudeModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"data": h.Models(),
	})
}

// handleNonStreamingResponse handles non-streaming content generation requests for Claude models.
// This function processes the request synchronously and returns the complete generated
// response in a single API call. It supports various generation parameters and
// response formats.
//
// Parameters:
//   - c: The Gin context for the request
//   - modelName: The name of the Gemini model to use for content generation
//   - rawJSON: The raw JSON request body containing generation parameters and content
func (h *ClaudeCodeAPIHandler) handleNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")
	alt := h.GetAlt(c)
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	modelName := gjson.GetBytes(rawJSON, "model").String()

	resp, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}

	// Decompress gzipped responses - Claude API sometimes returns gzip without Content-Encoding header
	// This fixes title generation and other non-streaming responses that arrive compressed
	if len(resp) >= 2 && resp[0] == 0x1f && resp[1] == 0x8b {
		gzReader, err := gzip.NewReader(bytes.NewReader(resp))
		if err != nil {
			log.Warnf("failed to decompress gzipped Claude response: %v", err)
		} else {
			defer gzReader.Close()
			if decompressed, err := io.ReadAll(gzReader); err != nil {
				log.Warnf("failed to read decompressed Claude response: %v", err)
			} else {
				resp = decompressed
			}
		}
	}

	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// handleStreamingResponse streams Claude-compatible responses backed by Gemini.
// It sets up SSE, selects a backend client with rotation/quota logic,
// forwards chunks, and translates them to Claude CLI format.
//
// Parameters:
//   - c: The Gin context for the request.
//   - rawJSON: The raw JSON request body.
func (h *ClaudeCodeAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
	// Set up Server-Sent Events (SSE) headers for streaming response
	// These headers are essential for maintaining a persistent connection
	// and enabling real-time streaming of chat completions
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")

	// Get the http.Flusher interface to manually flush the response.
	// This is crucial for streaming as it allows immediate sending of data chunks
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Type: "error",
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	modelName := gjson.GetBytes(rawJSON, "model").String()

	// Create a cancellable context for the backend client request
	// This allows proper cleanup and cancellation of ongoing requests
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	dataChan, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")
	h.forwardClaudeStream(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan)
	return
}

func (h *ClaudeCodeAPIHandler) forwardClaudeStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage) {
	// OpenAI-style stream forwarding: write each SSE chunk and flush immediately.
	// This guarantees clients see incremental output even for small responses.
	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return

		case chunk, ok := <-data:
			if !ok {
				flusher.Flush()
				cancel(nil)
				return
			}
			if len(chunk) > 0 {
				_, _ = c.Writer.Write(chunk)
				flusher.Flush()
			}

		case errMsg, ok := <-errs:
			if !ok {
				continue
			}
			if errMsg != nil {
				// Convert error to Claude API format and get the appropriate status code
				claudeErr, status := h.toClaudeError(errMsg)
				c.Status(status)

				// An error occurred: emit as a proper SSE error event
				errorBytes, _ := json.Marshal(claudeErr)
				_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", errorBytes)
				flusher.Flush()
			}

			var execErr error
			if errMsg != nil {
				execErr = errMsg.Error
			}
			cancel(execErr)
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

type claudeErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type claudeErrorResponse struct {
	Type  string            `json:"type"`
	Error claudeErrorDetail `json:"error"`
}

func (h *ClaudeCodeAPIHandler) toClaudeError(msg *interfaces.ErrorMessage) (claudeErrorResponse, int) {
	errMsg := msg.Error.Error()
	errType := "api_error"
	statusCode := msg.StatusCode

	// Try to extract meaningful error message from nested JSON structures
	// This handles errors from various upstream providers (Antigravity, Gemini, etc.)
	extractedMsg, extractedType := extractErrorFromJSON(errMsg)
	if extractedMsg != "" {
		errMsg = extractedMsg
	}
	if extractedType != "" {
		errType = extractedType
	}

	// Map specific error messages to appropriate Claude API error types
	// This ensures Droid and other clients handle errors correctly
	errMsg, errType, statusCode = mapToClaudeErrorType(errMsg, errType, statusCode)

	return claudeErrorResponse{
		Type: "error",
		Error: claudeErrorDetail{
			Type:    errType,
			Message: errMsg,
		},
	}, statusCode
}

// mapToClaudeErrorType maps specific error messages to the correct Claude API error types
// Reference: https://docs.anthropic.com/en/api/errors
func mapToClaudeErrorType(message, errType string, statusCode int) (string, string, int) {
	// "Prompt is too long" should be request_too_large (413) not invalid_request_error (400)
	// This tells Droid that compressing history won't help - the request itself is too large
	if strings.Contains(strings.ToLower(message), "prompt is too long") ||
		strings.Contains(strings.ToLower(message), "request too large") ||
		strings.Contains(strings.ToLower(message), "exceeds maximum") {
		return message, "request_too_large", http.StatusRequestEntityTooLarge // 413
	}

	// Rate limit errors
	if strings.Contains(strings.ToLower(message), "rate limit") ||
		strings.Contains(strings.ToLower(message), "too many requests") {
		return message, "rate_limit_error", http.StatusTooManyRequests // 429
	}

	// Overloaded errors
	if strings.Contains(strings.ToLower(message), "overloaded") ||
		strings.Contains(strings.ToLower(message), "temporarily unavailable") {
		return message, "overloaded_error", 529
	}

	return message, errType, statusCode
}

// extractErrorFromJSON attempts to extract a clean error message and type from potentially nested JSON error structures
func extractErrorFromJSON(rawError string) (message string, errType string) {
	// Try to find the innermost error message
	// Common patterns:
	// 1. {"error": {"code": 400, "message": "{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"Prompt is too long\"}}"}}
	// 2. {"type":"error","error":{"type":"invalid_request_error","message":"Prompt is too long"}}

	currentJSON := rawError

	// Attempt up to 3 levels of JSON unwrapping
	for i := 0; i < 3; i++ {
		// Try pattern: {"error": {"message": "..."}}
		if gjson.Valid(currentJSON) {
			result := gjson.Parse(currentJSON)

			// Check for Claude-style error: error.message or error.error.message
			if innerMsg := result.Get("error.error.message"); innerMsg.Exists() && innerMsg.String() != "" {
				message = innerMsg.String()
				if innerType := result.Get("error.error.type"); innerType.Exists() {
					errType = innerType.String()
				}
				return
			}

			if innerMsg := result.Get("error.message"); innerMsg.Exists() && innerMsg.String() != "" {
				// The message might be another JSON string
				innerMsgStr := innerMsg.String()
				if gjson.Valid(innerMsgStr) {
					currentJSON = innerMsgStr
					continue
				}
				message = innerMsgStr
				if innerType := result.Get("error.type"); innerType.Exists() {
					errType = innerType.String()
				}
				return
			}

			// Check for direct message field
			if directMsg := result.Get("message"); directMsg.Exists() && directMsg.String() != "" {
				message = directMsg.String()
				if directType := result.Get("type"); directType.Exists() {
					errType = directType.String()
				}
				return
			}
		}
		break
	}

	return "", ""
}
