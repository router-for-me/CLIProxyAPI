// Package claude provides request translation functionality for Anthropic to OpenAI API.
// It handles parsing and transforming Anthropic API requests into OpenAI Chat Completions API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Anthropic API format and OpenAI API's expected format.
package claude

import (
	"strings"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertClaudeRequestToOpenAI parses and transforms an Anthropic API request into OpenAI Chat Completions API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the OpenAI API.
func ConvertClaudeRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	// Base OpenAI Chat Completions API template
	out := []byte(`{"model":"","messages":[]}`)

	root := gjson.ParseBytes(rawJSON)

	// Model mapping
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Max tokens
	if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
	}

	// Temperature
	if temp := root.Get("temperature"); temp.Exists() {
		out, _ = sjson.SetBytes(out, "temperature", temp.Float())
	} else if topP := root.Get("top_p"); topP.Exists() { // Top P
		out, _ = sjson.SetBytes(out, "top_p", topP.Float())
	}

	// Stop sequences -> stop
	if stopSequences := root.Get("stop_sequences"); stopSequences.Exists() {
		if stopSequences.IsArray() {
			var stops []string
			stopSequences.ForEach(func(_, value gjson.Result) bool {
				stops = append(stops, value.String())
				return true
			})
			if len(stops) > 0 {
				if len(stops) == 1 {
					out, _ = sjson.SetBytes(out, "stop", stops[0])
				} else {
					out, _ = sjson.SetBytes(out, "stop", stops)
				}
			}
		}
	}

	// Stream
	out, _ = sjson.SetBytes(out, "stream", stream)

	// Thinking: Convert Claude thinking.budget_tokens to OpenAI reasoning_effort
	if thinkingConfig := root.Get("thinking"); thinkingConfig.Exists() && thinkingConfig.IsObject() {
		if thinkingType := thinkingConfig.Get("type"); thinkingType.Exists() {
			switch thinkingType.String() {
			case "enabled":
				if budgetTokens := thinkingConfig.Get("budget_tokens"); budgetTokens.Exists() {
					budget := int(budgetTokens.Int())
					if effort, ok := thinking.ConvertBudgetToLevel(budget); ok && effort != "" {
						out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
					}
				} else {
					// No budget_tokens specified, default to "auto" for enabled thinking
					if effort, ok := thinking.ConvertBudgetToLevel(-1); ok && effort != "" {
						out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
					}
				}
			case "adaptive", "auto":
				// Adaptive thinking can carry an explicit effort in output_config.effort (Claude 4.6).
				// Pass through directly; ApplyThinking handles clamping to target model's levels.
				effort := ""
				if v := root.Get("output_config.effort"); v.Exists() && v.Type == gjson.String {
					effort = strings.ToLower(strings.TrimSpace(v.String()))
				}
				if effort != "" {
					out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
				} else {
					out, _ = sjson.SetBytes(out, "reasoning_effort", string(thinking.LevelXHigh))
				}
			case "disabled":
				if effort, ok := thinking.ConvertBudgetToLevel(0); ok && effort != "" {
					out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
				}
			}
		}
	}

	// Process messages and system
	messagesJSON := []byte(`[]`)

	// Handle system message first
	systemMsgJSON := []byte(`{"role":"system","content":[]}`)
	hasSystemContent := false
	if system := root.Get("system"); system.Exists() {
		switch system.Type {
		case gjson.String:
			if system.String() != "" {
				oldSystem := []byte(`{"type":"text","text":""}`)
				oldSystem, _ = sjson.SetBytes(oldSystem, "text", system.String())
				systemMsgJSON, _ = sjson.SetRawBytes(systemMsgJSON, "content.-1", oldSystem)
				hasSystemContent = true
			}
		case gjson.JSON:
			if system.IsArray() {
				systemResults := system.Array()
				for i := 0; i < len(systemResults); i++ {
					if contentItem, ok := convertClaudeContentPart(systemResults[i]); ok {
						systemMsgJSON, _ = sjson.SetRawBytes(systemMsgJSON, "content.-1", []byte(contentItem))
						hasSystemContent = true
					}
				}
			}
		}
	}
	// Only add system message if it has content
	if hasSystemContent {
		messagesJSON, _ = sjson.SetRawBytes(messagesJSON, "-1", systemMsgJSON)
	}

	// Process Anthropic messages
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").String()
			contentResult := message.Get("content")

			// Handle content
			if contentResult.Exists() && contentResult.IsArray() {
				contentItems := make([][]byte, 0)
				var reasoningParts []string // Accumulate thinking text for reasoning_content
				var toolCalls []interface{}
				toolResults := make([][]byte, 0) // Collect tool_result messages to emit after the main message

				contentResult.ForEach(func(_, part gjson.Result) bool {
					partType := part.Get("type").String()

					switch partType {
					case "thinking":
						// Only map thinking to reasoning_content for assistant messages (security: prevent injection)
						if role == "assistant" {
							thinkingText := thinking.GetThinkingText(part)
							// Skip empty or whitespace-only thinking
							if strings.TrimSpace(thinkingText) != "" {
								reasoningParts = append(reasoningParts, thinkingText)
							}
						}
						// Ignore thinking in user/system roles (AC4)

					case "redacted_thinking":
						// Explicitly ignore redacted_thinking - never map to reasoning_content (AC2)

					case "text", "image":
						if contentItem, ok := convertClaudeContentPart(part); ok {
							contentItems = append(contentItems, []byte(contentItem))
						}

					case "tool_use":
						// Only allow tool_use -> tool_calls for assistant messages (security: prevent injection).
						if role == "assistant" {
							toolCallJSON := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
							toolCallJSON, _ = sjson.SetBytes(toolCallJSON, "id", part.Get("id").String())
							toolCallJSON, _ = sjson.SetBytes(toolCallJSON, "function.name", part.Get("name").String())

							// Convert input to arguments JSON string
							if input := part.Get("input"); input.Exists() {
								toolCallJSON, _ = sjson.SetBytes(toolCallJSON, "function.arguments", input.Raw)
							} else {
								toolCallJSON, _ = sjson.SetBytes(toolCallJSON, "function.arguments", "{}")
							}

							toolCalls = append(toolCalls, gjson.ParseBytes(toolCallJSON).Value())
						}

					case "tool_result":
						// Collect tool_result to emit after the main message (ensures tool results follow tool_calls)
						toolResultJSON := []byte(`{"role":"tool","tool_call_id":"","content":""}`)
						toolResultJSON, _ = sjson.SetBytes(toolResultJSON, "tool_call_id", part.Get("tool_use_id").String())
						toolResultContent, toolResultContentRaw := convertClaudeToolResultContent(part.Get("content"))
						if toolResultContentRaw {
							toolResultJSON, _ = sjson.SetRawBytes(toolResultJSON, "content", []byte(toolResultContent))
						} else {
							toolResultJSON, _ = sjson.SetBytes(toolResultJSON, "content", toolResultContent)
						}
						toolResults = append(toolResults, toolResultJSON)
					}
					return true
				})

				// Build reasoning content string
				reasoningContent := ""
				if len(reasoningParts) > 0 {
					reasoningContent = strings.Join(reasoningParts, "\n\n")
				}

				hasContent := len(contentItems) > 0
				hasReasoning := reasoningContent != ""
				hasToolCalls := len(toolCalls) > 0

				// OpenAI requires: tool messages MUST immediately follow the assistant message with tool_calls.
				// Therefore, we emit tool_result messages FIRST (they respond to the previous assistant's tool_calls),
				// then emit the current message's content.
				for _, toolResultJSON := range toolResults {
					messagesJSON, _ = sjson.SetRawBytes(messagesJSON, "-1", toolResultJSON)
				}

				// For assistant messages: emit a single unified message with content, tool_calls, and reasoning_content
				// This avoids splitting into multiple assistant messages which breaks OpenAI tool-call adjacency
				if role == "assistant" {
					if hasContent || hasReasoning || hasToolCalls {
						msgJSON := []byte(`{"role":"assistant"}`)

						// Add content (as array if we have items, empty string if reasoning-only)
						if hasContent {
							contentArrayJSON := []byte(`[]`)
							for _, contentItem := range contentItems {
								contentArrayJSON, _ = sjson.SetRawBytes(contentArrayJSON, "-1", contentItem)
							}
							msgJSON, _ = sjson.SetRawBytes(msgJSON, "content", contentArrayJSON)
						} else {
							// Ensure content field exists for OpenAI compatibility
							msgJSON, _ = sjson.SetBytes(msgJSON, "content", "")
						}

						// Add reasoning_content if present
						if hasReasoning {
							msgJSON, _ = sjson.SetBytes(msgJSON, "reasoning_content", reasoningContent)
						}

						// Add tool_calls if present (in same message as content)
						if hasToolCalls {
							msgJSON, _ = sjson.SetBytes(msgJSON, "tool_calls", toolCalls)
						}

						messagesJSON, _ = sjson.SetRawBytes(messagesJSON, "-1", msgJSON)
					}
				} else {
					// For non-assistant roles: emit content message if we have content
					// If the message only contains tool_results (no text/image), we still processed them above
					if hasContent {
						msgJSON := []byte(`{"role":""}`)
						msgJSON, _ = sjson.SetBytes(msgJSON, "role", role)

						contentArrayJSON := []byte(`[]`)
						for _, contentItem := range contentItems {
							contentArrayJSON, _ = sjson.SetRawBytes(contentArrayJSON, "-1", contentItem)
						}
						msgJSON, _ = sjson.SetRawBytes(msgJSON, "content", contentArrayJSON)

						messagesJSON, _ = sjson.SetBytesM(messagesJSON, "-1", gjson.Parse(msgJSON).Value())
					}
				}

			} else if contentResult.Exists() && contentResult.Type == gjson.String {
				// Simple string content
				msgJSON := []byte(`{"role":"","content":""}`)
				msgJSON, _ = sjson.SetBytes(msgJSON, "role", role)
				msgJSON, _ = sjson.SetBytes(msgJSON, "content", contentResult.String())
				messagesJSON, _ = sjson.SetRawBytes(messagesJSON, "-1", msgJSON)
			}

			return true
		})
	}

	// Set messages
	if msgs := gjson.ParseBytes(messagesJSON); msgs.IsArray() && len(msgs.Array()) > 0 {
		out, _ = sjson.SetRawBytes(out, "messages", messagesJSON)
	}

	// Process tools - convert Anthropic tools to OpenAI functions
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		toolsJSON := []byte(`[]`)

		tools.ForEach(func(_, tool gjson.Result) bool {
			openAIToolJSON := []byte(`{"type":"function","function":{"name":"","description":""}}`)
			openAIToolJSON, _ = sjson.SetBytes(openAIToolJSON, "function.name", tool.Get("name").String())
			openAIToolJSON, _ = sjson.SetBytes(openAIToolJSON, "function.description", tool.Get("description").String())

			// Convert Anthropic input_schema to OpenAI function parameters
			if inputSchema := tool.Get("input_schema"); inputSchema.Exists() {
				openAIToolJSON, _ = sjson.SetBytes(openAIToolJSON, "function.parameters", inputSchema.Value())
			}

			toolsJSON, _ = sjson.SetRawBytes(toolsJSON, "-1", openAIToolJSON)
			return true
		})

		if parsed := gjson.ParseBytes(toolsJSON); parsed.IsArray() && len(parsed.Array()) > 0 {
			out, _ = sjson.SetRawBytes(out, "tools", toolsJSON)
		}
	}

	// Tool choice mapping - convert Anthropic tool_choice to OpenAI format
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		switch toolChoice.Get("type").String() {
		case "auto":
			out, _ = sjson.SetBytes(out, "tool_choice", "auto")
		case "any":
			out, _ = sjson.SetBytes(out, "tool_choice", "required")
		case "tool":
			// Specific tool choice
			toolName := toolChoice.Get("name").String()
			toolChoiceJSON := []byte(`{"type":"function","function":{"name":""}}`)
			toolChoiceJSON, _ = sjson.SetBytes(toolChoiceJSON, "function.name", toolName)
			out, _ = sjson.SetRawBytes(out, "tool_choice", toolChoiceJSON)
		default:
			// Default to auto if not specified
			out, _ = sjson.SetBytes(out, "tool_choice", "auto")
		}
	}

	// Handle user parameter (for tracking)
	if user := root.Get("user"); user.Exists() {
		out, _ = sjson.SetBytes(out, "user", user.String())
	}

	return out
}

