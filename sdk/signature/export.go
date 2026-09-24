package signature

import (
	internalsig "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
)

type (
	ClaudeMessagesSignatureSanitizeOptions = internalsig.ClaudeMessagesSignatureSanitizeOptions
	SignatureSanitizeReport                = internalsig.SignatureSanitizeReport
	SignatureProvider                      = internalsig.SignatureProvider
)

const (
	SignatureProviderUnknown      = internalsig.SignatureProviderUnknown
	SignatureProviderClaude       = internalsig.SignatureProviderClaude
	SignatureProviderGemini       = internalsig.SignatureProviderGemini
	SignatureProviderGeminiBypass = internalsig.SignatureProviderGeminiBypass
	SignatureProviderGPT          = internalsig.SignatureProviderGPT
	SignatureProviderKimi         = internalsig.SignatureProviderKimi
	SignatureProviderGrok         = internalsig.SignatureProviderGrok
	SignatureProviderSWE          = internalsig.SignatureProviderSWE
)

func SanitizeClaudeMessagesSignaturesForModel(payload []byte, targetModel string) ([]byte, SignatureSanitizeReport) {
	return internalsig.SanitizeClaudeMessagesSignaturesForModel(payload, targetModel)
}

func SanitizeClaudeMessagesForClaudeUpstream(payload []byte, targetModel string, preserveEmptyThinkingBlocks ...bool) ([]byte, SignatureSanitizeReport) {
	return internalsig.SanitizeClaudeMessagesForClaudeUpstream(payload, targetModel, preserveEmptyThinkingBlocks...)
}

func SanitizeClaudeMessagesSignaturesForTarget(payload []byte, opts ClaudeMessagesSignatureSanitizeOptions) ([]byte, SignatureSanitizeReport) {
	return internalsig.SanitizeClaudeMessagesSignaturesForTarget(payload, opts)
}

func SignatureProviderFromModelName(model string) SignatureProvider {
	return internalsig.SignatureProviderFromModelName(model)
}
