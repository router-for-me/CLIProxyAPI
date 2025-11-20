// Package claude provides request translation functionality for Claude API.
// It handles parsing and transforming Claude API requests into the internal client format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package also performs JSON data cleaning and transformation to ensure compatibility
// between Claude API format and the internal client's expected format.
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
								ThoughtSignature: geminiClaudeThoughtSignature,
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
	var tools []client.ToolDeclaration
	toolsResult := gjson.GetBytes(rawJSON, "tools")
	if toolsResult.IsArray() {
		tools = make([]client.ToolDeclaration, 1)
		tools[0].FunctionDeclarations = make([]any, 0)
		toolsResults := toolsResult.Array()
		for i := 0; i < len(toolsResults); i++ {
			toolResult := toolsResults[i]
			inputSchemaResult := toolResult.Get("input_schema")
			if inputSchemaResult.Exists() && inputSchemaResult.IsObject() {
				inputSchema := inputSchemaResult.Raw
				tool, _ := sjson.Delete(toolResult.Raw, "input_schema")
				tool, _ = sjson.SetRaw(tool, "parametersJsonSchema", inputSchema)
				tool, _ = sjson.Delete(tool, "strict")
				tool, _ = sjson.Delete(tool, "input_examples")
				var toolDeclaration any
				if err := json.Unmarshal([]byte(tool), &toolDeclaration); err == nil {
					tools[0].FunctionDeclarations = append(tools[0].FunctionDeclarations, toolDeclaration)
				}
			}
		}
	} else {
		tools = make([]client.ToolDeclaration, 0)
	}

	// Build output Gemini CLI request JSON
	out := `{"contents":[]}`
	out, _ = sjson.Set(out, "model", modelName)
	if systemInstruction != nil {
		b, _ := json.Marshal(systemInstruction)
		out, _ = sjson.SetRaw(out, "system_instruction", string(b))
	}
	if len(contents) > 0 {
		b, _ := json.Marshal(contents)
		out, _ = sjson.SetRaw(out, "contents", string(b))
	}
	if len(tools) > 0 && len(tools[0].FunctionDeclarations) > 0 {
		b, _ := json.Marshal(tools)
		out, _ = sjson.SetRaw(out, "tools", string(b))
	}

	// Map Anthropic thinking -> Gemini thinkingBudget/include_thoughts when enabled
	if t := gjson.GetBytes(rawJSON, "thinking"); t.Exists() && t.IsObject() && util.ModelSupportsThinking(modelName) {
		if t.Get("type").String() == "enabled" {
			if b := t.Get("budget_tokens"); b.Exists() && b.Type == gjson.Number {
				budget := int(b.Int())
				budget = util.NormalizeThinkingBudget(modelName, budget)
				out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingBudget", budget)
				out, _ = sjson.Set(out, "generationConfig.thinkingConfig.include_thoughts", true)
			}
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
