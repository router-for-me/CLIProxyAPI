package responses

import (
	"encoding/json"
	"strings"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const geminiResponsesThoughtSignature = "skip_thought_signature_validator"

func ConvertOpenAIResponsesRequestToGemini(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON

	// Note: stream parameter is part of the fixed method signature
	useGeminiNativeReasoningLayout := sigcompat.SignatureProviderFromModelName(modelName) == sigcompat.SignatureProviderGemini
	_ = stream // Unused but required by interface

	// Base Gemini API template (do not include thinkingConfig by default)
	out := []byte(`{"contents":[]}`)

	root := gjson.ParseBytes(rawJSON)

	// Extract system instruction from OpenAI "instructions" field.
	systemParts := make([][]byte, 0, 2)
	if instructions := root.Get("instructions"); instructions.Exists() {
		part := []byte(`{"text":""}`)
		part, _ = sjson.SetBytes(part, "text", instructions.String())
		systemParts = append(systemParts, part)
		out, _ = sjson.SetRawBytes(out, "systemInstruction", geminiSystemInstruction(systemParts))
	}

	// Convert input messages to Gemini contents format
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		items := input.Array()
		contentItems := make([][]byte, 0, len(items))
		functionNamesByCallID := make(map[string]string)
		for _, item := range items {
			if item.Get("type").String() == "function_call" {
				callID := item.Get("call_id").String()
				if _, exists := functionNamesByCallID[callID]; !exists {
					functionNamesByCallID[callID] = item.Get("name").String()
				}
			}
		}

		// Normalize consecutive function calls and outputs so each call is immediately followed by its response
		normalized := make([]gjson.Result, 0, len(items))
		for i := 0; i < len(items); {
			item := items[i]
			itemType := item.Get("type").String()
			itemRole := item.Get("role").String()
			if itemType == "" && itemRole != "" {
				itemType = "message"
			}

			if itemType == "function_call" {
				var calls []gjson.Result
				var outputs []gjson.Result

				for i < len(items) {
					next := items[i]
					nextType := next.Get("type").String()
					nextRole := next.Get("role").String()
					if nextType == "" && nextRole != "" {
						nextType = "message"
					}
					if nextType != "function_call" {
						break
					}
					calls = append(calls, next)
					i++
				}

				for i < len(items) {
					next := items[i]
					nextType := next.Get("type").String()
					nextRole := next.Get("role").String()
					if nextType == "" && nextRole != "" {
						nextType = "message"
					}
					if nextType != "function_call_output" {
						break
					}
					outputs = append(outputs, next)
					i++
				}

				if len(calls) > 0 {
					outputMap := make(map[string]gjson.Result, len(outputs))
					for _, outItem := range outputs {
						outputMap[outItem.Get("call_id").String()] = outItem
					}
					for _, call := range calls {
						normalized = append(normalized, call)
						callID := call.Get("call_id").String()
						if resp, ok := outputMap[callID]; ok {
							normalized = append(normalized, resp)
							delete(outputMap, callID)
						}
					}
					for _, outItem := range outputs {
						if _, ok := outputMap[outItem.Get("call_id").String()]; ok {
							normalized = append(normalized, outItem)
						}
					}
					continue
				}
			}

			if itemType == "function_call_output" {
				normalized = append(normalized, item)
				i++
				continue
			}

			normalized = append(normalized, item)
			i++
		}

		for i := 0; i < len(normalized); i++ {
			item := normalized[i]
			itemType := item.Get("type").String()
			itemRole := item.Get("role").String()
			if itemType == "" && itemRole != "" {
				itemType = "message"
			}

			switch itemType {
			case "message":
				if strings.EqualFold(itemRole, "system") || strings.EqualFold(itemRole, "developer") {
					if contentArray := item.Get("content"); contentArray.Exists() {
						if contentArray.IsArray() {
							contentArray.ForEach(func(_, contentItem gjson.Result) bool {
								part := []byte(`{"text":""}`)
								part, _ = sjson.SetBytes(part, "text", contentItem.Get("text").String())
								systemParts = append(systemParts, part)
								return true
							})
						} else if contentArray.Type == gjson.String {
							part := []byte(`{"text":""}`)
							part, _ = sjson.SetBytes(part, "text", contentArray.String())
							systemParts = append(systemParts, part)
						}
					}
					continue
				}

				// Handle regular messages
				// Note: In Responses format, model outputs may appear as content items with type "output_text"
				// even when the message.role is "user". We split such items into distinct Gemini messages
				// with roles derived from the content type to match docs/convert-2.md.
				if contentArray := item.Get("content"); contentArray.Exists() && contentArray.IsArray() {
					currentRole := ""
					currentParts := make([][]byte, 0)

					flush := func() {
						if currentRole == "" || len(currentParts) == 0 {
							currentParts = currentParts[:0]
							return
						}
						contentItems = append(contentItems, geminiContent(currentRole, currentParts))
						currentParts = currentParts[:0]
					}

					contentArray.ForEach(func(_, contentItem gjson.Result) bool {
						contentType := contentItem.Get("type").String()
						if contentType == "" {
							contentType = "input_text"
						}

						effRole := "user"
						if itemRole != "" {
							switch strings.ToLower(itemRole) {
							case "assistant", "model":
								effRole = "model"
							default:
								effRole = strings.ToLower(itemRole)
							}
						}
						if contentType == "output_text" {
							effRole = "model"
						}
						if effRole == "assistant" {
							effRole = "model"
						}

						if currentRole != "" && effRole != currentRole {
							flush()
							currentRole = ""
						}
						if currentRole == "" {
							currentRole = effRole
						}

						var partJSON []byte
						switch contentType {
						case "input_text", "output_text":
							if text := contentItem.Get("text"); text.Exists() {
								partJSON = []byte(`{"text":""}`)
								partJSON, _ = sjson.SetBytes(partJSON, "text", text.String())
							}
						case "input_image":
							imageURL := contentItem.Get("image_url").String()
							if imageURL == "" {
								imageURL = contentItem.Get("url").String()
							}
							if imageURL != "" {
								mimeType := "application/octet-stream"
								data := ""
								if strings.HasPrefix(imageURL, "data:") {
									trimmed := strings.TrimPrefix(imageURL, "data:")
									mediaAndData := strings.SplitN(trimmed, ";base64,", 2)
									if len(mediaAndData) == 2 {
										if mediaAndData[0] != "" {
											mimeType = mediaAndData[0]
										}
										data = mediaAndData[1]
									} else {
										mediaAndData = strings.SplitN(trimmed, ",", 2)
										if len(mediaAndData) == 2 {
											if mediaAndData[0] != "" {
												mimeType = mediaAndData[0]
											}
											data = mediaAndData[1]
										}
									}
								}
								if data != "" {
									partJSON = []byte(`{"inline_data":{"mime_type":"","data":""}}`)
									partJSON, _ = sjson.SetBytes(partJSON, "inline_data.mime_type", mimeType)
									partJSON, _ = sjson.SetBytes(partJSON, "inline_data.data", data)
								}
							}
						case "input_audio":
							audioData := contentItem.Get("data").String()
							audioFormat := contentItem.Get("format").String()
							if audioData != "" {
								audioMimeMap := map[string]string{
									"mp3":       "audio/mpeg",
									"wav":       "audio/wav",
									"ogg":       "audio/ogg",
									"flac":      "audio/flac",
									"aac":       "audio/aac",
									"webm":      "audio/webm",
									"pcm16":     "audio/pcm",
									"g711_ulaw": "audio/basic",
									"g711_alaw": "audio/basic",
								}
								mimeType := "audio/wav"
								if audioFormat != "" {
									if mapped, ok := audioMimeMap[audioFormat]; ok {
										mimeType = mapped
									} else {
										mimeType = "audio/" + audioFormat
									}
								}
								partJSON = []byte(`{"inline_data":{"mime_type":"","data":""}}`)
								partJSON, _ = sjson.SetBytes(partJSON, "inline_data.mime_type", mimeType)
								partJSON, _ = sjson.SetBytes(partJSON, "inline_data.data", audioData)
							}
						}

						if len(partJSON) > 0 {
							currentParts = append(currentParts, partJSON)
						}
						return true
					})

					flush()
				} else if contentArray.Type == gjson.String {
					effRole := "user"
					if itemRole != "" {
						switch strings.ToLower(itemRole) {
						case "assistant", "model":
							effRole = "model"
						default:
							effRole = strings.ToLower(itemRole)
						}
					}

					part := []byte(`{"text":""}`)
					part, _ = sjson.SetBytes(part, "text", contentArray.String())
					contentItems = append(contentItems, geminiContent(effRole, [][]byte{part}))
				}

			case "function_call":
				// Handle function calls - convert to model message with functionCall
				name := util.SanitizeFunctionName(item.Get("name").String())
				arguments := item.Get("arguments").String()

				modelContent := []byte(`{"role":"model","parts":[]}`)
				functionCall := []byte(`{"functionCall":{"name":"","args":{}}}`)
				functionCall, _ = sjson.SetBytes(functionCall, "functionCall.name", name)
				functionCall, _ = sjson.SetBytes(functionCall, "thoughtSignature", geminiResponsesThoughtSignature)
				functionCall, _ = sjson.SetBytes(functionCall, "functionCall.id", item.Get("call_id").String())

				// Parse arguments JSON string and set as args object
				if arguments != "" {
					argsResult := gjson.Parse(arguments)
					functionCall, _ = sjson.SetRawBytes(functionCall, "functionCall.args", []byte(argsResult.Raw))
				}

				modelContent, _ = sjson.SetRawBytes(modelContent, "parts", translatorcommon.JoinRawArray([][]byte{functionCall}))
				contentItems = append(contentItems, modelContent)

			case "function_call_output":
				// Handle function call outputs - convert to function message with functionResponse
				callID := item.Get("call_id").String()
				// Use .Raw to preserve the JSON encoding (includes quotes for strings)
				outputRaw := item.Get("output").Str

				functionContent := []byte(`{"role":"function","parts":[]}`)
				functionResponse := []byte(`{"functionResponse":{"name":"","response":{}}}`)

				functionName := "unknown"
				if matchedName, ok := functionNamesByCallID[callID]; ok {
					functionName = matchedName
				}
				functionName = util.SanitizeFunctionName(functionName)

				functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.name", functionName)
				functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.id", callID)

				// Set the raw JSON output directly (preserves string encoding)
				if outputRaw != "" && outputRaw != "null" {
					output := gjson.Parse(outputRaw)
					if output.Type == gjson.JSON && json.Valid([]byte(output.Raw)) {
						functionResponse, _ = sjson.SetRawBytes(functionResponse, "functionResponse.response.result", []byte(output.Raw))
					} else {
						functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.response.result", outputRaw)
					}
				}
				functionContent, _ = sjson.SetRawBytes(functionContent, "parts", translatorcommon.JoinRawArray([][]byte{functionResponse}))
				contentItems = append(contentItems, functionContent)

			case "reasoning":
				thoughtText := item.Get("summary.0.text").String()
				signature := openAIResponsesGeminiThoughtSignature(item.Get("encrypted_content").String())

				visibleText := ""
				if useGeminiNativeReasoningLayout && i+1 < len(normalized) {
					next := normalized[i+1]
					if visible, ok := openAIResponsesAssistantVisibleText(next); ok {
						visibleText = visible
						i++
					}
				}

				modelContent := buildOpenAIResponsesReasoningModelContent(thoughtText, visibleText, signature, useGeminiNativeReasoningLayout)
				contentItems = append(contentItems, modelContent)
			}
		}
		if len(contentItems) > 0 && shouldStripTrailingOpenAIResponsesModelPrefill(gjson.ParseBytes(contentItems[len(contentItems)-1])) {
			contentItems = contentItems[:len(contentItems)-1]
		}
		out = translatorcommon.SetRawArrayItems(out, "contents", contentItems)
	} else if input.Exists() && input.Type == gjson.String {
		// Simple string input conversion to user message.
		part := []byte(`{"text":""}`)
		part, _ = sjson.SetBytes(part, "text", input.String())
		out = translatorcommon.SetRawArrayItems(out, "contents", [][]byte{geminiContent("user", [][]byte{part})})
	}
	if len(systemParts) > 0 {
		out, _ = sjson.SetRawBytes(out, "systemInstruction", geminiSystemInstruction(systemParts))
	}

	// Convert tools to Gemini functionDeclarations format
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		var functionDeclarations [][]byte
		tools.ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("type").String() == "function" {
				funcDecl := []byte(`{"name":"","description":"","parametersJsonSchema":{}}`)

				if name := tool.Get("name"); name.Exists() {
					funcDecl, _ = sjson.SetBytes(funcDecl, "name", util.SanitizeFunctionName(name.String()))
				}
				if desc := tool.Get("description"); desc.Exists() {
					funcDecl, _ = sjson.SetBytes(funcDecl, "description", desc.String())
				}
				if params := tool.Get("parameters"); params.Exists() {
					funcDecl, _ = sjson.SetRawBytes(funcDecl, "parametersJsonSchema", []byte(util.CleanJSONSchemaForGemini(params.Raw)))
				}

				functionDeclarations = append(functionDeclarations, funcDecl)
			}
			return true
		})

		// Only add tools if there are function declarations.
		if len(functionDeclarations) > 0 {
			geminiTools := []byte(`[{"functionDeclarations":[]}]`)
			geminiTools, _ = sjson.SetRawBytes(geminiTools, "0.functionDeclarations", translatorcommon.JoinRawArray(functionDeclarations))
			out, _ = sjson.SetRawBytes(out, "tools", geminiTools)
		}
	}

	// Handle generation config from OpenAI format
	if maxOutputTokens := root.Get("max_output_tokens"); maxOutputTokens.Exists() {
		genConfig := []byte(`{"maxOutputTokens":0}`)
		genConfig, _ = sjson.SetBytes(genConfig, "maxOutputTokens", maxOutputTokens.Int())
		out, _ = sjson.SetRawBytes(out, "generationConfig", genConfig)
	}

	// Handle temperature if present
	if temperature := root.Get("temperature"); temperature.Exists() {
		if !gjson.GetBytes(out, "generationConfig").Exists() {
			out, _ = sjson.SetRawBytes(out, "generationConfig", []byte(`{}`))
		}
		out, _ = sjson.SetBytes(out, "generationConfig.temperature", temperature.Float())
	}

	// Handle top_p if present
	if topP := root.Get("top_p"); topP.Exists() {
		if !gjson.GetBytes(out, "generationConfig").Exists() {
			out, _ = sjson.SetRawBytes(out, "generationConfig", []byte(`{}`))
		}
		out, _ = sjson.SetBytes(out, "generationConfig.topP", topP.Float())
	}

	// Handle stop sequences
	if stopSequences := root.Get("stop_sequences"); stopSequences.Exists() && stopSequences.IsArray() {
		if !gjson.GetBytes(out, "generationConfig").Exists() {
			out, _ = sjson.SetRawBytes(out, "generationConfig", []byte(`{}`))
		}
		var sequences []string
		stopSequences.ForEach(func(_, seq gjson.Result) bool {
			sequences = append(sequences, seq.String())
			return true
		})
		out, _ = sjson.SetBytes(out, "generationConfig.stopSequences", sequences)
	}

	out = applyOpenAIResponsesTextFormatToGemini(out, root)

	// Apply thinking configuration: convert OpenAI Responses API reasoning.effort to Gemini thinkingConfig.
	// Inline translation-only mapping; capability checks happen later in ApplyThinking.
	re := root.Get("reasoning.effort")
	if re.Exists() {
		effort := strings.ToLower(strings.TrimSpace(re.String()))
		if effort != "" {
			thinkingPath := "generationConfig.thinkingConfig"
			if effort == "auto" {
				out, _ = sjson.SetBytes(out, thinkingPath+".thinkingBudget", -1)
				out, _ = sjson.SetBytes(out, thinkingPath+".includeThoughts", true)
			} else {
				out, _ = sjson.SetBytes(out, thinkingPath+".thinkingLevel", effort)
				out, _ = sjson.SetBytes(out, thinkingPath+".includeThoughts", effort != "none")
			}
		}
	}

	result := out
	result = common.AttachDefaultSafetySettings(result, "safetySettings")
	return result
}

