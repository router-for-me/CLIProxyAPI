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

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

// geminiSSEKeepAliveFrame is an empty SSE frame used for Gemini SSE heartbeats.
// google.golang.org/genai's iterateResponseStream skips empty frames but rejects
// SSE comment lines (": keep-alive") with "invalid stream chunk".
var geminiSSEKeepAliveFrame = []byte("\n\n")

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
	h.WriteModelListResponse(c, h.HandlerType(), gin.H{
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
	trimmedAction := strings.TrimPrefix(request.Action, "/")
	colonIdx := strings.LastIndex(trimmedAction, ":")
	if colonIdx <= 0 || colonIdx == len(trimmedAction)-1 {
		c.JSON(http.StatusNotFound, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("%s not found.", c.Request.URL.Path),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	modelName := trimmedAction[:colonIdx]
	method := trimmedAction[colonIdx+1:]
	rawJSON, errRead := c.GetRawData()
	if errRead != nil && c.Request != nil && c.Request.Body != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(errRead, http.StatusBadRequest), handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", errRead),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	switch method {
	case "generateContent":
		h.handleGenerateContent(c, modelName, rawJSON)
	case "streamGenerateContent":
		h.handleStreamGenerateContent(c, modelName, rawJSON)
	case "countTokens":
		h.handleCountTokens(c, modelName, rawJSON)
	default:
		c.JSON(http.StatusNotFound, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Unsupported method: %s", method),
				Type:    "invalid_request_error",
			},
		})
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
	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}
	var upstreamHeaders http.Header
	committed := false
	commitHeaders := func() {
		if committed {
			return
		}
		if alt == "" {
			setSSEHeaders()
		}
		handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
		committed = true
	}
	writeKeepAlive := func() bool {
		commitHeaders()
		_, _ = c.Writer.Write(geminiSSEKeepAliveFrame)
		flusher.Flush()
		return true
	}
	var keepAliveC <-chan time.Time
	stopKeepAlive := func() {}
	if alt == "" {
		keepAliveC, stopKeepAlive = handlers.NewPreFirstTokenKeepAliveTicker(h.CurrentConfig())
	}
	defer stopKeepAlive()

	dataChan, resolvedHeaders, errChan, okBootstrap := h.ExecuteStreamWithPreFTKeepAlive(
		c, cliCtx, func(err error) { cliCancel(err) }, h.HandlerType(), modelName, rawJSON, alt, keepAliveC, writeKeepAlive,
	)
	if !okBootstrap {
		return
	}
	upstreamHeaders = resolvedHeaders
	waitKeepAliveC, canWriteWaitKeepAlive, stopWaitKeepAlive := h.StartContinuityWaitKeepAliveForPeek(c, keepAliveC)
	defer stopWaitKeepAlive()

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
			if committed {
				stopKeepAlive()
				stopWaitKeepAlive()
				pendingErrors := make(chan *interfaces.ErrorMessage, 1)
				pendingErrors <- errMsg
				close(pendingErrors)
				h.forwardGeminiStream(c, flusher, alt, func(err error) { cliCancel(err) }, make(chan []byte), pendingErrors)
				return
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
				stopKeepAlive()
				stopWaitKeepAlive()
				if errMsg, hasPendingError := handlers.PendingStreamError(errChan); hasPendingError {
					if committed {
						pendingErrors := make(chan *interfaces.ErrorMessage, 1)
						pendingErrors <- errMsg
						close(pendingErrors)
						h.forwardGeminiStream(c, flusher, alt, func(err error) { cliCancel(err) }, make(chan []byte), pendingErrors)
						return
					}
					h.WriteErrorResponse(c, errMsg)
					if errMsg != nil {
						cliCancel(errMsg.Error)
					} else {
						cliCancel(nil)
					}
					return
				}
				// Closed without data
				commitHeaders()
				flusher.Flush()
				cliCancel(nil)
				return
			}

			// Success! Set headers.
			stopKeepAlive()
			stopWaitKeepAlive()
			commitHeaders()

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
		case <-waitKeepAliveC:
			if canWriteWaitKeepAlive() && !writeKeepAlive() {
				return
			}
		case <-keepAliveC:
			if !writeKeepAlive() {
				return
			}
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
	resp, upstreamHeaders, errMsg := h.ExecuteCountWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
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
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

func (h *GeminiAPIHandler) forwardGeminiStream(c *gin.Context, flusher http.Flusher, alt string, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage) {
	var keepAliveInterval *time.Duration
	if alt != "" {
		keepAliveInterval = new(time.Duration(0))
	}

	h.ForwardStream(c, flusher, cancel, data, errs, handlers.StreamForwardOptions{
		KeepAliveInterval: keepAliveInterval,
		WriteKeepAlive: func() {
			if alt == "" {
				_, _ = c.Writer.Write(geminiSSEKeepAliveFrame)
			}
		},
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
			h.LoggingAPIResponseError(c, errMsg)
			status := http.StatusInternalServerError
			if errMsg.StatusCode > 0 {
				status = errMsg.StatusCode
			}
			errText := http.StatusText(status)
			if errMsg.Error != nil && errMsg.Error.Error() != "" {
				errText = errMsg.Error.Error()
			}
			body := buildGeminiErrorResponseBody(status, errText)
			if alt == "" {
				// Emit a raw Google APIError JSON frame without "event: error" or "data:"
				// wrapping so google.golang.org/genai's iterateResponseStream decodes it
				// as *genai.APIError in its default branch across all SDK versions.
				_, _ = fmt.Fprintf(c.Writer, "%s\n\n", string(body))
			} else {
				_, _ = c.Writer.Write(body)
			}
		},
	})
}

