// Package gemini provides in-provider request normalization for Gemini API.
// It ensures incoming v1beta requests meet minimal schema requirements
// expected by Google's Generative Language API.
package gemini

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
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
		var toolItems [][]byte
		toolsChanged := false
		toolsResult.ForEach(func(_, toolResult gjson.Result) bool {
			tool := []byte(toolResult.Raw)
			toolChanged := false
			if declarations := toolResult.Get("functionDeclarations"); declarations.Exists() {
				tool, _ = sjson.SetRawBytes(tool, "function_declarations", []byte(declarations.Raw))
				tool, _ = sjson.DeleteBytes(tool, "functionDeclarations")
				toolChanged = true
			}

			declarations := gjson.GetBytes(tool, "function_declarations")
			if declarations.IsArray() {
				var declarationItems [][]byte
				declarationsChanged := false
				declarations.ForEach(func(_, declarationResult gjson.Result) bool {
					declaration := []byte(declarationResult.Raw)
					if parameters := declarationResult.Get("parameters"); parameters.Exists() {
						declaration, _ = sjson.SetRawBytes(declaration, "parametersJsonSchema", []byte(parameters.Raw))
						declaration, _ = sjson.DeleteBytes(declaration, "parameters")
						declarationsChanged = true
					}
					declarationItems = append(declarationItems, declaration)
					return true
				})
				if declarationsChanged {
					tool, _ = sjson.SetRawBytes(tool, "function_declarations", translatorcommon.JoinRawArray(declarationItems))
					toolChanged = true
				}
			}
			toolsChanged = toolsChanged || toolChanged
			toolItems = append(toolItems, tool)
			return true
		})
		if toolsChanged {
			rawJSON, _ = sjson.SetRawBytes(rawJSON, "tools", translatorcommon.JoinRawArray(toolItems))
		}
	}

	// Walk contents and fix roles
	out := rawJSON
	prevRole := ""
	if contents.IsArray() {
		rolesChanged := false
		contents.ForEach(func(_, value gjson.Result) bool {
			role := value.Get("role").String()
			if role != "user" && role != "model" {
				role = nextGeminiRole(prevRole)
				rolesChanged = true
			}
			prevRole = role
			return true
		})
		if rolesChanged {
			prevRole = ""
			contentItems := translatorcommon.NewRawArrayItems(contents.Get("#").Int())
			contents.ForEach(func(_, value gjson.Result) bool {
				role := value.Get("role").String()
				item := []byte(value.Raw)
				if role != "user" && role != "model" {
					role = nextGeminiRole(prevRole)
					item, _ = sjson.SetBytes(item, "role", role)
				}
				prevRole = role
				contentItems = append(contentItems, item)
				return true
			})
			out, _ = sjson.SetRawBytes(out, "contents", translatorcommon.JoinRawArray(contentItems))
		}
	} else {
		idx := 0
		contents.ForEach(func(_ gjson.Result, value gjson.Result) bool {
			role := value.Get("role").String()
			if role != "user" && role != "model" {
				role = nextGeminiRole(prevRole)
				out, _ = sjson.SetBytes(out, fmt.Sprintf("contents.%d.role", idx), role)
			}
			prevRole = role
			idx++
			return true
		})
	}

	out = signature.SanitizeGeminiRequestThoughtSignatures(out, "contents")

	if gjson.GetBytes(rawJSON, "generationConfig.responseSchema").Exists() {
		strJson, _ := util.RenameKey(string(out), "generationConfig.responseSchema", "generationConfig.responseJsonSchema")
		out = []byte(strJson)
	}

	// Backfill empty functionResponse.name from the preceding functionCall.name.
	// Some clients send function responses with empty names; the Gemini API rejects these.
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
	canBatch := contents.IsArray()
	if canBatch {
		contents.ForEach(func(_, content gjson.Result) bool {
			parts := content.Get("parts")
			if parts.Exists() && !parts.IsArray() {
				canBatch = false
				return false
			}
			return true
		})
	}
	if !canBatch {
		return backfillEmptyFunctionResponseNamesLegacy(data, contents)
	}
	needsBackfill, excessResponseIndexes := geminiFunctionResponseNamesNeedBackfill(contents)
	if !needsBackfill {
		for _, contentIndex := range excessResponseIndexes {
			log.Debugf("more function responses than calls at contents[%d], skipping name backfill", contentIndex)
		}
		return data
	}

	changed := false
	contentItems := translatorcommon.NewRawArrayItems(contents.Get("#").Int())
	var pendingCallNames []string

	contents.ForEach(func(contentIdx, content gjson.Result) bool {
		role := content.Get("role").String()
		contentRaw := []byte(content.Raw)

		// Collect functionCall names from model turns.
		if role == "model" {
			var names []string
			content.Get("parts").ForEach(func(_, part gjson.Result) bool {
				if part.Get("functionCall").Exists() {
					names = append(names, part.Get("functionCall.name").String())
				}
				return true
			})
			pendingCallNames = names
			contentItems = append(contentItems, contentRaw)
			return true
		}

		// Backfill empty functionResponse names from pending call names.
		if len(pendingCallNames) > 0 {
			responseIndex := 0
			partsChanged := false
			partItems := make([][]byte, 0, 4)
			content.Get("parts").ForEach(func(_, part gjson.Result) bool {
				partRaw := []byte(part.Raw)
				if part.Get("functionResponse").Exists() {
					name := part.Get("functionResponse.name").String()
					if strings.TrimSpace(name) == "" {
						if responseIndex < len(pendingCallNames) {
							partRaw, _ = sjson.SetBytes(partRaw, "functionResponse.name", pendingCallNames[responseIndex])
							partsChanged = true
						} else {
							log.Debugf("more function responses than calls at contents[%d], skipping name backfill", contentIdx.Int())
						}
					}
					responseIndex++
				}
				partItems = append(partItems, partRaw)
				return true
			})
			if partsChanged {
				contentRaw, _ = sjson.SetRawBytes(contentRaw, "parts", translatorcommon.JoinRawArray(partItems))
				changed = true
			}
			pendingCallNames = nil
		}

		contentItems = append(contentItems, contentRaw)
		return true
	})

	if !changed {
		return data
	}
	out, errSetContents := sjson.SetRawBytes(data, "contents", translatorcommon.JoinRawArray(contentItems))
	if errSetContents != nil {
		return data
	}
	return out
}

