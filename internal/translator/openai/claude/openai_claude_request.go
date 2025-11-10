// Package claude provides request translation functionality for Anthropic to OpenAI API.
// It handles parsing and transforming Anthropic API requests into OpenAI Chat Completions API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Anthropic API format and OpenAI API's expected format.
package claude

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertClaudeRequestToOpenAI parses and transforms an Anthropic API request into OpenAI Chat Completions API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the OpenAI API.
func ConvertClaudeRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)
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

	// Process messages and system
	var messagesJSON = "[]"

	// Handle system message first
	systemMsgJSON := `{"role":"system","content":[{"type":"text","text":"Use ANY tool, the parameters MUST accord with RFC 8259 (The JavaScript Object Notation (JSON) Data Interchange Format), the keys and value MUST be enclosed in double quotes."}]}`
	if system := root.Get("system"); system.Exists() {
		if system.Type == gjson.String {
			if system.String() != "" {
				oldSystem := `{"type":"text","text":""}`
				oldSystem, _ = sjson.Set(oldSystem, "text", system.String())
				systemMsgJSON, _ = sjson.SetRaw(systemMsgJSON, "content.-1", oldSystem)
			}
		} else if system.Type == gjson.JSON {
			if system.IsArray() {
				systemResults := system.Array()
				for i := 0; i < len(systemResults); i++ {
					if contentItem, ok := convertClaudeContentPart(systemResults[i]); ok {
						systemMsgJSON, _ = sjson.SetRaw(systemMsgJSON, "content.-1", contentItem)
					}
				}
			}
		}
	}
	messagesJSON, _ = sjson.SetRaw(messagesJSON, "-1", systemMsgJSON)

	// Process Anthropic messages
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").String()
			contentResult := message.Get("content")

			// Handle content
			if contentResult.Exists() && contentResult.IsArray() {
				var contentItems []string
				var toolCalls []interface{}

				contentResult.ForEach(func(_, part gjson.Result) bool {
					partType := part.Get("type").String()

					switch partType {
					case "text", "image":
						if contentItem, ok := convertClaudeContentPart(part); ok {
							contentItems = append(contentItems, contentItem)
						}

					case "tool_use":
						// Convert to OpenAI tool call format
						toolCallJSON := `{"id":"","type":"function","function":{"name":"","arguments":""}}`
						toolCallJSON, _ = sjson.Set(toolCallJSON, "id", part.Get("id").String())
						toolCallJSON, _ = sjson.Set(toolCallJSON, "function.name", part.Get("name").String())

						// Convert input to arguments JSON string
						if input := part.Get("input"); input.Exists() {
							if inputJSON, err := json.Marshal(input.Value()); err == nil {
								toolCallJSON, _ = sjson.Set(toolCallJSON, "function.arguments", string(inputJSON))
							} else {
								toolCallJSON, _ = sjson.Set(toolCallJSON, "function.arguments", "{}")
							}
						} else {
							toolCallJSON, _ = sjson.Set(toolCallJSON, "function.arguments", "{}")
						}

						toolCalls = append(toolCalls, gjson.Parse(toolCallJSON).Value())

					case "tool_result":
						// Convert to OpenAI tool message format and add immediately to preserve order
						toolResultJSON := `{"role":"tool","tool_call_id":"","content":""}`
						toolResultJSON, _ = sjson.Set(toolResultJSON, "tool_call_id", part.Get("tool_use_id").String())
						toolResultJSON, _ = sjson.Set(toolResultJSON, "content", part.Get("content").String())
						messagesJSON, _ = sjson.Set(messagesJSON, "-1", gjson.Parse(toolResultJSON).Value())
					}
					return true
				})

				// Emit text/image content as one message
				if len(contentItems) > 0 {
					msgJSON := `{"role":"","content":""}`
					msgJSON, _ = sjson.Set(msgJSON, "role", role)

					contentArrayJSON := "[]"
					for _, contentItem := range contentItems {
						contentArrayJSON, _ = sjson.SetRaw(contentArrayJSON, "-1", contentItem)
					}
					msgJSON, _ = sjson.SetRaw(msgJSON, "content", contentArrayJSON)

					contentValue := gjson.Get(msgJSON, "content")
					hasContent := false
					switch {
					case !contentValue.Exists():
						hasContent = false
					case contentValue.Type == gjson.String:
						hasContent = contentValue.String() != ""
					case contentValue.IsArray():
						hasContent = len(contentValue.Array()) > 0
					default:
						hasContent = contentValue.Raw != "" && contentValue.Raw != "null"
					}

					if hasContent {
						messagesJSON, _ = sjson.Set(messagesJSON, "-1", gjson.Parse(msgJSON).Value())
					}
				}

				// Emit tool calls in a separate assistant message
				if role == "assistant" && len(toolCalls) > 0 {
					toolCallMsgJSON := `{"role":"assistant","tool_calls":[]}`
					toolCallsJSON, _ := json.Marshal(toolCalls)
					toolCallMsgJSON, _ = sjson.SetRaw(toolCallMsgJSON, "tool_calls", string(toolCallsJSON))
					messagesJSON, _ = sjson.Set(messagesJSON, "-1", gjson.Parse(toolCallMsgJSON).Value())
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
		if !part.Get("text").Exists() {
			return "", false
		}
		textContent := `{"type":"text","text":""}`
		textContent, _ = sjson.Set(textContent, "text", part.Get("text").String())
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
