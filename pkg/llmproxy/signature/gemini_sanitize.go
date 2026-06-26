package signature

import (
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// GeminiReplaySignatureOrBypass returns a Gemini-replayable thoughtSignature.
// Compatible Gemini signatures are normalized and preserved. Missing, unknown,
// or cross-provider signatures are replaced with Gemini's bypass sentinel.
func GeminiReplaySignatureOrBypass(rawSignature string, blockKind SignatureBlockKind) string {
	if signature, ok := CompatibleSignatureForProviderBlock(SignatureProviderGemini, rawSignature, blockKind); ok {
		return signature
	}
	decision := DecideSignatureCompatibility(SignatureProviderGemini, rawSignature, blockKind)
	if decision.Action == SignatureActionReplaceWithGeminiBypass && decision.ReplacementSignature != "" {
		return decision.ReplacementSignature
	}
	return GeminiSkipThoughtSignatureValidator
}

// SanitizeGeminiRequestThoughtSignatures applies Gemini replay policy to a
// Gemini-shaped request. Model-turn functionCall, thought, and signed parts keep
// compatible Gemini signatures and use the bypass sentinel otherwise. User-turn
// functionResponse parts must not carry thoughtSignature fields.
func SanitizeGeminiRequestThoughtSignatures(payload []byte, contentsPath string) []byte {
	contentsPath = strings.TrimSpace(contentsPath)
	if contentsPath == "" {
		contentsPath = "contents"
	}

	contents := gjson.GetBytes(payload, contentsPath)
	if !contents.IsArray() {
		return payload
	}

	contents.ForEach(func(contentIdx, content gjson.Result) bool {
		isModelTurn := content.Get("role").String() == "model"
		parts := content.Get("parts")
		if !parts.IsArray() {
			return true
		}

		parts.ForEach(func(partIdx, part gjson.Result) bool {
			partPath := fmt.Sprintf("%s.%d.parts.%d", contentsPath, contentIdx.Int(), partIdx.Int())
			if part.Get("functionResponse").Exists() {
				_, hadSignature := geminiPartThoughtSignature(part)
				payload = deleteGeminiPartThoughtSignatureFields(payload, partPath)
				if hadSignature {
					logGeminiThoughtSignatureSanitize(contentsPath, int(contentIdx.Int()), int(partIdx.Int()), SignatureCompatibilityDecision{
						TargetProvider: SignatureProviderGemini,
						BlockKind:      SignatureBlockKindGeminiModelPart,
						Action:         SignatureActionDropSignature,
						Reason:         "user-turn functionResponse parts cannot replay thought signatures",
					}, "", true)
				}
				return true
			}
			if !isModelTurn {
				return true
			}

			hasFunctionCall := part.Get("functionCall").Exists()
			hasThought := part.Get("thought").Exists()
			rawSignature, hasSignature := geminiPartThoughtSignature(part)
			if !hasFunctionCall && !hasThought && !hasSignature {
				return true
			}

			blockKind := SignatureBlockKindGeminiModelPart
			if hasFunctionCall {
				blockKind = SignatureBlockKindGeminiFunctionCall
			}
			payload = deleteGeminiPartThoughtSignatureFields(payload, partPath)
			decision := DecideSignatureCompatibility(SignatureProviderGemini, rawSignature, blockKind)
			replaySignature := GeminiReplaySignatureOrBypass(rawSignature, blockKind)
			payload, _ = sjson.SetBytes(payload, partPath+".thoughtSignature", replaySignature)
			if decision.Action != SignatureActionPreserve {
				logGeminiThoughtSignatureSanitize(contentsPath, int(contentIdx.Int()), int(partIdx.Int()), decision, rawSignature, hasSignature)
			}
			return true
		})
		return true
	})

	return payload
}

func logGeminiThoughtSignatureSanitize(contentsPath string, contentIndex, partIndex int, decision SignatureCompatibilityDecision, rawSignature string, hasSignature bool) {
	log.WithFields(log.Fields{
		"component":         "signature_sanitizer",
		"target_provider":   string(SignatureProviderGemini),
		"action":            string(decision.Action),
		"reason":            decision.Reason,
		"contents_path":     contentsPath,
		"content_index":     contentIndex,
		"part_index":        partIndex,
		"block_kind":        string(decision.BlockKind),
		"detected_provider": string(decision.DetectedProvider),
		"has_signature":     hasSignature,
		"signature_length":  len(strings.TrimSpace(rawSignature)),
	}).Debug("gemini request: sanitized thoughtSignature before upstream")
}

func geminiPartThoughtSignature(part gjson.Result) (string, bool) {
	for _, path := range []string{
		"thoughtSignature",
		"thought_signature",
		"functionCall.thoughtSignature",
		"functionCall.thought_signature",
		"functionResponse.thoughtSignature",
		"functionResponse.thought_signature",
		"extra_content.google.thought_signature",
	} {
		result := part.Get(path)
		if result.Exists() {
			return result.String(), true
		}
	}
	return "", false
}

func deleteGeminiPartThoughtSignatureFields(payload []byte, partPath string) []byte {
	for _, path := range []string{
		"thoughtSignature",
		"thought_signature",
		"functionCall.thoughtSignature",
		"functionCall.thought_signature",
		"functionResponse.thoughtSignature",
		"functionResponse.thought_signature",
		"extra_content.google.thought_signature",
	} {
		payload, _ = sjson.DeleteBytes(payload, partPath+"."+path)
	}
	return payload
}
