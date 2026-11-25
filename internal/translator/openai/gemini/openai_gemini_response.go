// Package gemini provides response translation functionality for OpenAI to Gemini API.
// This package handles the conversion of OpenAI Chat Completions API responses into Gemini API-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Gemini API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, and usage metadata appropriately.
package gemini

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// ConvertOpenAIResponseToGeminiParams holds parameters for response conversion
type ConvertOpenAIResponseToGeminiParams struct {
	// Tool calls accumulator for streaming
	ToolCallsAccumulator map[int]*ToolCallAccumulator
	// Content accumulator for streaming
	ContentAccumulator strings.Builder
	// Track if this is the first chunk
	IsFirstChunk bool
}

// ToolCallAccumulator holds the state for accumulating tool call data
type ToolCallAccumulator struct {
	ID               string
	Name             string
	Arguments        strings.Builder
	ThoughtSignature string
}

// ConvertOpenAIResponseToGemini converts OpenAI Chat Completions streaming response format to Gemini API format.
// This function processes OpenAI streaming chunks and transforms them into Gemini-compatible JSON responses.
// It handles text content, tool calls, and usage metadata, outputting responses that match the Gemini API format.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON response from the OpenAI API.
//   - param: A pointer to a parameter object for the conversion.
//
// Returns:
//   - [][]byte: A slice of Gemini-compatible JSON responses.
func ConvertOpenAIResponseToGemini(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &ConvertOpenAIResponseToGeminiParams{
			ToolCallsAccumulator: nil,
			ContentAccumulator:   strings.Builder{},
			IsFirstChunk:         false,
		}
	}

	// Handle [DONE] marker
	if bytes.Equal(bytes.TrimSpace(rawJSON), []byte("[DONE]")) {
		return [][]byte{}
	}

	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}

	root := util.ParseGJSONBytesNoCopy(rawJSON)

	// Initialize accumulators if needed
	if (*param).(*ConvertOpenAIResponseToGeminiParams).ToolCallsAccumulator == nil {
		(*param).(*ConvertOpenAIResponseToGeminiParams).ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
	}

	// Process choices
	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		// Handle empty choices array (usage-only chunk)
		if len(choices.Array()) == 0 {
			// This is a usage-only chunk, handle usage and return
			if usage := root.Get("usage"); usage.Exists() {
				templateDoc := ojson.NewDocString(`{"candidates":[],"usageMetadata":{}}`)

				// Set model if available
				if model := root.Get("model"); model.Exists() {
					_ = templateDoc.Set("model", model.String())
				}

				setGeminiUsageMetadataFromOpenAIUsageDoc(templateDoc, usage)
				return [][]byte{templateDoc.Bytes()}
			}
			return [][]byte{}
		}

		var results [][]byte

		choices.ForEach(func(choiceIndex, choice ojson.Result) bool {
			// Base Gemini response template without finishReason; set when known
			template := []byte(`{"candidates":[{"content":{"parts":[],"role":"model"},"index":0}]}`)

			// Set model if available
			if model := root.Get("model"); model.Exists() {
				template, _ = ojson.SetBytes(template, "model", model.String())
			}

			_ = int(choice.Get("index").Int()) // choiceIdx not used in streaming
			delta := choice.Get("delta")
			baseTemplate := append([]byte(nil), template...)

			// Handle role (only in first chunk)
			if role := delta.Get("role"); role.Exists() && (*param).(*ConvertOpenAIResponseToGeminiParams).IsFirstChunk {
				// OpenAI assistant -> Gemini model
				if role.String() == "assistant" {
					template, _ = ojson.SetBytes(template, "candidates.0.content.role", "model")
				}
				(*param).(*ConvertOpenAIResponseToGeminiParams).IsFirstChunk = false
				results = append(results, template)
				return true
			}

			var chunkOutputs [][]byte

			// Handle reasoning/thinking delta
			for _, reasoningText := range collectOpenAIObjectReasoningTexts(delta) {
				if reasoningText == "" {
					continue
				}
				reasoningDoc := ojson.NewDoc(baseTemplate)
				_ = reasoningDoc.Set("candidates.0.content.parts.0.thought", true)
				_ = reasoningDoc.Set("candidates.0.content.parts.0.text", reasoningText)
				chunkOutputs = append(chunkOutputs, reasoningDoc.Bytes())
			}

			// Handle content delta
			forEachOpenAIContentSegment(delta.Get("content"), func(isReasoning bool, segText string) {
				if segText == "" {
					return
				}
				partDoc := ojson.NewDoc(baseTemplate)
				if isReasoning {
					_ = partDoc.Set("candidates.0.content.parts.0.thought", true)
					_ = partDoc.Set("candidates.0.content.parts.0.text", segText)
				} else {
					(*param).(*ConvertOpenAIResponseToGeminiParams).ContentAccumulator.WriteString(segText)
					_ = partDoc.Set("candidates.0.content.parts.0.text", segText)
				}
				chunkOutputs = append(chunkOutputs, partDoc.Bytes())
			})

			if len(chunkOutputs) > 0 {
				results = append(results, chunkOutputs...)
				finishReason := choice.Get("finish_reason")
				if !delta.Get("tool_calls").Exists() && (finishReason.Type != ojson.String || finishReason.String() == "") {
					return true
				}
			}

			// Handle tool calls delta
			if toolCalls := delta.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
				toolCalls.ForEach(func(_, toolCall ojson.Result) bool {
					toolIndex := int(toolCall.Get("index").Int())
					toolID := toolCall.Get("id").String()
					toolType := toolCall.Get("type").String()
					function := toolCall.Get("function")
					thoughtSig := openAIToolCallThoughtSignature(toolCall)

					// Skip non-function tool calls explicitly marked as other types.
					if toolType != "" && toolType != "function" {
						return true
					}

					// OpenAI streaming deltas may omit the type field while still carrying function data.
					if !function.Exists() && thoughtSig == "" {
						return true
					}

					functionName := function.Get("name").String()
					functionArgs := function.Get("arguments").String()

					// Initialize accumulator if needed so later deltas without type can append arguments.
					if _, exists := (*param).(*ConvertOpenAIResponseToGeminiParams).ToolCallsAccumulator[toolIndex]; !exists {
						(*param).(*ConvertOpenAIResponseToGeminiParams).ToolCallsAccumulator[toolIndex] = &ToolCallAccumulator{
							ID:   strings.Clone(toolID),
							Name: strings.Clone(functionName),
						}
					}

					acc := (*param).(*ConvertOpenAIResponseToGeminiParams).ToolCallsAccumulator[toolIndex]

					// Update ID if provided
					if toolID != "" {
						acc.ID = strings.Clone(toolID)
					}

					// Update name if provided
					if functionName != "" {
						acc.Name = strings.Clone(functionName)
					}

					// Accumulate arguments
					if functionArgs != "" {
						acc.Arguments.WriteString(functionArgs)
					}

					if thoughtSig != "" {
						acc.ThoughtSignature = strings.Clone(thoughtSig)
					}

					return true
				})

				if finishReason := choice.Get("finish_reason"); finishReason.Type != ojson.String || finishReason.String() == "" {
					// Don't output anything for tool call deltas - wait for completion
					return true
				}
			}

			// Handle finish reason
			if finishReason := choice.Get("finish_reason"); finishReason.Type == ojson.String && finishReason.String() != "" {
				geminiFinishReason := mapOpenAIFinishReasonToGemini(finishReason.String())
				templateDoc := ojson.NewDoc(template)
				_ = templateDoc.Set("candidates.0.finishReason", geminiFinishReason)

				// If we have accumulated tool calls, output them now
				if len((*param).(*ConvertOpenAIResponseToGeminiParams).ToolCallsAccumulator) > 0 {
					toolIndices := make([]int, 0, len((*param).(*ConvertOpenAIResponseToGeminiParams).ToolCallsAccumulator))
					for toolIndex := range (*param).(*ConvertOpenAIResponseToGeminiParams).ToolCallsAccumulator {
						toolIndices = append(toolIndices, toolIndex)
					}
					sort.Ints(toolIndices)

					partIndex := 0
					for _, toolIndex := range toolIndices {
						accumulator := (*param).(*ConvertOpenAIResponseToGeminiParams).ToolCallsAccumulator[toolIndex]
						idPath := fmt.Sprintf("candidates.0.content.parts.%d.functionCall.id", partIndex)
						namePath := fmt.Sprintf("candidates.0.content.parts.%d.functionCall.name", partIndex)
						argsPath := fmt.Sprintf("candidates.0.content.parts.%d.functionCall.args", partIndex)
						if accumulator.ID != "" {
							_ = templateDoc.Set(idPath, accumulator.ID)
						}
						_ = templateDoc.Set(namePath, accumulator.Name)
						_ = templateDoc.SetRaw(argsPath, parseArgsToObjectRaw(accumulator.Arguments.String()))
						if accumulator.ThoughtSignature != "" {
							sigPath := fmt.Sprintf("candidates.0.content.parts.%d.thoughtSignature", partIndex)
							_ = templateDoc.Set(sigPath, accumulator.ThoughtSignature)
						}
						partIndex++
					}

					// Clear accumulators
					(*param).(*ConvertOpenAIResponseToGeminiParams).ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
				}

				results = append(results, templateDoc.Bytes())
				return true
			}

			// Handle usage information
			if usage := root.Get("usage"); usage.Exists() {
				templateDoc := ojson.NewDoc(template)
				setGeminiUsageMetadataFromOpenAIUsageDoc(templateDoc, usage)
				results = append(results, templateDoc.Bytes())
				return true
			}

			return true
		})
		return results
	}
	return [][]byte{}
}

