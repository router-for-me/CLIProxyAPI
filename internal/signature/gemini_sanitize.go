package signature

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// geminiSanitizeOptions scopes Gemini replay policy to the upstream endpoint.
type geminiSanitizeOptions struct {
	// rejectBareTink treats Vertex bare-Tink signatures as incompatible. Live
	// replay on 2026-10-26 showed Antigravity (cloudcode-pa) and AI Studio
	// (generativelanguage) reject them with 400 "Corrupted thought signature",
	// while they are the native form for Vertex AI.
	rejectBareTink bool
}

// GeminiReplaySignatureOrBypass returns a Gemini-replayable thoughtSignature.
// Compatible Gemini signatures are normalized and preserved. Missing, unknown,
// or cross-provider signatures are replaced with Gemini's bypass sentinel.
func GeminiReplaySignatureOrBypass(rawSignature string, blockKind SignatureBlockKind) string {
	return geminiReplaySignatureOrBypass(rawSignature, blockKind, geminiSanitizeOptions{})
}

func geminiReplaySignatureOrBypass(rawSignature string, blockKind SignatureBlockKind, opts geminiSanitizeOptions) string {
	decision := geminiSanitizeDecision(rawSignature, blockKind, opts)
	if decision.Compatible && decision.NormalizedSignature != "" {
		return decision.NormalizedSignature
	}
	if decision.Action == SignatureActionReplaceWithGeminiBypass && decision.ReplacementSignature != "" {
		return decision.ReplacementSignature
	}
	return GeminiSkipThoughtSignatureValidator
}

// geminiSanitizeDecision applies endpoint scoping on top of the family-level
// compatibility decision.
func geminiSanitizeDecision(rawSignature string, blockKind SignatureBlockKind, opts geminiSanitizeOptions) SignatureCompatibilityDecision {
	decision := DecideSignatureCompatibility(SignatureProviderGemini, rawSignature, blockKind)
	if !opts.rejectBareTink || !decision.Compatible || !IsGeminiVertexBareTinkSignature(rawSignature) {
		return decision
	}
	decision.Compatible = false
	decision.NormalizedSignature = ""
	decision.Action = SignatureActionReplaceWithGeminiBypass
	decision.ReplacementSignature = GeminiSkipThoughtSignatureValidator
	decision.Reason = "Vertex bare-Tink signature is not accepted by this Gemini endpoint"
	return decision
}

// SanitizeGeminiRequestThoughtSignatures applies Gemini replay policy to a
// Gemini-shaped request. Existing provider signatures stay on their original
// model parts. Server-side tool blocks (toolCall and toolResponse) are echoed back
// untouched per the Gemini API contract. Only a missing or incompatible first
// functionCall in each parallel-call group (at the start of a model turn or after
// a non-thought part) gets the bypass sentinel; unsigned sibling calls remain
// unsigned, matching native Gemini parallel-call history. functionResponse parts
// never carry signatures.
//
// Vertex bare-Tink signatures are preserved here because the target endpoint is
// not known at this layer; executors for other Gemini endpoints must also call
// SanitizeNonVertexGeminiRequestThoughtSignatures before sending.
func SanitizeGeminiRequestThoughtSignatures(payload []byte, contentsPath string) []byte {
	return sanitizeGeminiRequestThoughtSignatures(payload, contentsPath, geminiSanitizeOptions{})
}

// SanitizeNonVertexGeminiRequestThoughtSignatures applies the same policy for
// Gemini endpoints other than Vertex AI (Antigravity, AI Studio, Gemini API).
// Vertex bare-Tink signatures are treated as incompatible: the first
// functionCall in a group gets the bypass sentinel and other parts drop it.
func SanitizeNonVertexGeminiRequestThoughtSignatures(payload []byte, contentsPath string) []byte {
	return sanitizeGeminiRequestThoughtSignatures(payload, contentsPath, geminiSanitizeOptions{rejectBareTink: true})
}

