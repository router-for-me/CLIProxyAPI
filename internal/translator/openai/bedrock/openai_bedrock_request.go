// Package bedrock provides request/response translation between OpenAI format
// and AWS Bedrock Converse API format.
package bedrock

import (
	"strings"

	openairesponses "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/openai/openai/responses"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertOpenAIResponsesRequestToBedrock converts an OpenAI Responses-format
// request to Bedrock by composing:
// OpenAI Responses -> OpenAI Chat Completions -> Bedrock Converse.
func ConvertOpenAIResponsesRequestToBedrock(modelName string, inputRawJSON []byte, stream bool) []byte {
	chatCompletionsJSON := openairesponses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions(modelName, inputRawJSON, stream)
	return ConvertOpenAIRequestToBedrock(modelName, chatCompletionsJSON, stream)
}

// ConvertOpenAIRequestToBedrock converts an OpenAI Chat Completions request
// to the AWS Bedrock Converse API format.
func ConvertOpenAIRequestToBedrock(modelName string, inputRawJSON []byte, _ bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)

	// Start with the basic Converse API structure.
	out := []byte(`{"inferenceConfig":{}}`)

	// inferenceConfig
	// Note: thinking is incompatible with temperature, topP, or topK modifications.
	isThinking := root.Get("thinking").Exists()

	if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "inferenceConfig.maxTokens", maxTokens.Int())
	}
	if !isThinking {
		if temp := root.Get("temperature"); temp.Exists() {
			out, _ = sjson.SetBytes(out, "inferenceConfig.temperature", temp.Float())
		}
		if topP := root.Get("top_p"); topP.Exists() {
			out, _ = sjson.SetBytes(out, "inferenceConfig.topP", topP.Float())
		}
	}
	if stopSeqs := root.Get("stop"); stopSeqs.Exists() {
		var stops []string
		if stopSeqs.IsArray() {
			stopSeqs.ForEach(func(_, v gjson.Result) bool {
				stops = append(stops, v.String())
				return true
			})
		} else if stopSeqs.Type == gjson.String {
			stops = []string{stopSeqs.String()}
		}
		if len(stops) > 0 {
			out, _ = sjson.SetBytes(out, "inferenceConfig.stopSequences", stops)
		}
	}

	// system
	// Bedrock Converse requires system as an array of {text:"..."} objects.
	var systemParts [][]byte
	messages := root.Get("messages")
	if messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			if msg.Get("role").String() == "system" {
				content := msg.Get("content")
				if content.Type == gjson.String && content.String() != "" {
					part := []byte(`{"text":""}`)
					part, _ = sjson.SetBytes(part, "text", content.String())
					systemParts = append(systemParts, part)
				} else if content.IsArray() {
					content.ForEach(func(_, item gjson.Result) bool {
						if item.Get("type").String() == "text" {
							text := item.Get("text").String()
							if text != "" {
								part := []byte(`{"text":""}`)
								part, _ = sjson.SetBytes(part, "text", text)
								systemParts = append(systemParts, part)
							}
						}
						return true
					})
				}
			}
			return true
		})
	}
	if len(systemParts) > 0 {
		systemJSON := []byte(`[]`)
		for _, p := range systemParts {
			systemJSON, _ = sjson.SetRawBytes(systemJSON, "-1", p)
		}
		out, _ = sjson.SetRawBytes(out, "system", systemJSON)
	}

	// messages
	// Only include non-system roles.
	convoJSON := []byte(`[]`)
	if messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			role := msg.Get("role").String()
			if role == "system" {
				return true // already handled
			}
			// Map "assistant" and "user"; tool -> user content
			bedrockRole := role
			if role == "tool" {
				bedrockRole = "user"
			}

			content := msg.Get("content")
			contentBlocksJSON := []byte(`[]`)

			buildTextBlock := func(text string) []byte {
				block := []byte(`{"text":""}`)
				block, _ = sjson.SetBytes(block, "text", text)
				return block
			}

			switch {
			case content.Type == gjson.String:
				text := content.String()
				if strings.TrimSpace(text) != "" {
					contentBlocksJSON, _ = sjson.SetRawBytes(contentBlocksJSON, "-1", buildTextBlock(text))
				}

			case content.IsArray():
				content.ForEach(func(_, part gjson.Result) bool {
					partType := part.Get("type").String()
					switch partType {
					case "text":
						text := part.Get("text").String()
						if strings.TrimSpace(text) != "" {
							contentBlocksJSON, _ = sjson.SetRawBytes(contentBlocksJSON, "-1", buildTextBlock(text))
						}
					case "image_url":
						// Convert base64 data-URI images
						url := part.Get("image_url.url").String()
						if format, b64, ok := parseDataURIImage(url); ok {
							imgBlock := []byte(`{"image":{"format":"","source":{"bytes":""}}}`)
							imgBlock, _ = sjson.SetBytes(imgBlock, "image.format", format)
							imgBlock, _ = sjson.SetBytes(imgBlock, "image.source.bytes", b64)
							contentBlocksJSON, _ = sjson.SetRawBytes(contentBlocksJSON, "-1", imgBlock)
						} else if strings.HasPrefix(strings.ToLower(strings.TrimSpace(url)), "http://") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(url)), "https://") {
							// Bedrock Converse image block requires bytes/s3 source.
							// Preserve external image URLs as textual hints instead of dropping them.
							contentBlocksJSON, _ = sjson.SetRawBytes(contentBlocksJSON, "-1", buildTextBlock("[Image: "+url+"]"))
						}
					case "tool_use":
						// OpenAI assistant tool_use -> toolUse block
						toolBlock := []byte(`{"toolUse":{"toolUseId":"","name":"","input":{}}}`)
						toolBlock, _ = sjson.SetBytes(toolBlock, "toolUse.toolUseId", part.Get("id").String())
						toolBlock, _ = sjson.SetBytes(toolBlock, "toolUse.name", part.Get("name").String())
						if input := part.Get("input"); input.Exists() {
							toolBlock, _ = sjson.SetRawBytes(toolBlock, "toolUse.input", []byte(input.Raw))
						}
						contentBlocksJSON, _ = sjson.SetRawBytes(contentBlocksJSON, "-1", toolBlock)
					case "tool_result":
						// OpenAI tool result content blocks
						resultBlock := []byte(`{"toolResult":{"toolUseId":"","content":[]}}`)
						resultBlock, _ = sjson.SetBytes(resultBlock, "toolResult.toolUseId", part.Get("tool_use_id").String())
						resultContent := part.Get("content")
						if resultContent.Type == gjson.String {
							txtBlock := []byte(`{"text":""}`)
							txtBlock, _ = sjson.SetBytes(txtBlock, "text", resultContent.String())
							resultBlock, _ = sjson.SetRawBytes(resultBlock, "toolResult.content.-1", txtBlock)
						}
						contentBlocksJSON, _ = sjson.SetRawBytes(contentBlocksJSON, "-1", resultBlock)
					}
					return true
				})
			}

			// For "tool" role (OpenAI tool result message)
			if role == "tool" {
				resultBlock := []byte(`{"toolResult":{"toolUseId":"","content":[]}}`)
				resultBlock, _ = sjson.SetBytes(resultBlock, "toolResult.toolUseId", msg.Get("tool_call_id").String())
				txt := content.String()
				if txt != "" {
					txtBlock := []byte(`{"text":""}`)
					txtBlock, _ = sjson.SetBytes(txtBlock, "text", txt)
					resultBlock, _ = sjson.SetRawBytes(resultBlock, "toolResult.content.-1", txtBlock)
				}
				contentBlocksJSON = []byte(`[]`)
				contentBlocksJSON, _ = sjson.SetRawBytes(contentBlocksJSON, "-1", resultBlock)
			}

			// OpenAI assistant tool_calls (flat form)
			if role == "assistant" {
				if toolCalls := msg.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
					toolCalls.ForEach(func(_, tc gjson.Result) bool {
						toolBlock := []byte(`{"toolUse":{"toolUseId":"","name":"","input":{}}}`)
						toolBlock, _ = sjson.SetBytes(toolBlock, "toolUse.toolUseId", tc.Get("id").String())
						toolBlock, _ = sjson.SetBytes(toolBlock, "toolUse.name", tc.Get("function.name").String())
						argsStr := tc.Get("function.arguments").String()
						if argsStr != "" && gjson.Valid(argsStr) {
							toolBlock, _ = sjson.SetRawBytes(toolBlock, "toolUse.input", []byte(argsStr))
						}
						contentBlocksJSON, _ = sjson.SetRawBytes(contentBlocksJSON, "-1", toolBlock)
						return true
					})
				}
			}

			if len(gjson.ParseBytes(contentBlocksJSON).Array()) == 0 {
				return true // skip empty messages
			}

			msgBlock := []byte(`{"role":"","content":[]}`)
			msgBlock, _ = sjson.SetBytes(msgBlock, "role", bedrockRole)
			msgBlock, _ = sjson.SetRawBytes(msgBlock, "content", contentBlocksJSON)
			convoJSON, _ = sjson.SetRawBytes(convoJSON, "-1", msgBlock)
			return true
		})
	}
	out, _ = sjson.SetRawBytes(out, "messages", convoJSON)

	// tools
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		toolSpecsJSON := []byte(`[]`)
		tools.ForEach(func(_, tool gjson.Result) bool {
			spec := []byte(`{"toolSpec":{"name":"","description":"","inputSchema":{"json":{}}}}`)
			spec, _ = sjson.SetBytes(spec, "toolSpec.name", tool.Get("function.name").String())
			spec, _ = sjson.SetBytes(spec, "toolSpec.description", tool.Get("function.description").String())
			if params := tool.Get("function.parameters"); params.Exists() {
				spec, _ = sjson.SetRawBytes(spec, "toolSpec.inputSchema.json", []byte(params.Raw))
			}
			toolSpecsJSON, _ = sjson.SetRawBytes(toolSpecsJSON, "-1", spec)
			return true
		})
		toolConfigJSON := []byte(`{"tools":[]}`)
		toolConfigJSON, _ = sjson.SetRawBytes(toolConfigJSON, "tools", toolSpecsJSON)
		toolConfigJSON, enabled := applyBedrockToolChoice(toolConfigJSON, root.Get("tool_choice"))
		if enabled {
			out, _ = sjson.SetRawBytes(out, "toolConfig", toolConfigJSON)
		}
	}

	// thinking / reasoningConfig
	if thinking := root.Get("thinking"); thinking.Exists() {
		lowerModel := strings.ToLower(strings.TrimSpace(modelName))
		// Detect if we should use 'reasoningConfig' (for GLM-5/others) or 'thinking' (for Claude).
		// GLM-5 and new Bedrock reasoning-capable models (like Nova) use 'reasoningConfig' with 'maxReasoningEffort'.
		if strings.Contains(lowerModel, "glm") || strings.HasPrefix(lowerModel, "zai.") {
			budget := thinking.Get("budget_tokens").Int()
			effort := "medium"
			if budget <= 1024 {
				effort = "low"
			} else if budget >= 16000 {
				effort = "high"
			}

			// Use the native reasoningConfig structure defined in Bedrock Converse API for reasoning-capable models
			configObj := []byte(`{"type":"enabled","maxReasoningEffort":""}`)
			configObj, _ = sjson.SetBytes(configObj, "maxReasoningEffort", effort)
			out, _ = sjson.SetRawBytes(out, "additionalModelRequestFields.reasoningConfig", configObj)
		} else if strings.Contains(lowerModel, "anthropic.claude") || strings.HasPrefix(lowerModel, "claude") {
			out, _ = sjson.SetRawBytes(out, "additionalModelRequestFields.thinking", []byte(thinking.Raw))
		}
	}

	// model name is embedded in the endpoint URL by the executor, not in the body
	_ = modelName

	return out
}

