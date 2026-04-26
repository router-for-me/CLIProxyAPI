// Package geminiCLI provides response translation functionality for OpenAI to Gemini API.
// This package handles the conversion of OpenAI Chat Completions API responses into Gemini API-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Gemini API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, and usage metadata appropriately.
package geminiCLI

import (
	"context"

	openaigemini "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/openai/gemini"
	translatorcommon "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/translatorcommon"
)

// ConvertOpenAIResponseToGeminiCLI converts OpenAI Chat Completions streaming response format to Gemini CLI API format.
func ConvertOpenAIResponseToGeminiCLI(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	outputs := openaigemini.ConvertOpenAIResponseToGemini(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
	newOutputs := make([][]byte, 0, len(outputs))
	for i := 0; i < len(outputs); i++ {
		newOutputs = append(newOutputs, translatorcommon.WrapGeminiCLIResponse(outputs[i]))
	}
	return newOutputs
}

// ConvertOpenAIResponseToGeminiCLINonStream converts a non-streaming OpenAI response to a non-streaming Gemini CLI response.
func ConvertOpenAIResponseToGeminiCLINonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	out := openaigemini.ConvertOpenAIResponseToGeminiNonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
	return translatorcommon.WrapGeminiCLIResponse(out)
}

// GeminiCLITokenCount returns Gemini CLI token-count JSON.
func GeminiCLITokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.GeminiTokenCountJSON(count)
}
