// Package geminiCLI provides response translation functionality for OpenAI to Gemini API.
// This package handles the conversion of OpenAI Chat Completions API responses into Gemini API-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Gemini API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, and usage metadata appropriately.
package geminiCLI

import (
	"context"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/common"
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/openai/gemini"
)

// ConvertOpenAIResponseToGeminiCLI converts OpenAI Chat Completions streaming response format to Gemini API format.
// This function processes OpenAI streaming chunks and transforms them into Gemini-compatible JSON responses.
// It handles text content, tool calls, and usage metadata, outputting responses that match the Gemini API format.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON response from the OpenAI API.
//   - param: A pointer to a parameter object for the conversion.
//
// Returns:
//   - [][]byte: A slice of Gemini-compatible JSON responses.
func ConvertOpenAIResponseToGeminiCLI(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	outputs := ConvertOpenAIResponseToGemini(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
	newOutputs := make([][]byte, 0, len(outputs))
	for i := 0; i < len(outputs); i++ {
		newOutputs = append(newOutputs, translatorcommon.WrapGeminiCLIResponse(outputs[i]))
	}
	return newOutputs
}

// ConvertOpenAIResponseToGeminiCLINonStream converts a non-streaming OpenAI response to a non-streaming Gemini CLI response.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON response from the OpenAI API.
//   - param: A pointer to a parameter object for the conversion.
//
// Returns:
//   - []byte: A Gemini-compatible JSON response.
func ConvertOpenAIResponseToGeminiCLINonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	out := ConvertOpenAIResponseToGeminiNonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
	return translatorcommon.WrapGeminiCLIResponse(out)
}

func GeminiCLITokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.GeminiTokenCountJSON(count)
}
