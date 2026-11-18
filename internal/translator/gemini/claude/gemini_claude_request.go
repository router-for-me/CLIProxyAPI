// Package claude provides request translation functionality for Claude API.
// It handles parsing and transforming Claude API requests into the internal client format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package also performs JSON data cleaning and transformation to ensure compatibility
// between Claude API format and the internal client's expected format.
package claude

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const geminiClaudeThoughtSignature = "skip_thought_signature_validator"

// ConvertClaudeRequestToGemini parses a Claude API request and returns a complete
// Gemini request body (as JSON bytes) ready to be sent via SendRawMessageStream.
// All JSON transformations are performed using gjson/ojson.
//
// Parameters:
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON request from the Claude API.
//   - stream: A boolean indicating if the request is for a streaming response.
//
// Returns:
//   - []byte: The transformed request in Gemini format.
func ConvertClaudeRequestToGemini(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertClaudeRequestToGemini(modelName, inputRawJSON, stream, false)

}

// ConvertClaudeRequestToGeminiWithCompat preserves assistant thinking blocks
// with empty signatures for configured compatibility endpoints.
func ConvertClaudeRequestToGeminiWithCompat(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertClaudeRequestToGemini(modelName, inputRawJSON, stream, true)
}

func convertClaudeRequestToGemini(modelName string, inputRawJSON []byte, _ bool, preserveEmptyThinkingBlocks bool) ([]byte, error) {
	// The internal commentary marker must be restored as assistant text before
	// any thinking handling, so it is never dropped or mapped to thought.
	rawJSON, _ := sigcompat.RestoreCodexCommentaryMarkers(inputRawJSON)
	var drops translatorcommon.UserTurnDrops
	// Build output Gemini request JSON
	outDoc := ojson.NewDocString(`{"contents":[]}`)
	_ = outDoc.Set("model", modelName)

	// system instruction
	if systemResult := ojson.GetBytesNoCopy(rawJSON, "system"); systemResult.IsArray() {
		systemParts := make([][]byte, 0, 2)
		systemResult.ForEach(func(_, systemPromptResult ojson.Result) bool {
			if systemPromptResult.Get("type").String() == "text" {
				textResult := systemPromptResult.Get("text")
				if textResult.Type == ojson.String {
					if util.IsClaudeCodeAttributionSystemText(textResult.String()) {
						return true
					}
					part := []byte(`{"text":""}`)
					part, _ = ojson.SetBytes(part, "text", textResult.String())
					systemParts = append(systemParts, part)
				}
			}
			return true
		})
		if len(systemParts) > 0 {
			systemInstruction := []byte(`{"role":"user","parts":[]}`)
			systemInstruction, _ = ojson.SetRawBytes(systemInstruction, "parts", translatorcommon.JoinRawArray(systemParts))
			_ = outDoc.SetRawBytes("systemInstruction", systemInstruction)
		}
	} else if systemResult.Type == ojson.String && !util.IsClaudeCodeAttributionSystemText(systemResult.String()) {
		part := []byte(`{"text":""}`)
		part, _ = ojson.SetBytes(part, "text", systemResult.String())
		systemInstruction := []byte(`{"parts":[]}`)
		systemInstruction = translatorcommon.SetRawArrayItems(systemInstruction, "parts", [][]byte{part})
		_ = outDoc.SetRawBytes("systemInstruction", systemInstruction)
	}

	// contents
	if messagesResult := ojson.GetBytesNoCopy(rawJSON, "messages"); messagesResult.IsArray() {
		contentItems := translatorcommon.NewRawArrayItems(messagesResult.Get("#").Int())
		toolNameByID := make(map[string]string)
		var pendingToolUseIDs []string
		messagesResult.ForEach(func(_, messageResult ojson.Result) bool {
			roleResult := messageResult.Get("role")
			if roleResult.Type != ojson.String {
				return true
			}
			originalRole := roleResult.String()
			var precedingToolUseIDs []string
			if originalRole != "system" && originalRole != "developer" {
				precedingToolUseIDs = pendingToolUseIDs
				pendingToolUseIDs = nil
			}
			role := originalRole
			if role == "assistant" {
				role = "model"
			} else if role == "system" || role == "developer" {
				role = "user"
			}

			partItems := make([][]byte, 0, 4)
			contentsResult := messageResult.Get("content")
			if roleResult.String() == "system" || roleResult.String() == "developer" {
				if reminderText, ok := translatorcommon.ClaudeMessageSystemReminderText(contentsResult); ok {
					part := []byte(`{"text":""}`)
					part, _ = ojson.SetBytes(part, "text", reminderText)
					partItems = append(partItems, part)
					contentItems = append(contentItems, geminiContentWithParts(role, partItems))
				}
				return true
			}
			if contentsResult.IsArray() {
				if originalRole == "user" {
					contentsResult = translatorcommon.AlignClaudeToolResults(contentsResult, precedingToolUseIDs)
				}
				var carrierSignatures map[int]string
				if originalRole == "assistant" {
					carrierSignatures = geminiClaudeCarrierSignatures(contentsResult.Array())
				}
				contentsResult.ForEach(func(contentIndex, contentResult ojson.Result) bool {
					carrierSignature := carrierSignatures[int(contentIndex.Int())]
					switch contentResult.Get("type").String() {
					case "text":
						text := contentResult.Get("text").String()
						if text == "" {
							return true
						}
						partDoc := ojson.NewDocString(`{"text":""}`)
						_ = partDoc.Set("text", text)
						if carrierSignature != "" {
							_ = partDoc.Set("thoughtSignature", carrierSignature)
						}
						partItems = append(partItems, partDoc.Bytes())

					case "thinking":
						if contentResult.Get("thinking").String() == "" && strings.TrimSpace(contentResult.Get("signature").String()) != "" {
							if carrierSignature != "" {
								partDoc := ojson.NewDocString(`{"text":"","thought":true}`)
								_ = partDoc.Set("thoughtSignature", carrierSignature)
								partItems = append(partItems, partDoc.Bytes())
							}
							return true
						}
						if !preserveEmptyThinkingBlocks {
							return true
						}
						partDoc := ojson.NewDocString(`{"text":"","thought":true,"thoughtSignature":""}`)
						_ = partDoc.Set("text", contentResult.Get("thinking").String())
						signature := sigcompat.GeminiReplaySignatureOrBypass(contentResult.Get("signature").String(), sigcompat.SignatureBlockKindGeminiModelPart)
						if carrierSignature != "" {
							signature = carrierSignature
						}
						_ = partDoc.Set("thoughtSignature", signature)
						partItems = append(partItems, partDoc.Bytes())

					case "tool_use":
						functionName := contentResult.Get("name").String()
						toolUseID := contentResult.Get("id").String()
						if toolUseID != "" && functionName != "" {
							toolNameByID[toolUseID] = functionName
						}
						functionName = util.SanitizeFunctionName(functionName)
						functionArgs := contentResult.Get("input").String()
						argsResult := ojson.Parse(functionArgs)
						if argsResult.IsObject() && ojson.Valid(functionArgs) {
							partDoc := ojson.NewDocString(`{"thoughtSignature":"","functionCall":{"name":"","args":{}}}`)
							signature := geminiClaudeThoughtSignature
							if carrierSignature != "" {
								signature = carrierSignature
							}
							_ = partDoc.Set("thoughtSignature", signature)
							if toolUseID != "" {
								_ = partDoc.Set("functionCall.id", toolUseID)
							}
							_ = partDoc.Set("functionCall.name", functionName)
							_ = partDoc.SetRaw("functionCall.args", functionArgs)
							partItems = append(partItems, partDoc.Bytes())
							if originalRole == "assistant" {
								pendingToolUseIDs = append(pendingToolUseIDs, toolUseID)
							}
						}

					case "tool_result":
						toolCallID := contentResult.Get("tool_use_id").String()
						if toolCallID == "" {
							return true
						}
						funcName := toolNameByID[toolCallID]
						if funcName == "" {
							funcName = toolNameFromClaudeToolUseID(toolCallID)
						}
						if funcName == "" {
							funcName = toolCallID
						}
						funcName = util.SanitizeFunctionName(funcName)
						toolResult := util.ConvertClaudeToolResult(contentResult)
						partDoc := ojson.NewDocString(`{"functionResponse":{"name":"","response":{}}}`)
						_ = partDoc.Set("functionResponse.id", toolCallID)
						_ = partDoc.Set("functionResponse.name", funcName)
						resultPath := "functionResponse.response.result"
						if toolResult.IsError {
							resultPath = "functionResponse.response.error"
						}
						if toolResult.ResultIsRaw {
							translatorcommon.SetGeminiFunctionResponseRawDoc(partDoc, resultPath, toolResult.Result)
						} else {
							_ = partDoc.Set(resultPath, toolResult.Result)
						}
						for _, img := range toolResult.Images {
							imagePartDoc := ojson.NewDocString(`{"inlineData":{"mimeType":"","data":""}}`)
							_ = imagePartDoc.Set("inlineData.mimeType", img.MimeType)
							_ = imagePartDoc.Set("inlineData.data", img.Data)
							_ = partDoc.SetRawBytes("functionResponse.parts.-1", imagePartDoc.Bytes())
						}
						partItems = append(partItems, partDoc.Bytes())

					case "image", "document", "container_upload":
						if part := claudeBase64InlineData(contentResult.Get("source")); part != nil {
							partItems = append(partItems, part)
						} else if originalRole == "user" {
							// A part that cannot be inlined (url or file source) is dropped; the turn is refused only if nothing else is left.
							drops.Drop(contentResult.Get("type").String())
						}
					default:
						return true
					}
					return true
				})
				if role == "user" {
					partItems = translatorcommon.ReorderGeminiUserParts(partItems)
				}
				if originalRole == "user" {
					// Whitespace-only text is forwarded but never keeps an emptied turn alive.
					drops.EndTurn(translatorcommon.CountSendableGeminiParts(partItems))
				}
				if len(partItems) > 0 {
					contentItems = append(contentItems, geminiContentWithParts(role, partItems))
				}
			} else if contentsResult.Type == ojson.String {
				part := []byte(`{"text":""}`)
				part, _ = ojson.SetBytes(part, "text", contentsResult.String())
				partItems = append(partItems, part)
				contentItems = append(contentItems, geminiContentWithParts(role, partItems))
			}
			return true
		})

		// Strip a trailing model turn with unanswered function calls.
		if len(contentItems) > 0 {
			last := util.ParseGJSONBytesNoCopy(contentItems[len(contentItems)-1])
			if last.Get("role").String() == "model" {
				hasFunctionCall := false
				last.Get("parts").ForEach(func(_, part ojson.Result) bool {
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
		translatorcommon.SetRawArrayItemsDoc(outDoc, "contents", translatorcommon.MergeAdjacentGeminiContents(contentItems))
	}

	// tools
	var toolItems [][]byte
	hasStrictTool := false
	if toolsResult := ojson.GetBytesNoCopy(rawJSON, "tools"); toolsResult.IsArray() {
		toolsResult.ForEach(func(_, toolResult ojson.Result) bool {
			if toolResult.Get("strict").Type == ojson.True {
				hasStrictTool = true
			}
			inputSchemaResult := toolResult.Get("input_schema")
			if inputSchemaResult.Exists() && inputSchemaResult.IsObject() {
				inputSchema := util.CleanJSONSchemaForGeminiJSONSchema(inputSchemaResult.Raw)
				toolDoc := ojson.NewDocString(toolResult.Raw)
				if errDelete := toolDoc.Delete("input_schema"); errDelete != nil {
					return true
				}
				if errSetRaw := toolDoc.SetRaw("parametersJsonSchema", inputSchema); errSetRaw != nil {
					return true
				}
				for _, path := range []string{"strict", "input_examples", "type", "cache_control", "defer_loading", "eager_input_streaming"} {
					if toolResult.Get(path).Exists() {
						_ = toolDoc.Delete(path)
					}
				}
				nameResult := toolResult.Get("name")
				originalName := nameResult.String()
				sanitizedName := util.SanitizeFunctionName(originalName)
				if nameResult.Type != ojson.String || sanitizedName != originalName {
					_ = toolDoc.Set("name", sanitizedName)
				}
				tool := toolDoc.Bytes()
				if ojson.ValidBytes(tool) && util.ParseGJSONBytesNoCopy(tool).IsObject() {
					toolItems = append(toolItems, tool)
				}
			}
			return true
		})
		if len(toolItems) > 0 {
			tools := []byte(`[{"functionDeclarations":[]}]`)
			tools, _ = ojson.SetRawBytes(tools, "0.functionDeclarations", translatorcommon.JoinRawArray(toolItems))
			_ = outDoc.SetRawBytes("tools", tools)
		}
	}

	// tool_choice
	toolChoiceResult := ojson.GetBytesNoCopy(rawJSON, "tool_choice")
	if toolChoiceResult.Exists() && toolChoiceResult.Type != ojson.Null {
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
			if hasStrictTool {
				_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "VALIDATED")
			} else {
				_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "AUTO")
			}
		case "none":
			_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "NONE")
		case "any":
			_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "ANY")
		case "tool":
			_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "ANY")
			if toolChoiceName != "" {
				_ = outDoc.Set("toolConfig.functionCallingConfig.allowedFunctionNames", []string{util.SanitizeFunctionName(toolChoiceName)})
			}
		}
	} else if hasStrictTool && len(toolItems) > 0 {
		_ = outDoc.Set("toolConfig.functionCallingConfig.mode", "VALIDATED")
	}

	// Map Anthropic thinking -> Gemini thinking config when enabled
	// Translator only does format conversion, ApplyThinking handles model capability validation.
	if t := ojson.GetBytesNoCopy(rawJSON, "thinking"); t.Exists() && t.IsObject() {
		switch t.Get("type").String() {
		case "enabled":
			if b := t.Get("budget_tokens"); b.Exists() && b.Type == ojson.Number {
				budget := int(b.Int())
				_ = outDoc.Set("generationConfig.thinkingConfig.thinkingBudget", budget)
			}
		case "adaptive", "auto":
			// For adaptive thinking:
			// - If output_config.effort is explicitly present, pass through as thinkingLevel.
			// - Otherwise, treat it as "enabled with target-model maximum" and emit thinkingBudget=max.
			// ApplyThinking handles clamping to target model's supported levels.
			effort := ""
			if v := ojson.GetBytesNoCopy(rawJSON, "output_config.effort"); v.Exists() && v.Type == ojson.String {
				effort = strings.ToLower(strings.TrimSpace(v.String()))
			}
			if effort != "" {
				_ = outDoc.Set("generationConfig.thinkingConfig.thinkingLevel", effort)
			} else {
				maxBudget := 0
				if mi := registry.LookupModelInfo(modelName, "gemini"); mi != nil && mi.Thinking != nil {
					maxBudget = mi.Thinking.Max
				}
				if maxBudget > 0 {
					_ = outDoc.Set("generationConfig.thinkingConfig.thinkingBudget", maxBudget)
				} else {
					_ = outDoc.Set("generationConfig.thinkingConfig.thinkingLevel", "high")
				}
			}
		}
	}
	if v := ojson.GetBytesNoCopy(rawJSON, "temperature"); v.Exists() && v.Type == ojson.Number {
		_ = outDoc.Set("generationConfig.temperature", v.Num)
	}
	if v := ojson.GetBytesNoCopy(rawJSON, "top_p"); v.Exists() && v.Type == ojson.Number {
		_ = outDoc.Set("generationConfig.topP", v.Num)
	}
	if v := ojson.GetBytesNoCopy(rawJSON, "top_k"); v.Exists() && v.Type == ojson.Number {
		_ = outDoc.Set("generationConfig.topK", v.Num)
	}

	common.AttachDefaultSafetySettingsDoc(outDoc, "safetySettings")

	return outDoc.Bytes(), drops.Err()
}

func claudeBase64InlineData(source ojson.Result) []byte {
	if source.Get("type").String() != "base64" {
		return nil
	}
	mimeType := source.Get("media_type").String()
	data := source.Get("data").String()
	if mimeType == "" || data == "" {
		return nil
	}
	partDoc := ojson.NewDocString(`{"inline_data":{"mime_type":"","data":""}}`)
	_ = partDoc.Set("inline_data.mime_type", mimeType)
	_ = partDoc.Set("inline_data.data", data)
	return partDoc.Bytes()
}

func geminiContentWithParts(role string, parts [][]byte) []byte {
	contentDoc := ojson.NewDocString(`{"role":"","parts":[]}`)
	_ = contentDoc.Set("role", role)
	_ = contentDoc.SetRawBytes("parts", translatorcommon.JoinRawArray(parts))
	return contentDoc.Bytes()
}

func toolNameFromClaudeToolUseID(toolUseID string) string {
	parts := strings.Split(toolUseID, "-")
	if len(parts) <= 1 {
		return ""
	}
	return strings.Join(parts[0:len(parts)-1], "-")
}
