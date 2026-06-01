package util

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

const strictObjectJSONSchema = `{"type":"object","properties":{},"additionalProperties":false}`

func CleanJSONSchemaForStrictUpstream(jsonStr string) string {
	return cleanJSONSchemaForStrictUpstream(jsonStr, false)
}

func CleanJSONSchemaForOpenAIStructuredOutput(jsonStr string) string {
	return cleanJSONSchemaForStrictUpstream(jsonStr, true)
}

func cleanJSONSchemaForStrictUpstream(jsonStr string, requireAllObjectProperties bool) string {
	jsonStr = strings.TrimSpace(jsonStr)
	if jsonStr == "" || jsonStr == "null" || !gjson.Valid(jsonStr) {
		return strictObjectJSONSchema
	}

	jsonStr = CleanJSONSchemaForGemini(jsonStr)
	if !gjson.Valid(jsonStr) {
		return strictObjectJSONSchema
	}

	var parsed any
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return strictObjectJSONSchema
	}

	root, ok := normalizeStrictSchemaNode(parsed).(map[string]any)
	if !ok {
		return strictObjectJSONSchema
	}

	if schemaType, ok := root["type"].(string); !ok || strings.TrimSpace(schemaType) == "" {
		root["type"] = "object"
	}
	if root["type"] == "object" {
		if _, ok := root["properties"].(map[string]any); !ok {
			root["properties"] = map[string]any{}
		}
		root["additionalProperties"] = false
	}
	if requireAllObjectProperties {
		requireAllPropertiesForObjects(root)
	} else {
		if req := normalizeStringArray(root["required"]); len(req) > 0 {
			root["required"] = req
		} else {
			delete(root, "required")
		}
	}

	out, err := json.Marshal(root)
	if err != nil || !gjson.ValidBytes(out) {
		return strictObjectJSONSchema
	}
	return string(out)
}

func normalizeStrictSchemaNode(node any) any {
	switch value := node.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(value))
		for key, raw := range value {
			if key == "properties" {
				normalized[key] = normalizeStrictSchemaProperties(raw)
				continue
			}
			if key == "items" {
				if raw == nil {
					normalized[key] = map[string]any{"type": "string"}
					continue
				}
				if next := normalizeStrictSchemaDefinition(raw); next != nil {
					normalized[key] = next
				} else {
					normalized[key] = map[string]any{"type": "string"}
				}
				continue
			}
			if key == "additionalProperties" {
				if raw == nil {
					continue
				}
				if allow, ok := raw.(bool); ok {
					normalized[key] = allow
					continue
				}
				if next := normalizeStrictSchemaDefinition(raw); next != nil {
					normalized[key] = next
				}
				continue
			}
			if raw == nil {
				switch key {
				case "items":
					normalized[key] = map[string]any{"type": "string"}
				}
				continue
			}

			if key == "required" {
				if req := normalizeStringArray(raw); len(req) > 0 {
					normalized[key] = req
				}
				continue
			}

			next := normalizeStrictSchemaNode(raw)
			if next == nil {
				switch key {
				case "items":
					normalized[key] = map[string]any{"type": "string"}
				case "properties":
					normalized[key] = map[string]any{}
				}
				continue
			}
			normalized[key] = next
		}

		schemaType, _ := normalized["type"].(string)
		schemaType = strings.TrimSpace(schemaType)
		if schemaType == "" {
			if _, hasProperties := normalized["properties"]; hasProperties {
				normalized["type"] = "object"
				schemaType = "object"
			}
		}
		if schemaType == "array" {
			if _, ok := normalized["items"]; !ok {
				normalized["items"] = map[string]any{"type": "string"}
			}
		}
		if schemaType == "object" {
			if _, ok := normalized["properties"].(map[string]any); !ok {
				normalized["properties"] = map[string]any{}
			}
			normalized["additionalProperties"] = false
		}
		return normalized
	case []any:
		out := make([]any, 0, len(value))
		for _, item := range value {
			next := normalizeStrictSchemaNode(item)
			if next != nil {
				out = append(out, next)
			}
		}
		return out
	default:
		return value
	}
}

func normalizeStrictSchemaProperties(raw any) map[string]any {
	properties, ok := raw.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	normalized := make(map[string]any, len(properties))
	for name, value := range properties {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		next := normalizeStrictSchemaDefinition(value)
		if next == nil {
			next = map[string]any{"type": "string"}
		}
		normalized[name] = next
	}
	return normalized
}

func normalizeStrictSchemaDefinition(node any) any {
	if schemaType, ok := normalizeStrictSchemaScalarType(node); ok {
		return strictSchemaForType(schemaType)
	}
	return normalizeStrictSchemaNode(node)
}

func normalizeStrictSchemaScalarType(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return normalizeStrictSchemaType(typed)
	case []any:
		for _, item := range typed {
			if str, ok := item.(string); ok {
				if schemaType, okType := normalizeStrictSchemaType(str); okType && schemaType != "null" {
					return schemaType, true
				}
			}
		}
		return "", false
	default:
		return "", false
	}
}

func normalizeStrictSchemaType(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "object", "array", "string", "number", "integer", "boolean", "null":
		return strings.ToLower(strings.TrimSpace(raw)), true
	default:
		return "", false
	}
}

func strictSchemaForType(schemaType string) map[string]any {
	schema := map[string]any{"type": schemaType}
	switch schemaType {
	case "object":
		schema["properties"] = map[string]any{}
		schema["additionalProperties"] = false
	case "array":
		schema["items"] = map[string]any{"type": "string"}
	}
	return schema
}

func requireAllPropertiesForObjects(node any) {
	switch value := node.(type) {
	case map[string]any:
		for _, raw := range value {
			requireAllPropertiesForObjects(raw)
		}

		schemaType, _ := value["type"].(string)
		if schemaType != "object" {
			return
		}
		properties, ok := value["properties"].(map[string]any)
		if !ok {
			value["required"] = []string{}
			return
		}
		keys := make([]string, 0, len(properties))
		for key := range properties {
			key = strings.TrimSpace(key)
			if key != "" {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		value["required"] = keys
	case []any:
		for _, item := range value {
			requireAllPropertiesForObjects(item)
		}
	}
}

func normalizeStringArray(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if str, ok := item.(string); ok {
				str = strings.TrimSpace(str)
				if str != "" {
					out = append(out, str)
				}
			}
		}
		return out
	default:
		return nil
	}
}
