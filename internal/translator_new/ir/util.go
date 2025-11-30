package ir

import (
	"crypto/rand"
	"fmt"
	"strings"
	"unicode/utf8"

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
	case "low", "minimal":
		return 1024, true
	case "medium":
		return 8192, true
	case "high":
		return 32768, true
	case "xhigh":
		return 65536, true
	default:
		return -1, true // auto = let provider decide
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
func CleanJsonSchema(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}
	delete(schema, "strict")
	delete(schema, "input_examples")
	return schema
}

// CleanJsonSchemaForClaude prepares JSON Schema for Claude API compatibility.
func CleanJsonSchemaForClaude(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}
	schema = CleanJsonSchema(schema)
	schema["additionalProperties"] = false
	schema["$schema"] = "http://json-schema.org/draft-07/schema#"
	lowercaseTypeFields(schema)
	return schema
}

func lowercaseTypeFields(obj map[string]interface{}) {
	for key, value := range obj {
		if key == "type" {
			if strVal, ok := value.(string); ok {
				obj[key] = strings.ToLower(strVal)
			}
		} else if nested, ok := value.(map[string]interface{}); ok {
			lowercaseTypeFields(nested)
		} else if arr, ok := value.([]interface{}); ok {
			for _, item := range arr {
				if nestedMap, ok := item.(map[string]interface{}); ok {
					lowercaseTypeFields(nestedMap)
				}
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
