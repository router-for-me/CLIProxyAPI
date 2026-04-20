package util

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func NormalizeOpenAIResponsesRequestJSON(input []byte) []byte {
	if len(input) == 0 || !gjson.ValidBytes(input) {
		return input
	}
	root := gjson.ParseBytes(input)
	in := root.Get("input")
	if !in.Exists() || !in.IsArray() {
		return input
	}

	normalized := normalizeResponsesInputArray(in.Array())
	if normalized == "" || normalized == in.Raw {
		return input
	}
	out, err := sjson.SetRawBytes(input, "input", []byte(normalized))
	if err != nil {
		return input
	}
	return out
}

func NormalizeOpenAIChatRequestJSON(input []byte) []byte {
	if len(input) == 0 || !gjson.ValidBytes(input) {
		return input
	}
	root := gjson.ParseBytes(input)
	msgs := root.Get("messages")
	if !msgs.Exists() || !msgs.IsArray() {
		return input
	}

	normalized := normalizeChatMessagesArray(msgs.Array())
	if normalized == "" || normalized == msgs.Raw {
		return input
	}
	out, err := sjson.SetRawBytes(input, "messages", []byte(normalized))
	if err != nil {
		return input
	}
	return out
}

func normalizeResponsesInputArray(items []gjson.Result) string {
	out := []byte(`[]`)
	changed := false

	for _, item := range items {
		itemType := item.Get("type").String()
		itemRole := item.Get("role").String()
		if itemType == "" && itemRole != "" {
			itemType = "message"
		}

		switch itemType {
		case "message":
			msgRaw, extra := normalizeResponsesMessageItem(item)
			if msgRaw != "" {
				out, _ = sjson.SetRawBytes(out, "-1", []byte(msgRaw))
				if msgRaw != item.Raw {
					changed = true
				}
			}
			for _, extraItem := range extra {
				out, _ = sjson.SetRawBytes(out, "-1", []byte(extraItem))
				changed = true
			}
		case "tool_use":
			call := buildResponsesFunctionCall(
				strings.TrimSpace(item.Get("id").String()),
				strings.TrimSpace(item.Get("name").String()),
				jsonValueToString(item.Get("input").Value(), "{}"),
			)
			out, _ = sjson.SetRawBytes(out, "-1", []byte(call))
			changed = true
		case "tool_result":
			result := buildResponsesFunctionCallOutput(
				strings.TrimSpace(item.Get("tool_use_id").String()),
				toolResultValue(item.Get("content")),
			)
			out, _ = sjson.SetRawBytes(out, "-1", []byte(result))
			changed = true
		default:
			out, _ = sjson.SetRawBytes(out, "-1", []byte(item.Raw))
		}
	}

	if !changed {
		return ""
	}
	return string(out)
}

