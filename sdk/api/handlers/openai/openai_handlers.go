// Package openai provides HTTP handlers for OpenAI API endpoints.
// This package implements the OpenAI-compatible API interface, including model listing
// and chat completion functionality. It supports both streaming and non-streaming responses,
// and manages a pool of clients to interact with backend services.
// The handlers translate OpenAI API requests to the appropriate backend format and
// convert responses back to OpenAI-compatible format.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	codexmodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/models"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	codexconverter "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/openai/chat-completions"
	responsesconverter "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// OpenAIAPIHandler contains the handlers for OpenAI API endpoints.
// It holds a pool of clients to interact with the backend service.
type OpenAIAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewOpenAIAPIHandler creates a new OpenAI API handlers instance.
// It takes an BaseAPIHandler instance as input and returns an OpenAIAPIHandler.
//
// Parameters:
//   - apiHandlers: The base API handlers instance
//
// Returns:
//   - *OpenAIAPIHandler: A new OpenAI API handlers instance
func NewOpenAIAPIHandler(apiHandlers *handlers.BaseAPIHandler) *OpenAIAPIHandler {
	return &OpenAIAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *OpenAIAPIHandler) HandlerType() string {
	return OpenAI
}

// Models returns the OpenAI-compatible model metadata supported by this handler.
func (h *OpenAIAPIHandler) Models() []map[string]any {
	// Get dynamic models from the global registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("openai")
}

// OpenAIModels handles the /v1/models endpoint.
// It returns a list of available AI models with their capabilities
// and specifications in OpenAI-compatible format.
func (h *OpenAIAPIHandler) OpenAIModels(c *gin.Context) {
	if _, ok := c.Request.URL.Query()["client_version"]; ok {
		clientVersion := c.Query("client_version")
		body, errMarshal := codexmodels.MarshalCompact(h.codexClientModelsResponse(clientVersion))
		if errMarshal != nil {
			c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
				Error: handlers.ErrorDetail{
					Message: fmt.Sprintf("Failed to encode model list: %v", errMarshal),
					Type:    "server_error",
				},
			})
			return
		}
		h.WriteModelListResponse(c, h.HandlerType(), body)
		return
	}

	// Get all available models
	allModels := h.Models()

	// Filter to only include the 4 required fields: id, object, created, owned_by
	filteredModels := make([]map[string]any, len(allModels))
	for i, model := range allModels {
		filteredModel := map[string]any{
			"id":     model["id"],
			"object": model["object"],
		}

		// Add created field if it exists
		if created, exists := model["created"]; exists {
			filteredModel["created"] = created
		}

		// Add owned_by field if it exists
		if ownedBy, exists := model["owned_by"]; exists {
			filteredModel["owned_by"] = ownedBy
		}

		filteredModels[i] = filteredModel
	}

	h.WriteModelListResponse(c, h.HandlerType(), gin.H{
		"object": "list",
		"data":   filteredModels,
	})
}

// ChatCompletions handles the /v1/chat/completions endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIAPIHandler) ChatCompletions(c *gin.Context) {
	rawJSON, err := handlers.ReadRequestBody(c)
	// If data retrieval fails, return a 400 Bad Request error.
	if err != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Check if the client requested a streaming response.
	streamResult := ojson.GetBytesNoCopy(rawJSON, "stream")
	stream := streamResult.Type == ojson.True

	modelName := ojson.GetBytes(rawJSON, "model").String()
	if overrideEndpoint, ok := resolveEndpointOverride(modelName, openAIChatEndpoint); ok && overrideEndpoint == openAIResponsesEndpoint {
		originalChat := rawJSON
		if shouldTreatAsResponsesFormat(rawJSON) {
			// Already responses-style payload; no conversion needed.
		} else {
			var errTranslate error
			rawJSON, errTranslate = codexconverter.ConvertOpenAIRequestToCodex(modelName, rawJSON, stream)
			if errTranslate != nil {
				h.WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: clienterror.HTTPStatusFromErrorOr(errTranslate, http.StatusBadRequest), Error: errTranslate})
				return
			}
		}
		stream = ojson.GetBytes(rawJSON, "stream").Bool()
		if stream {
			h.handleStreamingResponseViaResponses(c, rawJSON, originalChat)
		} else {
			h.handleNonStreamingResponseViaResponses(c, rawJSON, originalChat)
		}
		return
	}

	// Some clients send OpenAI Responses-format payloads to /v1/chat/completions.
	// Convert them to Chat Completions so downstream translators preserve tool metadata.
	if shouldTreatAsResponsesFormat(rawJSON) {
		modelName := strings.Clone(ojson.GetBytesNoCopy(rawJSON, "model").String())
		rawJSON, _ = responsesconverter.ConvertOpenAIResponsesRequestToOpenAIChatCompletions(modelName, rawJSON, stream)
		stream = ojson.GetBytesNoCopy(rawJSON, "stream").Bool()
	}

	if stream {
		h.handleStreamingResponse(c, rawJSON)
	} else {
		h.handleNonStreamingResponse(c, rawJSON)
	}

}

// shouldTreatAsResponsesFormat detects OpenAI Responses-style payloads that are
// accidentally sent to the Chat Completions endpoint.
func shouldTreatAsResponsesFormat(rawJSON []byte) bool {
	var fieldsBuf [3]ojson.Result
	fields := ojson.GetManyBytesNoCopy(fieldsBuf[:0], rawJSON, "messages", "input", "instructions")
	if fields[0].Exists() {
		return false
	}
	return fields[1].Exists() || fields[2].Exists()
}

