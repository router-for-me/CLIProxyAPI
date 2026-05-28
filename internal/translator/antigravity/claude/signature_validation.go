// Claude thinking signature validation wrappers for Antigravity bypass mode.
package claude

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
)

const maxBypassSignatureLen = signature.MaxClaudeThinkingSignatureLen

type claudeSignatureTree = signature.ClaudeSignatureTree

// StripEmptySignatureThinkingBlocks removes thinking blocks whose signatures
// are empty or not valid Claude thinking signatures. These usually come from
// proxy-generated responses where no real Claude signature exists.
func StripEmptySignatureThinkingBlocks(payload []byte) []byte {
	return signature.StripInvalidClaudeThinkingBlocks(payload, signature.ClaudeSignatureValidationOptions{PrefixOnly: true})
}

func ValidateClaudeBypassSignatures(inputRawJSON []byte) error {
	return signature.ValidateClaudeThinkingSignatures(inputRawJSON, claudeBypassSignatureValidationOptions())
}

func normalizeClaudeBypassSignature(rawSignature string) (string, error) {
	return signature.NormalizeClaudeThinkingSignature(rawSignature, claudeBypassSignatureValidationOptions())
}

func inspectDoubleLayerSignature(sig string) (*claudeSignatureTree, error) {
	return signature.InspectClaudeDoubleLayerSignature(sig)
}

func inspectSingleLayerSignature(sig string) (*claudeSignatureTree, error) {
	return signature.InspectClaudeSingleLayerSignature(sig)
}

func inspectClaudeSignaturePayload(payload []byte, encodingLayers int) (*claudeSignatureTree, error) {
	return signature.InspectClaudeSignaturePayload(payload, encodingLayers)
}

func claudeBypassSignatureValidationOptions() signature.ClaudeSignatureValidationOptions {
	return signature.ClaudeSignatureValidationOptions{Strict: cache.SignatureBypassStrictMode()}
}
