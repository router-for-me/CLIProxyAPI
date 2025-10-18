// Package gemini provides in-provider request normalization for Gemini API.
// It ensures incoming v1beta requests meet minimal schema requirements
// expected by Google's Generative Language API.
package gemini

import (
	"bytes"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertGeminiRequestToGemini normalizes Gemini v1beta requests.
//   - Adds a default role for each content if missing or invalid.
//     The first message defaults to "user", then alternates user/model when needed.
//
// It keeps the payload otherwise unchanged.
func ConvertGeminiRequestToGemini(_ string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)
	// Fast path: if no contents field, return as-is
	contents := gjson.GetBytes(rawJSON, "contents")
	if !contents.Exists() {
		return rawJSON
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

	// Walk contents and fix roles
	out := rawJSON
	prevRole := ""
	idx := 0
	contents.ForEach(func(_ gjson.Result, value gjson.Result) bool {
		role := value.Get("role").String()

		// Only user/model are valid for Gemini v1beta requests
		valid := role == "user" || role == "model"
		if role == "" || !valid {
			var newRole string
			if prevRole == "" {
				newRole = "user"
			} else if prevRole == "user" {
				newRole = "model"
			} else {
				newRole = "user"
			}
			path := fmt.Sprintf("contents.%d.role", idx)
			out, _ = sjson.SetBytes(out, path, newRole)
			role = newRole
		}

		prevRole = role
		idx++
		return true
	})

	return out
}
