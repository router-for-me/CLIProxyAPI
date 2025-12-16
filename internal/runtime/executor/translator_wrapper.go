package executor

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/from_ir"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/ir"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/to_ir"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// convertRequestToIR converts a request payload to unified format.
// This is the shared logic used by all Gemini-family translators.
// Returns (nil, nil) if the format is unsupported (caller should use fallback).
func convertRequestToIR(from sdktranslator.Format, model string, payload []byte, metadata map[string]any) (*ir.UnifiedChatRequest, error) {
	var irReq *ir.UnifiedChatRequest
	var err error

	// Determine source format and convert to IR
	switch from.String() {
	case "openai", "cline": // Cline uses OpenAI-compatible format
		irReq, err = to_ir.ParseOpenAIRequest(payload)
	case "ollama":
		irReq, err = to_ir.ParseOllamaRequest(payload)
	case "claude":
		irReq, err = to_ir.ParseClaudeRequest(payload)
	default:
		// Unsupported format
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	// Override model if specified
	if model != "" {
		irReq.Model = model
	}

	// Store metadata for provider-specific handling
	if metadata != nil {
		irReq.Metadata = metadata
	}

	// Apply thinking overrides from metadata if present
	if metadata != nil {
		budgetOverride, includeOverride, hasOverride := extractThinkingFromMetadata(metadata)
		if hasOverride {
			if irReq.Thinking == nil {
				irReq.Thinking = &ir.ThinkingConfig{}
			}
			if budgetOverride != nil {
				irReq.Thinking.Budget = *budgetOverride
			}
			if includeOverride != nil {
				irReq.Thinking.IncludeThoughts = *includeOverride
			}
		}
	}

	return irReq, nil
}

// TranslateToGeminiCLI converts request to Gemini CLI format.
// Uses new translator if feature flag is enabled in config, otherwise uses old translator.
// metadata contains additional context like thinking overrides from request metadata.
// Note: Antigravity uses the same format as Gemini CLI, so this function works for both.
func TranslateToGeminiCLI(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	useCanonical := cfg != nil && cfg.UseCanonicalTranslator

	if !useCanonical {
		to := sdktranslator.FromString("gemini-cli")
		return sdktranslator.TranslateRequest(from, to, model, payload, streaming), nil
	}

	// Convert to IR using shared helper
	irReq, err := convertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}
	if irReq == nil {
		// Unsupported format, fall back to old translator
		to := sdktranslator.FromString("gemini-cli")
		return sdktranslator.TranslateRequest(from, to, model, payload, streaming), nil
	}

	// Convert IR to Gemini CLI format
	geminiJSON, err := (&from_ir.GeminiCLIProvider{}).ConvertRequest(irReq)
	if err != nil {
		return nil, err
	}

	// Apply default thinking for models that require it (e.g., gemini-3-pro-preview)
	geminiJSON = util.ApplyDefaultThinkingIfNeededCLI(model, geminiJSON)
	geminiJSON = util.NormalizeGeminiCLIThinkingBudget(model, geminiJSON)

	// Apply payload config overrides from YAML
	return applyPayloadConfigToIR(cfg, model, geminiJSON), nil
}

// extractThinkingFromMetadata extracts thinking config overrides from request metadata
func extractThinkingFromMetadata(metadata map[string]any) (budget *int, include *bool, hasOverride bool) {
	if metadata == nil {
		return nil, nil, false
	}

	if v, ok := metadata["thinking_budget"].(int); ok {
		budget = &v
		hasOverride = true
	}
	if v, ok := metadata["include_thoughts"].(bool); ok {
		include = &v
		hasOverride = true
	}

	return budget, include, hasOverride
}

// applyPayloadConfigToIR applies YAML payload config rules to the generated JSON
func applyPayloadConfigToIR(cfg *config.Config, model string, payload []byte) []byte {
	if cfg == nil || len(payload) == 0 {
		return payload
	}

	// Apply default rules (only set if missing)
	for _, rule := range cfg.Payload.Default {
		if matchesPayloadRule(rule, model, "gemini") {
			for path, value := range rule.Params {
				fullPath := "request." + path
				if !gjson.GetBytes(payload, fullPath).Exists() {
					payload, _ = sjson.SetBytes(payload, fullPath, value)
				}
			}
		}
	}

	// Apply override rules (always set)
	for _, rule := range cfg.Payload.Override {
		if matchesPayloadRule(rule, model, "gemini") {
			for path, value := range rule.Params {
				fullPath := "request." + path
				payload, _ = sjson.SetBytes(payload, fullPath, value)
			}
		}
	}

	return payload
}

