package util

import (
	"encoding/json"
	"sort"
	"strings"
)

// NormalizeOpenAIResponseSchema adjusts JSON Schema for the OpenAI Responses API.
// The API expects every object schema with properties to declare all property keys
// in required. Properties omitted from the original required list are made nullable
// to preserve optional semantics as closely as possible.
func NormalizeOpenAIResponseSchema(jsonStr string) string {
	if strings.TrimSpace(jsonStr) == "" {
		return jsonStr
	}

	var schema any
	if err := json.Unmarshal([]byte(jsonStr), &schema); err != nil {
		return jsonStr
	}

	normalizeOpenAIResponseSchemaNode(schema)

	normalized, err := json.Marshal(schema)
	if err != nil {
		return jsonStr
	}
	return string(normalized)
}

func normalizeOpenAIResponseSchemaNode(node any) {
	switch value := node.(type) {
	case map[string]any:
		normalizeOpenAIResponseSchemaObject(value)
	case []any:
		for _, item := range value {
			normalizeOpenAIResponseSchemaNode(item)
		}
	}
}

func normalizeOpenAIResponseSchemaObject(schema map[string]any) {
	properties, ok := schema["properties"].(map[string]any)
	if ok {
		requiredSet, requiredOrder := extractRequiredKeys(schema["required"])
		missing := make([]string, 0)

		for key, child := range properties {
			normalizeOpenAIResponseSchemaNode(child)
			if !requiredSet[key] {
				properties[key] = makeOpenAIResponseSchemaNullable(child)
				missing = append(missing, key)
			}
		}

		sort.Strings(missing)

		required := make([]any, 0, len(properties))
		for _, key := range requiredOrder {
			if _, exists := properties[key]; exists {
				required = append(required, key)
			}
		}
		for _, key := range missing {
			required = append(required, key)
		}
		schema["required"] = required
	}

	for key, child := range schema {
		if key == "properties" {
			continue
		}
		normalizeOpenAIResponseSchemaNode(child)
	}
}

func extractRequiredKeys(raw any) (map[string]bool, []string) {
	set := make(map[string]bool)
	order := make([]string, 0)

	items, ok := raw.([]any)
	if !ok {
		return set, order
	}

	for _, item := range items {
		key, ok := item.(string)
		if !ok || key == "" || set[key] {
			continue
		}
		set[key] = true
		order = append(order, key)
	}
	return set, order
}

func makeOpenAIResponseSchemaNullable(raw any) any {
	schema, ok := raw.(map[string]any)
	if !ok {
		return raw
	}

	if rawType, exists := schema["type"]; exists {
		switch typeValue := rawType.(type) {
		case string:
			if typeValue != "null" {
				schema["type"] = []any{typeValue, "null"}
			}
			return schema
		case []any:
			if !arrayContainsString(typeValue, "null") {
				schema["type"] = append(typeValue, "null")
			}
			return schema
		}
	}

	if anyOf, ok := schema["anyOf"].([]any); ok {
		if !schemaAlternativesContainNull(anyOf) {
			schema["anyOf"] = append(anyOf, map[string]any{"type": "null"})
		}
		return schema
	}

	if oneOf, ok := schema["oneOf"].([]any); ok {
		if !schemaAlternativesContainNull(oneOf) {
			schema["oneOf"] = append(oneOf, map[string]any{"type": "null"})
		}
		return schema
	}

	switch {
	case schema["properties"] != nil:
		schema["type"] = []any{"object", "null"}
	case schema["items"] != nil:
		schema["type"] = []any{"array", "null"}
	}

	return schema
}

func arrayContainsString(items []any, target string) bool {
	for _, item := range items {
		if value, ok := item.(string); ok && value == target {
			return true
		}
	}
	return false
}

func schemaAlternativesContainNull(items []any) bool {
	for _, item := range items {
		schema, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch typeValue := schema["type"].(type) {
		case string:
			if typeValue == "null" {
				return true
			}
		case []any:
			if arrayContainsString(typeValue, "null") {
				return true
			}
		}
	}
	return false
}
