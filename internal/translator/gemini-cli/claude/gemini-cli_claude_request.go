// Package claude provides request translation functionality for Claude Code API compatibility.
// This package handles the conversion of Claude Code API requests into Gemini CLI-compatible
// JSON format, transforming message contents, system instructions, and tool declarations
// into the format expected by Gemini CLI API clients. It performs JSON data transformation
// to ensure compatibility between Claude Code API format and Gemini CLI API's expected format.
package claude

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const geminiCLIClaudeThoughtSignature = "skip_thought_signature_validator"

// ConvertClaudeRequestToCLI parses and transforms a Claude Code API request into Gemini CLI API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Gemini CLI API.
// The function performs the following transformations:
// 1. Extracts the model information from the request
// 2. Restructures the JSON to match Gemini CLI API format
// 3. Converts system instructions to the expected format
// 4. Maps message contents with proper role transformations
// 5. Handles tool declarations and tool choices
// 6. Maps generation configuration parameters
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the Claude Code API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in Gemini CLI API format
func ConvertClaudeRequestToCLI(modelName string, inputRawJSON []byte, _ bool) ([]byte, error) {
	rawJSON := inputRawJSON

	// Build output Gemini CLI request JSON
	out := []byte(`{"model":"","request":{"contents":[]}}`)
	out, _ = ojson.SetBytes(out, "model", modelName)

	// system instruction
	if systemResult := ojson.GetBytes(rawJSON, "system"); systemResult.IsArray() {
		systemInstruction := []byte(`{"role":"user","parts":[]}`)
		hasSystemParts := false
		systemResult.ForEach(func(_, systemPromptResult ojson.Result) bool {
			if systemPromptResult.Get("type").String() == "text" {
				textResult := systemPromptResult.Get("text")
				if textResult.Type == ojson.String {
					if util.IsClaudeCodeAttributionSystemText(textResult.String()) {
						return true
					}
					part := []byte(`{"text":""}`)
					part, _ = ojson.SetBytes(part, "text", textResult.String())
					systemInstruction, _ = ojson.SetRawBytes(systemInstruction, "parts.-1", part)
					hasSystemParts = true
				}
			}
			return true
		})
		if hasSystemParts {
			out, _ = ojson.SetRawBytes(out, "request.systemInstruction", systemInstruction)
		}
	} else if systemResult.Type == ojson.String && !util.IsClaudeCodeAttributionSystemText(systemResult.String()) {
		out, _ = ojson.SetBytes(out, "request.systemInstruction.parts.-1.text", systemResult.String())
	}

	// contents
	if messagesResult := ojson.GetBytes(rawJSON, "messages"); messagesResult.IsArray() {
		messagesResult.ForEach(func(_, messageResult ojson.Result) bool {
			roleResult := messageResult.Get("role")
			if roleResult.Type != ojson.String {
				return true
			}
			role := roleResult.String()
			if role == "assistant" {
				role = "model"
			} else if role == "system" {
				role = "user"
			}

			contentJSON := []byte(`{"role":"","parts":[]}`)
			contentJSON, _ = ojson.SetBytes(contentJSON, "role", role)

			contentsResult := messageResult.Get("content")
			if contentsResult.IsArray() {
				contentsResult.ForEach(func(_, contentResult ojson.Result) bool {
					switch contentResult.Get("type").String() {
					case "text":
						part := []byte(`{"text":""}`)
						part, _ = ojson.SetBytes(part, "text", contentResult.Get("text").String())
						contentJSON, _ = ojson.SetRawBytes(contentJSON, "parts.-1", part)

					case "tool_use":
						functionName := util.SanitizeFunctionName(contentResult.Get("name").String())
						functionArgs := contentResult.Get("input").String()
						argsResult := ojson.Parse(functionArgs)
						if argsResult.IsObject() && ojson.Valid(functionArgs) {
							part := []byte(`{"thoughtSignature":"","functionCall":{"name":"","args":{}}}`)
							part, _ = ojson.SetBytes(part, "thoughtSignature", geminiCLIClaudeThoughtSignature)
							part, _ = ojson.SetBytes(part, "functionCall.name", functionName)
							part, _ = ojson.SetRawBytes(part, "functionCall.args", []byte(functionArgs))
							contentJSON, _ = ojson.SetRawBytes(contentJSON, "parts.-1", part)
						}

					case "tool_result":
						toolCallID := contentResult.Get("tool_use_id").String()
						if toolCallID == "" {
							return true
						}
						funcName := toolCallID
						toolCallIDs := strings.Split(toolCallID, "-")
						if len(toolCallIDs) > 1 {
							funcName = strings.Join(toolCallIDs[0:len(toolCallIDs)-1], "-")
						}
						responseData := contentResult.Get("content").Raw
						part := []byte(`{"functionResponse":{"name":"","response":{"result":""}}}`)
						part, _ = ojson.SetBytes(part, "functionResponse.name", util.SanitizeFunctionName(funcName))
						part, _ = ojson.SetBytes(part, "functionResponse.response.result", responseData)
						contentJSON, _ = ojson.SetRawBytes(contentJSON, "parts.-1", part)

					case "image":
						source := contentResult.Get("source")
						if source.Get("type").String() == "base64" {
							mimeType := source.Get("media_type").String()
							data := source.Get("data").String()
							if mimeType != "" && data != "" {
								part := []byte(`{"inlineData":{"mime_type":"","data":""}}`)
								part, _ = ojson.SetBytes(part, "inlineData.mime_type", mimeType)
								part, _ = ojson.SetBytes(part, "inlineData.data", data)
								contentJSON, _ = ojson.SetRawBytes(contentJSON, "parts.-1", part)
							}
						}
					}
					return true
				})
				out, _ = ojson.SetRawBytes(out, "request.contents.-1", contentJSON)
			} else if contentsResult.Type == ojson.String {
				part := []byte(`{"text":""}`)
				part, _ = ojson.SetBytes(part, "text", contentsResult.String())
				contentJSON, _ = ojson.SetRawBytes(contentJSON, "parts.-1", part)
				out, _ = ojson.SetRawBytes(out, "request.contents.-1", contentJSON)
			}
			return true
		})
	}

	// tools
	if toolsResult := ojson.GetBytes(rawJSON, "tools"); toolsResult.IsArray() {
		hasTools := false
		toolsResult.ForEach(func(_, toolResult ojson.Result) bool {
			inputSchemaResult := toolResult.Get("input_schema")
			if inputSchemaResult.Exists() && inputSchemaResult.IsObject() {
				inputSchema := util.CleanJSONSchemaForGemini(inputSchemaResult.Raw)
				tool, _ := ojson.DeleteBytes([]byte(toolResult.Raw), "input_schema")
				tool, _ = ojson.SetRawBytes(tool, "parametersJsonSchema", []byte(inputSchema))
				tool, _ = ojson.SetBytes(tool, "name", util.SanitizeFunctionName(ojson.GetBytes(tool, "name").String()))
				tool, _ = ojson.DeleteBytes(tool, "strict")
				tool, _ = ojson.DeleteBytes(tool, "input_examples")
				tool, _ = ojson.DeleteBytes(tool, "type")
				tool, _ = ojson.DeleteBytes(tool, "cache_control")
				tool, _ = ojson.DeleteBytes(tool, "defer_loading")
				tool, _ = ojson.DeleteBytes(tool, "eager_input_streaming")
				if ojson.ValidBytes(tool) && ojson.ParseBytes(tool).IsObject() {
					if !hasTools {
						out, _ = ojson.SetRawBytes(out, "request.tools", []byte(`[{"functionDeclarations":[]}]`))
						hasTools = true
					}
					out, _ = ojson.SetRawBytes(out, "request.tools.0.functionDeclarations.-1", tool)
				}
			}
			return true
		})
		if !hasTools {
			out, _ = ojson.DeleteBytes(out, "request.tools")
		}
	}

	// tool_choice
	toolChoiceResult := ojson.GetBytes(rawJSON, "tool_choice")
	if toolChoiceResult.Exists() {
		toolChoiceType := ""
		toolChoiceName := ""
		if toolChoiceResult.IsObject() {
			toolChoiceType = toolChoiceResult.Get("type").String()
			toolChoiceName = toolChoiceResult.Get("name").String()
		} else if toolChoiceResult.Type == ojson.String {
			toolChoiceType = toolChoiceResult.String()
		}

		switch toolChoiceType {
		case "auto":
			out, _ = ojson.SetBytes(out, "request.toolConfig.functionCallingConfig.mode", "AUTO")
		case "none":
			out, _ = ojson.SetBytes(out, "request.toolConfig.functionCallingConfig.mode", "NONE")
		case "any":
			out, _ = ojson.SetBytes(out, "request.toolConfig.functionCallingConfig.mode", "ANY")
		case "tool":
			out, _ = ojson.SetBytes(out, "request.toolConfig.functionCallingConfig.mode", "ANY")
			if toolChoiceName != "" {
				out, _ = ojson.SetBytes(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames", []string{util.SanitizeFunctionName(toolChoiceName)})
			}
		}
	}

	// Map Anthropic thinking -> Gemini CLI thinkingConfig when enabled
	// Translator only does format conversion, ApplyThinking handles model capability validation.
	if t := ojson.GetBytes(rawJSON, "thinking"); t.Exists() && t.IsObject() {
		switch t.Get("type").String() {
		case "enabled":
			if b := t.Get("budget_tokens"); b.Exists() && b.Type == ojson.Number {
				budget := int(b.Int())
				out, _ = ojson.SetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget", budget)
				out, _ = ojson.SetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts", true)
			}
		case "adaptive", "auto":
			// For adaptive thinking:
			// - If output_config.effort is explicitly present, pass through as thinkingLevel.
			// - Otherwise, treat it as "enabled with target-model maximum" and emit high.
			// ApplyThinking handles clamping to target model's supported levels.
			effort := ""
			if v := ojson.GetBytes(rawJSON, "output_config.effort"); v.Exists() && v.Type == ojson.String {
				effort = strings.ToLower(strings.TrimSpace(v.String()))
			}
			if effort != "" {
				out, _ = ojson.SetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel", effort)
			} else {
				out, _ = ojson.SetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel", "high")
			}
			out, _ = ojson.SetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts", true)
		}
	}
	if v := ojson.GetBytes(rawJSON, "temperature"); v.Exists() && v.Type == ojson.Number {
		out, _ = ojson.SetBytes(out, "request.generationConfig.temperature", v.Num)
	}
	if v := ojson.GetBytes(rawJSON, "top_p"); v.Exists() && v.Type == ojson.Number {
		out, _ = ojson.SetBytes(out, "request.generationConfig.topP", v.Num)
	}
	if v := ojson.GetBytes(rawJSON, "top_k"); v.Exists() && v.Type == ojson.Number {
		out, _ = ojson.SetBytes(out, "request.generationConfig.topK", v.Num)
	}

	out = common.AttachDefaultSafetySettings(out, "request.safetySettings")
	return out, nil
}
