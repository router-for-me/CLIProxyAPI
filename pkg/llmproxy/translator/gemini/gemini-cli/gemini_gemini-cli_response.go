// Package gemini_cli provides response translation functionality for Gemini API to Gemini CLI API.
// This package handles the conversion of Gemini API responses into Gemini CLI-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Gemini CLI API clients.
package geminiCLI

import (
	"bytes"
	"context"

	translatorcommon "github.com/kooshapari/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/sjson"
)

var dataTag = []byte("data:")

// ConvertGeminiResponseToGeminiCLI converts Gemini streaming response format to Gemini CLI single-line JSON format.
// This function processes various Gemini event types and transforms them into Gemini CLI-compatible JSON responses.
// It handles thinking content, regular text content, and function calls, outputting single-line JSON
// that matches the Gemini CLI API response format.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON response from the Gemini API.
//   - param: A pointer to a parameter object for the conversion (unused).
//
// Returns:
//   - [][]byte: A slice of Gemini CLI-compatible JSON responses.
func ConvertGeminiResponseToGeminiCLI(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) [][]byte {
	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		return [][]byte{}
	}
	rawJSON, _ = sjson.SetRawBytes([]byte(`{"response":{}}`), "response", rawJSON)
	return [][]byte{rawJSON}
}

// ConvertGeminiResponseToGeminiCLINonStream converts a non-streaming Gemini response to a non-streaming Gemini CLI response.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON response from the Gemini API.
//   - param: A pointer to a parameter object for the conversion (unused).
//
// Returns:
//   - []byte: A Gemini CLI-compatible JSON response.
func ConvertGeminiResponseToGeminiCLINonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	rawJSON, _ = sjson.SetRawBytes([]byte(`{"response":{}}`), "response", rawJSON)
	return rawJSON
}

func GeminiCLITokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.GeminiTokenCountJSON(count)
}
