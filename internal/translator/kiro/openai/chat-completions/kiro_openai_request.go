// Package chat_completions provides request translation from OpenAI to Kiro format.
package chat_completions

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// reasoningEffortToBudget maps OpenAI reasoning_effort values to Claude thinking budget_tokens.
// OpenAI uses "low", "medium", "high" while Claude uses numeric budget_tokens.
var reasoningEffortToBudget = map[string]int{
	"low":    4000,
	"medium": 16000,
	"high":   32000,
}

// ConvertOpenAIRequestToKiro transforms an OpenAI Chat Completions API request into Kiro (Claude) format.
// Kiro uses Claude-compatible format internally, so we primarily pass through to Claude format.
// Supports tool calling: OpenAI tools -> Claude tools, tool_calls -> tool_use, tool messages -> tool_result.
// Supports reasoning/thinking: OpenAI reasoning_effort -> Claude thinking parameter.
func ConvertOpenAIRequestToKiro(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)
	root := gjson.ParseBytes(rawJSON)

	// Build Claude-compatible request
	out := `{"model":"","max_tokens":32000,"messages":[]}`

	// Set model
	out, _ = sjson.Set(out, "model", modelName)

	// Copy max_tokens if present
	if v := root.Get("max_tokens"); v.Exists() {
		out, _ = sjson.Set(out, "max_tokens", v.Int())
	}

	// Copy temperature if present
	if v := root.Get("temperature"); v.Exists() {
		out, _ = sjson.Set(out, "temperature", v.Float())
	}

	// Copy top_p if present
	if v := root.Get("top_p"); v.Exists() {
		out, _ = sjson.Set(out, "top_p", v.Float())
	}

	// Handle OpenAI reasoning_effort parameter -> Claude thinking parameter
	// OpenAI format: {"reasoning_effort": "low"|"medium"|"high"}
	// Claude format: {"thinking": {"type": "enabled", "budget_tokens": N}}
	if v := root.Get("reasoning_effort"); v.Exists() {
		effort := v.String()
		if budget, ok := reasoningEffortToBudget[effort]; ok {
			thinking := map[string]interface{}{
				"type":          "enabled",
				"budget_tokens": budget,
			}
			out, _ = sjson.Set(out, "thinking", thinking)
		}
	}

	// Also support direct thinking parameter passthrough (for Claude API compatibility)
	// Claude format: {"thinking": {"type": "enabled", "budget_tokens": N}}
	if v := root.Get("thinking"); v.Exists() && v.IsObject() {
		out, _ = sjson.Set(out, "thinking", v.Value())
	}

	// Convert OpenAI tools to Claude tools format
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		claudeTools := make([]interface{}, 0)
		for _, tool := range tools.Array() {
			if tool.Get("type").String() == "function" {
				fn := tool.Get("function")
				claudeTool := map[string]interface{}{
					"name":        fn.Get("name").String(),
					"description": fn.Get("description").String(),
				}
				// Convert parameters to input_schema
				if params := fn.Get("parameters"); params.Exists() {
					claudeTool["input_schema"] = params.Value()
				} else {
					claudeTool["input_schema"] = map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{},
					}
				}
				claudeTools = append(claudeTools, claudeTool)
			}
		}
		if len(claudeTools) > 0 {
			out, _ = sjson.Set(out, "tools", claudeTools)
		}
	}

	// Process messages
	messages := root.Get("messages")
	if messages.Exists() && messages.IsArray() {
		claudeMessages := make([]interface{}, 0)
		var systemPrompt string
		
		// Track pending tool results to merge with next user message
		var pendingToolResults []map[string]interface{}

		for _, msg := range messages.Array() {
			role := msg.Get("role").String()
			content := msg.Get("content")

			if role == "system" {
				// Extract system message
				if content.IsArray() {
					for _, part := range content.Array() {
						if part.Get("type").String() == "text" {
							systemPrompt += part.Get("text").String() + "\n"
						}
					}
				} else {
					systemPrompt = content.String()
				}
				continue
			}

			if role == "tool" {
				// OpenAI tool message -> Claude tool_result content block
				toolCallID := msg.Get("tool_call_id").String()
				toolContent := content.String()
				
				toolResult := map[string]interface{}{
					"type":        "tool_result",
					"tool_use_id": toolCallID,
				}
				
				// Handle content - can be string or structured
				if content.IsArray() {
					contentParts := make([]interface{}, 0)
					for _, part := range content.Array() {
						if part.Get("type").String() == "text" {
							contentParts = append(contentParts, map[string]interface{}{
								"type": "text",
								"text": part.Get("text").String(),
							})
						}
					}
					toolResult["content"] = contentParts
				} else {
					toolResult["content"] = toolContent
				}
				
				pendingToolResults = append(pendingToolResults, toolResult)
				continue
			}

			claudeMsg := map[string]interface{}{
				"role": role,
			}

			// Handle assistant messages with tool_calls
			if role == "assistant" && msg.Get("tool_calls").Exists() {
				contentParts := make([]interface{}, 0)
				
				// Add text content if present
				if content.Exists() && content.String() != "" {
					contentParts = append(contentParts, map[string]interface{}{
						"type": "text",
						"text": content.String(),
					})
				}
				
				// Convert tool_calls to tool_use blocks
				for _, toolCall := range msg.Get("tool_calls").Array() {
					toolUseID := toolCall.Get("id").String()
					fnName := toolCall.Get("function.name").String()
					fnArgs := toolCall.Get("function.arguments").String()
					
					// Parse arguments JSON
					var argsMap map[string]interface{}
					if err := json.Unmarshal([]byte(fnArgs), &argsMap); err != nil {
						argsMap = map[string]interface{}{"raw": fnArgs}
					}
					
					contentParts = append(contentParts, map[string]interface{}{
						"type":  "tool_use",
						"id":    toolUseID,
						"name":  fnName,
						"input": argsMap,
					})
				}
				
				claudeMsg["content"] = contentParts
				claudeMessages = append(claudeMessages, claudeMsg)
				continue
			}

			// Handle user messages - may need to include pending tool results
			if role == "user" && len(pendingToolResults) > 0 {
				contentParts := make([]interface{}, 0)
				
				// Add pending tool results first
				for _, tr := range pendingToolResults {
					contentParts = append(contentParts, tr)
				}
				pendingToolResults = nil
				
				// Add user content
				if content.IsArray() {
					for _, part := range content.Array() {
						partType := part.Get("type").String()
						if partType == "text" {
							contentParts = append(contentParts, map[string]interface{}{
								"type": "text",
								"text": part.Get("text").String(),
							})
						} else if partType == "image_url" {
							imageURL := part.Get("image_url.url").String()
							
							// Check if it's base64 format (data:image/png;base64,xxxxx)
							if strings.HasPrefix(imageURL, "data:") {
								// Parse data URL format
								// Format: data:image/png;base64,xxxxx
								commaIdx := strings.Index(imageURL, ",")
								if commaIdx != -1 {
									// Extract media_type (e.g., "image/png")
									header := imageURL[5:commaIdx] // Remove "data:" prefix
									mediaType := header
									if semiIdx := strings.Index(header, ";"); semiIdx != -1 {
										mediaType = header[:semiIdx]
									}
									
									// Extract base64 data
									base64Data := imageURL[commaIdx+1:]
									
									contentParts = append(contentParts, map[string]interface{}{
										"type": "image",
										"source": map[string]interface{}{
											"type":       "base64",
											"media_type": mediaType,
											"data":       base64Data,
										},
									})
								}
							} else {
								// Regular URL format - keep original logic
								contentParts = append(contentParts, map[string]interface{}{
									"type": "image",
									"source": map[string]interface{}{
										"type": "url",
										"url":  imageURL,
									},
								})
							}
						}
					}
				} else if content.String() != "" {
					contentParts = append(contentParts, map[string]interface{}{
						"type": "text",
						"text": content.String(),
					})
				}
				
				claudeMsg["content"] = contentParts
				claudeMessages = append(claudeMessages, claudeMsg)
				continue
			}

			// Handle regular content
			if content.IsArray() {
				contentParts := make([]interface{}, 0)
				for _, part := range content.Array() {
					partType := part.Get("type").String()
					if partType == "text" {
						contentParts = append(contentParts, map[string]interface{}{
							"type": "text",
							"text": part.Get("text").String(),
						})
					} else if partType == "image_url" {
						imageURL := part.Get("image_url.url").String()
						
						// Check if it's base64 format (data:image/png;base64,xxxxx)
						if strings.HasPrefix(imageURL, "data:") {
							// Parse data URL format
							// Format: data:image/png;base64,xxxxx
							commaIdx := strings.Index(imageURL, ",")
							if commaIdx != -1 {
								// Extract media_type (e.g., "image/png")
								header := imageURL[5:commaIdx] // Remove "data:" prefix
								mediaType := header
								if semiIdx := strings.Index(header, ";"); semiIdx != -1 {
									mediaType = header[:semiIdx]
								}
								
								// Extract base64 data
								base64Data := imageURL[commaIdx+1:]
								
								contentParts = append(contentParts, map[string]interface{}{
									"type": "image",
									"source": map[string]interface{}{
										"type":       "base64",
										"media_type": mediaType,
										"data":       base64Data,
									},
								})
							}
						} else {
							// Regular URL format - keep original logic
							contentParts = append(contentParts, map[string]interface{}{
								"type": "image",
								"source": map[string]interface{}{
									"type": "url",
									"url":  imageURL,
								},
							})
						}
					}
				}
				claudeMsg["content"] = contentParts
			} else {
				claudeMsg["content"] = content.String()
			}

			claudeMessages = append(claudeMessages, claudeMsg)
		}

		// If there are pending tool results without a following user message,
		// create a user message with just the tool results
		if len(pendingToolResults) > 0 {
			contentParts := make([]interface{}, 0)
			for _, tr := range pendingToolResults {
				contentParts = append(contentParts, tr)
			}
			claudeMessages = append(claudeMessages, map[string]interface{}{
				"role":    "user",
				"content": contentParts,
			})
		}

		out, _ = sjson.Set(out, "messages", claudeMessages)

		if systemPrompt != "" {
			out, _ = sjson.Set(out, "system", systemPrompt)
		}
	}

	// Set stream
	out, _ = sjson.Set(out, "stream", stream)

	return []byte(out)
}
