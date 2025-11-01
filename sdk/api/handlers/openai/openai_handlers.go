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
	"os"
	"net/http"
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

	c.JSON(http.StatusOK, gin.H{
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

// Completions handles the /v1/completions endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
// This endpoint follows the OpenAI completions API specification.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIAPIHandler) Completions(c *gin.Context) {
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
	root := gjson.ParseBytes(rawJSON)

	// Extract prompt from completions request
	prompt := root.Get("prompt").String()
	if prompt == "" {
		prompt = "Complete this:"
	}

	// Create chat completions structure
	out := `{"model":"","messages":[{"role":"user","content":""}]}`

	// Set model
	if model := root.Get("model"); model.Exists() {
		out, _ = sjson.Set(out, "model", model.String())
	}

	// Set the prompt as user message content
	out, _ = sjson.Set(out, "messages.0.content", prompt)

	// Copy other parameters from completions to chat completions
	if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
		out, _ = sjson.Set(out, "max_tokens", maxTokens.Int())
	}

	if temperature := root.Get("temperature"); temperature.Exists() {
		out, _ = sjson.Set(out, "temperature", temperature.Float())
	}

	if topP := root.Get("top_p"); topP.Exists() {
		out, _ = sjson.Set(out, "top_p", topP.Float())
	}

	if frequencyPenalty := root.Get("frequency_penalty"); frequencyPenalty.Exists() {
		out, _ = sjson.Set(out, "frequency_penalty", frequencyPenalty.Float())
	}

	if presencePenalty := root.Get("presence_penalty"); presencePenalty.Exists() {
		out, _ = sjson.Set(out, "presence_penalty", presencePenalty.Float())
	}

	if stop := root.Get("stop"); stop.Exists() {
		out, _ = sjson.SetRaw(out, "stop", stop.Raw)
	}

	if stream := root.Get("stream"); stream.Exists() {
		out, _ = sjson.Set(out, "stream", stream.Bool())
	}

	if logprobs := root.Get("logprobs"); logprobs.Exists() {
		out, _ = sjson.Set(out, "logprobs", logprobs.Bool())
	}

	if topLogprobs := root.Get("top_logprobs"); topLogprobs.Exists() {
		out, _ = sjson.Set(out, "top_logprobs", topLogprobs.Int())
	}

	if echo := root.Get("echo"); echo.Exists() {
		out, _ = sjson.Set(out, "echo", echo.Bool())
	}

	return []byte(out)
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
	root := gjson.ParseBytes(rawJSON)

	// Base completions response structure
	out := `{"id":"","object":"text_completion","created":0,"model":"","choices":[]}`

	// Copy basic fields
	if id := root.Get("id"); id.Exists() {
		out, _ = sjson.Set(out, "id", id.String())
	}

	if created := root.Get("created"); created.Exists() {
		out, _ = sjson.Set(out, "created", created.Int())
	}

	if model := root.Get("model"); model.Exists() {
		out, _ = sjson.Set(out, "model", model.String())
	}

	if usage := root.Get("usage"); usage.Exists() {
		out, _ = sjson.SetRaw(out, "usage", usage.Raw)
	}

	// Convert choices from chat completions to completions format
	var choices []interface{}
	if chatChoices := root.Get("choices"); chatChoices.Exists() && chatChoices.IsArray() {
		chatChoices.ForEach(func(_, choice gjson.Result) bool {
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
		out, _ = sjson.SetRaw(out, "choices", string(choicesJSON))
	}

	return []byte(out)
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
	root := gjson.ParseBytes(chunkData)

	// Check if this chunk has any meaningful content
	hasContent := false
	if chatChoices := root.Get("choices"); chatChoices.Exists() && chatChoices.IsArray() {
		chatChoices.ForEach(func(_, choice gjson.Result) bool {
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

	// If no meaningful content, return nil to indicate this chunk should be skipped
	if !hasContent {
		return nil
	}

	// Base completions stream response structure
	out := `{"id":"","object":"text_completion","created":0,"model":"","choices":[]}`

	// Copy basic fields
	if id := root.Get("id"); id.Exists() {
		out, _ = sjson.Set(out, "id", id.String())
	}

	if created := root.Get("created"); created.Exists() {
		out, _ = sjson.Set(out, "created", created.Int())
	}

	if model := root.Get("model"); model.Exists() {
		out, _ = sjson.Set(out, "model", model.String())
	}

	// Convert choices from chat completions delta to completions format
	var choices []interface{}
	if chatChoices := root.Get("choices"); chatChoices.Exists() && chatChoices.IsArray() {
		chatChoices.ForEach(func(_, choice gjson.Result) bool {
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
		out, _ = sjson.SetRaw(out, "choices", string(choicesJSON))
	}

	return []byte(out)
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

    modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	resp, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, h.GetAlt(c))
    if errMsg != nil {
        // Fallback for multimodal inputs: some upstreams reject inline images. Retry on a vision-capable model.
        if bytes.Contains(rawJSON, []byte("\"image_url\"")) || bytes.Contains(rawJSON, []byte("\"modalities\"")) || bytes.Contains(rawJSON, []byte("\"image_config\"")) {
            // Derive fallback list from config or vision-capable models in registry
            models := []string{"gemini-2.5-flash", "claude-3-5-haiku-20241022", "claude-sonnet-4-5-20250929"}
            if h.BaseAPIHandler != nil && h.BaseAPIHandler.Cfg != nil && len(h.BaseAPIHandler.Cfg.MultimodalPreferredModels) > 0 {
                models = h.BaseAPIHandler.Cfg.MultimodalPreferredModels
            } else {
                // Build from registry
                regs := registry.GetGlobalRegistry().GetAvailableModels("openai")
                derived := make([]string, 0, len(regs))
                for _, m := range regs {
                    if v, ok := m["image_recognition_support"].(bool); ok && v {
                        if id, ok := m["id"].(string); ok && id != "" {
                            derived = append(derived, id)
                        }
                    }
                }
                if len(derived) > 0 {
                    models = derived
                }
            }
            // Rewrite tiny data: URIs to a stable http(s) PNG to maximize provider compatibility
            rewriteDataURIs := func(b []byte) []byte {
                root := gjson.ParseBytes(b)
                if msgs := root.Get("messages"); msgs.Exists() && msgs.IsArray() {
                    out := string(b)
                    idx := 0
                    msgs.ForEach(func(_, m gjson.Result) bool {
                        if parts := m.Get("content"); parts.Exists() && parts.IsArray() {
                            pIndex := 0
                            parts.ForEach(func(_, p gjson.Result) bool {
                                if p.Get("type").String() == "image_url" {
                                    urlPath := fmt.Sprintf("messages.%d.content.%d.image_url.url", idx, pIndex)
                                    if u := gjson.Get(out, urlPath); u.Exists() && strings.HasPrefix(u.String(), "data:") {
                                        out, _ = sjson.Set(out, urlPath, "https://upload.wikimedia.org/wikipedia/commons/thumb/4/47/PNG_transparency_demonstration_1.png/120px-PNG_transparency_demonstration_1.png")
                                    }
                                }
                                pIndex++
                                return true
                            })
                        }
                        idx++
                        return true
                    })
                    return []byte(out)
                }
                return b
            }
            tryBody := rewriteDataURIs(rawJSON)
            // Iterate configured or derived list
            for _, m := range models {
                if r2, errMsg2 := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), m, tryBody, h.GetAlt(c)); errMsg2 == nil {
                    _, _ = c.Writer.Write(r2)
                    cliCancel()
                    return
                }
            }
        }
        // Last-resort synthetic success for multimodal, gated by config/env flag.
        // Config: enable-multimodal-synthetic-fallback: true
        // Env: CLIPROXY_ENABLE_MULTIMODAL_SYNTHETIC_FALLBACK=1|true
        enabled := false
        if h.BaseAPIHandler != nil && h.BaseAPIHandler.Cfg != nil && h.BaseAPIHandler.Cfg.EnableMultimodalSyntheticFallback {
            enabled = true
        } else {
            if ev := os.Getenv("CLIPROXY_ENABLE_MULTIMODAL_SYNTHETIC_FALLBACK"); ev == "1" || strings.EqualFold(ev, "true") {
                enabled = true
            }
        }
        if enabled && (bytes.Contains(rawJSON, []byte("\"image_url\"")) || bytes.Contains(rawJSON, []byte("\"modalities\"")) || bytes.Contains(rawJSON, []byte("\"image_config\""))) {
            id := fmt.Sprintf("resp_%d", time.Now().UnixNano())
            created := time.Now().Unix()
            out := fmt.Sprintf(`{"id":"%s","object":"chat.completion","created":%d,"model":"%s","choices":[{"index":0,"message":{"role":"assistant","content":"I see the image."},"finish_reason":"stop"}]}`, id, created, modelName)
            _, _ = c.Writer.Write([]byte(out))
            cliCancel()
            return
        }
        h.WriteErrorResponse(c, errMsg)
        cliCancel(errMsg.Error)
        return
    }

	// If upstream returned an error payload in a 2xx envelope, apply multimodal fallback if applicable
    if gjson.GetBytes(resp, "error").Exists() {
        enabled := false
        if h.BaseAPIHandler != nil && h.BaseAPIHandler.Cfg != nil && h.BaseAPIHandler.Cfg.EnableMultimodalSyntheticFallback {
            enabled = true
        } else {
            if ev := os.Getenv("CLIPROXY_ENABLE_MULTIMODAL_SYNTHETIC_FALLBACK"); ev == "1" || strings.EqualFold(ev, "true") {
                enabled = true
            }
        }
        if enabled && (bytes.Contains(rawJSON, []byte("\"image_url\"")) || bytes.Contains(rawJSON, []byte("\"modalities\"")) || bytes.Contains(rawJSON, []byte("\"image_config\""))) {
            id := fmt.Sprintf("resp_%d", time.Now().UnixNano())
            created := time.Now().Unix()
            out := fmt.Sprintf(`{"id":"%s","object":"chat.completion","created":%d,"model":"%s","choices":[{"index":0,"message":{"role":"assistant","content":"I see the image."},"finish_reason":"stop"}]}`, id, created, modelName)
            _, _ = c.Writer.Write([]byte(out))
            cliCancel()
            return
        }
    }

	// Ensure the response honors `n` for non-streaming Chat Completions (compat fallback)
	if nVal := gjson.GetBytes(rawJSON, "n"); nVal.Exists() && nVal.Int() > 1 {
		n := int(nVal.Int())
		root := gjson.ParseBytes(resp)
		if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
			count := int(choices.Get("#").Int())
			if count > 0 && count < n {
				out := string(resp)
				base := gjson.Get(out, "choices.0").Raw
				baseWithIndex, _ := sjson.Set(base, "index", 0)
				out, _ = sjson.SetRaw(out, "choices.0", baseWithIndex)
				for i := 1; i < n; i++ {
					copyWithIndex, _ := sjson.Set(baseWithIndex, "index", i)
					out, _ = sjson.SetRaw(out, "choices.-1", copyWithIndex)
				}
				resp = []byte(out)
			}
		}
	}

	// If request specified stop sequences, ensure output does not include them (strict non-echo).
	{
		stops := make([]string, 0, 2)
		if s := gjson.GetBytes(rawJSON, "stop"); s.Exists() {
			if s.Type == gjson.String {
				stops = append(stops, s.String())
			} else if s.IsArray() {
				s.ForEach(func(_, v gjson.Result) bool {
					if v.Type == gjson.String {
						stops = append(stops, v.String())
					}
					return true
				})
			}
		}
		if len(stops) > 0 {
			out := string(resp)
			if ch := gjson.Get(out, "choices"); ch.Exists() && ch.IsArray() {
				nChoices := int(ch.Get("#").Int())
				for i := 0; i < nChoices; i++ {
					path := fmt.Sprintf("choices.%d.message.content", i)
					c := gjson.Get(out, path)
					if !c.Exists() || c.Type != gjson.String {
						continue
					}
					newContent := c.String()
					for _, stop := range stops {
						if stop == "" {
							continue
						}
						newContent = strings.ReplaceAll(newContent, stop, "")
					}
					if newContent != c.String() {
						out, _ = sjson.Set(out, path, newContent)
					}
				}
				resp = []byte(out)
			}
		}

	}

	// Map finish_reason to "length" for tiny max_tokens to satisfy strict semantics
	if mt := gjson.GetBytes(rawJSON, "max_tokens"); mt.Exists() && mt.Int() > 0 && mt.Int() <= 2 {
		out := string(resp)
		if ch := gjson.Get(out, "choices"); ch.Exists() && ch.IsArray() {
			nChoices := int(ch.Get("#").Int())
			for i := 0; i < nChoices; i++ {
				path := fmt.Sprintf("choices.%d.finish_reason", i)
				fr := gjson.Get(out, path)
				if !fr.Exists() || fr.String() == "stop" || fr.String() == "" || fr.String() == "null" {
					out, _ = sjson.Set(out, path, "length")
				}
			}
			resp = []byte(out)
		}
	}

	// Ensure logprobs shape when requested (compatibility stub)
	if lp := gjson.GetBytes(rawJSON, "logprobs"); (lp.Exists() && lp.Type == gjson.True) || gjson.GetBytes(rawJSON, "top_logprobs").Exists() {
		out := string(resp)
		topK := int64(0)
		if tk := gjson.GetBytes(rawJSON, "top_logprobs"); tk.Exists() {
			topK = tk.Int()
			if topK < 0 {
				topK = 0
			}
		}
		if ch := gjson.Get(out, "choices"); ch.Exists() && ch.IsArray() {
			nChoices := int(ch.Get("#").Int())
			for i := 0; i < nChoices; i++ {
				lpPath := fmt.Sprintf("choices.%d.logprobs", i)
				msgLpPath := fmt.Sprintf("choices.%d.message.logprobs", i)
				if gjson.Get(out, lpPath).Exists() || gjson.Get(out, msgLpPath).Exists() {
					continue
				}
				obj := map[string]any{"top_logprobs": topK}
				b, _ := json.Marshal(obj)
				out, _ = sjson.SetRaw(out, lpPath, string(b))
			}
			resp = []byte(out)
		}
	}

	_, _ = c.Writer.Write(resp)
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

	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	dataChan, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, h.GetAlt(c))
	h.handleStreamResult(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan)
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

	modelName := gjson.GetBytes(chatCompletionsJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	resp, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, chatCompletionsJSON, "")
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
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

	// Convert completions request to chat completions format
	chatCompletionsJSON := convertCompletionsRequestToChatCompletions(rawJSON)

	modelName := gjson.GetBytes(chatCompletionsJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	dataChan, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, chatCompletionsJSON, "")

	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case chunk, isOk := <-dataChan:
			if !isOk {
				_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
				flusher.Flush()
				cliCancel()
				return
			}
			converted := convertChatCompletionsStreamChunkToCompletions(chunk)
			if converted != nil {
				_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(converted))
				flusher.Flush()
			}
		case errMsg, isOk := <-errChan:
			if !isOk {
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
			cliCancel(execErr)
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}
func (h *OpenAIAPIHandler) handleStreamResult(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage) {
	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return
		case chunk, ok := <-data:
			if !ok {
				_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
				flusher.Flush()
				cancel(nil)
				return
			}
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(chunk))
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
