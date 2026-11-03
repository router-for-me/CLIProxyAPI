// Package gemini provides in-provider request normalization for Gemini API.
// It ensures incoming v1beta requests meet minimal schema requirements
// expected by Google's Generative Language API.
package gemini

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

// ConvertGeminiRequestToGemini normalizes Gemini v1beta requests.
//   - Adds a default role for each content if missing or invalid.
//     The first message defaults to "user", then alternates user/model when needed.
//
// It keeps the payload otherwise unchanged.
func ConvertGeminiRequestToGemini(_ string, inputRawJSON []byte, _ bool) ([]byte, error) {
	rawJSON := inputRawJSON
	// Fast path: if no contents field, only attach safety settings
	contents := util.GetGJSONBytesNoCopy(rawJSON, "contents")
	if !contents.Exists() {
		return common.AttachDefaultSafetySettings(rawJSON, "safetySettings"), nil
	}

	toolsResult := ojson.GetBytesNoCopy(rawJSON, "tools")
	if toolsResult.Exists() && toolsResult.IsArray() {
		var toolItems [][]byte
		toolsChanged := false
		toolsResult.ForEach(func(_, toolResult ojson.Result) bool {
			var toolDoc *ojson.Doc
			ensureToolDoc := func() *ojson.Doc {
				if toolDoc == nil {
					toolDoc = ojson.NewDocString(toolResult.Raw)
				}
				return toolDoc
			}
			declarations := toolResult.Get("function_declarations")
			if camelDecls := toolResult.Get("functionDeclarations"); camelDecls.Exists() {
				d := ensureToolDoc()
				_ = d.SetRaw("function_declarations", camelDecls.Raw)
				_ = d.Delete("functionDeclarations")
				declarations = camelDecls
			}

			if declarations.IsArray() {
				var declarationItems [][]byte
				declarationsChanged := false
				declarations.ForEach(func(_, declarationResult ojson.Result) bool {
					declaration := []byte(declarationResult.Raw)
					if parameters := declarationResult.Get("parameters"); parameters.Exists() {
						normalizedParams := util.NormalizeJSONSchemaNumericConstraints(parameters.Raw)
						declDoc := ojson.NewDocString(declarationResult.Raw)
						_ = declDoc.SetRaw("parametersJsonSchema", normalizedParams)
						_ = declDoc.Delete("parameters")
						declaration = declDoc.Bytes()
						declarationsChanged = true
					} else if parametersJSONSchema := declarationResult.Get("parametersJsonSchema"); parametersJSONSchema.Exists() {
						if normalizedParams := util.NormalizeJSONSchemaNumericConstraints(parametersJSONSchema.Raw); normalizedParams != parametersJSONSchema.Raw {
							declaration, _ = ojson.SetRawBytes(declaration, "parametersJsonSchema", []byte(normalizedParams))
							declarationsChanged = true
						}
					}
					declarationItems = append(declarationItems, declaration)
					return true
				})
				if declarationsChanged {
					_ = ensureToolDoc().SetRawBytes("function_declarations", translatorcommon.JoinRawArray(declarationItems))
				}
			}
			if toolDoc != nil {
				toolsChanged = true
				toolItems = append(toolItems, toolDoc.Bytes())
			} else {
				toolItems = append(toolItems, []byte(toolResult.Raw))
			}
			return true
		})
		if toolsChanged {
			rawJSON, _ = ojson.SetRawBytes(rawJSON, "tools", translatorcommon.JoinRawArray(toolItems))
		}
	}

	// Walk contents and fix roles
	out := rawJSON
	prevRole := ""
	if contents.IsArray() {
		rolesChanged := false
		contents.ForEach(func(_, value ojson.Result) bool {
			role := value.Get("role").String()
			if role != "user" && role != "model" {
				if translatorcommon.ContentHasGeminiFunctionResponse([]byte(value.Raw)) {
					role = "user"
				} else {
					role = nextGeminiRole(prevRole)
				}
				rolesChanged = true
			}
			prevRole = role
			return true
		})
		if rolesChanged {
			prevRole = ""
			contentItems := translatorcommon.NewRawArrayItems(contents.Get("#").Int())
			contents.ForEach(func(_, value ojson.Result) bool {
				role := value.Get("role").String()
				item := []byte(value.Raw)
				if role != "user" && role != "model" {
					if translatorcommon.ContentHasGeminiFunctionResponse([]byte(value.Raw)) {
						role = "user"
					} else {
						role = nextGeminiRole(prevRole)
					}
					item, _ = ojson.SetBytes(item, "role", role)
				}
				prevRole = role
				contentItems = append(contentItems, item)
				return true
			})
			out, _ = ojson.SetRawBytes(out, "contents", translatorcommon.JoinRawArray(contentItems))
		}
	} else {
		idx := 0
		var outDoc *ojson.Doc
		contents.ForEach(func(_ ojson.Result, value ojson.Result) bool {
			role := value.Get("role").String()
			if role != "user" && role != "model" {
				if translatorcommon.ContentHasGeminiFunctionResponse([]byte(value.Raw)) {
					role = "user"
				} else {
					role = nextGeminiRole(prevRole)
				}
				if outDoc == nil {
					outDoc = ojson.NewDoc(out)
				}
				_ = outDoc.Set(fmt.Sprintf("contents.%d.role", idx), role)
			}
			prevRole = role
			idx++
			return true
		})
		if outDoc != nil {
			out = outDoc.Bytes()
		}
	}

	out = signature.SanitizeGeminiRequestThoughtSignatures(out, "contents")

	if responseSchema := ojson.GetBytesNoCopy(out, "generationConfig.responseSchema"); responseSchema.Exists() {
		normalizedSchema := util.NormalizeJSONSchemaNumericConstraints(responseSchema.Raw)
		outDoc := ojson.NewDoc(out)
		_ = outDoc.SetRaw("generationConfig.responseJsonSchema", normalizedSchema)
		_ = outDoc.Delete("generationConfig.responseSchema")
		out = outDoc.Bytes()
	} else if responseJSONSchema := ojson.GetBytesNoCopy(out, "generationConfig.responseJsonSchema"); responseJSONSchema.Exists() {
		if normalizedSchema := util.NormalizeJSONSchemaNumericConstraints(responseJSONSchema.Raw); normalizedSchema != responseJSONSchema.Raw {
			out, _ = ojson.SetRawBytes(out, "generationConfig.responseJsonSchema", []byte(normalizedSchema))
		}
	}

	// Backfill empty functionResponse.name from the preceding functionCall.name.
	// Some clients send function responses with empty names; the Gemini API rejects these.
	out = backfillEmptyFunctionResponseNames(out)

	out = common.AttachDefaultSafetySettings(out, "safetySettings")
	return out, nil
}

