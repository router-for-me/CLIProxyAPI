package responses

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/gemini"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/openai/responses"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const antigravityWebSearchSystemInstruction = "You are a search engine bot. You will be given a query from a user. Your task is to search the web for relevant information that will help the user. You MUST perform a web search. Do not respond or interact with the user, please respond as if they typed the query into a search bar."

func antigravitySupportsNativeResponsesWebSearch(model string, modelInfo *registry.ModelInfo) bool {
	if modelInfo != nil && modelInfo.NativeCapabilities != nil && modelInfo.NativeCapabilities.WebSearch != nil {
		return *modelInfo.NativeCapabilities.WebSearch
	}
	model = strings.TrimSpace(thinking.ParseSuffix(strings.TrimSpace(model)).ModelName)
	if model == "" {
		return false
	}
	// Read the catalog veto and probe result from the same Antigravity record.
	for _, localInfo := range registry.GetGlobalRegistry().GetAvailableModelsByProvider("antigravity") {
		if localInfo == nil {
			continue
		}
		localModel := strings.TrimSpace(thinking.ParseSuffix(strings.TrimSpace(localInfo.ID)).ModelName)
		if !strings.EqualFold(localModel, model) {
			continue
		}
		if capabilities := localInfo.NativeCapabilities; capabilities != nil && capabilities.WebSearch != nil && !*capabilities.WebSearch {
			return false
		}
		return localInfo.SupportsWebSearch
	}
	return false
}

func shouldBuildAntigravityResponsesWebSearchRequest(model string, payload []byte, modelInfo *registry.ModelInfo) bool {
	root := gjson.ParseBytes(payload)
	return HasOnlyResponsesWebSearchTools(root) &&
		antigravitySupportsNativeResponsesWebSearch(model, modelInfo) &&
		AllowsResponsesWebSearchToolChoice(root)
}

func buildAntigravityResponsesWebSearchRequest(model string, payload []byte, stream bool) []byte {
	includedDomains := ExtractResponsesWebSearchAllowedDomains(gjson.ParseBytes(payload))
	rawJSON := ConvertOpenAIResponsesRequestToGemini(model, payload, stream)
	rawJSON = rewriteOpenAIResponsesReasoningForAntigravityClaude(model, payload, rawJSON)
	out := ConvertGeminiRequestToAntigravity(model, rawJSON, stream)
	out, _ = sjson.SetBytes(out, "requestType", "web_search")
	out = ensureAntigravityResponsesWebSearchTool(out, includedDomains)
	out = ensureAntigravityResponsesWebSearchSystemInstruction(out)
	return enableAntigravityResponsesThinkingSummary(payload, out)
}

func ensureAntigravityResponsesWebSearchTool(payload []byte, includedDomains []string) []byte {
	googleSearchTool := []byte(`{"googleSearch":{"enhancedContent":{"imageSearch":{"maxResultCount":5}}}}`)
	if len(includedDomains) > 0 {
		if domainsJSON, errMarshal := json.Marshal(includedDomains); errMarshal == nil {
			googleSearchTool, _ = sjson.SetRawBytes(googleSearchTool, "googleSearch.includedDomains", domainsJSON)
		}
	}

	tools := gjson.GetBytes(payload, "request.tools")
	if !tools.IsArray() {
		payload, _ = sjson.SetRawBytes(payload, "request.tools", translatorcommon.JoinRawArray([][]byte{googleSearchTool}))
		return payload
	}

	replaced := false
	filtered := make([][]byte, 0, len(tools.Array()))
	for _, tool := range tools.Array() {
		if tool.Get("googleSearch").Exists() {
			if !replaced {
				filtered = append(filtered, googleSearchTool)
				replaced = true
			}
			continue
		}
		filtered = append(filtered, []byte(tool.Raw))
	}
	if !replaced {
		filtered = append([][]byte{googleSearchTool}, filtered...)
	}
	payload, _ = sjson.SetRawBytes(payload, "request.tools", translatorcommon.JoinRawArray(filtered))
	return payload
}

