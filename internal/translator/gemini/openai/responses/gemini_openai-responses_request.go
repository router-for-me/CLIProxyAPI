// Package responses provides request translation functionality for OpenAI Responses to Gemini API compatibility.
// It converts OpenAI Responses API requests into Gemini compatible JSON using gjson/sjson only.
package responses

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertOpenAIResponsesRequestToGemini converts an OpenAI Responses API request (raw JSON)
// into a complete Gemini request JSON using gjson and sjson.
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI Responses API
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in Gemini API format
func ConvertOpenAIResponsesRequestToGemini(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	useGeminiNativeReasoningLayout := false
	if modelName != "" {
		useGeminiNativeReasoningLayout = true
	}
	// Base envelope (no default thinkingConfig)
	out := []byte(`{"contents":[]}`)

	root := gjson.ParseBytes(rawJSON)

	// Apply thinking configuration: convert OpenAI reasoning_effort to Gemini thinkingConfig.
	re := root.Get("reasoning_effort")
	if re.Exists() {
		effort := strings.ToLower(strings.TrimSpace(re.String()))
		if effort != "" {
			thinkingPath := "generationConfig.thinkingConfig"
			if effort == "auto" {
				out, _ = sjson.SetBytes(out, thinkingPath+".thinkingBudget", -1)
			} else {
				out, _ = sjson.SetBytes(out, thinkingPath+".thinkingLevel", effort)
			}
		}
	}
	out = applyOpenAIResponsesThinkingCompatibilityToGemini(out, rawJSON)

	// Convert input array to Gemini contents format
	input := root.Get("input")
	var systemParts [][]byte
	if input.Exists() && input.IsArray() {
		var contentItems [][]byte
		items := input.Array()

		// Tool response cache for matching function calls with results
		toolResponses := map[string]string{}
		for i := 0; i < len(items); i++ {
			item := items[i]
			itemType := item.Get("type").String()
			if itemType == "function_call_output" {
				callID := item.Get("call_id").String()
				if callID != "" {
					output := item.Get("output").String()
					toolResponses[callID] = output
				}
			}
		}

		for i := 0; i < len(items); i++ {
			item := items[i]
			itemType := item.Get("type").String()
			role := item.Get("role").String()

			// Handle system/developer instructions in input array
			if (role == "system" || role == "developer") && len(items) > 1 {
				content := item.Get("content")
				if content.Type == gjson.String {
					systemParts = append(systemParts, geminiTextPart(content.String()))
				} else if content.IsArray() {
					for _, part := range content.Array() {
						if part.Get("type").String() == "text" {
							systemParts = append(systemParts, geminiTextPart(part.Get("text").String()))
						}
					}
				}
				continue
			}

			switch itemType {
			case "message":
				msgRole := item.Get("role").String()
				// Map OpenAI roles to Gemini roles
				geminiRole := "user"
				if msgRole == "assistant" {
					geminiRole = "model"
				}

				var partItems [][]byte
				content := item.Get("content")
				if content.Type == gjson.String {
					partItems = append(partItems, geminiTextPart(content.String()))
				} else if content.IsArray() {
					for _, part := range content.Array() {
						partType := part.Get("type").String()
						switch partType {
						case "input_text", "text", "output_text":
							text := part.Get("text").String()
							if text != "" {
								partItems = append(partItems, geminiTextPart(text))
							}
						case "input_image":
							// Handle base64 image data
							imageURL := part.Get("image_url").String()
							if imageURL != "" {
								if mimeType, data, ok := translatorcommon.ParseDataURL(imageURL); ok {
									partItems = append(partItems, geminiInlineDataPart(mimeType, data))
								}
							}
						}
					}
				}

				if len(partItems) > 0 {
					contentItems = append(contentItems, geminiContent(geminiRole, partItems))
				}

			case "function_call":
				name := item.Get("name").String()
				arguments := item.Get("arguments").String()

				var partItems [][]byte
				funcCallPart := []byte(`{"functionCall":{"name":"","args":{}}}`)
				funcCallPart, _ = sjson.SetBytes(funcCallPart, "functionCall.name", util.SanitizeFunctionName(name))

				if arguments != "" {
					var argsMap map[string]any
					if errUnmarshal := util.UnmarshalJSONCaseFold([]byte(arguments), &argsMap); errUnmarshal == nil {
						funcCallPart, _ = sjson.SetBytes(funcCallPart, "functionCall.args", argsMap)
					}
				}

				partItems = append(partItems, funcCallPart)
				contentItems = append(contentItems, geminiContent("model", partItems))

			case "function_call_output":
				callID := item.Get("call_id").String()
				output := item.Get("output").String()

				// Find matching function call name if possible, otherwise use generic name
				funcName := "function"
				for j := 0; j < i; j++ {
					prevItem := items[j]
					if prevItem.Get("type").String() == "function_call" && prevItem.Get("call_id").String() == callID {
						funcName = prevItem.Get("name").String()
						break
					}
				}

				var partItems [][]byte
				funcRespPart := []byte(`{"functionResponse":{"name":"","response":{"result":null}}}`)
				funcRespPart, _ = sjson.SetBytes(funcRespPart, "functionResponse.name", util.SanitizeFunctionName(funcName))

				if output != "" {
					parsed := gjson.Parse(output)
					if parsed.Type == gjson.JSON {
						funcRespPart, _ = sjson.SetRawBytes(funcRespPart, "functionResponse.response.result", []byte(parsed.Raw))
					} else {
						funcRespPart, _ = sjson.SetBytes(funcRespPart, "functionResponse.response.result", output)
					}
				}

				partItems = append(partItems, funcRespPart)

				// Look ahead for consecutive function_call_output items and group them
				for i+1 < len(items) && items[i+1].Get("type").String() == "function_call_output" {
					i++
					nextItem := items[i]
					nextCallID := nextItem.Get("call_id").String()
					nextOutput := nextItem.Get("output").String()

					nextFuncName := "function"
					for j := 0; j < i; j++ {
						prevItem := items[j]
						if prevItem.Get("type").String() == "function_call" && prevItem.Get("call_id").String() == nextCallID {
							nextFuncName = prevItem.Get("name").String()
							break
						}
					}

					nextFuncRespPart := []byte(`{"functionResponse":{"name":"","response":{"result":null}}}`)
					nextFuncRespPart, _ = sjson.SetBytes(nextFuncRespPart, "functionResponse.name", util.SanitizeFunctionName(nextFuncName))

					if nextOutput != "" {
						parsed := gjson.Parse(nextOutput)
						if parsed.Type == gjson.JSON {
							nextFuncRespPart, _ = sjson.SetRawBytes(nextFuncRespPart, "functionResponse.response.result", []byte(parsed.Raw))
						} else {
							nextFuncRespPart, _ = sjson.SetBytes(nextFuncRespPart, "functionResponse.response.result", nextOutput)
						}
					}

					partItems = append(partItems, nextFuncRespPart)
				}

				contentItems = append(contentItems, geminiContent("user", partItems))

			case "reasoning":
				// Handle OpenAI Responses reasoning items by converting them to Gemini model turns
				// with thought parts or thought signatures.
				thoughtText := extractOpenAIResponsesReasoningText(item)
				visibleText := ""
				signature := item.Get("encrypted_content").String()

				// Look ahead: if the next item is a message from assistant, merge its text into this turn
				if i+1 < len(items) {
					next := items[i+1]
					if next.Get("type").String() == "message" && next.Get("role").String() == "assistant" {
						visibleText = extractOpenAIResponsesMessageText(next)
						i++ // consume the assistant message
					} else if next.Get("type").String() == "function_call" {
						// Followed by function call - the reasoning belongs to the model turn preceding the call.
						// Capture any consecutive function_call items that follow this reasoning.
						var pendingFunctionCallIDs []string
						if callID := strings.TrimSpace(next.Get("call_id").String()); callID != "" {
							pendingFunctionCallIDs = append(pendingFunctionCallIDs, callID)
						}
						for i+2 < len(items) && items[i+2].Get("type").String() == "function_call" {
							i++
							nextCall := items[i+1]
							if callID := strings.TrimSpace(nextCall.Get("call_id").String()); callID != "" {
								pendingFunctionCallIDs = append(pendingFunctionCallIDs, callID)
							}
						}

						if modelContent := buildOpenAIResponsesReasoningModelContent(thoughtText, visibleText, signature, useGeminiNativeReasoningLayout); len(modelContent) > 0 {
							contentItems = append(contentItems, modelContent)
						}
						if callID := strings.TrimSpace(next.Get("call_id").String()); callID != "" {
							pendingFunctionCallIDs = append(pendingFunctionCallIDs, callID)
						}
						i++
						continue
					}
				}

				if modelContent := buildOpenAIResponsesReasoningModelContent(thoughtText, visibleText, signature, useGeminiNativeReasoningLayout); len(modelContent) > 0 {
					contentItems = append(contentItems, modelContent)
				}
			}
		}
		contentItems = coalesceAdjacentOpenAIResponsesModelContents(contentItems)
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
			if tool.Get("type").String() == "function" || tool.Get("type").String() == "custom" || tool.Get("name").Exists() {
				funcDecl := []byte(`{"name":"","description":"","parametersJsonSchema":{"type":"object","properties":{}}}`)

				name := tool.Get("name").String()
				if name == "" {
					name = tool.Get("function.name").String()
				}
				if name != "" {
					funcDecl, _ = sjson.SetBytes(funcDecl, "name", util.SanitizeFunctionName(name))
				}
				desc := tool.Get("description").String()
				if desc == "" {
					desc = tool.Get("function.description").String()
				}
				if desc != "" {
					funcDecl, _ = sjson.SetBytes(funcDecl, "description", desc)
				}
				for _, k := range []string{"parameters", "parametersJsonSchema", "input_schema", "function.parameters", "function.parametersJsonSchema"} {
					if params := tool.Get(k); params.Exists() && params.Raw != "" && params.Raw != "null" && params.Raw != "{}" {
						funcDecl, _ = sjson.SetRawBytes(funcDecl, "parametersJsonSchema", []byte(util.CleanJSONSchemaForGemini(params.Raw)))
						break
					}
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
		out, _ = sjson.SetBytes(out, "generationConfig.maxOutputTokens", maxOutputTokens.Int())
	}

	// Handle temperature if present
	if temperature := root.Get("temperature"); temperature.Exists() {
		out, _ = sjson.SetBytes(out, "generationConfig.temperature", temperature.Float())
	}

	// Handle top_p if present
	if topP := root.Get("top_p"); topP.Exists() {
		out, _ = sjson.SetBytes(out, "generationConfig.topP", topP.Float())
	}

	// Handle stop sequences
	if stopSequences := root.Get("stop_sequences"); stopSequences.Exists() && stopSequences.IsArray() {
		var sequences []string
		stopSequences.ForEach(func(_, seq gjson.Result) bool {
			sequences = append(sequences, seq.String())
			return true
		})
		if len(sequences) > 0 {
			out, _ = sjson.SetBytes(out, "generationConfig.stopSequences", sequences)
		}
	}

	return out
}

func extractOpenAIResponsesReasoningText(item gjson.Result) string {
	if text := item.Get("reasoning_content.text").String(); text != "" {
		return text
	}
	if text := item.Get("content.0.text").String(); text != "" {
		return text
	}
	if text := item.Get("text").String(); text != "" {
		return text
	}
	return ""
}

func extractOpenAIResponsesMessageText(item gjson.Result) string {
	content := item.Get("content")
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var texts []string
		for _, part := range content.Array() {
			if part.Get("type").String() == "output_text" || part.Get("type").String() == "text" {
				if t := part.Get("text").String(); t != "" {
					texts = append(texts, t)
				}
			}
		}
		return strings.Join(texts, "")
	}
	return ""
}

func buildOpenAIResponsesReasoningModelContent(thoughtText, visibleText, signature string, useGeminiNativeReasoningLayout bool) []byte {
	var parts [][]byte

	if thoughtText != "" {
		part := []byte(`{"text":"","thought":true}`)
		part, _ = sjson.SetBytes(part, "text", thoughtText)
		if signature != "" {
			part, _ = sjson.SetBytes(part, "thoughtSignature", signature)
		}
		parts = append(parts, part)
	} else if signature != "" {
		// Signature-only reasoning
		part := []byte(`{"thought":true}`)
		part, _ = sjson.SetBytes(part, "thoughtSignature", signature)
		parts = append(parts, part)
	}

	if visibleText != "" {
		part := []byte(`{"text":""}`)
		part, _ = sjson.SetBytes(part, "text", visibleText)
		parts = append(parts, part)
	}

	if len(parts) == 0 {
		return nil
	}

	return geminiContent("model", parts)
}

func coalesceAdjacentOpenAIResponsesModelContents(contents [][]byte) [][]byte {
	if len(contents) <= 1 {
		return contents
	}

	var result [][]byte
	for _, contentJSON := range contents {
		if len(result) == 0 {
			result = append(result, contentJSON)
			continue
		}

		lastIdx := len(result) - 1
		lastRole := gjson.GetBytes(result[lastIdx], "role").String()
		currentRole := gjson.GetBytes(contentJSON, "role").String()

		if lastRole == "model" && currentRole == "model" {
			// Merge parts into the previous model content
			lastParts := gjson.GetBytes(result[lastIdx], "parts").Array()
			currentParts := gjson.GetBytes(contentJSON, "parts").Array()

			var mergedParts [][]byte
			for _, p := range lastParts {
				mergedParts = append(mergedParts, []byte(p.Raw))
			}
			for _, p := range currentParts {
				mergedParts = append(mergedParts, []byte(p.Raw))
			}

			mergedContent, _ := sjson.SetRawBytes(result[lastIdx], "parts", translatorcommon.JoinRawArray(mergedParts))
			result[lastIdx] = mergedContent
		} else {
			result = append(result, contentJSON)
		}
	}

	return result
}

func geminiTextPart(text string) []byte {
	part := []byte(`{"text":""}`)
	part, _ = sjson.SetBytes(part, "text", text)
	return part
}

func geminiInlineDataPart(mimeType, data string) []byte {
	part := []byte(`{"inlineData":{"mimeType":"","data":""}}`)
	part, _ = sjson.SetBytes(part, "inlineData.mimeType", mimeType)
	part, _ = sjson.SetBytes(part, "inlineData.data", data)
	return part
}

func geminiContent(role string, parts [][]byte) []byte {
	content := []byte(`{"role":"","parts":[]}`)
	content, _ = sjson.SetBytes(content, "role", role)
	content, _ = sjson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(parts))
	return content
}

func geminiSystemInstruction(parts [][]byte) []byte {
	si := []byte(`{"parts":[]}`)
	si, _ = sjson.SetRawBytes(si, "parts", translatorcommon.JoinRawArray(parts))
	return si
}

func applyOpenAIResponsesThinkingCompatibilityToGemini(out, rawJSON []byte) []byte {
	config := thinking.ExtractSummaryConfig(rawJSON, "openai")
	if config.SummaryMode != "" {
		return thinking.ApplySummaryConfig(out, "gemini", config)
	}
	return out
}
