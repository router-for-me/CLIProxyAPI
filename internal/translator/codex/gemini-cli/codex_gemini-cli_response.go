// Package geminiCLI provides response translation functionality for Codex to Gemini CLI API compatibility.
// This package handles the conversion of Codex API responses into Gemini CLI-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Gemini CLI API clients.
package geminiCLI

import (
	"context"

	. "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/codex/gemini"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/common"
)

// ConvertCodexResponseToGeminiCLI converts Codex streaming response format to Gemini CLI format.
// This function processes various Codex event types and transforms them into Gemini-compatible JSON responses.
// It handles text content, tool calls, and usage metadata, outputting responses that match the Gemini CLI API format.
// The function wraps each converted response in a "response" object to match the Gemini CLI API structure.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Codex API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - [][]byte: A slice of Gemini-compatible JSON responses wrapped in a response object
func ConvertCodexResponseToGeminiCLI(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	outputs := ConvertCodexResponseToGemini(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
	newOutputs := make([][]byte, 0, len(outputs))
	for i := 0; i < len(outputs); i++ {
		newOutputs = append(newOutputs, translatorcommon.WrapGeminiCLIResponse(outputs[i]))
	}
	return newOutputs
}

// ConvertCodexResponseToGeminiCLINonStream converts a non-streaming Codex response to a non-streaming Gemini CLI response.
// This function processes the complete Codex response and transforms it into a single Gemini-compatible
// JSON response. It wraps the converted response in a "response" object to match the Gemini CLI API structure.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Codex API
//   - param: A pointer to a parameter object for the conversion
//
// Returns:
//   - []byte: A Gemini-compatible JSON response wrapped in a response object
func ConvertCodexResponseToGeminiCLINonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	out := ConvertCodexResponseToGeminiNonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
	return translatorcommon.WrapGeminiCLIResponse(out)
}

func GeminiCLITokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.GeminiTokenCountJSON(count)
}
