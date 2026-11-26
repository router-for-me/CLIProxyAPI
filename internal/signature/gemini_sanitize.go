package signature

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	log "github.com/sirupsen/logrus"
)

// geminiSanitizeOptions scopes Gemini replay policy to the upstream endpoint.
type geminiSanitizeOptions struct {
	rejectBareTink bool
}

// GeminiReplaySignatureOrBypass returns a Gemini-replayable thoughtSignature.
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
// Gemini-shaped request.
func SanitizeGeminiRequestThoughtSignatures(payload []byte, contentsPath string) []byte {
	return sanitizeGeminiRequestThoughtSignatures(payload, contentsPath, geminiSanitizeOptions{})
}

// SanitizeGeminiRequestThoughtSignaturesDoc applies Gemini replay policy onto doc in place.
func SanitizeGeminiRequestThoughtSignaturesDoc(doc *ojson.Doc, contentsPath string) {
	_ = sanitizeGeminiRequestThoughtSignaturesDoc(doc, contentsPath, geminiSanitizeOptions{})
}

// SanitizeNonVertexGeminiRequestThoughtSignatures applies the same policy for
// Gemini endpoints other than Vertex AI (Antigravity, AI Studio, Gemini API).
func SanitizeNonVertexGeminiRequestThoughtSignatures(payload []byte, contentsPath string) []byte {
	return sanitizeGeminiRequestThoughtSignatures(payload, contentsPath, geminiSanitizeOptions{rejectBareTink: true})
}

// SanitizeNonVertexGeminiRequestThoughtSignaturesDoc applies non-Vertex Gemini
// signature sanitization onto doc in place.
func SanitizeNonVertexGeminiRequestThoughtSignaturesDoc(doc *ojson.Doc, contentsPath string) {
	_ = sanitizeGeminiRequestThoughtSignaturesDoc(doc, contentsPath, geminiSanitizeOptions{rejectBareTink: true})
}

// SanitizeGeminiSystemInstruction removes empty part objects ({}) from
// systemInstruction.parts (and system_instruction.parts) at the same envelope
// level as contentsPath.
func SanitizeGeminiSystemInstruction(payload []byte, contentsPath string) []byte {
	if len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !sanitizeGeminiSystemInstructionDoc(doc, contentsPath) {
		return payload
	}
	return doc.Bytes()
}

// SanitizeGeminiSystemInstructionDoc removes empty part objects ({}) from
// systemInstruction.parts (and system_instruction.parts) on doc in place.
func SanitizeGeminiSystemInstructionDoc(doc *ojson.Doc, contentsPath string) {
	_ = sanitizeGeminiSystemInstructionDoc(doc, contentsPath)
}

func sanitizeGeminiSystemInstructionDoc(doc *ojson.Doc, contentsPath string) bool {
	if doc == nil {
		return false
	}
	prefix := ""
	if trimmed := strings.TrimSpace(contentsPath); trimmed != "" {
		if idx := strings.LastIndex(trimmed, "."); idx > 0 {
			prefix = trimmed[:idx+1]
		}
	}
	modified := false
	for _, key := range []string{"systemInstruction", "system_instruction"} {
		sysPath := prefix + key
		sys := doc.Get(sysPath)
		if !sys.Exists() || !sys.IsObject() {
			continue
		}
		parts := sys.Get("parts")
		if !parts.IsArray() {
			continue
		}
		var emptyIndices []int
		totalParts := 0
		parts.ForEach(func(_, part ojson.Result) bool {
			if isEmptyGeminiPartObject(part) {
				emptyIndices = append(emptyIndices, totalParts)
			}
			totalParts++
			return true
		})
		if len(emptyIndices) == 0 {
			continue
		}
		if len(emptyIndices) == totalParts {
			if doc.Delete(sysPath) == nil {
				modified = true
			}
			continue
		}
		for i := len(emptyIndices) - 1; i >= 0; i-- {
			if doc.Delete(fmt.Sprintf("%s.parts.%d", sysPath, emptyIndices[i])) == nil {
				modified = true
			}
		}
	}
	return modified
}

