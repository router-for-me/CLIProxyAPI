package executor

// This file freezes the pre-batching Antigravity function-response and schema
// rewrites. Differential tests use it as an independent byte-for-byte oracle.
// Do not refactor these helpers to call the production implementations.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func legacyAntigravityCountClaudeToolProvenanceIDs(payload []byte) int {
	count := 0
	contents := util.GetGJSONBytesNoCopy(payload, "request.contents")
	if !contents.IsArray() {
		return 0
	}
	for _, content := range contents.Array() {
		for _, part := range content.Get("parts").Array() {
			for _, path := range []string{"functionCall.id", "functionResponse.id"} {
				if util.IsGeminiClaudeToolUseID(part.Get(path).String()) {
					count++
				}
			}
		}
	}
	return count
}

func legacyAntigravityPayloadHasClaudeToolProvenanceID(payload []byte) bool {
	contents := util.GetGJSONBytesNoCopy(payload, "request.contents")
	if !contents.IsArray() {
		return false
	}
	for _, content := range contents.Array() {
		for _, part := range content.Get("parts").Array() {
			for _, path := range []string{"functionCall.id", "functionResponse.id"} {
				if util.IsGeminiClaudeToolUseID(part.Get(path).String()) {
					return true
				}
			}
		}
	}
	return false
}

func legacyNormalizeAntigravityGeminiFunctionResponseRoles(rawJSON []byte) []byte {
	rawJSON = legacyRepairAntigravityGeminiFunctionResponseNames(rawJSON)
	contents := util.GetGJSONBytesNoCopy(rawJSON, "request.contents")
	if !contents.IsArray() {
		return rawJSON
	}
	type functionRef struct {
		id   string
		name string
	}
	out := rawJSON
	var pending []functionRef
	for contentIndex, content := range contents.Array() {
		parts := content.Get("parts")
		if !parts.IsArray() || len(parts.Array()) == 0 {
			pending = nil
			continue
		}
		var calls, responses []functionRef
		var responseParts []json.RawMessage
		hasOtherPart := false
		parts.ForEach(func(_, part gjson.Result) bool {
			switch {
			case part.Get("functionCall").Exists():
				calls = append(calls, functionRef{id: part.Get("functionCall.id").String(), name: part.Get("functionCall.name").String()})
			case part.Get("functionResponse").Exists():
				responses = append(responses, functionRef{id: part.Get("functionResponse.id").String(), name: part.Get("functionResponse.name").String()})
				responseParts = append(responseParts, json.RawMessage(part.Raw))
			default:
				hasOtherPart = true
			}
			return true
		})
		if len(calls) > 0 && len(responses) == 0 {
			pending = calls
			continue
		}
		if len(responses) == 0 {
			if hasOtherPart {
				pending = nil
			}
			continue
		}
		if len(calls) > 0 {
			pending = nil
			continue
		}

		if len(pending) == len(responses) {
			// Group-aware reorder: each functionResponse part keeps the inline
			// media parts that directly follow it, standalone parts stay in
			// place, and only a one-to-one match by id (or name when ids are
			// absent) rewrites the parts array.
			type slot struct {
				group      int // response index, -1 marks a standalone part
				standalone json.RawMessage
			}
			var layout []slot
			grouped := make([][]json.RawMessage, 0, len(responseParts))
			openGroup := -1
			parts.ForEach(func(_, part gjson.Result) bool {
				raw := json.RawMessage(part.Raw)
				switch {
				case part.Get("functionResponse").Exists():
					grouped = append(grouped, []json.RawMessage{raw})
					openGroup = len(grouped) - 1
					layout = append(layout, slot{group: openGroup})
				case (part.Get("inline_data").Exists() || part.Get("inlineData").Exists()) && !part.Get("functionCall").Exists() && openGroup >= 0:
					grouped[openGroup] = append(grouped[openGroup], raw)
				default:
					openGroup = -1
					layout = append(layout, slot{group: -1, standalone: raw})
				}
				return true
			})
			orderedGroups := make([][]json.RawMessage, 0, len(pending))
			used := make([]bool, len(responses))
			matchedAll := true
			for _, call := range pending {
				matched := -1
				for responseIndex, response := range responses {
					if used[responseIndex] {
						continue
					}
					if (call.id != "" && response.id == call.id) || (call.id == "" && call.name != "" && response.name == call.name) {
						matched = responseIndex
						break
					}
				}
				if matched < 0 {
					matchedAll = false
					break
				}
				used[matched] = true
				orderedGroups = append(orderedGroups, grouped[matched])
			}
			if matchedAll {
				var ordered []json.RawMessage
				nextGroup := 0
				for _, s := range layout {
					if s.group >= 0 {
						ordered = append(ordered, orderedGroups[nextGroup]...)
						nextGroup++
						continue
					}
					ordered = append(ordered, s.standalone)
				}
				if encoded, errMarshal := json.Marshal(ordered); errMarshal == nil {
					if updated, errSet := sjson.SetRawBytes(out, fmt.Sprintf("request.contents.%d.parts", contentIndex), encoded); errSet == nil {
						out = updated
					}
				}
			}
		}
		pending = nil
		// Only a pure tool-result turn carries the model role; mixed contents
		// keep their original role.
		if !hasOtherPart && content.Get("role").String() != "model" {
			if updated, errSet := sjson.SetBytes(out, fmt.Sprintf("request.contents.%d.role", contentIndex), "model"); errSet == nil {
				out = updated
			}
		}
	}
	return out
}