func ensureAntigravityResponsesWebSearchSystemInstruction(payload []byte) []byte {
	searchPart := []byte(`{"text":""}`)
	searchPart, _ = sjson.SetBytes(searchPart, "text", antigravityWebSearchSystemInstruction)

	sys := gjson.GetBytes(payload, "request.systemInstruction")
	if !sys.Exists() {
		instr := []byte(`{"role":"user","parts":[]}`)
		instr, _ = sjson.SetRawBytes(instr, "parts", translatorcommon.JoinRawArray([][]byte{searchPart}))
		payload, _ = sjson.SetRawBytes(payload, "request.systemInstruction", instr)
		return payload
	}

	var parts [][]byte
	alreadyPresent := false
	if sys.Get("parts").IsArray() {
		for _, part := range sys.Get("parts").Array() {
			if part.Get("text").String() == antigravityWebSearchSystemInstruction {
				alreadyPresent = true
			}
			parts = append(parts, []byte(part.Raw))
		}
	}
	if !alreadyPresent {
		parts = append(parts, searchPart)
	}
	payload, _ = sjson.SetRawBytes(payload, "request.systemInstruction.parts", translatorcommon.JoinRawArray(parts))
	return payload
}

// ConvertOpenAIResponsesRequestToAntigravity translates an OpenAI Responses request
// to the Antigravity schema using locally registered Antigravity capabilities.
func ConvertOpenAIResponsesRequestToAntigravity(modelName string, inputRawJSON []byte, stream bool) []byte {
	req := ConvertOpenAIResponsesRequestEnvelopeToAntigravity(context.Background(), sdktranslator.RequestEnvelope{
		Model:  modelName,
		Body:   inputRawJSON,
		Stream: stream,
	})
	return req.Body
}

// ConvertOpenAIResponsesRequestEnvelopeToAntigravity translates an OpenAI Responses
// request and consumes request-scoped model capabilities from the envelope.
func ConvertOpenAIResponsesRequestEnvelopeToAntigravity(_ context.Context, req sdktranslator.RequestEnvelope) sdktranslator.RequestEnvelope {
	if shouldBuildAntigravityResponsesWebSearchRequest(req.Model, req.Body, req.ModelInfo) {
		req.Body = buildAntigravityResponsesWebSearchRequest(req.Model, req.Body, req.Stream)
		return req
	}
	inputRawJSON := req.Body
	req.Body = ConvertOpenAIResponsesRequestToGemini(req.Model, req.Body, req.Stream)
	req.Body = stripAntigravityResponsesGoogleSearch(req.Body)
	req.Body = rewriteOpenAIResponsesReasoningForAntigravityClaude(req.Model, inputRawJSON, req.Body)
	req.Body = ConvertGeminiRequestToAntigravity(req.Model, req.Body, req.Stream)
	req.Body = stripAntigravityResponsesGoogleSearch(req.Body)
	req.Body = enableAntigravityResponsesThinkingSummary(inputRawJSON, req.Body)
	return req
}

// stripAntigravityResponsesGoogleSearch removes any native googleSearch tool block
// from the request payload when falling back to a normal chat request, because
// Antigravity only supports native search in dedicated web_search request envelopes
// and rejects requests with mixed googleSearch and function declarations.
func stripAntigravityResponsesGoogleSearch(payload []byte) []byte {
	for _, path := range []string{"tools", "request.tools"} {
		tools := gjson.GetBytes(payload, path)
		if !tools.IsArray() {
			continue
		}
		var filtered [][]byte
		hasGoogleSearch := false
		for _, tool := range tools.Array() {
			if tool.Get("googleSearch").Exists() {
				hasGoogleSearch = true
				continue
			}
			filtered = append(filtered, []byte(tool.Raw))
		}
		if hasGoogleSearch {
			if len(filtered) == 0 {
				payload, _ = sjson.DeleteBytes(payload, path)
			} else {
				payload, _ = sjson.SetRawBytes(payload, path, translatorcommon.JoinRawArray(filtered))
			}
		}
	}
	return payload
}