func isEmptyGeminiPartObject(part ojson.Result) bool {
	if !part.IsObject() {
		return false
	}
	hasField := false
	part.ForEach(func(_, _ ojson.Result) bool {
		hasField = true
		return false
	})
	return !hasField
}

func sanitizeGeminiRequestThoughtSignatures(payload []byte, contentsPath string, opts geminiSanitizeOptions) []byte {
	if len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !sanitizeGeminiRequestThoughtSignaturesDoc(doc, contentsPath, opts) {
		return payload
	}
	return doc.Bytes()
}

func sanitizeGeminiRequestThoughtSignaturesDoc(doc *ojson.Doc, contentsPath string, opts geminiSanitizeOptions) bool {
	if doc == nil {
		return false
	}
	contentsPath = strings.TrimSpace(contentsPath)
	if contentsPath == "" {
		contentsPath = "contents"
	}
	modified := sanitizeGeminiSystemInstructionDoc(doc, contentsPath)

	contents := doc.Get(contentsPath)
	if !contents.IsArray() || !geminiContentsThoughtSignaturesNeedSanitize(contents, opts) {
		return modified
	}

	contentsChanged := false
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

	contentIdx := 0
	contents.ForEach(func(_, content ojson.Result) bool {
		cIdx := contentIdx
		contentIdx++
		parts := content.Get("parts")
		if !parts.IsArray() {
			return true
		}

		isModelTurn := content.Get("role").String() == "model"
		firstFunctionCallSeen := false
		partIdx := 0
		parts.ForEach(func(_, part ojson.Result) bool {
			pIdx := partIdx
			partIdx++
			partPath := fmt.Sprintf("%s.%d.parts.%d", contentsPath, cIdx, pIdx)
			rawSignature, hasSignature := geminiPartThoughtSignature(part)
			if geminiPartHasFunctionResponse(part) {
				if hasSignature {
					if deleteGeminiPartThoughtSignatureFieldsDoc(doc, partPath) {
						contentsChanged = true
					}
					countSanitize(SignatureCompatibilityDecision{
						TargetProvider:   SignatureProviderGemini,
						DetectedProvider: DetectSignatureProviderForBlock(rawSignature, SignatureBlockKindGeminiModelPart),
						BlockKind:        SignatureBlockKindGeminiModelPart,
						Action:           SignatureActionDropSignature,
						Reason:           "tool responses cannot carry signatures",
					})
				}
				return true
			}
			if !isModelTurn {
				return true
			}
			if part.Get("toolCall").Exists() || part.Get("tool_call").Exists() || part.Get("toolResponse").Exists() || part.Get("tool_response").Exists() {
				return true
			}

			hasFunctionCall := geminiPartHasFunctionCall(part)
			isFirstFunctionCall := hasFunctionCall && !firstFunctionCallSeen
			if hasFunctionCall || !part.Get("thought").Bool() {
				firstFunctionCallSeen = hasFunctionCall
			}
			if !hasFunctionCall && !hasSignature {
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
					deleteGeminiPartThoughtSignatureFieldsDoc(doc, partPath)
					_ = doc.Set(partPath+".thoughtSignature", replaySignature)
					partChanged = true
				}
			} else if hasSignature {
				if deleteGeminiPartThoughtSignatureFieldsDoc(doc, partPath) {
					partChanged = true
				}
			}
			if partChanged {
				contentsChanged = true
				if decision.Action != SignatureActionPreserve {
					countSanitize(decision)
				}
			}
			return true
		})
		return true
	})

	if !contentsChanged {
		return modified
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
	return true
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

func geminiContentsThoughtSignaturesNeedSanitize(contents ojson.Result, opts geminiSanitizeOptions) bool {
	needsSanitize := false
	contents.ForEach(func(_, content ojson.Result) bool {
		parts := content.Get("parts")
		if !parts.IsArray() {
			return true
		}
		isModelTurn := content.Get("role").String() == "model"
		firstFunctionCallSeen := false
		parts.ForEach(func(_, part ojson.Result) bool {
			rawSignature, hasSignature := geminiPartThoughtSignature(part)
			if geminiPartHasFunctionResponse(part) {
				needsSanitize = hasSignature
				return !needsSanitize
			}
			if !isModelTurn {
				return true
			}
			if part.Get("toolCall").Exists() || part.Get("tool_call").Exists() || part.Get("toolResponse").Exists() || part.Get("tool_response").Exists() {
				return true
			}
			hasFunctionCall := geminiPartHasFunctionCall(part)
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
	"function_call.thoughtSignature",
	"function_call.thought_signature",
	"functionResponse.thoughtSignature",
	"functionResponse.thought_signature",
	"function_response.thoughtSignature",
	"function_response.thought_signature",
	"extra_content.google.thought_signature",
}

func geminiPartHasFunctionCall(part ojson.Result) bool {
	return part.Get("functionCall").Exists() || part.Get("function_call").Exists()
}

func geminiPartHasFunctionResponse(part ojson.Result) bool {
	return part.Get("functionResponse").Exists() || part.Get("function_response").Exists()
}

func geminiPartThoughtSignature(part ojson.Result) (string, bool) {
	foundEmpty := false
	for _, path := range geminiPartThoughtSignaturePaths {
		if path == "extra_content.google.thought_signature" {
			if sig, exists := ExtraContentGoogleThoughtSignature(part); exists {
				if sig != "" {
					return sig, true
				}
				foundEmpty = true
			}
			continue
		}
		result := part.Get(path)
		if !result.Exists() {
			continue
		}
		if sig := result.String(); strings.TrimSpace(sig) != "" {
			return sig, true
		}
		foundEmpty = true
	}
	return "", foundEmpty
}

func hasNormalizedGeminiPartThoughtSignature(part ojson.Result, replaySignature string) bool {
	canonicalCount := 0
	part.ForEach(func(key, _ ojson.Result) bool {
		if key.String() == "thoughtSignature" {
			canonicalCount++
		}
		return true
	})
	canonical := part.Get("thoughtSignature")
	if canonicalCount != 1 || canonical.Type != ojson.String || canonical.String() != replaySignature {
		return false
	}
	for _, path := range geminiPartThoughtSignaturePaths[1:] {
		if path == "extra_content.google.thought_signature" {
			if _, exists := ExtraContentGoogleThoughtSignature(part); exists {
				return false
			}
			continue
		}
		if part.Get(path).Exists() {
			return false
		}
	}
	return true
}

func deleteGeminiPartThoughtSignatureFieldsDoc(doc *ojson.Doc, partPath string) bool {
	modified := false
	prefix := ""
	if partPath != "" {
		prefix = partPath + "."
	}
	for _, relPath := range geminiPartThoughtSignaturePaths {
		if relPath == "extra_content.google.thought_signature" {
			continue
		}
		path := prefix + relPath
		for doc.Get(path).Exists() {
			beforeLen := doc.Len()
			if errDelete := doc.Delete(path); errDelete != nil || doc.Len() >= beforeLen {
				break
			}
			modified = true
		}
	}
	if StripExtraContentGoogleThoughtSignatureDoc(doc, partPath) {
		modified = true
	}
	return modified
}

// ExtraContentGoogleThoughtSignature returns the first non-empty thought_signature
// found across all extra_content.google object occurrences in part, and reports
// whether any extra_content.google.thought_signature key exists.
func ExtraContentGoogleThoughtSignature(part ojson.Result) (string, bool) {
	if !part.IsObject() || !part.Get("extra_content").Exists() {
		return "", false
	}
	foundEmpty := false
	foundSig := ""
	part.ForEach(func(key, val ojson.Result) bool {
		if key.String() != "extra_content" || !val.IsObject() {
			return true
		}
		val.ForEach(func(ecKey, ecVal ojson.Result) bool {
			if ecKey.String() != "google" || !ecVal.IsObject() {
				return true
			}
			ecVal.ForEach(func(gKey, gVal ojson.Result) bool {
				if gKey.String() != "thought_signature" {
					return true
				}
				if sig := gVal.String(); strings.TrimSpace(sig) != "" {
					foundSig = sig
					return false
				}
				foundEmpty = true
				return true
			})
			return foundSig == ""
		})
		return foundSig == ""
	})
	if foundSig != "" {
		return foundSig, true
	}
	return "", foundEmpty
}

// StripExtraContentGoogleThoughtSignatureDoc removes every occurrence of
// extra_content.google.thought_signature from the part at partPath and prunes
// empty google and extra_content wrapper objects while preserving non-empty siblings.
func StripExtraContentGoogleThoughtSignatureDoc(doc *ojson.Doc, partPath string) bool {
	if doc == nil || partPath == "" {
		return false
	}
	rebuilt, changed := stripExtraContentGoogleThoughtSignaturePartRaw(doc.Get(partPath))
	if !changed {
		return false
	}
	return doc.SetRaw(partPath, rebuilt) == nil
}

// StripExtraContentGoogleThoughtSignaturePartDoc applies the same stripping and
// empty-wrapper pruning to a standalone part document.
func StripExtraContentGoogleThoughtSignaturePartDoc(doc *ojson.Doc) (*ojson.Doc, bool) {
	if doc == nil {
		return doc, false
	}
	rebuilt, changed := stripExtraContentGoogleThoughtSignaturePartRaw(ojson.ParseBytes(doc.Bytes()))
	if !changed {
		return doc, false
	}
	return ojson.NewDocString(rebuilt), true
}

func stripExtraContentGoogleThoughtSignaturePartRaw(part ojson.Result) (string, bool) {
	if !part.IsObject() || !part.Get("extra_content").Exists() {
		return "", false
	}
	var partPairs []string
	partChanged := false
	part.ForEach(func(key, val ojson.Result) bool {
		if key.String() != "extra_content" || !val.IsObject() {
			partPairs = append(partPairs, key.Raw+":"+val.Raw)
			return true
		}
		rebuiltExtra, keepExtra, extraChanged := stripExtraContentGoogleObject(val)
		if !keepExtra {
			partChanged = true
			return true
		}
		if extraChanged {
			partChanged = true
			partPairs = append(partPairs, key.Raw+":"+rebuiltExtra)
			return true
		}
		partPairs = append(partPairs, key.Raw+":"+val.Raw)
		return true
	})
	if !partChanged {
		return "", false
	}
	return "{" + strings.Join(partPairs, ",") + "}", true
}

func stripExtraContentGoogleObject(extra ojson.Result) (string, bool, bool) {
	var extraPairs []string
	extraChanged := false
	extra.ForEach(func(key, val ojson.Result) bool {
		if key.String() != "google" || !val.IsObject() {
			extraPairs = append(extraPairs, key.Raw+":"+val.Raw)
			return true
		}
		rebuiltGoogle, keepGoogle, googleChanged := stripGoogleThoughtSignatureObject(val)
		if !keepGoogle {
			extraChanged = true
			return true
		}
		if googleChanged {
			extraChanged = true
			extraPairs = append(extraPairs, key.Raw+":"+rebuiltGoogle)
			return true
		}
		extraPairs = append(extraPairs, key.Raw+":"+val.Raw)
		return true
	})
	if len(extraPairs) == 0 {
		return "", false, true
	}
	if !extraChanged {
		return "", true, false
	}
	return "{" + strings.Join(extraPairs, ",") + "}", true, true
}

func stripGoogleThoughtSignatureObject(google ojson.Result) (string, bool, bool) {
	var googlePairs []string
	googleChanged := false
	google.ForEach(func(key, val ojson.Result) bool {
		if key.String() == "thought_signature" {
			googleChanged = true
			return true
		}
		googlePairs = append(googlePairs, key.Raw+":"+val.Raw)
		return true
	})
	if len(googlePairs) == 0 {
		return "", false, true
	}
	if !googleChanged {
		return "", true, false
	}
	return "{" + strings.Join(googlePairs, ",") + "}", true, true
}
