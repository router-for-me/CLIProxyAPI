// Package geminiCLI provides response translation functionality for Claude Code to Gemini CLI API compatibility.
// This package handles the conversion of Claude Code API responses into Gemini CLI-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Gemini CLI API clients.
package geminiCLI

import (
	"context"

	. "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/claude/gemini"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/common"
)

// ConvertClaudeResponseToGeminiCLI converts Claude Code streaming response format to Gemini CLI format.
// This function processes various Claude Code event types and transforms them into Gemini-compatible JSON responses.
// It handles text content, tool calls, and usage metadata, outputting responses that match the Gemini CLI API format.
// The function wraps each converted response in a "response" object to match the Gemini CLI API structure.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Claude Code API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - [][]byte: A slice of Gemini-compatible JSON responses wrapped in a response object
func ConvertClaudeResponseToGeminiCLI(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	outputs := ConvertClaudeResponseToGemini(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
	// Wrap each converted response in a "response" object to match Gemini CLI API structure
	newOutputs := make([][]byte, 0, len(outputs))
	for i := 0; i < len(outputs); i++ {
		newOutputs = append(newOutputs, translatorcommon.WrapGeminiCLIResponse(outputs[i]))
	}
	return newOutputs
}

// ConvertClaudeResponseToGeminiCLINonStream converts a non-streaming Claude Code response to a non-streaming Gemini CLI response.
// This function processes the complete Claude Code response and transforms it into a single Gemini-compatible
// JSON response. It wraps the converted response in a "response" object to match the Gemini CLI API structure.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Claude Code API
//   - param: A pointer to a parameter object for the conversion
//
// Returns:
//   - []byte: A Gemini-compatible JSON response wrapped in a response object
func ConvertClaudeResponseToGeminiCLINonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	out := ConvertClaudeResponseToGeminiNonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
	// Wrap the converted response in a "response" object to match Gemini CLI API structure
	return translatorcommon.WrapGeminiCLIResponse(out)
}

func GeminiCLITokenCount(ctx context.Context, count int64) []byte {
	return GeminiTokenCount(ctx, count)
}
