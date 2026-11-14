package common

import (
	"strings"

	"github.com/MoeForge/moego/base64"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
)

// Gemini Claude carriers encode direction and semantic target on the client-facing wire.
const (
	GeminiClaudeCarrierPrefix     = "cpa-gemini-carrier-v1:"
	GeminiClaudeCarrierNext       = "next"
	GeminiClaudeCarrierPrevious   = "previous"
	GeminiClaudeCarrierStandalone = "standalone"
	GeminiClaudeCarrierText       = "text"
	GeminiClaudeCarrierFunction   = "function"
	GeminiClaudeCarrierAny        = "any"
)

// EncodeGeminiClaudeCarrierSignature marks the native part a Claude-facing signature belongs to.
func EncodeGeminiClaudeCarrierSignature(rawSignature, direction, targetKind string) string {
	rawSignature = strings.TrimSpace(rawSignature)
	if rawSignature == "" {
		return ""
	}
	return GeminiClaudeCarrierPrefix + direction + ":" + targetKind + ":" + base64.RawStdEncoding.EncodeToString([]byte(rawSignature))
}

// DecodeGeminiClaudeCarrierSignature validates and unwraps a marked carrier, or returns a legacy raw signature.
func DecodeGeminiClaudeCarrierSignature(rawSignature string) (signatureValue, direction, targetKind string, marked, ok bool) {
	rawSignature = strings.TrimSpace(rawSignature)
	if !strings.HasPrefix(rawSignature, GeminiClaudeCarrierPrefix) {
		return rawSignature, "", "", false, true
	}
	marked = true
	if len(rawSignature) > (signature.MaxGeminiThoughtSignatureLen*4/3)+1024 {
		return "", "", "", true, false
	}
	fields := strings.SplitN(strings.TrimPrefix(rawSignature, GeminiClaudeCarrierPrefix), ":", 3)
	if len(fields) != 3 {
		return "", "", "", true, false
	}
	direction, targetKind = fields[0], fields[1]
	switch direction {
	case GeminiClaudeCarrierNext, GeminiClaudeCarrierPrevious, GeminiClaudeCarrierStandalone:
	default:
		return "", "", "", true, false
	}
	switch targetKind {
	case GeminiClaudeCarrierText, GeminiClaudeCarrierFunction, GeminiClaudeCarrierAny:
	default:
		return "", "", "", true, false
	}
	decoded, errDecode := base64.RawStdEncoding.DecodeString(fields[2])
	if errDecode != nil || len(decoded) == 0 || strings.HasPrefix(string(decoded), GeminiClaudeCarrierPrefix) {
		return "", "", "", true, false
	}
	blockKind := signature.SignatureBlockKindGeminiModelPart
	if targetKind == GeminiClaudeCarrierFunction {
		blockKind = signature.SignatureBlockKindGeminiFunctionCall
	}
	normalized, compatible := signature.CompatibleSignatureForProviderBlock(signature.SignatureProviderGemini, string(decoded), blockKind)
	if !compatible || signature.IsGeminiThoughtSignatureBypass(signature.SignaturePayloadWithoutProviderPrefix(normalized)) {
		return "", "", "", true, false
	}
	return normalized, direction, targetKind, true, true
}
