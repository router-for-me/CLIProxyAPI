// Package gemini provides in-provider request normalization for Gemini API.
// It ensures incoming v1beta requests meet minimal schema requirements
// expected by Google's Generative Language API.
package gemini

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertGeminiRequestToGemini normalizes Gemini v1beta requests.
//   - Adds a default role for each content if missing or invalid.
//     The first message defaults to "user", then alternates user/model when needed.
//
// It keeps the payload otherwise unchanged.
func ConvertGeminiRequestToGemini(_ string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON
	// Fast path: if no contents field, only attach safety settings
	contents := gjson.GetBytes(rawJSON, "contents")
	if !contents.Exists() {
		return common.AttachDefaultSafetySettings(rawJSON, "safetySettings")
	}

	toolsResult := gjson.GetBytes(rawJSON, "tools")
	if toolsResult.Exists() && toolsResult.IsArray() {
		toolResults := toolsResult.Array()
		for i := 0; i < len(toolResults); i++ {
			if gjson.GetBytes(rawJSON, fmt.Sprintf("tools.%d.functionDeclarations", i)).Exists() {
				strJson, _ := util.RenameKey(string(rawJSON), fmt.Sprintf("tools.%d.functionDeclarations", i), fmt.Sprintf("tools.%d.function_declarations", i))
				rawJSON = []byte(strJson)
			}

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

	gjson.GetBytes(out, "contents").ForEach(func(key, content gjson.Result) bool {
		if content.Get("role").String() == "model" {
			content.Get("parts").ForEach(func(partKey, part gjson.Result) bool {
				if part.Get("functionCall").Exists() {
					out, _ = sjson.SetBytes(out, fmt.Sprintf("contents.%d.parts.%d.thoughtSignature", key.Int(), partKey.Int()), "skip_thought_signature_validator")
				} else if part.Get("thoughtSignature").Exists() {
					out, _ = sjson.SetBytes(out, fmt.Sprintf("contents.%d.parts.%d.thoughtSignature", key.Int(), partKey.Int()), "skip_thought_signature_validator")
				}
				return true
			})
		}
		return true
	})

	if gjson.GetBytes(rawJSON, "generationConfig.responseSchema").Exists() {
		strJson, _ := util.RenameKey(string(out), "generationConfig.responseSchema", "generationConfig.responseJsonSchema")
		out = []byte(strJson)
	}

	// Backfill empty functionResponse.name from the preceding functionCall.name.
	// Amp may send function responses with empty names; the Gemini API rejects these.
	out = backfillEmptyFunctionResponseNames(out)

	out = common.AttachDefaultSafetySettings(out, "safetySettings")
	return out
}

// backfillEmptyFunctionResponseNames walks the contents array and for each
// model turn containing functionCall parts, records the call names in order.
// For the immediately following user/function turn containing functionResponse
// parts, any empty name is replaced with the corresponding call name.
func backfillEmptyFunctionResponseNames(data []byte) []byte {
	contents := gjson.GetBytes(data, "contents")
	if !contents.Exists() {
		return data
	}

	out := data
	var pendingCallNames []string

	contents.ForEach(func(contentIdx, content gjson.Result) bool {
		role := content.Get("role").String()

		// Collect functionCall names from model turns
		if role == "model" {
			var names []string
			content.Get("parts").ForEach(func(_, part gjson.Result) bool {
				if part.Get("functionCall").Exists() {
					names = append(names, part.Get("functionCall.name").String())
				}
				return true
			})
			if len(names) > 0 {
				pendingCallNames = names
			} else {
				pendingCallNames = nil
			}
			return true
		}

		// Backfill empty functionResponse names from pending call names
		if len(pendingCallNames) > 0 {
			ri := 0
			content.Get("parts").ForEach(func(partIdx, part gjson.Result) bool {
				if part.Get("functionResponse").Exists() {
					name := part.Get("functionResponse.name").String()
					if strings.TrimSpace(name) == "" {
						if ri < len(pendingCallNames) {
							out, _ = sjson.SetBytes(out,
								fmt.Sprintf("contents.%d.parts.%d.functionResponse.name", contentIdx.Int(), partIdx.Int()),
								pendingCallNames[ri])
						} else {
							log.Debugf("more function responses than calls at contents[%d], skipping name backfill", contentIdx.Int())
						}
					}
					ri++
				}
				return true
			})
			pendingCallNames = nil
		}

		return true
	})

	return out
}