// OpenAI Responses separates reasoning effort from reasoning summary visibility.
// Antigravity needs includeThoughts to emit thought parts, so effort alone would
// otherwise spend thinking tokens without returning a visible summary.
func enableAntigravityResponsesThinkingSummary(inputRawJSON, translated []byte) []byte {
	effort := gjson.GetBytes(inputRawJSON, "reasoning.effort")
	if effort.Type != gjson.String {
		return translated
	}
	effortVal := strings.ToLower(strings.TrimSpace(effort.String()))
	if effortVal == "" || effortVal == "none" {
		return translated
	}
	for _, path := range []string{"reasoning.summary", "reasoning.generate_summary"} {
		if value := gjson.GetBytes(inputRawJSON, path); value.Raw != "" {
			return translated
		}
	}
	return thinking.ApplySummaryConfig(translated, "antigravity", thinking.SummaryConfig{
		Mode:   thinking.SummaryEnabled,
		Detail: "auto",
	})
}

type antigravityClaudeReasoningSignature struct {
	Signature        string
	HasRawSignature  bool
	RawSignatureLen  int
	DetectedProvider sigcompat.SignatureProvider
}

func rewriteOpenAIResponsesReasoningForAntigravityClaude(modelName string, inputRawJSON, geminiJSON []byte) []byte {
	if sigcompat.SignatureProviderFromModelName(modelName) != sigcompat.SignatureProviderClaude {
		return geminiJSON
	}

	reasoningSignatures := antigravityClaudeReasoningSignatures(inputRawJSON)
	if len(reasoningSignatures) == 0 {
		return geminiJSON
	}

	var root map[string]any
	if err := json.Unmarshal(geminiJSON, &root); err != nil {
		log.WithError(err).Debug("antigravity responses translator: failed to parse Gemini request for Claude signature rewrite")
		return geminiJSON
	}

	contents, ok := root["contents"].([]any)
	if !ok {
		return geminiJSON
	}

	reasoningIndex := 0
	changed := false
	rewrittenContents := make([]any, 0, len(contents))
	for contentIndex, contentValue := range contents {
		content, ok := contentValue.(map[string]any)
		if !ok {
			rewrittenContents = append(rewrittenContents, contentValue)
			continue
		}

		parts, ok := content["parts"].([]any)
		if !ok {
			rewrittenContents = append(rewrittenContents, content)
			continue
		}

		rewrittenParts := make([]any, 0, len(parts))
		for partIndex, partValue := range parts {
			part, ok := partValue.(map[string]any)
			if !ok || part["thought"] != true {
				rewrittenParts = append(rewrittenParts, partValue)
				continue
			}

			var reasoningSig antigravityClaudeReasoningSignature
			if reasoningIndex < len(reasoningSignatures) {
				reasoningSig = reasoningSignatures[reasoningIndex]
			}
			reasoningIndex++

			if reasoningSig.Signature == "" {
				changed = true
				logDroppedOpenAIResponsesAntigravityClaudeReasoning(modelName, contentIndex, partIndex, reasoningIndex-1, reasoningSig)
				continue
			}
			if text, _ := part["text"].(string); strings.TrimSpace(text) == "" {
				changed = true
				logDroppedOpenAIResponsesAntigravityClaudeEmptyReasoning(modelName, contentIndex, partIndex, reasoningIndex-1, reasoningSig)
				continue
			}

			if currentSignature, _ := part["thoughtSignature"].(string); currentSignature != reasoningSig.Signature {
				changed = true
				logNormalizedOpenAIResponsesAntigravityClaudeReasoning(modelName, contentIndex, partIndex, reasoningIndex-1, reasoningSig)
			}
			part["thoughtSignature"] = reasoningSig.Signature
			rewrittenParts = append(rewrittenParts, part)
		}

		if len(rewrittenParts) == 0 {
			changed = true
			continue
		}
		content["parts"] = rewrittenParts
		rewrittenContents = append(rewrittenContents, content)
	}

	if !changed {
		return geminiJSON
	}

	root["contents"] = rewrittenContents
	out, err := json.Marshal(root)
	if err != nil {
		log.WithError(err).Debug("antigravity responses translator: failed to marshal Claude signature rewrite")
		return geminiJSON
	}
	return out
}

