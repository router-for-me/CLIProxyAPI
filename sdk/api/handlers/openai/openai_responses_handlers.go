// Package openai provides HTTP handlers for OpenAIResponses API endpoints.
// This package implements the OpenAIResponses-compatible API interface, including model listing
// and chat completion functionality. It supports both streaming and non-streaming responses,
// and manages a pool of clients to interact with backend services.
// The handlers translate OpenAIResponses API requests to the appropriate backend format and
// convert responses back to OpenAIResponses-compatible format.
package openai

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "sync"
    "strings"
    "time"

	"github.com/gin-gonic/gin"
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAIResponsesAPIHandler contains the handlers for OpenAIResponses API endpoints.
// It holds a pool of clients to interact with the backend service.
type OpenAIResponsesAPIHandler struct {
    *handlers.BaseAPIHandler
    prevOutputs sync.Map // key: response_id, value: string
}

// NewOpenAIResponsesAPIHandler creates a new OpenAIResponses API handlers instance.
// It takes an BaseAPIHandler instance as input and returns an OpenAIResponsesAPIHandler.
//
// Parameters:
//   - apiHandlers: The base API handlers instance
//
// Returns:
//   - *OpenAIResponsesAPIHandler: A new OpenAIResponses API handlers instance
func NewOpenAIResponsesAPIHandler(apiHandlers *handlers.BaseAPIHandler) *OpenAIResponsesAPIHandler {
    return &OpenAIResponsesAPIHandler{BaseAPIHandler: apiHandlers}
}

// HandlerType returns the identifier for this handler implementation.
func (h *OpenAIResponsesAPIHandler) HandlerType() string {
	return OpenaiResponse
}

// Models returns the OpenAIResponses-compatible model metadata supported by this handler.
func (h *OpenAIResponsesAPIHandler) Models() []map[string]any {
	// Get dynamic models from the global registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("openai")
}

// OpenAIResponsesModels handles the /v1/models endpoint.
// It returns a list of available AI models with their capabilities
// and specifications in OpenAIResponses-compatible format.
func (h *OpenAIResponsesAPIHandler) OpenAIResponsesModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   h.Models(),
	})
}

// Responses handles the /v1/responses endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIResponsesAPIHandler) Responses(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	// If data retrieval fails, return a 400 Bad Request error.
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Check if the client requested a streaming response.
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		h.handleStreamingResponse(c, rawJSON)
	} else {
		h.handleNonStreamingResponse(c, rawJSON)
	}

}

// handleNonStreamingResponse handles non-streaming chat completion responses
// for Gemini models. It selects a client from the pool, sends the request, and
// aggregates the response before sending it back to the client in OpenAIResponses format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	defer func() { cliCancel() }()

    // Continuity: inject prior assistant message if previous_response_id is present and known.
    // Use input_text (not output_text) so downstream translators treat it as a valid message part.
    if prevID := gjson.GetBytes(rawJSON, "previous_response_id").String(); prevID != "" {
        if v, ok := h.prevOutputs.Load(prevID); ok {
            prevText, _ := v.(string)
            if prevText != "" {
                // Structured prepend of assistant message to input array
                if arr := gjson.GetBytes(rawJSON, "input"); arr.Exists() && arr.IsArray() {
                    assistantObj := map[string]any{
                        "role": "assistant",
                        "content": []any{map[string]any{
                            "type": "output_text",
                            "text": prevText,
                        }},
                    }
                    newArr := make([]any, 0, len(arr.Array())+1)
                    newArr = append(newArr, assistantObj)
                    for _, it := range arr.Array() {
                        newArr = append(newArr, it.Value())
                    }
                    if b, err := json.Marshal(newArr); err == nil {
                        if updated, err2 := sjson.SetRawBytes(rawJSON, "input", b); err2 == nil {
                            rawJSON = updated
                        }
                    }
                }
            }
        }
    }

	resp, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		return
	}


	// Post-process ordering: ensure any function_call outputs precede assistant message outputs in the same turn.
	if out := gjson.GetBytes(resp, "output"); out.Exists() && out.IsArray() {
		arr := out.Array()
		if len(arr) > 1 {
			var others []string
			var funcs []string
			var msgs []string
			for _, it := range arr {
				t := it.Get("type").String()
				switch t {
				case "function_call":
					funcs = append(funcs, it.Raw)
				case "message":
					msgs = append(msgs, it.Raw)
				default:
					others = append(others, it.Raw)
				}
			}
			rebuilt := make([]string, 0, len(arr))
			rebuilt = append(rebuilt, others...)
			rebuilt = append(rebuilt, funcs...)
			rebuilt = append(rebuilt, msgs...)
			newRaw := "[" + strings.Join(rebuilt, ",") + "]"
			if updated, err := sjson.SetRawBytes(resp, "output", []byte(newRaw)); err == nil {
				resp = updated
			}
		}
	}

	// Store first assistant message content for continuity keyed by response id
	rid := gjson.GetBytes(resp, "id").String()
	if rid == "" {
		rid = fmt.Sprintf("resp_%d", time.Now().UnixNano())
	}
	var prevText string
	if out := gjson.GetBytes(resp, "output"); out.Exists() && out.IsArray() {
		for _, item := range out.Array() {
			if item.Get("type").String() == "message" {
				prevText = item.Get("content.0.text").String()
				if prevText != "" {
					break
				}
			}
		}
	}
    if prevText != "" {
        h.prevOutputs.Store(rid, prevText)
    }
	_, _ = c.Writer.Write(resp)
	return

	// no legacy fallback

}

// handleStreamingResponse handles streaming responses for Gemini models.
// It establishes a streaming connection with the backend service and forwards
// the response chunks to the client in real-time using Server-Sent Events.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")

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

	// New core execution path
	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	dataChan, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")
	h.forwardResponsesStream(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan)
	return
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage) {
	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return
		case chunk, ok := <-data:
			if !ok {
				_, _ = c.Writer.Write([]byte("\n"))
				flusher.Flush()
				cancel(nil)
				return
			}

			if bytes.HasPrefix(chunk, []byte("event:")) {
				_, _ = c.Writer.Write([]byte("\n"))
			}
			_, _ = c.Writer.Write(chunk)
			_, _ = c.Writer.Write([]byte("\n"))

			flusher.Flush()
		case errMsg, ok := <-errs:
			if !ok {
				continue
			}
			if errMsg != nil {
				h.WriteErrorResponse(c, errMsg)
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