func geminiContent(role string, parts [][]byte) []byte {
	content := []byte(`{"role":"","parts":[]}`)
	content, _ = sjson.SetBytes(content, "role", role)
	content, _ = sjson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(parts))
	return content
}

func geminiSystemInstruction(parts [][]byte) []byte {
	systemInstruction := []byte(`{"parts":[]}`)
	systemInstruction, _ = sjson.SetRawBytes(systemInstruction, "parts", translatorcommon.JoinRawArray(parts))
	return systemInstruction
}

func shouldStripTrailingOpenAIResponsesModelPrefill(lastContent gjson.Result) bool {
	if lastContent.Get("role").String() != "model" {
		return false
	}
	parts := lastContent.Get("parts")
	if !parts.IsArray() {
		return false
	}
	for _, part := range parts.Array() {
		if part.Get("thought").Bool() {
			return false
		}
	}
	return true
}

func isTrailingOpenAIResponsesAssistantPrefill(items []gjson.Result, assistantIndex int) bool {
	if assistantIndex < 0 || assistantIndex >= len(items) {
		return false
	}
	for j := assistantIndex + 1; j < len(items); j++ {
		itemType := items[j].Get("type").String()
		itemRole := items[j].Get("role").String()
		if itemType == "" && itemRole != "" {
			itemType = "message"
		}
		switch itemType {
		case "reasoning", "function_call", "function_call_output":
			return false
		case "message":
			if strings.EqualFold(itemRole, "system") || strings.EqualFold(itemRole, "developer") {
				continue
			}
			return false
		}
	}
	_, ok := openAIResponsesAssistantVisibleText(items[assistantIndex])
	return ok
}