func parseDataURIImage(url string) (format, data string, ok bool) {
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(url, "data:") {
		return "", "", false
	}

	metaAndData := strings.TrimPrefix(url, "data:")
	parts := strings.SplitN(metaAndData, ",", 2)
	if len(parts) != 2 {
		return "", "", false
	}

	meta := strings.TrimSpace(parts[0])
	data = strings.TrimSpace(parts[1])
	if meta == "" || data == "" {
		return "", "", false
	}
	if !strings.Contains(strings.ToLower(meta), ";base64") {
		return "", "", false
	}

	mediaType := strings.TrimSpace(strings.SplitN(meta, ";", 2)[0])
	typeParts := strings.SplitN(mediaType, "/", 2)
	if len(typeParts) != 2 {
		return "", "", false
	}
	format = strings.TrimSpace(typeParts[1])
	if format == "" {
		return "", "", false
	}
	return strings.ToLower(format), data, true
}

func applyBedrockToolChoice(toolConfigJSON []byte, toolChoice gjson.Result) ([]byte, bool) {
	if !toolChoice.Exists() {
		return toolConfigJSON, true
	}

	applyAuto := func(in []byte) []byte {
		out, _ := sjson.SetRawBytes(in, "toolChoice.auto", []byte(`{}`))
		return out
	}
	applyAny := func(in []byte) []byte {
		out, _ := sjson.SetRawBytes(in, "toolChoice.any", []byte(`{}`))
		return out
	}
	applyTool := func(in []byte, name string) []byte {
		out, _ := sjson.SetBytes(in, "toolChoice.tool.name", name)
		return out
	}

	if toolChoice.Type == gjson.String {
		switch strings.ToLower(strings.TrimSpace(toolChoice.String())) {
		case "none":
			return toolConfigJSON, false
		case "auto":
			return applyAuto(toolConfigJSON), true
		case "required":
			return applyAny(toolConfigJSON), true
		default:
			return toolConfigJSON, true
		}
	}

	if !toolChoice.IsObject() {
		return toolConfigJSON, true
	}

	choiceType := strings.ToLower(strings.TrimSpace(toolChoice.Get("type").String()))
	switch choiceType {
	case "none":
		return toolConfigJSON, false
	case "auto":
		return applyAuto(toolConfigJSON), true
	case "required", "any":
		return applyAny(toolConfigJSON), true
	case "function":
		name := strings.TrimSpace(toolChoice.Get("function.name").String())
		if name == "" {
			return toolConfigJSON, true
		}
		return applyTool(toolConfigJSON, name), true
	case "tool":
		name := strings.TrimSpace(toolChoice.Get("name").String())
		if name == "" {
			name = strings.TrimSpace(toolChoice.Get("tool.name").String())
		}
		if name == "" {
			name = strings.TrimSpace(toolChoice.Get("function.name").String())
		}
		if name == "" {
			return toolConfigJSON, true
		}
		return applyTool(toolConfigJSON, name), true
	default:
		return toolConfigJSON, true
	}
}