// backfillEmptyFunctionResponseNames walks the contents array and for each
// model turn containing functionCall parts, records the call names in order.
// For the immediately following user/function turn containing functionResponse
// parts, any empty name is replaced with the corresponding call name.
func backfillEmptyFunctionResponseNames(data []byte) []byte {
	contents := util.GetGJSONBytesNoCopy(data, "contents")
	if !contents.Exists() {
		return data
	}
	canBatch := contents.IsArray()
	if canBatch {
		contents.ForEach(func(_, content ojson.Result) bool {
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

	contents.ForEach(func(contentIdx, content ojson.Result) bool {
		role := content.Get("role").String()
		contentRaw := []byte(content.Raw)

		// Collect functionCall names from model turns.
		if role == "model" {
			var names []string
			content.Get("parts").ForEach(func(_, part ojson.Result) bool {
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
			content.Get("parts").ForEach(func(_, part ojson.Result) bool {
				partRaw := []byte(part.Raw)
				if part.Get("functionResponse").Exists() {
					name := part.Get("functionResponse.name").String()
					if strings.TrimSpace(name) == "" {
						if responseIndex < len(pendingCallNames) {
							partRaw, _ = ojson.SetBytes(partRaw, "functionResponse.name", pendingCallNames[responseIndex])
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
				contentRaw, _ = ojson.SetRawBytes(contentRaw, "parts", translatorcommon.JoinRawArray(partItems))
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
	out, errSetContents := ojson.SetRawBytes(data, "contents", translatorcommon.JoinRawArray(contentItems))
	if errSetContents != nil {
		return data
	}
	return out
}

func geminiFunctionResponseNamesNeedBackfill(contents ojson.Result) (bool, []int64) {
	var pendingCallNames []string
	var excessResponseIndexes []int64
	needsBackfill := false
	contents.ForEach(func(contentIdx, content ojson.Result) bool {
		if content.Get("role").String() == "model" {
			var names []string
			content.Get("parts").ForEach(func(_, part ojson.Result) bool {
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
		content.Get("parts").ForEach(func(_, part ojson.Result) bool {
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

func backfillEmptyFunctionResponseNamesLegacy(data []byte, contents ojson.Result) []byte {
	var outDoc *ojson.Doc
	var pendingCallNames []string
	contents.ForEach(func(contentIdx, content ojson.Result) bool {
		if content.Get("role").String() == "model" {
			var names []string
			content.Get("parts").ForEach(func(_, part ojson.Result) bool {
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
			content.Get("parts").ForEach(func(partIdx, part ojson.Result) bool {
				if part.Get("functionResponse").Exists() {
					if strings.TrimSpace(part.Get("functionResponse.name").String()) == "" {
						if responseIndex < len(pendingCallNames) {
							path := fmt.Sprintf("contents.%d.parts.%d.functionResponse.name", contentIdx.Int(), partIdx.Int())
							if outDoc == nil {
								outDoc = ojson.NewDoc(data)
							}
							_ = outDoc.Set(path, pendingCallNames[responseIndex])
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
	if outDoc != nil {
		return outDoc.Bytes()
	}
	return data
}

func nextGeminiRole(previousRole string) string {
	if previousRole == "" || previousRole == "model" {
		return "user"
	}
	return "model"
}
