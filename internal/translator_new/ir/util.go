package ir

import (
	"crypto/rand"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tailscale/hujson"
)

// =============================================================================
// UUID Generation
// =============================================================================

// GenerateUUID generates a UUID v4 string.
func GenerateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// =============================================================================
// Tool Call ID Generation
// =============================================================================

// GenToolCallID generates a unique tool call ID with default prefix "call".
func GenToolCallID() string {
	return GenToolCallIDWithName("call")
}

// GenToolCallIDWithName generates a unique tool call ID with the given function name.
func GenToolCallIDWithName(name string) string {
	return fmt.Sprintf("%s-%s", name, GenerateUUID()[:8])
}

// GenClaudeToolCallID generates a Claude-compatible tool call ID with default prefix "toolu".
func GenClaudeToolCallID() string {
	return GenClaudeToolCallIDWithName("toolu")
}

// GenClaudeToolCallIDWithName generates a Claude-compatible tool call ID with function name.
func GenClaudeToolCallIDWithName(name string) string {
	return fmt.Sprintf("%s-%s", name, GenerateUUID()[:8])
}

// =============================================================================
// Text Sanitization
// =============================================================================

// SanitizeText cleans text for safe use in API payloads.
// It removes invalid UTF-8 sequences and control characters (except tab, newline, carriage return).
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

// SanitizeUTF8 is an alias for SanitizeText.
// Deprecated: Use SanitizeText instead.
func SanitizeUTF8(s string) string { return SanitizeText(s) }

func hasProblematicChars(s string) bool {
	for _, r := range s {
		if r == 0 || (r < 0x20 && r != '\t' && r != '\n' && r != '\r') {
			return true
		}
	}
	return false
}

// =============================================================================
// JSON Schema Cleaning
// =============================================================================

// CleanJsonSchema removes fields not supported by Gemini from JSON Schema.
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
		"propertyNames",
	}
	for _, kw := range unsupportedKeywords {
		delete(schema, kw)
	}

	cleanNestedSchemas(schema)
	return schema
}

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
		for _, t := range typeVal {
			if tStr, ok := t.(string); ok && tStr != "null" {
				schema["type"] = tStr
				break
			}
		}
	}
}

// CleanJsonSchemaForClaude prepares JSON Schema for Claude API compatibility.
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

func cleanSchemaForClaudeRecursive(schema map[string]interface{}) {
	if schema == nil {
		return
	}

	// Convert "const" to "enum"
	if constVal, ok := schema["const"]; ok {
		schema["enum"] = []interface{}{constVal}
		delete(schema, "const")
	}

	// Handle "anyOf" / "oneOf" by taking the first element
	for _, key := range []string{"anyOf", "oneOf"} {
		if arr, ok := schema[key].([]interface{}); ok && len(arr) > 0 {
			if firstItem, ok := arr[0].(map[string]interface{}); ok {
				for k, v := range firstItem {
					schema[k] = v
				}
			}
			delete(schema, key)
		}
	}

	// Lowercase type fields
	if typeVal, ok := schema["type"].(string); ok {
		schema["type"] = strings.ToLower(typeVal)
	}

	// Remove unsupported fields
	unsupportedFields := []string{
		"allOf", "not",
		"any_of", "one_of", "all_of",
		"$ref", "$defs", "definitions", "$id", "$anchor", "$dynamicRef", "$dynamicAnchor",
		"$schema", "$vocabulary", "$comment",
		"if", "then", "else", "dependentSchemas", "dependentRequired",
		"unevaluatedItems", "unevaluatedProperties",
		"contentEncoding", "contentMediaType", "contentSchema",
		"dependencies",
		"minItems", "maxItems", "uniqueItems", "minContains", "maxContains",
		"minLength", "maxLength", "pattern", "format",
		"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
		"minProperties", "maxProperties",
		"default",
	}
	for _, field := range unsupportedFields {
		delete(schema, field)
	}

	// Recursively clean properties
	if properties, ok := schema["properties"].(map[string]interface{}); ok {
		for key, prop := range properties {
			if propMap, ok := prop.(map[string]interface{}); ok {
				cleanSchemaForClaudeRecursive(propMap)
				properties[key] = propMap
			}
		}
	}

	// Clean items
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

	// Handle prefixItems, additionalProperties, patternProperties, propertyNames, contains
	if prefixItems, ok := schema["prefixItems"].([]interface{}); ok {
		for i, item := range prefixItems {
			if itemMap, ok := item.(map[string]interface{}); ok {
				cleanSchemaForClaudeRecursive(itemMap)
				prefixItems[i] = itemMap
			}
		}
	}
	if addProps, ok := schema["additionalProperties"].(map[string]interface{}); ok {
		cleanSchemaForClaudeRecursive(addProps)
	}
	if patternProps, ok := schema["patternProperties"].(map[string]interface{}); ok {
		for key, prop := range patternProps {
			if propMap, ok := prop.(map[string]interface{}); ok {
				cleanSchemaForClaudeRecursive(propMap)
				patternProps[key] = propMap
			}
		}
	}
	if propNames, ok := schema["propertyNames"].(map[string]interface{}); ok {
		cleanSchemaForClaudeRecursive(propNames)
	}
	if contains, ok := schema["contains"].(map[string]interface{}); ok {
		cleanSchemaForClaudeRecursive(contains)
	}
}

