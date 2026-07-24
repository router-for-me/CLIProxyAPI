// Package claude provides request translation functionality for Claude Code API compatibility.
// This package handles the conversion of Claude Code API requests into Antigravity-compatible
// JSON format, transforming message contents, system instructions, and tool declarations
// into the format expected by Antigravity API clients. It performs JSON data transformation
// to ensure compatibility between Claude Code API format and Antigravity API's expected format.
package claude

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func resolveThinkingSignature(modelName, thinkingText, rawSignature string) string {
	signature, errSignature := resolveThinkingSignatureRequired(context.Background(), modelName, thinkingText, rawSignature)
	if errSignature != nil {
		return ""
	}
	return signature
}

func resolveThinkingSignatureRequired(ctx context.Context, modelName, thinkingText, rawSignature string) (string, error) {
	targetProvider := sigcompat.SignatureProviderFromModelName(modelName)
	if targetProvider == sigcompat.SignatureProviderGemini {
		innerSignature, _, targetKind, marked, okCarrier := decodeGeminiClaudeCarrierSignature(rawSignature)
		if !okCarrier {
			return "", nil
		}
		blockKind := sigcompat.SignatureBlockKindGeminiModelPart
		if marked && targetKind == geminiClaudeCarrierFunction {
			blockKind = sigcompat.SignatureBlockKindGeminiFunctionCall
		}
		return resolveProviderCompatibleSignature(targetProvider, innerSignature, blockKind), nil
	}
	if cache.SignatureCacheEnabled() {
		return resolveCacheModeSignatureRequired(ctx, modelName, thinkingText, rawSignature)
	}
	if signature := resolveProviderCompatibleSignature(targetProvider, rawSignature, sigcompat.SignatureBlockKindUnknown); signature != "" {
		return signature, nil
	}
	return resolveBypassModeSignatureForProvider(targetProvider, rawSignature), nil
}

func resolveCacheModeSignature(modelName, thinkingText, rawSignature string) string {
	signature, errSignature := resolveCacheModeSignatureRequired(context.Background(), modelName, thinkingText, rawSignature)
	if errSignature != nil {
		return ""
	}
	return signature
}

func resolveCacheModeSignatureRequired(ctx context.Context, modelName, thinkingText, rawSignature string) (string, error) {
	targetProvider := sigcompat.SignatureProviderFromModelName(modelName)
	if thinkingText != "" {
		cachedSig, errCachedSig := cache.GetCachedSignatureRequired(ctx, modelName, thinkingText)
		if errCachedSig != nil {
			return "", errCachedSig
		}
		if cachedSig != "" {
			if targetProvider == sigcompat.SignatureProviderClaude {
				signature, ok := sigcompat.CompatibleAntigravityClaudeThinkingSignature(cachedSig)
				if !ok {
					return "", nil
				}
				return signature, nil
			}
			return cachedSig, nil
		}
	}

	if rawSignature == "" {
		return "", nil
	}

	clientSignature := ""
	arrayClientSignatures := strings.SplitN(rawSignature, "#", 2)
	if len(arrayClientSignatures) == 2 {
		if cache.GetModelGroup(modelName) == arrayClientSignatures[0] {
			clientSignature = arrayClientSignatures[1]
		}
	}
	if cache.HasValidSignature(modelName, clientSignature) {
		if targetProvider == sigcompat.SignatureProviderClaude {
			signature, ok := sigcompat.CompatibleAntigravityClaudeThinkingSignature(clientSignature)
			if !ok {
				return "", nil
			}
			return signature, nil
		}
		return clientSignature, nil
	}

	return "", nil
}

func RequireCachedThinkingSignatures(ctx context.Context, modelName string, rawJSON []byte) error {
	if !cache.SignatureCacheEnabled() {
		return nil
	}
	if sigcompat.SignatureProviderFromModelName(modelName) == sigcompat.SignatureProviderGemini {
		return nil
	}
	messagesResult := gjson.GetBytes(rawJSON, "messages")
	if !messagesResult.IsArray() {
		return nil
	}
	for _, messageResult := range messagesResult.Array() {
		contentsResult := messageResult.Get("content")
		if !contentsResult.IsArray() {
			continue
		}
		for _, contentResult := range contentsResult.Array() {
			if contentResult.Get("type").String() != "thinking" {
				continue
			}
			thinkingText := thinking.GetThinkingText(contentResult)
			if thinkingText == "" {
				continue
			}
			if _, errSignature := cache.GetCachedSignatureRequired(ctx, modelName, thinkingText); errSignature != nil {
				return errSignature
			}
		}
	}
	return nil
}

