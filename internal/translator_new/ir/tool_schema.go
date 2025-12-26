// Package ir provides intermediate representation types for the translator system.
//
// This file implements Tool Schema Context - a mechanism for context-aware
// normalization of tool call parameters in model responses.
package ir

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// ToolSchemaContext holds the "expectation map" - what the client expects to receive.
type ToolSchemaContext struct {
	// Tools maps ToolName -> ParameterName -> ParameterType
	Tools map[string]map[string]string
}

// NewToolSchemaContextFromGJSON creates a context from gjson tools array.
func NewToolSchemaContextFromGJSON(toolsJSON []gjson.Result) *ToolSchemaContext {
	if len(toolsJSON) == 0 {
		return nil
	}
	ctx := &ToolSchemaContext{
		Tools: make(map[string]map[string]string),
	}

	for _, t := range toolsJSON {
		// Try OpenAI format first: tools[].function.name
		name := t.Get("function.name").String()
		var propsPath string
		if name != "" {
			propsPath = "function.parameters.properties"
		} else {
			// Try direct Gemini format: tools[].name
			name = t.Get("name").String()
			if name != "" {
				propsPath = "parametersJsonSchema.properties"
				if !t.Get(propsPath).Exists() {
					propsPath = "parameters.properties"
				}
			}
		}

		if name != "" {
			params := make(map[string]string)
			t.Get(propsPath).ForEach(func(key, value gjson.Result) bool {
				paramType := value.Get("type").String()
				if paramType == "" {
					paramType = "string"
				}
				params[key.String()] = paramType
				return true
			})
			ctx.Tools[name] = params
		}

		// Check for Gemini nested format
		funcDecls := t.Get("functionDeclarations")
		if funcDecls.IsArray() {
			for _, fd := range funcDecls.Array() {
				fdName := fd.Get("name").String()
				if fdName == "" {
					continue
				}
				params := make(map[string]string)
				fdPropsPath := "parametersJsonSchema.properties"
				if !fd.Get(fdPropsPath).Exists() {
					fdPropsPath = "parameters.properties"
				}
				fd.Get(fdPropsPath).ForEach(func(key, value gjson.Result) bool {
					paramType := value.Get("type").String()
					if paramType == "" {
						paramType = "string"
					}
					params[key.String()] = paramType
					return true
				})
				ctx.Tools[fdName] = params
			}
		}
	}
	return ctx
}

// NormalizeToolCallArgs fixes parameter names if the model made mistakes.
func (ctx *ToolSchemaContext) NormalizeToolCallArgs(toolName, argsJSON string) string {
	if argsJSON == "" || argsJSON == "{}" {
		return argsJSON
	}

	var actualArgs map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &actualArgs); err != nil {
		return argsJSON
	}

	changed := false

	// Apply tool-specific sanitizers first (e.g., grep context params fix)
	if sanitizer, ok := ToolArgsSanitizers[toolName]; ok {
		if sanitizer(actualArgs) {
			changed = true
		}
	}

	// Schema-based normalization (only if context available)
	if ctx != nil {
		if paramTypes, ok := ctx.Tools[toolName]; ok && len(paramTypes) > 0 {
			normalizedArgs, schemaChanged := normalizeMapRecursive(actualArgs, paramTypes)
			if schemaChanged {
				actualArgs = normalizedArgs
				changed = true
			}
			if addMissingDefaults(toolName, actualArgs, paramTypes) {
				changed = true
			}
		}
	}

	if !changed {
		return argsJSON
	}

	out, err := json.Marshal(actualArgs)
	if err != nil {
		return argsJSON
	}
	return string(out)
}

func addMissingDefaults(toolName string, args map[string]interface{}, paramTypes map[string]string) bool {
	changed := false
	if toolDefaults, ok := ToolDefaults[toolName]; ok {
		for param, defaultValue := range toolDefaults {
			if _, inSchema := paramTypes[param]; inSchema {
				if _, exists := args[param]; !exists {
					args[param] = defaultValue
					changed = true
				}
			}
		}
	}
	return changed
}

func normalizeMapRecursive(args map[string]interface{}, paramTypes map[string]string) (map[string]interface{}, bool) {
	changed := false
	normalized := make(map[string]interface{}, len(args))

	for key, value := range args {
		newKey := key
		newValue := value

		if _, inSchema := paramTypes[key]; !inSchema {
			if match := findBestMatch(key, paramTypes); match != "" {
				newKey = match
				changed = true
			}
		}

		expectedType := paramTypes[newKey]

		switch v := value.(type) {
		case map[string]interface{}:
			normalizedNested, nestedChanged := normalizeMapRecursive(v, paramTypes)
			if nestedChanged {
				newValue = normalizedNested
				changed = true
			}
		case []interface{}:
			if len(v) > 0 {
				first := v[0]
				switch expectedType {
				case "string":
					if str, ok := first.(string); ok {
						newValue = str
						changed = true
					}
				case "integer", "number":
					newValue = first
					changed = true
				}
			}
			if !changed {
				normalizedArray, arrayChanged := normalizeArrayRecursive(v, paramTypes)
				if arrayChanged {
					newValue = normalizedArray
					changed = true
				}
			}
		}
		normalized[newKey] = newValue
	}
	return normalized, changed
}

func normalizeArrayRecursive(arr []interface{}, paramTypes map[string]string) ([]interface{}, bool) {
	changed := false
	normalized := make([]interface{}, len(arr))

	for i, item := range arr {
		switch v := item.(type) {
		case map[string]interface{}:
			normalizedItem, itemChanged := normalizeMapRecursive(v, paramTypes)
			if itemChanged {
				normalized[i] = normalizedItem
				changed = true
			} else {
				normalized[i] = item
			}
		case []interface{}:
			normalizedItem, itemChanged := normalizeArrayRecursive(v, paramTypes)
			if itemChanged {
				normalized[i] = normalizedItem
				changed = true
			} else {
				normalized[i] = item
			}
		default:
			normalized[i] = item
		}
	}
	return normalized, changed
}

func findBestMatch(actualKey string, paramTypes map[string]string) string {
	inSchema := func(key string) bool {
		_, ok := paramTypes[key]
		return ok
	}

	snake := camelToSnake(actualKey)
	if inSchema(snake) {
		return snake
	}
	camel := snakeToCamel(actualKey)
	if inSchema(camel) {
		return camel
	}

	if candidates, ok := ParameterSynonyms[strings.ToLower(actualKey)]; ok {
		for _, candidate := range candidates {
			if inSchema(candidate) {
				return candidate
			}
			if cc := snakeToCamel(candidate); inSchema(cc) {
				return cc
			}
			if sc := camelToSnake(candidate); inSchema(sc) {
				return sc
			}
		}
	}
	return ""
}

func snakeToCamel(s string) string {
	parts := strings.Split(s, "_")
	if len(parts) <= 1 {
		return s
	}
	var b strings.Builder
	b.WriteString(parts[0])
	for _, part := range parts[1:] {
		if len(part) > 0 {
			b.WriteString(strings.ToUpper(part[:1]))
			b.WriteString(part[1:])
		}
	}
	return b.String()
}

func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}