// matchesPayloadRule checks if a payload rule matches the given model and protocol
func matchesPayloadRule(rule config.PayloadRule, model, protocol string) bool {
	for _, m := range rule.Models {
		if m.Protocol != "" && m.Protocol != protocol {
			continue
		}
		if matchesPattern(m.Name, model) {
			return true
		}
	}
	return false
}

// matchesPattern checks if a model name matches a pattern (supports wildcards)
func matchesPattern(pattern, name string) bool {
	if pattern == name {
		return true
	}
	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") {
		return strings.Contains(name, pattern[1:len(pattern)-1])
	}
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(name, pattern[1:])
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, pattern[:len(pattern)-1])
	}
	return false
}

// TranslateToGemini converts request to Gemini (AI Studio API) format.
// Uses new translator if feature flag is enabled in config, otherwise uses old translator.
// metadata contains additional context like thinking overrides from request metadata.
func TranslateToGemini(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	useCanonical := cfg != nil && cfg.UseCanonicalTranslator

	if !useCanonical {
		to := sdktranslator.FromString("gemini")
		return sdktranslator.TranslateRequest(from, to, model, payload, streaming), nil
	}

	// Convert to IR using shared helper
	irReq, err := convertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}
	if irReq == nil {
		// Unsupported format, fallback to old translator
		to := sdktranslator.FromString("gemini")
		return sdktranslator.TranslateRequest(from, to, model, payload, streaming), nil
	}

	// Convert IR to Gemini format
	geminiJSON, err := (&from_ir.GeminiProvider{}).ConvertRequest(irReq)
	if err != nil {
		return nil, err
	}

	// Apply default thinking for models that require it (e.g., gemini-3-pro-preview)
	geminiJSON = util.ApplyDefaultThinkingIfNeeded(model, geminiJSON)
	geminiJSON = util.NormalizeGeminiThinkingBudget(model, geminiJSON)

	// Apply payload config overrides from YAML
	return applyPayloadConfigToIR(cfg, model, geminiJSON), nil
}

// TranslateGeminiCLIResponseNonStream converts Gemini CLI non-streaming response to target format using new translator.
// Returns nil if new translator is disabled (caller should use old translator as fallback).
func TranslateGeminiCLIResponseNonStream(cfg *config.Config, to sdktranslator.Format, geminiResponse []byte, model string) ([]byte, error) {
	useCanonical := cfg != nil && cfg.UseCanonicalTranslator

	if !useCanonical {
		return nil, nil // Caller should use old translator
	}

	// Step 1: Parse Gemini CLI response to IR
	messages, usage, err := (&from_ir.GeminiCLIProvider{}).ParseResponse(geminiResponse)
	if err != nil {
		return nil, err
	}

	// Step 2: Convert IR to target format
	toStr := to.String()
	messageID := "chatcmpl-" + model // Simple ID generation

	switch toStr {
	case "openai", "cline":
		return from_ir.ToOpenAIChatCompletion(messages, usage, model, messageID)
	case "claude":
		return from_ir.ToClaudeResponse(messages, usage, model, messageID)
	case "ollama":
		// Ollama has two formats: chat and generate. Default to chat for compatibility.
		return from_ir.ToOllamaChatResponse(messages, usage, model)
	default:
		// Unsupported target format, return nil to trigger fallback
		return nil, nil
	}
}

// GeminiCLIStreamState maintains state for stateful streaming conversions (e.g., Claude tool calls).
type GeminiCLIStreamState struct {
	ClaudeState          *from_ir.ClaudeStreamState
	ToolCallIndex        int                   // Track tool call index across chunks for OpenAI format
	ReasoningTokensCount int                   // Track accumulated reasoning tokens for final usage chunk
	ReasoningCharsAccum  int                   // Track accumulated reasoning characters (for estimation if provider doesn't give count)
	ToolSchemaCtx        *ir.ToolSchemaContext // Schema context for normalizing tool call parameters
	FinishSent           bool                  // Track if finish event was already sent (prevent duplicates)
	ToolCallSentHeader   map[int]bool          // Track which tool call indices have sent their header (ID/Name/Type)
}