func sanitizeGeminiRequestThoughtSignatures(payload []byte, contentsPath string, opts geminiSanitizeOptions) []byte {
	contentsPath = strings.TrimSpace(contentsPath)
	if contentsPath == "" {
		contentsPath = "contents"
	}

	contents := util.GetGJSONBytesNoCopy(payload, contentsPath)
	if !contents.IsArray() || !geminiContentsThoughtSignaturesNeedSanitize(contents, opts) {
		return payload
	}

	contentsChanged := false
	contentItems := make([][]byte, 0, int(contents.Get("#").Int()))
	type sanitizeLogKey struct {
		action           SignatureCompatibilityAction
		reason           string
		blockKind        SignatureBlockKind
		detectedProvider SignatureProvider
	}
	sanitizeCounts := make(map[sanitizeLogKey]int)
	var sanitizeOrder []sanitizeLogKey
	countSanitize := func(decision SignatureCompatibilityDecision) {
		key := sanitizeLogKey{
			action:           decision.Action,
			reason:           decision.Reason,
			blockKind:        decision.BlockKind,
			detectedProvider: decision.DetectedProvider,
		}
		if sanitizeCounts[key] == 0 {
			sanitizeOrder = append(sanitizeOrder, key)
		}
		sanitizeCounts[key]++
	}

	contents.ForEach(func(_, content gjson.Result) bool {
		parts := content.Get("parts")
		if !parts.IsArray() {
			contentItems = append(contentItems, []byte(content.Raw))
			return true
		}

		isModelTurn := content.Get("role").String() == "model"
		firstFunctionCallSeen := false
		partsChanged := false
		partItems := make([][]byte, 0, int(parts.Get("#").Int()))
		parts.ForEach(func(_, part gjson.Result) bool {
			partJSON := []byte(part.Raw)
			rawSignature, hasSignature := geminiPartThoughtSignature(part)
			if part.Get("functionResponse").Exists() {
				if hasSignature {
					partJSON = deleteGeminiPartThoughtSignatureFields(partJSON)
					partsChanged = true
					countSanitize(SignatureCompatibilityDecision{
						TargetProvider:   SignatureProviderGemini,
						DetectedProvider: DetectSignatureProviderForBlock(rawSignature, SignatureBlockKindGeminiModelPart),
						BlockKind:        SignatureBlockKindGeminiModelPart,
						Action:           SignatureActionDropSignature,
						Reason:           "tool responses cannot carry signatures",
					})
				}
				partItems = append(partItems, partJSON)
				return true
			}
			if !isModelTurn {
				partItems = append(partItems, partJSON)
				return true
			}
			if part.Get("toolCall").Exists() || part.Get("tool_call").Exists() || part.Get("toolResponse").Exists() || part.Get("tool_response").Exists() {
				partItems = append(partItems, partJSON)
				return true
			}

			hasFunctionCall := part.Get("functionCall").Exists()
			isFirstFunctionCall := hasFunctionCall && !firstFunctionCallSeen
			// A text part, even an empty signature carrier, ends the parallel group:
			// the Gemini API rejects an unsigned functionCall after it. Thought parts do not.
			if hasFunctionCall || !part.Get("thought").Bool() {
				firstFunctionCallSeen = hasFunctionCall
			}
			if !hasFunctionCall && !hasSignature {
				partItems = append(partItems, partJSON)
				return true
			}

			blockKind := SignatureBlockKindGeminiModelPart
			if hasFunctionCall {
				blockKind = SignatureBlockKindGeminiFunctionCall
			}
			decision := geminiSanitizeDecision(rawSignature, blockKind, opts)
			replaySignature := ""
			switch {
			case isFirstFunctionCall:
				replaySignature = geminiReplaySignatureOrBypass(rawSignature, blockKind, opts)
				if decision.Action == SignatureActionReplaceWithGeminiBypass {
					decision.Reason = "missing or incompatible signature"
				}
			case hasSignature && decision.Action == SignatureActionPreserve && !IsGeminiThoughtSignatureBypass(SignaturePayloadWithoutProviderPrefix(rawSignature)):
				replaySignature = decision.NormalizedSignature
			case hasSignature:
				decision.Action = SignatureActionDropSignature
				decision.ReplacementSignature = ""
				if hasFunctionCall {
					decision.Reason = "sibling calls must be unsigned"
				} else {
					decision.Reason = "text parts cannot carry signatures"
				}
			}

			partChanged := false
			if replaySignature != "" {
				if !hasNormalizedGeminiPartThoughtSignature(part, replaySignature) {
					partJSON = deleteGeminiPartThoughtSignatureFields(partJSON)
					partJSON, _ = sjson.SetBytes(partJSON, "thoughtSignature", replaySignature)
					partChanged = true
				}
			} else if hasSignature {
				partJSON = deleteGeminiPartThoughtSignatureFields(partJSON)
				partChanged = true
			}
			if partChanged {
				partsChanged = true
				if decision.Action != SignatureActionPreserve {
					countSanitize(decision)
				}
			}
			partItems = append(partItems, partJSON)
			return true
		})

		contentJSON := []byte(content.Raw)
		if partsChanged {
			contentJSON, _ = sjson.SetRawBytes(contentJSON, "parts", joinGeminiSignatureRawArray(partItems))
			contentsChanged = true
		}
		contentItems = append(contentItems, contentJSON)
		return true
	})

	if !contentsChanged {
		return payload
	}
	updated, errSet := sjson.SetRawBytes(payload, contentsPath, joinGeminiSignatureRawArray(contentItems))
	if errSet != nil {
		return payload
	}
	for _, key := range sanitizeOrder {
		count := sanitizeCounts[key]
		noun := "thoughtSignature"
		if count > 1 {
			noun = "thoughtSignatures"
		}
		log.Debugf("gemini request: sanitized %d %s action=%s part=%s sig_type=%s path=%s reason=%q",
			count,
			noun,
			sanitizeActionName(key.action),
			sanitizeKindName(key.blockKind),
			sanitizeSigTypeName(key.detectedProvider),
			contentsPath,
			key.reason)
	}
	return updated
}