// Completions handles the /v1/completions endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
// This endpoint follows the OpenAI completions API specification.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIAPIHandler) Completions(c *gin.Context) {
	rawJSON, err := handlers.ReadRequestBody(c)
	// If data retrieval fails, return a 400 Bad Request error.
	if err != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Check if the client requested a streaming response.
	streamResult := ojson.GetBytesNoCopy(rawJSON, "stream")
	if streamResult.Type == ojson.True {
		h.handleCompletionsStreamingResponse(c, rawJSON)
	} else {
		h.handleCompletionsNonStreamingResponse(c, rawJSON)
	}

}

// convertCompletionsRequestToChatCompletions converts OpenAI completions API request to chat completions format.
// This allows the completions endpoint to use the existing chat completions infrastructure.
//
// Parameters:
//   - rawJSON: The raw JSON bytes of the completions request
//
// Returns:
//   - []byte: The converted chat completions request
func convertCompletionsRequestToChatCompletions(rawJSON []byte) []byte {
	root := util.ParseGJSONBytesNoCopy(rawJSON)

	// Extract prompt from completions request
	prompt := root.Get("prompt").String()
	if prompt == "" {
		prompt = "Complete this:"
	}

	// Create chat completions structure
	outDoc := ojson.NewDoc([]byte(`{"model":"","messages":[{"role":"user","content":""}]}`))

	// Set model
	if model := root.Get("model"); model.Exists() {
		_ = outDoc.Set("model", model.String())
	}

	// Set the prompt as user message content
	_ = outDoc.Set("messages.0.content", prompt)

	// Copy other parameters from completions to chat completions
	if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
		_ = outDoc.Set("max_tokens", maxTokens.Int())
	}

	if temperature := root.Get("temperature"); temperature.Exists() {
		_ = outDoc.Set("temperature", temperature.Float())
	}

	if topP := root.Get("top_p"); topP.Exists() {
		_ = outDoc.Set("top_p", topP.Float())
	}

	if frequencyPenalty := root.Get("frequency_penalty"); frequencyPenalty.Exists() {
		_ = outDoc.Set("frequency_penalty", frequencyPenalty.Float())
	}

	if presencePenalty := root.Get("presence_penalty"); presencePenalty.Exists() {
		_ = outDoc.Set("presence_penalty", presencePenalty.Float())
	}

	if stop := root.Get("stop"); stop.Exists() {
		_ = outDoc.SetRawBytes("stop", []byte(stop.Raw))
	}

	if stream := root.Get("stream"); stream.Exists() {
		_ = outDoc.Set("stream", stream.Bool())
	}

	if logprobs := root.Get("logprobs"); logprobs.Exists() {
		_ = outDoc.Set("logprobs", logprobs.Bool())
	}

	if topLogprobs := root.Get("top_logprobs"); topLogprobs.Exists() {
		_ = outDoc.Set("top_logprobs", topLogprobs.Int())
	}

	if echo := root.Get("echo"); echo.Exists() {
		_ = outDoc.Set("echo", echo.Bool())
	}

	return outDoc.Bytes()
}

func convertResponsesObjectToChatCompletion(ctx context.Context, modelName string, originalChatJSON, responsesRequestJSON, responsesPayload []byte) []byte {
	if len(responsesPayload) == 0 {
		return nil
	}
	wrapped := wrapResponsesPayloadAsCompleted(responsesPayload)
	if len(wrapped) == 0 {
		return nil
	}
	var param any
	converted := codexconverter.ConvertCodexResponseToOpenAINonStream(ctx, modelName, originalChatJSON, responsesRequestJSON, wrapped, &param)
	if len(converted) == 0 {
		return nil
	}
	return converted
}

func wrapResponsesPayloadAsCompleted(payload []byte) []byte {
	if ojson.GetBytes(payload, "type").Exists() {
		return payload
	}
	if ojson.GetBytes(payload, "object").String() != "response" {
		return payload
	}
	wrapped := `{"type":"response.completed","response":{}}`
	wrapped, _ = ojson.SetRaw(wrapped, "response", string(payload))
	return []byte(wrapped)
}

func writeConvertedResponsesChunk(c *gin.Context, ctx context.Context, modelName string, originalChatJSON, responsesRequestJSON, chunk []byte, param *any) {
	outputs := codexconverter.ConvertCodexResponseToOpenAI(ctx, modelName, originalChatJSON, responsesRequestJSON, chunk, param)
	for _, out := range outputs {
		if len(out) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", out)
	}
}

func (h *OpenAIAPIHandler) forwardResponsesAsChatStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, ctx context.Context, modelName string, originalChatJSON, responsesRequestJSON []byte, param *any) {
	h.ForwardStream(c, flusher, cancel, data, errs, handlers.StreamForwardOptions{
		WriteChunk: func(chunk []byte) {
			outputs := codexconverter.ConvertCodexResponseToOpenAI(ctx, modelName, originalChatJSON, responsesRequestJSON, chunk, param)
			for _, out := range outputs {
				if len(out) == 0 {
					continue
				}
				_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", out)
			}
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			if errMsg == nil {
				return
			}
			errMsg = sanitizeOpenAIErrorMessage(errMsg)
			status := http.StatusInternalServerError
			if errMsg.StatusCode > 0 {
				status = errMsg.StatusCode
			}
			errText := http.StatusText(status)
			if errMsg.Error != nil && errMsg.Error.Error() != "" {
				errText = errMsg.Error.Error()
			}
			body := handlers.BuildErrorResponseBody(status, errText)
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(body))
		},
		WriteDone: func() {
			_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
		},
	})
}

