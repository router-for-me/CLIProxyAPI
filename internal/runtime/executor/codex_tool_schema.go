package executor

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
)

func normalizeCodexToolSchemas(payload []byte) []byte {
	if len(payload) == 0 || !gjson.GetBytes(payload, "tools").IsArray() {
		return payload
	}

	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return payload
	}
	tools, ok := root["tools"].([]any)
	if !ok || len(tools) == 0 {
		return payload
	}

	changed := false
	for _, rawTool := range tools {
		tool, okTool := rawTool.(map[string]any)
		if !okTool {
			continue
		}
		if normalizeCodexToolSchemaMap(tool) {
			changed = true
		}
		if function, okFunction := tool["function"].(map[string]any); okFunction {
			if normalizeCodexToolSchemaMap(function) {
				changed = true
			}
		}
	}
	if !changed {
		return payload
	}

	root["tools"] = tools
	out, err := json.Marshal(root)
	if err != nil || !gjson.ValidBytes(out) {
		return payload
	}
	return out
}

func normalizeCodexToolSchemaMap(node map[string]any) bool {
	if node == nil {
		return false
	}
	changed := false
	for _, key := range []string{"parameters", "input_schema", "parametersJsonSchema"} {
		value, ok := node[key]
		if !ok {
			continue
		}
		normalized, okNormalize := cleanCodexToolSchemaValue(value)
		if !okNormalize || jsonValuesEqual(value, normalized) {
			continue
		}
		node[key] = normalized
		changed = true
	}
	return changed
}

func cleanCodexToolSchemaValue(value any) (any, bool) {
	var raw []byte
	switch typed := value.(type) {
	case string:
		raw = []byte(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return nil, false
		}
		raw = encoded
	}
	cleaned := util.CleanJSONSchemaForStrictUpstream(string(raw))
	var out any
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		return nil, false
	}
	return out, true
}
