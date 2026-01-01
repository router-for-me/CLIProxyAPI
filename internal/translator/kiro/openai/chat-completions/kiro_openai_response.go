// Package chat_completions provides response translation from Kiro to OpenAI format.
package chat_completions

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// ConvertKiroResponseToOpenAI converts Kiro streaming response to OpenAI SSE format.
// Handles Claude SSE events: content_block_start, content_block_delta, input_json_delta,
// content_block_stop, message_delta, and message_stop.
func ConvertKiroResponseToOpenAI(ctx context.Context, model string, originalRequest, request, rawResponse []byte, param *any) []string {
	root := gjson.ParseBytes(rawResponse)
	var results []string

	eventType := root.Get("type").String()

	switch eventType {
	case "message_start":
		// Initial message event - could emit initial chunk if needed
		return results

	case "content_block_start":
		// Start of a content block (text or tool_use)
		blockType := root.Get("content_block.type").String()
		index := int(root.Get("index").Int())

		if blockType == "tool_use" {
			// Start of tool_use block
			toolUseID := root.Get("content_block.id").String()
			toolName := root.Get("content_block.name").String()

			toolCall := map[string]interface{}{
				"index": index,
				"id":    toolUseID,
				"type":  "function",
				"function": map[string]interface{}{
					"name":      toolName,
					"arguments": "",
				},
			}

			response := map[string]interface{}{
				"id":      "chatcmpl-" + uuid.New().String()[:24],
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   model,
				"choices": []map[string]interface{}{
					{
						"index": 0,
						"delta": map[string]interface{}{
							"tool_calls": []map[string]interface{}{toolCall},
						},
						"finish_reason": nil,
					},
				},
			}
			result, _ := json.Marshal(response)
			results = append(results, string(result))
		}
		return results

	case "content_block_delta":
		index := int(root.Get("index").Int())
		deltaType := root.Get("delta.type").String()

		if deltaType == "text_delta" {
			// Text content delta
			contentDelta := root.Get("delta.text").String()
			if contentDelta != "" {
				response := map[string]interface{}{
					"id":      "chatcmpl-" + uuid.New().String()[:24],
					"object":  "chat.completion.chunk",
					"created": time.Now().Unix(),
					"model":   model,
					"choices": []map[string]interface{}{
						{
							"index": 0,
							"delta": map[string]interface{}{
								"content": contentDelta,
							},
							"finish_reason": nil,
						},
					},
				}
				result, _ := json.Marshal(response)
				results = append(results, string(result))
			}
		} else if deltaType == "input_json_delta" {
			// Tool input delta (streaming arguments)
			partialJSON := root.Get("delta.partial_json").String()
			if partialJSON != "" {
				toolCall := map[string]interface{}{
					"index": index,
					"function": map[string]interface{}{
						"arguments": partialJSON,
					},
				}

				response := map[string]interface{}{
					"id":      "chatcmpl-" + uuid.New().String()[:24],
					"object":  "chat.completion.chunk",
					"created": time.Now().Unix(),
					"model":   model,
					"choices": []map[string]interface{}{
						{
							"index": 0,
							"delta": map[string]interface{}{
								"tool_calls": []map[string]interface{}{toolCall},
							},
							"finish_reason": nil,
						},
					},
				}
				result, _ := json.Marshal(response)
				results = append(results, string(result))
			}
		}
		return results

	case "content_block_stop":
		// End of content block - no output needed for OpenAI format
		return results

	case "message_delta":
		// Final message delta with stop_reason
		stopReason := root.Get("delta.stop_reason").String()
		if stopReason != "" {
			finishReason := "stop"
			if stopReason == "tool_use" {
				finishReason = "tool_calls"
			} else if stopReason == "end_turn" {
				finishReason = "stop"
			} else if stopReason == "max_tokens" {
				finishReason = "length"
			}

			response := map[string]interface{}{
				"id":      "chatcmpl-" + uuid.New().String()[:24],
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   model,
				"choices": []map[string]interface{}{
					{
						"index":         0,
						"delta":         map[string]interface{}{},
						"finish_reason": finishReason,
					},
				},
			}
			result, _ := json.Marshal(response)
			results = append(results, string(result))
		}
		return results

	case "message_stop":
		// End of message - could emit [DONE] marker
		return results
	}

	// Fallback: handle raw content for backward compatibility
	var contentDelta string
	if delta := root.Get("delta.text"); delta.Exists() {
		contentDelta = delta.String()
	} else if content := root.Get("content"); content.Exists() && root.Get("type").String() == "" {
		contentDelta = content.String()
	}

	if contentDelta != "" {
		response := map[string]interface{}{
			"id":      "chatcmpl-" + uuid.New().String()[:24],
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"delta": map[string]interface{}{
						"content": contentDelta,
					},
					"finish_reason": nil,
				},
			},
		}
		result, _ := json.Marshal(response)
		results = append(results, string(result))
	}

	// Handle tool_use content blocks (Claude format) - fallback
	toolUses := root.Get("delta.tool_use")
	if !toolUses.Exists() {
		toolUses = root.Get("tool_use")
	}
	if toolUses.Exists() && toolUses.IsObject() {
		inputJSON := toolUses.Get("input").String()
		if inputJSON == "" {
			if inputObj := toolUses.Get("input"); inputObj.Exists() {
				inputBytes, _ := json.Marshal(inputObj.Value())
				inputJSON = string(inputBytes)
			}
		}

		toolCall := map[string]interface{}{
			"index": 0,
			"id":    toolUses.Get("id").String(),
			"type":  "function",
			"function": map[string]interface{}{
				"name":      toolUses.Get("name").String(),
				"arguments": inputJSON,
			},
		}

		response := map[string]interface{}{
			"id":      "chatcmpl-" + uuid.New().String()[:24],
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"delta": map[string]interface{}{
						"tool_calls": []map[string]interface{}{toolCall},
					},
					"finish_reason": nil,
				},
			},
		}
		result, _ := json.Marshal(response)
		results = append(results, string(result))
	}

	return results
}