func geminiFunctionResponseNamesNeedBackfill(contents gjson.Result) (bool, []int64) {
	var pendingCallNames []string
	var excessResponseIndexes []int64
	needsBackfill := false
	contents.ForEach(func(contentIdx, content gjson.Result) bool {
		if content.Get("role").String() == "model" {
			var names []string
			content.Get("parts").ForEach(func(_, part gjson.Result) bool {
				if part.Get("functionCall").Exists() {
					names = append(names, part.Get("functionCall.name").String())
				}
				return true
			})
			pendingCallNames = names
			return true
		}
		if len(pendingCallNames) == 0 {
			return true
		}
		responseIndex := 0
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if part.Get("functionResponse").Exists() {
				if strings.TrimSpace(part.Get("functionResponse.name").String()) == "" {
					if responseIndex < len(pendingCallNames) {
						needsBackfill = true
						return false
					}
					excessResponseIndexes = append(excessResponseIndexes, contentIdx.Int())
				}
				responseIndex++
			}
			return true
		})
		pendingCallNames = nil
		return !needsBackfill
	})
	return needsBackfill, excessResponseIndexes
}

func backfillEmptyFunctionResponseNamesLegacy(data []byte, contents gjson.Result) []byte {
	out := data
	var pendingCallNames []string
	contents.ForEach(func(contentIdx, content gjson.Result) bool {
		if content.Get("role").String() == "model" {
			var names []string
			content.Get("parts").ForEach(func(_, part gjson.Result) bool {
				if part.Get("functionCall").Exists() {
					names = append(names, part.Get("functionCall.name").String())
				}
				return true
			})
			pendingCallNames = names
			return true
		}
		if len(pendingCallNames) > 0 {
			responseIndex := 0
			content.Get("parts").ForEach(func(partIdx, part gjson.Result) bool {
				if part.Get("functionResponse").Exists() {
					if strings.TrimSpace(part.Get("functionResponse.name").String()) == "" {
						if responseIndex < len(pendingCallNames) {
							path := fmt.Sprintf("contents.%d.parts.%d.functionResponse.name", contentIdx.Int(), partIdx.Int())
							out, _ = sjson.SetBytes(out, path, pendingCallNames[responseIndex])
						} else {
							log.Debugf("more function responses than calls at contents[%d], skipping name backfill", contentIdx.Int())
						}
					}
					responseIndex++
				}
				return true
			})
			pendingCallNames = nil
		}
		return true
	})
	return out
}

func nextGeminiRole(previousRole string) string {
	if previousRole == "" || previousRole == "model" {
		return "user"
	}
	return "model"
}