func openAIResponsesAssistantVisibleText(item gjson.Result) (string, bool) {
	itemType := item.Get("type").String()
	itemRole := item.Get("role").String()
	if itemType == "" && itemRole != "" {
		itemType = "message"
	}
	if itemType != "message" {
		return "", false
	}

	content := item.Get("content")
	if !content.Exists() {
		return "", false
	}
	if content.Type == gjson.String {
		switch strings.ToLower(strings.TrimSpace(itemRole)) {
		case "assistant", "model":
			return content.String(), true
		default:
			return "", false
		}
	}
	if !content.IsArray() {
		return "", false
	}

	var textParts []string
	hasOutputText := false
	content.ForEach(func(_, contentItem gjson.Result) bool {
		contentType := contentItem.Get("type").String()
		if contentType == "" {
			contentType = "input_text"
		}
		if contentType != "output_text" {
			return true
		}
		hasOutputText = true
		textParts = append(textParts, contentItem.Get("text").String())
		return true
	})
	if !hasOutputText {
		return "", false
	}
	// output_text marks model-visible content even when message.role is "user".
	return strings.Join(textParts, "\n"), true
}

func buildOpenAIResponsesReasoningModelContent(thoughtText, visibleText, signature string, useGeminiNativeReasoningLayout bool) []byte {
	modelContent := []byte(`{"role":"model","parts":[]}`)
	if useGeminiNativeReasoningLayout {
		thought := []byte(`{"text":"","thought":true}`)
		thought, _ = sjson.SetBytes(thought, "text", thoughtText)
		modelContent, _ = sjson.SetRawBytes(modelContent, "parts.-1", thought)

		visible := []byte(`{"text":"","thoughtSignature":""}`)
		visible, _ = sjson.SetBytes(visible, "text", visibleText)
		visible, _ = sjson.SetBytes(visible, "thoughtSignature", signature)
		modelContent, _ = sjson.SetRawBytes(modelContent, "parts.-1", visible)
		return modelContent
	}

	thought := []byte(`{"text":"","thoughtSignature":"","thought":true}`)
	thought, _ = sjson.SetBytes(thought, "text", thoughtText)
	thought, _ = sjson.SetBytes(thought, "thoughtSignature", signature)
	modelContent, _ = sjson.SetRawBytes(modelContent, "parts.-1", thought)
	return modelContent
}

