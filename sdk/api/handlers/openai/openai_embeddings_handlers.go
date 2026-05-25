package openai

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/tidwall/gjson"
)

// Embeddings handles the OpenAI Embeddings API endpoint.
// This endpoint creates an embedding vector representing the input text.
//
// OpenAI Embeddings API Reference:
// https://platform.openai.com/docs/api-reference/embeddings
//
// The handler:
// - Reads the request body containing the model and input text
// - Extracts the model name from the JSON payload
// - Executes the request via the auth manager with round-robin load balancing
// - Returns the embedding vector(s) in the response
//
// Note: Embeddings do not support streaming, so this is a non-streaming endpoint only.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIAPIHandler) Embeddings(c *gin.Context) {
	rawJSON, err := handlers.ReadRequestBody(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Extract model name from the request
	modelName := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	if modelName == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: model is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Set response content type
	c.Header("Content-Type", "application/json")

	// Create context with cancellation support
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	// Start keep-alive for long-running requests
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	// Execute the request via auth manager (non-streaming)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")

	// Stop keep-alive after execution completes
	stopKeepAlive()

	// Handle errors
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	// Write upstream headers and response body
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel(nil)
}

// Made with Bob
