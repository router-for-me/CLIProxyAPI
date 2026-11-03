// Package claude provides request translation functionality for Anthropic to OpenAI API.
// It handles parsing and transforming Anthropic API requests into OpenAI Chat Completions API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Anthropic API format and OpenAI API's expected format.
package claude

import (
	"github.com/MoeForge/moego/base64"
	"strings"

	"github.com/MoeForge/moego/ojson"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// ConvertClaudeRequestToOpenAI parses and transforms an Anthropic API request into OpenAI Chat Completions API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the OpenAI API.
func ConvertClaudeRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertClaudeRequestToOpenAI(modelName, inputRawJSON, stream, false)

}

// ConvertClaudeRequestToOpenAIWithCompat preserves assistant thinking text
// for configured compatibility endpoints.
func ConvertClaudeRequestToOpenAIWithCompat(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertClaudeRequestToOpenAI(modelName, inputRawJSON, stream, true)
}

func convertClaudeRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool, preserveThinkingBlocks bool) ([]byte, error) {
	// The internal commentary marker must be restored as assistant text before
	// any thinking handling, so it is never dropped or mapped to reasoning.
	rawJSON, _ := sigcompat.RestoreCodexCommentaryMarkers(inputRawJSON)
	var drops translatorcommon.UserTurnDrops
	// Base OpenAI Chat Completions API template
	outDoc := ojson.NewDocString(`{"model":"","messages":[]}`)

	root := util.ParseGJSONBytesNoCopy(rawJSON)

	// Model mapping
	_ = outDoc.Set("model", modelName)

	// Max tokens
	if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
		_ = outDoc.Set("max_tokens", maxTokens.Int())
	}

	// Temperature
	if temp := root.Get("temperature"); temp.Exists() {
		_ = outDoc.Set("temperature", temp.Float())
	} else if topP := root.Get("top_p"); topP.Exists() { // Top P
		_ = outDoc.Set("top_p", topP.Float())
	}

	// Stop sequences -> stop
	if stopSequences := root.Get("stop_sequences"); stopSequences.Exists() {
		if stopSequences.IsArray() {
			var stops []string
			stopSequences.ForEach(func(_, value ojson.Result) bool {
				stops = append(stops, value.String())
				return true
			})
			if len(stops) > 0 {
				_ = outDoc.Set("stop", stops)
			}
		}
	}

	// Stream
	_ = outDoc.Set("stream", stream)

	// Thinking: Convert Claude thinking.budget_tokens to OpenAI reasoning_effort
	if thinkingConfig := root.Get("thinking"); thinkingConfig.Exists() && thinkingConfig.IsObject() {
		if thinkingType := thinkingConfig.Get("type"); thinkingType.Exists() {
			switch thinkingType.String() {
			case "enabled":
				if budgetTokens := thinkingConfig.Get("budget_tokens"); budgetTokens.Exists() {
					budget := int(budgetTokens.Int())
					if effort, ok := thinking.ConvertBudgetToLevel(budget); ok && effort != "" {
						_ = outDoc.Set("reasoning_effort", effort)
					}
				} else if v := root.Get("output_config.effort"); v.Exists() && v.Type == ojson.String && strings.TrimSpace(v.String()) != "" {
					// Some Claude-compatible clients pair manual thinking with output_config.effort.
					// Preserve that explicit level when there is no legacy token budget to map.
					_ = outDoc.Set("reasoning_effort", strings.ToLower(strings.TrimSpace(v.String())))
				} else {
					// No budget_tokens or explicit effort specified; preserve the enabled-thinking default.
					if effort, ok := thinking.ConvertBudgetToLevel(-1); ok && effort != "" {
						_ = outDoc.Set("reasoning_effort", effort)
					}
				}
			case "adaptive", "auto":
				// Adaptive thinking can carry an explicit effort in output_config.effort (Claude 4.6).
				// Pass through directly; ApplyThinking handles clamping to target model's levels.
				effort := ""
				if v := root.Get("output_config.effort"); v.Exists() && v.Type == ojson.String {
					effort = strings.ToLower(strings.TrimSpace(v.String()))
				}
				if effort != "" {
					_ = outDoc.Set("reasoning_effort", effort)
				} else {
					_ = outDoc.Set("reasoning_effort", string(thinking.LevelXHigh))
				}
			case "disabled":
				if effort, ok := thinking.ConvertBudgetToLevel(0); ok && effort != "" {
					_ = outDoc.Set("reasoning_effort", effort)
				}
			}
		}
	}

	// Process messages and system.
	messageCapacity := root.Get("messages.#").Int()
	if root.Get("system").Exists() {
		messageCapacity++
	}
	messageItems := translatorcommon.NewRawArrayItems(messageCapacity)

	// Handle system message first.
	systemContentItems := make([][]byte, 0, 2)
	appendSystemContent := func(content ojson.Result) {
		if !content.Exists() {
			return
		}
		if content.Type == ojson.String {
			if content.String() == "" || util.IsClaudeCodeAttributionSystemText(content.String()) {
				return
			}
			oldSystem := []byte(`{"type":"text","text":""}`)
			oldSystem, _ = ojson.SetBytes(oldSystem, "text", content.String())
			systemContentItems = append(systemContentItems, oldSystem)
			return
		}
		if content.IsArray() {
			content.ForEach(func(_, item ojson.Result) bool {
				if contentItem, ok := convertClaudeContentPart(item); ok {
					systemContentItems = append(systemContentItems, []byte(contentItem))
				}
				return true
			})
		}
	}

	if system := root.Get("system"); system.Exists() {
		appendSystemContent(system)
	}
	// Only add system message if it has content.
	if len(systemContentItems) > 0 {
		systemMessage := []byte(`{"role":"system","content":[]}`)
		systemMessage, _ = ojson.SetRawBytes(systemMessage, "content", translatorcommon.JoinRawArray(systemContentItems))
		messageItems = append(messageItems, systemMessage)
	}

	// Process Anthropic messages
	var midConversationToolDefs []ojson.Result
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		var pendingToolUseIDs []string
		var pendingSystemReminders [][]byte
		toolNameByID := make(map[string]string)

		messages.ForEach(func(_, message ojson.Result) bool {
			role := message.Get("role").String()
			contentResult := message.Get("content")
			if role == "system" {
				if reminderText, ok := translatorcommon.ClaudeMessageSystemReminderText(contentResult); ok {
					msgJSON := []byte(`{"role":"user","content":[{"type":"text","text":""}]}`)
					msgJSON, _ = ojson.SetBytes(msgJSON, "content.0.text", reminderText)
					if len(pendingToolUseIDs) > 0 {
						pendingSystemReminders = append(pendingSystemReminders, msgJSON)
					} else {
						messageItems = append(messageItems, msgJSON)
					}
				}
				if contentResult.IsArray() {
					contentResult.ForEach(func(_, part ojson.Result) bool {
						if part.Get("type").String() != "tool_addition" {
							return true
						}
						toolSpec := part.Get("tool")
						if toolSpec.Get("type").String() == "tool_definition" || toolSpec.Get("definition").IsObject() {
							if def := toolSpec.Get("definition"); def.IsObject() && def.Get("name").String() != "" {
								midConversationToolDefs = append(midConversationToolDefs, def)
							}
						}
						return true
					})
				}
				return true
			}

			// Handle content
			if contentResult.Exists() && contentResult.IsArray() {
				if role == "user" && len(pendingToolUseIDs) > 0 {
					contentResult = translatorcommon.AlignClaudeToolResults(contentResult, pendingToolUseIDs)
				}
				precedingToolCallsPending := len(pendingToolUseIDs) > 0
				pendingToolUseIDs = nil

				contentItems := make([][]byte, 0)
				var reasoningParts []string // Accumulate thinking text for reasoning_content
				var toolCalls []interface{}
				toolResults := make([][]byte, 0)       // Collect tool_result messages to emit after the main message
				relayedToolImages := make([][]byte, 0) // Images pulled out of tool_result content for user-message relay

				contentResult.ForEach(func(_, part ojson.Result) bool {
					partType := part.Get("type").String()

					switch partType {
					case "thinking":
						// Only map thinking to reasoning_content for assistant messages (security: prevent injection)
						if role == "assistant" {
							if !shouldMapClaudeThinkingToGPTReasoning(part, preserveThinkingBlocks) {
								return true
							}
							thinkingText := thinking.GetThinkingText(part)
							// Skip empty or whitespace-only thinking
							if strings.TrimSpace(thinkingText) != "" {
								reasoningParts = append(reasoningParts, thinkingText)
							}
						}
						// Ignore thinking in user/system roles (AC4)

					case "redacted_thinking":
						// Explicitly ignore redacted_thinking - never map to reasoning_content (AC2)

					case "text", "image", "document", "container_upload":
						if contentItem, ok := convertClaudeContentPart(part); ok {
							contentItems = append(contentItems, []byte(contentItem))
						} else if role == "user" && partType != "text" {
							drops.Drop(partType)
						}

					case "tool_use":
						// Only allow tool_use -> tool_calls for assistant messages (security: prevent injection).
						if role == "assistant" {
							toolUseID := part.Get("id").String()
							toolName := part.Get("name").String()
							if toolUseID != "" {
								pendingToolUseIDs = append(pendingToolUseIDs, toolUseID)
								if toolName != "" {
									toolNameByID[toolUseID] = toolName
								}
							}
							toolCallDoc := ojson.NewDocString(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
							_ = toolCallDoc.Set("id", toolUseID)
							_ = toolCallDoc.Set("function.name", toolName)

							// Convert input to arguments JSON string
							if input := part.Get("input"); input.Exists() {
								_ = toolCallDoc.Set("function.arguments", input.Raw)
							} else {
								_ = toolCallDoc.Set("function.arguments", "{}")
							}

							toolCalls = append(toolCalls, util.ParseGJSONBytesNoCopy(toolCallDoc.Bytes()).Value())
						}

					case "tool_result":
						// Collect tool_result to emit after the main message (ensures tool results follow tool_calls)
						toolUseID := part.Get("tool_use_id").String()
						toolResultDoc := ojson.NewDocString(`{"role":"tool","tool_call_id":"","content":""}`)
						_ = toolResultDoc.Set("tool_call_id", toolUseID)
						if toolName := toolNameByID[toolUseID]; toolName != "" {
							_ = toolResultDoc.Set("name", toolName)
						}
						toolResultContent, toolResultImages := convertClaudeToolResultContent(part.Get("content"))
						_ = toolResultDoc.Set("content", toolResultContent)
						relayedToolImages = append(relayedToolImages, toolResultImages...)
						toolResults = append(toolResults, toolResultDoc.Bytes())
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
				if role == "user" {
					drops.EndTurn(len(contentItems) + len(toolResults))
				}

				// Flush pending system reminders before new content if no tool_results responded to preceding calls
				if precedingToolCallsPending && !hasToolResults && len(pendingSystemReminders) > 0 {
					messageItems = append(messageItems, pendingSystemReminders...)
					pendingSystemReminders = nil
				}

				// OpenAI requires: tool messages MUST immediately follow the assistant message with tool_calls.
				// Therefore, we emit tool_result messages FIRST (they respond to the previous assistant's tool_calls),
				// then emit any queued system reminders, then emit the current message's content.
				messageItems = append(messageItems, toolResults...)

				// OpenAI tool messages cannot carry image parts, so images returned by a tool are
				// replayed as a user message directly after the tool results.
				if len(relayedToolImages) > 0 {
					relayItems := make([][]byte, 0, len(relayedToolImages)+1)
					noticeJSON := []byte(`{"type":"text","text":""}`)
					noticeJSON, _ = ojson.SetBytes(noticeJSON, "text", toolResultImageRelayNotice)
					relayItems = append(relayItems, noticeJSON)
					relayItems = append(relayItems, relayedToolImages...)

					if role == "user" && hasContent {
						// Merge into the current user message so the request keeps a single user turn.
						contentItems = append(relayItems, contentItems...)
					} else {
						relayJSON := []byte(`{"role":"user"}`)
						relayJSON, _ = ojson.SetRawBytes(relayJSON, "content", translatorcommon.JoinRawArray(relayItems))
						messageItems = append(messageItems, relayJSON)
					}
				}

				if len(pendingSystemReminders) > 0 {
					messageItems = append(messageItems, pendingSystemReminders...)
					pendingSystemReminders = nil
				}

				// For assistant messages: emit a single unified message with content, tool_calls, and reasoning_content
				// This avoids splitting into multiple assistant messages which breaks OpenAI tool-call adjacency
				if role == "assistant" {
					if hasContent || hasReasoning || hasToolCalls {
						msgDoc := ojson.NewDocString(`{"role":"assistant"}`)

						// Add content (as array if we have items, empty string if reasoning-only)
						if hasContent {
							_ = msgDoc.SetRawBytes("content", translatorcommon.JoinRawArray(contentItems))
						} else {
							// Ensure content field exists for OpenAI compatibility
							_ = msgDoc.Set("content", "")
						}

						// Add reasoning_content if present
						if hasReasoning {
							_ = msgDoc.Set("reasoning_content", reasoningContent)
						}

						// Add tool_calls if present (in same message as content)
						if hasToolCalls {
							_ = msgDoc.Set("tool_calls", toolCalls)
						}

						messageItems = append(messageItems, msgDoc.Bytes())
					}
				} else {
					// For non-assistant roles: emit content message if we have content
					// If the message only contains tool_results (no text/image), we still processed them above
					if hasContent {
						msgDoc := ojson.NewDocString(`{"role":""}`)
						_ = msgDoc.Set("role", role)

						_ = msgDoc.SetRawBytes("content", translatorcommon.JoinRawArray(contentItems))
						messageItems = append(messageItems, msgDoc.Bytes())
					} else if hasToolResults && !hasContent {
						// tool_results already emitted above, no additional user message needed
					}
				}

			} else if contentResult.Exists() && contentResult.Type == ojson.String {
				// Simple string content
				msgDoc := ojson.NewDocString(`{"role":"","content":""}`)
				_ = msgDoc.Set("role", role)
				_ = msgDoc.Set("content", contentResult.String())
				messageItems = append(messageItems, msgDoc.Bytes())
			}

			return true
		})
		if len(pendingSystemReminders) > 0 {
			messageItems = append(messageItems, pendingSystemReminders...)
		}
	}

	// Set messages.
	if len(messageItems) > 0 {
		messageItems = translatorcommon.AlignOpenAIToolCallMessages(messageItems)
		translatorcommon.SetRawArrayItemsDoc(outDoc, "messages", messageItems)
	}

	// Process tools - convert Anthropic tools (and mid-conversation tool_addition definitions) to OpenAI functions
	var toolItems [][]byte
	var toolIndexByName map[string]int
	if len(midConversationToolDefs) > 0 {
		toolIndexByName = make(map[string]int)
	}
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		tools.ForEach(func(_, tool ojson.Result) bool {
			openAIToolJSON := convertClaudeToolToOpenAI(tool)
			if toolIndexByName != nil {
				if name := tool.Get("name").String(); name != "" {
					toolIndexByName[name] = len(toolItems)
				}
			}
			toolItems = append(toolItems, openAIToolJSON)
			return true
		})
	}
	for _, def := range midConversationToolDefs {
		name := def.Get("name").String()
		converted := convertClaudeToolToOpenAI(def)
		if idx, exists := toolIndexByName[name]; exists {
			toolItems[idx] = converted
		} else {
			toolIndexByName[name] = len(toolItems)
			toolItems = append(toolItems, converted)
		}
	}
	if len(toolItems) > 0 {
		_ = outDoc.SetRawBytes("tools", translatorcommon.JoinRawArray(toolItems))
	}

	// Tool choice mapping - convert Anthropic tool_choice to OpenAI format
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() && toolChoice.Type != ojson.Null {
		choiceType := toolChoice.Get("type").String()
		if choiceType == "" && toolChoice.Type == ojson.String {
			choiceType = toolChoice.String()
		}
		switch choiceType {
		case "auto":
			_ = outDoc.Set("tool_choice", "auto")
		case "any":
			_ = outDoc.Set("tool_choice", "required")
		case "none":
			_ = outDoc.Set("tool_choice", "none")
		case "tool":
			// Specific tool choice
			toolName := toolChoice.Get("name").String()
			if toolName != "" {
				toolChoiceJSON := []byte(`{"type":"function","function":{"name":""}}`)
				toolChoiceJSON, _ = ojson.SetBytes(toolChoiceJSON, "function.name", toolName)
				_ = outDoc.SetRawBytes("tool_choice", toolChoiceJSON)
			} else {
				_ = outDoc.Set("tool_choice", "none")
			}
		default:
			// Fail closed: unrecognized tool_choice values must not turn into permission
			_ = outDoc.Set("tool_choice", "none")
		}

		if disableParallel := toolChoice.Get("disable_parallel_tool_use"); disableParallel.Type == ojson.True {
			_ = outDoc.Set("parallel_tool_calls", false)
		}
	}

	// Handle user parameter (for tracking)
	if user := root.Get("user"); user.Exists() {
		_ = outDoc.Set("user", user.String())
	}

	return outDoc.Bytes(), drops.Err()
}

func convertClaudeToolToOpenAI(tool ojson.Result) []byte {
	openAIToolDoc := ojson.NewDocString(`{"type":"function","function":{"name":"","description":""}}`)
	_ = openAIToolDoc.Set("function.name", tool.Get("name").String())
	_ = openAIToolDoc.Set("function.description", tool.Get("description").String())

	// Convert Anthropic input_schema to OpenAI function parameters
	if inputSchema := tool.Get("input_schema"); inputSchema.Exists() && inputSchema.Type != ojson.Null {
		_ = openAIToolDoc.Set("function.parameters", normalizeObjectSchemaProperties(inputSchema.Value()))
	} else {
		_ = openAIToolDoc.SetRaw("function.parameters", `{"type":"object","properties":{}}`)
	}
	return openAIToolDoc.Bytes()
}

func normalizeObjectSchemaProperties(schema any) any {
	switch value := schema.(type) {
	case bool:
		// JSON Schema boolean subschemas (true/false) are valid, but strict OpenAPI 3.0
		// upstream validators reject boolean subschemas.
		// Normalize `true` (accept anything) to an empty object schema `{}`.
		// Preserve `false` (reject all) to avoid turning rejection constraints into open schemas.
		if value {
			return map[string]any{}
		}
		return value
	case map[string]any:
		if schemaType, ok := value["type"].(string); ok && schemaType == "object" {
			if _, ok := value["properties"]; !ok {
				value["properties"] = map[string]any{}
			}
		}
		if patternVal, ok := value["pattern"].(string); ok && util.HasUnsupportedUnicodePropertyEscape(patternVal) {
			delete(value, "pattern")
		}

		// Inspect regex keys under patternProperties
		if patternProps, ok := value["patternProperties"].(map[string]any); ok {
			for patternKey, subSchema := range patternProps {
				if util.HasUnsupportedUnicodePropertyEscape(patternKey) {
					delete(patternProps, patternKey)
				} else {
					patternProps[patternKey] = normalizeObjectSchemaProperties(subSchema)
				}
			}
		}

		for _, mapKey := range util.SchemaMapKeywords {
			if mapKey == "patternProperties" {
				continue
			}
			if subMap, ok := value[mapKey].(map[string]any); ok {
				for subKey, subSchema := range subMap {
					subMap[subKey] = normalizeObjectSchemaProperties(subSchema)
				}
			}
		}

		for _, valKey := range util.SchemaValueKeywords {
			if val, exists := value[valKey]; exists {
				switch sub := val.(type) {
				case bool:
					// Normalize boolean subschemas (e.g. items: true), but preserve boolean
					// additionalProperties (false/true) required by OpenAI structured outputs.
					if valKey != "additionalProperties" && sub {
						value[valKey] = map[string]any{}
					}
				case map[string]any:
					value[valKey] = normalizeObjectSchemaProperties(sub)
				case []any:
					for i, item := range sub {
						sub[i] = normalizeObjectSchemaProperties(item)
					}
				}
			}
		}
		return value
	case []any:
		for i, child := range value {
			value[i] = normalizeObjectSchemaProperties(child)
		}
		return value
	default:
		return schema
	}
}

func shouldMapClaudeThinkingToGPTReasoning(part ojson.Result, preserveThinkingBlocks ...bool) bool {
	preserveThinking := len(preserveThinkingBlocks) > 0 && preserveThinkingBlocks[0]
	if preserveThinking {
		return true
	}

	signature := part.Get("signature")
	if !signature.Exists() || strings.TrimSpace(signature.String()) == "" {
		return false
	}
	_, ok := sigcompat.CompatibleSignatureForProvider(sigcompat.SignatureProviderGPT, signature.String())
	return ok
}

func convertClaudeContentPart(part ojson.Result) (string, bool) {
	partType := part.Get("type").String()

	switch partType {
	case "text":
		text := part.Get("text").String()
		if strings.TrimSpace(text) == "" || util.IsClaudeCodeAttributionSystemText(text) {
			return "", false
		}
		textContent := []byte(`{"type":"text","text":""}`)
		textContent, _ = ojson.SetBytes(textContent, "text", text)
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
		imageContent, _ = ojson.SetBytes(imageContent, "image_url.url", imageURL)

		return string(imageContent), true

	case "document", "container_upload":
		return convertClaudeFilePartToOpenAI(part)

	default:
		return "", false
	}
}

// convertClaudeFilePartToOpenAI emits an OpenAI file part for inline base64 bytes.
// A file id carries none, so it stays unconverted and the caller reports it.
func convertClaudeFilePartToOpenAI(part ojson.Result) (string, bool) {
	source := part.Get("source")
	mimeType := source.Get("media_type").String()

	var data []byte
	if source.Get("type").String() == "base64" {
		decoded, errDecode := base64.StdEncoding.DecodeString(strings.TrimSpace(source.Get("data").String()))
		if errDecode != nil {
			return "", false
		}
		data = decoded
	}

	if len(data) == 0 {
		return "", false
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	fileContentDoc := ojson.NewDocString(`{"type":"file","file":{"filename":"","file_data":""}}`)
	_ = fileContentDoc.Set("file.filename", part.Get("filename").String())
	_ = fileContentDoc.Set("file.file_data", base64.EncodeDataURL(mimeType, data))
	return fileContentDoc.String(), true
}

// toolResultImagePlaceholder keeps the OpenAI tool message non-empty when a Claude
// tool_result carried nothing but images.
const toolResultImagePlaceholder = "[Tool returned image content; the images follow in the next user message.]"

// toolResultImageRelayNotice labels the user message that carries relayed tool images.
const toolResultImageRelayNotice = "Images returned by the preceding tool call(s):"

func convertClaudeToolResultContent(content ojson.Result) (string, [][]byte) {
	if !content.Exists() {
		return "", nil
	}

	if content.Type == ojson.String {
		return content.String(), nil
	}

	if content.IsArray() {
		var parts []string
		var images [][]byte
		content.ForEach(func(_, item ojson.Result) bool {
			switch {
			case item.Type == ojson.String:
				parts = append(parts, item.String())
			case item.IsObject() && item.Get("type").String() == "text":
				parts = append(parts, item.Get("text").String())
			case item.IsObject() && item.Get("type").String() == "image":
				if contentItem, ok := convertClaudeContentPart(item); ok {
					images = append(images, []byte(contentItem))
				} else {
					parts = append(parts, item.Raw)
				}
			case item.IsObject() && item.Get("text").Exists() && item.Get("text").Type == ojson.String:
				parts = append(parts, item.Get("text").String())
			default:
				parts = append(parts, item.Raw)
			}
			return true
		})

		joined := strings.Join(parts, "\n\n")
		if strings.TrimSpace(joined) == "" {
			if len(images) > 0 {
				return toolResultImagePlaceholder, images
			}
			return content.Raw, nil
		}
		return joined, images
	}

	if content.IsObject() {
		if content.Get("type").String() == "image" {
			if contentItem, ok := convertClaudeContentPart(content); ok {
				return toolResultImagePlaceholder, [][]byte{[]byte(contentItem)}
			}
		}
		if text := content.Get("text"); text.Exists() && text.Type == ojson.String {
			return text.String(), nil
		}
		return content.Raw, nil
	}

	return content.Raw, nil
}
