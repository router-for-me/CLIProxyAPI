package executor

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var codexSpawnAgentSessionIDSchemaJSON = []byte(`{"type":["string","null"],"default":null,"description":"Existing subagent session id to continue. For creating a new subagent, omit this field or set it to JSON null. Never use empty string, /null, <null>, string null, or whitespace."}`)

func normalizeCodexSpawnAgentSessionIDToolSchema(rawJSON []byte) []byte {
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.IsArray() {
		return rawJSON
	}

	result := rawJSON
	for i, tool := range tools.Array() {
		if tool.Get("type").String() != "function" || tool.Get("name").String() != "spawn_agent" {
			continue
		}

		schemaPath := fmt.Sprintf("tools.%d.parameters.properties.session_id", i)
		if !gjson.GetBytes(result, schemaPath).Exists() {
			continue
		}
		if updated, err := sjson.SetRawBytes(result, schemaPath, codexSpawnAgentSessionIDSchemaJSON); err == nil {
			result = updated
		}

		requiredPath := fmt.Sprintf("tools.%d.parameters.required", i)
		result = removeCodexRequiredToolField(result, requiredPath, "session_id")
	}
	return result
}

func removeCodexRequiredToolField(rawJSON []byte, path string, field string) []byte {
	required := gjson.GetBytes(rawJSON, path)
	if !required.IsArray() {
		return rawJSON
	}

	changed := false
	filtered := []byte(`[]`)
	for _, item := range required.Array() {
		value := item.String()
		if value == field {
			changed = true
			continue
		}
		if updated, err := sjson.SetBytes(filtered, "-1", value); err == nil {
			filtered = updated
		}
	}
	if !changed {
		return rawJSON
	}

	updated, err := sjson.SetRawBytes(rawJSON, path, filtered)
	if err != nil {
		return rawJSON
	}
	return updated
}
