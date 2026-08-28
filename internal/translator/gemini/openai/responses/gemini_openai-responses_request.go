package responses

import (
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

	// Extract tools and forward map early so request contents and toolDeclarations use the exact same forward map
	functionDeclarations, forwardMap, _ := util.BuildGeminiFunctionDeclarations(root)
	if len(functionDeclarations) > 0 {
		geminiTools := []byte(`[{"functionDeclarations":[]}]`)
		geminiTools, _ = sjson.SetRawBytes(geminiTools, "0.functionDeclarations", translatorcommon.JoinRawArray(functionDeclarations))
		out, _ = sjson.SetRawBytes(out, "tools", geminiTools)
	}

	// Handle tool_choice if present
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		if toolConfig, ok := util.ConvertResponsesToolChoiceToGemini(toolChoice, forwardMap); ok {
			out, _ = sjson.SetRawBytes(out, "toolConfig.functionCallingConfig", toolConfig)
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
		inputItems, hasGeminiCarrier := normalizeGeminiResponsesCarriers(input.Array())
		if hasGeminiCarrier {
			useGeminiNativeReasoningLayout = true
		}
		items := pairOpenAIResponsesReasoningWithFunctionCalls(inputItems)
		contentItems := make([][]byte, 0, len(items))
		functionNamesByCallID := make(map[string]string)
		pendingFunctionCallIDs := make([]string, 0)
		for _, item := range items {
			itemType := item.Get("type").String()
			if itemType == "function_call" || itemType == "custom_tool_call" {
				callID := item.Get("call_id").String()
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
		for i := 0; i < len(normalized); i++ {
			if consumedFunctionOutputIndexes[i] {
				continue
			}
			item := normalized[i]
			itemType := item.Get("type").String()
			itemRole := item.Get("role").String()
			if itemType == "" && itemRole != "" {
				itemType = "message"
			}

			switch itemType {
			case "message":
				if strings.EqualFold(itemRole, "system") || strings.EqualFold(itemRole, "developer") {
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

				if _, isAssistantOutput := openAIResponsesAssistantVisibleText(item); !isAssistantOutput {
					pendingFunctionCallIDs = nil
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
								mimeType, data := parseOpenAIResponsesDataURL(imageURL)
								if data != "" {
									partJSON = geminiResponsesInlineDataPart(mimeType, data)
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

			case "function_call", "custom_tool_call":
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
				if callID := strings.TrimSpace(item.Get("call_id").String()); callID != "" {
					pendingFunctionCallIDs = append(pendingFunctionCallIDs, callID)
				}

			case "function_call_output", "custom_tool_call_output":
				orderedOutputs, consumedIndexes, remainingPending := collectOpenAIResponsesFunctionCallOutputs(normalized, i, pendingFunctionCallIDs)
				pendingFunctionCallIDs = remainingPending
				for consumedIndex := range consumedIndexes {
					consumedFunctionOutputIndexes[consumedIndex] = true
				}
				responseParts := make([][]byte, 0, len(orderedOutputs))
				for _, output := range orderedOutputs {
					responseParts = append(responseParts, buildOpenAIResponsesFunctionResponseParts(output, functionNamesByCallID)...)
				}
				if len(responseParts) > 0 {
					contentItems = append(contentItems, geminiContent("user", responseParts))
				}

			case "reasoning":
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
				callID := strings.TrimSpace(item.Get("call_id").String())
				if callID == "" {
					continue
				}
				for outputIndex := groupEnd; outputIndex < outputEnd; outputIndex++ {
					if strings.TrimSpace(items[outputIndex].Get("call_id").String()) == callID {
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
	functionCall, _ = sjson.SetBytes(functionCall, "functionCall.id", item.Get("call_id").String())

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

func parseOpenAIResponsesDataURL(imageURL string) (string, string) {
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
	return mimeType, data
}

func openAIResponsesImageFromBlock(block gjson.Result) (mimeType string, data string, ok bool) {
	blockType := block.Get("type").String()
	switch blockType {
	case "input_image", "image_url", "image":
		imageURL := ""
		if block.Get("image_url.url").Exists() {
			imageURL = block.Get("image_url.url").String()
		} else if block.Get("image_url").Type == gjson.String {
			imageURL = block.Get("image_url").String()
		} else if block.Get("url").Exists() {
			imageURL = block.Get("url").String()
		}
		if imageURL != "" {
			mimeType, data = parseOpenAIResponsesDataURL(imageURL)
			if data != "" {
				return mimeType, data, true
			}
		}
		if block.Get("source.type").String() == "base64" {
			data = block.Get("source.data").String()
			mimeType = block.Get("source.media_type").String()
			if mimeType == "" {
				mimeType = "image/png"
			}
			if data != "" {
				return mimeType, data, true
			}
		}
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
		if mimeType, data, ok := openAIResponsesImageFromBlock(block); ok {
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

func buildOpenAIResponsesFunctionResponseParts(item gjson.Result, functionNamesByCallID map[string]string) [][]byte {
	callID := item.Get("call_id").String()
	functionName := "unknown"
	if matchedName, ok := functionNamesByCallID[callID]; ok {
		functionName = matchedName
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
			functionResponse, _ = sjson.SetRawBytes(functionResponse, "functionResponse.response.result", []byte(result))
		} else {
			functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.response.result", result)
		}
	case outputResult.IsObject():
		if mimeType, data, ok := openAIResponsesImageFromBlock(outputResult); ok {
			imageParts = append(imageParts, geminiResponsesInlineDataPart(mimeType, data))
			functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.response.result", "")
		} else {
			functionResponse, _ = sjson.SetRawBytes(functionResponse, "functionResponse.response.result", []byte(outputResult.Raw))
		}
	case outputResult.Raw != "" && outputResult.Raw != "null":
		functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.response.result", outputResult.String())
	}

	parts := make([][]byte, 0, 1+len(imageParts))
	parts = append(parts, functionResponse)
	parts = append(parts, imageParts...)
	return parts
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
			if !used[outputIndex] && output.Get("call_id").String() == pendingID {
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
