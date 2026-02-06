// Package claude provides request translation functionality for Claude Code API compatibility.
// This package handles the conversion of Claude Code API requests into Gemini CLI-compatible
// JSON format, transforming message contents, system instructions, and tool declarations
// into the format expected by Gemini CLI API clients. It performs JSON data transformation
// to ensure compatibility between Claude Code API format and Gemini CLI API's expected format.
package claude

import (
	"bytes"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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
func ConvertClaudeRequestToCLI(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON
	rawJSON = bytes.Replace(rawJSON, []byte(`"url":{"type":"string","format":"uri",`), []byte(`"url":{"type":"string",`), -1)

	// Build output Gemini CLI request JSON
	out := `{"model":"","request":{"contents":[]}}`
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
			out, _ = sjson.SetRaw(out, "request.systemInstruction", systemInstruction)
		}
	} else if systemResult.Type == gjson.String {
		out, _ = sjson.Set(out, "request.systemInstruction.parts.-1.text", systemResult.String())
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
						functionArgs := contentResult.Get("input").String()
						argsResult := gjson.Parse(functionArgs)
						if argsResult.IsObject() && gjson.Valid(functionArgs) {
							part := `{"thoughtSignature":"","functionCall":{"name":"","args":{}}}`
							part, _ = sjson.Set(part, "thoughtSignature", geminiCLIClaudeThoughtSignature)
							part, _ = sjson.Set(part, "functionCall.name", functionName)
							part, _ = sjson.SetRaw(part, "functionCall.args", functionArgs)
							contentJSON, _ = sjson.SetRaw(contentJSON, "parts.-1", part)
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
						part := `{"functionResponse":{"name":"","response":{"result":""}}}`
						part, _ = sjson.Set(part, "functionResponse.name", funcName)
						part, _ = sjson.Set(part, "functionResponse.response.result", responseData)
						contentJSON, _ = sjson.SetRaw(contentJSON, "parts.-1", part)

					case "image":
						source := contentResult.Get("source")
						if source.Get("type").String() == "base64" {
							mimeType := source.Get("media_type").String()
							data := source.Get("data").String()
							if mimeType != "" && data != "" {
								part := `{"inlineData":{"mime_type":"","data":""}}`
								part, _ = sjson.Set(part, "inlineData.mime_type", mimeType)
								part, _ = sjson.Set(part, "inlineData.data", data)
								contentJSON, _ = sjson.SetRaw(contentJSON, "parts.-1", part)
							}
						}
					}
					return true
				})
				out, _ = sjson.SetRaw(out, "request.contents.-1", contentJSON)
			} else if contentsResult.Type == gjson.String {
				part := `{"text":""}`
				part, _ = sjson.Set(part, "text", contentsResult.String())
				contentJSON, _ = sjson.SetRaw(contentJSON, "parts.-1", part)
				out, _ = sjson.SetRaw(out, "request.contents.-1", contentJSON)
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
				if gjson.Valid(tool) && gjson.Parse(tool).IsObject() {
					if !hasTools {
						out, _ = sjson.SetRaw(out, "request.tools", `[{"functionDeclarations":[]}]`)
						hasTools = true
					}
					out, _ = sjson.SetRaw(out, "request.tools.0.functionDeclarations.-1", tool)
				}
			}
			return true
		})
		if !hasTools {
			out, _ = sjson.Delete(out, "request.tools")
		}
	}

	// Map Anthropic thinking -> Gemini thinkingBudget/include_thoughts when type==enabled
	if t := gjson.GetBytes(rawJSON, "thinking"); t.Exists() && t.IsObject() {
		if t.Get("type").String() == "enabled" {
			if b := t.Get("budget_tokens"); b.Exists() && b.Type == gjson.Number {
				budget := int(b.Int())
				out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.thinkingBudget", budget)
				out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.includeThoughts", true)
			}
		}
	}
	if v := gjson.GetBytes(rawJSON, "temperature"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "request.generationConfig.temperature", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "top_p"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "request.generationConfig.topP", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "top_k"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "request.generationConfig.topK", v.Num)
	}

	outBytes := []byte(out)
	outBytes = common.AttachDefaultSafetySettings(outBytes, "request.safetySettings")

	return outBytes
}
