// Package gemini provides request translation functionality for Gemini to Gemini CLI API compatibility.
// It handles parsing and transforming Gemini API requests into Gemini CLI API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Gemini API format and Gemini CLI API's expected format.
package gemini

import (
	"bytes"
	"context"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertGeminiCliResponseToGemini parses and transforms a Gemini CLI API request into Gemini API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Gemini API.
// The function performs the following transformations:
// 1. Extracts the response data from the request
// 2. Handles alternative response formats
// 3. Processes array responses by extracting individual response objects
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model to use for the request (unused in current implementation)
//   - rawJSON: The raw JSON request data from the Gemini CLI API
//   - param: A pointer to a parameter object for the conversion (unused in current implementation)
//
// Returns:
//   - []string: The transformed request data in Gemini API format
func ConvertGeminiCliResponseToGemini(ctx context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []string {
	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}

	// Default to SSE format (alt="") if alt is not set in context
	alt, _ := ctx.Value("alt").(string)

	var chunk []byte
	if alt == "" {
		responseResult := gjson.GetBytes(rawJSON, "response")
		if responseResult.Exists() {
			chunk = []byte(responseResult.Raw)
		} else if gjson.GetBytes(rawJSON, "candidates").Exists() {
			// Fallback: if no "response" wrapper but has "candidates", use rawJSON directly
			chunk = rawJSON
		}
	} else {
		chunkTemplate := "[]"
		responseResult := gjson.ParseBytes(rawJSON)
		if responseResult.IsArray() {
			responseResultItems := responseResult.Array()
			for i := 0; i < len(responseResultItems); i++ {
				responseResultItem := responseResultItems[i]
				if responseResultItem.Get("response").Exists() {
					chunkTemplate, _ = sjson.SetRaw(chunkTemplate, "-1", responseResultItem.Get("response").Raw)
				} else if responseResultItem.Get("candidates").Exists() {
					// Fallback: if no "response" wrapper but has "candidates", use item directly
					chunkTemplate, _ = sjson.SetRaw(chunkTemplate, "-1", responseResultItem.Raw)
				}
			}
		}
		chunk = []byte(chunkTemplate)
	}

	// Return empty slice if no valid chunk was extracted
	if len(chunk) == 0 {
		return []string{}
	}
	return []string{string(chunk)}
}

// ConvertGeminiCliResponseToGeminiNonStream converts a non-streaming Gemini CLI request to a non-streaming Gemini response.
// This function processes the complete Gemini CLI request and transforms it into a single Gemini-compatible
// JSON response. It extracts the response data from the request and returns it in the expected format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON request data from the Gemini CLI API
//   - param: A pointer to a parameter object for the conversion (unused in current implementation)
//
// Returns:
//   - string: A Gemini-compatible JSON response containing the response data
func ConvertGeminiCliResponseToGeminiNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) string {
	responseResult := gjson.GetBytes(rawJSON, "response")
	if responseResult.Exists() {
		return responseResult.Raw
	}
	return string(rawJSON)
}

func GeminiTokenCount(ctx context.Context, count int64) string {
	return fmt.Sprintf(`{"totalTokens":%d,"promptTokensDetails":[{"modality":"TEXT","tokenCount":%d}]}`, count, count)
}
