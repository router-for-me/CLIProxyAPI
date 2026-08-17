package toolcall

import (
	"encoding/json"
	"strings"
)

func NormalizeParsedToolCallsForSchemas(calls []ParsedToolCall, toolsRaw any) []ParsedToolCall {
	if len(calls) == 0 {
		return calls
	}
	schemas := buildToolSchemaIndex(toolsRaw)
	if len(schemas) == 0 {
		return calls
	}

	var changedAny bool
	out := make([]ParsedToolCall, len(calls))
	for i, call := range calls {
		out[i] = call
		schema, ok := schemas[strings.ToLower(strings.TrimSpace(call.Name))]
		if !ok || call.Input == nil {
			continue
		}
		normalized, changed := normalizeToolValueWithSchema(call.Input, schema)
		if !changed {
			continue
		}
		changedAny = true
		if input, ok := normalized.(map[string]any); ok {
			out[i].Input = input
		}
	}
	if !changedAny {
		return calls
	}
	return out
}

func buildToolSchemaIndex(toolsRaw any) map[string]any {
	tools, ok := toolsRaw.([]any)
	if !ok || len(tools) == 0 {
		return nil
	}
	out := make(map[string]any, len(tools))
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _, schema := ExtractToolMeta(tool)
		if name == "" || schema == nil {
			continue
		}
		out[strings.ToLower(name)] = schema
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func ExtractToolMeta(tool map[string]any) (string, string, any) {
	name := strings.TrimSpace(asStringValue(tool["name"]))
	desc := strings.TrimSpace(asStringValue(tool["description"]))
	schema := firstNonNil(
		tool["parameters"],
		tool["input_schema"],
		tool["inputSchema"],
		tool["schema"],
	)
	if fn, ok := tool["function"].(map[string]any); ok {
		if name == "" {
			name = strings.TrimSpace(asStringValue(fn["name"]))
		}
		if desc == "" {
			desc = strings.TrimSpace(asStringValue(fn["description"]))
		}
		schema = firstNonNil(
			schema,
			fn["parameters"],
			fn["input_schema"],
			fn["inputSchema"],
			fn["schema"],
		)
	}
	return name, desc, schema
}

func normalizeToolValueWithSchema(value any, schema any) (any, bool) {
	if value == nil || schema == nil {
		return value, false
	}
	schemaMap, ok := schema.(map[string]any)
	if !ok || len(schemaMap) == 0 {
		return value, false
	}
	if shouldCoerceSchemaToString(schemaMap) {
		return stringifySchemaValue(value)
	}
	if looksLikeObjectSchema(schemaMap) {
		obj, ok := value.(map[string]any)
		if !ok || len(obj) == 0 {
			return value, false
		}
		properties, _ := schemaMap["properties"].(map[string]any)
		additional := schemaMap["additionalProperties"]
		changed := false
		out := make(map[string]any, len(obj))
		for key, current := range obj {
			next := current
			var fieldChanged bool
			if propSchema, ok := properties[key]; ok {
				next, fieldChanged = normalizeToolValueWithSchema(current, propSchema)
			} else if additional != nil {
				next, fieldChanged = normalizeToolValueWithSchema(current, additional)
			}
			out[key] = next
			changed = changed || fieldChanged
		}
		if !changed {
			return value, false
		}
		return out, true
	}
	if looksLikeArraySchema(schemaMap) {
		arr, ok := value.([]any)
		if !ok || len(arr) == 0 {
			return value, false
		}
		itemsSchema := schemaMap["items"]
		if itemsSchema == nil {
			return value, false
		}
		changed := false
		out := make([]any, len(arr))
		switch itemSchemas := itemsSchema.(type) {
		case []any:
			for i, item := range arr {
				if i >= len(itemSchemas) {
					out[i] = item
					continue
				}
				next, itemChanged := normalizeToolValueWithSchema(item, itemSchemas[i])
				out[i] = next
				changed = changed || itemChanged
			}
		default:
			for i, item := range arr {
				next, itemChanged := normalizeToolValueWithSchema(item, itemsSchema)
				out[i] = next
				changed = changed || itemChanged
			}
		}
		if !changed {
			return value, false
		}
		return out, true
	}
	return value, false
}

func shouldCoerceSchemaToString(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	if isStringConst(schema["const"]) {
		return true
	}
	if isStringEnum(schema["enum"]) {
		return true
	}
	switch v := schema["type"].(type) {
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "string")
	case []any:
		return isOnlyStringLikeTypes(v)
	case []string:
		items := make([]any, 0, len(v))
		for _, item := range v {
			items = append(items, item)
		}
		return isOnlyStringLikeTypes(items)
	default:
		return false
	}
}

func looksLikeObjectSchema(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	if typ, ok := schema["type"].(string); ok && strings.EqualFold(strings.TrimSpace(typ), "object") {
		return true
	}
	if _, ok := schema["properties"].(map[string]any); ok {
		return true
	}
	_, hasAdditional := schema["additionalProperties"]
	return hasAdditional
}

func looksLikeArraySchema(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	if typ, ok := schema["type"].(string); ok && strings.EqualFold(strings.TrimSpace(typ), "array") {
		return true
	}
	_, hasItems := schema["items"]
	return hasItems
}

func isOnlyStringLikeTypes(values []any) bool {
	if len(values) == 0 {
		return false
	}
	hasString := false
	for _, item := range values {
		typ, ok := item.(string)
		if !ok {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(typ)) {
		case "string":
			hasString = true
		case "null":
			continue
		default:
			return false
		}
	}
	return hasString
}

func isStringConst(v any) bool {
	_, ok := v.(string)
	return ok
}

func isStringEnum(v any) bool {
	values, ok := v.([]any)
	if !ok || len(values) == 0 {
		return false
	}
	for _, item := range values {
		if _, ok := item.(string); !ok {
			return false
		}
	}
	return true
}

func stringifySchemaValue(value any) (any, bool) {
	if value == nil {
		return value, false
	}
	if s, ok := value.(string); ok {
		return s, false
	}
	b, err := json.Marshal(value)
	if err != nil {
		return value, false
	}
	return string(b), true
}

func asStringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