func antigravityClaudeReasoningSignatures(inputRawJSON []byte) []antigravityClaudeReasoningSignature {
	input := gjson.GetBytes(inputRawJSON, "input")
	if !input.IsArray() {
		return nil
	}

	signatures := make([]antigravityClaudeReasoningSignature, 0)
	input.ForEach(func(_, item gjson.Result) bool {
		itemType := item.Get("type").String()
		if itemType == "" && item.Get("role").Exists() {
			itemType = "message"
		}
		if itemType != "reasoning" {
			return true
		}

		rawSignatureResult := item.Get("encrypted_content")
		rawSignature := rawSignatureResult.String()
		signature, ok := sigcompat.CompatibleAntigravityClaudeThinkingSignature(rawSignature)
		reasoningSignature := antigravityClaudeReasoningSignature{
			HasRawSignature:  rawSignatureResult.Exists(),
			RawSignatureLen:  len(rawSignature),
			DetectedProvider: sigcompat.SignatureProviderUnknown,
		}
		if rawSignature != "" {
			reasoningSignature.DetectedProvider = sigcompat.DetectSignatureProviderForBlock(rawSignature, sigcompat.SignatureBlockKindClaudeThinking)
		}
		if ok {
			reasoningSignature.Signature = signature
		}
		signatures = append(signatures, reasoningSignature)
		return true
	})
	return signatures
}

func logDroppedOpenAIResponsesAntigravityClaudeReasoning(modelName string, contentIndex, partIndex, reasoningIndex int, sig antigravityClaudeReasoningSignature) {
	log.WithFields(log.Fields{
		"component":         "signature_sanitizer",
		"translator":        "antigravity_openai_responses",
		"target_provider":   string(sigcompat.SignatureProviderClaude),
		"action":            "drop_thinking_block",
		"reason":            "missing_or_incompatible_signature",
		"model":             modelName,
		"content_index":     contentIndex,
		"part_index":        partIndex,
		"reasoning_index":   reasoningIndex,
		"has_signature":     sig.HasRawSignature,
		"signature_length":  sig.RawSignatureLen,
		"detected_provider": string(sig.DetectedProvider),
	}).Debug("antigravity responses translator: dropped Claude reasoning block with incompatible encrypted_content")
}

func logDroppedOpenAIResponsesAntigravityClaudeEmptyReasoning(modelName string, contentIndex, partIndex, reasoningIndex int, sig antigravityClaudeReasoningSignature) {
	log.WithFields(log.Fields{
		"component":         "signature_sanitizer",
		"translator":        "antigravity_openai_responses",
		"target_provider":   string(sigcompat.SignatureProviderClaude),
		"action":            "drop_thinking_block",
		"reason":            "empty_thinking_text",
		"model":             modelName,
		"content_index":     contentIndex,
		"part_index":        partIndex,
		"reasoning_index":   reasoningIndex,
		"has_signature":     sig.HasRawSignature,
		"signature_length":  sig.RawSignatureLen,
		"detected_provider": string(sig.DetectedProvider),
	}).Debug("antigravity responses translator: dropped Claude reasoning block with empty thinking text")
}

func logNormalizedOpenAIResponsesAntigravityClaudeReasoning(modelName string, contentIndex, partIndex, reasoningIndex int, sig antigravityClaudeReasoningSignature) {
	log.WithFields(log.Fields{
		"component":         "signature_sanitizer",
		"translator":        "antigravity_openai_responses",
		"target_provider":   string(sigcompat.SignatureProviderClaude),
		"action":            "normalize_signature",
		"reason":            "compatible_claude_signature",
		"model":             modelName,
		"content_index":     contentIndex,
		"part_index":        partIndex,
		"reasoning_index":   reasoningIndex,
		"has_signature":     sig.HasRawSignature,
		"signature_length":  sig.RawSignatureLen,
		"detected_provider": string(sig.DetectedProvider),
	}).Debug("antigravity responses translator: normalized Claude reasoning encrypted_content before upstream")
}
