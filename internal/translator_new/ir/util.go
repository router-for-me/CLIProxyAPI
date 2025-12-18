package ir

import (
	"crypto/rand"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tailscale/hujson"
	"github.com/tidwall/gjson"
)

// =============================================================================
// OpenAI Shared Helpers (used by both to_ir and from_ir for OpenAI formats)
// =============================================================================

// OpenAIMeta contains metadata for OpenAI response/stream generation.
// Used to pass through original response fields from upstream provider.
type OpenAIMeta struct {
	ResponseID         string // Original response ID (e.g., from Gemini)
	CreateTime         int64  // Unix timestamp from upstream
	NativeFinishReason string // Original finish reason string (e.g., "STOP", "MAX_TOKENS")
	ThoughtsTokenCount int    // Reasoning token count for completion_tokens_details
}

// EstimateTokenCount estimates token count from text.
// Uses simple heuristic: ~4 characters per token for English, ~2 for CJK/Cyrillic.
// This is used when provider doesn't return reasoning_tokens but we have reasoning content.
func EstimateTokenCount(text string) int {
	if text == "" {
		return 0
	}
	// Count characters and estimate tokens
	// Average: 1 token ≈ 4 chars for English, 1-2 chars for CJK/Cyrillic
	runeCount := utf8.RuneCountInString(text)
	// Use conservative estimate of ~3 chars per token (mix of languages)
	return (runeCount + 2) / 3
}

// ParseOpenAIUsage parses usage from OpenAI API response.
// Handles both Chat Completions format (prompt_tokens, completion_tokens)
// and Responses API format (input_tokens, output_tokens).
func ParseOpenAIUsage(u gjson.Result) *Usage {
	if !u.Exists() {
		return nil
	}
	usage := &Usage{
		PromptTokens:     int(u.Get("prompt_tokens").Int() + u.Get("input_tokens").Int()),
		CompletionTokens: int(u.Get("completion_tokens").Int() + u.Get("output_tokens").Int()),
		TotalTokens:      int(u.Get("total_tokens").Int()),
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}

	// Cached tokens
	if v := u.Get("input_tokens_details.cached_tokens"); v.Exists() {
		usage.CachedTokens = int(v.Int())
	} else if v := u.Get("prompt_tokens_details.cached_tokens"); v.Exists() {
		usage.CachedTokens = int(v.Int())
	}

	// Reasoning tokens
	if v := u.Get("output_tokens_details.reasoning_tokens"); v.Exists() {
		usage.ThoughtsTokenCount = int(v.Int())
	} else if v := u.Get("completion_tokens_details.reasoning_tokens"); v.Exists() {
		usage.ThoughtsTokenCount = int(v.Int())
	}

	return usage
}

// MapEffortToBudget converts reasoning effort string to token budget.
// Used when parsing OpenAI requests with reasoning_effort parameter.
// Returns (budget, includeThoughts) - budget is token count, includeThoughts indicates if reasoning is enabled.
func MapEffortToBudget(effort string) (int, bool) {
	switch effort {
	case "none":
		return 0, false // Reasoning disabled
	case "auto":
		return -1, true // Dynamic budget, let provider decide
	case "minimal":
		return 512, true
	case "low":
		return 1024, true
	case "medium":
		return 8192, true
	case "high":
		return 24576, true
	case "xhigh":
		return 32768, true
	default:
		return -1, true // Unknown effort = auto
	}
}

// MapBudgetToEffort converts token budget to reasoning effort string.
// Used when generating OpenAI requests with reasoning_effort parameter.
// defaultForZero is returned when budget <= 0 (typically "auto" for Chat Completions, "low" for Responses API).
func MapBudgetToEffort(budget int, defaultForZero string) string {
	if budget <= 0 {
		return defaultForZero
	}
	if budget <= 1024 {
		return "low"
	}
	if budget <= 8192 {
		return "medium"
	}
	return "high"
}

// DefaultGeminiSafetySettings returns the default safety settings for Gemini API
func DefaultGeminiSafetySettings() []map[string]string {
	return []map[string]string{
		{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "BLOCK_NONE"},
	}
}

