package responses

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertOpenAIResponsesRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON

	inputResult := gjson.GetBytes(rawJSON, "input")
	if inputResult.Type == gjson.String {
		input, _ := sjson.Set(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`, "0.content.0.text", inputResult.String())
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "input", []byte(input))
	}

	root := gjson.ParseBytes(rawJSON)

	if !root.Get("stream").Exists() || !root.Get("stream").Bool() {
		rawJSON, _ = sjson.SetBytes(rawJSON, "stream", true)
	}
	if !root.Get("store").Exists() || root.Get("store").Bool() {
		rawJSON, _ = sjson.SetBytes(rawJSON, "store", false)
	}
	if !root.Get("parallel_tool_calls").Exists() || !root.Get("parallel_tool_calls").Bool() {
		rawJSON, _ = sjson.SetBytes(rawJSON, "parallel_tool_calls", true)
	}
	rawJSON, _ = sjson.SetBytes(rawJSON, "include", []string{"reasoning.encrypted_content"})
	// Codex Responses rejects token limit fields, so strip them out before forwarding.
	if root.Get("max_output_tokens").Exists() {
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_output_tokens")
	}
	if root.Get("max_completion_tokens").Exists() {
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_completion_tokens")
	}
	if root.Get("temperature").Exists() {
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "temperature")
	}
	if root.Get("top_p").Exists() {
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "top_p")
	}
	if v := root.Get("service_tier"); v.Exists() {
		if v.String() != "priority" {
			rawJSON, _ = sjson.DeleteBytes(rawJSON, "service_tier")
		}
	}

	if root.Get("truncation").Exists() {
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "truncation")
	}
	if root.Get("context_management").Exists() {
		rawJSON = applyResponsesCompactionCompatibility(rawJSON)
	}

	// Delete the user field as it is not supported by the Codex upstream.
	if root.Get("user").Exists() {
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "user")
	}

	// Convert role "system" to "developer" in input array to comply with Codex API requirements.
	rawJSON = convertSystemRoleToDeveloper(rawJSON)

	return rawJSON
}

// applyResponsesCompactionCompatibility handles OpenAI Responses context_management.compaction
// for Codex upstream compatibility.
//
// Codex /responses currently rejects context_management with:
// {"detail":"Unsupported parameter: context_management"}.
//
// Compatibility strategy:
// 1) Remove context_management before forwarding to Codex upstream.
func applyResponsesCompactionCompatibility(rawJSON []byte) []byte {
	if !gjson.GetBytes(rawJSON, "context_management").Exists() {
		return rawJSON
	}

	rawJSON, _ = sjson.DeleteBytes(rawJSON, "context_management")
	return rawJSON
}

// convertSystemRoleToDeveloper traverses the input array and converts any message items
// with role "system" to role "developer". This is necessary because Codex API does not
// accept "system" role in the input array.
func convertSystemRoleToDeveloper(rawJSON []byte) []byte {
	inputResult := gjson.GetBytes(rawJSON, "input")
	if !inputResult.IsArray() {
		return rawJSON
	}

	inputArray := inputResult.Array()
	systemIndexes := make([]int, 0, len(inputArray))
	for i := 0; i < len(inputArray); i++ {
		if inputArray[i].Get("role").String() == "system" {
			systemIndexes = append(systemIndexes, i)
		}
	}
	if len(systemIndexes) == 0 {
		return rawJSON
	}

	result := rawJSON
	for _, i := range systemIndexes {
		rolePath := fmt.Sprintf("input.%d.role", i)
		result, _ = sjson.SetBytes(result, rolePath, "developer")
	}

	return result
}
