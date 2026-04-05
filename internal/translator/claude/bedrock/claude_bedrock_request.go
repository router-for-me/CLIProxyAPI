// Package bedrock provides request translation between Anthropic Claude Messages API
// format and AWS Bedrock Converse API format.
package bedrock

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// reorderBedrockAssistantBlocks ensures toolUse blocks are consecutive in
// assistant messages. Bedrock Converse requires all toolUse blocks to appear
// without intervening text blocks; text must come before all toolUse blocks.
//
// Anthropic allows: [tool_use, text, tool_use]
// Bedrock rejects with: "tool_use ids were found without tool_result blocks immediately after"
// After reorder: [text, tool_use, tool_use]
func reorderBedrockAssistantBlocks(contentBlocks []byte) []byte {
	arr := gjson.ParseBytes(contentBlocks).Array()
	if len(arr) == 0 {
		return contentBlocks
	}

	// Check if we have both text and toolUse blocks.
	hasText := false
	hasToolUse := false
	for _, block := range arr {
		if block.Get("text").Exists() {
			hasText = true
		}
		if block.Get("toolUse").Exists() {
			hasToolUse = true
		}
	}
	if !hasText || !hasToolUse {
		return contentBlocks
	}

	// Reorder: text blocks first, then toolUse blocks.
	reordered := []byte(`[]`)
	for _, block := range arr {
		if block.Get("text").Exists() {
			reordered, _ = sjson.SetRawBytes(reordered, "-1", []byte(block.Raw))
		}
	}
	for _, block := range arr {
		if block.Get("toolUse").Exists() {
			reordered, _ = sjson.SetRawBytes(reordered, "-1", []byte(block.Raw))
		}
	}
	return reordered
}

