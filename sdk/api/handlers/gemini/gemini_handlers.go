// Package gemini provides HTTP handlers for Gemini API endpoints.
// This package implements handlers for managing Gemini model operations including
// model listing, content generation, streaming content generation, and token counting.
// It serves as a proxy layer between clients and the Gemini backend service,
// handling request translation, client management, and response processing.
package gemini

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
)

// GeminiAPIHandler contains the handlers for Gemini API endpoints.
// It holds a pool of clients to interact with the backend service.
type GeminiAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewGeminiAPIHandler creates a new Gemini API handlers instance.
// It takes an BaseAPIHandler instance as input and returns a GeminiAPIHandler.
func NewGeminiAPIHandler(apiHandlers *handlers.BaseAPIHandler) *GeminiAPIHandler {
	return &GeminiAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *GeminiAPIHandler) HandlerType() string {
	return Gemini
}

// Models returns the Gemini-compatible model metadata supported by this handler.
func (h *GeminiAPIHandler) Models() []map[string]any {
	// Get dynamic models from the global registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("gemini")
}

// GeminiModels handles the Gemini models listing endpoint.
// It returns a JSON response containing available Gemini models and their specifications.
func (h *GeminiAPIHandler) GeminiModels(c *gin.Context) {
	rawModels := h.Models()
	normalizedModels := make([]map[string]any, 0, len(rawModels))
	defaultMethods := []string{"generateContent"}
	for _, model := range rawModels {
		normalizedModel := make(map[string]any, len(model))
		for k, v := range model {
			normalizedModel[k] = v
		}
		if name, ok := normalizedModel["name"].(string); ok && name != "" {
			if !strings.HasPrefix(name, "models/") {
				normalizedModel["name"] = "models/" + name
			}
			if displayName, _ := normalizedModel["displayName"].(string); displayName == "" {
				normalizedModel["displayName"] = name
			}
			if description, _ := normalizedModel["description"].(string); description == "" {
				normalizedModel["description"] = name
			}
		}
		if _, ok := normalizedModel["supportedGenerationMethods"]; !ok {
			normalizedModel["supportedGenerationMethods"] = defaultMethods
		}
		normalizedModels = append(normalizedModels, normalizedModel)
	}
	c.JSON(http.StatusOK, gin.H{
		"models": normalizedModels,
	})
}

// GeminiGetHandler handles GET requests for specific Gemini model information.
// It returns detailed information about a specific Gemini model based on the action parameter.
func (h *GeminiAPIHandler) GeminiGetHandler(c *gin.Context) {
	var request struct {
		Action string `uri:"action" binding:"required"`
	}
	if err := c.ShouldBindUri(&request); err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	action := strings.TrimPrefix(request.Action, "/")

	// Get dynamic models from the global registry and find the matching one
	availableModels := h.Models()
	var targetModel map[string]any

	for _, model := range availableModels {
		name, _ := model["name"].(string)
		// Match name with or without 'models/' prefix
		if name == action || name == "models/"+action {
			targetModel = model
			break
		}
	}

	if targetModel != nil {
		// Ensure the name has 'models/' prefix in the output if it's a Gemini model
		if name, ok := targetModel["name"].(string); ok && name != "" && !strings.HasPrefix(name, "models/") {
			targetModel["name"] = "models/" + name
		}
		c.JSON(http.StatusOK, targetModel)
		return
	}

	c.JSON(http.StatusNotFound, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: "Not Found",
			Type:    "not_found",
		},
	})
}

