package responses

import (
	"bytes"
	"encoding/json"
)

func ConvertOpenAIResponsesRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	payload, err := decodeResponsesRequest(inputRawJSON)
	if err != nil {
		return inputRawJSON
	}

	normalizeResponsesInput(payload)
	payload["stream"] = true
	payload["store"] = false
	payload["parallel_tool_calls"] = true
	payload["include"] = []string{"reasoning.encrypted_content"}

	delete(payload, "max_output_tokens")
	delete(payload, "max_completion_tokens")
	delete(payload, "temperature")
	delete(payload, "top_p")
	delete(payload, "truncation")
	delete(payload, "user")
	delete(payload, "context_management")

	if tier, ok := payload["service_tier"].(string); ok {
		if tier != "priority" {
			delete(payload, "service_tier")
		}
	} else {
		delete(payload, "service_tier")
	}

	convertSystemRoleToDeveloper(payload)

	rawJSON, err := json.Marshal(payload)
	if err != nil {
		return inputRawJSON
	}
	return rawJSON
}

func decodeResponsesRequest(inputRawJSON []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(inputRawJSON))
	decoder.UseNumber()

	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func normalizeResponsesInput(payload map[string]any) {
	input, exists := payload["input"]
	if !exists {
		return
	}

	inputText, ok := input.(string)
	if !ok {
		return
	}

	payload["input"] = []any{
		map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{
				map[string]any{
					"type": "input_text",
					"text": inputText,
				},
			},
		},
	}
}

func convertSystemRoleToDeveloper(payload map[string]any) {
	input, ok := payload["input"].([]any)
	if !ok {
		return
	}

	for _, item := range input {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, ok := message["role"].(string)
		if ok && role == "system" {
			message["role"] = "developer"
		}
	}
}
