// Package openai provides response translation from Kiro to OpenAI format.
// This package handles the conversion of Kiro API responses into OpenAI Chat Completions-compatible
// JSON format, transforming streaming events and non-streaming responses.
package openai

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// functionCallIDCounter provides a process-wide unique counter for function call identifiers.
var functionCallIDCounter uint64

// BuildOpenAIResponse constructs an OpenAI Chat Completions-compatible response.
// Supports tool_calls when tools are present in the response.
// stopReason is passed from upstream; fallback logic applied if empty.
func BuildOpenAIResponse(content string, toolUses []KiroToolUse, model string, usageInfo usage.Detail, stopReason string) []byte {
	return BuildOpenAIResponseWithReasoning(content, "", toolUses, model, usageInfo, stopReason)
}

// BuildOpenAIResponseWithReasoning constructs an OpenAI Chat Completions-compatible response with reasoning_content support.
// Supports tool_calls when tools are present in the response.
// reasoningContent is included as reasoning_content field in the message when present.
// stopReason is passed from upstream; fallback logic applied if empty.
func BuildOpenAIResponseWithReasoning(content, reasoningContent string, toolUses []KiroToolUse, model string, usageInfo usage.Detail, stopReason string) []byte {
	// Build the message object
	message := map[string]interface{}{
		"role":    "assistant",
		"content": content,
	}

	// Add reasoning_content if present (for thinking/reasoning models)
	if reasoningContent != "" {
		message["reasoning_content"] = reasoningContent
	}

	// Add tool_calls if present
	if len(toolUses) > 0 {
		var toolCalls []map[string]interface{}
		for i, tu := range toolUses {
			inputJSON, _ := json.Marshal(tu.Input)
			toolCalls = append(toolCalls, map[string]interface{}{
				"id":    tu.ToolUseID,
				"type":  "function",
				"index": i,
				"function": map[string]interface{}{
					"name":      tu.Name,
					"arguments": string(inputJSON),
				},
			})
		}
		message["tool_calls"] = toolCalls
		// When tool_calls are present, content should be null according to OpenAI spec
		if content == "" {
			message["content"] = nil
		}
	}

	// Use upstream stopReason; apply fallback logic if not provided
	finishReason := mapKiroStopReasonToOpenAI(stopReason)
	if finishReason == "" {
		finishReason = "stop"
		if len(toolUses) > 0 {
			finishReason = "tool_calls"
		}
		log.Debugf("kiro-openai: buildOpenAIResponse using fallback finish_reason: %s", finishReason)
	}

	response := map[string]interface{}{
		"id":      "chatcmpl-" + uuid.New().String()[:24],
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     usageInfo.InputTokens,
			"completion_tokens": usageInfo.OutputTokens,
			"total_tokens":      usageInfo.InputTokens + usageInfo.OutputTokens,
		},
	}

	result, _ := json.Marshal(response)
	return result
}

// mapKiroStopReasonToOpenAI converts Kiro/Claude stop_reason to OpenAI finish_reason
func mapKiroStopReasonToOpenAI(stopReason string) string {
	switch stopReason {
	case "end_turn":
		return "stop"
	case "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "content_filtered":
		return "content_filter"
	default:
		return stopReason
	}
}

// BuildOpenAIStreamChunk constructs an OpenAI Chat Completions streaming chunk.
// This is the delta format used in streaming responses.
func BuildOpenAIStreamChunk(model string, deltaContent string, deltaToolCalls []map[string]interface{}, finishReason string, index int) []byte {
	delta := map[string]interface{}{}

	// First chunk should include role
	if index == 0 && deltaContent == "" && len(deltaToolCalls) == 0 {
		delta["role"] = "assistant"
		delta["content"] = ""
	} else if deltaContent != "" {
		delta["content"] = deltaContent
	}

	// Add tool_calls delta if present
	if len(deltaToolCalls) > 0 {
		delta["tool_calls"] = deltaToolCalls
	}

	choice := map[string]interface{}{
		"index": 0,
		"delta": delta,
	}

	if finishReason != "" {
		choice["finish_reason"] = finishReason
	} else {
		choice["finish_reason"] = nil
	}

	chunk := map[string]interface{}{
		"id":      "chatcmpl-" + uuid.New().String()[:12],
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{choice},
	}

	result, _ := json.Marshal(chunk)
	return result
}

// BuildOpenAIStreamChunkWithToolCallStart creates a stream chunk for tool call start
func BuildOpenAIStreamChunkWithToolCallStart(model string, toolUseID, toolName string, toolIndex int) []byte {
	toolCall := map[string]interface{}{
		"index": toolIndex,
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

	choice := map[string]interface{}{
		"index":         0,
		"delta":         delta,
		"finish_reason": nil,
	}

	chunk := map[string]interface{}{
		"id":      "chatcmpl-" + uuid.New().String()[:12],
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{choice},
	}

	result, _ := json.Marshal(chunk)
	return result
}

// BuildOpenAIStreamChunkWithToolCallDelta creates a stream chunk for tool call arguments delta
func BuildOpenAIStreamChunkWithToolCallDelta(model string, argumentsDelta string, toolIndex int) []byte {
	toolCall := map[string]interface{}{
		"index": toolIndex,
		"function": map[string]interface{}{
			"arguments": argumentsDelta,
		},
	}

	delta := map[string]interface{}{
		"tool_calls": []map[string]interface{}{toolCall},
	}

	choice := map[string]interface{}{
		"index":         0,
		"delta":         delta,
		"finish_reason": nil,
	}

	chunk := map[string]interface{}{
		"id":      "chatcmpl-" + uuid.New().String()[:12],
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{choice},
	}

	result, _ := json.Marshal(chunk)
	return result
}

// BuildOpenAIStreamDoneChunk creates the final [DONE] stream event
func BuildOpenAIStreamDoneChunk() []byte {
	return []byte("data: [DONE]")
}

// BuildOpenAIStreamFinishChunk creates the final chunk with finish_reason
func BuildOpenAIStreamFinishChunk(model string, finishReason string) []byte {
	choice := map[string]interface{}{
		"index":         0,
		"delta":         map[string]interface{}{},
		"finish_reason": finishReason,
	}

	chunk := map[string]interface{}{
		"id":      "chatcmpl-" + uuid.New().String()[:12],
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{choice},
	}

	result, _ := json.Marshal(chunk)
	return result
}

// BuildOpenAIStreamUsageChunk creates a chunk with usage information (optional, for stream_options.include_usage)
func BuildOpenAIStreamUsageChunk(model string, usageInfo usage.Detail) []byte {
	chunk := map[string]interface{}{
		"id":      "chatcmpl-" + uuid.New().String()[:12],
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{},
		"usage": map[string]interface{}{
			"prompt_tokens":     usageInfo.InputTokens,
			"completion_tokens": usageInfo.OutputTokens,
			"total_tokens":      usageInfo.InputTokens + usageInfo.OutputTokens,
		},
	}

	result, _ := json.Marshal(chunk)
	return result
}

// GenerateToolCallID generates a unique tool call ID in OpenAI format
func GenerateToolCallID(toolName string) string {
	return fmt.Sprintf("call_%s_%d_%d", toolName[:min(8, len(toolName))], time.Now().UnixNano(), atomic.AddUint64(&functionCallIDCounter, 1))
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}