func openAIResponsesGeminiThoughtSignature(rawSignature string) string {
	return sigcompat.GeminiReplaySignatureOrBypass(rawSignature, sigcompat.SignatureBlockKindGeminiModelPart)
}

func applyOpenAIResponsesTextFormatToGemini(out []byte, root gjson.Result) []byte {
	textFormat := root.Get("text.format")
	if !textFormat.Exists() {
		return out
	}

	formatType := strings.ToLower(strings.TrimSpace(textFormat.Get("type").String()))
	switch formatType {
	case "json_object":
		out = ensureGeminiGenerationConfig(out)
		out, _ = sjson.SetBytes(out, "generationConfig.responseMimeType", "application/json")
	case "json_schema":
		out = ensureGeminiGenerationConfig(out)
		out, _ = sjson.SetBytes(out, "generationConfig.responseMimeType", "application/json")
		out, _ = sjson.DeleteBytes(out, "generationConfig.responseSchema")

		schema := textFormat.Get("schema")
		if !schema.Exists() {
			schema = textFormat.Get("json_schema.schema")
		}
		if schema.Exists() {
			out, _ = sjson.SetRawBytes(out, "generationConfig.responseJsonSchema", []byte(schema.Raw))
		}
	}

	return out
}

func ensureGeminiGenerationConfig(out []byte) []byte {
	if !gjson.GetBytes(out, "generationConfig").Exists() {
		out, _ = sjson.SetRawBytes(out, "generationConfig", []byte(`{}`))
	}
	return out
}