// ConvertClaudeRequestToBedrock converts an Anthropic /v1/messages request body
// to the AWS Bedrock Converse API format.
func ConvertClaudeRequestToBedrock(modelName string, inputRawJSON []byte, _ bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)

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
	if stopSeqs := root.Get("stop_sequences"); stopSeqs.Exists() && stopSeqs.IsArray() {
		var stops []string
		stopSeqs.ForEach(func(_, v gjson.Result) bool {
			stops = append(stops, v.String())
			return true
		})
		if len(stops) > 0 {
			out, _ = sjson.SetBytes(out, "inferenceConfig.stopSequences", stops)
		}
	}

	// system
	// Bedrock Converse requires system as an array of {text:"..."} objects.
	// Claude API sends system as either a plain string or an array of content blocks.
	if system := root.Get("system"); system.Exists() {
		systemJSON := []byte(`[]`)
		switch system.Type {
		case gjson.String:
			if s := system.String(); s != "" {
				part := []byte(`{"text":""}`)
				part, _ = sjson.SetBytes(part, "text", s)
				systemJSON, _ = sjson.SetRawBytes(systemJSON, "-1", part)
			}
		case gjson.JSON:
			if system.IsArray() {
				system.ForEach(func(_, block gjson.Result) bool {
					if block.Get("type").String() == "text" {
						text := block.Get("text").String()
						if text != "" {
							part := []byte(`{"text":""}`)
							part, _ = sjson.SetBytes(part, "text", text)
							systemJSON, _ = sjson.SetRawBytes(systemJSON, "-1", part)
						}
					}
					return true
				})
			}
		}
		if len(gjson.ParseBytes(systemJSON).Array()) > 0 {
			out, _ = sjson.SetRawBytes(out, "system", systemJSON)
		}
	}

	// messages
	convoJSON := []byte(`[]`)
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			role := msg.Get("role").String()
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

					case "image":
						// Claude: {type:"image", source:{type:"base64",media_type:"...",data:"..."}}
						source := part.Get("source")
						switch source.Get("type").String() {
						case "base64":
							mediaType := source.Get("media_type").String()
							data := source.Get("data").String()
							if data != "" {
								format := strings.Split(mediaType, "/")
								imgFmt := "jpeg"
								if len(format) > 1 {
									imgFmt = format[1]
								}
								imgBlock := []byte(`{"image":{"format":"","source":{"bytes":""}}}`)
								imgBlock, _ = sjson.SetBytes(imgBlock, "image.format", imgFmt)
								imgBlock, _ = sjson.SetBytes(imgBlock, "image.source.bytes", data)
								contentBlocksJSON, _ = sjson.SetRawBytes(contentBlocksJSON, "-1", imgBlock)
							}
						case "url":
							// URL-based images: pass as text reference (Bedrock doesn't support URL images natively)
							url := source.Get("url").String()
							if url != "" {
								contentBlocksJSON, _ = sjson.SetRawBytes(contentBlocksJSON, "-1", buildTextBlock("[Image: "+url+"]"))
							}
						}

					case "tool_use":
						// Claude tool_use block (assistant) -> Bedrock toolUse
						toolBlock := []byte(`{"toolUse":{"toolUseId":"","name":"","input":{}}}`)
						toolBlock, _ = sjson.SetBytes(toolBlock, "toolUse.toolUseId", part.Get("id").String())
						toolBlock, _ = sjson.SetBytes(toolBlock, "toolUse.name", part.Get("name").String())
						if input := part.Get("input"); input.Exists() {
							toolBlock, _ = sjson.SetRawBytes(toolBlock, "toolUse.input", []byte(input.Raw))
						}
						contentBlocksJSON, _ = sjson.SetRawBytes(contentBlocksJSON, "-1", toolBlock)

					case "tool_result":
						// Claude tool_result (user) -> Bedrock toolResult
						resultBlock := []byte(`{"toolResult":{"toolUseId":"","content":[]}}`)
						resultBlock, _ = sjson.SetBytes(resultBlock, "toolResult.toolUseId", part.Get("tool_use_id").String())
						resultContent := part.Get("content")
						switch {
						case resultContent.Type == gjson.String:
							txtBlock := []byte(`{"text":""}`)
							txtBlock, _ = sjson.SetBytes(txtBlock, "text", resultContent.String())
							resultBlock, _ = sjson.SetRawBytes(resultBlock, "toolResult.content.-1", txtBlock)
						case resultContent.IsArray():
							resultContent.ForEach(func(_, item gjson.Result) bool {
								if item.Get("type").String() == "text" {
									txtBlock := []byte(`{"text":""}`)
									txtBlock, _ = sjson.SetBytes(txtBlock, "text", item.Get("text").String())
									resultBlock, _ = sjson.SetRawBytes(resultBlock, "toolResult.content.-1", txtBlock)
								}
								return true
							})
						}
						contentBlocksJSON, _ = sjson.SetRawBytes(contentBlocksJSON, "-1", resultBlock)

					case "thinking", "redacted_thinking":
						// Claude thinking blocks: skip (Bedrock Converse doesn't support them in input)
					}
					return true
				})
			}

			if len(gjson.ParseBytes(contentBlocksJSON).Array()) == 0 {
				return true
			}
			// For assistant messages with both text and toolUse, reorder so
			// toolUse blocks are consecutive (required by Bedrock Converse).
			if role == "assistant" {
				contentBlocksJSON = reorderBedrockAssistantBlocks(contentBlocksJSON)
			}
			msgBlock := []byte(`{"role":"","content":[]}`)
			msgBlock, _ = sjson.SetBytes(msgBlock, "role", role)
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
			spec, _ = sjson.SetBytes(spec, "toolSpec.name", tool.Get("name").String())
			spec, _ = sjson.SetBytes(spec, "toolSpec.description", tool.Get("description").String())
			if schema := tool.Get("input_schema"); schema.Exists() {
				spec, _ = sjson.SetRawBytes(spec, "toolSpec.inputSchema.json", []byte(schema.Raw))
			}
			toolSpecsJSON, _ = sjson.SetRawBytes(toolSpecsJSON, "-1", spec)
			return true
		})
		toolConfigJSON := []byte(`{"tools":[]}`)
		toolConfigJSON, _ = sjson.SetRawBytes(toolConfigJSON, "tools", toolSpecsJSON)
		out, _ = sjson.SetRawBytes(out, "toolConfig", toolConfigJSON)
	}

	// tool choice
	if tc := root.Get("tool_choice"); tc.Exists() {
		switch tc.Get("type").String() {
		case "auto":
			out, _ = sjson.SetRawBytes(out, "toolConfig.toolChoice", []byte(`{"auto":{}}`))
		case "any":
			out, _ = sjson.SetRawBytes(out, "toolConfig.toolChoice", []byte(`{"any":{}}`))
		case "tool":
			toolName := tc.Get("name").String()
			tcJSON := []byte(`{"tool":{"name":""}}`)
			tcJSON, _ = sjson.SetBytes(tcJSON, "tool.name", toolName)
			out, _ = sjson.SetRawBytes(out, "toolConfig.toolChoice", tcJSON)
		}
	}

	// thinking / reasoningConfig / reasoning_effort
	if thinking := root.Get("thinking"); thinking.Exists() {
		lowerModel := strings.ToLower(strings.TrimSpace(modelName))
		// GLM family uses Bedrock native reasoningConfig.
		if strings.Contains(lowerModel, "glm") || strings.HasPrefix(lowerModel, "zai.") {
			budget := thinking.Get("budget_tokens").Int()
			effort := "medium"
			if budget <= 1024 {
				effort = "low"
			} else if budget >= 16000 { // Claude standard high thinking is usually 16k+
				effort = "high"
			}

			// Use the native reasoningConfig structure defined in Bedrock Converse API for reasoning-capable models
			configObj := []byte(`{"type":"enabled","maxReasoningEffort":""}`)
			configObj, _ = sjson.SetBytes(configObj, "maxReasoningEffort", effort)
			out, _ = sjson.SetRawBytes(out, "additionalModelRequestFields.reasoningConfig", configObj)
		} else if strings.Contains(lowerModel, "anthropic.claude") || strings.HasPrefix(lowerModel, "claude") {
			// Claude family keeps Anthropic thinking payload.
			out, _ = sjson.SetRawBytes(out, "additionalModelRequestFields.thinking", []byte(thinking.Raw))
		} else {
			// DeepSeek and other Bedrock models may reject Anthropic thinking payload.
			// Keep request valid by omitting model-specific thinking fields here.
		}
	}

	return out
}