// convertChatCompletionsResponseToCompletions converts chat completions API response back to completions format.
// This ensures the completions endpoint returns data in the expected format.
//
// Parameters:
//   - rawJSON: The raw JSON bytes of the chat completions response
//
// Returns:
//   - []byte: The converted completions response
func convertChatCompletionsResponseToCompletions(rawJSON []byte) []byte {
	root := util.ParseGJSONBytesNoCopy(rawJSON)

	// Base completions response structure
	outDoc := ojson.NewDoc([]byte(`{"id":"","object":"text_completion","created":0,"model":"","choices":[]}`))

	// Copy basic fields
	if id := root.Get("id"); id.Exists() {
		_ = outDoc.Set("id", id.String())
	}

	if created := root.Get("created"); created.Exists() {
		_ = outDoc.Set("created", created.Int())
	}

	if model := root.Get("model"); model.Exists() {
		_ = outDoc.Set("model", model.String())
	}

	if usage := root.Get("usage"); usage.Exists() {
		_ = outDoc.SetRawBytes("usage", []byte(usage.Raw))
	}

	// Convert choices from chat completions to completions format
	var choices []interface{}
	if chatChoices := root.Get("choices"); chatChoices.Exists() && chatChoices.IsArray() {
		chatChoices.ForEach(func(_, choice ojson.Result) bool {
			completionsChoice := map[string]interface{}{
				"index": choice.Get("index").Int(),
			}

			// Extract text content from message.content
			if message := choice.Get("message"); message.Exists() {
				if content := message.Get("content"); content.Exists() {
					completionsChoice["text"] = content.String()
				}
			} else if delta := choice.Get("delta"); delta.Exists() {
				// For streaming responses, use delta.content
				if content := delta.Get("content"); content.Exists() {
					completionsChoice["text"] = content.String()
				}
			}

			// Copy finish_reason
			if finishReason := choice.Get("finish_reason"); finishReason.Exists() {
				completionsChoice["finish_reason"] = finishReason.String()
			}

			// Copy logprobs if present
			if logprobs := choice.Get("logprobs"); logprobs.Exists() {
				completionsChoice["logprobs"] = logprobs.Value()
			}

			choices = append(choices, completionsChoice)
			return true
		})
	}

	if len(choices) > 0 {
		choicesJSON, _ := json.Marshal(choices)
		_ = outDoc.SetRawBytes("choices", choicesJSON)
	}

	return outDoc.Bytes()
}

// convertChatCompletionsStreamChunkToCompletions converts a streaming chat completions chunk to completions format.
// This handles the real-time conversion of streaming response chunks and filters out empty text responses.
//
// Parameters:
//   - chunkData: The raw JSON bytes of a single chat completions stream chunk
//
// Returns:
//   - []byte: The converted completions stream chunk, or nil if should be filtered out
func convertChatCompletionsStreamChunkToCompletions(chunkData []byte) []byte {
	root := util.ParseGJSONBytesNoCopy(chunkData)

	// Check if this chunk has any meaningful content
	hasContent := false
	hasUsage := root.Get("usage").Exists()
	if chatChoices := root.Get("choices"); chatChoices.Exists() && chatChoices.IsArray() {
		chatChoices.ForEach(func(_, choice ojson.Result) bool {
			// Check if delta has content or finish_reason
			if delta := choice.Get("delta"); delta.Exists() {
				if content := delta.Get("content"); content.Exists() && content.String() != "" {
					hasContent = true
					return false // Break out of forEach
				}
			}
			// Also check for finish_reason to ensure we don't skip final chunks
			if finishReason := choice.Get("finish_reason"); finishReason.Exists() && finishReason.String() != "" && finishReason.String() != "null" {
				hasContent = true
				return false // Break out of forEach
			}
			return true
		})
	}

	// If no meaningful content and no usage, return nil to indicate this chunk should be skipped
	if !hasContent && !hasUsage {
		return nil
	}

	// Base completions stream response structure
	outDoc := ojson.NewDoc([]byte(`{"id":"","object":"text_completion","created":0,"model":"","choices":[]}`))

	// Copy basic fields
	if id := root.Get("id"); id.Exists() {
		_ = outDoc.Set("id", id.String())
	}

	if created := root.Get("created"); created.Exists() {
		_ = outDoc.Set("created", created.Int())
	}

	if model := root.Get("model"); model.Exists() {
		_ = outDoc.Set("model", model.String())
	}

	// Convert choices from chat completions delta to completions format
	var choices []interface{}
	if chatChoices := root.Get("choices"); chatChoices.Exists() && chatChoices.IsArray() {
		chatChoices.ForEach(func(_, choice ojson.Result) bool {
			completionsChoice := map[string]interface{}{
				"index": choice.Get("index").Int(),
			}

			// Extract text content from delta.content
			if delta := choice.Get("delta"); delta.Exists() {
				if content := delta.Get("content"); content.Exists() && content.String() != "" {
					completionsChoice["text"] = content.String()
				} else {
					completionsChoice["text"] = ""
				}
			} else {
				completionsChoice["text"] = ""
			}

			// Copy finish_reason
			if finishReason := choice.Get("finish_reason"); finishReason.Exists() && finishReason.String() != "null" {
				completionsChoice["finish_reason"] = finishReason.String()
			}

			// Copy logprobs if present
			if logprobs := choice.Get("logprobs"); logprobs.Exists() {
				completionsChoice["logprobs"] = logprobs.Value()
			}

			choices = append(choices, completionsChoice)
			return true
		})
	}

	if len(choices) > 0 {
		choicesJSON, _ := json.Marshal(choices)
		_ = outDoc.SetRawBytes("choices", choicesJSON)
	}

	// Copy usage if present
	if usage := root.Get("usage"); usage.Exists() {
		_ = outDoc.SetRawBytes("usage", []byte(usage.Raw))
	}

	return outDoc.Bytes()
}

