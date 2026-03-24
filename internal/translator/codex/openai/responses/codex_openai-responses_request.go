package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexResponsesIncludeRaw = `["reasoning.encrypted_content"]`

func ConvertOpenAIResponsesRequestToCodex(_ string, inputRawJSON []byte, _ bool) []byte {
	if len(inputRawJSON) == 0 {
		return inputRawJSON
	}
	if !gjson.ValidBytes(inputRawJSON) {
		// Preserve legacy passthrough behavior for malformed payloads.
		return inputRawJSON
	}

	if output, ok := convertOpenAIResponsesRequestToCodexFastPath(inputRawJSON); ok {
		return output
	}

	return normalizeCodexBuiltinTools(convertOpenAIResponsesRequestToCodexFallback(inputRawJSON))
}

func convertOpenAIResponsesRequestToCodexFastPath(inputRawJSON []byte) ([]byte, bool) {
	root := gjson.ParseBytes(inputRawJSON)
	if !root.IsObject() {
		return nil, false
	}

	output := make([]byte, 0, len(inputRawJSON)+96)
	output = append(output, '{')
	wroteField := false
	hasStream := false
	hasStore := false
	hasParallelToolCalls := false
	hasInclude := false
	hasInstructions := false
	supported := true

	root.ForEach(func(key, value gjson.Result) bool {
		field := key.Str
		switch field {
		case "max_output_tokens", "max_completion_tokens", "temperature", "top_p", "truncation", "context_management", "user":
			return true
		case "input":
			output = appendJSONObjectFieldName(output, field, &wroteField)
			var ok bool
			output, ok = appendNormalizedOpenAIResponsesInput(output, value)
			if !ok {
				supported = false
				return false
			}
			return true
		case "stream":
			hasStream = true
			output = appendJSONObjectFieldName(output, field, &wroteField)
			output = append(output, "true"...)
			return true
		case "store":
			hasStore = true
			output = appendJSONObjectFieldName(output, field, &wroteField)
			output = append(output, "false"...)
			return true
		case "parallel_tool_calls":
			hasParallelToolCalls = true
			output = appendJSONObjectFieldName(output, field, &wroteField)
			output = append(output, "true"...)
			return true
		case "include":
			hasInclude = true
			output = appendJSONObjectFieldName(output, field, &wroteField)
			output = append(output, codexResponsesIncludeRaw...)
			return true
		case "instructions":
			hasInstructions = true
			output = appendJSONObjectFieldName(output, field, &wroteField)
			output = append(output, value.Raw...)
			return true
		case "tools":
			output = appendJSONObjectFieldName(output, field, &wroteField)
			output = appendNormalizedCodexFastPathTools(output, value.Raw)
			return true
		case "tool_choice":
			output = appendJSONObjectFieldName(output, field, &wroteField)
			output = appendNormalizedCodexFastPathToolChoice(output, value.Raw)
			return true
		case "service_tier":
			if value.Type == gjson.String && value.Str == "priority" {
				output = appendJSONObjectFieldName(output, field, &wroteField)
				output = append(output, value.Raw...)
			}
			return true
		default:
			output = appendJSONObjectFieldName(output, field, &wroteField)
			output = append(output, value.Raw...)
			return true
		}
	})

	if !supported {
		return nil, false
	}

	if !hasStream {
		output = appendJSONObjectFieldName(output, "stream", &wroteField)
		output = append(output, "true"...)
	}
	if !hasStore {
		output = appendJSONObjectFieldName(output, "store", &wroteField)
		output = append(output, "false"...)
	}
	if !hasParallelToolCalls {
		output = appendJSONObjectFieldName(output, "parallel_tool_calls", &wroteField)
		output = append(output, "true"...)
	}
	if !hasInclude {
		output = appendJSONObjectFieldName(output, "include", &wroteField)
		output = append(output, codexResponsesIncludeRaw...)
	}
	if !hasInstructions {
		output = appendJSONObjectFieldName(output, "instructions", &wroteField)
		output = append(output, `""`...)
	}

	output = append(output, '}')
	return output, true
}

func appendJSONObjectFieldName(dst []byte, field string, wroteField *bool) []byte {
	if *wroteField {
		dst = append(dst, ',')
	} else {
		*wroteField = true
	}
	if raw := fastJSONObjectFieldName(field); raw != "" {
		return append(dst, raw...)
	}
	dst = strconv.AppendQuote(dst, field)
	dst = append(dst, ':')
	return dst
}