func legacyRepairAntigravityGeminiFunctionResponseNames(rawJSON []byte) []byte {
	contents := util.GetGJSONBytesNoCopy(rawJSON, "request.contents")
	if !contents.IsArray() {
		return rawJSON
	}
	callIDToName := make(map[string]string)
	contents.ForEach(func(_, content gjson.Result) bool {
		parts := content.Get("parts")
		if !parts.IsArray() {
			return true
		}
		parts.ForEach(func(_, part gjson.Result) bool {
			fc := part.Get("functionCall")
			if fc.Exists() {
				id := strings.TrimSpace(fc.Get("id").String())
				name := strings.TrimSpace(fc.Get("name").String())
				if id != "" && name != "" && name != "unknown" {
					callIDToName[id] = name
				}
			}
			return true
		})
		return true
	})
	if len(callIDToName) == 0 {
		return rawJSON
	}

	out := rawJSON
	contents.ForEach(func(contentIdx, content gjson.Result) bool {
		parts := content.Get("parts")
		if !parts.IsArray() {
			return true
		}
		parts.ForEach(func(partIdx, part gjson.Result) bool {
			fr := part.Get("functionResponse")
			if fr.Exists() {
				id := strings.TrimSpace(fr.Get("id").String())
				name := strings.TrimSpace(fr.Get("name").String())
				if id != "" && (name == "" || name == "unknown") {
					if realName, ok := callIDToName[id]; ok {
						path := fmt.Sprintf("request.contents.%d.parts.%d.functionResponse.name", contentIdx.Int(), partIdx.Int())
						if updated, errSet := sjson.SetBytes(out, path, realName); errSet == nil {
							out = updated
						}
					}
				}
			}
			return true
		})
		return true
	})
	return out
}

func legacySanitizeAntigravityRequestSchemas(payloadStr string, useAntigravitySchema bool) string {
	for _, base := range antigravityFunctionDeclarationPaths(payloadStr) {
		oldPath := base + ".parametersJsonSchema"
		if !gjson.Get(payloadStr, oldPath).Exists() {
			continue
		}
		renamed, errRename := util.RenameKey(payloadStr, oldPath, base+".parameters")
		if errRename != nil {
			log.Debugf("antigravity: failed to rename %s: %v", oldPath, errRename)
			continue
		}
		payloadStr = renamed
	}

	toolSchemaCleaner := util.CleanJSONSchemaForGemini
	if useAntigravitySchema {
		toolSchemaCleaner = util.CleanJSONSchemaForAntigravity
	}
	responseSchemaCleaner := util.CleanJSONSchemaForAntigravityResponse
	cleanNestedToolSchema := func(schemaRaw string) string {
		return cleanNestedSchema(toolSchemaCleaner, schemaRaw)
	}
	payloadStr = legacyCleanAntigravitySchemasAtPaths(
		payloadStr,
		antigravityDeclarationSchemaPaths(payloadStr),
		cleanNestedToolSchema,
	)
	return legacyCleanAntigravitySchemasAtPaths(
		payloadStr,
		antigravityGenerationSchemaPaths(payloadStr),
		responseSchemaCleaner,
	)
}

func legacyCleanAntigravitySchemasAtPaths(payloadStr string, schemaPaths []string, clean func(string) string) string {
	for _, schemaPath := range schemaPaths {
		schema := gjson.Get(payloadStr, schemaPath)
		if !schema.Exists() {
			continue
		}
		updated, errSet := sjson.SetRawBytes([]byte(payloadStr), schemaPath, []byte(clean(schema.Raw)))
		if errSet != nil {
			continue
		}
		payloadStr = string(updated)
	}
	return payloadStr
}