type geminiErrorResponse struct {
	Error *geminiErrorDetail `json:"error"`
}

type geminiErrorDetail struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Status  string           `json:"status"`
	Details []map[string]any `json:"details,omitempty"`
}

func geminiRPCStatusFromHTTPStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return "DEADLINE_EXCEEDED"
	case http.StatusConflict:
		return "ABORTED"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case 499:
		return "CANCELLED"
	case http.StatusNotImplemented:
		return "UNIMPLEMENTED"
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	default:
		if status >= http.StatusInternalServerError {
			return "INTERNAL"
		}
		if status >= http.StatusBadRequest {
			return "INVALID_ARGUMENT"
		}
		return "UNKNOWN"
	}
}

func extractGeminiErrorMessage(raw, fallback string) string {
	if !ojson.Valid(raw) {
		return fallback
	}
	root := ojson.Parse(raw)
	for _, path := range []string{"error.message", "0.error.message", "message"} {
		res := root.Get(path)
		if res.Type == ojson.String {
			if msg := strings.TrimSpace(res.String()); msg != "" {
				return msg
			}
		}
	}
	return fallback
}

// buildGeminiErrorResponseBody builds a compact single-line Google RPC error
// JSON body compatible with google.golang.org/genai's APIError struct.
func buildGeminiErrorResponseBody(status int, errText string) []byte {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	trimmed := strings.TrimSpace(errText)
	if trimmed == "" {
		trimmed = http.StatusText(status)
	}

	if trimmed != "" && ojson.Valid(trimmed) {
		var existing geminiErrorResponse
		if errUnmarshal := json.Unmarshal([]byte(trimmed), &existing); errUnmarshal == nil && existing.Error != nil && existing.Error.Code > 0 {
			if strings.TrimSpace(existing.Error.Status) == "" {
				existing.Error.Status = geminiRPCStatusFromHTTPStatus(existing.Error.Code)
			}
			if strings.TrimSpace(existing.Error.Message) == "" {
				existing.Error.Message = http.StatusText(existing.Error.Code)
			}
			if payload, errMarshal := json.Marshal(existing); errMarshal == nil {
				return payload
			}
		}
		if strings.HasPrefix(trimmed, "[") {
			var existingList []geminiErrorResponse
			if errUnmarshal := json.Unmarshal([]byte(trimmed), &existingList); errUnmarshal == nil && len(existingList) > 0 && existingList[0].Error != nil && existingList[0].Error.Code > 0 {
				entry := existingList[0]
				if strings.TrimSpace(entry.Error.Status) == "" {
					entry.Error.Status = geminiRPCStatusFromHTTPStatus(entry.Error.Code)
				}
				if strings.TrimSpace(entry.Error.Message) == "" {
					entry.Error.Message = http.StatusText(entry.Error.Code)
				}
				if payload, errMarshal := json.Marshal(entry); errMarshal == nil {
					return payload
				}
			}
		}
	}

	message := extractGeminiErrorMessage(trimmed, trimmed)
	rpcStatus := geminiRPCStatusFromHTTPStatus(status)
	payload, errMarshal := json.Marshal(geminiErrorResponse{
		Error: &geminiErrorDetail{
			Code:    status,
			Message: message,
			Status:  rpcStatus,
		},
	})
	if errMarshal != nil {
		return []byte(fmt.Sprintf(`{"error":{"code":%d,"message":%q,"status":%q}}`, status, message, rpcStatus))
	}
	return payload
}
