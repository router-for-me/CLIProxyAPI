package oairesponses

import (
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

const (
	geminiResponsesInputTypeCustomToolCall       = "custom_tool_call"
	geminiResponsesInputTypeCustomToolCallOutput = "custom_tool_call_output"
	geminiResponsesInputTypeFunctionCallOutput   = "function_call_output"
)

const (
	ResponsesInputTypeCustomToolCallOutput = geminiResponsesInputTypeCustomToolCallOutput
)

func PrepareOpenAIResponsesRequest(request dto.OpenAIResponsesRequest) (dto.OpenAIResponsesRequest, error) {
	tools, err := filterGeminiResponsesTools(request.Tools)
	if err != nil {
		return request, err
	}
	request.Tools = tools

	input, err := filterGeminiResponsesInput(request.Input)
	if err != nil {
		return request, err
	}
	request.Input = input

	return request, nil
}

func filterGeminiResponsesTools(raw []byte) ([]byte, error) {
	if !geminiRawJSONPresent(raw) || kitutil.GetJsonType(raw) != "array" {
		return raw, nil
	}

	var tools []map[string]any
	if err := kitutil.Unmarshal(raw, &tools); err != nil {
		return nil, err
	}

	filtered := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if strings.TrimSpace(kitutil.Interface2String(tool["type"])) != "function" {
			continue
		}
		filtered = append(filtered, tool)
	}
	if len(filtered) == 0 {
		return nil, nil
	}
	return kitutil.Marshal(filtered)
}

func filterGeminiResponsesInput(raw []byte) ([]byte, error) {
	if !geminiRawJSONPresent(raw) || kitutil.GetJsonType(raw) != "array" {
		return raw, nil
	}

	var items []map[string]any
	if err := kitutil.Unmarshal(raw, &items); err != nil {
		return nil, err
	}

	skippedCustomCallIDs := make(map[string]struct{})
	for _, item := range items {
		if strings.TrimSpace(kitutil.Interface2String(item["type"])) != geminiResponsesInputTypeCustomToolCall {
			continue
		}
		if callID := strings.TrimSpace(kitutil.Interface2String(item["call_id"])); callID != "" {
			skippedCustomCallIDs[callID] = struct{}{}
		}
	}

	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		itemType := strings.TrimSpace(kitutil.Interface2String(item["type"]))
		switch itemType {
		case geminiResponsesInputTypeCustomToolCall, geminiResponsesInputTypeCustomToolCallOutput:
			continue
		case geminiResponsesInputTypeFunctionCallOutput:
			if _, ok := skippedCustomCallIDs[strings.TrimSpace(kitutil.Interface2String(item["call_id"]))]; ok {
				continue
			}
		}
		filtered = append(filtered, item)
	}

	return kitutil.Marshal(filtered)
}

func geminiRawJSONPresent(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	return kitutil.GetJsonType(raw) != "null"
}