func openAIToolCallThoughtSignature(toolCall ojson.Result) string {
	for _, path := range []string{
		"extra_content.google.thought_signature",
		"function.extra_content.google.thought_signature",
		"thoughtSignature",
		"thought_signature",
	} {
		if sig := toolCall.Get(path); sig.Exists() && sig.String() != "" {
			return sig.String()
		}
	}
	return ""
}

// mapOpenAIFinishReasonToGemini maps OpenAI finish reasons to Gemini finish reasons
func mapOpenAIFinishReasonToGemini(openAIReason string) string {
	switch openAIReason {
	case "stop":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	case "tool_calls":
		return "STOP" // Gemini doesn't have a specific tool_calls finish reason
	case "content_filter":
		return "SAFETY"
	default:
		return "STOP"
	}
}

// parseArgsToObjectRaw safely parses a JSON string of function arguments into an object JSON string.
// It returns "{}" if the input is empty or cannot be parsed as a JSON object.
func parseArgsToObjectRaw(argsStr string) string {
	trimmed := strings.TrimSpace(argsStr)
	if trimmed == "" || trimmed == "{}" {
		return "{}"
	}

	// First try strict JSON
	if ojson.Valid(trimmed) {
		strict := ojson.Parse(trimmed)
		if strict.IsObject() {
			return strict.Raw
		}
	}

	// Tolerant parse: handle streams where values are barewords (e.g., 北京, celsius)
	tolerant := tolerantParseJSONObjectRaw(trimmed)
	if tolerant != "{}" {
		return tolerant
	}

	// Fallback: return empty object when parsing fails
	return "{}"
}

