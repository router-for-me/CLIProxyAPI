// Package openai provides request translation functionality for OpenAI to Claude Code API compatibility.
// It handles parsing and transforming OpenAI Chat Completions API requests into Claude Code API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between OpenAI API format and Claude Code API's expected format.
package chat_completions

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const (
	defaultClaudeChatMaxTokens = 32000
	defaultFableChatMaxTokens  = 64000
)

// ConvertOpenAIRequestToClaude parses and transforms an OpenAI Chat Completions API request into Claude Code API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Claude Code API.
// The function performs comprehensive transformation including:
// 1. Model name mapping and parameter extraction (max_tokens, top_p, etc.)
// 2. Message content conversion from OpenAI to Claude Code format
// 3. Tool call and tool result handling with proper ID mapping
// 4. Image data conversion from OpenAI data URLs to Claude Code base64 format
// 5. Stop sequence and streaming configuration handling
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI API
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in Claude Code API format
func ConvertOpenAIRequestToClaude(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertOpenAIRequestToClaude(modelName, inputRawJSON, stream, false)

}

// ConvertOpenAIRequestToClaudeWithCompat preserves assistant reasoning content
// as an unsigned thinking block for configured compatibility endpoints.
func ConvertOpenAIRequestToClaudeWithCompat(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertOpenAIRequestToClaude(modelName, inputRawJSON, stream, true)
}

