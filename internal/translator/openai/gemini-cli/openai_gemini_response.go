// Package geminiCLI provides response translation functionality for OpenAI to Gemini API.
// This package handles the conversion of OpenAI Chat Completions API responses into Gemini API-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Gemini API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, and usage metadata appropriately.
package geminiCLI

import (
	"context"
	"fmt"

	. "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/openai/gemini"
	"github.com/tidwall/sjson"
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
//   - []string: A slice of strings, each containing a Gemini-compatible JSON response.
func ConvertOpenAIResponseToGeminiCLI(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	outputs := ConvertOpenAIResponseToGemini(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
	newOutputs := make([]string, 0)
	for i := 0; i < len(outputs); i++ {
		json := `{"response": {}}`
		output, _ := sjson.SetRaw(json, "response", outputs[i])
		newOutputs = append(newOutputs, output)
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
//   - string: A Gemini-compatible JSON response.
func ConvertOpenAIResponseToGeminiCLINonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) string {
	strJSON := ConvertOpenAIResponseToGeminiNonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
	json := `{"response": {}}`
	strJSON, _ = sjson.SetRaw(json, "response", strJSON)
	return strJSON
}

func GeminiCLITokenCount(ctx context.Context, count int64) string {
	return fmt.Sprintf(`{"totalTokens":%d,"promptTokensDetails":[{"modality":"TEXT","tokenCount":%d}]}`, count, count)
}
