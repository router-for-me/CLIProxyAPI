package common

import (
	"strings"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/tidwall/gjson"
)

// ResolveCodexReplayTargetModel returns the model name to use for reasoning
// signature family checks, falling back to the client-supplied "model" field in
// rawPayload when modelName is an unmapped upstream deployment alias for a
// Responses-native model family (Meta Muse, Grok, or GPT).
func ResolveCodexReplayTargetModel(modelName string, rawPayload []byte) string {
	if sigcompat.SignatureProviderFromModelName(thinking.ParseSuffix(modelName).ModelName) != sigcompat.SignatureProviderUnknown {
		return modelName
	}
	if len(rawPayload) > 0 {
		if rawModel := strings.TrimSpace(gjson.GetBytes(rawPayload, "model").String()); rawModel != "" {
			switch sigcompat.SignatureProviderFromModelName(thinking.ParseSuffix(rawModel).ModelName) {
			case sigcompat.SignatureProviderMeta, sigcompat.SignatureProviderGrok, sigcompat.SignatureProviderGPT:
				return rawModel
			}
		}
	}
	return modelName
}

// CodexReplaySignatureForTarget returns the exact value that may be written as a
// Responses reasoning encrypted_content for the Codex-format upstream serving
// modelName, or false when rawSignature must not replay there. Positively
// detected families must match the target family exactly (an unmapped target
// defaults to the Codex GPT family, mirroring the Codex translators' purpose)
// and go through the shared compatibility normalization, so cache-prefixed
// values such as "gpt#..." are stripped to their native payload instead of
// being forwarded verbatim. Grok has no detectable envelope, so it falls back
// to its shape inspector keyed on the target.
func CodexReplaySignatureForTarget(modelName, rawSignature string) (string, bool) {
	rawSignature = strings.TrimSpace(rawSignature)
	if rawSignature == "" {
		return "", false
	}
	target := sigcompat.SignatureProviderFromModelName(thinking.ParseSuffix(modelName).ModelName)
	detected := sigcompat.DetectSignatureProvider(rawSignature)
	if detected != sigcompat.SignatureProviderUnknown {
		effectiveTarget := target
		if effectiveTarget == sigcompat.SignatureProviderUnknown {
			effectiveTarget = sigcompat.SignatureProviderGPT
		}
		if detected != effectiveTarget {
			return "", false
		}
		normalized, ok := sigcompat.CompatibleSignatureForProvider(effectiveTarget, rawSignature)
		return normalized, ok && normalized != ""
	}
	if target == sigcompat.SignatureProviderGrok {
		if _, err := sigcompat.InspectGrokEncryptedContent(rawSignature); err == nil {
			return rawSignature, true
		}
	}
	return "", false
}