// handleNonStreamingResponse handles non-streaming chat completion responses
// for Gemini models. It selects a client from the pool, sends the request, and
// aggregates the response before sending it back to the client in OpenAI format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAI-compatible request
func (h *OpenAIAPIHandler) handleNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	modelName := strings.Clone(ojson.GetBytesNoCopy(rawJSON, "model").String())
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, h.GetAlt(c))
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, sanitizeOpenAIErrorMessage(errMsg))
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

func (h *OpenAIAPIHandler) handleNonStreamingResponseViaResponses(c *gin.Context, rawJSON []byte, originalChatJSON []byte) {
	c.Header("Content-Type", "application/json")

	modelName := ojson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, OpenaiResponse, modelName, rawJSON, h.GetAlt(c))
	if errMsg != nil {
		h.WriteErrorResponse(c, sanitizeOpenAIErrorMessage(errMsg))
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	converted := convertResponsesObjectToChatCompletion(cliCtx, modelName, originalChatJSON, rawJSON, resp)
	if converted == nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{
			StatusCode: http.StatusInternalServerError,
			Error:      fmt.Errorf("failed to convert response to chat completion format"),
		})
		cliCancel(fmt.Errorf("response conversion failed"))
		return
	}
	_, _ = c.Writer.Write(converted)
	cliCancel()
}

// handleStreamingResponse handles streaming responses for Gemini models.
// It establishes a streaming connection with the backend service and forwards
// the response chunks to the client in real-time using Server-Sent Events.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAI-compatible request
func (h *OpenAIAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
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

	modelName := strings.Clone(ojson.GetBytesNoCopy(rawJSON, "model").String())
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}
	var upstreamHeaders http.Header
	committed := false
	commitSSE := func() {
		if committed {
			return
		}
		setSSEHeaders()
		handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
		committed = true
	}
	writeKeepAlive := func() bool {
		commitSSE()
		_, _ = c.Writer.Write([]byte(": keep-alive\n\n"))
		flusher.Flush()
		return true
	}
	keepAliveC, stopKeepAlive := handlers.NewPreFirstTokenKeepAliveTicker(h.CurrentConfig())
	defer stopKeepAlive()

	dataChan, resolvedHeaders, errChan, okBootstrap := h.ExecuteStreamWithPreFTKeepAlive(
		c, cliCtx, func(err error) { cliCancel(err) }, h.HandlerType(), modelName, rawJSON, h.GetAlt(c), keepAliveC, writeKeepAlive,
	)
	if !okBootstrap {
		return
	}
	upstreamHeaders = resolvedHeaders

	// Peek at the first chunk to determine success or failure before setting headers
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
				pendingErrors := make(chan *interfaces.ErrorMessage, 1)
				pendingErrors <- errMsg
				close(pendingErrors)
				h.handleStreamResult(c, flusher, func(err error) { cliCancel(err) }, make(chan []byte), pendingErrors, false)
				return
			}
			// Upstream failed immediately. Return proper error status and JSON.
			h.WriteErrorResponse(c, sanitizeOpenAIErrorMessage(errMsg))
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				stopKeepAlive()
				if errMsg, hasPendingError := handlers.PendingStreamError(errChan); hasPendingError {
					if committed {
						pendingErrors := make(chan *interfaces.ErrorMessage, 1)
						pendingErrors <- errMsg
						close(pendingErrors)
						h.handleStreamResult(c, flusher, func(err error) { cliCancel(err) }, make(chan []byte), pendingErrors, false)
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
				// Stream closed without data? Send DONE or just headers.
				commitSSE()
				_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
				flusher.Flush()
				cliCancel(nil)
				return
			}

			// Success! Commit to streaming headers.
			stopKeepAlive()
			commitSSE()

			termErr := chunkTerminalError(chunk)
			if termErr != nil {
				h.LoggingAPIResponseError(c, termErr)
			}
			initialHasFinishReason := termErr != nil || chunkHasFinishReason(chunk)
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(chunk))
			flusher.Flush()
			if termErr != nil {
				cliCancel(termErr.Error)
				return
			}

			// Continue streaming the rest
			h.handleStreamResult(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan, initialHasFinishReason)
			return
		case <-keepAliveC:
			writeKeepAlive()
		}
	}
}

func (h *OpenAIAPIHandler) handleStreamingResponseViaResponses(c *gin.Context, rawJSON []byte, originalChatJSON []byte) {
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

	modelName := ojson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	dataChan, upstreamHeaders, errChan := h.ExecuteStreamWithAuthManager(cliCtx, OpenaiResponse, modelName, rawJSON, h.GetAlt(c))
	var param any

	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}

	// Peek for first usable chunk
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case errMsg, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			h.WriteErrorResponse(c, sanitizeOpenAIErrorMessage(errMsg))
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				// Stream closed without data. Surface a buffered pending error
				// before committing SSE headers, so a failed upstream never
				// looks like a successful empty stream.
				if pErr, pending := pendingOpenAIStreamError(errChan); pending {
					h.WriteErrorResponse(c, sanitizeOpenAIErrorMessage(pErr))
					cliCancel(pErr.Error)
					return
				}
				// Clean close. Send DONE or just headers.
				setSSEHeaders()
				handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
				_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
				flusher.Flush()
				cliCancel(nil)
				return
			}

			setSSEHeaders()
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
			writeConvertedResponsesChunk(c, cliCtx, modelName, originalChatJSON, rawJSON, chunk, &param)
			flusher.Flush()

			h.forwardResponsesAsChatStream(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan, cliCtx, modelName, originalChatJSON, rawJSON, &param)
			return
		}
	}
}

