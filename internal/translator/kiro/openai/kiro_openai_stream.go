// Package openai provides streaming SSE event building for OpenAI format.
// This package handles the construction of OpenAI-compatible Server-Sent Events (SSE)
// for streaming responses from Kiro API.
package openai

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

// OpenAIStreamState tracks the state of streaming response conversion
type OpenAIStreamState struct {
	ChunkIndex        int
	ToolCallIndex     int
	HasSentFirstChunk bool
	Model             string
	ResponseID        string
	Created           int64
}

// NewOpenAIStreamState creates a new stream state for tracking
func NewOpenAIStreamState(model string) *OpenAIStreamState {
	return &OpenAIStreamState{
		ChunkIndex:        0,
		ToolCallIndex:     0,
		HasSentFirstChunk: false,
		Model:             model,
		ResponseID:        "chatcmpl-" + uuid.New().String()[:24],
		Created:           time.Now().Unix(),
	}
}

// FormatSSEEvent formats a JSON payload for SSE streaming.
// Note: This returns raw JSON data without "data:" prefix.
// The SSE "data:" prefix is added by the Handler layer (e.g., openai_handlers.go)
// to maintain architectural consistency and avoid double-prefix issues.
func FormatSSEEvent(data []byte) string {
	return string(data)
}

// BuildOpenAISSETextDelta creates an SSE event for text content delta
func BuildOpenAISSETextDelta(state *OpenAIStreamState, textDelta string) string {
	delta := map[string]interface{}{
		"content": textDelta,
	}

	// Include role in first chunk
	if !state.HasSentFirstChunk {
		delta["role"] = "assistant"
		state.HasSentFirstChunk = true
	}

	chunk := buildBaseChunk(state, delta, nil)
	result, _ := json.Marshal(chunk)
	state.ChunkIndex++
	return FormatSSEEvent(result)
}

// BuildOpenAISSEToolCallStart creates an SSE event for tool call start
func BuildOpenAISSEToolCallStart(state *OpenAIStreamState, toolUseID, toolName string) string {
	toolCall := map[string]interface{}{
		"index": state.ToolCallIndex,
		"id":    toolUseID,
		"type":  "function",
		"function": map[string]interface{}{
			"name":      toolName,
			"arguments": "",
		},
	}

	delta := map[string]interface{}{
		"tool_calls": []map[string]interface{}{toolCall},
	}

	// Include role in first chunk if not sent yet
	if !state.HasSentFirstChunk {
		delta["role"] = "assistant"
		state.HasSentFirstChunk = true
	}

	chunk := buildBaseChunk(state, delta, nil)
	result, _ := json.Marshal(chunk)
	state.ChunkIndex++
	return FormatSSEEvent(result)
}

// BuildOpenAISSEToolCallArgumentsDelta creates an SSE event for tool call arguments delta
func BuildOpenAISSEToolCallArgumentsDelta(state *OpenAIStreamState, argumentsDelta string, toolIndex int) string {
	toolCall := map[string]interface{}{
		"index": toolIndex,
		"function": map[string]interface{}{
			"arguments": argumentsDelta,
		},
	}

	delta := map[string]interface{}{
		"tool_calls": []map[string]interface{}{toolCall},
	}

	chunk := buildBaseChunk(state, delta, nil)
	result, _ := json.Marshal(chunk)
	state.ChunkIndex++
	return FormatSSEEvent(result)
}

// BuildOpenAISSEFinish creates an SSE event with finish_reason
func BuildOpenAISSEFinish(state *OpenAIStreamState, finishReason string) string {
	chunk := buildBaseChunk(state, map[string]interface{}{}, &finishReason)
	result, _ := json.Marshal(chunk)
	state.ChunkIndex++
	return FormatSSEEvent(result)
}

// BuildOpenAISSEUsage creates an SSE event with usage information
func BuildOpenAISSEUsage(state *OpenAIStreamState, usageInfo usage.Detail) string {
	chunk := map[string]interface{}{
		"id":      state.ResponseID,
		"object":  "chat.completion.chunk",
		"created": state.Created,
		"model":   state.Model,
		"choices": []map[string]interface{}{},
		"usage": map[string]interface{}{
			"prompt_tokens":     usageInfo.InputTokens,
			"completion_tokens": usageInfo.OutputTokens,
			"total_tokens":      usageInfo.InputTokens + usageInfo.OutputTokens,
		},
	}
	result, _ := json.Marshal(chunk)
	return FormatSSEEvent(result)
}

// BuildOpenAISSEDone creates the final [DONE] SSE event.
// Note: This returns raw "[DONE]" without "data:" prefix.
// The SSE "data:" prefix is added by the Handler layer (e.g., openai_handlers.go)
// to maintain architectural consistency and avoid double-prefix issues.
func BuildOpenAISSEDone() string {
	return "[DONE]"
}

// buildBaseChunk creates a base chunk structure for streaming
func buildBaseChunk(state *OpenAIStreamState, delta map[string]interface{}, finishReason *string) map[string]interface{} {
	choice := map[string]interface{}{
		"index": 0,
		"delta": delta,
	}

	if finishReason != nil {
		choice["finish_reason"] = *finishReason
	} else {
		choice["finish_reason"] = nil
	}

	return map[string]interface{}{
		"id":      state.ResponseID,
		"object":  "chat.completion.chunk",
		"created": state.Created,
		"model":   state.Model,
		"choices": []map[string]interface{}{choice},
	}
}

// BuildOpenAISSEReasoningDelta creates an SSE event for reasoning content delta
// This is used for o1/o3 style models that expose reasoning tokens
func BuildOpenAISSEReasoningDelta(state *OpenAIStreamState, reasoningDelta string) string {
	delta := map[string]interface{}{
		"reasoning_content": reasoningDelta,
	}

	// Include role in first chunk
	if !state.HasSentFirstChunk {
		delta["role"] = "assistant"
		state.HasSentFirstChunk = true
	}

	chunk := buildBaseChunk(state, delta, nil)
	result, _ := json.Marshal(chunk)
	state.ChunkIndex++
	return FormatSSEEvent(result)
}

// BuildOpenAISSEFirstChunk creates the first chunk with role only
func BuildOpenAISSEFirstChunk(state *OpenAIStreamState) string {
	delta := map[string]interface{}{
		"role":    "assistant",
		"content": "",
	}

	state.HasSentFirstChunk = true
	chunk := buildBaseChunk(state, delta, nil)
	result, _ := json.Marshal(chunk)
	state.ChunkIndex++
	return FormatSSEEvent(result)
}

// ThinkingTagState tracks state for thinking tag detection in streaming
type ThinkingTagState struct {
	InThinkingBlock   bool
	PendingStartChars int
	PendingEndChars   int
}

// NewThinkingTagState creates a new thinking tag state
func NewThinkingTagState() *ThinkingTagState {
	return &ThinkingTagState{
		InThinkingBlock:   false,
		PendingStartChars: 0,
		PendingEndChars:   0,
	}
}