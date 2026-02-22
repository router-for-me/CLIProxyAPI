package claude

import (
	"encoding/json"
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const placeholderReasonDescription = "Brief explanation of why you are calling this tool"

func sanitizeClaudeRequest(rawJSON []byte) []byte {
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return rawJSON
	}

	updated := rawJSON
	changed := false
	for i, tool := range tools.Array() {
		inputSchema := tool.Get("input_schema")
		if !inputSchema.Exists() || !inputSchema.IsObject() {
			continue
		}
		sanitizedSchema, schemaChanged := sanitizeToolInputSchema([]byte(inputSchema.Raw))
		if !schemaChanged {
			continue
		}
		next, err := sjson.SetRawBytes(updated, "tools."+strconv.Itoa(i)+".input_schema", sanitizedSchema)
		if err != nil {
			return rawJSON
		}
		updated = next
		changed = true
	}
	if !changed {
		return rawJSON
	}
	return updated
}

func sanitizeToolInputSchema(rawSchema []byte) ([]byte, bool) {
	var schema any
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		return rawSchema, false
	}
	changed := stripSchemaPlaceholders(schema)
	if !changed {
		return rawSchema, false
	}
	out, err := json.Marshal(schema)
	if err != nil {
		return rawSchema, false
	}
	return out, true
}

func stripSchemaPlaceholders(node any) bool {
	changed := false

	switch current := node.(type) {
	case map[string]any:
		for _, v := range current {
			if stripSchemaPlaceholders(v) {
				changed = true
			}
		}

		propsRaw, ok := current["properties"]
		if !ok {
			return changed
		}
		props, ok := propsRaw.(map[string]any)
		if !ok {
			return changed
		}

		if _, ok := props["_"]; ok {
			delete(props, "_")
			filterRequired(current, "_")
			changed = true
		}

		reasonRaw, hasReason := props["reason"]
		if hasReason && len(props) == 1 && isPlaceholderReason(reasonRaw) {
			delete(props, "reason")
			filterRequired(current, "reason")
			changed = true
		}
	case []any:
		for _, v := range current {
			if stripSchemaPlaceholders(v) {
				changed = true
			}
		}
	}

	return changed
}

func filterRequired(schema map[string]any, key string) {
	requiredRaw, ok := schema["required"]
	if !ok {
		return
	}
	requiredList, ok := requiredRaw.([]any)
	if !ok {
		return
	}
	filtered := make([]any, 0, len(requiredList))
	for _, v := range requiredList {
		if str, ok := v.(string); ok && str == key {
			continue
		}
		filtered = append(filtered, v)
	}
	if len(filtered) == 0 {
		delete(schema, "required")
		return
	}
	schema["required"] = filtered
}

func isPlaceholderReason(reasonSchema any) bool {
	reasonMap, ok := reasonSchema.(map[string]any)
	if !ok {
		return false
	}
	description, _ := reasonMap["description"].(string)
	return description == placeholderReasonDescription
}
