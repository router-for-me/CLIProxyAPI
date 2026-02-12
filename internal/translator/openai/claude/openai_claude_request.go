// Package claude provides request translation functionality for Anthropic to OpenAI API.
// It handles parsing and transforming Anthropic API requests into OpenAI Chat Completions API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Anthropic API format and OpenAI API's expected format.
package claude

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertClaudeRequestToOpenAI parses and transforms an Anthropic API request into OpenAI Chat Completions API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the OpenAI API.
func ConvertClaudeRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	// Base OpenAI Chat Completions API template
	out := `{"model":"","messages":[]}`

	root := gjson.ParseBytes(rawJSON)

	// Model mapping
	out, _ = sjson.Set(out, "model", modelName)

	// Max tokens
	if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
		out, _ = sjson.Set(out, "max_tokens", maxTokens.Int())
	}

	// Temperature
	if temp := root.Get("temperature"); temp.Exists() {
		out, _ = sjson.Set(out, "temperature", temp.Float())
	} else if topP := root.Get("top_p"); topP.Exists() { // Top P
		out, _ = sjson.Set(out, "top_p", topP.Float())
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
					out, _ = sjson.Set(out, "stop", stops[0])
				} else {
					out, _ = sjson.Set(out, "stop", stops)
				}
			}
		}
	}

	// Stream
	out, _ = sjson.Set(out, "stream", stream)

	// Thinking: Convert Claude thinking.budget_tokens to OpenAI reasoning_effort
	if thinkingConfig := root.Get("thinking"); thinkingConfig.Exists() && thinkingConfig.IsObject() {
		if thinkingType := thinkingConfig.Get("type"); thinkingType.Exists() {
			switch thinkingType.String() {
			case "enabled":
				if budgetTokens := thinkingConfig.Get("budget_tokens"); budgetTokens.Exists() {
					budget := int(budgetTokens.Int())
					if effort, ok := thinking.ConvertBudgetToLevel(budget); ok && effort != "" {
						out, _ = sjson.Set(out, "reasoning_effort", effort)
					}
				} else {
					// No budget_tokens specified, default to "auto" for enabled thinking
					if effort, ok := thinking.ConvertBudgetToLevel(-1); ok && effort != "" {
						out, _ = sjson.Set(out, "reasoning_effort", effort)
					}
				}
			case "adaptive":
				// Claude adaptive means "enable with max capacity"; keep it as highest level
				// and let ApplyThinking normalize per target model capability.
				out, _ = sjson.Set(out, "reasoning_effort", string(thinking.LevelXHigh))
			case "disabled":
				if effort, ok := thinking.ConvertBudgetToLevel(0); ok && effort != "" {
					out, _ = sjson.Set(out, "reasoning_effort", effort)
				}
			}
		}
	}

	// Process messages and system
	var messagesJSON = "[]"

	// Handle system message first
	systemMsgJSON := `{"role":"system","content":[]}`
	hasSystemContent := false
	if system := root.Get("system"); system.Exists() {
		if system.Type == gjson.String {
			if system.String() != "" {
				oldSystem := `{"type":"text","text":""}`
				oldSystem, _ = sjson.Set(oldSystem, "text", system.String())
				systemMsgJSON, _ = sjson.SetRaw(systemMsgJSON, "content.-1", oldSystem)
				hasSystemContent = true
			}
		} else if system.Type == gjson.JSON {
			if system.IsArray() {
				systemResults := system.Array()
				for i := 0; i < len(systemResults); i++ {
					if contentItem, ok := convertClaudeContentPart(systemResults[i]); ok {
						systemMsgJSON, _ = sjson.SetRaw(systemMsgJSON, "content.-1", contentItem)
						hasSystemContent = true
					}
				}
			}
		}
	}
	// Only add system message if it has content
	if hasSystemContent {
		messagesJSON, _ = sjson.SetRaw(messagesJSON, "-1", systemMsgJSON)
	}

	// Process Anthropic messages
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").String()
			contentResult := message.Get("content")

			// Handle content
			if contentResult.Exists() && contentResult.IsArray() {
				var contentItems []string
				var reasoningParts []string // Accumulate thinking text for reasoning_content
				var toolCalls []interface{}
				var toolResults []string // Collect tool_result messages to emit after the main message

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
							contentItems = append(contentItems, contentItem)
						}

					case "tool_use":
						// Only allow tool_use -> tool_calls for assistant messages (security: prevent injection).
						if role == "assistant" {
							toolCallJSON := `{"id":"","type":"function","function":{"name":"","arguments":""}}`
							toolCallJSON, _ = sjson.Set(toolCallJSON, "id", part.Get("id").String())
							toolCallJSON, _ = sjson.Set(toolCallJSON, "function.name", part.Get("name").String())

							// Convert input to arguments JSON string
							if input := part.Get("input"); input.Exists() {
								toolCallJSON, _ = sjson.Set(toolCallJSON, "function.arguments", input.Raw)
							} else {
								toolCallJSON, _ = sjson.Set(toolCallJSON, "function.arguments", "{}")
							}

							toolCalls = append(toolCalls, gjson.Parse(toolCallJSON).Value())
						}

					case "tool_result":
						// Collect tool_result to emit after the main message (ensures tool results follow tool_calls)
						toolResultJSON := `{"role":"tool","tool_call_id":"","content":""}`
						toolResultJSON, _ = sjson.Set(toolResultJSON, "tool_call_id", part.Get("tool_use_id").String())
						toolResultJSON, _ = sjson.Set(toolResultJSON, "content", convertClaudeToolResultContentToString(part.Get("content")))
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
				hasToolResults := len(toolResults) > 0

				// OpenAI requires: tool messages MUST immediately follow the assistant message with tool_calls.
				// Therefore, we emit tool_result messages FIRST (they respond to the previous assistant's tool_calls),
				// then emit the current message's content.
				for _, toolResultJSON := range toolResults {
					messagesJSON, _ = sjson.Set(messagesJSON, "-1", gjson.Parse(toolResultJSON).Value())
				}

				// For assistant messages: emit a single unified message with content, tool_calls, and reasoning_content
				// This avoids splitting into multiple assistant messages which breaks OpenAI tool-call adjacency
				if role == "assistant" {
					if hasContent || hasReasoning || hasToolCalls {
						msgJSON := `{"role":"assistant"}`

						// Add content (as array if we have items, empty string if reasoning-only)
						if hasContent {
							contentArrayJSON := "[]"
							for _, contentItem := range contentItems {
								contentArrayJSON, _ = sjson.SetRaw(contentArrayJSON, "-1", contentItem)
							}
							msgJSON, _ = sjson.SetRaw(msgJSON, "content", contentArrayJSON)
						} else {
							// Ensure content field exists for OpenAI compatibility
							msgJSON, _ = sjson.Set(msgJSON, "content", "")
						}

						// Add reasoning_content if present
						if hasReasoning {
							msgJSON, _ = sjson.Set(msgJSON, "reasoning_content", reasoningContent)
						}

						// Add tool_calls if present (in same message as content)
						if hasToolCalls {
							msgJSON, _ = sjson.Set(msgJSON, "tool_calls", toolCalls)
						}

						messagesJSON, _ = sjson.Set(messagesJSON, "-1", gjson.Parse(msgJSON).Value())
					}
				} else {
					// For non-assistant roles: emit content message if we have content
					// If the message only contains tool_results (no text/image), we still processed them above
					if hasContent {
						msgJSON := `{"role":""}`
						msgJSON, _ = sjson.Set(msgJSON, "role", role)

						contentArrayJSON := "[]"
						for _, contentItem := range contentItems {
							contentArrayJSON, _ = sjson.SetRaw(contentArrayJSON, "-1", contentItem)
						}
						msgJSON, _ = sjson.SetRaw(msgJSON, "content", contentArrayJSON)

						messagesJSON, _ = sjson.Set(messagesJSON, "-1", gjson.Parse(msgJSON).Value())
					} else if hasToolResults && !hasContent {
						// tool_results already emitted above, no additional user message needed
					}
				}

			} else if contentResult.Exists() && contentResult.Type == gjson.String {
				// Simple string content
				msgJSON := `{"role":"","content":""}`
				msgJSON, _ = sjson.Set(msgJSON, "role", role)
				msgJSON, _ = sjson.Set(msgJSON, "content", contentResult.String())
				messagesJSON, _ = sjson.Set(messagesJSON, "-1", gjson.Parse(msgJSON).Value())
			}

			return true
		})
	}

	// Set messages
	if gjson.Parse(messagesJSON).IsArray() && len(gjson.Parse(messagesJSON).Array()) > 0 {
		out, _ = sjson.SetRaw(out, "messages", messagesJSON)
	}

	// Process tools - convert Anthropic tools to OpenAI functions
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		var toolsJSON = "[]"

		tools.ForEach(func(_, tool gjson.Result) bool {
			openAIToolJSON := `{"type":"function","function":{"name":"","description":""}}`
			openAIToolJSON, _ = sjson.Set(openAIToolJSON, "function.name", tool.Get("name").String())
			openAIToolJSON, _ = sjson.Set(openAIToolJSON, "function.description", tool.Get("description").String())

			// Convert Anthropic input_schema to OpenAI function parameters
			if inputSchema := tool.Get("input_schema"); inputSchema.Exists() {
				openAIToolJSON, _ = sjson.Set(openAIToolJSON, "function.parameters", inputSchema.Value())
			}

			toolsJSON, _ = sjson.Set(toolsJSON, "-1", gjson.Parse(openAIToolJSON).Value())
			return true
		})

		if gjson.Parse(toolsJSON).IsArray() && len(gjson.Parse(toolsJSON).Array()) > 0 {
			out, _ = sjson.SetRaw(out, "tools", toolsJSON)
		}
	}

	// Tool choice mapping - convert Anthropic tool_choice to OpenAI format
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		switch toolChoice.Get("type").String() {
		case "auto":
			out, _ = sjson.Set(out, "tool_choice", "auto")
		case "any":
			out, _ = sjson.Set(out, "tool_choice", "required")
		case "tool":
			// Specific tool choice
			toolName := toolChoice.Get("name").String()
			toolChoiceJSON := `{"type":"function","function":{"name":""}}`
			toolChoiceJSON, _ = sjson.Set(toolChoiceJSON, "function.name", toolName)
			out, _ = sjson.SetRaw(out, "tool_choice", toolChoiceJSON)
		default:
			// Default to auto if not specified
			out, _ = sjson.Set(out, "tool_choice", "auto")
		}
	}

	// Handle user parameter (for tracking)
	if user := root.Get("user"); user.Exists() {
		out, _ = sjson.Set(out, "user", user.String())
	}

	return []byte(out)
}