// CleanJsonSchema removes fields not supported by Gemini from JSON Schema.
// This is a simplified version - for more advanced cleaning (handling $ref, allOf, anyOf, etc.)
// use util.CleanJSONSchemaForGemini() on the JSON string.
func CleanJsonSchema(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}
	// Remove unsupported top-level keywords
	unsupportedKeywords := []string{
		"strict", "input_examples", "$schema", "$id", "$defs", "definitions",
		"additionalProperties", "patternProperties", "unevaluatedProperties",
		"minProperties", "maxProperties", "dependentRequired", "dependentSchemas",
		"if", "then", "else", "not", "contentEncoding", "contentMediaType",
		"deprecated", "readOnly", "writeOnly", "examples", "$comment",
		"$vocabulary", "$anchor", "$dynamicRef", "$dynamicAnchor",
	}
	for _, kw := range unsupportedKeywords {
		delete(schema, kw)
	}

	// Recursively clean nested schemas
	cleanNestedSchemas(schema)

	return schema
}

// cleanNestedSchemas recursively cleans nested schema objects.
func cleanNestedSchemas(schema map[string]interface{}) {
	// Clean properties
	if props, ok := schema["properties"].(map[string]interface{}); ok {
		for _, v := range props {
			if propSchema, ok := v.(map[string]interface{}); ok {
				CleanJsonSchema(propSchema)
			}
		}
	}

	// Clean items (for arrays)
	if items, ok := schema["items"].(map[string]interface{}); ok {
		CleanJsonSchema(items)
	}

	// Clean allOf, anyOf, oneOf
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		if arr, ok := schema[key].([]interface{}); ok {
			for _, item := range arr {
				if itemSchema, ok := item.(map[string]interface{}); ok {
					CleanJsonSchema(itemSchema)
				}
			}
		}
	}

	// Flatten type arrays like ["string", "null"] to just "string"
	if typeVal, ok := schema["type"].([]interface{}); ok && len(typeVal) > 0 {
		// Find first non-null type
		for _, t := range typeVal {
			if tStr, ok := t.(string); ok && tStr != "null" {
				schema["type"] = tStr
				break
			}
		}
	}
}

// GenToolCallID generates a unique tool call ID.
func GenToolCallID() string { return GenToolCallIDWithName("call") }

// GenToolCallIDWithName generates a unique tool call ID with the given function name.
func GenToolCallIDWithName(name string) string {
	return fmt.Sprintf("%s-%s", name, GenerateUUID()[:8])
}

// GenClaudeToolCallID generates a Claude-compatible tool call ID.
func GenClaudeToolCallID() string { return GenToolCallIDWithName("toolu") }

// GenClaudeToolCallIDWithName generates a Claude-compatible tool call ID with function name.
func GenClaudeToolCallIDWithName(name string) string {
	return fmt.Sprintf("%s-%s", name, GenerateUUID()[:8])
}

