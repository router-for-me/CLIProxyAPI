// Package ir provides intermediate representation types for the translator system.
//
// This file implements Tool Schema Context - a mechanism for context-aware
// normalization of tool call parameters in model responses.
//
// # Problem
//
// Some AI models ignore the tool parameter schema provided in the request and
// return parameters with different names. For example:
//   - Client sends schema with "target_file" parameter
//   - Model returns tool call with "path" or "file_path" instead
//   - Client rejects the response: "missing required argument target_file"
//
// This causes tool call failures even though the model's intent was correct.
//
// # Solution
//
// Context-dependent normalization: we extract the expected parameter schema
// from the original client request and use it to fix parameter names in the
// model's response before sending back to the client.
//
// The normalization is:
//   - Transparent: if parameters already match, no changes are made
//   - Safe: only renames parameters if a clear match exists in the schema
//   - Efficient: uses gjson for fast schema extraction without full JSON parsing
//   - Recursive: handles nested objects and arrays at any depth
//
// # Current Usage
//
// Currently enabled only for the Antigravity provider, which exhibits this
// parameter naming issue when proxying through Gemini CLI.
//
// # Potential Applications
//
// This mechanism can be enabled for any provider to achieve:
//
//  1. Client Compatibility: Different clients (Cursor, Copilot, Cline) may use
//     different parameter naming conventions. This normalizer can bridge the gap
//     between what a model returns and what a specific client expects.
//
//  2. Model Compatibility: Some models consistently use snake_case while others
//     prefer camelCase. Instead of hardcoding mappings per model, this approach
//     dynamically adapts based on the client's schema.
//
//  3. Provider Abstraction: When adding new providers, you don't need to worry
//     about their parameter naming quirks - the normalizer handles mismatches
//     automatically based on the original request schema.
//
//  4. Bidirectional Support: While currently used for response normalization,
//     the same approach could normalize requests TO providers that expect
//     different parameter names than what the client sends.
//
// To enable for other providers, use NewAntigravityStreamState() pattern in
// the respective executor, or create a similar helper function.
//
// # Usage Example
//
//	// In executor, create context from original request:
//	tools := gjson.GetBytes(originalRequest, "tools").Array()
//	schemaCtx := ir.NewToolSchemaContextFromGJSON(tools)
//
//	// When parsing model response, normalize tool call args:
//	normalizedArgs := schemaCtx.NormalizeToolCallArgs(toolName, argsJSON)
package ir

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// ToolSchemaContext holds the "expectation map" - what the client expects to receive.
// Uses gjson.Result for efficient parsing without full unmarshaling.
type ToolSchemaContext struct {
	// Tools maps ToolName -> ParameterName -> ParameterType ("string", "array", "object", "boolean", "number", "integer")
	Tools map[string]map[string]string
}