func normalizeResponsesMessageItem(item gjson.Result) (string, []string) {
	msg := []byte(`{}`)
	msg, _ = sjson.SetBytes(msg, "type", "message")
	role := strings.TrimSpace(item.Get("role").String())
	if role == "" {
		role = "user"
	}
	msg, _ = sjson.SetBytes(msg, "role", role)

	content := item.Get("content")
	extra := make([]string, 0)
	reasoning := strings.TrimSpace(item.Get("reasoning_content").String())
	contentAdded := false
	if content.IsArray() {
		for _, part := range content.Array() {
			partType := strings.TrimSpace(part.Get("type").String())
			switch partType {
			case "input_text", "output_text", "input_image", "input_audio", "input_file":
				msg, _ = sjson.SetRawBytes(msg, "content.-1", []byte(part.Raw))
				contentAdded = true
			case "text":
				normalizedType := "input_text"
				if role == "assistant" || role == "model" {
					normalizedType = "output_text"
				}
				textPart := []byte(`{}`)
				textPart, _ = sjson.SetBytes(textPart, "type", normalizedType)
				textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
				msg, _ = sjson.SetRawBytes(msg, "content.-1", textPart)
				contentAdded = true
			case "image":
				if dataURL := claudeImageSourceToDataURL(part.Get("source")); dataURL != "" {
					imagePart := []byte(`{}`)
					imagePart, _ = sjson.SetBytes(imagePart, "type", "input_image")
					imagePart, _ = sjson.SetBytes(imagePart, "image_url", dataURL)
					msg, _ = sjson.SetRawBytes(msg, "content.-1", imagePart)
					contentAdded = true
				}
			case "tool_use":
				callID := strings.TrimSpace(part.Get("id").String())
				name := strings.TrimSpace(part.Get("name").String())
				args := jsonValueToString(part.Get("input").Value(), "{}")
				extra = append(extra, buildResponsesFunctionCall(callID, name, args))
			case "tool_result":
				callID := strings.TrimSpace(part.Get("tool_use_id").String())
				output := toolResultValue(part.Get("content"))
				extra = append(extra, buildResponsesFunctionCallOutput(callID, output))
			case "thinking":
				if reasoning == "" {
					reasoning = strings.TrimSpace(part.Get("thinking").String())
				}
			}
		}
	} else if content.Exists() && content.Type == gjson.String {
		textPart := []byte(`{}`)
		partType := "input_text"
		if role == "assistant" || role == "model" {
			partType = "output_text"
		}
		textPart, _ = sjson.SetBytes(textPart, "type", partType)
		textPart, _ = sjson.SetBytes(textPart, "text", content.String())
		msg, _ = sjson.SetRawBytes(msg, "content.-1", textPart)
		contentAdded = true
	}

	if tc := item.Get("tool_calls"); tc.Exists() && tc.IsArray() {
		for _, call := range tc.Array() {
			if call.Get("type").String() != "function" {
				continue
			}
			callID := strings.TrimSpace(call.Get("id").String())
			name := strings.TrimSpace(call.Get("function.name").String())
			args := call.Get("function.arguments").String()
			extra = append(extra, buildResponsesFunctionCall(callID, name, args))
		}
	}

	if reasoning != "" {
		msg, _ = sjson.SetBytes(msg, "reasoning_content", reasoning)
	}
	if !contentAdded {
		msg, _ = sjson.SetRawBytes(msg, "content", []byte(`[]`))
	}
	return string(msg), extra
}

func normalizeChatMessagesArray(messages []gjson.Result) string {
	out := []byte(`[]`)
	changed := false

	for _, message := range messages {
		before, msg, after := normalizeChatMessage(message)
		for _, extraMsg := range before {
			out, _ = sjson.SetRawBytes(out, "-1", []byte(extraMsg))
			changed = true
		}
		if msg != "" {
			out, _ = sjson.SetRawBytes(out, "-1", []byte(msg))
			if msg != message.Raw {
				changed = true
			}
		}
		for _, extraMsg := range after {
			out, _ = sjson.SetRawBytes(out, "-1", []byte(extraMsg))
			changed = true
		}
	}

	if !changed {
		return ""
	}
	return string(out)
}

