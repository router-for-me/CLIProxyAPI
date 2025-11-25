// Package claude provides request translation functionality for Claude Code API compatibility.
// This package handles the conversion of Claude Code API requests into Gemini CLI-compatible
// JSON format, transforming message contents, system instructions, and tool declarations
// into the format expected by Gemini CLI API clients. It performs JSON data transformation
// to ensure compatibility between Claude Code API format and Gemini CLI API's expected format.
package claude

import (
	"bytes"
	"encoding/json"
	"strings"

	client "github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const geminiCLIClaudeThoughtSignature = "skip_thought_signature_validator"

// ConvertClaudeRequestToAntigravity parses and transforms a Claude Code API request into Gemini CLI API format.
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
func ConvertClaudeRequestToAntigravity(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)
	rawJSON = bytes.Replace(rawJSON, []byte(`"url":{"type":"string","format":"uri",`), []byte(`"url":{"type":"string",`), -1)

	// system instruction
	var systemInstruction *client.Content
	systemResult := gjson.GetBytes(rawJSON, "system")
	if systemResult.IsArray() {
		systemResults := systemResult.Array()
		systemInstruction = &client.Content{Role: "user", Parts: []client.Part{}}
		for i := 0; i < len(systemResults); i++ {
			systemPromptResult := systemResults[i]
			systemTypePromptResult := systemPromptResult.Get("type")
			if systemTypePromptResult.Type == gjson.String && systemTypePromptResult.String() == "text" {
				systemPrompt := systemPromptResult.Get("text").String()
				systemPart := client.Part{Text: systemPrompt}
				systemInstruction.Parts = append(systemInstruction.Parts, systemPart)
			}
		}
		if len(systemInstruction.Parts) == 0 {
			systemInstruction = nil
		}
	}

	// contents
	contents := make([]client.Content, 0)
	messagesResult := gjson.GetBytes(rawJSON, "messages")
	if messagesResult.IsArray() {
		messageResults := messagesResult.Array()
		for i := 0; i < len(messageResults); i++ {
			messageResult := messageResults[i]
			roleResult := messageResult.Get("role")
			if roleResult.Type != gjson.String {
				continue
			}
			role := roleResult.String()
			if role == "assistant" {
				role = "model"
			}
			clientContent := client.Content{Role: role, Parts: []client.Part{}}
			contentsResult := messageResult.Get("content")
			if contentsResult.IsArray() {
				contentResults := contentsResult.Array()
				for j := 0; j < len(contentResults); j++ {
					contentResult := contentResults[j]
					contentTypeResult := contentResult.Get("type")
					if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "text" {
						prompt := contentResult.Get("text").String()
						clientContent.Parts = append(clientContent.Parts, client.Part{Text: prompt})
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "tool_use" {
						functionName := contentResult.Get("name").String()
						functionArgs := contentResult.Get("input").String()
						var args map[string]any
						if err := json.Unmarshal([]byte(functionArgs), &args); err == nil {
							clientContent.Parts = append(clientContent.Parts, client.Part{
								FunctionCall:     &client.FunctionCall{Name: functionName, Args: args},
								ThoughtSignature: geminiCLIClaudeThoughtSignature,
							})
						}
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "tool_result" {
						toolCallID := contentResult.Get("tool_use_id").String()
						if toolCallID != "" {
							funcName := toolCallID
							toolCallIDs := strings.Split(toolCallID, "-")
							if len(toolCallIDs) > 1 {
								funcName = strings.Join(toolCallIDs[0:len(toolCallIDs)-1], "-")
							}
							responseData := contentResult.Get("content").Raw
							functionResponse := client.FunctionResponse{Name: funcName, Response: map[string]interface{}{"result": responseData}}
							clientContent.Parts = append(clientContent.Parts, client.Part{FunctionResponse: &functionResponse})
						}
					}
				}
				contents = append(contents, clientContent)
			} else if contentsResult.Type == gjson.String {
				prompt := contentsResult.String()
				contents = append(contents, client.Content{Role: role, Parts: []client.Part{{Text: prompt}}})
			}
		}
	}

	// tools
	// NOTE:
	// For Antigravity + Claude Code, upstream currently expects a different
	// custom tool schema than the Claude messages API provides. Forwarding
	// tools as-is can cause hard 400 errors like:
	//   tools.0.custom.input_schema: Field required
	// To keep Sonnet 4.5 thinking stable, we intentionally drop tool
	// declarations in this translator and rely on Claude Code's local tools.
	// This preserves core chat / reasoning functionality while avoiding
	// provider_request_error failures.
	var tools []client.ToolDeclaration
	tools = make([]client.ToolDeclaration, 0)

	// Build output Gemini CLI request JSON
	out := `{"model":"","request":{"contents":[]}}`
	out, _ = sjson.Set(out, "model", modelName)
	if systemInstruction != nil {
		b, _ := json.Marshal(systemInstruction)
		out, _ = sjson.SetRaw(out, "request.systemInstruction", string(b))
	}
	if len(contents) > 0 {
		b, _ := json.Marshal(contents)
		out, _ = sjson.SetRaw(out, "request.contents", string(b))
	}
	if len(tools) > 0 && len(tools[0].FunctionDeclarations) > 0 {
		b, _ := json.Marshal(tools)
		out, _ = sjson.SetRaw(out, "request.tools", string(b))
	}

	// Map Anthropic thinking -> Gemini thinkingBudget/include_thoughts when type==enabled
	if t := gjson.GetBytes(rawJSON, "thinking"); t.Exists() && t.IsObject() && util.ModelSupportsThinking(modelName) {
		if t.Get("type").String() == "enabled" {
			if b := t.Get("budget_tokens"); b.Exists() && b.Type == gjson.Number {
				budget := int(b.Int())
				budget = util.NormalizeThinkingBudget(modelName, budget)
				out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.thinkingBudget", budget)
				out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.include_thoughts", true)
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
