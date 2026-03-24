// Package claude provides request translation functionality for Claude API.
// It handles parsing and transforming Claude API requests into the internal client format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package also performs JSON data cleaning and transformation to ensure compatibility
// between Claude API format and the internal client's expected format.
package claude

import (
	"bytes"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const geminiClaudeThoughtSignature = "skip_thought_signature_validator"

// ConvertClaudeRequestToGemini parses a Claude API request and returns a complete
// Gemini CLI request body (as JSON bytes) ready to be sent via SendRawMessageStream.
// All JSON transformations are performed using gjson/sjson.
//
// Parameters:
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON request from the Claude API.
//   - stream: A boolean indicating if the request is for a streaming response.
//
// Returns:
//   - []byte: The transformed request in Gemini CLI format.
func ConvertClaudeRequestToGemini(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON
	rawJSON = bytes.Replace(rawJSON, []byte(`"url":{"type":"string","format":"uri",`), []byte(`"url":{"type":"string",`), -1)

	// Build output Gemini CLI request JSON
	out := `{"contents":[]}`
	out, _ = sjson.Set(out, "model", modelName)

	// system instruction
	if systemResult := gjson.GetBytes(rawJSON, "system"); systemResult.IsArray() {
		systemInstruction := `{"role":"user","parts":[]}`
		hasSystemParts := false
		systemResult.ForEach(func(_, systemPromptResult gjson.Result) bool {
			if systemPromptResult.Get("type").String() == "text" {
				textResult := systemPromptResult.Get("text")
				if textResult.Type == gjson.String {
					part := `{"text":""}`
					part, _ = sjson.Set(part, "text", textResult.String())
					systemInstruction, _ = sjson.SetRaw(systemInstruction, "parts.-1", part)
					hasSystemParts = true
				}
			}
			return true
		})
		if hasSystemParts {
			out, _ = sjson.SetRaw(out, "system_instruction", systemInstruction)
		}
	} else if systemResult.Type == gjson.String {
		out, _ = sjson.Set(out, "system_instruction.parts.-1.text", systemResult.String())
	}

	// contents
	if messagesResult := gjson.GetBytes(rawJSON, "messages"); messagesResult.IsArray() {
		messagesResult.ForEach(func(_, messageResult gjson.Result) bool {
			roleResult := messageResult.Get("role")
			if roleResult.Type != gjson.String {
				return true
			}
			role := roleResult.String()
			if role == "assistant" {
				role = "model"
			}

			contentJSON := `{"role":"","parts":[]}`
			contentJSON, _ = sjson.Set(contentJSON, "role", role)

			contentsResult := messageResult.Get("content")
			if contentsResult.IsArray() {
				contentsResult.ForEach(func(_, contentResult gjson.Result) bool {
					switch contentResult.Get("type").String() {
					case "text":
						part := `{"text":""}`
						part, _ = sjson.Set(part, "text", contentResult.Get("text").String())
						contentJSON, _ = sjson.SetRaw(contentJSON, "parts.-1", part)

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
							part := `{"thoughtSignature":"","functionCall":{"name":"","args":{}}}`
							part, _ = sjson.Set(part, "thoughtSignature", geminiClaudeThoughtSignature)
							part, _ = sjson.Set(part, "functionCall.name", functionName)
							part, _ = sjson.SetRaw(part, "functionCall.args", functionArgs)
							contentJSON, _ = sjson.SetRaw(contentJSON, "parts.-1", part)
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
						responseData := contentResult.Get("content").Raw
						part := `{"functionResponse":{"name":"","response":{"result":""}}}`
						part, _ = sjson.Set(part, "functionResponse.name", funcName)
						part, _ = sjson.Set(part, "functionResponse.response.result", responseData)
						contentJSON, _ = sjson.SetRaw(contentJSON, "parts.-1", part)

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
						part := `{"inline_data":{"mime_type":"","data":""}}`
						part, _ = sjson.Set(part, "inline_data.mime_type", mimeType)
						part, _ = sjson.Set(part, "inline_data.data", data)
						contentJSON, _ = sjson.SetRaw(contentJSON, "parts.-1", part)
					}
					return true
				})
				out, _ = sjson.SetRaw(out, "contents.-1", contentJSON)
			} else if contentsResult.Type == gjson.String {
				part := `{"text":""}`
				part, _ = sjson.Set(part, "text", contentsResult.String())
				contentJSON, _ = sjson.SetRaw(contentJSON, "parts.-1", part)
				out, _ = sjson.SetRaw(out, "contents.-1", contentJSON)
			}
			return true
		})
	}

	// tools
	if toolsResult := gjson.GetBytes(rawJSON, "tools"); toolsResult.IsArray() {
		hasTools := false
		toolsResult.ForEach(func(_, toolResult gjson.Result) bool {
			inputSchemaResult := toolResult.Get("input_schema")
			if inputSchemaResult.Exists() && inputSchemaResult.IsObject() {
				inputSchema := inputSchemaResult.Raw
				tool, _ := sjson.Delete(toolResult.Raw, "input_schema")
				tool, _ = sjson.SetRaw(tool, "parametersJsonSchema", inputSchema)
				tool, _ = sjson.Delete(tool, "strict")
				tool, _ = sjson.Delete(tool, "input_examples")
				tool, _ = sjson.Delete(tool, "type")
				tool, _ = sjson.Delete(tool, "cache_control")
				tool, _ = sjson.Delete(tool, "defer_loading")
				tool, _ = sjson.Set(tool, "name", util.SanitizeFunctionName(gjson.Get(tool, "name").String()))
				if gjson.Valid(tool) && gjson.Parse(tool).IsObject() {
					if !hasTools {
						out, _ = sjson.SetRaw(out, "tools", `[{"functionDeclarations":[]}]`)
						hasTools = true
					}
					out, _ = sjson.SetRaw(out, "tools.0.functionDeclarations.-1", tool)
				}
			}
			return true
		})
		if !hasTools {
			out, _ = sjson.Delete(out, "tools")
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
			out, _ = sjson.Set(out, "toolConfig.functionCallingConfig.mode", "AUTO")
		case "none":
			out, _ = sjson.Set(out, "toolConfig.functionCallingConfig.mode", "NONE")
		case "any":
			out, _ = sjson.Set(out, "toolConfig.functionCallingConfig.mode", "ANY")
		case "tool":
			out, _ = sjson.Set(out, "toolConfig.functionCallingConfig.mode", "ANY")
			if toolChoiceName != "" {
				out, _ = sjson.Set(out, "toolConfig.functionCallingConfig.allowedFunctionNames", []string{util.SanitizeFunctionName(toolChoiceName)})
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
				out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingBudget", budget)
				out, _ = sjson.Set(out, "generationConfig.thinkingConfig.includeThoughts", true)
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
				out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingLevel", effort)
			} else {
				maxBudget := 0
				if mi := registry.LookupModelInfo(modelName, "gemini"); mi != nil && mi.Thinking != nil {
					maxBudget = mi.Thinking.Max
				}
				if maxBudget > 0 {
					out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingBudget", maxBudget)
				} else {
					out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingLevel", "high")
				}
			}
			out, _ = sjson.Set(out, "generationConfig.thinkingConfig.includeThoughts", true)
		}
	}
	if v := gjson.GetBytes(rawJSON, "temperature"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "generationConfig.temperature", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "top_p"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "generationConfig.topP", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "top_k"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "generationConfig.topK", v.Num)
	}

	result := []byte(out)
	result = common.AttachDefaultSafetySettings(result, "safetySettings")

	return result
}

func toolNameFromClaudeToolUseID(toolUseID string) string {
	parts := strings.Split(toolUseID, "-")
	if len(parts) <= 1 {
		return ""
	}
	return strings.Join(parts[0:len(parts)-1], "-")
}