func normalizeChatMessage(message gjson.Result) ([]string, string, []string) {
	msg := []byte(message.Raw)
	role := strings.TrimSpace(message.Get("role").String())
	content := message.Get("content")
	if !content.IsArray() {
		return nil, string(msg), nil
	}

	normalizedContent := []byte(`[]`)
	before := make([]string, 0)
	contentChanged := false
	reasoning := strings.TrimSpace(message.Get("reasoning_content").String())
	toolCalls := message.Get("tool_calls").Raw
	hasToolCalls := message.Get("tool_calls").IsArray()
	hasContentParts := false

	for _, part := range content.Array() {
		partType := strings.TrimSpace(part.Get("type").String())
		switch partType {
		case "text", "image_url", "file":
			normalizedContent, _ = sjson.SetRawBytes(normalizedContent, "-1", []byte(part.Raw))
			hasContentParts = true
		case "input_text", "output_text":
			textPart := []byte(`{"type":"text","text":""}`)
			textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
			normalizedContent, _ = sjson.SetRawBytes(normalizedContent, "-1", textPart)
			contentChanged = true
			hasContentParts = true
		case "tool_use":
			call := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
			call, _ = sjson.SetBytes(call, "id", part.Get("id").String())
			call, _ = sjson.SetBytes(call, "function.name", part.Get("name").String())
			call, _ = sjson.SetBytes(call, "function.arguments", jsonValueToString(part.Get("input").Value(), "{}"))
			if !hasToolCalls {
				toolCalls = `[]`
				hasToolCalls = true
			}
			toolCallsBytes, _ := sjson.SetRawBytes([]byte(toolCalls), "-1", call)
			toolCalls = string(toolCallsBytes)
			contentChanged = true
		case "tool_result":
			toolMsg := []byte(`{"role":"tool","tool_call_id":"","content":""}`)
			toolMsg, _ = sjson.SetBytes(toolMsg, "tool_call_id", part.Get("tool_use_id").String())
			toolMsg, _ = sjson.SetBytes(toolMsg, "content", toolResultValue(part.Get("content")))
			before = append(before, string(toolMsg))
			contentChanged = true
		case "thinking":
			if role == "assistant" && reasoning == "" {
				reasoning = strings.TrimSpace(part.Get("thinking").String())
			}
			contentChanged = true
		default:
			normalizedContent, _ = sjson.SetRawBytes(normalizedContent, "-1", []byte(part.Raw))
			hasContentParts = true
		}
	}

	if !contentChanged {
		return nil, string(msg), nil
	}
	if hasContentParts {
		msg, _ = sjson.SetRawBytes(msg, "content", normalizedContent)
	} else if role == "assistant" && hasToolCalls {
		// OpenAI-compatible backends often expect assistant tool-call messages
		// to keep an explicit empty content field instead of an empty array.
		msg, _ = sjson.SetBytes(msg, "content", "")
	} else {
		msg = nil
	}
	if hasToolCalls {
		msg, _ = sjson.SetRawBytes(msg, "tool_calls", []byte(toolCalls))
	}
	if reasoning != "" {
		msg, _ = sjson.SetBytes(msg, "reasoning_content", reasoning)
	}
	return before, string(msg), nil
}

func buildResponsesFunctionCall(callID, name, args string) string {
	item := []byte(`{"type":"function_call","call_id":"","name":"","arguments":"{}"}`)
	item, _ = sjson.SetBytes(item, "call_id", callID)
	item, _ = sjson.SetBytes(item, "name", name)
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	item, _ = sjson.SetBytes(item, "arguments", args)
	return string(item)
}

func buildResponsesFunctionCallOutput(callID, output string) string {
	if strings.TrimSpace(output) == "" {
		output = "(empty)"
	}
	item := []byte(`{"type":"function_call_output","call_id":"","output":""}`)
	item, _ = sjson.SetBytes(item, "call_id", callID)
	item, _ = sjson.SetBytes(item, "output", output)
	return string(item)
}

func jsonValueToString(value any, fallback string) string {
	if value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return fallback
		}
		return typed
	default:
		raw, err := json.Marshal(value)
		if err != nil || len(raw) == 0 {
			return fallback
		}
		return string(raw)
	}
}

func toolResultValue(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		parts := make([]string, 0, len(content.Array()))
		for _, item := range content.Array() {
			switch item.Get("type").String() {
			case "text":
				if text := strings.TrimSpace(item.Get("text").String()); text != "" {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return content.Raw
}

func claudeImageSourceToDataURL(source gjson.Result) string {
	if !source.Exists() {
		return ""
	}
	switch source.Get("type").String() {
	case "base64":
		mediaType := strings.TrimSpace(source.Get("media_type").String())
		data := strings.TrimSpace(source.Get("data").String())
		if mediaType == "" || data == "" {
			return ""
		}
		return "data:" + mediaType + ";base64," + data
	case "url":
		return strings.TrimSpace(source.Get("url").String())
	default:
		return ""
	}
}
