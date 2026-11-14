// Package geminiCLI provides request translation functionality for Gemini to OpenAI API.
// It handles parsing and transforming Gemini API requests into OpenAI Chat Completions API format,
// extracting model information, generation config, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Gemini API format and OpenAI API's expected format.
package geminiCLI

import (
	"github.com/MoeForge/moego/ojson"
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/gemini"
)

// ConvertGeminiCLIRequestToOpenAI parses and transforms a Gemini API request into OpenAI Chat Completions API format.
// It extracts the model name, generation config, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the OpenAI API.
func ConvertGeminiCLIRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	rawJSON := inputRawJSON
	rawJSON = []byte(ojson.GetBytes(rawJSON, "request").Raw)
	rawJSON, _ = ojson.SetBytes(rawJSON, "model", modelName)
	if ojson.GetBytes(rawJSON, "systemInstruction").Exists() {
		rawJSON, _ = ojson.SetRawBytes(rawJSON, "system_instruction", []byte(ojson.GetBytes(rawJSON, "systemInstruction").Raw))
		rawJSON, _ = ojson.DeleteBytes(rawJSON, "systemInstruction")
	}

	return ConvertGeminiRequestToOpenAI(modelName, rawJSON, stream)
}