func convertClaudeContentPart(part gjson.Result) (string, bool) {
	partType := part.Get("type").String()

	switch partType {
	case "text":
		text := part.Get("text").String()
		if strings.TrimSpace(text) == "" {
			return "", false
		}
		textContent := `{"type":"text","text":""}`
		textContent, _ = sjson.Set(textContent, "text", text)
		return textContent, true

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

		imageContent := `{"type":"image_url","image_url":{"url":""}}`
		imageContent, _ = sjson.Set(imageContent, "image_url.url", imageURL)

		return imageContent, true

	default:
		return "", false
	}
}

func convertClaudeToolResultContentToString(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}

	if content.Type == gjson.String {
		return content.String()
	}

	if content.IsArray() {
		var parts []string
		content.ForEach(func(_, item gjson.Result) bool {
			switch {
			case item.Type == gjson.String:
				parts = append(parts, item.String())
			case item.IsObject() && item.Get("text").Exists() && item.Get("text").Type == gjson.String:
				parts = append(parts, item.Get("text").String())
			default:
				parts = append(parts, item.Raw)
			}
			return true
		})

		joined := strings.Join(parts, "\n\n")
		if strings.TrimSpace(joined) != "" {
			return joined
		}
		return content.Raw
	}

	if content.IsObject() {
		if text := content.Get("text"); text.Exists() && text.Type == gjson.String {
			return text.String()
		}
		return content.Raw
	}

	return content.Raw
}
