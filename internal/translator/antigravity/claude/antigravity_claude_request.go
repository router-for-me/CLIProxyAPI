// Package claude provides request translation functionality for Claude Code API compatibility.
// This package handles the conversion of Claude Code API requests into Antigravity-compatible
// JSON format, transforming message contents, system instructions, and tool declarations
// into the format expected by Antigravity API clients. It performs JSON data transformation
// to ensure compatibility between Claude Code API format and Antigravity API's expected format.
package claude

import (
	"context"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
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

	// 1. Check client-carried provider-native (or legacy prefixed) signature first.
	// If the client provided a signature that is incompatible or invalid, do not
	// fall back to recovery cache.
	if rawSignature != "" {
		if signature := resolveProviderCompatibleSignature(targetProvider, rawSignature, sigcompat.SignatureBlockKindUnknown); signature != "" {
			return signature, nil
		}
		return "", nil
	}

	// 2. Recovery cache only when client omitted signature (rawSignature == "").
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

	return "", nil
}

func RequireCachedThinkingSignatures(ctx context.Context, modelName string, rawJSON []byte) error {
	if !cache.SignatureCacheEnabled() {
		return nil
	}
	if sigcompat.SignatureProviderFromModelName(modelName) == sigcompat.SignatureProviderGemini {
		return nil
	}
	messagesResult := ojson.GetBytesNoCopy(rawJSON, "messages")
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

func resolveToolUseThoughtSignature(modelName string, contentResult ojson.Result, allowSyntheticFallback bool) string {
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

func firstToolUseSignatureField(contentResult ojson.Result) (string, string, bool) {
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

func logDroppedAntigravityThinkingSignature(modelName string, messageIndex, contentIndex int, thinkingText string, signatureResult ojson.Result) {
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

func logDroppedAntigravityToolUseSignature(modelName string, messageIndex, contentIndex int, contentResult ojson.Result) {
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
func ConvertClaudeRequestToAntigravity(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertClaudeRequestToAntigravity(modelName, inputRawJSON, stream)

}

// convertClaudeRequestToAntigravity also reports an attachment Antigravity
// cannot receive when it leaves a user turn with nothing to send.
func convertClaudeRequestToAntigravity(modelName string, inputRawJSON []byte, _ bool) ([]byte, error) {
	enableThoughtTranslate := true
	// The internal commentary marker must be restored as assistant text before
	// any thinking handling, so it is never dropped or treated as reasoning.
	rawJSON, _ := sigcompat.RestoreCodexCommentaryMarkers(inputRawJSON)
	if shouldBuildAntigravityWebSearchRequest(modelName, rawJSON) {
		return buildAntigravityWebSearchRequest(modelName, rawJSON), nil
	}
	var drops translatorcommon.UserTurnDrops
	functionNameMap := util.SanitizedFunctionNameMap(rawJSON)

	// system instruction
	systemParts := make([][]byte, 0, 2)
	systemResult := ojson.GetBytesNoCopy(rawJSON, "system")
	if systemResult.IsArray() {
		systemResults := systemResult.Array()
		for i := 0; i < len(systemResults); i++ {
			systemPromptResult := systemResults[i]
			systemTypePromptResult := systemPromptResult.Get("type")
			if systemTypePromptResult.Type == ojson.String && systemTypePromptResult.String() == "text" {
				systemPrompt := systemPromptResult.Get("text").String()
				if util.IsClaudeCodeAttributionSystemText(systemPrompt) {
					continue
				}
				partJSON := []byte(`{}`)
				if systemPrompt != "" {
					partJSON, _ = ojson.SetBytes(partJSON, "text", systemPrompt)
				}
				systemParts = append(systemParts, partJSON)
			}
		}
	} else if systemResult.Type == ojson.String && !util.IsClaudeCodeAttributionSystemText(systemResult.String()) {
		partJSON := []byte(`{"text":""}`)
		partJSON, _ = ojson.SetBytes(partJSON, "text", systemResult.String())
		systemParts = append(systemParts, partJSON)
	}

	// contents
	contentItems := translatorcommon.NewRawArrayItems(ojson.GetBytesNoCopy(rawJSON, "messages.#").Int())

	// tool_use_id → tool_name lookup, populated incrementally during the main loop.
	// Claude's tool_result references tool_use by ID; Gemini requires functionResponse.name.
	toolNameByID := make(map[string]string)
	var pendingToolUseIDs []string

	messagesResult := ojson.GetBytesNoCopy(rawJSON, "messages")
	if messagesResult.IsArray() {
		messageResults := messagesResult.Array()
		numMessages := len(messageResults)
		for i := 0; i < numMessages; i++ {
			messageResult := messageResults[i]
			roleResult := messageResult.Get("role")
			if roleResult.Type != ojson.String {
				continue
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
			appendDetachedCarrier := func(signature string, _ bool) {
				carrier := []byte(`{"text":"","thoughtSignature":""}`)
				carrier, _ = ojson.SetBytes(carrier, "thoughtSignature", signature)
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
			if originalRole == "system" || originalRole == "developer" {
				if reminderText, ok := translatorcommon.ClaudeMessageSystemReminderText(contentsResult); ok {
					partJSON := []byte(`{}`)
					partJSON, _ = ojson.SetBytes(partJSON, "text", reminderText)
					partItems = append(partItems, partJSON)
					contentItems = append(contentItems, antigravityClaudeContent(role, partItems))
				}
				continue
			}
			if contentsResult.IsArray() {
				if originalRole == "user" {
					contentsResult = translatorcommon.AlignClaudeToolResults(contentsResult, precedingToolUseIDs)
				}
				contentResults := contentsResult.Array()
				numContents := len(contentResults)
				for j := 0; j < numContents; j++ {
					contentResult := contentResults[j]
					contentTypeResult := contentResult.Get("type")
					if contentTypeResult.Type == ojson.String && contentTypeResult.String() == "thinking" {
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

						isGeminiSignature := sigcompat.SignatureProviderFromModelName(modelName) == sigcompat.SignatureProviderGemini

						// Skip unsigned thinking blocks instead of converting them to text for non-Gemini providers.
						isUnsigned := !hasResolvedThinkingSignature(modelName, signature)

						// If unsigned, skip entirely (don't convert to text)
						// Claude requires assistant messages to start with thinking blocks when thinking is enabled
						// Converting to text would break this requirement
						if isUnsigned && !isGeminiSignature {
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
						_, carrierDirection, carrierTargetKind, markedCarrier, validCarrier := decodeGeminiClaudeCarrierSignature(signatureResult.String())

						// Gemini places the signature on the visible text/function part that
						// follows hidden thought text. Keep the thought text, but defer its
						// opaque signature to that native neighboring part.
						if thinkingText != "" {
							partDoc := ojson.NewDoc([]byte(`{}`))
							_ = partDoc.Set("thought", true)
							_ = partDoc.Set("text", thinkingText)
							if signatureFromPendingCarrier {
								_ = partDoc.Set("thoughtSignature", signature)
							} else if markedCarrier {
								carrierTargetsNext := carrierTargetKind == geminiClaudeCarrierAny || carrierTargetKind == nextTargetKind
								if validCarrier && carrierDirection == geminiClaudeCarrierStandalone && (carrierTargetKind == geminiClaudeCarrierText || carrierTargetKind == geminiClaudeCarrierAny) {
									_ = partDoc.Set("thoughtSignature", signature)
								} else if validCarrier && carrierDirection == geminiClaudeCarrierNext && nextAcceptsDetachedSignature && carrierTargetsNext {
									setPendingDetachedSignature(signature, carrierTargetKind)
								}
							} else if isGeminiSignature && nextAcceptsDetachedSignature {
								setPendingDetachedSignature(signature, nextTargetKind)
							} else if signature != "" {
								_ = partDoc.Set("thoughtSignature", signature)
							}
							partItems = append(partItems, partDoc.Bytes())
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
							part := util.ParseGJSONBytesNoCopy(partItems[partIndex])
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
							partSignature := strings.TrimSpace(part.Get("thoughtSignature").String())
							replaceFallback := bindBackward && partTargetKind == geminiClaudeCarrierFunction && partSignature == sigcompat.GeminiSkipThoughtSignatureValidator
							if partSignature == "" || replaceFallback {
								partItems[partIndex], _ = ojson.SetBytes(partItems[partIndex], "thoughtSignature", signature)
								attached = true
							}
							break
						}
						if !attached && (foundSemanticPart || bindBackward) {
							appendDetachedCarrier(signature, false)
						} else if !attached {
							setPendingDetachedSignature(signature, carrierTargetKind)
						}
					} else if contentTypeResult.Type == ojson.String && contentTypeResult.String() == "text" {
						prompt := contentResult.Get("text").String()
						// Skip empty text parts to avoid Gemini API error:
						// "required oneof field 'data' must have one initialized field"
						if prompt == "" {
							continue
						}
						partDoc := ojson.NewDoc([]byte(`{}`))
						_ = partDoc.Set("text", prompt)
						if pendingDetachedSignature != "" {
							if pendingDetachedTargetKind == "" || pendingDetachedTargetKind == geminiClaudeCarrierAny || pendingDetachedTargetKind == geminiClaudeCarrierText {
								_ = partDoc.Set("thoughtSignature", pendingDetachedSignature)
							} else {
								appendDetachedCarrier(pendingDetachedSignature, true)
							}
							clearPendingDetachedSignature()
						}
						partItems = append(partItems, partDoc.Bytes())
					} else if contentTypeResult.Type == ojson.String && contentTypeResult.String() == "tool_use" {
						// NOTE: Do NOT inject dummy thinking blocks here.
						// Antigravity API validates signatures, so dummy values are rejected.

						originalFunctionName := contentResult.Get("name").String()
						functionName := util.MapSanitizedFunctionName(functionNameMap, originalFunctionName)
						argsResult := contentResult.Get("input")
						functionID := contentResult.Get("id").String()

						if functionID != "" && originalFunctionName != "" {
							toolNameByID[functionID] = originalFunctionName
						}

						// Preserve every present input as valid JSON for the function call.
						var argsRaw string
						if argsResult.IsObject() {
							argsRaw = argsResult.Raw
						} else if argsResult.Exists() {
							switch argsResult.Type {
							case ojson.String:
								// Parse JSON-encoded object strings while preserving other strings as JSON strings.
								parsed := ojson.Parse(argsResult.String())
								if parsed.IsObject() {
									argsRaw = parsed.Raw
								} else {
									argsRaw = argsResult.Raw
								}
							case ojson.Null:
								argsRaw = `{}`
							default:
								argsRaw = argsResult.Raw
							}
						}

						if argsRaw != "" {
							partDoc := ojson.NewDoc([]byte(`{}`))

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
								_ = partDoc.Set("thoughtSignature", signature)
							} else {
								logDroppedAntigravityToolUseSignature(modelName, i, j, contentResult)
							}

							if functionID != "" {
								_ = partDoc.Set("functionCall.id", functionID)
							}
							_ = partDoc.Set("functionCall.name", functionName)
							_ = partDoc.SetRaw("functionCall.args", argsRaw)
							partItems = append(partItems, partDoc.Bytes())
							if originalRole == "assistant" {
								pendingToolUseIDs = append(pendingToolUseIDs, functionID)
							}
						}
					} else if contentTypeResult.Type == ojson.String && contentTypeResult.String() == "tool_result" {
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

							functionResponseDoc := ojson.NewDoc([]byte(`{}`))
							_ = functionResponseDoc.Set("id", toolCallID)
							_ = functionResponseDoc.Set("name", util.MapSanitizedFunctionName(functionNameMap, funcName))

							responsePath := "response.result"
							if contentResult.Get("is_error").Type == ojson.True {
								responsePath = "response.error"
							}

							responseData := ""
							if functionResponseResult.Type == ojson.String {
								responseData = functionResponseResult.String()
								_ = functionResponseDoc.Set(responsePath, responseData)
							} else if functionResponseResult.IsArray() {
								frResults := functionResponseResult.Array()
								nonImageItems := make([][]byte, 0, len(frResults))
								imagePartItems := make([][]byte, 0, 2)
								for _, fr := range frResults {
									if fr.Get("type").String() == "image" && fr.Get("source.type").String() == "base64" {
										inlineDataDoc := ojson.NewDoc([]byte(`{}`))
										if mimeType := fr.Get("source.media_type").String(); mimeType != "" {
											_ = inlineDataDoc.Set("mimeType", mimeType)
										}
										if data := fr.Get("source.data").String(); data != "" {
											_ = inlineDataDoc.Set("data", data)
										}

										imagePartJSON, _ := ojson.SetRawBytes([]byte(`{}`), "inlineData", inlineDataDoc.Bytes())
										imagePartItems = append(imagePartItems, imagePartJSON)
										continue
									}

									nonImageItems = append(nonImageItems, []byte(fr.Raw))
								}

								if len(nonImageItems) == 1 {
									translatorcommon.SetGeminiFunctionResponseRawDoc(functionResponseDoc, responsePath, string(nonImageItems[0]))
								} else if len(nonImageItems) > 1 {
									translatorcommon.SetGeminiFunctionResponseRawDoc(functionResponseDoc, responsePath, string(translatorcommon.JoinRawArray(nonImageItems)))
								} else {
									_ = functionResponseDoc.Set(responsePath, "")
								}

								// Place image data inside functionResponse.parts as inlineData
								// instead of as sibling parts in the outer content, to avoid
								// base64 data bloating the text context.
								if len(imagePartItems) > 0 {
									_ = functionResponseDoc.SetRawBytes("parts", translatorcommon.JoinRawArray(imagePartItems))
								}

							} else if functionResponseResult.IsObject() {
								if functionResponseResult.Get("type").String() == "image" && functionResponseResult.Get("source.type").String() == "base64" {
									inlineDataDoc := ojson.NewDoc([]byte(`{}`))
									if mimeType := functionResponseResult.Get("source.media_type").String(); mimeType != "" {
										_ = inlineDataDoc.Set("mimeType", mimeType)
									}
									if data := functionResponseResult.Get("source.data").String(); data != "" {
										_ = inlineDataDoc.Set("data", data)
									}

									imagePartJSON, _ := ojson.SetRawBytes([]byte(`{}`), "inlineData", inlineDataDoc.Bytes())
									_ = functionResponseDoc.SetRawBytes("parts", translatorcommon.JoinRawArray([][]byte{imagePartJSON}))
									_ = functionResponseDoc.Set(responsePath, "")
								} else {
									translatorcommon.SetGeminiFunctionResponseResultDoc(functionResponseDoc, responsePath, functionResponseResult)
								}
							} else if functionResponseResult.Raw != "" {
								translatorcommon.SetGeminiFunctionResponseResultDoc(functionResponseDoc, responsePath, functionResponseResult)
							} else {
								// Content field is missing entirely — .Raw is empty which
								// causes ojson.SetRaw to produce invalid JSON (e.g. "result":}).
								_ = functionResponseDoc.Set(responsePath, "")
							}

							partJSON, _ := ojson.SetRawBytes([]byte(`{}`), "functionResponse", functionResponseDoc.Bytes())
							partItems = append(partItems, partJSON)
						}
					} else if contentTypeResult.Type == ojson.String && contentTypeResult.String() == "image" {
						sourceResult := contentResult.Get("source")
						if sourceResult.Get("type").String() == "base64" {
							inlineDataDoc := ojson.NewDoc([]byte(`{}`))
							if mimeType := sourceResult.Get("media_type").String(); mimeType != "" {
								_ = inlineDataDoc.Set("mimeType", mimeType)
							}
							if data := sourceResult.Get("data").String(); data != "" {
								_ = inlineDataDoc.Set("data", data)
							}

							partJSON, _ := ojson.SetRawBytes([]byte(`{}`), "inlineData", inlineDataDoc.Bytes())
							partItems = append(partItems, partJSON)
						} else if originalRole == "user" {
							// A part that cannot be inlined (url or file source) is dropped; the turn is refused only if nothing else is left.
							drops.Drop("image")
						}
					} else if contentTypeResult.Type == ojson.String && (contentTypeResult.String() == "document" || contentTypeResult.String() == "container_upload") {
						if partJSON := claudeBase64InlineData(contentResult.Get("source")); partJSON != nil {
							partItems = append(partItems, partJSON)
						} else if originalRole == "user" {
							drops.Drop(contentTypeResult.String())
						}
					}
				}
				if pendingDetachedSignature != "" {
					appendDetachedCarrier(pendingDetachedSignature, false)
					clearPendingDetachedSignature()
				}

				if originalRole == "user" {
					// Whitespace-only text is forwarded but never keeps an emptied turn alive.
					drops.EndTurn(translatorcommon.CountSendableGeminiParts(partItems))
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
						part := util.ParseGJSONBytesNoCopy(partJSON)
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
						clientContentJSON, _ = ojson.SetRawBytes(clientContentJSON, "parts", translatorcommon.JoinRawArray(newParts))
					}
				}
				contentItems = append(contentItems, clientContentJSON)
			} else if contentsResult.Type == ojson.String {
				partJSON := []byte(`{}`)
				if prompt := contentsResult.String(); prompt != "" {
					partJSON, _ = ojson.SetBytes(partJSON, "text", prompt)
				}
				contentItems = append(contentItems, antigravityClaudeContent(role, [][]byte{partJSON}))
			}
		}
	}

	// tools
	var toolsJSON []byte
	toolDeclCount := 0
	allowedToolKeys := []string{"name", "description", "behavior", "parameters", "parametersJsonSchema", "response", "responseJsonSchema"}
	toolsResult := ojson.GetBytesNoCopy(rawJSON, "tools")
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
				toolDoc := ojson.NewDocString(toolResult.Raw)
				_ = toolDoc.Delete("input_schema")
				_ = toolDoc.SetRaw("parametersJsonSchema", inputSchema)
				nameResult := toolResult.Get("name")
				originalName := nameResult.String()
				mappedName := util.MapSanitizedFunctionName(functionNameMap, originalName)
				if nameResult.Type != ojson.String || mappedName != originalName {
					_ = toolDoc.Set("name", mappedName)
				}
				for toolKey := range toolResult.Map() {
					if toolKey == "input_schema" || util.InArray(allowedToolKeys, toolKey) {
						continue
					}
					_ = toolDoc.Delete(toolKey)
				}
				functionDeclarations = append(functionDeclarations, toolDoc.Bytes())
			}
		}
		if len(functionDeclarations) > 0 {
			deduplicated := util.DeduplicateFunctionDeclarations(translatorcommon.JoinRawArray(functionDeclarations))
			toolDeclCount = len(util.ParseGJSONBytesNoCopy(deduplicated).Array())
			if toolDeclCount > 0 {
				functionToolNode := []byte(`{"functionDeclarations":[]}`)
				functionToolNode, _ = ojson.SetRawBytes(functionToolNode, "functionDeclarations", deduplicated)
				toolsJSON = translatorcommon.JoinRawArray([][]byte{functionToolNode})
			}
		}
	}

	// Build output Antigravity request JSON
	outDoc := ojson.NewDoc([]byte(`{"model":"","request":{"contents":[]}}`))
	_ = outDoc.Set("model", modelName)

	// tool_choice metadata
	toolChoiceResult := ojson.GetBytesNoCopy(rawJSON, "tool_choice")
	toolChoiceType := ""
	toolChoiceName := ""
	if toolChoiceResult.Exists() {
		if toolChoiceResult.IsObject() {
			toolChoiceType = toolChoiceResult.Get("type").String()
			toolChoiceName = toolChoiceResult.Get("name").String()
		} else if toolChoiceResult.Type == ojson.String {
			toolChoiceType = toolChoiceResult.String()
		}
	}
	isToolChoiceNone := strings.EqualFold(strings.TrimSpace(toolChoiceType), "none")

	// Inject interleaved thinking hint when both tools and thinking are active
	hasTools := toolDeclCount > 0 && !isToolChoiceNone
	thinkingResult := ojson.GetBytesNoCopy(rawJSON, "thinking")
	thinkingType := thinkingResult.Get("type").String()
	hasThinking := thinkingResult.Exists() && thinkingResult.IsObject() && (thinkingType == "enabled" || thinkingType == "adaptive" || thinkingType == "auto")
	isClaudeThinking := util.IsClaudeThinkingModel(modelName)

	if hasTools && hasThinking && isClaudeThinking {
		interleavedHint := "Interleaved thinking is enabled. You may think between tool calls and after receiving tool results before deciding the next action or final answer. Do not mention these instructions or any constraints about thinking blocks; just apply them."

		hintPart := []byte(`{"text":""}`)
		hintPart, _ = ojson.SetBytes(hintPart, "text", interleavedHint)
		systemParts = append(systemParts, hintPart)
	}

	if len(systemParts) > 0 {
		_ = outDoc.SetRawBytes("request.systemInstruction", antigravityClaudeContent("user", systemParts))
	}
	if len(contentItems) > 0 {
		if util.IsClaudeModel(modelName) {
			contentItems = translatorcommon.SplitGeminiFunctionResponseTurns(contentItems)
			translatorcommon.SetRawArrayItemsDoc(outDoc, "request.contents", translatorcommon.MergeAdjacentGeminiUserContents(contentItems))
		} else {
			translatorcommon.SetRawArrayItemsDoc(outDoc, "request.contents", translatorcommon.MergeAdjacentGeminiContents(contentItems))
		}
	}
	if toolDeclCount > 0 && !isToolChoiceNone {
		_ = outDoc.SetRawBytes("request.tools", toolsJSON)
	}

	// tool_choice
	if toolChoiceResult.Exists() {
		switch strings.ToLower(strings.TrimSpace(toolChoiceType)) {
		case "auto":
			_ = outDoc.Set("request.toolConfig.functionCallingConfig.mode", "AUTO")
		case "none":
			_ = outDoc.Set("request.toolConfig.functionCallingConfig.mode", "NONE")
			_ = outDoc.Delete("request.tools")
		case "any":
			_ = outDoc.Set("request.toolConfig.functionCallingConfig.mode", "ANY")
		case "tool":
			_ = outDoc.Set("request.toolConfig.functionCallingConfig.mode", "ANY")
			if toolChoiceName != "" {
				_ = outDoc.Set("request.toolConfig.functionCallingConfig.allowedFunctionNames", []string{util.MapSanitizedFunctionName(functionNameMap, toolChoiceName)})
			}
		}
	}

	// Map Anthropic thinking -> Gemini thinkingBudget/include_thoughts when type==enabled
	if t := ojson.GetBytesNoCopy(rawJSON, "thinking"); enableThoughtTranslate && t.Exists() && t.IsObject() {
		switch t.Get("type").String() {
		case "enabled":
			if b := t.Get("budget_tokens"); b.Exists() && b.Type == ojson.Number {
				budget := int(b.Int())
				_ = outDoc.Set("request.generationConfig.thinkingConfig.thinkingBudget", budget)
			}
		case "adaptive", "auto":
			// For adaptive thinking:
			// - If output_config.effort is explicitly present, pass through as thinkingLevel.
			// - Otherwise, treat it as "enabled with target-model maximum" and emit high.
			// ApplyThinking handles clamping to target model's supported levels.
			effort := ""
			if v := ojson.GetBytesNoCopy(rawJSON, "output_config.effort"); v.Exists() && v.Type == ojson.String {
				effort = strings.ToLower(strings.TrimSpace(v.String()))
			}
			if effort != "" {
				_ = outDoc.Set("request.generationConfig.thinkingConfig.thinkingLevel", effort)
			} else {
				_ = outDoc.Set("request.generationConfig.thinkingConfig.thinkingLevel", "high")
			}
		}
	}
	if v := ojson.GetBytesNoCopy(rawJSON, "temperature"); v.Exists() && v.Type == ojson.Number {
		_ = outDoc.Set("request.generationConfig.temperature", v.Num)
	}
	if v := ojson.GetBytesNoCopy(rawJSON, "top_p"); v.Exists() && v.Type == ojson.Number {
		_ = outDoc.Set("request.generationConfig.topP", v.Num)
	}
	if v := ojson.GetBytesNoCopy(rawJSON, "top_k"); v.Exists() && v.Type == ojson.Number {
		_ = outDoc.Set("request.generationConfig.topK", v.Num)
	}
	if v := ojson.GetBytesNoCopy(rawJSON, "max_tokens"); v.Exists() && v.Type == ojson.Number {
		_ = outDoc.Set("request.generationConfig.maxOutputTokens", v.Num)
	}

	common.AttachDefaultSafetySettingsDoc(outDoc, "request.safetySettings")
	out := outDoc.Bytes()
	if sigcompat.SignatureProviderFromModelName(modelName) == sigcompat.SignatureProviderGemini {
		out = sigcompat.SanitizeGeminiRequestThoughtSignatures(out, "request.contents")
	} else {
		out = sigcompat.SanitizeGeminiSystemInstruction(out, "request.contents")
	}

	return out, drops.Err()
}

// claudeBase64InlineData converts a Claude base64 source into an Antigravity
// inlineData part. A file id or url carries no bytes, so it stays unconverted
// and the caller reports it.
func claudeBase64InlineData(source ojson.Result) []byte {
	if source.Get("type").String() != "base64" {
		return nil
	}
	mimeType := source.Get("media_type").String()
	data := source.Get("data").String()
	if mimeType == "" || data == "" {
		return nil
	}
	inlineDataDoc := ojson.NewDoc([]byte(`{}`))
	_ = inlineDataDoc.Set("mimeType", mimeType)
	_ = inlineDataDoc.Set("data", data)
	partJSON, _ := ojson.SetRawBytes([]byte(`{}`), "inlineData", inlineDataDoc.Bytes())
	return partJSON
}

func antigravityClaudeContent(role string, parts [][]byte) []byte {
	contentDoc := ojson.NewDoc([]byte(`{"role":"","parts":[]}`))
	_ = contentDoc.Set("role", role)
	_ = contentDoc.SetRawBytes("parts", translatorcommon.JoinRawArray(parts))
	return contentDoc.Bytes()
}
