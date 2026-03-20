package responses

import (
	"bytes"
	"encoding/json"
)

func ConvertOpenAIResponsesRequestToCodex(_ string, inputRawJSON []byte, _ bool) []byte {
	if len(inputRawJSON) == 0 {
		return inputRawJSON
	}

	decoder := json.NewDecoder(bytes.NewReader(inputRawJSON))
	decoder.UseNumber()

	payload := make(map[string]any)
	if err := decoder.Decode(&payload); err != nil {
		// Preserve legacy passthrough behavior for malformed payloads.
		return inputRawJSON
	}

	normalizeOpenAIResponsesInputForCodex(payload)

	payload["stream"] = true
	payload["store"] = false
	payload["parallel_tool_calls"] = true
	payload["include"] = []string{"reasoning.encrypted_content"}

	// Codex Responses rejects these OpenAI Responses fields.
	delete(payload, "max_output_tokens")
	delete(payload, "max_completion_tokens")
	delete(payload, "temperature")
	delete(payload, "top_p")
	delete(payload, "truncation")
	delete(payload, "context_management")
	delete(payload, "user")

	if tier, ok := payload["service_tier"].(string); !ok || tier != "priority" {
		delete(payload, "service_tier")
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return inputRawJSON
	}
	return encoded
}

func normalizeOpenAIResponsesInputForCodex(payload map[string]any) {
	if payload == nil {
		return
	}

	switch input := payload["input"].(type) {
	case string:
		payload["input"] = []map[string]any{
			{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{
						"type": "input_text",
						"text": input,
					},
				},
			},
		}
	case []any:
		for _, rawItem := range input {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			role, ok := item["role"].(string)
			if !ok || role != "system" {
				continue
			}
			item["role"] = "developer"
		}
	}
}
