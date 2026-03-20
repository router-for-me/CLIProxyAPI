package responses

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertOpenAIResponsesRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	if output, ok := convertOpenAIResponsesRequestToCodexFast(inputRawJSON); ok {
		return output
	}

	return convertOpenAIResponsesRequestToCodexSlow(inputRawJSON)
}

func convertOpenAIResponsesRequestToCodexFast(inputRawJSON []byte) ([]byte, bool) {
	if len(inputRawJSON) == 0 || !gjson.ValidBytes(inputRawJSON) {
		return nil, false
	}

	input := gjson.GetBytes(inputRawJSON, "input")
	if input.Exists() && input.IsArray() && inputHasSystemRole(input) {
		return nil, false
	}

	rawJSON, err := applyCodexTopLevelCompatibility(inputRawJSON)
	if err != nil {
		return nil, false
	}

	rawJSON, err = normalizeStringInput(rawJSON)
	if err != nil {
		return nil, false
	}

	return rawJSON, true
}

func convertOpenAIResponsesRequestToCodexSlow(inputRawJSON []byte) []byte {
	payload, err := decodeResponsesRequest(inputRawJSON)
	if err != nil {
		return inputRawJSON
	}

	normalizeResponsesInputSlow(payload)
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

	convertSystemRoleToDeveloperSlow(payload)

	rawJSON, err := json.Marshal(payload)
	if err != nil {
		return inputRawJSON
	}
	return rawJSON
}

func applyCodexTopLevelCompatibility(rawJSON []byte) ([]byte, error) {
	var err error
	rawJSON, err = sjson.SetBytes(rawJSON, "stream", true)
	if err != nil {
		return nil, err
	}
	rawJSON, err = sjson.SetBytes(rawJSON, "store", false)
	if err != nil {
		return nil, err
	}
	rawJSON, err = sjson.SetBytes(rawJSON, "parallel_tool_calls", true)
	if err != nil {
		return nil, err
	}
	rawJSON, err = sjson.SetBytes(rawJSON, "include", []string{"reasoning.encrypted_content"})
	if err != nil {
		return nil, err
	}

	for _, field := range []string{
		"max_output_tokens",
		"max_completion_tokens",
		"temperature",
		"top_p",
		"truncation",
		"user",
		"context_management",
	} {
		rawJSON, err = sjson.DeleteBytes(rawJSON, field)
		if err != nil {
			return nil, err
		}
	}

	tier := gjson.GetBytes(rawJSON, "service_tier")
	if !tier.Exists() || tier.Type != gjson.String || tier.String() != "priority" {
		rawJSON, err = sjson.DeleteBytes(rawJSON, "service_tier")
		if err != nil {
			return nil, err
		}
	}

	return rawJSON, nil
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

func normalizeResponsesInputSlow(payload map[string]any) {
	input, exists := payload["input"]
	if !exists {
		return
	}

	inputText, ok := input.(string)
	if !ok {
		return
	}

	payload["input"] = normalizedStringInput(inputText)
}

func normalizeStringInput(rawJSON []byte) ([]byte, error) {
	input := gjson.GetBytes(rawJSON, "input")
	if !input.Exists() || input.Type != gjson.String {
		return rawJSON, nil
	}

	return sjson.SetBytes(rawJSON, "input", normalizedStringInput(input.String()))
}

func normalizedStringInput(inputText string) []any {
	return []any{
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

func convertSystemRoleToDeveloperSlow(payload map[string]any) {
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

func inputHasSystemRole(input gjson.Result) bool {
	if !input.Exists() || !input.IsArray() {
		return false
	}

	items := input.Array()
	for i := range items {
		if items[i].Get("role").String() == "system" {
			return true
		}
	}
	return false
}
