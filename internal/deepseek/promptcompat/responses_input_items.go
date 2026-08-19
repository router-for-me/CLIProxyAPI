package promptcompat

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/prompt"
)

func normalizeResponsesInputItem(m map[string]any) map[string]any {
	return normalizeResponsesInputItemWithState(m, nil)
}

func normalizeResponsesInputItemWithState(m map[string]any, callNameByID map[string]string) map[string]any {
	if m == nil {
		return nil
	}

	role := strings.ToLower(strings.TrimSpace(asString(m["role"])))
	if role != "" {
		if role == "assistant" {
			return normalizeResponsesAssistantMessage(m)
		}
		content := m["content"]
		if content == nil {
			if txt, _ := m["text"].(string); strings.TrimSpace(txt) != "" {
				content = txt
			}
		}
		if content == nil {
			return nil
		}
		out := map[string]any{
			"role":    normalizeOpenAIRoleForPrompt(role),
			"content": content,
		}
		if role == "tool" || role == "function" {
			if callID := strings.TrimSpace(asString(m["tool_call_id"])); callID != "" {
				out["tool_call_id"] = callID
			}
			if callID := strings.TrimSpace(asString(m["call_id"])); callID != "" {
				out["tool_call_id"] = callID
			}
			if name := strings.TrimSpace(asString(m["name"])); name != "" {
				out["name"] = name
			}
		}
		return out
	}

	itemType := strings.ToLower(strings.TrimSpace(asString(m["type"])))
	switch itemType {
	case "message", "input_message":
		role := strings.ToLower(strings.TrimSpace(asString(m["role"])))
		if role == "assistant" {
			return normalizeResponsesAssistantMessage(m)
		}
		content := m["content"]
		if content == nil {
			if txt, _ := m["text"].(string); strings.TrimSpace(txt) != "" {
				content = txt
			}
		}
		if content == nil {
			return nil
		}
		if role == "" {
			role = "user"
		}
		return map[string]any{
			"role":    normalizeOpenAIRoleForPrompt(role),
			"content": content,
		}
	case "function_call_output", "tool_result":
		content := m["output"]
		if content == nil {
			content = m["content"]
		}
		if content == nil {
			content = ""
		}
		out := map[string]any{
			"role":    "tool",
			"content": content,
		}
		if callID := strings.TrimSpace(asString(m["call_id"])); callID != "" {
			out["tool_call_id"] = callID
		} else if callID = strings.TrimSpace(asString(m["tool_call_id"])); callID != "" {
			out["tool_call_id"] = callID
		}
		if name := strings.TrimSpace(asString(m["name"])); name != "" {
			out["name"] = name
		} else if name = strings.TrimSpace(asString(m["tool_name"])); name != "" {
			out["name"] = name
		} else if callID := strings.TrimSpace(asString(out["tool_call_id"])); callID != "" {
			if inferred := strings.TrimSpace(callNameByID[callID]); inferred != "" {
				out["name"] = inferred
			}
		}
		return out
	case "function_call", "tool_call":
		name := strings.TrimSpace(asString(m["name"]))
		var fn map[string]any
		if rawFn, ok := m["function"].(map[string]any); ok {
			fn = rawFn
			if name == "" {
				name = strings.TrimSpace(asString(fn["name"]))
			}
		}
		if name == "" {
			return nil
		}

		var argsRaw any
		if v, ok := m["arguments"]; ok {
			argsRaw = v
		} else if v, ok := m["input"]; ok {
			argsRaw = v
		}
		if argsRaw == nil && fn != nil {
			if v, ok := fn["arguments"]; ok {
				argsRaw = v
			} else if v, ok := fn["input"]; ok {
				argsRaw = v
			}
		}

		functionPayload := map[string]any{
			"name":      name,
			"arguments": prompt.StringifyToolCallArguments(argsRaw),
		}
		call := map[string]any{
			"type":     "function",
			"function": functionPayload,
		}
		if callID := strings.TrimSpace(asString(m["call_id"])); callID != "" {
			call["id"] = callID
		} else if callID = strings.TrimSpace(asString(m["id"])); callID != "" {
			call["id"] = callID
		}
		if callID := strings.TrimSpace(asString(call["id"])); callID != "" && callNameByID != nil {
			callNameByID[callID] = name
		}
		return map[string]any{
			"role":       "assistant",
			"tool_calls": []any{call},
		}
	case "input_text":
		if txt, _ := m["text"].(string); strings.TrimSpace(txt) != "" {
			return map[string]any{
				"role":    "user",
				"content": txt,
			}
		}
	}

	if txt, _ := m["text"].(string); strings.TrimSpace(txt) != "" {
		return map[string]any{
			"role":    "user",
			"content": txt,
		}
	}
	if content, ok := m["content"]; ok {
		if strings.TrimSpace(NormalizeOpenAIContentForPrompt(content)) != "" {
			return map[string]any{
				"role":    "user",
				"content": content,
			}
		}
	}
	return nil
}

func normalizeResponsesAssistantMessage(m map[string]any) map[string]any {
	out := map[string]any{
		"role": "assistant",
	}
	if toolCalls, ok := m["tool_calls"].([]any); ok && len(toolCalls) > 0 {
		out["tool_calls"] = toolCalls
	}
	content := m["content"]
	if content == nil {
		if txt, _ := m["text"].(string); strings.TrimSpace(txt) != "" {
			content = txt
		}
	}
	if content != nil {
		out["content"] = content
	}
	if reasoning := strings.TrimSpace(normalizeOpenAIReasoningContentForPrompt(m["reasoning_content"])); reasoning != "" {
		out["reasoning_content"] = m["reasoning_content"]
	}
	if _, hasToolCalls := out["tool_calls"]; hasToolCalls || out["content"] != nil || out["reasoning_content"] != nil {
		return out
	}
	return nil
}

func normalizeResponsesFallbackPart(m map[string]any) string {
	if m == nil {
		return ""
	}
	if t, _ := m["type"].(string); strings.EqualFold(strings.TrimSpace(t), "input_text") {
		if txt, _ := m["text"].(string); strings.TrimSpace(txt) != "" {
			return txt
		}
	}
	if txt, _ := m["text"].(string); strings.TrimSpace(txt) != "" {
		return txt
	}
	if content, ok := m["content"]; ok {
		if normalized := strings.TrimSpace(NormalizeOpenAIContentForPrompt(content)); normalized != "" {
			return normalized
		}
	}
	return strings.TrimSpace(fmt.Sprintf("%v", m))
}