// handleCompletionsNonStreamingResponse handles non-streaming completions responses.
// It converts completions request to chat completions format, sends to backend,
// then converts the response back to completions format before sending to client.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAI-compatible completions request
func (h *OpenAIAPIHandler) handleCompletionsNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	// Convert completions request to chat completions format
	chatCompletionsJSON := convertCompletionsRequestToChatCompletions(rawJSON)

	modelName := strings.Clone(ojson.GetBytesNoCopy(chatCompletionsJSON, "model").String())
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, chatCompletionsJSON, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, sanitizeOpenAIErrorMessage(errMsg))
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	completionsResp := convertChatCompletionsResponseToCompletions(resp)
	_, _ = c.Writer.Write(completionsResp)
	cliCancel()
}

// handleCompletionsStreamingResponse handles streaming completions responses.
// It converts completions request to chat completions format, streams from backend,
// then converts each response chunk back to completions format before sending to client.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAI-compatible completions request
func (h *OpenAIAPIHandler) handleCompletionsStreamingResponse(c *gin.Context, rawJSON []byte) {
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

	// Convert completions request to chat completions format
	chatCompletionsJSON := convertCompletionsRequestToChatCompletions(rawJSON)

	modelName := strings.Clone(ojson.GetBytesNoCopy(chatCompletionsJSON, "model").String())
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}
	var upstreamHeaders http.Header
	committed := false
	commitSSE := func() {
		if committed {
			return
		}
		setSSEHeaders()
		handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
		committed = true
	}
	writeKeepAlive := func() bool {
		commitSSE()
		_, _ = c.Writer.Write([]byte(": keep-alive\n\n"))
		flusher.Flush()
		return true
	}
	keepAliveC, stopKeepAlive := handlers.NewPreFirstTokenKeepAliveTicker(h.CurrentConfig())
	defer stopKeepAlive()

	dataChan, resolvedHeaders, errChan, okBootstrap := h.ExecuteStreamWithPreFTKeepAlive(
		c, cliCtx, func(err error) { cliCancel(err) }, h.HandlerType(), modelName, chatCompletionsJSON, "", keepAliveC, writeKeepAlive,
	)
	if !okBootstrap {
		return
	}
	upstreamHeaders = resolvedHeaders

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
			errMsg = sanitizeOpenAIErrorMessage(errMsg)
			if committed {
				stopKeepAlive()
				pendingErrors := make(chan *interfaces.ErrorMessage, 1)
				pendingErrors <- errMsg
				close(pendingErrors)
				h.handleStreamResult(c, flusher, func(err error) { cliCancel(err) }, make(chan []byte), pendingErrors, false)
				return
			}
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
				if errMsg, hasPendingError := handlers.PendingStreamError(errChan); hasPendingError {
					if committed {
						pendingErrors := make(chan *interfaces.ErrorMessage, 1)
						pendingErrors <- errMsg
						close(pendingErrors)
						h.handleStreamResult(c, flusher, func(err error) { cliCancel(err) }, make(chan []byte), pendingErrors, false)
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
				commitSSE()
				_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
				flusher.Flush()
				cliCancel(nil)
				return
			}

			// Success! Set headers.
			stopKeepAlive()
			commitSSE()

			if termErr := chunkTerminalError(chunk); termErr != nil {
				h.LoggingAPIResponseError(c, termErr)
				_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(chunk))
				flusher.Flush()
				cliCancel(termErr.Error)
				return
			}

			// Write the first chunk
			converted := convertChatCompletionsStreamChunkToCompletions(chunk)
			var initialHasFinishReason bool
			if converted != nil {
				initialHasFinishReason = chunkHasFinishReason(converted)
				_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(converted))
				flusher.Flush()
			}

			done := make(chan struct{})
			var doneOnce sync.Once
			stop := func() { doneOnce.Do(func() { close(done) }) }

			convertedChan := make(chan []byte)
			go func() {
				defer close(convertedChan)
				for {
					select {
					case <-done:
						return
					case chunk, ok := <-dataChan:
						if !ok {
							return
						}
						converted := convertChatCompletionsStreamChunkToCompletions(chunk)
						if converted == nil {
							continue
						}
						select {
						case <-done:
							return
						case convertedChan <- converted:
						}
					}
				}
			}()

			h.handleStreamResult(c, flusher, func(err error) {
				stop()
				cliCancel(err)
			}, convertedChan, errChan, initialHasFinishReason)
			return
		case <-keepAliveC:
			writeKeepAlive()
		}
	}
}
func chunkTerminalError(chunk []byte) *interfaces.ErrorMessage {
	chunk = bytes.TrimSpace(chunk)
	if bytes.HasPrefix(chunk, []byte("data:")) {
		chunk = bytes.TrimSpace(chunk[5:])
	}
	var fieldsBuf [2]ojson.Result
	fields := ojson.GetManyBytesNoCopy(fieldsBuf[:0], chunk, "choices", "error")
	if fields[0].Exists() || !fields[1].IsObject() {
		return nil
	}
	msg := fields[1].Get("message").String()
	if msg == "" {
		msg = string(chunk)
	}
	return &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      fmt.Errorf("%s", msg),
	}
}

