// Package claude provides request translation functionality for Claude Code API compatibility.
// This package handles the conversion of Claude Code API requests into Gemini CLI-compatible
// JSON format, transforming message contents, system instructions, and tool declarations
// into the format expected by Gemini CLI API clients. It performs JSON data transformation
// to ensure compatibility between Claude Code API format and Gemini CLI API's expected format.
package claude

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

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
	enableThoughtTranslate := true
	rawJSON := inputRawJSON

	// system instruction
	var systemInstructionJSON []byte
	hasSystemInstruction := false
	systemResult := gjson.GetBytes(rawJSON, "system")
	if systemResult.IsArray() {
		systemResults := systemResult.Array()
		systemInstructionJSON = []byte(`{"role":"user","parts":[]}`)
		for i := 0; i < len(systemResults); i++ {
			systemPromptResult := systemResults[i]
			systemTypePromptResult := systemPromptResult.Get("type")
			if systemTypePromptResult.Type == gjson.String && systemTypePromptResult.String() == "text" {
				systemPrompt := systemPromptResult.Get("text").String()
				partJSON := []byte(`{}`)
				if systemPrompt != "" {
					partJSON, _ = sjson.SetBytes(partJSON, "text", systemPrompt)
				}
				systemInstructionJSON, _ = sjson.SetRawBytes(systemInstructionJSON, "parts.-1", partJSON)
				hasSystemInstruction = true
			}
		}
	} else if systemResult.Type == gjson.String {
		systemInstructionJSON = []byte(`{"role":"user","parts":[{"text":""}]}`)
		systemInstructionJSON, _ = sjson.SetBytes(systemInstructionJSON, "parts.0.text", systemResult.String())
		hasSystemInstruction = true
	}

	// contents
	contentsJSON := []byte(`[]`)
	hasContents := false

	// tool_use_id → tool_name lookup, populated incrementally during the main loop.
	// Claude's tool_result references tool_use by ID; Gemini requires functionResponse.name.
	toolNameByID := make(map[string]string)

	messagesResult := gjson.GetBytes(rawJSON, "messages")
	if messagesResult.IsArray() {
		messageResults := messagesResult.Array()
		numMessages := len(messageResults)
		for i := 0; i < numMessages; i++ {
			messageResult := messageResults[i]
			roleResult := messageResult.Get("role")
			if roleResult.Type != gjson.String {
				continue
			}
			originalRole := roleResult.String()
			role := originalRole
			if role == "assistant" {
				role = "model"
			}
			clientContentJSON := []byte(`{"role":"","parts":[]}`)
			clientContentJSON, _ = sjson.SetBytes(clientContentJSON, "role", role)
			contentsResult := messageResult.Get("content")
			if contentsResult.IsArray() {
				contentResults := contentsResult.Array()
				numContents := len(contentResults)
				var currentMessageThinkingSignature string
				for j := 0; j < numContents; j++ {
					contentResult := contentResults[j]
					contentTypeResult := contentResult.Get("type")
					if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "thinking" {
						// Use GetThinkingText to handle wrapped thinking objects
						thinkingText := thinking.GetThinkingText(contentResult)

						// Always try cached signature first (more reliable than client-provided)
						// Client may send stale or invalid signatures from different sessions
						signature := ""
						if thinkingText != "" {
							if cachedSig := cache.GetCachedSignature(modelName, thinkingText); cachedSig != "" {
								signature = cachedSig
								// log.Debugf("Using cached signature for thinking block")
							}
						}

						// Fallback to client signature only if cache miss and client signature is valid
						if signature == "" {
							signatureResult := contentResult.Get("signature")
							clientSignature := ""
							if signatureResult.Exists() && signatureResult.String() != "" {
								arrayClientSignatures := strings.SplitN(signatureResult.String(), "#", 2)
								if len(arrayClientSignatures) == 2 {
									if cache.GetModelGroup(modelName) == arrayClientSignatures[0] {
										clientSignature = arrayClientSignatures[1]
									}
								}
							}
							if cache.HasValidSignature(modelName, clientSignature) {
								signature = clientSignature
							}
							// log.Debugf("Using client-provided signature for thinking block")
						}

						// Store for subsequent tool_use in the same message
						if cache.HasValidSignature(modelName, signature) {
							currentMessageThinkingSignature = signature
						}

						// Skip trailing unsigned thinking blocks on last assistant message
						isUnsigned := !cache.HasValidSignature(modelName, signature)

						// If unsigned, skip entirely (don't convert to text)
						// Claude requires assistant messages to start with thinking blocks when thinking is enabled
						// Converting to text would break this requirement
						if isUnsigned {
							// log.Debugf("Dropping unsigned thinking block (no valid signature)")
							enableThoughtTranslate = false
							continue
						}

						// Valid signature, send as thought block
						// Always include "text" field — Google Antigravity API requires it
						// even for redacted thinking where the text is empty.
						partJSON := []byte(`{}`)
						partJSON, _ = sjson.SetBytes(partJSON, "thought", true)
						partJSON, _ = sjson.SetBytes(partJSON, "text", thinkingText)
						if signature != "" {
							partJSON, _ = sjson.SetBytes(partJSON, "thoughtSignature", signature)
						}
						clientContentJSON, _ = sjson.SetRawBytes(clientContentJSON, "parts.-1", partJSON)
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "text" {
						prompt := contentResult.Get("text").String()
						// Skip empty text parts to avoid Gemini API error:
						// "required oneof field 'data' must have one initialized field"
						if prompt == "" {
							continue
						}
						partJSON := []byte(`{}`)
						partJSON, _ = sjson.SetBytes(partJSON, "text", prompt)
						clientContentJSON, _ = sjson.SetRawBytes(clientContentJSON, "parts.-1", partJSON)
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "tool_use" {
						// NOTE: Do NOT inject dummy thinking blocks here.
						// Antigravity API validates signatures, so dummy values are rejected.

						functionName := util.SanitizeFunctionName(contentResult.Get("name").String())
						argsResult := contentResult.Get("input")
						functionID := contentResult.Get("id").String()

						if functionID != "" && functionName != "" {
							toolNameByID[functionID] = functionName
						}

						// Handle both object and string input formats
						var argsRaw string
						if argsResult.IsObject() {
							argsRaw = argsResult.Raw
						} else if argsResult.Type == gjson.String {
							// Input is a JSON string, parse and validate it
							parsed := gjson.Parse(argsResult.String())
							if parsed.IsObject() {
								argsRaw = parsed.Raw
							}
						}

						if argsRaw != "" {
							partJSON := []byte(`{}`)

							// Use skip_thought_signature_validator for tool calls without valid thinking signature
							// This is the approach used in opencode-google-antigravity-auth for Gemini
							// and also works for Claude through Antigravity API
							const skipSentinel = "skip_thought_signature_validator"
							if cache.HasValidSignature(modelName, currentMessageThinkingSignature) {
								partJSON, _ = sjson.SetBytes(partJSON, "thoughtSignature", currentMessageThinkingSignature)
							} else {
								// No valid signature - use skip sentinel to bypass validation
								partJSON, _ = sjson.SetBytes(partJSON, "thoughtSignature", skipSentinel)
							}

							if functionID != "" {
								partJSON, _ = sjson.SetBytes(partJSON, "functionCall.id", functionID)
							}
							partJSON, _ = sjson.SetBytes(partJSON, "functionCall.name", functionName)
							partJSON, _ = sjson.SetRawBytes(partJSON, "functionCall.args", []byte(argsRaw))
							clientContentJSON, _ = sjson.SetRawBytes(clientContentJSON, "parts.-1", partJSON)
						}
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "tool_result" {
						toolCallID := contentResult.Get("tool_use_id").String()
						if toolCallID != "" {
							funcName, ok := toolNameByID[toolCallID]
							if !ok {
								// Fallback: derive a semantic name from the ID by stripping
								// the last two dash-separated segments (e.g. "get_weather-call-123" → "get_weather").
								// Only use the raw ID as a last resort when the heuristic produces an empty string.
								parts := strings.Split(toolCallID, "-")
								if len(parts) > 2 {
									funcName = strings.Join(parts[:len(parts)-2], "-")
								}
								if funcName == "" {
									funcName = toolCallID
								}
								log.Warnf("antigravity claude request: tool_result references unknown tool_use_id=%s, derived function name=%s", toolCallID, funcName)
							}
							functionResponseResult := contentResult.Get("content")

							functionResponseJSON := []byte(`{}`)
							functionResponseJSON, _ = sjson.SetBytes(functionResponseJSON, "id", toolCallID)
							functionResponseJSON, _ = sjson.SetBytes(functionResponseJSON, "name", util.SanitizeFunctionName(funcName))

							responseData := ""
							if functionResponseResult.Type == gjson.String {
								responseData = functionResponseResult.String()
								functionResponseJSON, _ = sjson.SetBytes(functionResponseJSON, "response.result", responseData)
							} else if functionResponseResult.IsArray() {
								frResults := functionResponseResult.Array()
								nonImageCount := 0
								lastNonImageRaw := ""
								filteredJSON := []byte(`[]`)
								imagePartsJSON := []byte(`[]`)
								for _, fr := range frResults {
									if fr.Get("type").String() == "image" && fr.Get("source.type").String() == "base64" {
										inlineDataJSON := []byte(`{}`)
										if mimeType := fr.Get("source.media_type").String(); mimeType != "" {
											inlineDataJSON, _ = sjson.SetBytes(inlineDataJSON, "mimeType", mimeType)
										}
										if data := fr.Get("source.data").String(); data != "" {
											inlineDataJSON, _ = sjson.SetBytes(inlineDataJSON, "data", data)
										}

										imagePartJSON := []byte(`{}`)
										imagePartJSON, _ = sjson.SetRawBytes(imagePartJSON, "inlineData", inlineDataJSON)
										imagePartsJSON, _ = sjson.SetRawBytes(imagePartsJSON, "-1", imagePartJSON)
										continue
									}

									nonImageCount++
									lastNonImageRaw = fr.Raw
									filteredJSON, _ = sjson.SetRawBytes(filteredJSON, "-1", []byte(fr.Raw))
								}

								if nonImageCount == 1 {
									functionResponseJSON, _ = sjson.SetRawBytes(functionResponseJSON, "response.result", []byte(lastNonImageRaw))
								} else if nonImageCount > 1 {
									functionResponseJSON, _ = sjson.SetRawBytes(functionResponseJSON, "response.result", filteredJSON)
								} else {
									functionResponseJSON, _ = sjson.SetBytes(functionResponseJSON, "response.result", "")
								}

								// Place image data inside functionResponse.parts as inlineData
								// instead of as sibling parts in the outer content, to avoid
								// base64 data bloating the text context.
								if gjson.GetBytes(imagePartsJSON, "#").Int() > 0 {
									functionResponseJSON, _ = sjson.SetRawBytes(functionResponseJSON, "parts", imagePartsJSON)
								}

							} else if functionResponseResult.IsObject() {
								if functionResponseResult.Get("type").String() == "image" && functionResponseResult.Get("source.type").String() == "base64" {
									inlineDataJSON := []byte(`{}`)
									if mimeType := functionResponseResult.Get("source.media_type").String(); mimeType != "" {
										inlineDataJSON, _ = sjson.SetBytes(inlineDataJSON, "mimeType", mimeType)
									}
									if data := functionResponseResult.Get("source.data").String(); data != "" {
										inlineDataJSON, _ = sjson.SetBytes(inlineDataJSON, "data", data)
									}

									imagePartJSON := []byte(`{}`)
									imagePartJSON, _ = sjson.SetRawBytes(imagePartJSON, "inlineData", inlineDataJSON)
									imagePartsJSON := []byte(`[]`)
									imagePartsJSON, _ = sjson.SetRawBytes(imagePartsJSON, "-1", imagePartJSON)
									functionResponseJSON, _ = sjson.SetRawBytes(functionResponseJSON, "parts", imagePartsJSON)
									functionResponseJSON, _ = sjson.SetBytes(functionResponseJSON, "response.result", "")
								} else {
									functionResponseJSON, _ = sjson.SetRawBytes(functionResponseJSON, "response.result", []byte(functionResponseResult.Raw))
								}
							} else if functionResponseResult.Raw != "" {
								functionResponseJSON, _ = sjson.SetRawBytes(functionResponseJSON, "response.result", []byte(functionResponseResult.Raw))
							} else {
								// Content field is missing entirely — .Raw is empty which
								// causes sjson.SetRaw to produce invalid JSON (e.g. "result":}).
								functionResponseJSON, _ = sjson.SetBytes(functionResponseJSON, "response.result", "")
							}

							partJSON := []byte(`{}`)
							partJSON, _ = sjson.SetRawBytes(partJSON, "functionResponse", functionResponseJSON)
							clientContentJSON, _ = sjson.SetRawBytes(clientContentJSON, "parts.-1", partJSON)
						}
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "image" {
						sourceResult := contentResult.Get("source")
						if sourceResult.Get("type").String() == "base64" {
							inlineDataJSON := []byte(`{}`)
							if mimeType := sourceResult.Get("media_type").String(); mimeType != "" {
								inlineDataJSON, _ = sjson.SetBytes(inlineDataJSON, "mimeType", mimeType)
							}
							if data := sourceResult.Get("data").String(); data != "" {
								inlineDataJSON, _ = sjson.SetBytes(inlineDataJSON, "data", data)
							}

							partJSON := []byte(`{}`)
							partJSON, _ = sjson.SetRawBytes(partJSON, "inlineData", inlineDataJSON)
							clientContentJSON, _ = sjson.SetRawBytes(clientContentJSON, "parts.-1", partJSON)
						}
					}
				}

				// Reorder parts for 'model' role:
				// 1. Thinking parts first (Antigravity API requirement)
				// 2. Regular parts (text, inlineData, etc.)
				// 3. FunctionCall parts last
				//
				// Moving functionCall parts to the end prevents tool_use↔tool_result
				// pairing breakage: the Antigravity API internally splits model messages
				// at functionCall boundaries. If a text part follows a functionCall, the
				// split creates an extra assistant turn between tool_use and tool_result,
				// which Claude rejects with "tool_use ids were found without tool_result
				// blocks immediately after".
				if role == "model" {
					partsResult := gjson.GetBytes(clientContentJSON, "parts")
					if partsResult.IsArray() {
						parts := partsResult.Array()
						if len(parts) > 1 {
							var thinkingParts []gjson.Result
							var regularParts []gjson.Result
							var functionCallParts []gjson.Result
							for _, part := range parts {
								if part.Get("thought").Bool() {
									thinkingParts = append(thinkingParts, part)
								} else if part.Get("functionCall").Exists() {
									functionCallParts = append(functionCallParts, part)
								} else {
									regularParts = append(regularParts, part)
								}
							}
							var newParts []interface{}
							for _, p := range thinkingParts {
								newParts = append(newParts, p.Value())
							}
							for _, p := range regularParts {
								newParts = append(newParts, p.Value())
							}
							for _, p := range functionCallParts {
								newParts = append(newParts, p.Value())
							}
							clientContentJSON, _ = sjson.SetBytes(clientContentJSON, "parts", newParts)
						}
					}
				}

				// Skip messages with empty parts array to avoid Gemini API error:
				// "required oneof field 'data' must have one initialized field"
				partsCheck := gjson.GetBytes(clientContentJSON, "parts")
				if !partsCheck.IsArray() || len(partsCheck.Array()) == 0 {
					continue
				}

				contentsJSON, _ = sjson.SetRawBytes(contentsJSON, "-1", clientContentJSON)
				hasContents = true
			} else if contentsResult.Type == gjson.String {
				prompt := contentsResult.String()
				partJSON := []byte(`{}`)
				if prompt != "" {
					partJSON, _ = sjson.SetBytes(partJSON, "text", prompt)
				}
				clientContentJSON, _ = sjson.SetRawBytes(clientContentJSON, "parts.-1", partJSON)
				contentsJSON, _ = sjson.SetRawBytes(contentsJSON, "-1", clientContentJSON)
				hasContents = true
			}
		}
	}

	// tools
	var toolsJSON []byte
	toolDeclCount := 0
	allowedToolKeys := []string{"name", "description", "behavior", "parameters", "parametersJsonSchema", "response", "responseJsonSchema"}
	toolsResult := gjson.GetBytes(rawJSON, "tools")
	if toolsResult.IsArray() {
		toolsJSON = []byte(`[{"functionDeclarations":[]}]`)
		toolsResults := toolsResult.Array()
		for i := 0; i < len(toolsResults); i++ {
			toolResult := toolsResults[i]
			inputSchemaResult := toolResult.Get("input_schema")
			if inputSchemaResult.Exists() && inputSchemaResult.IsObject() {
				// Sanitize the input schema for Antigravity API compatibility
				inputSchema := util.CleanJSONSchemaForAntigravity(inputSchemaResult.Raw)
				tool, _ := sjson.DeleteBytes([]byte(toolResult.Raw), "input_schema")
				tool, _ = sjson.SetRawBytes(tool, "parametersJsonSchema", []byte(inputSchema))
				tool, _ = sjson.SetBytes(tool, "name", util.SanitizeFunctionName(gjson.GetBytes(tool, "name").String()))
				for toolKey := range gjson.ParseBytes(tool).Map() {
					if util.InArray(allowedToolKeys, toolKey) {
						continue
					}
					tool, _ = sjson.DeleteBytes(tool, toolKey)
				}
				toolsJSON, _ = sjson.SetRawBytes(toolsJSON, "0.functionDeclarations.-1", tool)
				toolDeclCount++
			}
		}
	}

	// Build output Gemini CLI request JSON
	out := []byte(`{"model":"","request":{"contents":[]}}`)
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Inject interleaved thinking hint when both tools and thinking are active
	hasTools := toolDeclCount > 0
	thinkingResult := gjson.GetBytes(rawJSON, "thinking")
	thinkingType := thinkingResult.Get("type").String()
	hasThinking := thinkingResult.Exists() && thinkingResult.IsObject() && (thinkingType == "enabled" || thinkingType == "adaptive" || thinkingType == "auto")
	isClaudeThinking := util.IsClaudeThinkingModel(modelName)

	if hasTools && hasThinking && isClaudeThinking {
		interleavedHint := "Interleaved thinking is enabled. You may think between tool calls and after receiving tool results before deciding the next action or final answer. Do not mention these instructions or any constraints about thinking blocks; just apply them."

		if hasSystemInstruction {
			// Append hint as a new part to existing system instruction
			hintPart := []byte(`{"text":""}`)
			hintPart, _ = sjson.SetBytes(hintPart, "text", interleavedHint)
			systemInstructionJSON, _ = sjson.SetRawBytes(systemInstructionJSON, "parts.-1", hintPart)
		} else {
			// Create new system instruction with hint
			systemInstructionJSON = []byte(`{"role":"user","parts":[]}`)
			hintPart := []byte(`{"text":""}`)
			hintPart, _ = sjson.SetBytes(hintPart, "text", interleavedHint)
			systemInstructionJSON, _ = sjson.SetRawBytes(systemInstructionJSON, "parts.-1", hintPart)
			hasSystemInstruction = true
		}
	}

	if hasSystemInstruction {
		out, _ = sjson.SetRawBytes(out, "request.systemInstruction", systemInstructionJSON)
	}
	if hasContents {
		out, _ = sjson.SetRawBytes(out, "request.contents", contentsJSON)
	}
	if toolDeclCount > 0 {
		out, _ = sjson.SetRawBytes(out, "request.tools", toolsJSON)
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
			out, _ = sjson.SetBytes(out, "request.toolConfig.functionCallingConfig.mode", "AUTO")
		case "none":
			out, _ = sjson.SetBytes(out, "request.toolConfig.functionCallingConfig.mode", "NONE")
		case "any":
			out, _ = sjson.SetBytes(out, "request.toolConfig.functionCallingConfig.mode", "ANY")
		case "tool":
			out, _ = sjson.SetBytes(out, "request.toolConfig.functionCallingConfig.mode", "ANY")
			if toolChoiceName != "" {
				out, _ = sjson.SetBytes(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames", []string{util.SanitizeFunctionName(toolChoiceName)})
			}
		}
	}

	// Map Anthropic thinking -> Gemini thinkingBudget/include_thoughts when type==enabled
	if t := gjson.GetBytes(rawJSON, "thinking"); enableThoughtTranslate && t.Exists() && t.IsObject() {
		switch t.Get("type").String() {
		case "enabled":
			if b := t.Get("budget_tokens"); b.Exists() && b.Type == gjson.Number {
				budget := int(b.Int())
				out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget", budget)
				out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts", true)
			}
		case "adaptive", "auto":
			// For adaptive thinking:
			// - If output_config.effort is explicitly present, pass through as thinkingLevel.
			// - Otherwise, treat it as "enabled with target-model maximum" and emit high.
			// ApplyThinking handles clamping to target model's supported levels.
			effort := ""
			if v := gjson.GetBytes(rawJSON, "output_config.effort"); v.Exists() && v.Type == gjson.String {
				effort = strings.ToLower(strings.TrimSpace(v.String()))
			}
			if effort != "" {
				out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel", effort)
			} else {
				out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel", "high")
			}
			out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts", true)
		}
	}
	if v := gjson.GetBytes(rawJSON, "temperature"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.temperature", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "top_p"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.topP", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "top_k"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.topK", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "max_tokens"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.maxOutputTokens", v.Num)
	}

	out = common.AttachDefaultSafetySettings(out, "request.safetySettings")

	return out
}
