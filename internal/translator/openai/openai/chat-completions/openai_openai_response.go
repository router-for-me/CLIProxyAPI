// Package chat_completions provides passthrough response translation for OpenAI Chat Completions.
// It normalizes OpenAI-compatible SSE lines by stripping the "data:" prefix and dropping "[DONE]".
package chat_completions

import (
	"bytes"
	"context"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertOpenAIResponseToOpenAI normalizes a single chunk of an OpenAI-compatible streaming response.
// If the chunk is an SSE "data:" line, the prefix is stripped and the remaining JSON payload is returned.
// The "[DONE]" marker yields no output.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Gemini CLI API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - [][]byte: A slice of JSON payload chunks in OpenAI format.
func ConvertOpenAIResponseToOpenAI(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}
	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		return [][]byte{}
	}
	if converted := convertOpenAINonStreamToChunkSequence(rawJSON); len(converted) > 0 {
		return converted
	}
	return [][]byte{rawJSON}
}

// ConvertOpenAIResponseToOpenAINonStream passes through a non-streaming OpenAI response.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Gemini CLI API
//   - param: A pointer to a parameter object for the conversion
//
// Returns:
//   - []byte: The OpenAI-compatible JSON response.
func ConvertOpenAIResponseToOpenAINonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	return rawJSON
}

func convertOpenAINonStreamToChunkSequence(rawJSON []byte) [][]byte {
	trimmed := bytes.TrimSpace(rawJSON)
	if len(trimmed) == 0 || !gjson.ValidBytes(trimmed) {
		return nil
	}
	root := gjson.ParseBytes(trimmed)
	if strings.TrimSpace(root.Get("object").String()) != "chat.completion" {
		return nil
	}

	choices := root.Get("choices")
	if !choices.Exists() || !choices.IsArray() {
		return nil
	}

	base := `{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[]}`
	if id := root.Get("id"); id.Exists() {
		base, _ = sjson.Set(base, "id", id.String())
	}
	base, _ = sjson.Set(base, "object", "chat.completion.chunk")
	if created := root.Get("created"); created.Exists() {
		base, _ = sjson.Set(base, "created", created.Value())
	}
	if model := root.Get("model"); model.Exists() {
		base, _ = sjson.Set(base, "model", model.String())
	}
	if systemFingerprint := root.Get("system_fingerprint"); systemFingerprint.Exists() {
		base, _ = sjson.Set(base, "system_fingerprint", systemFingerprint.Value())
	}
	if extendFields := root.Get("extend_fields"); extendFields.Exists() {
		base, _ = sjson.SetRaw(base, "extend_fields", extendFields.Raw)
	}

	contentChunk := base
	contentChunkNeeded := false
	finishChunk := base
	finishChunkNeeded := false

	for _, choice := range choices.Array() {
		index := choice.Get("index").Int()
		msg := choice.Get("message")
		delta := `{"role":null,"content":null,"reasoning_content":null,"tool_calls":null}`
		hasDelta := false

		role := strings.TrimSpace(msg.Get("role").String())
		if role == "" && (msg.Get("content").Exists() || msg.Get("reasoning_content").Exists() || msg.Get("tool_calls").Exists()) {
			role = "assistant"
		}
		if role != "" {
			delta, _ = sjson.Set(delta, "role", role)
			hasDelta = true
		}
		if content := msg.Get("content"); content.Exists() {
			if text := content.String(); text != "" {
				delta, _ = sjson.Set(delta, "content", text)
				hasDelta = true
			}
		}
		if reasoning := msg.Get("reasoning_content"); reasoning.Exists() {
			if text := reasoning.String(); text != "" {
				delta, _ = sjson.Set(delta, "reasoning_content", text)
				hasDelta = true
			}
		}
		if toolCalls := msg.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
			delta, _ = sjson.SetRaw(delta, "tool_calls", toolCalls.Raw)
			hasDelta = true
		}
		if hasDelta {
			choiceChunk := `{"index":0,"delta":{},"finish_reason":null}`
			choiceChunk, _ = sjson.Set(choiceChunk, "index", index)
			choiceChunk, _ = sjson.SetRaw(choiceChunk, "delta", delta)
			contentChunk, _ = sjson.SetRaw(contentChunk, "choices.-1", choiceChunk)
			contentChunkNeeded = true
		}

		finishReason := strings.TrimSpace(choice.Get("finish_reason").String())
		if finishReason == "" {
			finishReason = "stop"
		}
		finishChoice := `{"index":0,"delta":{},"finish_reason":"stop"}`
		finishChoice, _ = sjson.Set(finishChoice, "index", index)
		finishChoice, _ = sjson.Set(finishChoice, "finish_reason", finishReason)
		if nativeFinishReason := choice.Get("native_finish_reason"); nativeFinishReason.Exists() {
			finishChoice, _ = sjson.Set(finishChoice, "native_finish_reason", nativeFinishReason.Value())
		}
		finishChunk, _ = sjson.SetRaw(finishChunk, "choices.-1", finishChoice)
		finishChunkNeeded = true
	}

	if !finishChunkNeeded {
		return nil
	}
	if usage := root.Get("usage"); usage.Exists() {
		finishChunk, _ = sjson.SetRaw(finishChunk, "usage", usage.Raw)
	}

	out := make([][]byte, 0, 2)
	if contentChunkNeeded {
		out = append(out, []byte(contentChunk))
	}
	out = append(out, []byte(finishChunk))
	return out
}