func sanitizeActionName(action SignatureCompatibilityAction) string {
	switch action {
	case SignatureActionDropSignature:
		return "drop"
	case SignatureActionReplaceWithGeminiBypass:
		return "replace_bypass"
	case SignatureActionDropBlock:
		return "drop_block"
	default:
		return string(action)
	}
}

func sanitizeKindName(kind SignatureBlockKind) string {
	switch kind {
	case SignatureBlockKindGeminiFunctionCall:
		return "function_call"
	case SignatureBlockKindGeminiModelPart:
		return "model_part"
	default:
		return strings.TrimPrefix(string(kind), "gemini_")
	}
}

func sanitizeSigTypeName(provider SignatureProvider) string {
	switch provider {
	case SignatureProviderGeminiBypass:
		return "bypass"
	default:
		return string(provider)
	}
}

func geminiContentsThoughtSignaturesNeedSanitize(contents gjson.Result, opts geminiSanitizeOptions) bool {
	needsSanitize := false
	contents.ForEach(func(_, content gjson.Result) bool {
		parts := content.Get("parts")
		if !parts.IsArray() {
			return true
		}
		isModelTurn := content.Get("role").String() == "model"
		firstFunctionCallSeen := false
		parts.ForEach(func(_, part gjson.Result) bool {
			rawSignature, hasSignature := geminiPartThoughtSignature(part)
			if part.Get("functionResponse").Exists() {
				needsSanitize = hasSignature
				return !needsSanitize
			}
			if !isModelTurn {
				return true
			}
			if part.Get("toolCall").Exists() || part.Get("tool_call").Exists() || part.Get("toolResponse").Exists() || part.Get("tool_response").Exists() {
				return true
			}
			hasFunctionCall := part.Get("functionCall").Exists()
			isFirstFunctionCall := hasFunctionCall && !firstFunctionCallSeen
			if hasFunctionCall || !part.Get("thought").Bool() {
				firstFunctionCallSeen = hasFunctionCall
			}
			if isFirstFunctionCall {
				replaySignature := geminiReplaySignatureOrBypass(rawSignature, SignatureBlockKindGeminiFunctionCall, opts)
				needsSanitize = !hasNormalizedGeminiPartThoughtSignature(part, replaySignature)
				return !needsSanitize
			}
			if !hasSignature {
				return true
			}
			blockKind := SignatureBlockKindGeminiModelPart
			if hasFunctionCall {
				blockKind = SignatureBlockKindGeminiFunctionCall
			}
			decision := geminiSanitizeDecision(rawSignature, blockKind, opts)
			if decision.Action != SignatureActionPreserve || IsGeminiThoughtSignatureBypass(SignaturePayloadWithoutProviderPrefix(rawSignature)) {
				needsSanitize = true
				return false
			}
			needsSanitize = !hasNormalizedGeminiPartThoughtSignature(part, decision.NormalizedSignature)
			return !needsSanitize
		})
		return !needsSanitize
	})
	return needsSanitize
}

var geminiPartThoughtSignaturePaths = []string{
	"thoughtSignature",
	"thought_signature",
	"functionCall.thoughtSignature",
	"functionCall.thought_signature",
	"functionResponse.thoughtSignature",
	"functionResponse.thought_signature",
	"extra_content.google.thought_signature",
}

func geminiPartThoughtSignature(part gjson.Result) (string, bool) {
	for _, path := range geminiPartThoughtSignaturePaths {
		result := part.Get(path)
		if result.Exists() {
			return result.String(), true
		}
	}
	return "", false
}

func hasNormalizedGeminiPartThoughtSignature(part gjson.Result, replaySignature string) bool {
	canonicalCount := 0
	part.ForEach(func(key, _ gjson.Result) bool {
		if key.String() == "thoughtSignature" {
			canonicalCount++
		}
		return true
	})
	canonical := part.Get("thoughtSignature")
	if canonicalCount != 1 || canonical.Type != gjson.String || canonical.String() != replaySignature {
		return false
	}
	for _, path := range geminiPartThoughtSignaturePaths[1:] {
		if part.Get(path).Exists() {
			return false
		}
	}
	return true
}

func deleteGeminiPartThoughtSignatureFields(payload []byte) []byte {
	for _, path := range geminiPartThoughtSignaturePaths {
		for gjson.GetBytes(payload, path).Exists() {
			updated, errDelete := sjson.DeleteBytes(payload, path)
			if errDelete != nil || len(updated) >= len(payload) {
				break
			}
			payload = updated
		}
	}
	return payload
}

func joinGeminiSignatureRawArray(items [][]byte) []byte {
	size := len(items) + 1
	for _, item := range items {
		size += len(item)
	}
	out := make([]byte, 0, size)
	out = append(out, '[')
	for index, item := range items {
		if index > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}
