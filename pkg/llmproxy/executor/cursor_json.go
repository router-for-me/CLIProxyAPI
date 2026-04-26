package executor

import "encoding/json"

func cursorCompletionJSON(id string, created int64, model string, content string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": content,
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	})
}

func cursorChunkJSON(id string, created int64, model string, delta json.RawMessage, finishReason string) ([]byte, error) {
	var parsedDelta any
	if err := json.Unmarshal(delta, &parsedDelta); err != nil {
		return nil, err
	}
	var finish any
	if finishReason != "" {
		finish = finishReason
	}
	return json.Marshal(map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         parsedDelta,
			"finish_reason": finish,
		}},
	})
}

func cursorUsageChunkJSON(id string, created int64, model string, inputTokens int64, outputTokens int64) ([]byte, error) {
	return json.Marshal(map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": "stop",
		}},
		"usage": map[string]int64{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outputTokens,
			"total_tokens":      inputTokens + outputTokens,
		},
	})
}

func cursorContentDeltaJSON(content string) string {
	return string(mustCursorMarshal(map[string]any{"content": content}))
}

func cursorToolCallDeltaJSON(index int, id string, name string, args string) string {
	return string(mustCursorMarshal(map[string]any{
		"tool_calls": []map[string]any{{
			"index": index,
			"id":    id,
			"type":  "function",
			"function": map[string]any{
				"name":      name,
				"arguments": args,
			},
		}},
	}))
}

func mustCursorMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
