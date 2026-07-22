// Package claude provides request translation functionality for Claude API.
// It handles parsing and transforming Claude API requests into the internal client format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package also performs JSON data cleaning and transformation to ensure compatibility
// between Claude API format and the internal client's expected format.
package claude

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const geminiClaudeThoughtSignature = "skip_thought_signature_validator"

// ConvertClaudeRequestToGemini parses a Claude API request and returns a complete
// Gemini request body (as JSON bytes) ready to be sent via SendRawMessageStream.
// All JSON transformations are performed using gjson/sjson.
//
// Parameters:
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON request from the Claude API.
//   - stream: A boolean indicating if the request is for a streaming response.
//
// Returns:
//   - []byte: The transformed request in Gemini format.
func ConvertClaudeRequestToGemini(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON
	// Build output Gemini request JSON
	out := []byte(`{"contents":[]}`)
	out, _ = sjson.SetBytes(out, "model", modelName)

	// system instruction
	if systemResult := gjson.GetBytes(rawJSON, "system"); systemResult.IsArray() {
		systemParts := make([][]byte, 0, 2)
		systemResult.ForEach(func(_, systemPromptResult gjson.Result) bool {
			if systemPromptResult.Get("type").String() == "text" {
				textResult := systemPromptResult.Get("text")
				if textResult.Type == gjson.String {
					if util.IsClaudeCodeAttributionSystemText(textResult.String()) {
						return true
					}
					part := []byte(`{"text":""}`)
					part, _ = sjson.SetBytes(part, "text", textResult.String())
					systemParts = append(systemParts, part)
				}
			}
			return true
		})
		if len(systemParts) > 0 {
			systemInstruction := []byte(`{"role":"user","parts":[]}`)
			systemInstruction, _ = sjson.SetRawBytes(systemInstruction, "parts", translatorcommon.JoinRawArray(systemParts))
			out, _ = sjson.SetRawBytes(out, "system_instruction", systemInstruction)
		}
	} else if systemResult.Type == gjson.String && !util.IsClaudeCodeAttributionSystemText(systemResult.String()) {
		out, _ = sjson.SetBytes(out, "system_instruction.parts.-1.text", systemResult.String())
	}

	// contents
	if messagesResult := gjson.GetBytes(rawJSON, "messages"); messagesResult.IsArray() {
		contentItems := translatorcommon.NewRawArrayItems(messagesResult.Get("#").Int())
		messagesResult.ForEach(func(_, messageResult gjson.Result) bool {
			roleResult := messageResult.Get("role")
			if roleResult.Type != gjson.String {
				return true
			}
			role := roleResult.String()
			if role == "assistant" {
				role = "model"
			} else if role == "system" {
				role = "user"
			}

			partItems := make([][]byte, 0, 4)
			contentsResult := messageResult.Get("content")
			if roleResult.String() == "system" {
				if reminderText, ok := translatorcommon.ClaudeMessageSystemReminderText(contentsResult); ok {
					part := []byte(`{"text":""}`)
					part, _ = sjson.SetBytes(part, "text", reminderText)
					partItems = append(partItems, part)
					contentItems = append(contentItems, geminiContentWithParts(role, partItems))
				}
				return true
			}
			if contentsResult.IsArray() {
				contentsResult.ForEach(func(_, contentResult gjson.Result) bool {
					switch contentResult.Get("type").String() {
					case "text":
						text := contentResult.Get("text").String()
						if text == "" {
							return true
						}
						part := []byte(`{"text":""}`)
						part, _ = sjson.SetBytes(part, "text", text)
						partItems = append(partItems, part)

					case "tool_use":
						functionName := contentResult.Get("name").String()
						if toolUseID := contentResult.Get("id").String(); toolUseID != "" {
							if derived := toolNameFromClaudeToolUseID(toolUseID); derived != "" {
								functionName = derived
							}
						}
						functionName = util.SanitizeFunctionName(functionName)
						functionArgs := contentResult.Get("input").String()
						argsResult := gjson.Parse(functionArgs)
						if argsResult.IsObject() && gjson.Valid(functionArgs) {
							part := []byte(`{"thoughtSignature":"","functionCall":{"name":"","args":{}}}`)
							part, _ = sjson.SetBytes(part, "thoughtSignature", geminiClaudeThoughtSignature)
							part, _ = sjson.SetBytes(part, "functionCall.name", functionName)
							part, _ = sjson.SetRawBytes(part, "functionCall.args", []byte(functionArgs))
							partItems = append(partItems, part)
						}

					case "tool_result":
						toolCallID := contentResult.Get("tool_use_id").String()
						if toolCallID == "" {
							return true
						}
						funcName := toolNameFromClaudeToolUseID(toolCallID)
						if funcName == "" {
							funcName = toolCallID
						}
						funcName = util.SanitizeFunctionName(funcName)
						toolResult := util.ConvertClaudeToolResultContent(contentResult.Get("content"))
						part := []byte(`{"functionResponse":{"name":"","response":{"result":""}}}`)
						part, _ = sjson.SetBytes(part, "functionResponse.name", funcName)
						if toolResult.ResultIsRaw {
							part, _ = sjson.SetRawBytes(part, "functionResponse.response.result", []byte(toolResult.Result))
						} else {
							part, _ = sjson.SetBytes(part, "functionResponse.response.result", toolResult.Result)
						}
						partItems = append(partItems, part)
						for _, img := range toolResult.Images {
							imagePart := []byte(`{"inline_data":{"mime_type":"","data":""}}`)
							imagePart, _ = sjson.SetBytes(imagePart, "inline_data.mime_type", img.MimeType)
							imagePart, _ = sjson.SetBytes(imagePart, "inline_data.data", img.Data)
							partItems = append(partItems, imagePart)
						}

					case "image":
						source := contentResult.Get("source")
						if source.Get("type").String() != "base64" {
							return true
						}
						mimeType := source.Get("media_type").String()
						data := source.Get("data").String()
						if mimeType == "" || data == "" {
							return true
						}
						part := []byte(`{"inline_data":{"mime_type":"","data":""}}`)
						part, _ = sjson.SetBytes(part, "inline_data.mime_type", mimeType)
						part, _ = sjson.SetBytes(part, "inline_data.data", data)
						partItems = append(partItems, part)
					}
					return true
				})
				contentItems = append(contentItems, geminiContentWithParts(role, partItems))
			} else if contentsResult.Type == gjson.String {
				part := []byte(`{"text":""}`)
				part, _ = sjson.SetBytes(part, "text", contentsResult.String())
				partItems = append(partItems, part)
				contentItems = append(contentItems, geminiContentWithParts(role, partItems))
			}
			return true
		})

		// Strip a trailing model turn with unanswered function calls.
		if len(contentItems) > 0 {
			last := gjson.ParseBytes(contentItems[len(contentItems)-1])
			if last.Get("role").String() == "model" {
				hasFunctionCall := false
				last.Get("parts").ForEach(func(_, part gjson.Result) bool {
					if part.Get("functionCall").Exists() {
						hasFunctionCall = true
						return false
					}
					return true
				})
				if hasFunctionCall {
					contentItems = contentItems[:len(contentItems)-1]
				}
			}
		}
		out = translatorcommon.SetRawArrayItems(out, "contents", contentItems)
	}

	// tools
	if toolsResult := gjson.GetBytes(rawJSON, "tools"); toolsResult.IsArray() {
		var toolItems [][]byte
		toolsResult.ForEach(func(_, toolResult gjson.Result) bool {
			inputSchemaResult := toolResult.Get("input_schema")
			if inputSchemaResult.Exists() && inputSchemaResult.IsObject() {
				inputSchema := util.CleanJSONSchemaForGemini(inputSchemaResult.Raw)
				tool := []byte(toolResult.Raw)
				var err error
				tool, err = sjson.DeleteBytes(tool, "input_schema")
				if err != nil {
					return true
				}
				tool, err = sjson.SetRawBytes(tool, "parametersJsonSchema", []byte(inputSchema))
				if err != nil {
					return true
				}
				for _, path := range []string{"strict", "input_examples", "type", "cache_control", "defer_loading", "eager_input_streaming"} {
					if toolResult.Get(path).Exists() {
						tool, _ = sjson.DeleteBytes(tool, path)
					}
				}
				nameResult := toolResult.Get("name")
				originalName := nameResult.String()
				sanitizedName := util.SanitizeFunctionName(originalName)
				if nameResult.Type != gjson.String || sanitizedName != originalName {
					tool, _ = sjson.SetBytes(tool, "name", sanitizedName)
				}
				if gjson.ValidBytes(tool) && gjson.ParseBytes(tool).IsObject() {
					toolItems = append(toolItems, tool)
				}
			}
			return true
		})
		if len(toolItems) > 0 {
			tools := []byte(`[{"functionDeclarations":[]}]`)
			tools, _ = sjson.SetRawBytes(tools, "0.functionDeclarations", translatorcommon.JoinRawArray(toolItems))
			out, _ = sjson.SetRawBytes(out, "tools", tools)
		}
	}

	// tool_choice
	toolChoiceResult := gjson.GetBytes(rawJSON, "tool_choice")
	if toolChoiceResult.Exists() {
		toolChoiceType := ""
		toolChoiceName := ""
		if toolChoiceResult.IsObject() {
			toolChoiceType = toolChoiceResult.Get("type").String()
			toolChoiceName = toolChoiceResult.Get("name").String()
		} else if toolChoiceResult.Type == gjson.String {
			toolChoiceType = toolChoiceResult.String()
		}

		switch toolChoiceType {
		case "auto":
			out, _ = sjson.SetBytes(out, "toolConfig.functionCallingConfig.mode", "AUTO")
		case "none":
			out, _ = sjson.SetBytes(out, "toolConfig.functionCallingConfig.mode", "NONE")
		case "any":
			out, _ = sjson.SetBytes(out, "toolConfig.functionCallingConfig.mode", "ANY")
		case "tool":
			out, _ = sjson.SetBytes(out, "toolConfig.functionCallingConfig.mode", "ANY")
			if toolChoiceName != "" {
				out, _ = sjson.SetBytes(out, "toolConfig.functionCallingConfig.allowedFunctionNames", []string{util.SanitizeFunctionName(toolChoiceName)})
			}
		}
	}

	// Map Anthropic thinking -> Gemini thinking config when enabled
	// Translator only does format conversion, ApplyThinking handles model capability validation.
	if t := gjson.GetBytes(rawJSON, "thinking"); t.Exists() && t.IsObject() {
		switch t.Get("type").String() {
		case "enabled":
			if b := t.Get("budget_tokens"); b.Exists() && b.Type == gjson.Number {
				budget := int(b.Int())
				out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingBudget", budget)
				out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.includeThoughts", true)
			}
		case "adaptive", "auto":
			// For adaptive thinking:
			// - If output_config.effort is explicitly present, pass through as thinkingLevel.
			// - Otherwise, treat it as "enabled with target-model maximum" and emit thinkingBudget=max.
			// ApplyThinking handles clamping to target model's supported levels.
			effort := ""
			if v := gjson.GetBytes(rawJSON, "output_config.effort"); v.Exists() && v.Type == gjson.String {
				effort = strings.ToLower(strings.TrimSpace(v.String()))
			}
			if effort != "" {
				out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingLevel", effort)
			} else {
				maxBudget := 0
				if mi := registry.LookupModelInfo(modelName, "gemini"); mi != nil && mi.Thinking != nil {
					maxBudget = mi.Thinking.Max
				}
				if maxBudget > 0 {
					out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingBudget", maxBudget)
				} else {
					out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingLevel", "high")
				}
			}
			out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.includeThoughts", true)
		}
	}
	if v := gjson.GetBytes(rawJSON, "temperature"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "generationConfig.temperature", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "top_p"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "generationConfig.topP", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "top_k"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "generationConfig.topK", v.Num)
	}

	result := out
	result = common.AttachDefaultSafetySettings(result, "safetySettings")

	return result
}

func geminiContentWithParts(role string, parts [][]byte) []byte {
	content := []byte(`{"role":"","parts":[]}`)
	content, _ = sjson.SetBytes(content, "role", role)
	content, _ = sjson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(parts))
	return content
}

func toolNameFromClaudeToolUseID(toolUseID string) string {
	parts := strings.Split(toolUseID, "-")
	if len(parts) <= 1 {
		return ""
	}
	return strings.Join(parts[0:len(parts)-1], "-")
}