// NewToolSchemaContextFromGJSON creates a context from gjson tools array (fast, no full unmarshal).
// toolsJSON is the array from gjson.GetBytes(body, "tools").Array()
// Supports multiple formats:
// - OpenAI format: tools[].type="function", tools[].function.name, tools[].function.parameters.properties
// - Gemini format: tools[].functionDeclarations[].name, tools[].functionDeclarations[].parametersJsonSchema.properties
// - Direct Gemini: tools[].name, tools[].parametersJsonSchema.properties
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
			// Try direct Gemini format: tools[].name (single function declaration)
			name = t.Get("name").String()
			if name != "" {
				// Gemini uses parametersJsonSchema instead of parameters
				propsPath = "parametersJsonSchema.properties"
				if !t.Get(propsPath).Exists() {
					propsPath = "parameters.properties"
				}
			}
		}

		if name != "" {
			params := make(map[string]string)
			t.Get(propsPath).ForEach(func(key, value gjson.Result) bool {
				// Extract type from property schema
				paramType := value.Get("type").String()
				if paramType == "" {
					paramType = "string" // Default to string if not specified
				}
				params[key.String()] = paramType
				return true
			})
			ctx.Tools[name] = params
		}

		// Also check for Gemini nested format: tools[].functionDeclarations[]
		funcDecls := t.Get("functionDeclarations")
		if funcDecls.IsArray() {
			for _, fd := range funcDecls.Array() {
				fdName := fd.Get("name").String()
				if fdName == "" {
					continue
				}
				params := make(map[string]string)
				// Try parametersJsonSchema first, then parameters
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
// Only normalizes complete JSON arguments (not partial/streaming fragments).
//
// Strategy:
//  1. If param exists in schema - keep as is
//  2. If param doesn't exist, try to find a match:
//     - snake_case <-> camelCase conversion
//     - Semantic synonyms (path -> target_file, etc.)
//  3. If no match found - keep original (let client handle the error)
//  4. Recursively normalize nested objects
//  5. Handle array-to-string conversion based on schema type
//  6. Add default values for commonly missing required parameters
func (ctx *ToolSchemaContext) NormalizeToolCallArgs(toolName, argsJSON string) string {
	// 1. Fast checks
	if ctx == nil || argsJSON == "" || argsJSON == "{}" {
		return argsJSON
	}
	paramTypes, ok := ctx.Tools[toolName]
	if !ok || len(paramTypes) == 0 {
		return argsJSON
	}

	// 2. Parse what the model sent
	var actualArgs map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &actualArgs); err != nil {
		return argsJSON // If not valid JSON, return as-is (let it fail downstream)
	}

	// 3. Normalize recursively (includes array-to-string conversion based on schema types)
	normalizedArgs, changed := normalizeMapRecursive(actualArgs, paramTypes)

	// 4. Add default values for commonly missing required parameters
	defaultsChanged := addMissingDefaults(toolName, normalizedArgs, paramTypes)
	changed = changed || defaultsChanged

	if !changed {
		return argsJSON
	}

	out, err := json.Marshal(normalizedArgs)
	if err != nil {
		return argsJSON
	}
	return string(out)
}

// addMissingDefaults adds default values for commonly missing required parameters.
// Returns true if any defaults were added.
// Uses ToolDefaults from normalization_config.go instead of hardcoded values.
func addMissingDefaults(toolName string, args map[string]interface{}, paramTypes map[string]string) bool {
	changed := false

	// Use configurable defaults from normalization_config.go
	if toolDefaults, ok := ToolDefaults[toolName]; ok {
		for param, defaultValue := range toolDefaults {
			// Only add if parameter is expected in schema and not already present
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

// normalizeMapRecursive normalizes a map and all nested maps recursively.
// paramTypes maps parameter names to their expected types from schema.
// Returns the normalized map and whether any changes were made.
func normalizeMapRecursive(args map[string]interface{}, paramTypes map[string]string) (map[string]interface{}, bool) {
	changed := false
	normalized := make(map[string]interface{}, len(args))

	for key, value := range args {
		newKey := key
		newValue := value

		// Check if key needs normalization (key not in schema)
		if _, inSchema := paramTypes[key]; !inSchema {
			if match := findBestMatch(key, paramTypes); match != "" {
				newKey = match
				changed = true
			}
		}

		// Get expected type for the (possibly renamed) key
		expectedType := paramTypes[newKey]

		// Recursively normalize nested objects and handle type mismatches
		switch v := value.(type) {
		case map[string]interface{}:
			// Nested object - normalize recursively
			normalizedNested, nestedChanged := normalizeMapRecursive(v, paramTypes)
			if nestedChanged {
				newValue = normalizedNested
				changed = true
			}
		case []interface{}:
			// Array handling:
			// If schema expects string but model sent array, extract first element
			if len(v) > 0 {
				// Schema expects scalar but got array - extract first element
				first := v[0]
				switch expectedType {
				case "string":
					if str, ok := first.(string); ok {
						newValue = str
						changed = true
					}
				case "integer", "number":
					// Keep numeric value as-is
					newValue = first
					changed = true
				}
			}
			if !changed {
				// Schema expects array or unknown - normalize array recursively
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

// normalizeArrayRecursive normalizes all objects within an array recursively.
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

// findBestMatch finds a suitable key in the schema for the model's key.
// paramTypes maps parameter names to their expected types.
// Uses ParameterSynonyms from normalization_config.go for semantic matching.
func findBestMatch(actualKey string, paramTypes map[string]string) string {
	// Helper to check if key exists in schema
	inSchema := func(key string) bool {
		_, ok := paramTypes[key]
		return ok
	}

	// 1. Check camelCase <-> snake_case conversions
	// model: "filePath" -> schema: "file_path"
	snake := camelToSnake(actualKey)
	if inSchema(snake) {
		return snake
	}
	// model: "file_path" -> schema: "filePath"
	camel := snakeToCamel(actualKey)
	if inSchema(camel) {
		return camel
	}

	// 2. Check semantic synonyms from configurable ParameterSynonyms map
	// This is safer than hardcoding - we only remap if the target exists in schema.
	// If schema doesn't have target_file, we won't rename path.
	// NOTE: Mappings work bidirectionally - if model sends "target_file" but schema expects "file_path",
	// we check if "file_path" is in the candidates list for "target_file".
	if candidates, ok := ParameterSynonyms[strings.ToLower(actualKey)]; ok {
		for _, candidate := range candidates {
			if inSchema(candidate) {
				return candidate
			}
			// Also try case conversions
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

// snakeToCamel converts snake_case to camelCase.
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

// camelToSnake converts camelCase to snake_case.
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