func fastJSONObjectFieldName(field string) string {
	switch field {
	case "model":
		return `"model":`
	case "input":
		return `"input":`
	case "stream":
		return `"stream":`
	case "store":
		return `"store":`
	case "parallel_tool_calls":
		return `"parallel_tool_calls":`
	case "include":
		return `"include":`
	case "instructions":
		return `"instructions":`
	case "tools":
		return `"tools":`
	case "tool_choice":
		return `"tool_choice":`
	case "service_tier":
		return `"service_tier":`
	case "type":
		return `"type":`
	case "role":
		return `"role":`
	case "content":
		return `"content":`
	case "call_id":
		return `"call_id":`
	case "output":
		return `"output":`
	case "name":
		return `"name":`
	case "arguments":
		return `"arguments":`
	case "status":
		return `"status":`
	default:
		return ""
	}
}

func appendNormalizedOpenAIResponsesInput(dst []byte, input gjson.Result) ([]byte, bool) {
	switch input.Type {
	case gjson.String:
		dst = append(dst, `[{"type":"message","role":"user","content":[{"type":"input_text","text":`...)
		dst = append(dst, input.Raw...)
		dst = append(dst, `}]}]`...)
		return dst, true
	case gjson.JSON:
		if !input.IsArray() {
			dst = append(dst, input.Raw...)
			return dst, true
		}

		hasSystemRole := false
		input.ForEach(func(_, item gjson.Result) bool {
			if !item.IsObject() {
				return true
			}
			role := item.Get("role")
			if role.Type == gjson.String && role.Str == "system" {
				hasSystemRole = true
				return false
			}
			return true
		})
		if !hasSystemRole {
			dst = append(dst, input.Raw...)
			return dst, true
		}

		dst = append(dst, '[')
		wroteItem := false
		ok := true
		input.ForEach(func(_, item gjson.Result) bool {
			if wroteItem {
				dst = append(dst, ',')
			} else {
				wroteItem = true
			}
			var itemOK bool
			dst, itemOK = appendNormalizedOpenAIResponsesInputItem(dst, item)
			if !itemOK {
				ok = false
				return false
			}
			return true
		})
		if !ok {
			return nil, false
		}
		dst = append(dst, ']')
		return dst, true
	default:
		dst = append(dst, input.Raw...)
		return dst, true
	}
}

func appendNormalizedOpenAIResponsesInputItem(dst []byte, item gjson.Result) ([]byte, bool) {
	if !item.IsObject() {
		dst = append(dst, item.Raw...)
		return dst, true
	}

	role := item.Get("role")
	if role.Type != gjson.String || role.Str != "system" {
		dst = append(dst, item.Raw...)
		return dst, true
	}

	dst = append(dst, '{')
	wroteField := false
	item.ForEach(func(key, value gjson.Result) bool {
		field := key.Str
		dst = appendJSONObjectFieldName(dst, field, &wroteField)
		if field == "role" {
			dst = append(dst, `"developer"`...)
			return true
		}
		dst = append(dst, value.Raw...)
		return true
	})
	dst = append(dst, '}')
	return dst, true
}

func appendNormalizedCodexFastPathTools(dst []byte, raw string) []byte {
	if !strings.Contains(raw, "web_search_preview") {
		return append(dst, raw...)
	}

	result := []byte(raw)
	tools := gjson.Parse(raw)
	if !tools.IsArray() {
		return append(dst, raw...)
	}
	toolArray := tools.Array()
	for i := 0; i < len(toolArray); i++ {
		typePath := fmt.Sprintf("%d.type", i)
		if normalized := normalizeCodexBuiltinToolType(gjson.GetBytes(result, typePath).Str); normalized != "" {
			if updated, err := sjson.SetBytes(result, typePath, normalized); err == nil {
				result = updated
			}
		}
	}
	return append(dst, result...)
}

func appendNormalizedCodexFastPathToolChoice(dst []byte, raw string) []byte {
	if !strings.Contains(raw, "web_search_preview") {
		return append(dst, raw...)
	}

	result := []byte(raw)
	if normalized := normalizeCodexBuiltinToolType(gjson.GetBytes(result, "type").Str); normalized != "" {
		if updated, err := sjson.SetBytes(result, "type", normalized); err == nil {
			result = updated
		}
	}
	toolChoiceTools := gjson.GetBytes(result, "tools")
	if toolChoiceTools.IsArray() {
		toolArray := toolChoiceTools.Array()
		for i := 0; i < len(toolArray); i++ {
			typePath := fmt.Sprintf("tools.%d.type", i)
			if normalized := normalizeCodexBuiltinToolType(gjson.GetBytes(result, typePath).Str); normalized != "" {
				if updated, err := sjson.SetBytes(result, typePath, normalized); err == nil {
					result = updated
				}
			}
		}
	}
	return append(dst, result...)
}

