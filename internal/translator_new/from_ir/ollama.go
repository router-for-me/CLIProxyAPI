// Package from_ir converts unified request format to provider-specific formats.
// This file handles conversion to Ollama API format.
package from_ir

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/ir"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/to_ir"
)

// =============================================================================
// Request Conversion (Unified → Ollama API)
// =============================================================================

// ToOllamaRequest converts unified request to Ollama API JSON format.
// Use when sending request TO Ollama API (e.g., client sent OpenAI format, proxy to Ollama).
// Returns /api/chat format by default. Use metadata["ollama_endpoint"] = "generate" for /api/generate.
func ToOllamaRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	// Check if generate endpoint is requested
	if req.Metadata != nil {
		if endpoint, ok := req.Metadata["ollama_endpoint"].(string); ok && endpoint == "generate" {
			return convertToOllamaGenerateRequest(req)
		}
	}
	return convertToOllamaChatRequest(req)
}

// convertToOllamaChatRequest converts to Ollama /api/chat request format.
func convertToOllamaChatRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	m := map[string]interface{}{
		"model":    req.Model,
		"messages": []interface{}{},
		"stream":   req.Metadata["stream"] == true, // Preserve stream flag if present
	}

	// Generation options
	m["options"] = buildOllamaOptions(req)

	// Messages
	var messages []interface{}
	for _, msg := range req.Messages {
		if msgObj := convertMessageToOllama(msg); msgObj != nil {
			messages = append(messages, msgObj)
		}
	}
	m["messages"] = messages

	// Tools (Ollama uses OpenAI format)
	if len(req.Tools) > 0 {
		tools := make([]interface{}, len(req.Tools))
		for i, t := range req.Tools {
			params := t.Parameters
			if params == nil {
				params = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
			}
			tools[i] = map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  params,
				},
			}
		}
		m["tools"] = tools
	}

	// Format (JSON mode or Schema)
	if req.ResponseSchema != nil {
		m["format"] = req.ResponseSchema
	} else if req.Metadata != nil {
		if format, ok := req.Metadata["ollama_format"].(string); ok && format != "" {
			m["format"] = format
		}
	}

	if req.Metadata != nil {
		if keepAlive, ok := req.Metadata["ollama_keep_alive"].(string); ok && keepAlive != "" {
			m["keep_alive"] = keepAlive
		}
	}

	return json.Marshal(m)
}

// convertToOllamaGenerateRequest converts to Ollama /api/generate request format.
func convertToOllamaGenerateRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	m := map[string]interface{}{
		"model":  req.Model,
		"prompt": "",
		"stream": req.Metadata["stream"] == true,
	}

	// Generation options
	m["options"] = buildOllamaOptions(req)

	// Extract system prompt and user prompt from messages
	var systemPrompt, userPrompt string
	var images []string

	for _, msg := range req.Messages {
		switch msg.Role {
		case ir.RoleSystem:
			systemPrompt = ir.CombineTextParts(msg)
		case ir.RoleUser:
			userPrompt = ir.CombineTextParts(msg)
			// Extract images
			for _, part := range msg.Content {
				if part.Type == ir.ContentTypeImage && part.Image != nil {
					images = append(images, part.Image.Data)
				}
			}
		}
	}

	if systemPrompt != "" {
		m["system"] = systemPrompt
	}
	if userPrompt != "" {
		m["prompt"] = userPrompt
	}
	if len(images) > 0 {
		m["images"] = images
	}

	// Format (JSON mode or Schema)
	if req.ResponseSchema != nil {
		m["format"] = req.ResponseSchema
	} else if req.Metadata != nil {
		if format, ok := req.Metadata["ollama_format"].(string); ok && format != "" {
			m["format"] = format
		}
	}

	if req.Metadata != nil {
		if keepAlive, ok := req.Metadata["ollama_keep_alive"].(string); ok && keepAlive != "" {
			m["keep_alive"] = keepAlive
		}
	}

	return json.Marshal(m)
}