func escapeSjsonPathKey(key string) string {
	key = strings.ReplaceAll(key, `\`, `\\`)
	key = strings.ReplaceAll(key, `.`, `\.`)
	return key
}

// tolerantParseJSONObjectRaw attempts to parse a JSON-like object string into a JSON object string, tolerating
// bareword values (unquoted strings) commonly seen during streamed tool calls.
// Example input: {"location": 北京, "unit": celsius}
func tolerantParseJSONObjectRaw(s string) string {
	// Ensure we operate within the outermost braces if present
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || start >= end {
		return "{}"
	}
	content := s[start+1 : end]

	runes := []rune(content)
	n := len(runes)
	i := 0
	resultDoc := ojson.NewDocString(`{}`)

	for i < n {
		// Skip whitespace and commas
		for i < n && (runes[i] == ' ' || runes[i] == '\n' || runes[i] == '\r' || runes[i] == '\t' || runes[i] == ',') {
			i++
		}
		if i >= n {
			break
		}

		// Expect quoted key
		if runes[i] != '"' {
			// Unable to parse this segment reliably; skip to next comma
			for i < n && runes[i] != ',' {
				i++
			}
			continue
		}

		// Parse JSON string for key
		keyToken, nextIdx := parseJSONStringRunes(runes, i)
		if nextIdx == -1 {
			break
		}
		keyName := jsonStringTokenToRawString(keyToken)
		sjsonKey := escapeSjsonPathKey(keyName)
		i = nextIdx

		// Skip whitespace
		for i < n && (runes[i] == ' ' || runes[i] == '\n' || runes[i] == '\r' || runes[i] == '\t') {
			i++
		}
		if i >= n || runes[i] != ':' {
			break
		}
		i++ // skip ':'
		// Skip whitespace
		for i < n && (runes[i] == ' ' || runes[i] == '\n' || runes[i] == '\r' || runes[i] == '\t') {
			i++
		}
		if i >= n {
			break
		}

		// Parse value (string, number, object/array, bareword)
		switch runes[i] {
		case '"':
			// JSON string
			valToken, ni := parseJSONStringRunes(runes, i)
			if ni == -1 {
				// Malformed; treat as empty string
				_ = resultDoc.Set(sjsonKey, "")
				i = n
			} else {
				_ = resultDoc.Set(sjsonKey, jsonStringTokenToRawString(valToken))
				i = ni
			}
		case '{', '[':
			// Bracketed value: attempt to capture balanced structure
			seg, ni := captureBracketed(runes, i)
			if ni == -1 {
				i = n
			} else {
				if ojson.Valid(seg) {
					_ = resultDoc.SetRaw(sjsonKey, seg)
				} else {
					_ = resultDoc.Set(sjsonKey, seg)
				}
				i = ni
			}
		default:
			// Bare token until next comma or end
			j := i
			for j < n && runes[j] != ',' {
				j++
			}
			token := strings.TrimSpace(string(runes[i:j]))
			// Interpret common JSON atoms and numbers; otherwise treat as string
			if token == "true" {
				_ = resultDoc.Set(sjsonKey, true)
			} else if token == "false" {
				_ = resultDoc.Set(sjsonKey, false)
			} else if token == "null" {
				_ = resultDoc.Set(sjsonKey, nil)
			} else if numVal, ok := tryParseNumber(token); ok {
				_ = resultDoc.Set(sjsonKey, numVal)
			} else {
				_ = resultDoc.Set(sjsonKey, token)
			}
			i = j
		}

		// Skip trailing whitespace and optional comma before next pair
		for i < n && (runes[i] == ' ' || runes[i] == '\n' || runes[i] == '\r' || runes[i] == '\t') {
			i++
		}
		if i < n && runes[i] == ',' {
			i++
		}
	}

	return resultDoc.String()
}

// parseJSONStringRunes returns the JSON string token (including quotes) and the index just after it.
func parseJSONStringRunes(runes []rune, start int) (string, int) {
	if start >= len(runes) || runes[start] != '"' {
		return "", -1
	}
	i := start + 1
	escaped := false
	for i < len(runes) {
		r := runes[i]
		if r == '\\' && !escaped {
			escaped = true
			i++
			continue
		}
		if r == '"' && !escaped {
			return string(runes[start : i+1]), i + 1
		}
		escaped = false
		i++
	}
	return string(runes[start:]), -1
}

// jsonStringTokenToRawString converts a JSON string token (including quotes) to a raw Go string value.
func jsonStringTokenToRawString(token string) string {
	r := ojson.Parse(token)
	if r.Type == ojson.String {
		return r.String()
	}
	// Fallback: strip surrounding quotes if present
	if len(token) >= 2 && token[0] == '"' && token[len(token)-1] == '"' {
		return token[1 : len(token)-1]
	}
	return token
}

// captureBracketed captures a balanced JSON object/array starting at index i.
// Returns the segment string and the index just after it; -1 if malformed.
func captureBracketed(runes []rune, i int) (string, int) {
	if i >= len(runes) {
		return "", -1
	}
	startRune := runes[i]
	var endRune rune
	if startRune == '{' {
		endRune = '}'
	} else if startRune == '[' {
		endRune = ']'
	} else {
		return "", -1
	}
	depth := 0
	j := i
	inStr := false
	escaped := false
	for j < len(runes) {
		r := runes[j]
		if inStr {
			if r == '\\' && !escaped {
				escaped = true
				j++
				continue
			}
			if r == '"' && !escaped {
				inStr = false
			} else {
				escaped = false
			}
			j++
			continue
		}
		if r == '"' {
			inStr = true
			j++
			continue
		}
		if r == startRune {
			depth++
		} else if r == endRune {
			depth--
			if depth == 0 {
				return string(runes[i : j+1]), j + 1
			}
		}
		j++
	}
	return string(runes[i:]), -1
}

// tryParseNumber attempts to parse a string as an int or float.
func tryParseNumber(s string) (interface{}, bool) {
	if s == "" {
		return nil, false
	}
	// Try integer
	if i64, errParseInt := strconv.ParseInt(s, 10, 64); errParseInt == nil {
		return i64, true
	}
	if u64, errParseUInt := strconv.ParseUint(s, 10, 64); errParseUInt == nil {
		return u64, true
	}
	if f64, errParseFloat := strconv.ParseFloat(s, 64); errParseFloat == nil {
		return f64, true
	}
	return nil, false
}

// ConvertOpenAIResponseToGeminiNonStream converts a non-streaming OpenAI response to a non-streaming Gemini response.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON response from the OpenAI API.
//   - param: A pointer to a parameter object for the conversion.
//
// Returns:
//   - []byte: A Gemini-compatible JSON response.
func ConvertOpenAIResponseToGeminiNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	root := util.ParseGJSONBytesNoCopy(rawJSON)

	// Base Gemini response template without finishReason; set when known
	outDoc := ojson.NewDocString(`{"candidates":[{"content":{"parts":[],"role":"model"},"index":0}]}`)

	// Set model if available
	if model := root.Get("model"); model.Exists() {
		_ = outDoc.Set("model", model.String())
	}

	// Process choices
	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		var candidates [][]byte
		candidatePos := 0
		choices.ForEach(func(_, choice ojson.Result) bool {
			choiceIdx := candidatePos
			if idx := choice.Get("index"); idx.Exists() && idx.Type == ojson.Number {
				choiceIdx = int(idx.Int())
			}
			candidatePos++
			message := choice.Get("message")

			candDoc := ojson.NewDocString(`{"content":{"parts":[],"role":"model"},"index":0}`)

			var parts [][]byte

			// Handle reasoning content before visible text
			for _, reasoningText := range collectOpenAIObjectReasoningTexts(message) {
				if reasoningText == "" {
					continue
				}
				partDoc := ojson.NewDocString(`{"thought":true,"text":""}`)
				_ = partDoc.Set("text", reasoningText)
				parts = append(parts, partDoc.Bytes())
			}

			// Handle visible and inline reasoning content segments
			forEachOpenAIContentSegment(message.Get("content"), func(isReasoning bool, segText string) {
				if segText == "" {
					return
				}
				var partDoc *ojson.Doc
				if isReasoning {
					partDoc = ojson.NewDocString(`{"thought":true,"text":""}`)
				} else {
					partDoc = ojson.NewDocString(`{"text":""}`)
				}
				_ = partDoc.Set("text", segText)
				parts = append(parts, partDoc.Bytes())
			})

			// Handle tool calls
			if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
				toolCalls.ForEach(func(_, toolCall ojson.Result) bool {
					if toolCall.Get("type").String() == "function" {
						function := toolCall.Get("function")
						functionName := function.Get("name").String()
						functionArgs := function.Get("arguments").String()
						functionID := toolCall.Get("id").String()

						partDoc := ojson.NewDocString(`{"functionCall":{"name":""}}`)
						if functionID != "" {
							_ = partDoc.Set("functionCall.id", functionID)
						}
						_ = partDoc.Set("functionCall.name", functionName)
						_ = partDoc.SetRaw("functionCall.args", parseArgsToObjectRaw(functionArgs))
						if thoughtSig := openAIToolCallThoughtSignature(toolCall); thoughtSig != "" {
							_ = partDoc.Set("thoughtSignature", thoughtSig)
						}
						parts = append(parts, partDoc.Bytes())
					}
					return true
				})
			}

			if len(parts) > 0 {
				_ = candDoc.SetRawBytes("content.parts", translatorcommon.JoinRawArray(parts))
			}

			// Handle finish reason
			if finishReason := choice.Get("finish_reason"); finishReason.Type == ojson.String && finishReason.String() != "" {
				geminiFinishReason := mapOpenAIFinishReasonToGemini(finishReason.String())
				_ = candDoc.Set("finishReason", geminiFinishReason)
			}

			// Set index
			_ = candDoc.Set("index", choiceIdx)

			candidates = append(candidates, candDoc.Bytes())
			return true
		})

		if len(candidates) > 0 {
			_ = outDoc.SetRawBytes("candidates", translatorcommon.JoinRawArray(candidates))
		}
	}

	// Handle usage information
	if usage := root.Get("usage"); usage.Exists() {
		setGeminiUsageMetadataFromOpenAIUsageDoc(outDoc, usage)
	}

	return outDoc.Bytes()
}

func GeminiTokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.GeminiTokenCountJSON(count)
}

func reasoningTokensFromUsage(usage ojson.Result) int64 {
	if usage.Exists() {
		if v := usage.Get("completion_tokens_details.reasoning_tokens"); v.Exists() {
			return v.Int()
		}
		if v := usage.Get("output_tokens_details.reasoning_tokens"); v.Exists() {
			return v.Int()
		}
	}
	return 0
}

func setGeminiUsageMetadataFromOpenAIUsageDoc(outDoc *ojson.Doc, usage ojson.Result) {
	promptTokens, hasPromptTokens := tokenCountFromUsage(usage, "prompt_tokens", "input_tokens")
	completionTokens, hasCompletionTokens := tokenCountFromUsage(usage, "completion_tokens", "output_tokens")
	totalTokens, hasTotalTokens := tokenCountFromUsage(usage, "total_tokens")
	if hasPromptTokens {
		_ = outDoc.Set("usageMetadata.promptTokenCount", promptTokens)
	}
	if hasCompletionTokens {
		_ = outDoc.Set("usageMetadata.candidatesTokenCount", completionTokens)
	}
	if hasTotalTokens {
		_ = outDoc.Set("usageMetadata.totalTokenCount", totalTokens)
	} else if hasPromptTokens || hasCompletionTokens {
		_ = outDoc.Set("usageMetadata.totalTokenCount", promptTokens+completionTokens)
	}
	if reasoningTokens := reasoningTokensFromUsage(usage); reasoningTokens > 0 {
		_ = outDoc.Set("usageMetadata.thoughtsTokenCount", reasoningTokens)
	}
	if cachedTokens := cachedTokensFromUsage(usage); cachedTokens > 0 {
		_ = outDoc.Set("usageMetadata.cachedContentTokenCount", cachedTokens)
	}
}

func tokenCountFromUsage(usage ojson.Result, paths ...string) (int64, bool) {
	for _, path := range paths {
		if v := usage.Get(path); v.Exists() {
			return v.Int(), true
		}
	}
	return 0, false
}

func cachedTokensFromUsage(usage ojson.Result) int64 {
	if usage.Exists() {
		if v := usage.Get("prompt_tokens_details.cached_tokens"); v.Exists() {
			return v.Int()
		}
		if v := usage.Get("input_tokens_details.cached_tokens"); v.Exists() {
			return v.Int()
		}
	}
	return 0
}

func collectOpenAIObjectReasoningTexts(obj ojson.Result) []string {
	if !obj.Exists() {
		return nil
	}
	for _, path := range []string{"reasoning_content", "reasoning", "reasoning_details"} {
		if texts := extractReasoningTexts(obj.Get(path)); len(texts) > 0 {
			return texts
		}
	}
	return nil
}

func extractOpenAIContentPartThinkingText(part ojson.Result) string {
	for _, field := range []string{"thinking", "reasoning", "reasoning_content", "text"} {
		if texts := extractReasoningTexts(part.Get(field)); len(texts) > 0 {
			return strings.Join(texts, "")
		}
	}
	return ""
}

func forEachOpenAIContentSegment(content ojson.Result, emit func(isReasoning bool, text string)) {
	if !content.Exists() {
		return
	}
	if content.IsArray() {
		content.ForEach(func(_, part ojson.Result) bool {
			if part.Type == ojson.String {
				emit(false, part.String())
				return true
			}
			switch part.Get("type").String() {
			case "thinking", "reasoning", "thought", "reasoning.text", "reasoning_text":
				emit(true, extractOpenAIContentPartThinkingText(part))
			case "text", "output_text":
				if text := part.Get("text"); text.Type == ojson.String {
					emit(false, text.String())
				}
			case "refusal":
				refusalText := ""
				if refusal := part.Get("refusal"); refusal.Type == ojson.String {
					refusalText = refusal.String()
				}
				if refusalText == "" {
					if text := part.Get("text"); text.Type == ojson.String {
						refusalText = text.String()
					}
				}
				emit(false, refusalText)
			default:
				if text := part.Get("text"); text.Type == ojson.String {
					emit(false, text.String())
				}
			}
			return true
		})
		return
	}
	if content.Type == ojson.String {
		emit(false, content.String())
	}
}

func extractReasoningTexts(node ojson.Result) []string {
	var texts []string
	if !node.Exists() {
		return texts
	}

	if node.IsArray() {
		node.ForEach(func(_, value ojson.Result) bool {
			texts = append(texts, extractReasoningTexts(value)...)
			return true
		})
		return texts
	}

	switch node.Type {
	case ojson.String:
		if text := node.String(); text != "" {
			texts = append(texts, text)
		}
	case ojson.JSON:
		if text := node.Get("text"); text.Exists() && text.Type == ojson.String {
			if textStr := text.String(); textStr != "" {
				texts = append(texts, textStr)
			}
		} else if node.Get("type").String() == "reasoning.summary" {
			if summary := node.Get("summary"); summary.Exists() && summary.Type == ojson.String {
				if summaryStr := summary.String(); summaryStr != "" {
					texts = append(texts, summaryStr)
				}
			}
		} else if raw := strings.TrimSpace(node.Raw); raw != "" && !strings.HasPrefix(raw, "{") && !strings.HasPrefix(raw, "[") {
			texts = append(texts, raw)
		}
	}

	return texts
}