// normalizeCodexBuiltinTools rewrites preview built-in tool variants to the
// stable names expected by the current Codex upstream.
func normalizeCodexBuiltinTools(rawJSON []byte) []byte {
	if !bytes.Contains(rawJSON, []byte("web_search_preview")) {
		return rawJSON
	}

	result := rawJSON

	tools := gjson.GetBytes(result, "tools")
	if tools.IsArray() {
		toolArray := tools.Array()
		for i := 0; i < len(toolArray); i++ {
			typePath := fmt.Sprintf("tools.%d.type", i)
			if normalized := normalizeCodexBuiltinToolType(gjson.GetBytes(result, typePath).Str); normalized != "" {
				if updated, err := sjson.SetBytes(result, typePath, normalized); err == nil {
					result = updated
				}
			}
		}
	}

	if normalized := normalizeCodexBuiltinToolType(gjson.GetBytes(result, "tool_choice.type").Str); normalized != "" {
		if updated, err := sjson.SetBytes(result, "tool_choice.type", normalized); err == nil {
			result = updated
		}
	}

	toolChoiceTools := gjson.GetBytes(result, "tool_choice.tools")
	if toolChoiceTools.IsArray() {
		toolArray := toolChoiceTools.Array()
		for i := 0; i < len(toolArray); i++ {
			typePath := fmt.Sprintf("tool_choice.tools.%d.type", i)
			if normalized := normalizeCodexBuiltinToolType(gjson.GetBytes(result, typePath).Str); normalized != "" {
				if updated, err := sjson.SetBytes(result, typePath, normalized); err == nil {
					result = updated
				}
			}
		}
	}

	return result
}

func normalizeCodexBuiltinToolType(toolType string) string {
	switch toolType {
	case "web_search_preview", "web_search_preview_2025_03_11":
		return "web_search"
	default:
		return ""
	}
}

func convertOpenAIResponsesRequestToCodexFallback(inputRawJSON []byte) []byte {
	output := inputRawJSON
	var err error
	if output, err = normalizeOpenAIResponsesInputForCodexFallback(output); err != nil {
		return inputRawJSON
	}
	if output, err = sjson.SetBytes(output, "stream", true); err != nil {
		return inputRawJSON
	}
	if output, err = sjson.SetBytes(output, "store", false); err != nil {
		return inputRawJSON
	}
	if output, err = sjson.SetBytes(output, "parallel_tool_calls", true); err != nil {
		return inputRawJSON
	}
	if output, err = sjson.SetRawBytes(output, "include", []byte(codexResponsesIncludeRaw)); err != nil {
		return inputRawJSON
	}
	if !gjson.GetBytes(output, "instructions").Exists() {
		if output, err = sjson.SetBytes(output, "instructions", ""); err != nil {
			return inputRawJSON
		}
	}

	// Codex Responses rejects these OpenAI Responses fields.
	for _, path := range []string{
		"max_output_tokens",
		"max_completion_tokens",
		"temperature",
		"top_p",
		"truncation",
		"context_management",
		"user",
	} {
		if output, err = sjson.DeleteBytes(output, path); err != nil {
			return inputRawJSON
		}
	}

	if tier := gjson.GetBytes(output, "service_tier"); tier.Exists() && (tier.Type != gjson.String || tier.Str != "priority") {
		if output, err = sjson.DeleteBytes(output, "service_tier"); err != nil {
			return inputRawJSON
		}
	}

	return output
}

func normalizeOpenAIResponsesInputForCodexFallback(inputRawJSON []byte) ([]byte, error) {
	input := gjson.GetBytes(inputRawJSON, "input")
	switch input.Type {
	case gjson.String:
		encodedText, err := json.Marshal(input.String())
		if err != nil {
			return nil, err
		}
		replacement := []byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":`)
		replacement = append(replacement, encodedText...)
		replacement = append(replacement, []byte(`}]}]`)...)
		return sjson.SetRawBytes(inputRawJSON, "input", replacement)
	case gjson.JSON:
		if !input.IsArray() {
			return inputRawJSON, nil
		}
		output := inputRawJSON
		for index, rawItem := range input.Array() {
			if !rawItem.IsObject() {
				continue
			}
			role := rawItem.Get("role")
			if role.Type != gjson.String || role.Str != "system" {
				continue
			}
			var err error
			output, err = sjson.SetBytes(output, "input."+strconv.Itoa(index)+".role", "developer")
			if err != nil {
				return nil, err
			}
		}
		return output, nil
	default:
		return inputRawJSON, nil
	}
}
