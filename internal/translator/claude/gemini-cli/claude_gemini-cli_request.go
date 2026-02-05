// Package geminiCLI provides request translation functionality for Gemini CLI to Claude Code API compatibility.
// It handles parsing and transforming Gemini CLI API requests into Claude Code API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Gemini CLI API format and Claude Code API's expected format.
package geminiCLI

import (
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/claude/gemini"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertGeminiCLIRequestToClaude parses and transforms a Gemini CLI API request into Claude Code API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Claude Code API.
// The function performs the following transformations:
// 1. Extracts the model information from the request
// 2. Restructures the JSON to match Claude Code API format
// 3. Converts system instructions to the expected format
// 4. Delegates to the Gemini-to-Claude conversion function for further processing
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the Gemini CLI API
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in Claude Code API format
func ConvertGeminiCLIRequestToClaude(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON

	modelResult := gjson.GetBytes(rawJSON, "model")
	// Extract the inner request object and promote it to the top level
	rawJSON = []byte(gjson.GetBytes(rawJSON, "request").Raw)
	// Restore the model information at the top level
	rawJSON, _ = sjson.SetBytes(rawJSON, "model", modelResult.String())
	// Convert systemInstruction field to system_instruction for Claude Code compatibility
	if gjson.GetBytes(rawJSON, "systemInstruction").Exists() {
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "system_instruction", []byte(gjson.GetBytes(rawJSON, "systemInstruction").Raw))
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "systemInstruction")
	}
	// Delegate to the Gemini-to-Claude conversion function for further processing
	return ConvertGeminiRequestToClaude(modelName, rawJSON, stream)
}