// GenerateUUID generates a UUID v4 string.
func GenerateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// SanitizeText cleans text for safe use in API payloads.
func SanitizeText(s string) string {
	if s == "" || (utf8.ValidString(s) && !hasProblematicChars(s)) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		if r == 0 || (r < 0x20 && r != '\t' && r != '\n' && r != '\r') {
			i += size
			continue
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

func hasProblematicChars(s string) bool {
	for _, r := range s {
		if r == 0 || (r < 0x20 && r != '\t' && r != '\n' && r != '\r') {
			return true
		}
	}
	return false
}

// SanitizeUTF8 is an alias for SanitizeText.
// Deprecated: Use SanitizeText instead.
func SanitizeUTF8(s string) string { return SanitizeText(s) }

// MapGeminiFinishReason converts Gemini finishReason to FinishReason.
func MapGeminiFinishReason(geminiReason string) FinishReason {
	switch strings.ToUpper(geminiReason) {
	case "STOP", "FINISH_REASON_UNSPECIFIED", "UNKNOWN":
		return FinishReasonStop
	case "MAX_TOKENS":
		return FinishReasonLength
	case "SAFETY", "RECITATION":
		return FinishReasonContentFilter
	case "MALFORMED_FUNCTION_CALL":
		// Treat as tool_calls - we'll parse the malformed call separately
		return FinishReasonToolCalls
	default:
		return FinishReasonUnknown
	}
}

// ParseMalformedFunctionCall extracts function name and arguments from Gemini's
// MALFORMED_FUNCTION_CALL finishMessage. This is a workaround for a known Gemini bug
// where the model generates text-based function calls like:
// "call:default_api:list_dir{path:\"src/server\"}" instead of proper JSON.
// Returns (funcName, argsJSON, ok).
func ParseMalformedFunctionCall(finishMessage string) (string, string, bool) {
	// Format: "Malformed function call: call:default_api:func_name{key:\"value\",key2:123}"
	// or just: "call:default_api:func_name{...}"

	// Find the call pattern - look for ": call:" to find the actual function call
	// (avoids matching "function call:" which appears earlier in the message)
	idx := strings.Index(finishMessage, ": call:")
	if idx != -1 {
		idx += 2 // skip ": ", point to 'c' in "call:"
	} else {
		// Try at the beginning of string
		if strings.HasPrefix(finishMessage, "call:") {
			idx = 0
		} else {
			// Last resort: find last occurrence of "call:"
			idx = strings.LastIndex(finishMessage, "call:")
			if idx == -1 {
				return "", "", false
			}
		}
	}

	// Extract from "call:" onwards
	callPart := finishMessage[idx:]

	// Find function name - format is "call:default_api:func_name{...}"
	// Skip "call:" prefix
	rest := callPart[5:] // skip "call:"

	// Skip the API namespace (e.g., "default_api:")
	colonIdx := strings.Index(rest, ":")
	if colonIdx == -1 {
		return "", "", false
	}
	rest = rest[colonIdx+1:] // skip "default_api:"

	// Find the opening brace
	braceIdx := strings.Index(rest, "{")
	if braceIdx == -1 {
		return "", "", false
	}

	funcName := rest[:braceIdx]
	argsRaw := rest[braceIdx:]

	// Find matching closing brace
	depth := 0
	endIdx := -1
	for i, c := range argsRaw {
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				endIdx = i + 1
				break
			}
		}
	}
	if endIdx == -1 {
		return "", "", false
	}
	argsRaw = argsRaw[:endIdx]

	// Convert the pseudo-JSON to valid JSON
	// The format uses unquoted keys: {path:"value"} -> {"path":"value"}
	argsJSON := convertMalformedArgsToJSON(argsRaw)

	return funcName, argsJSON, true
}

// convertMalformedArgsToJSON converts Gemini's malformed args format to valid JSON.
// Uses hujson library to handle "human JSON" (unquoted keys, trailing commas, etc.)
// Input: {path:"src/server",count:123,flag:true}
// Output: {"path":"src/server","count":123,"flag":true}
func convertMalformedArgsToJSON(argsRaw string) string {
	if argsRaw == "{}" || argsRaw == "" {
		return "{}"
	}

	// Use hujson to standardize the malformed JSON
	// hujson handles: unquoted keys, trailing commas, comments, etc.
	standardized, err := hujson.Standardize([]byte(argsRaw))
	if err != nil {
		// If hujson fails, fall back to manual repair
		return convertMalformedArgsToJSONFallback(argsRaw)
	}

	return string(standardized)
}

// convertMalformedArgsToJSONFallback is a fallback parser when hujson fails.
// Uses a simple state machine to add quotes around unquoted keys.
func convertMalformedArgsToJSONFallback(argsRaw string) string {
	var result strings.Builder
	result.Grow(len(argsRaw) + 20)

	inString := false
	escaped := false

	for i := 0; i < len(argsRaw); i++ {
		c := argsRaw[i]

		if escaped {
			result.WriteByte(c)
			escaped = false
			continue
		}

		if c == '\\' && inString {
			result.WriteByte(c)
			escaped = true
			continue
		}

		if c == '"' {
			inString = !inString
			result.WriteByte(c)
			continue
		}

		if inString {
			result.WriteByte(c)
			continue
		}

		// Outside string - look for unquoted keys
		if c == '{' || c == ',' {
			result.WriteByte(c)
			// Skip whitespace
			for i+1 < len(argsRaw) && (argsRaw[i+1] == ' ' || argsRaw[i+1] == '\t' || argsRaw[i+1] == '\n') {
				i++
			}
			// Check if next is a key (not a quote, not closing brace)
			if i+1 < len(argsRaw) && argsRaw[i+1] != '"' && argsRaw[i+1] != '}' {
				// Find the colon to get the key
				keyStart := i + 1
				keyEnd := keyStart
				for keyEnd < len(argsRaw) && argsRaw[keyEnd] != ':' && argsRaw[keyEnd] != ' ' {
					keyEnd++
				}
				if keyEnd < len(argsRaw) && keyStart < keyEnd {
					key := argsRaw[keyStart:keyEnd]
					result.WriteByte('"')
					result.WriteString(key)
					result.WriteByte('"')
					i = keyEnd - 1 // -1 because loop will increment
				}
			}
			continue
		}

		result.WriteByte(c)
	}

	return result.String()
}