func chunkHasFinishReason(chunk []byte) bool {
	chunk = bytes.TrimSpace(chunk)
	if bytes.HasPrefix(chunk, []byte("data:")) {
		chunk = bytes.TrimSpace(chunk[5:])
	}
	if chunkTerminalError(chunk) != nil {
		return true
	}
	for _, choice := range ojson.GetBytesNoCopy(chunk, "choices").Array() {
		if reason := choice.Get("finish_reason"); reason.Exists() && reason.Type != ojson.Null && reason.String() != "" {
			return true
		}
	}
	return false
}

func (h *OpenAIAPIHandler) handleStreamResult(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, initialHasFinishReason bool) {
	sawFinishReason := initialHasFinishReason
	loggedTerminalErr := false
	var terminalChunkErr *interfaces.ErrorMessage
	h.ForwardStream(c, flusher, cancel, data, errs, handlers.StreamForwardOptions{
		WriteChunk: func(chunk []byte) {
			if termErr := chunkTerminalError(chunk); termErr != nil {
				terminalChunkErr = termErr
				sawFinishReason = true
				if !loggedTerminalErr {
					h.LoggingAPIResponseError(c, termErr)
					loggedTerminalErr = true
				}
			} else if !sawFinishReason && chunkHasFinishReason(chunk) {
				sawFinishReason = true
			}
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(chunk))
		},
		ChunkError: func() *interfaces.ErrorMessage {
			if terminalChunkErr != nil && !loggedTerminalErr {
				h.LoggingAPIResponseError(c, terminalChunkErr)
				loggedTerminalErr = true
			}
			return terminalChunkErr
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			if errMsg == nil {
				return
			}
			if !loggedTerminalErr {
				h.LoggingAPIResponseError(c, errMsg)
				loggedTerminalErr = true
			}
			if terminalChunkErr != nil {
				return
			}
			errMsg = sanitizeOpenAIErrorMessage(errMsg)
			status := http.StatusInternalServerError
			if errMsg.StatusCode > 0 {
				status = errMsg.StatusCode
			}
			errText := http.StatusText(status)
			if errMsg.Error != nil && errMsg.Error.Error() != "" {
				errText = errMsg.Error.Error()
			}
			body := handlers.BuildErrorResponseBody(status, errText)
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(body))
		},
		CloseError: func() *interfaces.ErrorMessage {
			if sawFinishReason {
				return nil
			}
			return &interfaces.ErrorMessage{
				StatusCode: http.StatusBadGateway,
				Error:      fmt.Errorf("upstream stream closed before any chunk carried finish_reason"),
			}
		},
		WriteDone: func() {
			_, _ = fmt.Fprint(c.Writer, "data: [DONE]\n\n")
		},
	})
}

// pendingOpenAIStreamError returns an immediately available non-nil stream error
// buffered on errChan. It mirrors pendingClaudeStreamError in the Claude handler:
// the initial peek consumes a queued upstream failure before committing SSE
// headers so a failed stream never looks like a successful empty stream.
func pendingOpenAIStreamError(errs <-chan *interfaces.ErrorMessage) (*interfaces.ErrorMessage, bool) {
	if errs == nil {
		return nil, false
	}
	select {
	case errMsg, ok := <-errs:
		if !ok || errMsg == nil {
			return nil, false
		}
		return errMsg, true
	default:
		return nil, false
	}
}

// sanitizeOpenAIErrorMessage is the trust-boundary sanitizer for the OpenAI
// pre-output sinks. It preserves a DirectResponse only when it is explicitly
// trusted (local plugin/interceptor); every other error path sanitizes
// strictly: forces a valid status, clears Body, forces DirectResponse=false,
// and redacts credential material from the error text. It returns nil for a
// nil input.
func sanitizeOpenAIErrorMessage(errMsg *interfaces.ErrorMessage) *interfaces.ErrorMessage {
	if errMsg != nil && errMsg.DirectResponse && errMsg.TrustedDirectResponse {
		return errMsg
	}
	return sanitizeOpenAIStrictErrorMessage(errMsg)
}

func sanitizeOpenAIStrictErrorMessage(errMsg *interfaces.ErrorMessage) *interfaces.ErrorMessage {
	if errMsg == nil {
		return nil
	}
	status := errMsg.StatusCode
	if status < http.StatusBadRequest || status > 599 {
		status = http.StatusInternalServerError
	}
	safe := *errMsg
	safe.StatusCode = status
	safe.DirectResponse = false
	safe.Body = nil
	if errMsg.Error != nil {
		safe.Error = &openAIStreamSanitizedError{
			message:      openAIStreamErrorText(errMsg.Error.Error(), status),
			safeHeaders:  coreauth.SafeResponseHeaders(errMsg.Error),
			cause:        errMsg.Error,
			terminalAuth: coreauth.IsTerminalAuthError(errMsg.Error),
		}
	}
	return &safe
}

type openAIStreamSanitizedError struct {
	message      string
	safeHeaders  http.Header
	cause        error
	terminalAuth bool
}

func (e *openAIStreamSanitizedError) IsTerminalAuth() bool {
	if e == nil {
		return false
	}
	return e.terminalAuth
}

func (e *openAIStreamSanitizedError) Error() string { return e.message }

func (e *openAIStreamSanitizedError) SafeResponseHeaders() http.Header {
	if e == nil {
		return nil
	}
	if e.safeHeaders != nil {
		return e.safeHeaders.Clone()
	}
	if e.cause != nil {
		return coreauth.SafeResponseHeaders(e.cause)
	}
	return nil
}