// =============================================================================
// Malformed Function Call Parsing (Gemini Workaround)
// =============================================================================

// ParseMalformedFunctionCall extracts function name and arguments from Gemini's MALFORMED_FUNCTION_CALL.
func ParseMalformedFunctionCall(finishMessage string) (string, string, bool) {
	// Find "call:" marker
	idx := strings.LastIndex(finishMessage, "call:")
	if idx == -1 {
		// Fallback: try finding at start or after ": "
		idx = strings.Index(finishMessage, ": call:")
		if idx != -1 {
			idx += 2
		} else if strings.HasPrefix(finishMessage, "call:") {
			idx = 0
		} else {
			return "", "", false
		}
	}

	// Extract content after "call:"
	rest := finishMessage[idx+5:]

	// Skip namespace (e.g., "default_api:")
	if colonIdx := strings.Index(rest, ":"); colonIdx != -1 {
		rest = rest[colonIdx+1:]
	} else {
		return "", "", false
	}

	// Find opening brace
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

	return funcName, convertMalformedArgsToJSON(argsRaw), true
}

func convertMalformedArgsToJSON(argsRaw string) string {
	if argsRaw == "{}" || argsRaw == "" {
		return "{}"
	}
	// Try hujson standardizer
	if standardized, err := hujson.Standardize([]byte(argsRaw)); err == nil {
		return string(standardized)
	}
	// Fallback to manual repair
	return convertMalformedArgsToJSONFallback(argsRaw)
}

func convertMalformedArgsToJSONFallback(argsRaw string) string {
	var result strings.Builder
	result.Grow(len(argsRaw) + 20)
	inString, escaped := false, false

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

		// Handle keys
		if c == '{' || c == ',' {
			result.WriteByte(c)
			// Skip whitespace
			for i+1 < len(argsRaw) && (argsRaw[i+1] == ' ' || argsRaw[i+1] == '\t' || argsRaw[i+1] == '\n') {
				i++
			}
			// Check if next token is an unquoted key
			if i+1 < len(argsRaw) && argsRaw[i+1] != '"' && argsRaw[i+1] != '}' {
				keyStart := i + 1
				keyEnd := keyStart
				for keyEnd < len(argsRaw) && argsRaw[keyEnd] != ':' && argsRaw[keyEnd] != ' ' {
					keyEnd++
				}
				if keyEnd < len(argsRaw) && keyStart < keyEnd {
					result.WriteByte('"')
					result.WriteString(argsRaw[keyStart:keyEnd])
					result.WriteByte('"')
					i = keyEnd - 1
				}
			}
			continue
		}
		result.WriteByte(c)
	}
	return result.String()
}

// =============================================================================
// Mapping Helpers
// =============================================================================

// DefaultGeminiSafetySettings returns the default safety settings for Gemini API.
func DefaultGeminiSafetySettings() []map[string]string {
	return []map[string]string{
		{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "BLOCK_NONE"},
	}
}

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
		return FinishReasonToolCalls
	default:
		return FinishReasonUnknown
	}
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
// Token Estimation and Budget Mapping
// =============================================================================

// EstimateTokenCount estimates token count from text (~4 chars/token).
func EstimateTokenCount(text string) int {
	if text == "" {
		return 0
	}
	// Conservative estimate: ~3 chars per token
	return (utf8.RuneCountInString(text) + 2) / 3
}

// MapEffortToBudget converts reasoning effort string to token budget.
// Returns (budget, includeThoughts).
func MapEffortToBudget(effort string) (int, bool) {
	switch effort {
	case "none":
		return 0, false
	case "auto":
		return -1, true
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
		return -1, true
	}
}

// MapBudgetToEffort converts token budget to reasoning effort string.
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