// MapClaudeFinishReason converts Claude stop_reason to FinishReason.
func MapClaudeFinishReason(claudeReason string) FinishReason {
	switch claudeReason {
	case "end_turn", "stop_sequence":
		return FinishReasonStop
	case "max_tokens":
		return FinishReasonLength
	case "tool_use":
		return FinishReasonToolCalls
	default:
		return FinishReasonUnknown
	}
}

// MapOpenAIFinishReason converts OpenAI finish_reason to FinishReason.
func MapOpenAIFinishReason(openaiReason string) FinishReason {
	switch openaiReason {
	case "stop":
		return FinishReasonStop
	case "length":
		return FinishReasonLength
	case "tool_calls", "function_call":
		return FinishReasonToolCalls
	case "content_filter":
		return FinishReasonContentFilter
	default:
		return FinishReasonUnknown
	}
}

// MapFinishReasonToOpenAI converts FinishReason to OpenAI format.
func MapFinishReasonToOpenAI(reason FinishReason) string {
	switch reason {
	case FinishReasonLength:
		return "length"
	case FinishReasonToolCalls:
		return "tool_calls"
	case FinishReasonContentFilter:
		return "content_filter"
	default:
		return "stop"
	}
}

// MapStandardRole maps standard role strings to IR Role.
func MapStandardRole(role string) Role {
	switch role {
	case "system":
		return RoleSystem
	case "assistant":
		return RoleAssistant
	case "tool":
		return RoleTool
	default:
		return RoleUser
	}
}

// MapFinishReasonToClaude converts FinishReason to Claude format.
func MapFinishReasonToClaude(reason FinishReason) string {
	switch reason {
	case FinishReasonLength:
		return "max_tokens"
	case FinishReasonToolCalls:
		return "tool_use"
	default:
		return "end_turn"
	}
}

// MapFinishReasonToGemini converts FinishReason to Gemini format.
func MapFinishReasonToGemini(reason FinishReason) string {
	switch reason {
	case FinishReasonStop, FinishReasonToolCalls:
		return "STOP"
	case FinishReasonLength:
		return "MAX_TOKENS"
	case FinishReasonContentFilter:
		return "SAFETY"
	default:
		return "OTHER"
	}
}

// HasThoughtSignatureOnly checks if a Gemini part contains only thoughtSignature.
func HasThoughtSignatureOnly(thoughtSig, thoughtSigSnake, text, functionCall, inlineData, inlineDataSnake interface{}) bool {
	getString := func(r interface{}) string {
		if r == nil {
			return ""
		}
		if sg, ok := r.(interface{ String() string }); ok {
			return sg.String()
		}
		return ""
	}
	exists := func(r interface{}) bool {
		if r == nil {
			return false
		}
		if ec, ok := r.(interface{ Exists() bool }); ok {
			return ec.Exists()
		}
		return false
	}

	hasThoughtSig := (exists(thoughtSig) && getString(thoughtSig) != "") ||
		(exists(thoughtSigSnake) && getString(thoughtSigSnake) != "")

	if !hasThoughtSig {
		return false
	}

	return !(exists(text) || exists(functionCall) || exists(inlineData) || exists(inlineDataSnake))
}

// =============================================================================
// Claude JSON Schema Cleaning
// =============================================================================

// CleanJsonSchemaForClaude prepares JSON Schema for Claude API compatibility.
// This is a wrapper that calls cleanSchemaForClaudeRecursive and adds required fields.
func CleanJsonSchemaForClaude(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}
	schema = CleanJsonSchema(schema)
	cleanSchemaForClaudeRecursive(schema)
	schema["additionalProperties"] = false
	schema["$schema"] = "http://json-schema.org/draft-07/schema#"
	return schema
}

