// Package gemini provides request translation functionality for Claude API.
// It handles parsing and transforming Claude API requests into the internal client format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package also performs JSON data cleaning and transformation to ensure compatibility
// between Claude API format and the internal client's expected format.
package geminiCLI

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// PrepareClaudeRequest parses and transforms a Claude API request into internal client format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the internal client.
func ConvertGeminiCLIRequestToGemini(_ string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON
	modelResult := gjson.GetBytes(rawJSON, "model")
	rawJSON = []byte(gjson.GetBytes(rawJSON, "request").Raw)
	rawJSON, _ = sjson.SetBytes(rawJSON, "model", modelResult.String())
	if gjson.GetBytes(rawJSON, "systemInstruction").Exists() {
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "system_instruction", []byte(gjson.GetBytes(rawJSON, "systemInstruction").Raw))
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "systemInstruction")
	}

	toolsResult := gjson.GetBytes(rawJSON, "tools")
	if toolsResult.Exists() && toolsResult.IsArray() {
		toolResults := toolsResult.Array()
		for i := 0; i < len(toolResults); i++ {
			functionDeclarationsResult := gjson.GetBytes(rawJSON, fmt.Sprintf("tools.%d.function_declarations", i))
			if functionDeclarationsResult.Exists() && functionDeclarationsResult.IsArray() {
				functionDeclarationsResults := functionDeclarationsResult.Array()
				for j := 0; j < len(functionDeclarationsResults); j++ {
					parametersResult := gjson.GetBytes(rawJSON, fmt.Sprintf("tools.%d.function_declarations.%d.parameters", i, j))
					if parametersResult.Exists() {
						strJson, _ := util.RenameKey(string(rawJSON), fmt.Sprintf("tools.%d.function_declarations.%d.parameters", i, j), fmt.Sprintf("tools.%d.function_declarations.%d.parametersJsonSchema", i, j))
						rawJSON = []byte(strJson)
					}
				}
			}
		}
	}

	gjson.GetBytes(rawJSON, "contents").ForEach(func(key, content gjson.Result) bool {
		if content.Get("role").String() == "model" {
			content.Get("parts").ForEach(func(partKey, part gjson.Result) bool {
				if part.Get("functionCall").Exists() {
					rawJSON, _ = sjson.SetBytes(rawJSON, fmt.Sprintf("contents.%d.parts.%d.thoughtSignature", key.Int(), partKey.Int()), "skip_thought_signature_validator")
				} else if part.Get("thoughtSignature").Exists() {
					rawJSON, _ = sjson.SetBytes(rawJSON, fmt.Sprintf("contents.%d.parts.%d.thoughtSignature", key.Int(), partKey.Int()), "skip_thought_signature_validator")
				}
				return true
			})
		}
		return true
	})

	return common.AttachDefaultSafetySettings(rawJSON, "safetySettings")
}
