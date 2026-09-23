package responses

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
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

	// Extract tools and forward map early so request contents and toolDeclarations use the exact same forward map
	functionDeclarations, forwardMap, _ := util.BuildGeminiFunctionDeclarations(root)
	var toolBlocks [][]byte
	if HasResponsesWebSearchTool(root) && ModelSupportsWebSearch(modelName) && AllowsResponsesWebSearchToolChoice(root) {
		googleSearchBlock := []byte(`{"googleSearch":{}}`)
		if allowedDomains := ExtractResponsesWebSearchAllowedDomains(root); len(allowedDomains) > 0 {
			if domainsJSON, errMarshal := json.Marshal(allowedDomains); errMarshal == nil {
				googleSearchBlock, _ = sjson.SetRawBytes(googleSearchBlock, "googleSearch.includedDomains", domainsJSON)
			}
		}
		toolBlocks = append(toolBlocks, googleSearchBlock)
	}
	if len(functionDeclarations) > 0 {
		fnBlock := []byte(`{"functionDeclarations":[]}`)
		fnBlock, _ = sjson.SetRawBytes(fnBlock, "functionDeclarations", translatorcommon.JoinRawArray(functionDeclarations))
		toolBlocks = append(toolBlocks, fnBlock)
	}
	if len(toolBlocks) > 0 {
		out, _ = sjson.SetRawBytes(out, "tools", translatorcommon.JoinRawArray(toolBlocks))
	}

	// Handle tool_choice if present (only configure function calling when function declarations exist)
	if len(functionDeclarations) > 0 {
		if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
			if toolConfig, ok := util.ConvertResponsesToolChoiceToGemini(toolChoice, forwardMap); ok {
				out, _ = sjson.SetRawBytes(out, "toolConfig.functionCallingConfig", toolConfig)
			}
		}
	}

	// Extract system instruction from OpenAI "instructions" field.
	systemParts := make([][]byte, 0, 2)
	if instructions := root.Get("instructions"); instructions.Exists() {
		part := []byte(`{"text":""}`)
		part, _ = sjson.SetBytes(part, "text", instructions.String())
		systemParts = append(systemParts, part)
	}

	// Convert input messages to Gemini contents format
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		inputItems, hasGeminiCarrier := normalizeGeminiResponsesCarriers(restoreGeminiResponsesTextSignatures(modelName, input.Array()))
		if hasGeminiCarrier {
			useGeminiNativeReasoningLayout = true
		}
		inputItems = translatorcommon.NormalizeResponsesToolCallOutputs(inputItems)
		items := pairOpenAIResponsesReasoningWithFunctionCalls(inputItems)
		contentItems := make([][]byte, 0, len(items))
		functionNamesByCallID := make(map[string]string)
		pendingFunctionCallIDs := make([]string, 0)
		for _, item := range items {
			itemType := item.Get("type").String()
			if itemType == "function_call" || itemType == "custom_tool_call" {
				callID := extractOpenAIResponsesCallID(item)
				if _, exists := functionNamesByCallID[callID]; !exists {
					name := item.Get("name").String()
					if ns := item.Get("namespace").String(); ns != "" {
						name = util.QualifyResponsesNamespaceToolName(ns, name)
					}
					functionNamesByCallID[callID] = util.MapResponsesToolName(forwardMap, name)
				}
			}
		}

		normalized := items
		if useGeminiNativeReasoningLayout {
			normalized = reorderOpenAIResponsesDetachedReasoning(normalized)
		}
		consumedFunctionOutputIndexes := make(map[int]bool)
		hasEncounteredConversation := false
		var pendingDeveloperParts [][]byte
		for i := 0; i < len(normalized); i++ {
			if consumedFunctionOutputIndexes[i] {
				continue
			}
			item := normalized[i]
			itemType := item.Get("type").String()
			itemRole := item.Get("role").String()
			if itemType == "" && itemRole != "" {
				itemType = "message"
			} else if isResponsesContentPartType(itemType) && itemRole == "" {
				itemType = "message"
				itemRole = "user"
			}

			switch itemType {
			case "message":
				if strings.EqualFold(itemRole, "system") || strings.EqualFold(itemRole, "developer") {
					if !hasEncounteredConversation {
						pendingFunctionCallIDs = nil
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

					var devParts [][]byte
					if contentArray := item.Get("content"); contentArray.Exists() {
						if contentArray.IsArray() {
							var texts []string
							contentArray.ForEach(func(_, contentItem gjson.Result) bool {
								text := contentItem.Get("text").String()
								if text == "" && contentItem.Type == gjson.String {
									text = contentItem.String()
								}
								if text != "" {
									texts = append(texts, text)
								}
								return true
							})
							if len(texts) > 0 {
								joined := strings.Join(texts, "\n")
								if strings.TrimSpace(joined) != "" {
									part := []byte(`{"text":""}`)
									part, _ = sjson.SetBytes(part, "text", translatorcommon.SystemReminderText(joined))
									devParts = append(devParts, part)
								}
							}
						} else if contentArray.Type == gjson.String && contentArray.String() != "" {
							text := contentArray.String()
							if strings.TrimSpace(text) != "" {
								part := []byte(`{"text":""}`)
								part, _ = sjson.SetBytes(part, "text", translatorcommon.SystemReminderText(text))
								devParts = append(devParts, part)
							}
						}
					}
					if len(devParts) > 0 {
						if len(pendingFunctionCallIDs) > 0 {
							pendingDeveloperParts = append(pendingDeveloperParts, devParts...)
						} else {
							contentItems = append(contentItems, geminiContent("user", devParts))
						}
					}
					continue
				}

				hasEncounteredConversation = true
				if _, isAssistantOutput := openAIResponsesAssistantVisibleText(item); !isAssistantOutput {
					if len(pendingFunctionCallIDs) > 0 {
						anyHasFutureOutput := false
						for _, callID := range pendingFunctionCallIDs {
							if responsesHasMatchingOutput(normalized[i:], callID) {
								anyHasFutureOutput = true
								break
							}
						}
						if !anyHasFutureOutput {
							var synthesizedParts [][]byte
							for _, callID := range pendingFunctionCallIDs {
								synthesizedParts = append(synthesizedParts, buildOpenAIResponsesSynthesizedFunctionResponsePart(callID, functionNamesByCallID))
							}
							if len(synthesizedParts) > 0 {
								contentItems = append(contentItems, geminiContent("user", synthesizedParts))
							}
							pendingFunctionCallIDs = nil
						}
					}
					if len(pendingDeveloperParts) > 0 {
						contentItems = append(contentItems, geminiContent("user", pendingDeveloperParts))
						pendingDeveloperParts = nil
					}
				}

				// Handle regular messages
				// Note: In Responses format, model outputs may appear as content items with type "output_text"
				// even when the message.role is "user". We split such items into distinct Gemini messages
				// with roles derived from the content type to match docs/convert-2.md.
				contentArray := item.Get("content")
				var partsToProcess []gjson.Result
				if contentArray.Exists() && contentArray.IsArray() {
					partsToProcess = contentArray.Array()
				} else if isResponsesContentPartType(item.Get("type").String()) {
					partsToProcess = append(partsToProcess, item)
					for i+1 < len(normalized) {
						nextItem := normalized[i+1]
						nextType := nextItem.Get("type").String()
						nextRole := nextItem.Get("role").String()
						if nextRole == "" && isResponsesContentPartType(nextType) {
							partsToProcess = append(partsToProcess, nextItem)
							i++
						} else {
							break
						}
					}
				}

				if len(partsToProcess) > 0 {
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

					for _, contentItem := range partsToProcess {
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
						case "input_text", "output_text", "text":
							if text := contentItem.Get("text"); text.Exists() {
								partJSON = []byte(`{"text":""}`)
								partJSON, _ = sjson.SetBytes(partJSON, "text", text.String())
							}
						default:
							if part, ok := openAIResponsesPartFromBlock(contentItem); ok {
								partJSON = part
							}
						}

						if len(partJSON) > 0 {
							currentParts = append(currentParts, partJSON)
						}
					}

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

			case "function_call", "custom_tool_call":
				hasEncounteredConversation = true
				signature := geminiResponsesThoughtSignature
				if rawSignature := strings.TrimSpace(item.Get("_cpa_reasoning_signature").String()); rawSignature != "" {
					signature = openAIResponsesGeminiThoughtSignature(rawSignature)
				}
				if thoughtText := item.Get("_cpa_reasoning_summary").String(); thoughtText != "" {
					contentItems = append(contentItems, buildOpenAIResponsesReasoningFunctionCallModelContent(thoughtText, item, signature, forwardMap))
				} else if !useGeminiNativeReasoningLayout && strings.TrimSpace(item.Get("_cpa_reasoning_signature").String()) != "" {
					contentItems = append(contentItems, buildOpenAIResponsesEmptyReasoningFunctionCallModelContent(item, signature, forwardMap))
				} else {
					contentItems = append(contentItems, buildOpenAIResponsesFunctionCallModelContent(item, signature, forwardMap))
				}
				if callID := extractOpenAIResponsesCallID(item); callID != "" {
					pendingFunctionCallIDs = append(pendingFunctionCallIDs, callID)
				}

			case "function_call_output", "custom_tool_call_output":
				hasEncounteredConversation = true
				orderedOutputs, consumedIndexes, _ := collectOpenAIResponsesFunctionCallOutputs(normalized, i, pendingFunctionCallIDs)
				for consumedIndex := range consumedIndexes {
					consumedFunctionOutputIndexes[consumedIndex] = true
				}
				end := i + len(consumedIndexes)
				hasSubsequent := responsesHasSubsequentTurn(normalized[end:])

				outputByCallID := make(map[string]gjson.Result)
				var extraOutputs []gjson.Result
				anyMatched := false
				for _, out := range orderedOutputs {
					id := extractOpenAIResponsesCallID(out)
					if id != "" {
						outputByCallID[id] = out
					} else {
						extraOutputs = append(extraOutputs, out)
					}
				}
				for _, pendingID := range pendingFunctionCallIDs {
					if _, ok := outputByCallID[pendingID]; ok {
						anyMatched = true
						break
					}
				}

				responseParts := make([][]byte, 0, len(pendingFunctionCallIDs)+len(extraOutputs))
				stillPending := make([]string, 0, len(pendingFunctionCallIDs))

				for _, pendingID := range pendingFunctionCallIDs {
					if out, ok := outputByCallID[pendingID]; ok {
						responseParts = append(responseParts, buildOpenAIResponsesFunctionResponseParts(out, functionNamesByCallID)...)
						delete(outputByCallID, pendingID)
					} else if (hasSubsequent || anyMatched) && !responsesHasMatchingOutput(normalized[end:], pendingID) {
						responseParts = append(responseParts, buildOpenAIResponsesSynthesizedFunctionResponsePart(pendingID, functionNamesByCallID))
					} else {
						stillPending = append(stillPending, pendingID)
					}
				}

				var standaloneOutputContents [][]byte
				appendStandaloneOutput := func(out gjson.Result) {
					// Orphan outputs (no matching function_call, e.g. Codex
					// send_message_to_thread cards) must not become unpaired
					// functionResponse parts. Surface them as user text instead.
					if parts := buildOpenAIResponsesStandaloneToolOutputTextParts(out); len(parts) > 0 {
						standaloneOutputContents = append(standaloneOutputContents, geminiContent("user", parts))
					}
				}
				for _, out := range orderedOutputs {
					id := extractOpenAIResponsesCallID(out)
					if _, remaining := outputByCallID[id]; remaining {
						appendStandaloneOutput(out)
						delete(outputByCallID, id)
					}
				}
				for _, out := range extraOutputs {
					appendStandaloneOutput(out)
				}

				pendingFunctionCallIDs = stillPending
				if len(responseParts) > 0 {
					contentItems = append(contentItems, geminiContent("user", responseParts))
				}
				contentItems = append(contentItems, standaloneOutputContents...)
				if len(pendingFunctionCallIDs) == 0 && len(pendingDeveloperParts) > 0 {
					contentItems = append(contentItems, geminiContent("user", pendingDeveloperParts))
					pendingDeveloperParts = nil
				}

			case "reasoning":
				hasEncounteredConversation = true
				thoughtText := item.Get("summary.0.text").String()
				rawSignature := item.Get("encrypted_content").String()
				carrierDirection := geminiResponsesCarrierDirection(item)
				carrierTarget := geminiResponsesCarrierTarget(item)
				if strings.TrimSpace(rawSignature) == "" && i+1 < len(normalized) {
					nextReasoning := normalized[i+1]
					if nextReasoning.Get("type").String() == "reasoning" && strings.Contains(nextReasoning.Get("id").String(), "_detached_after_") && strings.TrimSpace(nextReasoning.Get("summary.0.text").String()) == "" && strings.TrimSpace(nextReasoning.Get("encrypted_content").String()) != "" {
						rawSignature = nextReasoning.Get("encrypted_content").String()
						i++
					}
				}
				signature := openAIResponsesGeminiThoughtSignature(rawSignature)

				visibleText := ""
				if useGeminiNativeReasoningLayout && i+1 < len(normalized) {
					next := normalized[i+1]
					canBindText := (carrierDirection == "" || carrierDirection == geminiResponsesCarrierNext) && (carrierTarget == "" || carrierTarget == geminiResponsesCarrierText || carrierTarget == geminiResponsesCarrierAny)
					canBindFunction := (carrierDirection == "" || carrierDirection == geminiResponsesCarrierNext) && (carrierTarget == "" || carrierTarget == geminiResponsesCarrierFunction || carrierTarget == geminiResponsesCarrierAny)
					if visible, ok := openAIResponsesAssistantVisibleText(next); ok && canBindText {
						visibleText = visible
						i++
					} else if (next.Get("type").String() == "function_call" || next.Get("type").String() == "custom_tool_call") && canBindFunction && strings.TrimSpace(next.Get("_cpa_reasoning_signature").String()) == "" && signature != geminiResponsesThoughtSignature {
						contentItems = append(contentItems, buildOpenAIResponsesReasoningFunctionCallModelContent(thoughtText, next, signature, forwardMap))
						if callID := extractOpenAIResponsesCallID(next); callID != "" {
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
		if len(pendingDeveloperParts) > 0 {
			contentItems = append(contentItems, geminiContent("user", pendingDeveloperParts))
			pendingDeveloperParts = nil
		}
		contentItems = coalesceAdjacentOpenAIResponsesModelContents(contentItems)
		contentItems = translatorcommon.MergeAdjacentGeminiUserContents(contentItems)
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

	// Handle generation config from OpenAI format
	if maxOutputTokens := root.Get("max_output_tokens"); maxOutputTokens.Exists() {
		genConfig := []byte(`{"maxOutputTokens":0}`)
		genConfig, _ = sjson.SetBytes(genConfig, "maxOutputTokens", maxOutputTokens.Int())
		out, _ = sjson.SetRawBytes(out, "generationConfig", genConfig)
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
			} else {
				out, _ = sjson.SetBytes(out, thinkingPath+".thinkingLevel", effort)
			}
		}
	}

	result := out
	result = common.AttachDefaultSafetySettings(result, "safetySettings")
	if useGeminiNativeReasoningLayout {
		result = sigcompat.SanitizeGeminiRequestThoughtSignatures(result, "contents")
	}
	return stripTrailingOpenAIResponsesModelPrefill(result)
}

func geminiContent(role string, parts [][]byte) []byte {
	content := []byte(`{"role":"","parts":[]}`)
	content, _ = sjson.SetBytes(content, "role", role)
	content, _ = sjson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(parts))
	return content
}

func coalesceAdjacentOpenAIResponsesModelContents(contents [][]byte) [][]byte {
	coalesced := make([][]byte, 0, len(contents))
	for _, content := range contents {
		contentResult := gjson.ParseBytes(content)
		if !strings.EqualFold(strings.TrimSpace(contentResult.Get("role").String()), "model") || len(coalesced) == 0 {
			coalesced = append(coalesced, content)
			continue
		}
		lastIndex := len(coalesced) - 1
		lastResult := gjson.ParseBytes(coalesced[lastIndex])
		if !strings.EqualFold(strings.TrimSpace(lastResult.Get("role").String()), "model") {
			coalesced = append(coalesced, content)
			continue
		}
		merged := coalesced[lastIndex]
		parts := contentResult.Get("parts")
		if !parts.IsArray() {
			coalesced = append(coalesced, content)
			continue
		}
		var extraParts [][]byte
		parts.ForEach(func(_, part gjson.Result) bool {
			extraParts = append(extraParts, []byte(part.Raw))
			return true
		})
		if len(extraParts) > 0 {
			var existingParts [][]byte
			gjson.GetBytes(merged, "parts").ForEach(func(_, p gjson.Result) bool {
				existingParts = append(existingParts, []byte(p.Raw))
				return true
			})
			merged = translatorcommon.SetRawArrayItems(merged, "parts", append(existingParts, extraParts...))
		}
		coalesced[lastIndex] = merged
	}
	return coalesced
}

func geminiSystemInstruction(parts [][]byte) []byte {
	systemInstruction := []byte(`{"parts":[]}`)
	systemInstruction, _ = sjson.SetRawBytes(systemInstruction, "parts", translatorcommon.JoinRawArray(parts))
	return systemInstruction
}

func stripTrailingOpenAIResponsesModelPrefill(payload []byte) []byte {
	contents := gjson.GetBytes(payload, "contents")
	if !contents.IsArray() {
		return payload
	}
	contentArray := contents.Array()
	if len(contentArray) == 0 || !shouldStripTrailingOpenAIResponsesModelPrefill(contentArray[len(contentArray)-1]) {
		return payload
	}
	items := make([][]byte, 0, len(contentArray)-1)
	for _, content := range contentArray[:len(contentArray)-1] {
		items = append(items, []byte(content.Raw))
	}
	if len(items) == 0 {
		updated, errSet := sjson.SetRawBytes(payload, "contents", []byte("[]"))
		if errSet == nil {
			return updated
		}
		return payload
	}
	return translatorcommon.SetRawArrayItems(payload, "contents", items)
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
		if part.Get("thought").Bool() || part.Get("functionCall").Exists() || strings.TrimSpace(part.Get("thoughtSignature").String()) != "" {
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
		case "reasoning", "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output":
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

func isOpenAIResponsesToolCall(item gjson.Result) bool {
	t := item.Get("type").String()
	return t == "function_call" || t == "custom_tool_call"
}

func isOpenAIResponsesToolOutput(item gjson.Result) bool {
	t := item.Get("type").String()
	return t == "function_call_output" || t == "custom_tool_call_output"
}

func pairOpenAIResponsesReasoningWithFunctionCalls(items []gjson.Result) []gjson.Result {
	isDetachedCarrier := isOpenAIResponsesDetachedCarrier
	postCallSignature := make(map[int]string)
	postCallCarrier := make(map[int]bool)
	consumedPostCallCarrier := make(map[int]bool)
	for groupStart := 0; groupStart < len(items); {
		if !isOpenAIResponsesToolCall(items[groupStart]) && !isDetachedCarrier(items[groupStart]) {
			groupStart++
			continue
		}
		groupEnd := groupStart
		hasFunctionCall := false
		for groupEnd < len(items) && (isOpenAIResponsesToolCall(items[groupEnd]) || isDetachedCarrier(items[groupEnd])) {
			hasFunctionCall = hasFunctionCall || isOpenAIResponsesToolCall(items[groupEnd])
			groupEnd++
		}
		if !hasFunctionCall || groupEnd >= len(items) || !isOpenAIResponsesToolOutput(items[groupEnd]) {
			groupStart = groupEnd
			continue
		}
		outputEnd := groupEnd
		for outputEnd < len(items) && isOpenAIResponsesToolOutput(items[outputEnd]) {
			outputEnd++
		}
		// A run beginning with a carrier uses leading-carrier semantics. A run
		// beginning with a call uses post-call semantics. This preserves both
		// carrier,call,carrier,call and call,carrier,call,carrier histories.
		if isOpenAIResponsesToolCall(items[groupStart]) {
			for callIndex := groupStart; callIndex < groupEnd; callIndex++ {
				item := items[callIndex]
				if !isOpenAIResponsesToolCall(item) || strings.TrimSpace(item.Get("_cpa_reasoning_signature").String()) != "" || callIndex+1 >= groupEnd || !isDetachedCarrier(items[callIndex+1]) {
					continue
				}
				carrierDirection := geminiResponsesCarrierDirection(items[callIndex+1])
				carrierTarget := geminiResponsesCarrierTarget(items[callIndex+1])
				if carrierDirection != "" && (carrierDirection != geminiResponsesCarrierPrevious || (carrierTarget != geminiResponsesCarrierFunction && carrierTarget != geminiResponsesCarrierAny)) {
					continue
				}
				carrierEnd := callIndex + 1
				for carrierEnd < groupEnd && isDetachedCarrier(items[carrierEnd]) {
					postCallCarrier[carrierEnd] = true
					carrierEnd++
				}
				callID := extractOpenAIResponsesCallID(item)
				if callID == "" {
					continue
				}
				for outputIndex := groupEnd; outputIndex < outputEnd; outputIndex++ {
					if extractOpenAIResponsesCallID(items[outputIndex]) == callID {
						postCallSignature[callIndex] = strings.TrimSpace(items[callIndex+1].Get("encrypted_content").String())
						consumedPostCallCarrier[callIndex+1] = true
						break
					}
				}
			}
		}
		groupStart = outputEnd
	}

	paired := make([]gjson.Result, 0, len(items))
	for index := 0; index < len(items); index++ {
		item := items[index]
		if signature := postCallSignature[index]; signature != "" {
			functionCall := []byte(item.Raw)
			functionCall, _ = sjson.SetBytes(functionCall, "_cpa_reasoning_signature", signature)
			paired = append(paired, gjson.ParseBytes(functionCall))
			continue
		}
		if consumedPostCallCarrier[index] {
			continue
		}
		carrierDirection := geminiResponsesCarrierDirection(item)
		carrierTarget := geminiResponsesCarrierTarget(item)
		canBindFollowingCall := carrierDirection == "" || (carrierDirection == geminiResponsesCarrierNext && (carrierTarget == geminiResponsesCarrierFunction || carrierTarget == geminiResponsesCarrierAny))
		if item.Get("type").String() == "reasoning" && !postCallCarrier[index] && canBindFollowingCall && !strings.Contains(item.Get("id").String(), "_detached_after_") && index+1 < len(items) && isOpenAIResponsesToolCall(items[index+1]) {
			rawSignature := strings.TrimSpace(item.Get("encrypted_content").String())
			if rawSignature != "" {
				functionCall := []byte(items[index+1].Raw)
				functionCall, _ = sjson.SetBytes(functionCall, "_cpa_reasoning_signature", rawSignature)
				if summary := item.Get("summary.0.text").String(); summary != "" {
					functionCall, _ = sjson.SetBytes(functionCall, "_cpa_reasoning_summary", summary)
				}
				paired = append(paired, gjson.ParseBytes(functionCall))
				index++
				continue
			}
		}
		paired = append(paired, item)
	}
	return paired
}

func reorderOpenAIResponsesDetachedReasoning(items []gjson.Result) []gjson.Result {
	reordered := make([]gjson.Result, 0, len(items))
	for itemIndex, item := range items {
		isReasoningCarrier := isOpenAIResponsesDetachedCarrier(item)
		markedDetached := strings.Contains(item.Get("id").String(), "_detached_after_")
		if isReasoningCarrier && len(reordered) > 0 {
			previous := reordered[len(reordered)-1]
			previousType := previous.Get("type").String()
			if previousType == "" && previous.Get("role").String() != "" {
				previousType = "message"
			}
			isAssistantMessage := false
			if previousType == "message" {
				_, isAssistantMessage = openAIResponsesAssistantVisibleText(previous)
			}

			direction := geminiResponsesCarrierDirection(item)
			targetKind := geminiResponsesCarrierTarget(item)
			if direction != "" {
				alreadyPairedText := false
				alreadyPairedFunction := false
				if len(reordered) > 1 {
					prior := reordered[len(reordered)-2]
					priorDirection := geminiResponsesCarrierDirection(prior)
					priorTarget := geminiResponsesCarrierTarget(prior)
					priorBindsFollowing := isOpenAIResponsesDetachedCarrier(prior) && (priorDirection == geminiResponsesCarrierNext || priorDirection == geminiResponsesCarrierPrevious)
					alreadyPairedText = priorBindsFollowing && (priorTarget == geminiResponsesCarrierText || priorTarget == geminiResponsesCarrierAny)
					alreadyPairedFunction = priorBindsFollowing && (priorTarget == geminiResponsesCarrierFunction || priorTarget == geminiResponsesCarrierAny)
				}
				bindPreviousMessage := direction == geminiResponsesCarrierPrevious && (targetKind == geminiResponsesCarrierText || targetKind == geminiResponsesCarrierAny) && isAssistantMessage && !alreadyPairedText
				bindPreviousFunction := direction == geminiResponsesCarrierPrevious && (targetKind == geminiResponsesCarrierFunction || targetKind == geminiResponsesCarrierAny) && (previousType == "function_call" || previousType == "custom_tool_call") && strings.TrimSpace(previous.Get("_cpa_reasoning_signature").String()) == "" && !alreadyPairedFunction
				if bindPreviousMessage || bindPreviousFunction {
					movedItemJSON, _ := sjson.SetBytes([]byte(item.Raw), geminiResponsesCarrierDirectionField, geminiResponsesCarrierNext)
					reordered[len(reordered)-1] = gjson.ParseBytes(movedItemJSON)
					reordered = append(reordered, previous)
					continue
				}
				reordered = append(reordered, item)
				continue
			}

			if isAssistantMessage && !markedDetached && itemIndex+1 < len(items) {
				_, nextIsAssistantMessage := openAIResponsesAssistantVisibleText(items[itemIndex+1])
				isAssistantMessage = !nextIsAssistantMessage
			}
			alreadyPaired := false
			if len(reordered) > 1 {
				prior := reordered[len(reordered)-2]
				alreadyPaired = isOpenAIResponsesDetachedCarrier(prior) && strings.Contains(prior.Get("id").String(), "_detached_after_")
			}
			if !alreadyPaired && (isAssistantMessage || (markedDetached && (previousType == "function_call" || previousType == "custom_tool_call") && strings.TrimSpace(previous.Get("_cpa_reasoning_signature").String()) == "")) {
				reordered[len(reordered)-1] = item
				reordered = append(reordered, previous)
				continue
			}
		}
		reordered = append(reordered, item)
	}
	return reordered
}

func buildOpenAIResponsesFunctionCallPart(item gjson.Result, signature string, forwardMap map[string]string) []byte {
	name := item.Get("name").String()
	if ns := item.Get("namespace").String(); ns != "" {
		name = util.QualifyResponsesNamespaceToolName(ns, name)
	}
	name = util.MapResponsesToolName(forwardMap, name)
	functionCall := []byte(`{"functionCall":{"name":"","args":{}}}`)
	functionCall, _ = sjson.SetBytes(functionCall, "functionCall.name", name)
	functionCall, _ = sjson.SetBytes(functionCall, "thoughtSignature", signature)
	functionCall, _ = sjson.SetBytes(functionCall, "functionCall.id", extractOpenAIResponsesCallID(item))

	if item.Get("type").String() == "custom_tool_call" {
		inputVal := item.Get("input")
		if inputVal.Exists() {
			if inputVal.Type == gjson.String {
				functionCall, _ = sjson.SetBytes(functionCall, "functionCall.args.input", inputVal.String())
			} else {
				functionCall, _ = sjson.SetRawBytes(functionCall, "functionCall.args.input", []byte(inputVal.Raw))
			}
		} else {
			functionCall, _ = sjson.SetBytes(functionCall, "functionCall.args.input", "")
		}
	} else {
		arguments := item.Get("arguments").String()
		if arguments != "" {
			argsResult := gjson.Parse(arguments)
			if argsResult.IsObject() || argsResult.IsArray() {
				functionCall, _ = sjson.SetRawBytes(functionCall, "functionCall.args", []byte(argsResult.Raw))
			} else {
				functionCall, _ = sjson.SetBytes(functionCall, "functionCall.args.arguments", arguments)
			}
		}
	}
	return functionCall
}

func geminiResponsesInlineDataPart(mimeType, data string) []byte {
	partJSON := []byte(`{"inline_data":{"mime_type":"","data":""}}`)
	partJSON, _ = sjson.SetBytes(partJSON, "inline_data.mime_type", mimeType)
	partJSON, _ = sjson.SetBytes(partJSON, "inline_data.data", data)
	return partJSON
}

func geminiResponsesFileDataPart(mimeType, fileURI string) []byte {
	partJSON := []byte(`{"file_data":{"mime_type":"","file_uri":""}}`)
	partJSON, _ = sjson.SetBytes(partJSON, "file_data.mime_type", mimeType)
	partJSON, _ = sjson.SetBytes(partJSON, "file_data.file_uri", fileURI)
	return partJSON
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func isDataURL(raw string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(trimmed, "data:")
}

func isRemoteURL(u string) bool {
	lower := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "gs://")
}

func isGenericMIME(mimeType string) bool {
	m := strings.ToLower(strings.TrimSpace(mimeType))
	return m == "" || m == "application/octet-stream" || m == "binary/octet-stream"
}

func firstNonGenericFormat(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" && !isGenericMIME(v) {
			return v
		}
	}
	return ""
}

func parseOpenAIResponsesDataURL(rawURL string) (string, string) {
	trimmedRaw := strings.TrimSpace(rawURL)
	if len(trimmedRaw) < 5 || !strings.EqualFold(trimmedRaw[:5], "data:") {
		return "", ""
	}
	trimmed := trimmedRaw[5:]
	metadata, payload, found := strings.Cut(trimmed, ",")
	if !found || strings.TrimSpace(payload) == "" {
		return "", ""
	}
	payload = strings.TrimSpace(payload)
	fields := strings.Split(metadata, ";")
	mimeType := strings.TrimSpace(fields[0])
	isBase64 := false
	for _, field := range fields[1:] {
		if strings.EqualFold(strings.TrimSpace(field), "base64") {
			isBase64 = true
			break
		}
	}
	if !isBase64 {
		return "", ""
	}
	if _, errDecode := base64.StdEncoding.DecodeString(payload); errDecode != nil {
		if _, errRaw := base64.RawStdEncoding.DecodeString(payload); errRaw != nil {
			return "", ""
		}
	}
	return mimeType, payload
}

func isResponsesContentPartType(itemType string) bool {
	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case "input_text", "output_text", "text",
		"input_image", "image_url", "image",
		"input_audio", "audio",
		"input_video", "video_url", "video",
		"input_file", "file":
		return true
	default:
		return false
	}
}

func openAIResponsesAudioMimeType(audioFormat string) string {
	audioFormat = strings.TrimSpace(audioFormat)
	if isGenericMIME(audioFormat) {
		return "audio/wav"
	}
	if strings.Contains(audioFormat, "/") {
		return audioFormat
	}
	fLower := strings.ToLower(audioFormat)
	switch fLower {
	case "wav":
		return "audio/wav"
	case "mp3", "mpeg":
		return "audio/mpeg"
	case "ogg":
		return "audio/ogg"
	case "flac":
		return "audio/flac"
	case "aac":
		return "audio/aac"
	case "webm":
		return "audio/webm"
	case "pcm16", "pcm":
		return "audio/pcm"
	case "g711_ulaw", "g711_alaw":
		return "audio/basic"
	case "opus":
		return "audio/opus"
	case "m4a":
		return "audio/mp4"
	case "wma":
		return "audio/x-ms-wma"
	default:
		if mapped := misc.MimeTypes[fLower]; mapped != "" && strings.HasPrefix(mapped, "audio/") {
			return mapped
		}
		return "audio/wav"
	}
}

func openAIResponsesVideoMimeType(format string) string {
	format = strings.TrimSpace(format)
	if isGenericMIME(format) {
		return "video/mp4"
	}
	if strings.Contains(format, "/") {
		return format
	}
	fLower := strings.ToLower(format)
	switch fLower {
	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mov", "quicktime":
		return "video/quicktime"
	case "avi", "x-msvideo":
		return "video/x-msvideo"
	case "mpeg":
		return "video/mpeg"
	case "ogg":
		return "video/ogg"
	case "mkv", "x-matroska":
		return "video/x-matroska"
	case "flv", "x-flv":
		return "video/x-flv"
	case "3gpp":
		return "video/3gpp"
	default:
		if mapped := misc.MimeTypes[fLower]; mapped != "" && strings.HasPrefix(mapped, "video/") {
			return mapped
		}
		return "video/mp4"
	}
}

func openAIResponsesAudioFromBlock(block gjson.Result) (mimeType string, data string, ok bool) {
	bType := strings.ToLower(strings.TrimSpace(block.Get("type").String()))
	if bType != "input_audio" && bType != "audio" {
		return "", "", false
	}

	filename := firstNonEmpty(block.Get("filename").String(), block.Get("file.filename").String())
	audioObj := block.Get("input_audio")
	if !audioObj.Exists() {
		audioObj = block.Get("audio")
	}
	audioFormat := firstNonGenericFormat(
		audioObj.Get("format").String(),
		audioObj.Get("mime_type").String(),
		block.Get("format").String(),
		block.Get("mime_type").String(),
	)

	// 1. Nested input_audio object (standard OpenAI Responses schema)
	audioData := audioObj.Get("data").String()

	// 2. Flat data
	if audioData == "" {
		audioData = block.Get("data").String()
	}

	// 3. audio_url / url
	if audioData == "" {
		audioURL := firstNonEmpty(
			block.Get("audio_url.url").String(),
			block.Get("audio_url").String(),
			block.Get("url").String(),
		)
		if audioURL != "" {
			if isDataURL(audioURL) {
				mType, d := parseOpenAIResponsesDataURL(audioURL)
				if d != "" {
					if isGenericMIME(mType) {
						if audioFormat != "" && !isGenericMIME(audioFormat) {
							mType = openAIResponsesAudioMimeType(audioFormat)
						} else if filename != "" {
							mType = openAIResponsesAudioMimeType(strings.TrimPrefix(filepath.Ext(filename), "."))
						} else {
							mType = "audio/wav"
						}
					}
					return mType, d, true
				}
				return "", "", false
			} else if !isRemoteURL(audioURL) {
				mType := openAIResponsesAudioMimeType(audioFormat)
				if isGenericMIME(audioFormat) && filename != "" {
					mType = openAIResponsesAudioMimeType(strings.TrimPrefix(filepath.Ext(filename), "."))
				}
				return mType, audioURL, true
			}
		}
	}

	// 4. Source object (base64)
	if audioData == "" && block.Get("source.type").String() == "base64" {
		audioData = block.Get("source.data").String()
		if audioFormat == "" {
			audioFormat = block.Get("source.media_type").String()
		}
	}

	if audioData == "" {
		return "", "", false
	}

	if isDataURL(audioData) {
		mType, d := parseOpenAIResponsesDataURL(audioData)
		if d != "" {
			if isGenericMIME(mType) {
				if audioFormat != "" && !isGenericMIME(audioFormat) {
					mType = openAIResponsesAudioMimeType(audioFormat)
				} else if filename != "" {
					mType = openAIResponsesAudioMimeType(strings.TrimPrefix(filepath.Ext(filename), "."))
				} else {
					mType = "audio/wav"
				}
			}
			return mType, d, true
		}
		return "", "", false
	}

	mType := openAIResponsesAudioMimeType(audioFormat)
	if isGenericMIME(audioFormat) && filename != "" {
		mType = openAIResponsesAudioMimeType(strings.TrimPrefix(filepath.Ext(filename), "."))
	}
	return mType, audioData, true
}

func openAIResponsesVideoFromBlock(block gjson.Result) (mimeType string, data string, ok bool) {
	bType := strings.ToLower(strings.TrimSpace(block.Get("type").String()))
	if bType != "input_video" && bType != "video_url" && bType != "video" {
		return "", "", false
	}

	filename := firstNonEmpty(block.Get("filename").String(), block.Get("file.filename").String())
	videoObj := block.Get("input_video")
	if !videoObj.Exists() {
		videoObj = block.Get("video")
	}
	format := firstNonGenericFormat(
		videoObj.Get("format").String(),
		videoObj.Get("mime_type").String(),
		block.Get("format").String(),
		block.Get("mime_type").String(),
	)

	// 1. video_url (string or { "url": "..." }) or url
	videoURL := firstNonEmpty(
		block.Get("video_url.url").String(),
		block.Get("video_url").String(),
		block.Get("url").String(),
	)
	if videoURL != "" {
		if isDataURL(videoURL) {
			mType, d := parseOpenAIResponsesDataURL(videoURL)
			if d != "" {
				if isGenericMIME(mType) {
					if format != "" && !isGenericMIME(format) {
						mType = openAIResponsesVideoMimeType(format)
					} else if filename != "" {
						mType = openAIResponsesVideoMimeType(strings.TrimPrefix(filepath.Ext(filename), "."))
					} else {
						mType = "video/mp4"
					}
				}
				return mType, d, true
			}
			return "", "", false
		} else if !isRemoteURL(videoURL) {
			mType := openAIResponsesVideoMimeType(format)
			if isGenericMIME(format) && filename != "" {
				mType = openAIResponsesVideoMimeType(strings.TrimPrefix(filepath.Ext(filename), "."))
			}
			return mType, videoURL, true
		}
	}

	// 2. Nested input_video or video object data
	videoData := videoObj.Get("data").String()

	// 3. Flat data
	if videoData == "" {
		videoData = block.Get("data").String()
	}

	// 4. Source object (base64)
	if videoData == "" && block.Get("source.type").String() == "base64" {
		videoData = block.Get("source.data").String()
		if format == "" {
			format = block.Get("source.media_type").String()
		}
	}

	if videoData == "" {
		return "", "", false
	}

	if isDataURL(videoData) {
		mType, d := parseOpenAIResponsesDataURL(videoData)
		if d != "" {
			if isGenericMIME(mType) {
				if format != "" && !isGenericMIME(format) {
					mType = openAIResponsesVideoMimeType(format)
				} else if filename != "" {
					mType = openAIResponsesVideoMimeType(strings.TrimPrefix(filepath.Ext(filename), "."))
				} else {
					mType = "video/mp4"
				}
			}
			return mType, d, true
		}
		return "", "", false
	}

	mType := openAIResponsesVideoMimeType(format)
	if isGenericMIME(format) && filename != "" {
		mType = openAIResponsesVideoMimeType(strings.TrimPrefix(filepath.Ext(filename), "."))
	}
	return mType, videoData, true
}

func normalizeFormatToMIME(format string) string {
	format = strings.TrimSpace(format)
	if format == "" || isGenericMIME(format) {
		return ""
	}
	if strings.Contains(format, "/") {
		return format
	}
	fLower := strings.ToLower(format)
	if fLower == "jpg" || fLower == "jpeg" {
		return "image/jpeg"
	}
	if fLower == "wav" {
		return "audio/wav"
	}
	if fLower == "mp3" {
		return "audio/mpeg"
	}
	if fLower == "mp4" {
		return "video/mp4"
	}
	if fLower == "webm" {
		return "video/webm"
	}
	if fLower == "pdf" {
		return "application/pdf"
	}
	if mapped := misc.MimeTypes[fLower]; mapped != "" {
		return mapped
	}
	return ""
}

func openAIResponsesFileFromBlock(block gjson.Result) (mimeType string, data string, ok bool) {
	bType := strings.ToLower(strings.TrimSpace(block.Get("type").String()))
	if bType != "input_file" && bType != "file" {
		return "", "", false
	}

	filename := firstNonEmpty(block.Get("filename").String(), block.Get("file.filename").String())
	fileData := firstNonEmpty(
		block.Get("file_data").String(),
		block.Get("file.file_data").String(),
		block.Get("data").String(),
	)
	if fileData == "" {
		fileURL := firstNonEmpty(
			block.Get("file_url.url").String(),
			block.Get("file_url").String(),
			block.Get("file.file_url").String(),
			block.Get("url").String(),
		)
		if isDataURL(fileURL) {
			fileData = fileURL
		}
	}

	fileObj := block.Get("file")
	fallbackMIME := firstNonGenericFormat(
		block.Get("mime_type").String(),
		fileObj.Get("mime_type").String(),
		block.Get("format").String(),
		fileObj.Get("format").String(),
	)
	if fallbackMIME != "" {
		fallbackMIME = normalizeFormatToMIME(fallbackMIME)
	}
	if isGenericMIME(fallbackMIME) && filename != "" {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
		if ext != "" {
			fallbackMIME = normalizeFormatToMIME(ext)
		}
	}

	if isDataURL(fileData) {
		mType, d := parseOpenAIResponsesDataURL(fileData)
		if d != "" {
			if isGenericMIME(mType) && fallbackMIME != "" {
				mType = fallbackMIME
			}
			if isGenericMIME(mType) && filename != "" {
				ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
				if ext != "" {
					if norm := normalizeFormatToMIME(ext); norm != "" {
						mType = norm
					}
				}
			}
			if isGenericMIME(mType) {
				mType = "application/octet-stream"
			}
			return mType, d, true
		}
		return "", "", false
	}

	return translatorcommon.NormalizeOpenAIFileData(filename, fallbackMIME, fileData)
}

func openAIResponsesMediaFromBlock(block gjson.Result) (mimeType string, data string, ok bool) {
	if mType, d, ok := openAIResponsesImageFromBlock(block); ok {
		return mType, d, true
	}
	if mType, d, ok := openAIResponsesAudioFromBlock(block); ok {
		return mType, d, true
	}
	if mType, d, ok := openAIResponsesVideoFromBlock(block); ok {
		return mType, d, true
	}
	if mType, d, ok := openAIResponsesFileFromBlock(block); ok {
		return mType, d, true
	}
	return "", "", false
}

func openAIResponsesPartFromBlock(block gjson.Result) ([]byte, bool) {
	bType := strings.ToLower(strings.TrimSpace(block.Get("type").String()))

	// 1. Check for remote URLs (http://, https://, gs://)
	rawURL := firstNonEmpty(
		block.Get("video_url.url").String(),
		block.Get("video_url").String(),
		block.Get("audio_url.url").String(),
		block.Get("audio_url").String(),
		block.Get("image_url.url").String(),
		block.Get("image_url").String(),
		block.Get("file_url.url").String(),
		block.Get("file_url").String(),
		block.Get("file.file_url").String(),
		block.Get("url").String(),
	)
	if isRemoteURL(rawURL) {
		filename := firstNonEmpty(block.Get("filename").String(), block.Get("file.filename").String())
		if filename == "" {
			if parsed, errParse := url.Parse(rawURL); errParse == nil {
				filename = filepath.Base(parsed.Path)
			}
		}
		format := firstNonGenericFormat(
			block.Get("format").String(),
			block.Get("mime_type").String(),
			block.Get("input_video.format").String(),
			block.Get("input_video.mime_type").String(),
			block.Get("video.format").String(),
			block.Get("video.mime_type").String(),
			block.Get("input_audio.format").String(),
			block.Get("input_audio.mime_type").String(),
			block.Get("audio.format").String(),
			block.Get("audio.mime_type").String(),
			block.Get("input_image.format").String(),
			block.Get("input_image.mime_type").String(),
			block.Get("image.format").String(),
			block.Get("image.mime_type").String(),
			block.Get("file.format").String(),
			block.Get("file.mime_type").String(),
		)
		var mimeType string
		switch bType {
		case "input_video", "video_url", "video":
			if format != "" && !isGenericMIME(format) {
				mimeType = openAIResponsesVideoMimeType(format)
			} else if filename != "" {
				ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
				if ext != "" {
					mimeType = openAIResponsesVideoMimeType(ext)
				}
			}
			if isGenericMIME(mimeType) {
				mimeType = "video/mp4"
			}
		case "input_audio", "audio":
			if format != "" && !isGenericMIME(format) {
				mimeType = openAIResponsesAudioMimeType(format)
			} else if filename != "" {
				ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
				if ext != "" {
					mimeType = openAIResponsesAudioMimeType(ext)
				}
			}
			if isGenericMIME(mimeType) {
				mimeType = "audio/wav"
			}
		case "input_image", "image_url", "image":
			mimeType = openAIResponsesImageMimeType(format, filename)
		default:
			if format != "" {
				mimeType = normalizeFormatToMIME(format)
			}
			if isGenericMIME(mimeType) && filename != "" {
				ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
				if ext != "" {
					mimeType = normalizeFormatToMIME(ext)
				}
			}
			if isGenericMIME(mimeType) {
				mimeType = "application/octet-stream"
			}
		}
		return geminiResponsesFileDataPart(mimeType, rawURL), true
	}

	// 2. Check for inline base64 or data URL
	if mimeType, data, ok := openAIResponsesMediaFromBlock(block); ok {
		return geminiResponsesInlineDataPart(mimeType, data), true
	}

	return nil, false
}

func openAIResponsesImageMimeType(format, filename string) string {
	format = strings.TrimSpace(format)
	if format != "" && !isGenericMIME(format) {
		if strings.Contains(format, "/") {
			return format
		}
		fLower := strings.ToLower(format)
		if fLower == "jpg" || fLower == "jpeg" {
			return "image/jpeg"
		}
		if mapped := misc.MimeTypes[fLower]; mapped != "" {
			return mapped
		}
		return "image/" + format
	}
	if filename != "" {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
		if ext == "jpg" || ext == "jpeg" {
			return "image/jpeg"
		}
		if ext != "" {
			if mapped := misc.MimeTypes[ext]; mapped != "" {
				return mapped
			}
		}
	}
	return "image/png"
}

func openAIResponsesImageFromBlock(block gjson.Result) (mimeType string, data string, ok bool) {
	blockType := strings.ToLower(strings.TrimSpace(block.Get("type").String()))
	switch blockType {
	case "input_image", "image_url", "image":
		format := firstNonGenericFormat(
			block.Get("format").String(),
			block.Get("mime_type").String(),
			block.Get("input_image.format").String(),
			block.Get("input_image.mime_type").String(),
			block.Get("image.format").String(),
			block.Get("image.mime_type").String(),
		)
		filename := firstNonEmpty(
			block.Get("filename").String(),
			block.Get("file.filename").String(),
		)

		// 1. image_url
		imageURL := firstNonEmpty(
			block.Get("image_url.url").String(),
			block.Get("image_url").String(),
			block.Get("url").String(),
		)
		if imageURL != "" {
			if isDataURL(imageURL) {
				mType, d := parseOpenAIResponsesDataURL(imageURL)
				if d != "" {
					if isGenericMIME(mType) {
						mType = openAIResponsesImageMimeType(format, filename)
					}
					return mType, d, true
				}
				return "", "", false
			} else if !isRemoteURL(imageURL) {
				return openAIResponsesImageMimeType(format, filename), imageURL, true
			}
		}

		// 2. source object (base64)
		imageData := ""
		if block.Get("source.type").String() == "base64" {
			imageData = block.Get("source.data").String()
			if format == "" {
				format = block.Get("source.media_type").String()
			}
		}

		// 3. direct data
		if imageData == "" && block.Get("data").Exists() {
			imageData = block.Get("data").String()
		}

		if imageData == "" {
			return "", "", false
		}

		if isDataURL(imageData) {
			mType, d := parseOpenAIResponsesDataURL(imageData)
			if d != "" {
				if isGenericMIME(mType) {
					mType = openAIResponsesImageMimeType(format, filename)
				}
				return mType, d, true
			}
			return "", "", false
		}

		return openAIResponsesImageMimeType(format, filename), imageData, true
	}
	return "", "", false
}

type openAIResponsesOutputBlock struct {
	text   string
	isText bool
	raw    string
}

func parseOpenAIResponsesArrayOutput(outputResult gjson.Result) (result string, isRaw bool, images [][]byte) {
	var imageParts [][]byte
	var nonImageEntries []openAIResponsesOutputBlock
	var hasContentBlock bool
	var hasNonTextBlock bool

	outputResult.ForEach(func(_, block gjson.Result) bool {
		if mimeType, data, ok := openAIResponsesMediaFromBlock(block); ok {
			hasContentBlock = true
			imageParts = append(imageParts, geminiResponsesInlineDataPart(mimeType, data))
			return true
		}
		bType := block.Get("type").String()
		if bType == "input_text" || bType == "output_text" || bType == "text" {
			hasContentBlock = true
			nonImageEntries = append(nonImageEntries, openAIResponsesOutputBlock{
				text:   block.Get("text").String(),
				isText: true,
				raw:    block.Raw,
			})
		} else if block.Type == gjson.String {
			nonImageEntries = append(nonImageEntries, openAIResponsesOutputBlock{
				text:   block.String(),
				isText: true,
				raw:    block.Raw,
			})
		} else {
			hasNonTextBlock = true
			nonImageEntries = append(nonImageEntries, openAIResponsesOutputBlock{
				text:   block.Raw,
				isText: false,
				raw:    block.Raw,
			})
		}
		return true
	})

	if !hasContentBlock {
		return outputResult.Raw, true, nil
	}

	switch len(nonImageEntries) {
	case 0:
		return "", false, imageParts
	case 1:
		if nonImageEntries[0].isText {
			return nonImageEntries[0].text, false, imageParts
		}
		return nonImageEntries[0].raw, true, imageParts
	default:
		if !hasNonTextBlock {
			texts := make([]string, len(nonImageEntries))
			for idx, e := range nonImageEntries {
				texts[idx] = e.text
			}
			return strings.Join(texts, "\n"), false, imageParts
		}
		rawItems := make([][]byte, len(nonImageEntries))
		for idx, e := range nonImageEntries {
			rawItems[idx] = []byte(e.raw)
		}
		return string(translatorcommon.JoinRawArray(rawItems)), true, imageParts
	}
}

func extractOpenAIResponsesCallID(node gjson.Result) string {
	return translatorcommon.ExtractResponsesCallID(node)
}

func buildOpenAIResponsesSynthesizedFunctionResponsePart(callID string, functionNamesByCallID map[string]string) []byte {
	functionName := "unknown"
	if matchedName, ok := functionNamesByCallID[callID]; ok && matchedName != "" {
		functionName = matchedName
	}
	functionResponse := []byte(`{"functionResponse":{"name":"","response":{"result":"call interrupted, no output"}}}`)
	functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.name", util.SanitizeFunctionName(functionName))
	if callID != "" {
		functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.id", callID)
	}
	return functionResponse
}

func responsesHasMatchingOutput(items []gjson.Result, callID string) bool {
	if callID == "" {
		return false
	}
	for _, item := range items {
		typ := item.Get("type").String()
		if typ == "function_call_output" || typ == "custom_tool_call_output" {
			if extractOpenAIResponsesCallID(item) == callID {
				return true
			}
		}
	}
	return false
}

func responsesHasSubsequentTurn(items []gjson.Result) bool {
	for _, item := range items {
		typ := item.Get("type").String()
		role := item.Get("role").String()
		if typ == "message" || (typ == "" && role != "") {
			return true
		}
		if typ == "function_call" || typ == "custom_tool_call" {
			return true
		}
	}
	return false
}

func buildOpenAIResponsesStandaloneToolOutputTextParts(item gjson.Result) [][]byte {
	output := item.Get("output")
	if !output.Exists() {
		return nil
	}
	if output.IsArray() {
		var parts [][]byte
		output.ForEach(func(_, part gjson.Result) bool {
			text := part.Get("text").String()
			if strings.TrimSpace(text) == "" {
				return true
			}
			textPart := []byte(`{"text":""}`)
			textPart, _ = sjson.SetBytes(textPart, "text", text)
			parts = append(parts, textPart)
			return true
		})
		return parts
	}
	text := output.String()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	textPart := []byte(`{"text":""}`)
	textPart, _ = sjson.SetBytes(textPart, "text", text)
	return [][]byte{textPart}
}

func buildOpenAIResponsesFunctionResponseParts(item gjson.Result, functionNamesByCallID map[string]string) [][]byte {
	callID := extractOpenAIResponsesCallID(item)
	functionName := "unknown"
	if matchedName, ok := functionNamesByCallID[callID]; ok {
		functionName = matchedName
	} else if name := strings.TrimSpace(item.Get("name").String()); name != "" {
		functionName = name
	}
	functionResponse := []byte(`{"functionResponse":{"name":"","response":{}}}`)
	functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.name", util.SanitizeFunctionName(functionName))
	functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.id", callID)

	outputResult := item.Get("output")
	if outputResult.Type == gjson.String {
		str := outputResult.String()
		if str == "" || str == "null" {
			return [][]byte{functionResponse}
		}
		// Keep it as a string instead of parsing it into JSON.
		// Parsing it as JSON, similar to reading a JSON file with readFile, may trigger an upstream 400 error.
		functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.response.result", str)
		return [][]byte{functionResponse}
	}

	var imageParts [][]byte
	switch {
	case outputResult.IsArray():
		result, isRaw, images := parseOpenAIResponsesArrayOutput(outputResult)
		imageParts = images
		if isRaw {
			functionResponse = translatorcommon.SetGeminiFunctionResponseRaw(functionResponse, "functionResponse.response.result", result)
		} else {
			functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.response.result", result)
		}
	case outputResult.IsObject():
		if mimeType, data, ok := openAIResponsesMediaFromBlock(outputResult); ok {
			imageParts = append(imageParts, geminiResponsesInlineDataPart(mimeType, data))
			functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.response.result", "")
		} else {
			functionResponse = translatorcommon.SetGeminiFunctionResponseResult(functionResponse, "functionResponse.response.result", outputResult)
		}
	case outputResult.Raw != "" && outputResult.Raw != "null":
		functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.response.result", outputResult.String())
	}

	for _, part := range imageParts {
		if inline := gjson.GetBytes(part, "inline_data"); inline.Exists() {
			inlineData := []byte(`{"inlineData":{"mimeType":"","data":""}}`)
			inlineData, _ = sjson.SetBytes(inlineData, "inlineData.mimeType", inline.Get("mime_type").String())
			inlineData, _ = sjson.SetBytes(inlineData, "inlineData.data", inline.Get("data").String())
			functionResponse, _ = sjson.SetRawBytes(functionResponse, "functionResponse.parts.-1", inlineData)
		} else if fileData := gjson.GetBytes(part, "file_data"); fileData.Exists() {
			fileDataObj := []byte(`{"fileData":{"mimeType":"","fileUri":""}}`)
			fileDataObj, _ = sjson.SetBytes(fileDataObj, "fileData.mimeType", fileData.Get("mime_type").String())
			fileDataObj, _ = sjson.SetBytes(fileDataObj, "fileData.fileUri", fileData.Get("file_uri").String())
			functionResponse, _ = sjson.SetRawBytes(functionResponse, "functionResponse.parts.-1", fileDataObj)
		}
	}
	return [][]byte{functionResponse}
}

func collectOpenAIResponsesFunctionCallOutputs(items []gjson.Result, start int, pendingCallIDs []string) ([]gjson.Result, map[int]bool, []string) {
	end := start + 1
	for end < len(items) && (items[end].Get("type").String() == "function_call_output" || items[end].Get("type").String() == "custom_tool_call_output") {
		end++
	}
	outputs := items[start:end]
	ordered, remainingPending := orderOpenAIResponsesFunctionCallOutputs(outputs, pendingCallIDs)
	consumed := make(map[int]bool, len(outputs))
	for itemIndex := start; itemIndex < end; itemIndex++ {
		consumed[itemIndex] = true
	}
	return ordered, consumed, remainingPending
}

func orderOpenAIResponsesFunctionCallOutputs(outputs []gjson.Result, pendingCallIDs []string) ([]gjson.Result, []string) {
	ordered := make([]gjson.Result, 0, len(outputs))
	used := make([]bool, len(outputs))
	remainingPending := make([]string, 0, len(pendingCallIDs))
	for _, pendingID := range pendingCallIDs {
		match := -1
		for outputIndex, output := range outputs {
			if !used[outputIndex] && extractOpenAIResponsesCallID(output) == pendingID {
				match = outputIndex
				break
			}
		}
		if match < 0 {
			remainingPending = append(remainingPending, pendingID)
			continue
		}
		used[match] = true
		ordered = append(ordered, outputs[match])
	}
	for outputIndex, output := range outputs {
		if !used[outputIndex] {
			ordered = append(ordered, output)
		}
	}
	return ordered, remainingPending
}

func buildOpenAIResponsesFunctionCallModelContent(item gjson.Result, signature string, forwardMap map[string]string) []byte {
	modelContent := []byte(`{"role":"model","parts":[]}`)
	modelContent, _ = sjson.SetRawBytes(modelContent, "parts", translatorcommon.JoinRawArray([][]byte{buildOpenAIResponsesFunctionCallPart(item, signature, forwardMap)}))
	return modelContent
}

func buildOpenAIResponsesEmptyReasoningFunctionCallModelContent(item gjson.Result, signature string, forwardMap map[string]string) []byte {
	thought := []byte(`{"text":"","thought":true,"thoughtSignature":""}`)
	thought, _ = sjson.SetBytes(thought, "thoughtSignature", signature)
	parts := [][]byte{thought, buildOpenAIResponsesFunctionCallPart(item, signature, forwardMap)}
	modelContent := []byte(`{"role":"model","parts":[]}`)
	modelContent, _ = sjson.SetRawBytes(modelContent, "parts", translatorcommon.JoinRawArray(parts))
	return modelContent
}

func buildOpenAIResponsesReasoningFunctionCallModelContent(thoughtText string, item gjson.Result, signature string, forwardMap map[string]string) []byte {
	parts := make([][]byte, 0, 2)
	if thoughtText != "" {
		thought := []byte(`{"text":"","thought":true}`)
		thought, _ = sjson.SetBytes(thought, "text", thoughtText)
		parts = append(parts, thought)
	}
	parts = append(parts, buildOpenAIResponsesFunctionCallPart(item, signature, forwardMap))
	modelContent := []byte(`{"role":"model","parts":[]}`)
	modelContent, _ = sjson.SetRawBytes(modelContent, "parts", translatorcommon.JoinRawArray(parts))
	return modelContent
}

func buildOpenAIResponsesReasoningModelContent(thoughtText, visibleText, signature string, useGeminiNativeReasoningLayout bool) []byte {
	modelContent := []byte(`{"role":"model","parts":[]}`)
	if useGeminiNativeReasoningLayout {
		if thoughtText == "" && visibleText == "" {
			carrier := []byte(`{"text":"","thoughtSignature":""}`)
			carrier, _ = sjson.SetBytes(carrier, "thoughtSignature", signature)
			return translatorcommon.SetRawArrayItems(modelContent, "parts", [][]byte{carrier})
		}
		var parts [][]byte
		if thoughtText != "" {
			thought := []byte(`{"text":"","thought":true}`)
			thought, _ = sjson.SetBytes(thought, "text", thoughtText)
			if visibleText == "" {
				thought, _ = sjson.SetBytes(thought, "thoughtSignature", signature)
			}
			parts = append(parts, thought)
		}
		if visibleText != "" {
			visible := []byte(`{"text":"","thoughtSignature":""}`)
			visible, _ = sjson.SetBytes(visible, "text", visibleText)
			visible, _ = sjson.SetBytes(visible, "thoughtSignature", signature)
			parts = append(parts, visible)
		}
		return translatorcommon.SetRawArrayItems(modelContent, "parts", parts)
	}

	thought := []byte(`{"text":"","thoughtSignature":"","thought":true}`)
	thought, _ = sjson.SetBytes(thought, "text", thoughtText)
	thought, _ = sjson.SetBytes(thought, "thoughtSignature", signature)
	return translatorcommon.SetRawArrayItems(modelContent, "parts", [][]byte{thought})
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
		out, _ = sjson.SetBytes(out, "generationConfig.responseMimeType", "application/json")
	case "json_schema":
		out, _ = sjson.SetBytes(out, "generationConfig.responseMimeType", "application/json")

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
