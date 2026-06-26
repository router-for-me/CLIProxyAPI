// Package claude provides request translation functionality for Claude API.
// It handles parsing and transforming Claude API requests into the internal client format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package also performs JSON data cleaning and transformation to ensure compatibility
// between Claude API format and the internal client's expected format.
package claude

import (
	"fmt"
	"strings"

<<<<<<< HEAD:pkg/llmproxy/translator/gemini/claude/gemini_claude_request.go
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/gemini/common"
=======
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
>>>>>>> upstream/main:internal/translator/gemini/claude/gemini_claude_request.go
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
<<<<<<< HEAD:pkg/llmproxy/translator/gemini/claude/gemini_claude_request.go
	rawJSON = bytes.ReplaceAll(rawJSON, []byte(`"url":{"type":"string","format":"uri",`), []byte(`"url":{"type":"string",`))

	// Build output Gemini CLI request JSON
=======
	// Build output Gemini request JSON
>>>>>>> upstream/main:internal/translator/gemini/claude/gemini_claude_request.go
	out := []byte(`{"contents":[]}`)
	out, _ = sjson.SetBytes(out, "model", modelName)

	// system instruction
	if systemResult := gjson.GetBytes(rawJSON, "system"); systemResult.IsArray() {
		systemInstruction := []byte(`{"role":"user","parts":[]}`)
		hasSystemParts := false
		systemResult.ForEach(func(_, systemPromptResult gjson.Result) bool {
			if systemPromptResult.Get("type").String() == "text" {
				textResult := systemPromptResult.Get("text")
				if textResult.Type == gjson.String {
					if util.IsClaudeCodeAttributionSystemText(textResult.String()) {
						return true
					}
					part := []byte(`{"text":""}`)
					part, _ = sjson.SetBytes(part, "text", textResult.String())
					systemInstruction, _ = sjson.SetRawBytes(systemInstruction, "parts.-1", part)
					hasSystemParts = true
				}
			}
			return true
		})
		if hasSystemParts {
			out, _ = sjson.SetRawBytes(out, "system_instruction", systemInstruction)
		}
	} else if systemResult.Type == gjson.String && !util.IsClaudeCodeAttributionSystemText(systemResult.String()) {
		out, _ = sjson.SetBytes(out, "system_instruction.parts.-1.text", systemResult.String())
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
			} else if role == "system" {
				role = "user"
			}

			contentJSON := []byte(`{"role":"","parts":[]}`)
			contentJSON, _ = sjson.SetBytes(contentJSON, "role", role)

			contentsResult := messageResult.Get("content")
			if roleResult.String() == "system" {
				if reminderText, ok := translatorcommon.ClaudeMessageSystemReminderText(contentsResult); ok {
					part := []byte(`{"text":""}`)
					part, _ = sjson.SetBytes(part, "text", reminderText)
					contentJSON, _ = sjson.SetRawBytes(contentJSON, "parts.-1", part)
					out, _ = sjson.SetRawBytes(out, "contents.-1", contentJSON)
				}
				return true
			}
			if contentsResult.IsArray() {
				contentsResult.ForEach(func(_, contentResult gjson.Result) bool {
					switch contentResult.Get("type").String() {
					case "text":
<<<<<<< HEAD:pkg/llmproxy/translator/gemini/claude/gemini_claude_request.go
						text := strings.TrimSpace(contentResult.Get("text").String())
						// Skip empty text parts to avoid Gemini API error:
						// "required oneof field 'data' must have one initialized field"
						if strings.TrimSpace(text) == "" {
=======
						text := contentResult.Get("text").String()
						if text == "" {
>>>>>>> upstream/main:internal/translator/gemini/claude/gemini_claude_request.go
							return true
						}
						part := []byte(`{"text":""}`)
						part, _ = sjson.SetBytes(part, "text", text)
						contentJSON, _ = sjson.SetRawBytes(contentJSON, "parts.-1", part)

					case "tool_use":
						functionName := contentResult.Get("name").String()
						functionArgs := contentResult.Get("input").String()
						argsResult := gjson.Parse(functionArgs)
						if argsResult.IsObject() && gjson.Valid(functionArgs) {
							// Claude may include thought_signature in tool args; Gemini treats this as
							// a base64 thought signature and can reject malformed values.
							sanitizedArgs, err := sjson.DeleteBytes([]byte(functionArgs), "thought_signature")
							if err != nil {
								sanitizedArgs = []byte(functionArgs)
							}
							part := []byte(`{"thoughtSignature":"","functionCall":{"name":"","args":{}}}`)
							part, _ = sjson.SetBytes(part, "thoughtSignature", geminiClaudeThoughtSignature)
							part, _ = sjson.SetBytes(part, "functionCall.name", functionName)
							part, _ = sjson.SetRawBytes(part, "functionCall.args", sanitizedArgs)
							contentJSON, _ = sjson.SetRawBytes(contentJSON, "parts.-1", part)
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
<<<<<<< HEAD:pkg/llmproxy/translator/gemini/claude/gemini_claude_request.go
						responseData := contentResult.Get("content").Raw
=======
						funcName = util.SanitizeFunctionName(funcName)
						toolResult := util.ConvertClaudeToolResultContent(contentResult.Get("content"))
>>>>>>> upstream/main:internal/translator/gemini/claude/gemini_claude_request.go
						part := []byte(`{"functionResponse":{"name":"","response":{"result":""}}}`)
						part, _ = sjson.SetBytes(part, "functionResponse.name", funcName)
						if toolResult.ResultIsRaw {
							part, _ = sjson.SetRawBytes(part, "functionResponse.response.result", []byte(toolResult.Result))
						} else {
							part, _ = sjson.SetBytes(part, "functionResponse.response.result", toolResult.Result)
						}
						contentJSON, _ = sjson.SetRawBytes(contentJSON, "parts.-1", part)
<<<<<<< HEAD:pkg/llmproxy/translator/gemini/claude/gemini_claude_request.go
=======
						for _, img := range toolResult.Images {
							imagePart := []byte(`{"inline_data":{"mime_type":"","data":""}}`)
							imagePart, _ = sjson.SetBytes(imagePart, "inline_data.mime_type", img.MimeType)
							imagePart, _ = sjson.SetBytes(imagePart, "inline_data.data", img.Data)
							contentJSON, _ = sjson.SetRawBytes(contentJSON, "parts.-1", imagePart)
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
						contentJSON, _ = sjson.SetRawBytes(contentJSON, "parts.-1", part)
>>>>>>> upstream/main:internal/translator/gemini/claude/gemini_claude_request.go
					}
					return true
				})
				if len(gjson.GetBytes(contentJSON, "parts").Array()) > 0 {
					out, _ = sjson.SetRawBytes(out, "contents.-1", contentJSON)
				}
			} else if contentsResult.Type == gjson.String {
				text := strings.TrimSpace(contentsResult.String())
				// Skip empty text parts to avoid Gemini API error
				if strings.TrimSpace(text) != "" {
					part := []byte(`{"text":""}`)
					part, _ = sjson.SetBytes(part, "text", text)
					contentJSON, _ = sjson.SetRawBytes(contentJSON, "parts.-1", part)
					out, _ = sjson.SetRawBytes(out, "contents.-1", contentJSON)
				}
			}
			return true
		})
	}

	// strip trailing model turn with unanswered function calls —
	// Gemini returns empty responses when the last turn is a model
	// functionCall with no corresponding user functionResponse.
	contents := gjson.GetBytes(out, "contents")
	if contents.Exists() && contents.IsArray() {
		arr := contents.Array()
		if len(arr) > 0 {
			last := arr[len(arr)-1]
			if last.Get("role").String() == "model" {
				hasFC := false
				last.Get("parts").ForEach(func(_, part gjson.Result) bool {
					if part.Get("functionCall").Exists() {
						hasFC = true
						return false
					}
					return true
				})
				if hasFC {
					out, _ = sjson.DeleteBytes(out, fmt.Sprintf("contents.%d", len(arr)-1))
				}
			}
		}
	}

	// tools
	if toolsResult := gjson.GetBytes(rawJSON, "tools"); toolsResult.IsArray() {
		hasTools := false
		toolsResult.ForEach(func(_, toolResult gjson.Result) bool {
			inputSchemaResult := toolResult.Get("input_schema")
			if inputSchemaResult.Exists() && inputSchemaResult.IsObject() {
<<<<<<< HEAD:pkg/llmproxy/translator/gemini/claude/gemini_claude_request.go
				inputSchema := common.SanitizeParametersJSONSchemaForGemini(inputSchemaResult.Raw)
				tool, _ := sjson.DeleteBytes([]byte(toolResult.Raw), "input_schema")
				tool, _ = sjson.SetRawBytes(tool, "parametersJsonSchema", []byte(inputSchema))
=======
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
>>>>>>> upstream/main:internal/translator/gemini/claude/gemini_claude_request.go
				tool, _ = sjson.DeleteBytes(tool, "strict")
				tool, _ = sjson.DeleteBytes(tool, "input_examples")
				tool, _ = sjson.DeleteBytes(tool, "type")
				tool, _ = sjson.DeleteBytes(tool, "cache_control")
<<<<<<< HEAD:pkg/llmproxy/translator/gemini/claude/gemini_claude_request.go
=======
				tool, _ = sjson.DeleteBytes(tool, "defer_loading")
				tool, _ = sjson.DeleteBytes(tool, "eager_input_streaming")
				tool, _ = sjson.SetBytes(tool, "name", util.SanitizeFunctionName(gjson.GetBytes(tool, "name").String()))
>>>>>>> upstream/main:internal/translator/gemini/claude/gemini_claude_request.go
				if gjson.ValidBytes(tool) && gjson.ParseBytes(tool).IsObject() {
					if !hasTools {
						out, _ = sjson.SetRawBytes(out, "tools", []byte(`[{"functionDeclarations":[]}]`))
						hasTools = true
					}
					out, _ = sjson.SetRawBytes(out, "tools.0.functionDeclarations.-1", tool)
				}
			}
			return true
		})
		if !hasTools {
			out, _ = sjson.DeleteBytes(out, "tools")
		}
	}

	// Map Anthropic thinking -> Gemini thinkingBudget/include_thoughts when enabled
	// Translator only does format conversion, ApplyThinking handles model capability validation.
	if t := gjson.GetBytes(rawJSON, "thinking"); t.Exists() && t.IsObject() {
		switch t.Get("type").String() {
		case "enabled":
			if b := t.Get("budget_tokens"); b.Exists() && b.Type == gjson.Number {
				budget := int(b.Int())
				out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingBudget", budget)
				out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.includeThoughts", true)
			}
		case "adaptive":
			// Keep adaptive as a high level sentinel; ApplyThinking resolves it
			// to model-specific max capability.
			out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingLevel", "high")
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

	result := []byte(out)
	result = common.AttachDefaultSafetySettings(result, "safetySettings")

	return result
}