// NewAntigravityStreamState creates a new stream state with tool schema context for Antigravity provider.
// Antigravity has a known issue where Gemini ignores tool parameter schemas and returns
// different parameter names (e.g., "path" instead of "target_file").
// This function extracts the expected schema from the original request to normalize responses.
// Also detects if the client is using Claude format (tool_use) to ensure proper response formatting.
// Uses gjson for efficient extraction without full JSON unmarshaling.
func NewAntigravityStreamState(originalRequest []byte) *GeminiCLIStreamState {
	state := &GeminiCLIStreamState{
		ClaudeState:        from_ir.NewClaudeStreamState(),
		ToolCallSentHeader: make(map[int]bool),
	}

	if len(originalRequest) > 0 {
		// Extract tool schemas efficiently using gjson (no full unmarshal)
		tools := gjson.GetBytes(originalRequest, "tools").Array()
		if len(tools) > 0 {
			state.ToolSchemaCtx = ir.NewToolSchemaContextFromGJSON(tools)
		}
	}

	return state
}

// TranslateGeminiCLIResponseStream converts Gemini CLI streaming chunk to target format using new translator.
// Returns nil if new translator is disabled (caller should use old translator as fallback).
// state parameter is optional but recommended for stateful conversions (e.g., Claude tool calls).
func TranslateGeminiCLIResponseStream(cfg *config.Config, to sdktranslator.Format, geminiChunk []byte, model string, messageID string, state *GeminiCLIStreamState) ([][]byte, error) {
	useCanonical := cfg != nil && cfg.UseCanonicalTranslator

	if !useCanonical {
		return nil, nil // Caller should use old translator
	}

	// Step 1: Parse Gemini CLI chunk to IR events (with schema context if available)
	var events []ir.UnifiedEvent
	var err error
	if state != nil && state.ToolSchemaCtx != nil {
		events, err = (&from_ir.GeminiCLIProvider{}).ParseStreamChunkWithContext(geminiChunk, state.ToolSchemaCtx)
	} else {
		events, err = (&from_ir.GeminiCLIProvider{}).ParseStreamChunk(geminiChunk)
	}
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return nil, nil
	}

	// Step 2: Convert IR events to target format chunks
	toStr := to.String()

	// Initialize as empty slice (not nil) so that even when all events are skipped,
	// we return non-nil to prevent fallback to old translator
	chunks := make([][]byte, 0)

	switch toStr {
	case "openai", "cline":
		if state == nil {
			state = &GeminiCLIStreamState{ToolCallSentHeader: make(map[int]bool)}
		}
		if state.ToolCallSentHeader == nil {
			state.ToolCallSentHeader = make(map[int]bool)
		}
		for i := range events {
			event := &events[i]

			// Skip duplicate finish events - Gemini sends finishReason in multiple chunks
			// but OpenAI clients expect only one finish_reason per stream
			if event.Type == ir.EventTypeFinish {
				if state.FinishSent {
					continue // Skip duplicate finish
				}
				state.FinishSent = true
				// On finish, ensure reasoning_tokens is set if we had reasoning content
				if state.ReasoningCharsAccum > 0 {
					if event.Usage == nil {
						event.Usage = &ir.Usage{}
					}
					if event.Usage.ThoughtsTokenCount == 0 {
						// Estimate: ~3 chars per token (conservative for mixed languages)
						event.Usage.ThoughtsTokenCount = (state.ReasoningCharsAccum + 2) / 3
					}
				}
			}

			idx := 0
			if event.Type == ir.EventTypeToolCall {
				idx = state.ToolCallIndex
				state.ToolCallIndex++
			}

			// Set correct tool call index and ensure ID/Name sent only once per tool
			if event.ToolCall != nil {
				event.ToolCallIndex = idx
				if state.ToolCallSentHeader[idx] {
					event.ToolCall.ID = ""
					event.ToolCall.Name = ""
				} else {
					state.ToolCallSentHeader[idx] = true
				}
			}

			// Track reasoning tokens for final usage
			if event.Type == ir.EventTypeReasoning && event.Reasoning != "" {
				state.ReasoningCharsAccum += len(event.Reasoning)
			}
			chunk, err := from_ir.ToOpenAIChunk(*event, model, messageID, idx)
			if err != nil {
				return nil, err
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		}
	case "claude":
		if state == nil {
			state = &GeminiCLIStreamState{ClaudeState: &from_ir.ClaudeStreamState{}}
		}
		if state.ClaudeState == nil {
			state.ClaudeState = &from_ir.ClaudeStreamState{}
		}
		for _, event := range events {
			claudeChunks, err := from_ir.ToClaudeSSE(event, model, messageID, state.ClaudeState)
			if err != nil {
				return nil, err
			}
			if claudeChunks != nil {
				chunks = append(chunks, claudeChunks)
			}
		}
	case "ollama":
		for _, event := range events {
			chunk, err := from_ir.ToOllamaChatChunk(event, model)
			if err != nil {
				return nil, err
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		}
	default:
		// Unsupported target format, return nil to trigger fallback
		return nil, nil
	}

	return chunks, nil
}

// TranslateGeminiResponseNonStream converts Gemini (AI Studio) non-streaming response to target format using new translator.
// Returns nil if new translator is disabled (caller should use old translator as fallback).
func TranslateGeminiResponseNonStream(cfg *config.Config, to sdktranslator.Format, geminiResponse []byte, model string) ([]byte, error) {
	useCanonical := cfg != nil && cfg.UseCanonicalTranslator

	if !useCanonical {
		return nil, nil // Caller should use old translator
	}

	// Step 1: Parse Gemini response to IR with metadata
	messages, usage, meta, err := to_ir.ParseGeminiResponseMeta(geminiResponse)
	if err != nil {
		return nil, err
	}

	// Step 2: Convert IR to target format
	toStr := to.String()

	// Use responseId from metadata if available, otherwise generate
	messageID := "chatcmpl-" + model
	if meta != nil && meta.ResponseID != "" {
		messageID = meta.ResponseID
	}

	switch toStr {
	case "openai", "cline":
		// Build OpenAI metadata from Gemini response metadata
		var openaiMeta *ir.OpenAIMeta
		if meta != nil {
			openaiMeta = &ir.OpenAIMeta{
				ResponseID:         meta.ResponseID,
				CreateTime:         meta.CreateTime,
				NativeFinishReason: meta.NativeFinishReason,
			}
			if usage != nil {
				openaiMeta.ThoughtsTokenCount = usage.ThoughtsTokenCount
			}
		}
		return from_ir.ToOpenAIChatCompletionMeta(messages, usage, model, messageID, openaiMeta)
	case "claude":
		return from_ir.ToClaudeResponse(messages, usage, model, messageID)
	case "ollama":
		return from_ir.ToOllamaChatResponse(messages, usage, model)
	default:
		return nil, nil
	}
}

// TranslateGeminiResponseStream converts Gemini (AI Studio) streaming chunk to target format using new translator.
// Returns nil if new translator is disabled (caller should use old translator as fallback).
func TranslateGeminiResponseStream(cfg *config.Config, to sdktranslator.Format, geminiChunk []byte, model string, messageID string, state *GeminiCLIStreamState) ([][]byte, error) {
	useCanonical := cfg != nil && cfg.UseCanonicalTranslator

	if !useCanonical {
		return nil, nil
	}

	// Step 1: Parse Gemini chunk to IR events (with schema context if available)
	var events []ir.UnifiedEvent
	var err error
	if state != nil && state.ToolSchemaCtx != nil {
		events, err = to_ir.ParseGeminiChunkWithContext(geminiChunk, state.ToolSchemaCtx)
	} else {
		events, err = to_ir.ParseGeminiChunk(geminiChunk)
	}
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return nil, nil
	}

	// Step 2: Convert IR events to target format chunks
	toStr := to.String()
	var chunks [][]byte

	switch toStr {
	case "openai", "cline":
		if state == nil {
			state = &GeminiCLIStreamState{}
		}
		for i := range events {
			event := &events[i]
			idx := 0
			if event.Type == ir.EventTypeToolCall {
				idx = state.ToolCallIndex
				state.ToolCallIndex++
			}
			// Track reasoning tokens for final usage
			if event.Type == ir.EventTypeReasoning && event.Reasoning != "" {
				state.ReasoningCharsAccum += len(event.Reasoning)
			}
			// On finish, ensure reasoning_tokens is set if we had reasoning content
			if event.Type == ir.EventTypeFinish && state.ReasoningCharsAccum > 0 {
				if event.Usage == nil {
					event.Usage = &ir.Usage{}
				}
				if event.Usage.ThoughtsTokenCount == 0 {
					// Estimate: ~3 chars per token (conservative for mixed languages)
					event.Usage.ThoughtsTokenCount = (state.ReasoningCharsAccum + 2) / 3
				}
			}
			chunk, err := from_ir.ToOpenAIChunk(*event, model, messageID, idx)
			if err != nil {
				return nil, err
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		}
	case "claude":
		if state == nil {
			state = &GeminiCLIStreamState{ClaudeState: from_ir.NewClaudeStreamState()}
		}
		if state.ClaudeState == nil {
			state.ClaudeState = from_ir.NewClaudeStreamState()
		}
		for _, event := range events {
			claudeChunks, err := from_ir.ToClaudeSSE(event, model, messageID, state.ClaudeState)
			if err != nil {
				return nil, err
			}
			if claudeChunks != nil {
				chunks = append(chunks, claudeChunks)
			}
		}
	case "ollama":
		for _, event := range events {
			chunk, err := from_ir.ToOllamaChatChunk(event, model)
			if err != nil {
				return nil, err
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		}
	default:
		return nil, nil
	}

	return chunks, nil
}

// TranslateClaudeResponseNonStream converts Claude non-streaming response to target format using new translator.
// Returns nil if new translator is disabled (caller should use old translator as fallback).
func TranslateClaudeResponseNonStream(cfg *config.Config, to sdktranslator.Format, claudeResponse []byte, model string) ([]byte, error) {
	useCanonical := cfg != nil && cfg.UseCanonicalTranslator

	if !useCanonical {
		return nil, nil // Caller should use old translator
	}

	// Step 1: Parse Claude response to IR
	messages, usage, err := to_ir.ParseClaudeResponse(claudeResponse)
	if err != nil {
		return nil, err
	}

	// Step 2: Convert IR to target format
	toStr := to.String()
	messageID := "msg-" + model // Simple ID generation

	switch toStr {
	case "openai", "cline":
		return from_ir.ToOpenAIChatCompletion(messages, usage, model, messageID)
	case "ollama":
		return from_ir.ToOllamaChatResponse(messages, usage, model)
	case "claude":
		// Passthrough - already in Claude format
		return claudeResponse, nil
	default:
		// Unsupported target format, return nil to trigger fallback
		return nil, nil
	}
}

// TranslateClaudeResponseStream converts Claude streaming chunk to target format using new translator.
// Returns nil if new translator is disabled or conversion not applicable (caller should use old translator as fallback).
func TranslateClaudeResponseStream(cfg *config.Config, to sdktranslator.Format, claudeChunk []byte, model string, messageID string, state *from_ir.ClaudeStreamState) ([][]byte, error) {
	useCanonical := cfg != nil && cfg.UseCanonicalTranslator

	if !useCanonical {
		return nil, nil // Caller should use old translator
	}

	// Step 1: Parse Claude chunk to IR events
	events, err := to_ir.ParseClaudeChunk(claudeChunk)
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return nil, nil
	}

	// Step 2: Convert IR events to target format chunks
	toStr := to.String()
	var chunks [][]byte

	switch toStr {
	case "openai", "cline":
		for _, event := range events {
			// Use ToolCallIndex from event for proper tool call indexing
			idx := event.ToolCallIndex
			chunk, err := from_ir.ToOpenAIChunk(event, model, messageID, idx)
			if err != nil {
				return nil, err
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		}
	case "ollama":
		for _, event := range events {
			chunk, err := from_ir.ToOllamaChatChunk(event, model)
			if err != nil {
				return nil, err
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		}
	case "claude":
		// Passthrough - already in Claude format
		return [][]byte{claudeChunk}, nil
	default:
		// Unsupported target format, return nil to trigger fallback
		return nil, nil
	}

	return chunks, nil
}

// OpenAIStreamState maintains state for OpenAI → OpenAI streaming conversions.
type OpenAIStreamState struct {
	ReasoningCharsAccum int // Track accumulated reasoning characters for token estimation
}

// TranslateToClaude converts request to Claude Messages API format.
// Uses new translator if feature flag is enabled in config, otherwise uses old translator.
// metadata contains additional context like thinking overrides from request metadata.
func TranslateToClaude(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	useCanonical := cfg != nil && cfg.UseCanonicalTranslator

	if !useCanonical {
		to := sdktranslator.FromString("claude")
		return sdktranslator.TranslateRequest(from, to, model, payload, streaming), nil
	}

	// Convert to IR using shared helper
	irReq, err := convertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}
	if irReq == nil {
		// Unsupported format, fall back to old translator
		to := sdktranslator.FromString("claude")
		return sdktranslator.TranslateRequest(from, to, model, payload, streaming), nil
	}

	// Convert IR to Claude format
	claudeJSON, err := (&from_ir.ClaudeProvider{}).ConvertRequest(irReq)
	if err != nil {
		return nil, err
	}

	// Add stream parameter if streaming is requested
	if streaming {
		claudeJSON, _ = sjson.SetBytes(claudeJSON, "stream", true)
	}

	return claudeJSON, nil
}

// TranslateOpenAIResponseStream converts OpenAI streaming chunk to target format using new translator.
// This is used for OpenAI-compatible providers (like Ollama) to ensure reasoning_tokens is properly set.
// Returns nil if new translator is disabled (caller should use old translator as fallback).
func TranslateOpenAIResponseStream(cfg *config.Config, to sdktranslator.Format, openaiChunk []byte, model string, messageID string, state *OpenAIStreamState) ([][]byte, error) {
	useCanonical := cfg != nil && cfg.UseCanonicalTranslator

	if !useCanonical {
		return nil, nil // Caller should use old translator
	}

	// Step 1: Parse OpenAI chunk to IR events
	events, err := to_ir.ParseOpenAIChunk(openaiChunk)
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return nil, nil
	}

	// Step 2: Convert IR events to target format chunks
	toStr := to.String()
	var chunks [][]byte

	switch toStr {
	case "openai", "cline":
		if state == nil {
			state = &OpenAIStreamState{}
		}
		for i := range events {
			event := &events[i]

			// Track reasoning content for token estimation
			if event.Type == ir.EventTypeReasoning && event.Reasoning != "" {
				state.ReasoningCharsAccum += len(event.Reasoning)
			}

			// On finish, ensure reasoning_tokens is set if we had reasoning content
			if event.Type == ir.EventTypeFinish && state.ReasoningCharsAccum > 0 {
				if event.Usage == nil {
					event.Usage = &ir.Usage{}
				}
				if event.Usage.ThoughtsTokenCount == 0 {
					// Estimate: ~3 chars per token (conservative for mixed languages)
					event.Usage.ThoughtsTokenCount = (state.ReasoningCharsAccum + 2) / 3
				}
			}

			// Use ToolCallIndex from event for proper tool call indexing
			idx := event.ToolCallIndex
			chunk, err := from_ir.ToOpenAIChunk(*event, model, messageID, idx)
			if err != nil {
				return nil, err
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		}
	case "ollama":
		for _, event := range events {
			chunk, err := from_ir.ToOllamaChatChunk(event, model)
			if err != nil {
				return nil, err
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		}
	default:
		// Unsupported target format, return nil to trigger fallback
		return nil, nil
	}

	return chunks, nil
}

// TranslateOpenAIResponseNonStream converts OpenAI non-streaming response to target format using new translator.
// Returns nil if new translator is disabled (caller should use old translator as fallback).
func TranslateOpenAIResponseNonStream(cfg *config.Config, to sdktranslator.Format, openaiResponse []byte, model string) ([]byte, error) {
	useCanonical := cfg != nil && cfg.UseCanonicalTranslator

	if !useCanonical {
		return nil, nil // Caller should use old translator
	}

	// Step 1: Parse OpenAI response to IR
	messages, usage, err := to_ir.ParseOpenAIResponse(openaiResponse)
	if err != nil {
		return nil, err
	}

	// Step 2: Convert IR to target format
	toStr := to.String()
	messageID := "chatcmpl-" + model // Simple ID generation

	switch toStr {
	case "openai", "cline":
		return from_ir.ToOpenAIChatCompletion(messages, usage, model, messageID)
	case "ollama":
		return from_ir.ToOllamaChatResponse(messages, usage, model)
	default:
		// Unsupported target format, return nil to trigger fallback
		return nil, nil
	}
}