func resolveBypassModeSignature(rawSignature string) string {
	return resolveBypassModeSignatureForProvider(sigcompat.SignatureProviderClaude, rawSignature)
}

func resolveBypassModeSignatureForProvider(targetProvider sigcompat.SignatureProvider, rawSignature string) string {
	if rawSignature == "" {
		return ""
	}
	if targetProvider != sigcompat.SignatureProviderClaude && targetProvider != sigcompat.SignatureProviderUnknown {
		return ""
	}
	if targetProvider == sigcompat.SignatureProviderClaude {
		signature, ok := sigcompat.CompatibleAntigravityClaudeThinkingSignature(rawSignature)
		if !ok {
			return ""
		}
		return signature
	}
	normalized, err := normalizeClaudeBypassSignature(rawSignature)
	if err != nil {
		return ""
	}
	return normalized
}

func hasResolvedThinkingSignature(modelName, signature string) bool {
	targetProvider := sigcompat.SignatureProviderFromModelName(modelName)
	if targetProvider == sigcompat.SignatureProviderClaude {
		_, ok := sigcompat.CompatibleAntigravityClaudeThinkingSignature(signature)
		return ok
	}
	if _, ok := sigcompat.CompatibleSignatureForProvider(targetProvider, signature); ok {
		return true
	}
	if cache.SignatureCacheEnabled() {
		return cache.HasValidSignature(modelName, signature)
	}
	return signature != ""
}

func resolveProviderCompatibleSignature(targetProvider sigcompat.SignatureProvider, rawSignature string, blockKind sigcompat.SignatureBlockKind) string {
	if rawSignature == "" {
		return ""
	}
	if targetProvider == sigcompat.SignatureProviderClaude {
		signature, ok := sigcompat.CompatibleAntigravityClaudeThinkingSignature(rawSignature)
		if !ok {
			return ""
		}
		return signature
	}
	signature, ok := sigcompat.CompatibleSignatureForProviderBlock(targetProvider, rawSignature, blockKind)
	if !ok {
		return ""
	}
	return signature
}

func resolveToolUseThoughtSignature(modelName string, contentResult gjson.Result, allowSyntheticFallback bool) string {
	targetProvider := sigcompat.SignatureProviderFromModelName(modelName)
	if targetProvider == sigcompat.SignatureProviderGemini {
		for _, path := range []string{
			"signature",
			"thought_signature",
			"extra_content.google.thought_signature",
		} {
			if signatureResult := contentResult.Get(path); signatureResult.Exists() {
				if signature := resolveProviderCompatibleSignature(targetProvider, signatureResult.String(), sigcompat.SignatureBlockKindGeminiFunctionCall); signature != "" {
					return signature
				}
			}
		}
		if allowSyntheticFallback {
			return sigcompat.GeminiSkipThoughtSignatureValidator
		}
		return ""
	}

	for _, path := range []string{
		"signature",
		"thought_signature",
		"extra_content.google.thought_signature",
	} {
		if signatureResult := contentResult.Get(path); signatureResult.Exists() {
			if signature := resolveProviderCompatibleSignature(targetProvider, signatureResult.String(), sigcompat.SignatureBlockKindUnknown); signature != "" {
				return signature
			}
		}
	}
	if targetProvider == sigcompat.SignatureProviderClaude {
		return ""
	}
	return sigcompat.GeminiSkipThoughtSignatureValidator
}

func firstToolUseSignatureField(contentResult gjson.Result) (string, string, bool) {
	for _, path := range []string{
		"signature",
		"thought_signature",
		"extra_content.google.thought_signature",
	} {
		signatureResult := contentResult.Get(path)
		if signatureResult.Exists() {
			return path, signatureResult.String(), true
		}
	}
	return "", "", false
}