// openAIStreamErrorText produces a client-safe error message. JSON error bodies
// are preserved field-by-field with sanitization; free-form text is kept with
// credential material redacted.
func openAIStreamErrorText(text string, status int) string {
	if t := strings.TrimSpace(text); t != "" && json.Valid([]byte(t)) {
		return sanitizeOpenAIStreamJSON(t, status)
	}
	fallback := http.StatusText(status)
	if strings.TrimSpace(text) == "" {
		return fallback
	}
	return redactOpenAIStreamErrorText(strings.TrimSpace(text))
}

func sanitizeOpenAIStreamJSON(text string, status int) string {
	root := ojson.Parse(text)
	errorNode := root.Get("error")
	if !errorNode.Exists() || !errorNode.IsObject() {
		errorNode = root.Get("response.error")
	}
	if errorNode.Exists() && errorNode.IsObject() {
		safe := []byte(`{"error":{}}`)
		copied := false
		for _, field := range []string{"type", "code", "message", "param"} {
			value := errorNode.Get(field)
			if !value.Exists() || value.Type == ojson.Null {
				continue
			}
			limit := openAIStreamErrorFieldLimit
			if field == "message" {
				limit = openAIStreamErrorMessageLimit
			}
			safe, _ = ojson.SetBytes(safe, "error."+field, truncateOpenAIStreamErrorText(redactOpenAIStreamErrorText(value.String()), limit))
			copied = true
		}
		if copied {
			return string(safe)
		}
	}

	safe := []byte(`{"type":"error"}`)
	copied := false
	for _, field := range []string{"code", "message", "param"} {
		value := root.Get(field)
		if !value.Exists() || value.Type == ojson.Null {
			continue
		}
		limit := openAIStreamErrorFieldLimit
		if field == "message" {
			limit = openAIStreamErrorMessageLimit
		}
		safe, _ = ojson.SetBytes(safe, field, truncateOpenAIStreamErrorText(redactOpenAIStreamErrorText(value.String()), limit))
		copied = true
	}
	if copied {
		return string(safe)
	}
	return http.StatusText(status)
}

const (
	openAIStreamErrorMessageLimit = 2048
	openAIStreamErrorFieldLimit   = 256
)

var (
	// openAIStreamKeyPattern matches a sensitive key name and its separator
	// (= or :), preceded by a boundary and optional quote/escape syntax.
	// Group 1 is the leading boundary/quote syntax, group 2 the key, group 3
	// the trailing quote/space syntax plus separator.
	openAIStreamKeyPattern = regexp.MustCompile(`(?i)((?:^|[^A-Za-z0-9_])(?:\\*["']?)?)(api[_-]?key|apikey|access[_-]?key[_-]?id|aws[_-]?access[_-]?key[_-]?id|api[_-]?key[_-]?id|access[_-]?token|authorization|token|secret|credential|aws[_-]?credential|refresh[_-]?token|client[_-]?secret|(?:[A-Za-z0-9]+(?:[_-][A-Za-z0-9]+)*)[_-](?:key|token|secret|credential|key[_-]?id)|(?-i:[A-Za-z0-9]*(?:[a-z0-9]|_[a-z0-9]|-[a-z0-9])(?:Key|Token|Secret|Credential|KeyId|Key_Id|Key-Id)))((?:\\*["']?)?\s*[=:])`)
	// openAIStreamSpaceAPIKeyPattern matches the "api key:" spelling with a
	// space between api and key, in header/assignment contexts.
	openAIStreamSpaceAPIKeyPattern = regexp.MustCompile(`(?i)((?:^|[^A-Za-z0-9_]))(api[ _]key)(["']?\s*[=:])`)
	// openAIStreamBareKeyDenyPattern marks key names that merely mention a
	// credential kind without being a credential themselves.
	openAIStreamBareKeyDenyPattern = regexp.MustCompile(`(?i)^(?:not|non|no|count|counter|key[_-]?count|token[_-]?count)(?:[_-]|$)`)
	// openAIStreamAuthSchemePattern detects a Bearer/Basic scheme at the
	// start of a credential value so the scheme can be preserved.
	openAIStreamAuthSchemePattern = regexp.MustCompile(`(?i)^(Bearer|Basic)\s+`)
	// openAIStreamAuthPattern redacts standalone Bearer/Basic credentials
	// that appear outside key/value contexts.
	openAIStreamAuthPattern = regexp.MustCompile(`(?i)(\b(?:Bearer|Basic)\s+)([-A-Za-z0-9._~+/=]+)`)
)

func truncateOpenAIStreamErrorText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func redactOpenAIStreamErrorText(text string) string {
	text = redactOpenAIStreamKeyValues(text)
	return openAIStreamAuthPattern.ReplaceAllString(text, "${1}[REDACTED]")
}