// ConvertKiroResponseToOpenAINonStream converts Kiro non-streaming response to OpenAI format.
func ConvertKiroResponseToOpenAINonStream(ctx context.Context, model string, originalRequest, request, rawResponse []byte, param *any) string {
	root := gjson.ParseBytes(rawResponse)

	var content string
	var toolCalls []map[string]interface{}

	contentArray := root.Get("content")
	if contentArray.IsArray() {
		for _, item := range contentArray.Array() {
			itemType := item.Get("type").String()
			if itemType == "text" {
				content += item.Get("text").String()
			} else if itemType == "tool_use" {
				// Convert Claude tool_use to OpenAI tool_calls format
				inputJSON := item.Get("input").String()
				if inputJSON == "" {
					// If input is an object, marshal it
					if inputObj := item.Get("input"); inputObj.Exists() {
						inputBytes, _ := json.Marshal(inputObj.Value())
						inputJSON = string(inputBytes)
					}
				}
				toolCall := map[string]interface{}{
					"id":   item.Get("id").String(),
					"type": "function",
					"function": map[string]interface{}{
						"name":      item.Get("name").String(),
						"arguments": inputJSON,
					},
				}
				toolCalls = append(toolCalls, toolCall)
			}
		}
	} else {
		content = root.Get("content").String()
	}

	inputTokens := root.Get("usage.input_tokens").Int()
	outputTokens := root.Get("usage.output_tokens").Int()

	message := map[string]interface{}{
		"role":    "assistant",
		"content": content,
	}

	// Add tool_calls if present
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	response := map[string]interface{}{
		"id":      "chatcmpl-" + uuid.New().String()[:24],
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outputTokens,
			"total_tokens":      inputTokens + outputTokens,
		},
	}

	result, _ := json.Marshal(response)
	return string(result)
}