func logDroppedAntigravityThinkingSignature(modelName string, messageIndex, contentIndex int, thinkingText string, signatureResult gjson.Result) {
	rawSignature := signatureResult.String()
	fields := log.Fields{
		"component":        "signature_sanitizer",
		"translator":       "antigravity_claude",
		"target_provider":  string(sigcompat.SignatureProviderFromModelName(modelName)),
		"action":           "drop_thinking_block",
		"reason":           "missing_or_incompatible_signature",
		"model":            modelName,
		"message_index":    messageIndex,
		"content_index":    contentIndex,
		"thinking_length":  len(thinkingText),
		"has_signature":    signatureResult.Exists(),
		"signature_length": len(strings.TrimSpace(rawSignature)),
	}
	if signatureResult.Exists() {
		fields["detected_provider"] = string(sigcompat.DetectSignatureProviderForBlock(rawSignature, sigcompat.SignatureBlockKindClaudeThinking))
	}
	log.WithFields(fields).Debug("antigravity claude translator: dropped thinking block with incompatible signature")
}

func logDroppedAntigravityEmptyThinking(modelName string, messageIndex, contentIndex int) {
	log.WithFields(log.Fields{
		"component":       "signature_sanitizer",
		"translator":      "antigravity_claude",
		"target_provider": string(sigcompat.SignatureProviderFromModelName(modelName)),
		"action":          "drop_thinking_block",
		"reason":          "empty_thinking_text",
		"model":           modelName,
		"message_index":   messageIndex,
		"content_index":   contentIndex,
	}).Debug("antigravity claude translator: dropped empty thinking block")
}

func logDroppedAntigravityToolUseSignature(modelName string, messageIndex, contentIndex int, contentResult gjson.Result) {
	path, rawSignature, ok := firstToolUseSignatureField(contentResult)
	if !ok {
		return
	}
	log.WithFields(log.Fields{
		"component":         "signature_sanitizer",
		"translator":        "antigravity_claude",
		"target_provider":   string(sigcompat.SignatureProviderFromModelName(modelName)),
		"action":            "drop_tool_use_signature",
		"reason":            "missing_or_incompatible_signature",
		"model":             modelName,
		"message_index":     messageIndex,
		"content_index":     contentIndex,
		"signature_path":    path,
		"signature_length":  len(strings.TrimSpace(rawSignature)),
		"detected_provider": string(sigcompat.DetectSignatureProviderForBlock(rawSignature, sigcompat.SignatureBlockKindUnknown)),
	}).Debug("antigravity claude translator: dropped tool_use signature field")
}

