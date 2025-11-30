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
	// Tools maps ToolName -> Set of expected ParameterNames
	Tools map[string]map[string]bool
}

// NewToolSchemaContextFromGJSON creates a context from gjson tools array (fast, no full unmarshal).
// toolsJSON is the array from gjson.GetBytes(body, "tools").Array()
func NewToolSchemaContextFromGJSON(toolsJSON []gjson.Result) *ToolSchemaContext {
	if len(toolsJSON) == 0 {
		return nil
	}
	ctx := &ToolSchemaContext{
		Tools: make(map[string]map[string]bool),
	}

	for _, t := range toolsJSON {
		name := t.Get("function.name").String()
		if name == "" {
			continue
		}

		params := make(map[string]bool)
		// Collect keys from properties
		t.Get("function.parameters.properties").ForEach(func(key, _ gjson.Result) bool {
			params[key.String()] = true
			return true
		})

		ctx.Tools[name] = params
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
func (ctx *ToolSchemaContext) NormalizeToolCallArgs(toolName, argsJSON string) string {
	// 1. Fast checks
	if ctx == nil || argsJSON == "" || argsJSON == "{}" {
		return argsJSON
	}
	expectedParams, ok := ctx.Tools[toolName]
	if !ok || len(expectedParams) == 0 {
		return argsJSON
	}

	// 2. Parse what the model sent
	var actualArgs map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &actualArgs); err != nil {
		return argsJSON // If not valid JSON, return as-is (let it fail downstream)
	}

	// 3. Normalize recursively
	normalizedArgs, changed := normalizeMapRecursive(actualArgs, expectedParams)

	if !changed {
		return argsJSON
	}

	out, err := json.Marshal(normalizedArgs)
	if err != nil {
		return argsJSON
	}
	return string(out)
}

// normalizeMapRecursive normalizes a map and all nested maps recursively.
// Returns the normalized map and whether any changes were made.
func normalizeMapRecursive(args map[string]interface{}, expectedKeys map[string]bool) (map[string]interface{}, bool) {
	changed := false
	normalized := make(map[string]interface{}, len(args))

	for key, value := range args {
		newKey := key
		newValue := value

		// Check if key needs normalization
		if !expectedKeys[key] {
			if match := findBestMatch(key, expectedKeys); match != "" {
				newKey = match
				changed = true
			}
		}

		// Recursively normalize nested objects
		switch v := value.(type) {
		case map[string]interface{}:
			// Nested object - normalize recursively
			normalizedNested, nestedChanged := normalizeMapRecursive(v, expectedKeys)
			if nestedChanged {
				newValue = normalizedNested
				changed = true
			}
		case []interface{}:
			// Array - check each element for nested objects
			normalizedArray, arrayChanged := normalizeArrayRecursive(v, expectedKeys)
			if arrayChanged {
				newValue = normalizedArray
				changed = true
			}
		}

		normalized[newKey] = newValue
	}

	return normalized, changed
}

// normalizeArrayRecursive normalizes all objects within an array recursively.
func normalizeArrayRecursive(arr []interface{}, expectedKeys map[string]bool) ([]interface{}, bool) {
	changed := false
	normalized := make([]interface{}, len(arr))

	for i, item := range arr {
		switch v := item.(type) {
		case map[string]interface{}:
			normalizedItem, itemChanged := normalizeMapRecursive(v, expectedKeys)
			if itemChanged {
				normalized[i] = normalizedItem
				changed = true
			} else {
				normalized[i] = item
			}
		case []interface{}:
			normalizedItem, itemChanged := normalizeArrayRecursive(v, expectedKeys)
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
func findBestMatch(actualKey string, expectedKeys map[string]bool) string {
	// 1. Check camelCase <-> snake_case conversions
	// model: "filePath" -> schema: "file_path"
	snake := camelToSnake(actualKey)
	if expectedKeys[snake] {
		return snake
	}
	// model: "file_path" -> schema: "filePath"
	camel := snakeToCamel(actualKey)
	if expectedKeys[camel] {
		return camel
	}

	// 2. Check semantic synonyms (minimal dictionary of Gemini's most common mistakes)
	// This is MUCH smaller and safer - we only remap if the target exists in schema.
	// If schema doesn't have target_file, we won't rename path.
	synonyms := map[string][]string{
		"path":       {"target_file", "file_path", "filename", "target_directory"},
		"content":    {"contents", "code", "text"},
		"code":       {"content", "contents"},
		"background": {"is_background"},
	}

	if candidates, ok := synonyms[strings.ToLower(actualKey)]; ok {
		for _, candidate := range candidates {
			if expectedKeys[candidate] {
				return candidate
			}
			// Also try case conversions
			if cc := snakeToCamel(candidate); expectedKeys[cc] {
				return cc
			}
			if sc := camelToSnake(candidate); expectedKeys[sc] {
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