// cleanSchemaForClaudeRecursive recursively removes JSON Schema fields that Claude API doesn't support.
// Claude uses JSON Schema draft 2020-12 but doesn't support all features.
// See: https://docs.anthropic.com/en/docs/build-with-claude/tool-use
func cleanSchemaForClaudeRecursive(schema map[string]interface{}) {
	if schema == nil {
		return
	}

	// CRITICAL: Convert "const" to "enum" before deletion
	// Claude doesn't support "const" but supports "enum" with single value
	// This preserves discriminator semantics (e.g., Pydantic Literal types)
	if constVal, ok := schema["const"]; ok {
		schema["enum"] = []interface{}{constVal}
		delete(schema, "const")
	}

	// CRITICAL: Handle "anyOf" by taking the first element
	// This preserves type information instead of losing it completely
	// Example: {"anyOf": [{"type": "string"}, {"type": "null"}]} → {"type": "string"}
	if anyOf, ok := schema["anyOf"].([]interface{}); ok && len(anyOf) > 0 {
		if firstItem, ok := anyOf[0].(map[string]interface{}); ok {
			// Merge first anyOf item into schema
			for k, v := range firstItem {
				schema[k] = v
			}
		}
		delete(schema, "anyOf")
	}

	// Handle "oneOf" similarly - take first element
	if oneOf, ok := schema["oneOf"].([]interface{}); ok && len(oneOf) > 0 {
		if firstItem, ok := oneOf[0].(map[string]interface{}); ok {
			for k, v := range firstItem {
				schema[k] = v
			}
		}
		delete(schema, "oneOf")
	}

	// Lowercase type fields for consistency
	if typeVal, ok := schema["type"].(string); ok {
		schema["type"] = strings.ToLower(typeVal)
	}

	// Fields that Claude doesn't support in JSON Schema
	// Based on JSON Schema draft 2020-12 compatibility
	unsupportedFields := []string{
		// Composition keywords - anyOf/oneOf handled above, others just deleted
		"allOf", "not",
		// Snake_case variants
		"any_of", "one_of", "all_of",
		// Reference keywords
		"$ref", "$defs", "definitions", "$id", "$anchor", "$dynamicRef", "$dynamicAnchor",
		// Schema metadata
		"$schema", "$vocabulary", "$comment",
		// Conditional keywords
		"if", "then", "else", "dependentSchemas", "dependentRequired",
		// Unevaluated keywords
		"unevaluatedItems", "unevaluatedProperties",
		// Content keywords
		"contentEncoding", "contentMediaType", "contentSchema",
		// Deprecated keywords
		"dependencies",
		// Array validation keywords that may not be supported
		"minItems", "maxItems", "uniqueItems", "minContains", "maxContains",
		// String validation keywords that may cause issues
		"minLength", "maxLength", "pattern", "format",
		// Number validation keywords
		"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
		// Object validation keywords that may cause issues
		"minProperties", "maxProperties",
		// Default values - Claude officially doesn't support in input_schema
		"default",
	}

	for _, field := range unsupportedFields {
		delete(schema, field)
	}

	// Recursively clean nested objects in properties
	if properties, ok := schema["properties"].(map[string]interface{}); ok {
		for key, prop := range properties {
			if propMap, ok := prop.(map[string]interface{}); ok {
				cleanSchemaForClaudeRecursive(propMap)
				properties[key] = propMap
			}
		}
	}

	// Clean items - can be object or array
	if items := schema["items"]; items != nil {
		switch v := items.(type) {
		case map[string]interface{}:
			cleanSchemaForClaudeRecursive(v)
		case []interface{}:
			for i, item := range v {
				if itemMap, ok := item.(map[string]interface{}); ok {
					cleanSchemaForClaudeRecursive(itemMap)
					v[i] = itemMap
				}
			}
		}
	}

	// Handle prefixItems (tuple validation)
	if prefixItems, ok := schema["prefixItems"].([]interface{}); ok {
		for i, item := range prefixItems {
			if itemMap, ok := item.(map[string]interface{}); ok {
				cleanSchemaForClaudeRecursive(itemMap)
				prefixItems[i] = itemMap
			}
		}
	}

	// Handle additionalProperties if it's an object
	if addProps, ok := schema["additionalProperties"].(map[string]interface{}); ok {
		cleanSchemaForClaudeRecursive(addProps)
	}

	// Handle patternProperties
	if patternProps, ok := schema["patternProperties"].(map[string]interface{}); ok {
		for key, prop := range patternProps {
			if propMap, ok := prop.(map[string]interface{}); ok {
				cleanSchemaForClaudeRecursive(propMap)
				patternProps[key] = propMap
			}
		}
	}

	// Handle propertyNames
	if propNames, ok := schema["propertyNames"].(map[string]interface{}); ok {
		cleanSchemaForClaudeRecursive(propNames)
	}

	// Handle contains
	if contains, ok := schema["contains"].(map[string]interface{}); ok {
		cleanSchemaForClaudeRecursive(contains)
	}
}
