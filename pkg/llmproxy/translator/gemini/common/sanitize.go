package common

import (
	"sort"

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
	return deleteJSONKeys(raw, "$id", "patternProperties")
}

// SanitizeToolSearchForGemini removes ToolSearch fields unsupported by Gemini.
func SanitizeToolSearchForGemini(raw string) string {
	return deleteJSONKeys(raw, "defer_loading", "deferLoading")
}