func convertOpenAIRequestToClaude(modelName string, inputRawJSON []byte, stream, preserveEmptyThinkingBlocks bool) ([]byte, error) {
	rawJSON := inputRawJSON
	var drops common.UserTurnDrops

	userID := common.DeriveClaudeUserID(rawJSON)

	// Base Claude Code API template with default max_tokens value
	outDoc := ojson.NewDoc([]byte(`{"model":"","max_tokens":32000,"messages":[],"metadata":{}}`))
	_ = outDoc.Set("metadata.user_id", userID)
	_ = outDoc.Set("max_tokens", defaultClaudeChatMaxTokensForModel(modelName))

	root := util.ParseGJSONBytesNoCopy(rawJSON)

	// Convert OpenAI reasoning_effort to Claude thinking config.
	if v := root.Get("reasoning_effort"); v.Exists() {
		effort := strings.ToLower(strings.TrimSpace(v.String()))
		if effort != "" {
			mi := registry.LookupModelInfo(modelName, "claude")
			supportsAdaptive := mi != nil && mi.Thinking != nil && len(mi.Thinking.Levels) > 0
			supportsMax := supportsAdaptive && thinking.HasLevel(mi.Thinking.Levels, string(thinking.LevelMax))

			// Claude 4.6 supports adaptive thinking with output_config.effort.
			// MapToClaudeEffort normalizes levels (e.g. minimal→low, xhigh→high) to avoid
			// validation errors since validate treats same-provider unsupported levels as errors.
			if supportsAdaptive {
				switch effort {
				case "none":
					_ = outDoc.Set("thinking.type", "disabled")
					_ = outDoc.Delete("thinking.budget_tokens")
					_ = outDoc.Delete("output_config.effort")
				case "auto":
					_ = outDoc.Set("thinking.type", "adaptive")
					_ = outDoc.Delete("thinking.budget_tokens")
					_ = outDoc.Delete("output_config.effort")
				default:
					if !thinking.HasLevel(mi.Thinking.Levels, effort) {
						if mapped, ok := thinking.MapToClaudeEffort(effort, supportsMax); ok {
							effort = mapped
						}
					}
					_ = outDoc.Set("thinking.type", "adaptive")
					_ = outDoc.Delete("thinking.budget_tokens")
					_ = outDoc.Set("output_config.effort", effort)
				}
			} else {
				// Legacy/manual thinking (budget_tokens).
				budget, ok := thinking.ConvertLevelToBudget(effort)
				if ok {
					switch budget {
					case 0:
						_ = outDoc.Set("thinking.type", "disabled")
					case -1:
						_ = outDoc.Set("thinking.type", "enabled")
					default:
						if budget > 0 {
							_ = outDoc.Set("thinking.type", "enabled")
							_ = outDoc.Set("thinking.budget_tokens", budget)
						}
					}
				}
			}
		}
	}

	// Model mapping to specify which Claude Code model to use
	_ = outDoc.Set("model", modelName)

	// Max tokens configuration with fallback to default value.
	// OpenAI Chat Completions deprecated max_tokens in favor of
	// max_completion_tokens, so accept either spelling.
	if maxTokens := firstExisting(root.Get("max_tokens"), root.Get("max_completion_tokens")); maxTokens.Exists() {
		_ = outDoc.Set("max_tokens", maxTokens.Int())
	}

	// Top P setting for nucleus sampling.
	if topP := root.Get("top_p"); topP.Exists() {
		_ = outDoc.Set("top_p", topP.Float())
	}

	// Stop sequences configuration for custom termination conditions
	if stop := root.Get("stop"); stop.Exists() {
		if stop.IsArray() {
			var stopSequences []string
			stop.ForEach(func(_, value ojson.Result) bool {
				stopSequences = append(stopSequences, value.String())
				return true
			})
			if len(stopSequences) > 0 {
				_ = outDoc.Set("stop_sequences", stopSequences)
			}
		} else {
			_ = outDoc.Set("stop_sequences", []string{stop.String()})
		}
	}

	// Stream configuration to enable or disable streaming responses
	_ = outDoc.Set("stream", stream)

	systemBlocks := make([][]byte, 0)
	messageBlocks := make([][]byte, 0)

	// Process messages and transform them to Claude Code format
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		lastToolMessage := map[string]ojson.Result{}
		messages.ForEach(func(_, message ojson.Result) bool {
			if message.Get("role").String() == "tool" {
				rawID := message.Get("tool_call_id").String()
				if rawID != "" {
					lastToolMessage[rawID] = message
				}
			}
			return true
		})
		emittedToolResults := map[string]struct{}{}

		messageAccumulator := common.NewClaudeMessageAccumulator(int(root.Get("messages.#").Int()))
		messages.ForEach(func(_, message ojson.Result) bool {
			role := message.Get("role").String()
			contentResult := message.Get("content")

			switch role {
			// Developer messages rank with system messages in OpenAI's instruction
			// hierarchy, so both become top-level Claude system blocks. Dropping the
			// developer role, as this translator used to, silently removed operator
			// instructions from the upstream request.
			case "system", "developer":
				systemStart := len(systemBlocks)
				if contentResult.Exists() && contentResult.Type == ojson.String && strings.TrimSpace(contentResult.String()) != "" {
					textPart := []byte(`{"type":"text","text":""}`)
					textPart, _ = ojson.SetBytes(textPart, "text", contentResult.String())
					textPart = common.AttachCacheControl(textPart, message)
					systemBlocks = append(systemBlocks, textPart)
				} else if contentResult.Exists() && contentResult.IsArray() {
					contentResult.ForEach(func(_, part ojson.Result) bool {
						if part.Get("type").String() == "text" {
							txt := part.Get("text").String()
							if strings.TrimSpace(txt) == "" {
								if part.Get("cache_control").Exists() && len(systemBlocks) > systemStart {
									prevIdx := len(systemBlocks) - 1
									if !ojson.GetBytesNoCopy(systemBlocks[prevIdx], "cache_control").Exists() {
										systemBlocks[prevIdx] = common.AttachCacheControl(systemBlocks[prevIdx], part)
									}
								}
								return true
							}
							textPart := []byte(`{"type":"text","text":""}`)
							textPart, _ = ojson.SetBytes(textPart, "text", txt)
							textPart = common.AttachCacheControl(textPart, part)
							systemBlocks = append(systemBlocks, textPart)
						}
						return true
					})
					// Message-level cache_control applies to the last system block from this message.
					if message.Get("cache_control").Exists() && len(systemBlocks) > systemStart {
						lastIdx := len(systemBlocks) - 1
						if !ojson.GetBytesNoCopy(systemBlocks[lastIdx], "cache_control").Exists() {
							systemBlocks[lastIdx] = common.AttachCacheControl(systemBlocks[lastIdx], message)
						}
					}
				}
			case "user", "assistant":
				contentBlocks := make([][]byte, 0, 4)
				if preserveEmptyThinkingBlocks && role == "assistant" {
					if reasoningContent := message.Get("reasoning_content"); reasoningContent.Type == ojson.String && strings.TrimSpace(reasoningContent.String()) != "" {
						part := []byte(`{"type":"thinking","thinking":"","signature":""}`)
						part, _ = ojson.SetBytes(part, "thinking", reasoningContent.String())
						contentBlocks = append(contentBlocks, part)
					}
				}

				// Handle content based on its type
				if contentResult.Exists() && contentResult.Type == ojson.String && strings.TrimSpace(contentResult.String()) != "" {
					part := []byte(`{"type":"text","text":""}`)
					part, _ = ojson.SetBytes(part, "text", contentResult.String())
					contentBlocks = append(contentBlocks, part)
				} else if contentResult.Exists() && contentResult.IsArray() {
					contentResult.ForEach(func(_, part ojson.Result) bool {
						if claudePart := convertOpenAIContentPartToClaudePart(part); claudePart != "" {
							contentBlocks = append(contentBlocks, []byte(claudePart))
						} else if partType := part.Get("type").String(); partType == "text" {
							if part.Get("cache_control").Exists() && len(contentBlocks) > 0 {
								prevIdx := len(contentBlocks) - 1
								if !ojson.GetBytesNoCopy(contentBlocks[prevIdx], "cache_control").Exists() {
									contentBlocks[prevIdx] = common.AttachCacheControl(contentBlocks[prevIdx], part)
								}
							}
						} else if role == "user" && (partType == "file" || partType == "input_audio") {
							drops.Drop(partType)
						}
						return true
					})
				}

				if role == "user" {
					drops.EndTurn(len(contentBlocks))
				}

				// Handle tool calls (for assistant messages)
				if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() && role == "assistant" {
					toolCalls.ForEach(func(_, toolCall ojson.Result) bool {
						if toolCall.Get("type").String() == "function" {
							toolCallID := toolCall.Get("id").String()
							if toolCallID == "" {
								toolCallID = common.GenerateClaudeToolCallID()
							}
							toolCallID = util.SanitizeClaudeToolID(toolCallID)

							function := toolCall.Get("function")
							toolUseDoc := ojson.NewDoc([]byte(`{"type":"tool_use","id":"","name":"","input":{}}`))
							_ = toolUseDoc.Set("id", toolCallID)
							_ = toolUseDoc.Set("name", util.SanitizeClaudeFunctionName(function.Get("name").String()))

							// Parse arguments for the tool call
							if args := function.Get("arguments"); args.Exists() {
								argsStr := args.String()
								if argsStr != "" && ojson.Valid(argsStr) {
									argsJSON := ojson.Parse(argsStr)
									if argsJSON.IsObject() {
										_ = toolUseDoc.SetRaw("input", argsJSON.Raw)
									} else {
										_ = toolUseDoc.SetRaw("input", "{}")
									}
								} else {
									_ = toolUseDoc.SetRaw("input", "{}")
								}
							} else {
								_ = toolUseDoc.SetRaw("input", "{}")
							}

							contentBlocks = append(contentBlocks, toolUseDoc.Bytes())
						}
						return true
					})
				}

				msgDoc := ojson.NewDoc([]byte(`{"role":"","content":[]}`))
				_ = msgDoc.Set("role", role)
				_ = msgDoc.SetRawBytes("content", common.JoinRawArray(contentBlocks))
				msg := common.AttachMessageCacheControl(msgDoc.Bytes(), message)
				messageAccumulator.Append(msg)

			case "tool":
				// Handle tool result messages conversion
				rawID := message.Get("tool_call_id").String()
				toolCallID := util.SanitizeClaudeToolID(rawID)
				if rawID != "" {
					if _, exists := emittedToolResults[rawID]; exists {
						return true
					}
					emittedToolResults[rawID] = struct{}{}
				}

				targetMsg := message
				if rawID != "" {
					if lastMsg, exists := lastToolMessage[rawID]; exists {
						targetMsg = lastMsg
					}
				}
				toolContentResult := targetMsg.Get("content")

				msgDoc := ojson.NewDoc([]byte(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"","content":""}]}`))
				_ = msgDoc.Set("content.0.tool_use_id", toolCallID)
				toolResultContent, toolResultContentRaw := convertOpenAIToolResultContent(toolContentResult)
				if toolResultContentRaw {
					_ = msgDoc.SetRaw("content.0.content", toolResultContent)
				} else {
					_ = msgDoc.Set("content.0.content", toolResultContent)
				}
				// Anthropic rejects cache_control inside tool_result.content, so
				// part-level or message-level cache_control is hoisted onto the
				// tool_result block itself.
				msg := common.AttachToolMessageCacheControl(msgDoc.Bytes(), targetMsg)
				messageAccumulator.Append(msg)
			}
			return true
		})

		messageBlocks = messageAccumulator.Messages()
	}

	if formatInstruction := common.BuildClaudeStructuredOutputInstruction(root.Get("response_format")); formatInstruction != "" {
		systemBlock := []byte(`{"type":"text","text":""}`)
		systemBlock, _ = ojson.SetBytes(systemBlock, "text", formatInstruction)
		systemBlocks = append(systemBlocks, systemBlock)
	}

	// Decided per user turn, so neither a system prompt nor the blank turn added below hides an emptied turn.
	nothingLeftErr := drops.Err()

	// Preserve a minimal conversational turn for system-only inputs.
	// Claude payloads with top-level system instructions but no messages are risky for downstream validation.
	if len(messageBlocks) == 0 && len(systemBlocks) > 0 {
		messageBlocks = append(messageBlocks, []byte(`{"role":"user","content":[{"type":"text","text":""}]}`))
	}

	if len(systemBlocks) > 0 {
		_ = outDoc.SetRawBytes("system", common.JoinRawArray(systemBlocks))
	}
	if len(messageBlocks) > 0 {
		common.SetRawArrayItemsDoc(outDoc, "messages", messageBlocks)
	}

	// Tools mapping: OpenAI tools -> Claude Code tools
	allowedToolNames := make(map[string]struct{})
	isAllowedTools := false
	allowedMode := "auto"
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() && toolChoice.IsObject() && toolChoice.Get("type").String() == "allowed_tools" {
		isAllowedTools = true
		toolList := toolChoice.Get("allowed_tools.tools").Array()
		if len(toolList) == 0 {
			toolList = toolChoice.Get("tools").Array()
		}
		for _, t := range toolList {
			fnName := strings.TrimSpace(t.Get("function.name").String())
			if fnName == "" {
				fnName = strings.TrimSpace(t.Get("name").String())
			}
			if fnName != "" {
				allowedToolNames[fnName] = struct{}{}
				allowedToolNames[util.SanitizeClaudeFunctionName(fnName)] = struct{}{}
			}
		}
		modeVal := strings.ToLower(strings.TrimSpace(toolChoice.Get("allowed_tools.mode").String()))
		if modeVal == "" {
			modeVal = strings.ToLower(strings.TrimSpace(toolChoice.Get("mode").String()))
		}
		if modeVal != "" {
			allowedMode = modeVal
		}
	}

	var anthropicTools [][]byte
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() && len(tools.Array()) > 0 {
		tools.ForEach(func(_, tool ojson.Result) bool {
			if tool.Get("type").String() == "function" {
				function := tool.Get("function")
				fnName := function.Get("name").String()
				sanitizedFnName := util.SanitizeClaudeFunctionName(fnName)
				if isAllowedTools {
					if _, ok := allowedToolNames[fnName]; !ok {
						if _, okSanitized := allowedToolNames[sanitizedFnName]; !okSanitized {
							return true
						}
					}
				}
				toolDoc := ojson.NewDoc([]byte(`{"name":"","description":""}`))
				_ = toolDoc.Set("name", sanitizedFnName)
				_ = toolDoc.Set("description", function.Get("description").String())

				// Convert parameters schema for the tool
				if parameters := function.Get("parameters"); parameters.Exists() {
					_ = toolDoc.SetRawBytes("input_schema", util.NormalizeClaudeToolInputSchema([]byte(parameters.Raw)))
				} else if parameters := function.Get("parametersJsonSchema"); parameters.Exists() {
					_ = toolDoc.SetRawBytes("input_schema", util.NormalizeClaudeToolInputSchema([]byte(parameters.Raw)))
				} else {
					_ = toolDoc.SetRawBytes("input_schema", util.NormalizeClaudeToolInputSchema(nil))
				}
				strict := function.Get("strict")
				if !strict.Exists() {
					strict = tool.Get("strict")
				}
				if strict.Exists() {
					if strict.Type == ojson.True {
						_ = toolDoc.Set("strict", true)
					} else if strict.Type == ojson.False {
						_ = toolDoc.Set("strict", false)
					}
				}
				anthropicTool := common.AttachCacheControl(toolDoc.Bytes(), tool)
				if !ojson.GetBytesNoCopy(anthropicTool, "cache_control").Exists() {
					anthropicTool = common.AttachCacheControl(anthropicTool, function)
				}

				anthropicTools = append(anthropicTools, anthropicTool)
			}
			return true
		})

		if len(anthropicTools) > 0 {
			_ = outDoc.SetRawBytes("tools", common.JoinRawArray(anthropicTools))
		} else {
			_ = outDoc.Delete("tools")
		}
	}

	// Tool choice mapping from OpenAI format to Claude Code format
	if isAllowedTools {
		if len(anthropicTools) == 0 {
			_ = outDoc.SetRaw("tool_choice", `{"type":"none"}`)
		} else if allowedMode == "required" {
			_ = outDoc.SetRaw("tool_choice", `{"type":"any"}`)
		} else {
			_ = outDoc.SetRaw("tool_choice", `{"type":"auto"}`)
		}
	} else if toolChoice := root.Get("tool_choice"); toolChoice.Exists() && toolChoice.Type != ojson.Null {
		switch toolChoice.Type {
		case ojson.String:
			choice := toolChoice.String()
			switch choice {
			case "none":
				_ = outDoc.SetRaw("tool_choice", `{"type":"none"}`)
			case "auto":
				_ = outDoc.SetRaw("tool_choice", `{"type":"auto"}`)
			case "required":
				_ = outDoc.SetRaw("tool_choice", `{"type":"any"}`)
			}
		case ojson.JSON:
			choiceType := toolChoice.Get("type").String()
			switch choiceType {
			case "none":
				_ = outDoc.SetRaw("tool_choice", `{"type":"none"}`)
			case "auto":
				_ = outDoc.SetRaw("tool_choice", `{"type":"auto"}`)
			case "required", "any":
				_ = outDoc.SetRaw("tool_choice", `{"type":"any"}`)
			case "function":
				functionName := toolChoice.Get("function.name").String()
				if functionName == "" {
					functionName = toolChoice.Get("name").String()
				}
				if functionName != "" {
					toolChoiceJSON := []byte(`{"type":"tool","name":""}`)
					toolChoiceJSON, _ = ojson.SetBytes(toolChoiceJSON, "name", util.SanitizeClaudeFunctionName(functionName))
					_ = outDoc.SetRawBytes("tool_choice", toolChoiceJSON)
				} else {
					_ = outDoc.SetRaw("tool_choice", `{"type":"none"}`)
				}
			}
		default:
		}
	}

	if parallelToolCalls := root.Get("parallel_tool_calls"); parallelToolCalls.Type == ojson.False {
		if outDoc.Get("tool_choice").Exists() {
			if outDoc.Get("tool_choice.type").String() != "none" {
				_ = outDoc.Set("tool_choice.disable_parallel_tool_use", true)
			}
		} else if outDoc.Get("tools").Exists() {
			_ = outDoc.SetRaw("tool_choice", `{"type":"auto","disable_parallel_tool_use":true}`)
		}
	}

	return thinking.ApplyTranslatedSummaryToClaude(outDoc.Bytes(), rawJSON, "openai", modelName), nothingLeftErr
}

func convertOpenAIContentPartToClaudePartRaw(part ojson.Result) []byte {
	switch part.Get("type").String() {
	case "text":
		txt := part.Get("text").String()
		if strings.TrimSpace(txt) == "" {
			return nil
		}
		textPart := []byte(`{"type":"text","text":""}`)
		textPart, _ = ojson.SetBytes(textPart, "text", txt)
		return textPart

	case "image_url":
		return []byte(convertOpenAIImageURLToClaudePart(part.Get("image_url.url").String()))

	case "file":
		fileData := part.Get("file.file_data").String()
		if strings.HasPrefix(fileData, "data:") {
			semicolonIdx := strings.Index(fileData, ";")
			commaIdx := strings.Index(fileData, ",")
			if semicolonIdx != -1 && commaIdx != -1 && commaIdx > semicolonIdx {
				mediaType := strings.TrimPrefix(fileData[:semicolonIdx], "data:")
				data := fileData[commaIdx+1:]
				docPartDoc := ojson.NewDoc([]byte(`{"type":"document","source":{"type":"base64","media_type":"","data":""}}`))
				_ = docPartDoc.Set("source.media_type", mediaType)
				_ = docPartDoc.Set("source.data", data)
				return docPartDoc.Bytes()
			}
		}
	}
	return nil
}

func convertOpenAIContentPartToClaudePart(part ojson.Result) string {
	claudePart := convertOpenAIContentPartToClaudePartRaw(part)
	if len(claudePart) == 0 {
		return ""
	}
	return string(common.AttachCacheControl(claudePart, part))
}

func convertOpenAIImageURLToClaudePart(imageURL string) string {
	if imageURL == "" {
		return ""
	}

	if strings.HasPrefix(imageURL, "data:") {
		parts := strings.SplitN(imageURL, ",", 2)
		if len(parts) != 2 {
			return ""
		}

		mediaTypePart := strings.SplitN(parts[0], ";", 2)[0]
		mediaType := strings.TrimPrefix(mediaTypePart, "data:")
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}

		imagePartDoc := ojson.NewDoc([]byte(`{"type":"image","source":{"type":"base64","media_type":"","data":""}}`))
		_ = imagePartDoc.Set("source.media_type", mediaType)
		_ = imagePartDoc.Set("source.data", parts[1])
		return imagePartDoc.String()
	}

	imagePart := []byte(`{"type":"image","source":{"type":"url","url":""}}`)
	imagePart, _ = ojson.SetBytes(imagePart, "source.url", imageURL)
	return string(imagePart)
}

func convertOpenAIToolResultContent(content ojson.Result) (string, bool) {
	if !content.Exists() {
		return "", false
	}

	if content.Type == ojson.String {
		return content.String(), false
	}

	if content.IsArray() {
		claudeParts := make([][]byte, 0, 4)
		droppedEmptyText := false
		content.ForEach(func(_, part ojson.Result) bool {
			if part.Type == ojson.String {
				if strings.TrimSpace(part.String()) == "" {
					droppedEmptyText = true
					return true
				}
				textPart := []byte(`{"type":"text","text":""}`)
				textPart, _ = ojson.SetBytes(textPart, "text", part.String())
				claudeParts = append(claudeParts, textPart)
				return true
			}

			if claudePart := convertOpenAIContentPartToClaudePartRaw(part); len(claudePart) > 0 {
				claudeParts = append(claudeParts, claudePart)
			} else if part.Get("type").String() == "text" {
				droppedEmptyText = true
			}
			return true
		})

		if len(claudeParts) > 0 || len(content.Array()) == 0 {
			return string(common.JoinRawArray(claudeParts)), true
		}
		if droppedEmptyText {
			return "", false
		}

		return content.Raw, false
	}

	if content.IsObject() {
		if claudePart := convertOpenAIContentPartToClaudePartRaw(content); len(claudePart) > 0 {
			return string(common.JoinRawArray([][]byte{claudePart})), true
		}
		return content.Raw, false
	}

	return content.Raw, false
}

func defaultClaudeChatMaxTokensForModel(modelName string) int {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	maxTokens := defaultClaudeChatMaxTokens
	if strings.Contains(normalized, "fable") {
		maxTokens = defaultFableChatMaxTokens
	}
	info := registry.LookupModelInfo(modelName, "claude")
	if info == nil || info.MaxCompletionTokens <= 0 {
		return maxTokens
	}
	// Fable keeps its conservative omitted-field ceiling. Every other registered
	// model uses its output limit, including when that limit is above the
	// historical 32000 default.
	if strings.Contains(normalized, "fable") && info.MaxCompletionTokens >= maxTokens {
		return maxTokens
	}
	return info.MaxCompletionTokens
}

// firstExisting returns the first non-null result that exists, or an empty result.
func firstExisting(values ...ojson.Result) ojson.Result {
	for _, value := range values {
		if value.Exists() && value.Type != ojson.Null {
			return value
		}
	}
	return ojson.Result{}
}
