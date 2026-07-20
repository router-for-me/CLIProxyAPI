package signature

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
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

	contents := util.GetGJSONBytesNoCopy(payload, contentsPath)
	if !contents.IsArray() || !geminiContentsThoughtSignaturesNeedSanitize(contents) {
		return payload
	}

	contentsChanged := false
	contentItems := make([][]byte, 0, int(contents.Get("#").Int()))
	contents.ForEach(func(contentIdx, content gjson.Result) bool {
		parts := content.Get("parts")
		if !parts.IsArray() {
			contentItems = append(contentItems, []byte(content.Raw))
			return true
		}

		isModelTurn := content.Get("role").String() == "model"
		partsChanged := false
		partItems := make([][]byte, 0, int(parts.Get("#").Int()))
		parts.ForEach(func(partIdx, part gjson.Result) bool {
			partJSON := []byte(part.Raw)
			if part.Get("functionResponse").Exists() {
				_, hadSignature := geminiPartThoughtSignature(part)
				if hadSignature {
					partJSON = deleteGeminiPartThoughtSignatureFields(partJSON)
					partsChanged = true
					logGeminiThoughtSignatureSanitize(contentsPath, int(contentIdx.Int()), int(partIdx.Int()), SignatureCompatibilityDecision{
						TargetProvider: SignatureProviderGemini,
						BlockKind:      SignatureBlockKindGeminiModelPart,
						Action:         SignatureActionDropSignature,
						Reason:         "user-turn functionResponse parts cannot replay thought signatures",
					}, "", true)
				}
				partItems = append(partItems, partJSON)
				return true
			}
			if !isModelTurn {
				partItems = append(partItems, partJSON)
				return true
			}

			hasFunctionCall := part.Get("functionCall").Exists()
			hasThought := part.Get("thought").Exists()
			rawSignature, hasSignature := geminiPartThoughtSignature(part)
			if !hasFunctionCall && !hasThought && !hasSignature {
				partItems = append(partItems, partJSON)
				return true
			}

			blockKind := SignatureBlockKindGeminiModelPart
			if hasFunctionCall {
				blockKind = SignatureBlockKindGeminiFunctionCall
			}
			decision := DecideSignatureCompatibility(SignatureProviderGemini, rawSignature, blockKind)
			replaySignature := GeminiReplaySignatureOrBypass(rawSignature, blockKind)
			if !hasNormalizedGeminiPartThoughtSignature(part, replaySignature) {
				partJSON = deleteGeminiPartThoughtSignatureFields(partJSON)
				partJSON, _ = sjson.SetBytes(partJSON, "thoughtSignature", replaySignature)
				partsChanged = true
			}
			if decision.Action != SignatureActionPreserve {
				logGeminiThoughtSignatureSanitize(contentsPath, int(contentIdx.Int()), int(partIdx.Int()), decision, rawSignature, hasSignature)
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
	return updated
}

func geminiContentsThoughtSignaturesNeedSanitize(contents gjson.Result) bool {
	needsSanitize := false
	contents.ForEach(func(_, content gjson.Result) bool {
		isModelTurn := content.Get("role").String() == "model"
		parts := content.Get("parts")
		if !parts.IsArray() {
			return true
		}
		parts.ForEach(func(_, part gjson.Result) bool {
			if part.Get("functionResponse").Exists() {
				_, needsSanitize = geminiPartThoughtSignature(part)
				return !needsSanitize
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
			replaySignature := GeminiReplaySignatureOrBypass(rawSignature, blockKind)
			needsSanitize = !hasNormalizedGeminiPartThoughtSignature(part, replaySignature)
			return !needsSanitize
		})
		return !needsSanitize
	})
	return needsSanitize
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