// GeminiHandler handles POST requests for Gemini API operations.
// It routes requests to appropriate handlers based on the action parameter (model:method format).
func (h *GeminiAPIHandler) GeminiHandler(c *gin.Context) {
	var request struct {
		Action string `uri:"action" binding:"required"`
	}
	if err := c.ShouldBindUri(&request); err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	action := strings.Split(strings.TrimPrefix(request.Action, "/"), ":")
	if len(action) != 2 {
		c.JSON(http.StatusNotFound, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("%s not found.", c.Request.URL.Path),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	method := action[1]
	rawJSON, _ := c.GetRawData()

	switch method {
	case "generateContent":
		h.handleGenerateContent(c, action[0], rawJSON)
	case "streamGenerateContent":
		h.handleStreamGenerateContent(c, action[0], rawJSON)
	case "countTokens":
		h.handleCountTokens(c, action[0], rawJSON)
	}
}

// handleStreamGenerateContent handles streaming content generation requests for Gemini models.
// This function establishes a Server-Sent Events connection and streams the generated content
// back to the client in real-time. It supports both SSE format and direct streaming based
// on the 'alt' query parameter.
//
// Parameters:
//   - c: The Gin context for the request
//   - modelName: The name of the Gemini model to use for content generation
//   - rawJSON: The raw JSON request body containing generation parameters
func (h *GeminiAPIHandler) handleStreamGenerateContent(c *gin.Context, modelName string, rawJSON []byte) {
	alt := h.GetAlt(c)

	// Get the http.Flusher interface to manually flush the response.
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	dataChan, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)

	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}

	// Peek at the first chunk
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case errMsg, ok := <-errChan:
			if !ok {
				// Err channel closed cleanly; wait for data channel.
				errChan = nil
				continue
			}
			// Upstream failed immediately. Return proper error status and JSON.
			h.WriteErrorResponse(c, errMsg)
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				// Closed without data
				if alt == "" {
					setSSEHeaders()
				}
				flusher.Flush()
				cliCancel(nil)
				return
			}

			// Success! Set headers.
			if alt == "" {
				setSSEHeaders()
			}

			// Write first chunk
			if alt == "" {
				_, _ = c.Writer.Write([]byte("data: "))
				_, _ = c.Writer.Write(chunk)
				_, _ = c.Writer.Write([]byte("\n\n"))
			} else {
				_, _ = c.Writer.Write(chunk)
			}
			flusher.Flush()

			// Continue
			h.forwardGeminiStream(c, flusher, alt, func(err error) { cliCancel(err) }, dataChan, errChan)
			return
		}
	}
}

// handleCountTokens handles token counting requests for Gemini models.
// This function counts the number of tokens in the provided content without
// generating a response. It's useful for quota management and content validation.
//
// Parameters:
//   - c: The Gin context for the request
//   - modelName: The name of the Gemini model to use for token counting
//   - rawJSON: The raw JSON request body containing the content to count
func (h *GeminiAPIHandler) handleCountTokens(c *gin.Context, modelName string, rawJSON []byte) {
	c.Header("Content-Type", "application/json")
	alt := h.GetAlt(c)
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	resp, errMsg := h.ExecuteCountWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// handleGenerateContent handles non-streaming content generation requests for Gemini models.
// This function processes the request synchronously and returns the complete generated
// response in a single API call. It supports various generation parameters and
// response formats.
//
// Parameters:
//   - c: The Gin context for the request
//   - modelName: The name of the Gemini model to use for content generation
//   - rawJSON: The raw JSON request body containing generation parameters and content
func (h *GeminiAPIHandler) handleGenerateContent(c *gin.Context, modelName string, rawJSON []byte) {
	c.Header("Content-Type", "application/json")
	alt := h.GetAlt(c)
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

func (h *GeminiAPIHandler) forwardGeminiStream(c *gin.Context, flusher http.Flusher, alt string, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage) {
	var keepAliveInterval *time.Duration
	if alt != "" {
		disabled := time.Duration(0)
		keepAliveInterval = &disabled
	}

	h.ForwardStream(c, flusher, cancel, data, errs, handlers.StreamForwardOptions{
		KeepAliveInterval: keepAliveInterval,
		WriteChunk: func(chunk []byte) {
			if alt == "" {
				_, _ = c.Writer.Write([]byte("data: "))
				_, _ = c.Writer.Write(chunk)
				_, _ = c.Writer.Write([]byte("\n\n"))
			} else {
				_, _ = c.Writer.Write(chunk)
			}
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			if errMsg == nil {
				return
			}
			status := http.StatusInternalServerError
			if errMsg.StatusCode > 0 {
				status = errMsg.StatusCode
			}
			errText := http.StatusText(status)
			if errMsg.Error != nil && errMsg.Error.Error() != "" {
				errText = errMsg.Error.Error()
			}
			body := handlers.BuildErrorResponseBody(status, errText)
			if alt == "" {
				_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", string(body))
			} else {
				_, _ = c.Writer.Write(body)
			}
		},
	})
}
