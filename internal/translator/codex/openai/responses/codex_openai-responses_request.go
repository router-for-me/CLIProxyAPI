package responses

import (
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertOpenAIResponsesRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := util.NormalizeOpenAIResponsesRequestJSON(inputRawJSON)

	inputResult := gjson.GetBytes(rawJSON, "input")
	if inputResult.Type == gjson.String {
		input, _ := sjson.SetBytes([]byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`), "0.content.0.text", inputResult.String())
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "input", input)
	}

	rawJSON, _ = sjson.SetBytes(rawJSON, "stream", true)
	rawJSON, _ = sjson.SetBytes(rawJSON, "store", false)
	rawJSON, _ = sjson.SetBytes(rawJSON, "parallel_tool_calls", true)
	rawJSON, _ = sjson.SetBytes(rawJSON, "include", []string{"reasoning.encrypted_content"})
	// Codex Responses rejects token limit fields, so strip them out before forwarding.
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_output_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_completion_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "temperature")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "top_p")
	if v := gjson.GetBytes(rawJSON, "service_tier"); v.Exists() {
		if v.String() != "priority" {
			rawJSON, _ = sjson.DeleteBytes(rawJSON, "service_tier")
		}
	}

	rawJSON, _ = sjson.DeleteBytes(rawJSON, "truncation")
	rawJSON = applyResponsesCompactionCompatibility(rawJSON)
	rawJSON = normalizeResponsesStructuredOutputSchema(rawJSON)

	// Delete the user field as it is not supported by the Codex upstream.
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "user")

	// Convert role "system" to "developer" in input array to comply with Codex API requirements.
	rawJSON = convertSystemRoleToDeveloper(rawJSON)
	rawJSON = normalizeCodexBuiltinTools(rawJSON)
	rawJSON = dedupeCodexResponsesFunctionCalls(rawJSON)

	return rawJSON
}

func normalizeResponsesStructuredOutputSchema(rawJSON []byte) []byte {
	textFormat := gjson.GetBytes(rawJSON, "text.format")
	if !textFormat.Exists() || textFormat.Get("type").String() != "json_schema" {
		return rawJSON
	}

	schema := textFormat.Get("schema")
	cleaned := util.CleanJSONSchemaForOpenAIStructuredOutput("")
	if schema.Exists() {
		cleaned = util.CleanJSONSchemaForOpenAIStructuredOutput(schema.Raw)
	}

	updated, err := sjson.SetRawBytes(rawJSON, "text.format.schema", []byte(cleaned))
	if err != nil {
		return rawJSON
	}
	return updated
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
	result := rawJSON

	// Directly modify role values for items with "system" role
	for i := 0; i < len(inputArray); i++ {
		rolePath := fmt.Sprintf("input.%d.role", i)
		if gjson.GetBytes(result, rolePath).String() == "system" {
			result, _ = sjson.SetBytes(result, rolePath, "developer")
		}
	}

	return result
}

// normalizeCodexBuiltinTools rewrites legacy/preview built-in tool variants to the
// stable names expected by the current Codex upstream.
func normalizeCodexBuiltinTools(rawJSON []byte) []byte {
	result := rawJSON

	tools := gjson.GetBytes(result, "tools")
	if tools.IsArray() {
		toolArray := tools.Array()
		for i := 0; i < len(toolArray); i++ {
			typePath := fmt.Sprintf("tools.%d.type", i)
			result = normalizeCodexBuiltinToolAtPath(result, typePath)
		}
	}

	result = normalizeCodexBuiltinToolAtPath(result, "tool_choice.type")

	toolChoiceTools := gjson.GetBytes(result, "tool_choice.tools")
	if toolChoiceTools.IsArray() {
		toolArray := toolChoiceTools.Array()
		for i := 0; i < len(toolArray); i++ {
			typePath := fmt.Sprintf("tool_choice.tools.%d.type", i)
			result = normalizeCodexBuiltinToolAtPath(result, typePath)
		}
	}

	return result
}

func normalizeCodexBuiltinToolAtPath(rawJSON []byte, path string) []byte {
	currentType := gjson.GetBytes(rawJSON, path).String()
	normalizedType := normalizeCodexBuiltinToolType(currentType)
	if normalizedType == "" {
		return rawJSON
	}

	updated, err := sjson.SetBytes(rawJSON, path, normalizedType)
	if err != nil {
		return rawJSON
	}

	log.Debugf("codex responses: normalized builtin tool type at %s from %q to %q", path, currentType, normalizedType)
	return updated
}

// normalizeCodexBuiltinToolType centralizes the current known Codex Responses
// built-in tool alias compatibility. If Codex introduces more legacy aliases,
// extend this helper instead of adding path-specific rewrite logic elsewhere.
func normalizeCodexBuiltinToolType(toolType string) string {
	switch toolType {
	case "web_search_preview", "web_search_preview_2025_03_11":
		return "web_search"
	default:
		return ""
	}
}

func dedupeCodexResponsesFunctionCalls(rawJSON []byte) []byte {
	inputResult := gjson.GetBytes(rawJSON, "input")
	if !inputResult.IsArray() {
		return rawJSON
	}

	var root map[string]any
	if err := json.Unmarshal(rawJSON, &root); err != nil {
		return rawJSON
	}
	input, ok := root["input"].([]any)
	if !ok || len(input) == 0 {
		return rawJSON
	}

	seenCallIDs := make(map[string]bool)
	cleaned := make([]any, 0, len(input))
	changed := false
	for _, rawItem := range input {
		item, okItem := rawItem.(map[string]any)
		if !okItem {
			cleaned = append(cleaned, rawItem)
			continue
		}
		if !isCodexResponsesFunctionCallType(stringValue(item["type"])) {
			cleaned = append(cleaned, rawItem)
			continue
		}
		callID := stringValue(item["call_id"])
		if callID == "" {
			cleaned = append(cleaned, rawItem)
			continue
		}
		if seenCallIDs[callID] {
			changed = true
			continue
		}
		seenCallIDs[callID] = true
		cleaned = append(cleaned, rawItem)
	}

	if !changed {
		return rawJSON
	}
	root["input"] = cleaned
	out, err := json.Marshal(root)
	if err != nil || !gjson.ValidBytes(out) {
		return rawJSON
	}
	return out
}

func isCodexResponsesFunctionCallType(itemType string) bool {
	switch itemType {
	case "function_call", "custom_tool_call":
		return true
	default:
		return false
	}
}

func stringValue(value any) string {
	str, _ := value.(string)
	return str
}