// buildOllamaOptions builds generation options map.
func buildOllamaOptions(req *ir.UnifiedChatRequest) map[string]interface{} {
	opts := make(map[string]interface{})
	if req.Temperature != nil {
		opts["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		opts["top_p"] = *req.TopP
	}
	if req.TopK != nil {
		opts["top_k"] = *req.TopK
	}
	if req.MaxTokens != nil {
		opts["num_predict"] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 {
		opts["stop"] = req.StopSequences
	}

	// Ollama-specific options from metadata
	if req.Metadata != nil {
		if seed, ok := req.Metadata["ollama_seed"].(int64); ok {
			opts["seed"] = seed
		}
		if numCtx, ok := req.Metadata["ollama_num_ctx"].(int64); ok {
			opts["num_ctx"] = numCtx
		}
	}
	return opts
}

// convertMessageToOllama converts single message to Ollama format.
func convertMessageToOllama(msg ir.Message) map[string]interface{} {
	switch msg.Role {
	case ir.RoleSystem:
		if text := ir.CombineTextParts(msg); text != "" {
			return map[string]interface{}{"role": "system", "content": text}
		}
	case ir.RoleUser:
		return buildOllamaUserMessage(msg)
	case ir.RoleAssistant:
		return buildOllamaAssistantMessage(msg)
	case ir.RoleTool:
		return buildOllamaToolMessage(msg)
	}
	return nil
}

// buildOllamaUserMessage builds user message with text and images.
func buildOllamaUserMessage(msg ir.Message) map[string]interface{} {
	result := map[string]interface{}{"role": "user"}
	var text string
	var images []string

	for _, part := range msg.Content {
		switch part.Type {
		case ir.ContentTypeText:
			text += part.Text
		case ir.ContentTypeImage:
			if part.Image != nil {
				// Ollama expects raw base64 without data URI prefix
				images = append(images, part.Image.Data)
			}
		}
	}

	if text != "" {
		result["content"] = text
	}
	if len(images) > 0 {
		result["images"] = images
	}
	if text == "" && len(images) == 0 {
		return nil
	}
	return result
}

// buildOllamaAssistantMessage builds assistant message with text and tool calls.
func buildOllamaAssistantMessage(msg ir.Message) map[string]interface{} {
	result := map[string]interface{}{"role": "assistant"}
	if text := ir.CombineTextParts(msg); text != "" {
		result["content"] = text
	}
	if reasoning := ir.CombineReasoningParts(msg); reasoning != "" {
		result["thinking"] = reasoning
	}

	// Tool calls (Ollama uses OpenAI format)
	if len(msg.ToolCalls) > 0 {
		tcs := make([]interface{}, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			tcs[i] = map[string]interface{}{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]interface{}{
					"name":      tc.Name,
					"arguments": tc.Args,
				},
			}
		}
		result["tool_calls"] = tcs
	}
	return result
}

// buildOllamaToolMessage builds tool result message.
func buildOllamaToolMessage(msg ir.Message) map[string]interface{} {
	for _, part := range msg.Content {
		if part.Type == ir.ContentTypeToolResult && part.ToolResult != nil {
			return map[string]interface{}{
				"role":         "tool",
				"tool_call_id": part.ToolResult.ToolCallID,
				"content":      part.ToolResult.Result,
			}
		}
	}
	return nil
}

// =============================================================================
// Response Conversion (Unified → Ollama Response)
// =============================================================================

// ToOllamaChatResponse converts messages to Ollama /api/chat response.
// Use when sending response TO client in Ollama chat format (non-streaming).
func ToOllamaChatResponse(messages []ir.Message, usage *ir.Usage, model string) ([]byte, error) {
	builder := ir.NewResponseBuilder(messages, usage, model)

	response := map[string]interface{}{
		"model":      model,
		"created_at": time.Now().UTC().Format(time.RFC3339),
		"done":       true,
		"message": map[string]interface{}{
			"role":    "assistant",
			"content": "",
		},
	}

	if msg := builder.GetLastMessage(); msg != nil {
		msgMap := response["message"].(map[string]interface{})
		msgMap["role"] = string(msg.Role)

		if text := builder.GetTextContent(); text != "" {
			msgMap["content"] = text
		}

		// Add thinking/reasoning content (for models like DeepSeek R1)
		if reasoning := builder.GetReasoningContent(); reasoning != "" {
			msgMap["thinking"] = reasoning
		}

		if tcs := builder.BuildOpenAIToolCalls(); tcs != nil {
			msgMap["tool_calls"] = tcs
			response["done_reason"] = "tool_calls"
		} else {
			response["done_reason"] = "stop"
		}
	}

	// Usage statistics
	if usage != nil {
		response["prompt_eval_count"] = usage.PromptTokens
		response["eval_count"] = usage.CompletionTokens
		// Zero out durations as we don't track them
		response["total_duration"] = 0
		response["load_duration"] = 0
		response["prompt_eval_duration"] = 0
		response["eval_duration"] = 0
	}

	return json.Marshal(response)
}

// ToOllamaGenerateResponse converts messages to Ollama /api/generate response.
// Use when sending response TO client in Ollama generate format (non-streaming).
func ToOllamaGenerateResponse(messages []ir.Message, usage *ir.Usage, model string) ([]byte, error) {
	builder := ir.NewResponseBuilder(messages, usage, model)

	response := map[string]interface{}{
		"model":       model,
		"created_at":  time.Now().UTC().Format(time.RFC3339),
		"done":        true,
		"response":    "",
		"done_reason": "stop",
	}

	if text := builder.GetTextContent(); text != "" {
		response["response"] = text
	}

	// Add thinking/reasoning content (for models like DeepSeek R1)
	if reasoning := builder.GetReasoningContent(); reasoning != "" {
		response["thinking"] = reasoning
	}

	// Usage statistics
	if usage != nil {
		response["prompt_eval_count"] = usage.PromptTokens
		response["eval_count"] = usage.CompletionTokens
		response["total_duration"] = 0
		response["load_duration"] = 0
		response["prompt_eval_duration"] = 0
		response["eval_duration"] = 0
	}

	return json.Marshal(response)
}

// =============================================================================
// Streaming Response Conversion (Events → Ollama Chunks)
// =============================================================================

// ToOllamaChatChunk converts event to Ollama /api/chat streaming chunk.
func ToOllamaChatChunk(event ir.UnifiedEvent, model string) ([]byte, error) {
	chunk := map[string]interface{}{
		"model":      model,
		"created_at": time.Now().UTC().Format(time.RFC3339),
		"done":       false,
		"message": map[string]interface{}{
			"role":    "assistant",
			"content": "",
		},
	}

	switch event.Type {
	case ir.EventTypeToken:
		chunk["message"].(map[string]interface{})["content"] = event.Content

	case ir.EventTypeReasoning:
		// Ollama native API uses "thinking" field for reasoning/chain-of-thought
		chunk["message"].(map[string]interface{})["thinking"] = event.Reasoning

	case ir.EventTypeToolCall:
		if event.ToolCall != nil {
			chunk["message"].(map[string]interface{})["tool_calls"] = []interface{}{
				map[string]interface{}{
					"id":   event.ToolCall.ID,
					"type": "function",
					"function": map[string]interface{}{
						"name":      event.ToolCall.Name,
						"arguments": event.ToolCall.Args,
					},
				},
			}
		}

	case ir.EventTypeFinish:
		chunk["done"] = true
		chunk["done_reason"] = mapFinishReasonToOllama(event.FinishReason)
		chunk["message"].(map[string]interface{})["content"] = ""

		if event.Usage != nil {
			chunk["prompt_eval_count"] = event.Usage.PromptTokens
			chunk["eval_count"] = event.Usage.CompletionTokens
			chunk["total_duration"] = 0
			chunk["load_duration"] = 0
			chunk["prompt_eval_duration"] = 0
			chunk["eval_duration"] = 0
		}

	case ir.EventTypeError:
		return nil, fmt.Errorf("stream error: %v", event.Error)

	default:
		// Unknown event types are silently skipped (graceful handling)
		return nil, nil
	}

	jsonBytes, err := json.Marshal(chunk)
	if err != nil {
		return nil, err
	}

	// Ollama uses newline-delimited JSON (not SSE)
	return append(jsonBytes, '\n'), nil
}

// ToOllamaGenerateChunk converts event to Ollama /api/generate streaming chunk.
func ToOllamaGenerateChunk(event ir.UnifiedEvent, model string) ([]byte, error) {
	chunk := map[string]interface{}{
		"model":      model,
		"created_at": time.Now().UTC().Format(time.RFC3339),
		"done":       false,
		"response":   "",
	}

	switch event.Type {
	case ir.EventTypeToken:
		chunk["response"] = event.Content

	case ir.EventTypeReasoning:
		// Ollama native API uses "thinking" field for reasoning/chain-of-thought
		chunk["thinking"] = event.Reasoning

	case ir.EventTypeFinish:
		chunk["done"] = true
		chunk["done_reason"] = mapFinishReasonToOllama(event.FinishReason)
		chunk["response"] = ""

		if event.Usage != nil {
			chunk["prompt_eval_count"] = event.Usage.PromptTokens
			chunk["eval_count"] = event.Usage.CompletionTokens
			chunk["total_duration"] = 0
			chunk["load_duration"] = 0
			chunk["prompt_eval_duration"] = 0
			chunk["eval_duration"] = 0
		}

	case ir.EventTypeToolCall:
		// Generate endpoint doesn't support tool calls, skip silently
		return nil, nil

	case ir.EventTypeError:
		return nil, fmt.Errorf("stream error: %v", event.Error)

	default:
		return nil, nil
	}

	jsonBytes, err := json.Marshal(chunk)
	if err != nil {
		return nil, err
	}

	return append(jsonBytes, '\n'), nil
}

// =============================================================================
// OpenAI Response → Ollama Conversion
// =============================================================================

// OpenAIToOllamaChat converts OpenAI response to Ollama chat format.
// This is a convenience function that parses OpenAI response, then converts to Ollama.
func OpenAIToOllamaChat(rawJSON []byte, model string) ([]byte, error) {
	messages, usage, err := to_ir.ParseOpenAIResponse(rawJSON)
	if err != nil {
		return nil, err
	}
	return ToOllamaChatResponse(messages, usage, model)
}

// OpenAIToOllamaGenerate converts OpenAI response to Ollama generate format.
func OpenAIToOllamaGenerate(rawJSON []byte, model string) ([]byte, error) {
	messages, usage, err := to_ir.ParseOpenAIResponse(rawJSON)
	if err != nil {
		return nil, err
	}
	return ToOllamaGenerateResponse(messages, usage, model)
}

// OpenAIChunkToOllamaChat converts OpenAI streaming chunk to Ollama chat chunk.
func OpenAIChunkToOllamaChat(rawJSON []byte, model string) ([]byte, error) {
	events, err := to_ir.ParseOpenAIChunk(rawJSON)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	// Convert first event (OpenAI chunks typically have one event)
	return ToOllamaChatChunk(events[0], model)
}

// OpenAIChunkToOllamaGenerate converts OpenAI streaming chunk to Ollama generate chunk.
func OpenAIChunkToOllamaGenerate(rawJSON []byte, model string) ([]byte, error) {
	events, err := to_ir.ParseOpenAIChunk(rawJSON)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	return ToOllamaGenerateChunk(events[0], model)
}

// =============================================================================
// Utility Functions
// =============================================================================

func mapFinishReasonToOllama(reason ir.FinishReason) string {
	switch reason {
	case ir.FinishReasonStop:
		return "stop"
	case ir.FinishReasonLength:
		return "length"
	case ir.FinishReasonToolCalls:
		return "tool_calls"
	default:
		return "stop"
	}
}

// =============================================================================
// Model Configuration (for /api/show endpoint)
// =============================================================================

// ToOllamaShowResponse generates an Ollama show response for a given model name.
// Looks up model info from registry, falls back to sensible defaults.
func ToOllamaShowResponse(modelName string) []byte {
	// Default values for unknown models
	contextLength := 128000  // 128K context
	maxOutputTokens := 16384 // 16K output
	architecture := "transformer"

	// Try to find model in registry (search across all providers)
	if info := findModelInfoByName(modelName); info != nil {
		if info.Type != "" {
			architecture = info.Type
		}
		if info.ContextLength > 0 {
			contextLength = info.ContextLength
		} else if info.InputTokenLimit > 0 {
			contextLength = info.InputTokenLimit
		}
		if info.MaxCompletionTokens > 0 {
			maxOutputTokens = info.MaxCompletionTokens
		} else if info.OutputTokenLimit > 0 {
			maxOutputTokens = info.OutputTokenLimit
		}
	}

	result := map[string]interface{}{
		"license":    "",
		"modelfile":  "# Modelfile for " + modelName + "\nFROM " + modelName,
		"parameters": fmt.Sprintf("num_ctx %d\nnum_predict %d\ntemperature 0.7\ntop_p 0.9", contextLength, maxOutputTokens),
		"template":   "{{ if .System }}{{ .System }}\n{{ end }}{{ .Prompt }}",
		"details": map[string]interface{}{
			"parent_model":       "",
			"format":             "gguf",
			"family":             "Ollama",
			"families":           []string{"Ollama"},
			"parameter_size":     "0B",
			"quantization_level": "Q4_K_M",
		},
		"model_info": map[string]interface{}{
			"general.architecture":           architecture,
			"general.basename":               modelName,
			"general.file_type":              2,
			"general.parameter_count":        0,
			"general.quantization_version":   2,
			"general.context_length":         contextLength,
			"llama.context_length":           contextLength,
			"llama.rope.freq_base":           10000.0,
			architecture + ".context_length": contextLength,
		},
		"capabilities": []string{"tools", "vision", "completion"},
	}

	jsonBytes, _ := json.Marshal(result)
	return jsonBytes
}

// findModelInfoByName searches for model info in registry by model name.
// It handles the provider:modelID format used internally by registry.
func findModelInfoByName(modelName string) *registry.ModelInfo {
	reg := registry.GetGlobalRegistry()

	// First try direct lookup
	if info := reg.GetModelInfo(modelName); info != nil {
		return info
	}

	// Search through available models (handles provider:modelID format)
	models := reg.GetAvailableModels("openai")
	for _, m := range models {
		id, ok := m["id"].(string)
		if !ok {
			continue
		}
		// Strip provider prefix if present (e.g., "[Gemini CLI] gemini-2.5-flash" -> "gemini-2.5-flash")
		cleanID := id
		if idx := strings.Index(id, "] "); idx != -1 {
			cleanID = id[idx+2:]
		}
		if strings.EqualFold(cleanID, modelName) {
			// Found match, build ModelInfo from map
			info := &registry.ModelInfo{ID: cleanID}
			if v, ok := m["type"].(string); ok {
				info.Type = v
			}
			if v, ok := m["context_length"].(int); ok {
				info.ContextLength = v
			}
			if v, ok := m["max_completion_tokens"].(int); ok {
				info.MaxCompletionTokens = v
			}
			return info
		}
	}

	return nil
}