func convertClaudeContentPart(part gjson.Result) (string, bool) {
	partType := part.Get("type").String()

	switch partType {
	case "text":
		text := part.Get("text").String()
		if strings.TrimSpace(text) == "" {
			return "", false
		}
		textContent := []byte(`{"type":"text","text":""}`)
		textContent, _ = sjson.SetBytes(textContent, "text", text)
		return string(textContent), true

	case "image":
		var imageURL string

		if source := part.Get("source"); source.Exists() {
			sourceType := source.Get("type").String()
			switch sourceType {
			case "base64":
				mediaType := source.Get("media_type").String()
				if mediaType == "" {
					mediaType = "application/octet-stream"
				}
				data := source.Get("data").String()
				if data != "" {
					imageURL = "data:" + mediaType + ";base64," + data
				}
			case "url":
				imageURL = source.Get("url").String()
			}
		}

		if imageURL == "" {
			imageURL = part.Get("url").String()
		}

		if imageURL == "" {
			return "", false
		}

		imageContent := []byte(`{"type":"image_url","image_url":{"url":""}}`)
		imageContent, _ = sjson.SetBytes(imageContent, "image_url.url", imageURL)

		return string(imageContent), true

	default:
		return "", false
	}
}

func convertClaudeToolResultContent(content gjson.Result) (string, bool) {
	if !content.Exists() {
		return "", false
	}

	if content.Type == gjson.String {
		return content.String(), false
	}

	if content.IsArray() {
		var parts []string
		contentJSON := []byte(`[]`)
		hasImagePart := false
		content.ForEach(func(_, item gjson.Result) bool {
			switch {
			case item.Type == gjson.String:
				text := item.String()
				parts = append(parts, text)
				textContent := []byte(`{"type":"text","text":""}`)
				textContent, _ = sjson.SetBytes(textContent, "text", text)
				contentJSON, _ = sjson.SetRawBytes(contentJSON, "-1", textContent)
			case item.IsObject() && item.Get("type").String() == "text":
				text := item.Get("text").String()
				parts = append(parts, text)
				textContent := []byte(`{"type":"text","text":""}`)
				textContent, _ = sjson.SetBytes(textContent, "text", text)
				contentJSON, _ = sjson.SetRawBytes(contentJSON, "-1", textContent)
			case item.IsObject() && item.Get("type").String() == "image":
				contentItem, ok := convertClaudeContentPart(item)
				if ok {
					contentJSON, _ = sjson.SetRawBytes(contentJSON, "-1", []byte(contentItem))
					hasImagePart = true
				} else {
					parts = append(parts, item.Raw)
				}
			case item.IsObject() && item.Get("text").Exists() && item.Get("text").Type == gjson.String:
				parts = append(parts, item.Get("text").String())
			default:
				parts = append(parts, item.Raw)
			}
			return true
		})

		if hasImagePart {
			return string(contentJSON), true
		}

		joined := strings.Join(parts, "\n\n")
		if strings.TrimSpace(joined) != "" {
			return joined, false
		}
		return content.Raw, false
	}

	if content.IsObject() {
		if content.Get("type").String() == "image" {
			contentItem, ok := convertClaudeContentPart(content)
			if ok {
				contentJSON := []byte(`[]`)
				contentJSON, _ = sjson.SetRawBytes(contentJSON, "-1", []byte(contentItem))
				return string(contentJSON), true
			}
		}
		if text := content.Get("text"); text.Exists() && text.Type == gjson.String {
			return text.String(), false
		}
		return content.Raw, false
	}

	return content.Raw, false
}