// ConvertClaudeRequestToAntigravity parses and transforms a Claude Code API request into Antigravity API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Antigravity API.
// The function performs the following transformations:
// 1. Extracts the model information from the request
// 2. Restructures the JSON to match Antigravity API format
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
//   - []byte: The transformed request data in Antigravity API format
func ConvertClaudeRequestToAntigravity(modelName string, inputRawJSON []byte, _ bool) []byte {
	enableThoughtTranslate := true
	rawJSON := inputRawJSON
	if shouldBuildAntigravityWebSearchRequest(modelName, rawJSON) {
		return buildAntigravityWebSearchRequest(modelName, rawJSON)
	}
	functionNameMap := util.SanitizedFunctionNameMap(rawJSON)

	// system instruction
	systemParts := make([][]byte, 0, 2)
	systemResult := gjson.GetBytes(rawJSON, "system")
	if systemResult.IsArray() {
		systemResults := systemResult.Array()
		for i := 0; i < len(systemResults); i++ {
			systemPromptResult := systemResults[i]
			systemTypePromptResult := systemPromptResult.Get("type")
			if systemTypePromptResult.Type == gjson.String && systemTypePromptResult.String() == "text" {
				systemPrompt := systemPromptResult.Get("text").String()
				if util.IsClaudeCodeAttributionSystemText(systemPrompt) {
					continue
				}
				partJSON := []byte(`{}`)
				if systemPrompt != "" {
					partJSON, _ = sjson.SetBytes(partJSON, "text", systemPrompt)
				}
				systemParts = append(systemParts, partJSON)
			}
		}
	} else if systemResult.Type == gjson.String && !util.IsClaudeCodeAttributionSystemText(systemResult.String()) {
		partJSON := []byte(`{"text":""}`)
		partJSON, _ = sjson.SetBytes(partJSON, "text", systemResult.String())
		systemParts = append(systemParts, partJSON)
	}

	// contents
	contentItems := translatorcommon.NewRawArrayItems(gjson.GetBytes(rawJSON, "messages.#").Int())

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
			} else if role == "system" {
				role = "user"
			}
			partItems := make([][]byte, 0, 4)
			appendDetachedCarrier := func(signature string, _ bool) {
				carrier := []byte(`{"text":"","thoughtSignature":""}`)
				carrier, _ = sjson.SetBytes(carrier, "thoughtSignature", signature)
				partItems = append(partItems, carrier)
			}
			pendingDetachedSignature := ""
			pendingDetachedTargetKind := ""
			clearPendingDetachedSignature := func() {
				pendingDetachedSignature = ""
				pendingDetachedTargetKind = ""
			}
			setPendingDetachedSignature := func(signature, targetKind string) {
				if pendingDetachedSignature != "" {
					appendDetachedCarrier(pendingDetachedSignature, true)
				}
				pendingDetachedSignature = signature
				pendingDetachedTargetKind = targetKind
			}
			contentsResult := messageResult.Get("content")
			if originalRole == "system" {
				if reminderText, ok := translatorcommon.ClaudeMessageSystemReminderText(contentsResult); ok {
					partJSON := []byte(`{}`)
					partJSON, _ = sjson.SetBytes(partJSON, "text", reminderText)
					partItems = append(partItems, partJSON)
					contentItems = append(contentItems, antigravityClaudeContent(role, partItems))
				}
				continue
			}
			if contentsResult.IsArray() {
				contentResults := contentsResult.Array()
				numContents := len(contentResults)
				for j := 0; j < numContents; j++ {
					contentResult := contentResults[j]
					contentTypeResult := contentResult.Get("type")
					if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "thinking" {
						if originalRole != "assistant" {
							continue
						}
						// Use GetThinkingText to handle wrapped thinking objects
						thinkingText := thinking.GetThinkingText(contentResult)
						signatureResult := contentResult.Get("signature")
						signature := resolveThinkingSignature(modelName, thinkingText, signatureResult.String())
						if signature != "" && pendingDetachedSignature != "" {
							if pendingDetachedSignature != signature {
								appendDetachedCarrier(pendingDetachedSignature, false)
							}
							clearPendingDetachedSignature()
						}
						signatureFromPendingCarrier := false
						if signature == "" && thinkingText != "" && pendingDetachedSignature != "" {
							if pendingDetachedTargetKind == "" || pendingDetachedTargetKind == geminiClaudeCarrierAny || pendingDetachedTargetKind == geminiClaudeCarrierText {
								signature = pendingDetachedSignature
								signatureFromPendingCarrier = true
							} else {
								appendDetachedCarrier(pendingDetachedSignature, true)
							}
							clearPendingDetachedSignature()
						}

						// Skip unsigned thinking blocks instead of converting them to text.
						isUnsigned := !hasResolvedThinkingSignature(modelName, signature)

						// If unsigned, skip entirely (don't convert to text)
						// Claude requires assistant messages to start with thinking blocks when thinking is enabled
						// Converting to text would break this requirement
						if isUnsigned {
							logDroppedAntigravityThinkingSignature(modelName, i, j, thinkingText, signatureResult)
							enableThoughtTranslate = false
							continue
						}

						nextAcceptsDetachedSignature := false
						nextTargetKind := geminiClaudeCarrierAny
						if j+1 < numContents {
							switch contentResults[j+1].Get("type").String() {
							case "text":
								nextAcceptsDetachedSignature = true
								nextTargetKind = geminiClaudeCarrierText
							case "tool_use":
								nextAcceptsDetachedSignature = true
								nextTargetKind = geminiClaudeCarrierFunction
							}
						}
						isGeminiSignature := sigcompat.SignatureProviderFromModelName(modelName) == sigcompat.SignatureProviderGemini
						_, carrierDirection, carrierTargetKind, markedCarrier, validCarrier := decodeGeminiClaudeCarrierSignature(signatureResult.String())

						// Gemini places the signature on the visible text/function part that
						// follows hidden thought text. Keep the thought text, but defer its
						// opaque signature to that native neighboring part.
						if thinkingText != "" {
							partJSON := []byte(`{}`)
							partJSON, _ = sjson.SetBytes(partJSON, "thought", true)
							partJSON, _ = sjson.SetBytes(partJSON, "text", thinkingText)
							if signatureFromPendingCarrier {
								partJSON, _ = sjson.SetBytes(partJSON, "thoughtSignature", signature)
							} else if markedCarrier {
								carrierTargetsNext := carrierTargetKind == geminiClaudeCarrierAny || carrierTargetKind == nextTargetKind
								if validCarrier && carrierDirection == geminiClaudeCarrierStandalone && (carrierTargetKind == geminiClaudeCarrierText || carrierTargetKind == geminiClaudeCarrierAny) {
									partJSON, _ = sjson.SetBytes(partJSON, "thoughtSignature", signature)
								} else if validCarrier && carrierDirection == geminiClaudeCarrierNext && nextAcceptsDetachedSignature && carrierTargetsNext {
									setPendingDetachedSignature(signature, carrierTargetKind)
								}
							} else if isGeminiSignature && nextAcceptsDetachedSignature {
								setPendingDetachedSignature(signature, nextTargetKind)
							} else if signature != "" {
								partJSON, _ = sjson.SetBytes(partJSON, "thoughtSignature", signature)
							}
							partItems = append(partItems, partJSON)
							continue
						}

						if !isGeminiSignature {
							logDroppedAntigravityEmptyThinking(modelName, i, j)
							continue
						}
						if markedCarrier && !validCarrier {
							continue
						}
						if markedCarrier && carrierDirection == geminiClaudeCarrierNext {
							if geminiClaudeCarrierMatchesAdjacent(contentResults, j, carrierDirection, carrierTargetKind) {
								setPendingDetachedSignature(signature, carrierTargetKind)
							}
							continue
						}
						if markedCarrier && carrierDirection == geminiClaudeCarrierStandalone {
							appendDetachedCarrier(signature, false)
							continue
						}

						// Tagged trailing carriers bind backward even when another semantic
						// block follows. Untagged legacy carriers retain adjacency behavior.
						bindBackward := markedCarrier && carrierDirection == geminiClaudeCarrierPrevious
						if bindBackward && !geminiClaudeCarrierMatchesAdjacent(contentResults, j, carrierDirection, carrierTargetKind) {
							continue
						}
						if !bindBackward && nextAcceptsDetachedSignature {
							setPendingDetachedSignature(signature, nextTargetKind)
							continue
						}
						attached := false
						foundSemanticPart := false
						for partIndex := len(partItems) - 1; partIndex >= 0; partIndex-- {
							part := gjson.ParseBytes(partItems[partIndex])
							partTargetKind := ""
							switch {
							case part.Get("functionCall").Exists():
								partTargetKind = geminiClaudeCarrierFunction
							case part.Get("text").Exists() && part.Get("text").String() != "":
								partTargetKind = geminiClaudeCarrierText
							default:
								continue
							}
							foundSemanticPart = true
							if markedCarrier && carrierTargetKind != geminiClaudeCarrierAny && carrierTargetKind != partTargetKind {
								break
							}
							if strings.TrimSpace(part.Get("thoughtSignature").String()) == "" {
								partItems[partIndex], _ = sjson.SetBytes(partItems[partIndex], "thoughtSignature", signature)
								attached = true
							}
							break
						}
						if !attached && (foundSemanticPart || bindBackward) {
							appendDetachedCarrier(signature, false)
						} else if !attached {
							setPendingDetachedSignature(signature, carrierTargetKind)
						}
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "text" {
						prompt := contentResult.Get("text").String()
						// Skip empty text parts to avoid Gemini API error:
						// "required oneof field 'data' must have one initialized field"
						if prompt == "" {
							continue
						}
						partJSON := []byte(`{}`)
						partJSON, _ = sjson.SetBytes(partJSON, "text", prompt)
						if pendingDetachedSignature != "" {
							if pendingDetachedTargetKind == "" || pendingDetachedTargetKind == geminiClaudeCarrierAny || pendingDetachedTargetKind == geminiClaudeCarrierText {
								partJSON, _ = sjson.SetBytes(partJSON, "thoughtSignature", pendingDetachedSignature)
							} else {
								appendDetachedCarrier(pendingDetachedSignature, true)
							}
							clearPendingDetachedSignature()
						}
						partItems = append(partItems, partJSON)
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "tool_use" {
						// NOTE: Do NOT inject dummy thinking blocks here.
						// Antigravity API validates signatures, so dummy values are rejected.

						originalFunctionName := contentResult.Get("name").String()
						functionName := util.MapSanitizedFunctionName(functionNameMap, originalFunctionName)
						argsResult := contentResult.Get("input")
						functionID := contentResult.Get("id").String()

						if functionID != "" && originalFunctionName != "" {
							toolNameByID[functionID] = originalFunctionName
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

							signature := resolveToolUseThoughtSignature(modelName, contentResult, true)
							if pendingDetachedSignature != "" {
								pendingMatchesTool := pendingDetachedTargetKind == "" || pendingDetachedTargetKind == geminiClaudeCarrierAny || pendingDetachedTargetKind == geminiClaudeCarrierFunction
								if pendingMatchesTool && (signature == "" || signature == sigcompat.GeminiSkipThoughtSignatureValidator) {
									signature = pendingDetachedSignature
								} else {
									appendDetachedCarrier(pendingDetachedSignature, true)
								}
								clearPendingDetachedSignature()
							}
							if signature != "" {
								partJSON, _ = sjson.SetBytes(partJSON, "thoughtSignature", signature)
							} else {
								logDroppedAntigravityToolUseSignature(modelName, i, j, contentResult)
							}

							if functionID != "" {
								partJSON, _ = sjson.SetBytes(partJSON, "functionCall.id", functionID)
							}
							partJSON, _ = sjson.SetBytes(partJSON, "functionCall.name", functionName)
							partJSON, _ = sjson.SetRawBytes(partJSON, "functionCall.args", []byte(argsRaw))
							partItems = append(partItems, partJSON)
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
							functionResponseJSON, _ = sjson.SetBytes(functionResponseJSON, "name", util.MapSanitizedFunctionName(functionNameMap, funcName))

							responseData := ""
							if functionResponseResult.Type == gjson.String {
								responseData = functionResponseResult.String()
								functionResponseJSON, _ = sjson.SetBytes(functionResponseJSON, "response.result", responseData)
							} else if functionResponseResult.IsArray() {
								frResults := functionResponseResult.Array()
								nonImageItems := make([][]byte, 0, len(frResults))
								imagePartItems := make([][]byte, 0, 2)
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
										imagePartItems = append(imagePartItems, imagePartJSON)
										continue
									}

									nonImageItems = append(nonImageItems, []byte(fr.Raw))
								}

								if len(nonImageItems) == 1 {
									functionResponseJSON, _ = sjson.SetRawBytes(functionResponseJSON, "response.result", nonImageItems[0])
								} else if len(nonImageItems) > 1 {
									functionResponseJSON, _ = sjson.SetRawBytes(functionResponseJSON, "response.result", translatorcommon.JoinRawArray(nonImageItems))
								} else {
									functionResponseJSON, _ = sjson.SetBytes(functionResponseJSON, "response.result", "")
								}

								// Place image data inside functionResponse.parts as inlineData
								// instead of as sibling parts in the outer content, to avoid
								// base64 data bloating the text context.
								if len(imagePartItems) > 0 {
									functionResponseJSON, _ = sjson.SetRawBytes(functionResponseJSON, "parts", translatorcommon.JoinRawArray(imagePartItems))
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
									functionResponseJSON, _ = sjson.SetRawBytes(functionResponseJSON, "parts", translatorcommon.JoinRawArray([][]byte{imagePartJSON}))
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
							partItems = append(partItems, partJSON)
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
							partItems = append(partItems, partJSON)
						}
					}
				}
				if pendingDetachedSignature != "" {
					appendDetachedCarrier(pendingDetachedSignature, false)
					clearPendingDetachedSignature()
				}

				// Reorder model parts: thinking first, regular content second, function calls and trailing signature carriers last.
				if len(partItems) == 0 {
					continue
				}
				clientContentJSON := antigravityClaudeContent(role, partItems)
				if role == "model" && len(partItems) > 1 {
					var thinkingParts [][]byte
					var regularParts [][]byte
					var trailingParts [][]byte
					needsReorder := false
					previousCategory := -1
					seenFunctionCall := false
					for _, partJSON := range partItems {
						part := gjson.ParseBytes(partJSON)
						category := 1
						isSignatureCarrier := part.Get("text").Exists() && part.Get("text").String() == "" && strings.TrimSpace(part.Get("thoughtSignature").String()) != ""
						isFunctionTailCarrier := isSignatureCarrier && seenFunctionCall
						if part.Get("thought").Bool() {
							category = 0
							thinkingParts = append(thinkingParts, partJSON)
						} else if part.Get("functionCall").Exists() || isFunctionTailCarrier {
							category = 2
							trailingParts = append(trailingParts, partJSON)
							seenFunctionCall = seenFunctionCall || part.Get("functionCall").Exists()
						} else {
							regularParts = append(regularParts, partJSON)
						}
						needsReorder = needsReorder || category < previousCategory
						previousCategory = category
					}
					if needsReorder {
						newParts := make([][]byte, 0, len(partItems))
						newParts = append(newParts, thinkingParts...)
						newParts = append(newParts, regularParts...)
						newParts = append(newParts, trailingParts...)
						clientContentJSON, _ = sjson.SetRawBytes(clientContentJSON, "parts", translatorcommon.JoinRawArray(newParts))
					}
				}
				contentItems = append(contentItems, clientContentJSON)
			} else if contentsResult.Type == gjson.String {
				partJSON := []byte(`{}`)
				if prompt := contentsResult.String(); prompt != "" {
					partJSON, _ = sjson.SetBytes(partJSON, "text", prompt)
				}
				contentItems = append(contentItems, antigravityClaudeContent(role, [][]byte{partJSON}))
			}
		}
	}

	// tools
	var toolsJSON []byte
	toolDeclCount := 0
	allowedToolKeys := []string{"name", "description", "behavior", "parameters", "parametersJsonSchema", "response", "responseJsonSchema"}
	toolsResult := gjson.GetBytes(rawJSON, "tools")
	if toolsResult.IsArray() {
		var functionDeclarations [][]byte
		toolsResults := toolsResult.Array()
		for i := 0; i < len(toolsResults); i++ {
			toolResult := toolsResults[i]
			if isClaudeTypedWebSearchToolType(toolResult.Get("type").String()) {
				continue
			}
			inputSchemaResult := toolResult.Get("input_schema")
			if inputSchemaResult.Exists() && inputSchemaResult.IsObject() {
				// Sanitize the input schema for Antigravity API compatibility
				inputSchema := util.CleanJSONSchemaForAntigravity(inputSchemaResult.Raw)
				tool, _ := sjson.DeleteBytes([]byte(toolResult.Raw), "input_schema")
				tool, _ = sjson.SetRawBytes(tool, "parametersJsonSchema", []byte(inputSchema))
				nameResult := gjson.GetBytes(tool, "name")
				originalName := nameResult.String()
				mappedName := util.MapSanitizedFunctionName(functionNameMap, originalName)
				if nameResult.Type != gjson.String || mappedName != originalName {
					tool, _ = sjson.SetBytes(tool, "name", mappedName)
				}
				for toolKey := range gjson.ParseBytes(tool).Map() {
					if util.InArray(allowedToolKeys, toolKey) {
						continue
					}
					tool, _ = sjson.DeleteBytes(tool, toolKey)
				}
				functionDeclarations = append(functionDeclarations, tool)
			}
		}
		if len(functionDeclarations) > 0 {
			deduplicated := util.DeduplicateFunctionDeclarations(translatorcommon.JoinRawArray(functionDeclarations))
			toolDeclCount = len(gjson.ParseBytes(deduplicated).Array())
			if toolDeclCount > 0 {
				functionToolNode := []byte(`{"functionDeclarations":[]}`)
				functionToolNode, _ = sjson.SetRawBytes(functionToolNode, "functionDeclarations", deduplicated)
				toolsJSON = translatorcommon.JoinRawArray([][]byte{functionToolNode})
			}
		}
	}

	// Build output Antigravity request JSON
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

		hintPart := []byte(`{"text":""}`)
		hintPart, _ = sjson.SetBytes(hintPart, "text", interleavedHint)
		systemParts = append(systemParts, hintPart)
	}

	if len(systemParts) > 0 {
		out, _ = sjson.SetRawBytes(out, "request.systemInstruction", antigravityClaudeContent("user", systemParts))
	}
	if len(contentItems) > 0 {
		out = translatorcommon.SetRawArrayItems(out, "request.contents", contentItems)
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
				out, _ = sjson.SetBytes(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames", []string{util.MapSanitizedFunctionName(functionNameMap, toolChoiceName)})
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
	if sigcompat.SignatureProviderFromModelName(modelName) == sigcompat.SignatureProviderGemini {
		out = sigcompat.SanitizeGeminiRequestThoughtSignatures(out, "request.contents")
	}

	return out
}

func antigravityClaudeContent(role string, parts [][]byte) []byte {
	content := []byte(`{"role":"","parts":[]}`)
	content, _ = sjson.SetBytes(content, "role", role)
	content, _ = sjson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(parts))
	return content
}
