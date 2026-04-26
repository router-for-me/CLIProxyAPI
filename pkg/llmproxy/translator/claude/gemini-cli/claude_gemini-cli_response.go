// Package geminiCLI provides response translation functionality for Claude Code to Gemini CLI API compatibility.
// This package handles the conversion of Claude Code API responses into Gemini CLI-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Gemini CLI API clients.
package geminiCLI

import (
	"context"

	claudegemini "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/claude/gemini"
	translatorcommon "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/translatorcommon"
)

// ConvertClaudeResponseToGeminiCLI converts Claude Code streaming response format to Gemini CLI format.
// Wraps each converted response in a {"response": ...} envelope to match the Gemini CLI API structure.
func ConvertClaudeResponseToGeminiCLI(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	outputs := claudegemini.ConvertClaudeResponseToGemini(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
	newOutputs := make([][]byte, 0, len(outputs))
	for i := 0; i < len(outputs); i++ {
		newOutputs = append(newOutputs, translatorcommon.WrapGeminiCLIResponse(outputs[i]))
	}
	return newOutputs
}

// ConvertClaudeResponseToGeminiCLINonStream converts a non-streaming Claude Code response to a non-streaming Gemini CLI response.
func ConvertClaudeResponseToGeminiCLINonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	out := claudegemini.ConvertClaudeResponseToGeminiNonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
	return translatorcommon.WrapGeminiCLIResponse(out)
}

// GeminiCLITokenCount returns the Gemini CLI token-count payload.
func GeminiCLITokenCount(ctx context.Context, count int64) []byte {
	return claudegemini.GeminiTokenCount(ctx, count)
}
