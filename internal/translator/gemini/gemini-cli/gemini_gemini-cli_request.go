// Package gemini provides request translation functionality for Claude API.
// It handles parsing and transforming Claude API requests into the internal client format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package also performs JSON data cleaning and transformation to ensure compatibility
// between Claude API format and the internal client's expected format.
package geminiCLI

import (
	"fmt"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// PrepareClaudeRequest parses and transforms a Claude API request into internal client format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the internal client.
func ConvertGeminiCLIRequestToGemini(_ string, inputRawJSON []byte, _ bool) ([]byte, error) {
	rawJSON := inputRawJSON
	modelResult := ojson.GetBytes(rawJSON, "model")
	rawJSON = []byte(ojson.GetBytes(rawJSON, "request").Raw)
	rawJSON, _ = ojson.SetBytes(rawJSON, "model", modelResult.String())
	if ojson.GetBytes(rawJSON, "systemInstruction").Exists() {
		rawJSON, _ = ojson.SetRawBytes(rawJSON, "system_instruction", []byte(ojson.GetBytes(rawJSON, "systemInstruction").Raw))
		rawJSON, _ = ojson.DeleteBytes(rawJSON, "systemInstruction")
	}

	toolsResult := ojson.GetBytes(rawJSON, "tools")
	if toolsResult.Exists() && toolsResult.IsArray() {
		toolResults := toolsResult.Array()
		for i := 0; i < len(toolResults); i++ {
			functionDeclarationsResult := ojson.GetBytes(rawJSON, fmt.Sprintf("tools.%d.function_declarations", i))
			if functionDeclarationsResult.Exists() && functionDeclarationsResult.IsArray() {
				functionDeclarationsResults := functionDeclarationsResult.Array()
				for j := 0; j < len(functionDeclarationsResults); j++ {
					parametersResult := ojson.GetBytes(rawJSON, fmt.Sprintf("tools.%d.function_declarations.%d.parameters", i, j))
					if parametersResult.Exists() {
						strJson, _ := util.RenameKey(string(rawJSON), fmt.Sprintf("tools.%d.function_declarations.%d.parameters", i, j), fmt.Sprintf("tools.%d.function_declarations.%d.parametersJsonSchema", i, j))
						rawJSON = []byte(strJson)
					}
				}
			}
		}
	}

	rawJSON = signature.SanitizeGeminiRequestThoughtSignatures(rawJSON, "contents")

	return common.AttachDefaultSafetySettings(rawJSON, "safetySettings"), nil
}