// redactOpenAIStreamKeyValues locates sensitive key/value pairs and replaces
// their credential with [REDACTED], preserving quote/escape syntax and, for
// Bearer/Basic values, the scheme. Compound keys always redact; bare
// token/secret/credential keys redact only in explicit JSON, assignment, or
// line-start header contexts.
func redactOpenAIStreamKeyValues(text string) string {
	type keyMatch struct {
		loc []int
		key string
	}
	var matches []keyMatch
	for _, loc := range openAIStreamKeyPattern.FindAllStringSubmatchIndex(text, -1) {
		matches = append(matches, keyMatch{loc: loc, key: text[loc[4]:loc[5]]})
	}
	for _, loc := range openAIStreamSpaceAPIKeyPattern.FindAllStringSubmatchIndex(text, -1) {
		matches = append(matches, keyMatch{loc: loc, key: "api key"})
	}
	if len(matches) == 0 {
		return text
	}
	sort.Slice(matches, func(a, b int) bool { return matches[a].loc[0] < matches[b].loc[0] })
	var b strings.Builder
	b.Grow(len(text) + 16*len(matches))
	last := 0
	for _, m := range matches {
		loc := m.loc
		if loc[0] < last {
			continue
		}
		key := strings.ToLower(m.key)
		if openAIStreamBareKeyDenyPattern.MatchString(key) {
			continue
		}
		if key == "token" || key == "secret" || key == "credential" {
			if !openAIStreamBareKeyContextOK(text, loc) {
				continue
			}
		}
		sepEnd := loc[1]
		valueEnd, redactStart, redactEnd := openAIStreamValueBounds(text, sepEnd, key == "authorization" || key == "api key")
		b.WriteString(text[last:sepEnd])
		b.WriteString(text[sepEnd:redactStart])
		if redactEnd > redactStart {
			b.WriteString("[REDACTED]")
		}
		b.WriteString(text[redactEnd:valueEnd])
		last = valueEnd
	}
	b.WriteString(text[last:])
	return b.String()
}

// openAIStreamBareKeyContextOK applies the deterministic context rule for bare
// token/secret/credential keys: they redact only as explicit JSON fields, '='
// assignments, or line-start headers.
func openAIStreamBareKeyContextOK(text string, loc []int) bool {
	if loc[4] == 0 || text[loc[4]-1] == '\n' {
		return true
	}
	if b := text[loc[4]-1]; b == '"' || b == '\\' || b == '\'' {
		return true
	}
	for i := loc[6]; i < loc[7]; i++ {
		if text[i] == '=' {
			return true
		}
	}
	return false
}

// openAIStreamValueBounds returns the region [redactStart, redactEnd) to
// replace with [REDACTED] for the value starting at start, and the full value
// span [start, valueEnd) the redaction consumes. Quoted values keep their
// opening/closing quote syntax; unquoted generic values stop at whitespace;
// Bearer/Basic credentials span the space between scheme and token;
// authorization/api key values consume the whole multi-part value.
func openAIStreamValueBounds(text string, start int, isAuth bool) (valueEnd, redactStart, redactEnd int) {
	n := len(text)
	i := start
	for i < n && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	if i >= n {
		return start, start, start
	}
	backslashes := 0
	j := i
	for j < n && text[j] == '\\' {
		backslashes++
		j++
	}
	if j < n && (text[j] == '"' || text[j] == '\'') {
		quote := text[j]
		openEnd := j + 1
		closeStart, closeEnd := openAIStreamQuoteClose(text, openEnd, quote, backslashes)
		if schemeEnd := openAIStreamAuthSchemeEnd(text, openEnd); schemeEnd >= 0 && schemeEnd <= closeStart {
			return closeEnd, schemeEnd, closeStart
		}
		return closeEnd, openEnd, closeStart
	}
	end := i
	for end < n {
		c := text[end]
		if c == '\\' {
			if end+1 >= n {
				end = n
				break
			}
			end += 2
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '}' || c == ')' || c == ']' || c == ',' || c == ';' || c == '\'' || c == '"' {
			break
		}
		end++
	}
	if schemeEnd := openAIStreamAuthSchemeEnd(text, i); schemeEnd >= 0 {
		credEnd := schemeEnd
		for credEnd < n {
			c := text[credEnd]
			if c == '\\' {
				if credEnd+1 >= n {
					credEnd = n
					break
				}
				credEnd += 2
				continue
			}
			if c == '\n' || c == '\r' || c == '}' || c == ')' || c == ']' || c == ',' || c == ';' || c == '"' {
				break
			}
			credEnd++
		}
		return credEnd, schemeEnd, credEnd
	}
	if isAuth {
		authEnd := i
		for authEnd < n {
			c := text[authEnd]
			if c == '\\' {
				if authEnd+1 >= n {
					authEnd = n
					break
				}
				authEnd += 2
				continue
			}
			if c == '\n' || c == '\r' || c == '}' || c == ')' || c == ']' {
				break
			}
			authEnd++
		}
		return authEnd, i, authEnd
	}
	return end, i, end
}

// openAIStreamQuoteClose finds the closing quote syntax of a quoted value
// starting after the opening quote at openEnd.
func openAIStreamQuoteClose(text string, openEnd int, quote byte, openRun int) (closeSyntaxStart, closeEnd int) {
	n := len(text)
	if openRun == 0 {
		k := openEnd
		for k < n {
			if text[k] == '\\' {
				if k+1 >= n {
					return n, n
				}
				k += 2
				continue
			}
			if text[k] == quote {
				return k, k + 1
			}
			k++
		}
		return n, n
	}
	k := openEnd
	for k < n {
		if text[k] == '\\' {
			r := 0
			j := k
			for j < n && text[j] == '\\' {
				r++
				j++
			}
			if j < n && text[j] == quote {
				if r == openRun {
					return j - openRun, j + 1
				}
				if r > openRun {
					k = j + 1
					continue
				}
				return j - r, j + 1
			}
			k = j
			continue
		}
		if text[k] == quote {
			return k, k + 1
		}
		k++
	}
	return n, n
}

// openAIStreamAuthSchemeEnd reports the position just after a Bearer/Basic
// scheme word plus following whitespace at start, or -1 when no such scheme is
// present.
func openAIStreamAuthSchemeEnd(text string, start int) int {
	i := start
	n := len(text)
	for i < n && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	m := openAIStreamAuthSchemePattern.FindStringSubmatchIndex(text[i:])
	if m == nil {
		return -1
	}
	return i + m[1]
}
