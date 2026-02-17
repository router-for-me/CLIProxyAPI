package util

import (
	"encoding/json"
	"strings"
)

// NormalizeStructuredOutputSchema enforces Codex/OpenAI strict structured-output
// requirements by setting additionalProperties=false on all object schemas.
// If the input is not valid JSON, the original string is returned unchanged.
func NormalizeStructuredOutputSchema(schemaRaw string) string {
	trimmed := strings.TrimSpace(schemaRaw)
	if trimmed == "" {
		return schemaRaw
	}

	var node any
	if err := json.Unmarshal([]byte(trimmed), &node); err != nil {
		return schemaRaw
	}

	node = normalizeStructuredOutputSchemaNode(node)
	out, err := json.Marshal(node)
	if err != nil {
		return schemaRaw
	}
	return string(out)
}

func normalizeStructuredOutputSchemaNode(node any) any {
	schema, ok := node.(map[string]any)
	if !ok {
		return node
	}

	if isStructuredOutputObjectSchema(schema) {
		schema["additionalProperties"] = false
	}

	normalizeStructuredOutputMapValues(schema, "properties")
	normalizeStructuredOutputMapValues(schema, "patternProperties")
	normalizeStructuredOutputMapValues(schema, "$defs")
	normalizeStructuredOutputMapValues(schema, "definitions")
	normalizeStructuredOutputMapValues(schema, "dependentSchemas")

	normalizeStructuredOutputSchemaValue(schema, "additionalProperties")
	normalizeStructuredOutputSchemaValue(schema, "items")
	normalizeStructuredOutputSchemaValue(schema, "contains")
	normalizeStructuredOutputSchemaValue(schema, "propertyNames")
	normalizeStructuredOutputSchemaValue(schema, "not")
	normalizeStructuredOutputSchemaValue(schema, "if")
	normalizeStructuredOutputSchemaValue(schema, "then")
	normalizeStructuredOutputSchemaValue(schema, "else")
	normalizeStructuredOutputSchemaValue(schema, "unevaluatedItems")
	normalizeStructuredOutputSchemaValue(schema, "unevaluatedProperties")

	normalizeStructuredOutputSchemaArray(schema, "allOf")
	normalizeStructuredOutputSchemaArray(schema, "anyOf")
	normalizeStructuredOutputSchemaArray(schema, "oneOf")
	normalizeStructuredOutputSchemaArray(schema, "prefixItems")

	return schema
}

func normalizeStructuredOutputMapValues(schema map[string]any, key string) {
	raw, ok := schema[key]
	if !ok {
		return
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return
	}
	for childKey, child := range m {
		m[childKey] = normalizeStructuredOutputSchemaNode(child)
	}
}

func normalizeStructuredOutputSchemaValue(schema map[string]any, key string) {
	raw, ok := schema[key]
	if !ok {
		return
	}

	switch child := raw.(type) {
	case map[string]any:
		schema[key] = normalizeStructuredOutputSchemaNode(child)
	case []any:
		for i := range child {
			child[i] = normalizeStructuredOutputSchemaNode(child[i])
		}
		schema[key] = child
	}
}

func normalizeStructuredOutputSchemaArray(schema map[string]any, key string) {
	raw, ok := schema[key]
	if !ok {
		return
	}
	arr, ok := raw.([]any)
	if !ok {
		return
	}
	for i := range arr {
		arr[i] = normalizeStructuredOutputSchemaNode(arr[i])
	}
	schema[key] = arr
}

func isStructuredOutputObjectSchema(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	if hasObjectType(schema["type"]) {
		return true
	}
	if _, ok := schema["properties"].(map[string]any); ok {
		return true
	}
	if _, ok := schema["required"].([]any); ok {
		return true
	}
	return false
}

func hasObjectType(raw any) bool {
	switch t := raw.(type) {
	case string:
		return t == "object"
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok && s == "object" {
				return true
			}
		}
	}
	return false
}
