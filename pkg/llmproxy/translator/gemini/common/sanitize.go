package common

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func deleteJSONKeys(raw string, keys ...string) string {
	cleaned := raw
	for _, key := range keys {
		var paths []string
		util.Walk(gjson.Parse(cleaned), "", key, &paths)
		sort.Strings(paths)
		for _, path := range paths {
			cleaned, _ = sjson.Delete(cleaned, path)
		}
	}
	return cleaned
}

// SanitizeParametersJSONSchemaForGemini removes JSON Schema fields that Gemini rejects.
func SanitizeParametersJSONSchemaForGemini(raw string) string {
	withoutUnsupportedKeywords := deleteJSONKeys(raw, "$id", "patternProperties")
	return util.CleanJSONSchemaForGemini(withoutUnsupportedKeywords)
}

// SanitizeToolSearchForGemini removes ToolSearch fields unsupported by Gemini.
func SanitizeToolSearchForGemini(raw string) string {
	return deleteJSONKeys(raw, "defer_loading", "deferLoading")
}

// NormalizeOpenAIFunctionSchemaForGemini builds a Gemini-safe parametersJsonSchema
// from OpenAI function schema inputs and enforces a deterministic root shape.
func NormalizeOpenAIFunctionSchemaForGemini(params gjson.Result, strict bool) string {
	out := `{"type":"OBJECT","properties":{}}`
	if params.Exists() {
		raw := strings.TrimSpace(params.Raw)
		if params.Type == gjson.String {
			raw = strings.TrimSpace(params.String())
		}
		if raw != "" && raw != "null" && gjson.Valid(raw) {
			out = SanitizeParametersJSONSchemaForGemini(raw)
		}
	}
	out, _ = sjson.Set(out, "type", "OBJECT")
	if !gjson.Get(out, "properties").Exists() {
		out, _ = sjson.SetRaw(out, "properties", `{}`)
	}
	if strict {
		out, _ = sjson.Set(out, "additionalProperties", false)
	}
	return out
}
