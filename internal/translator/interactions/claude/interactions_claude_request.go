package claude

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertClaudeRequestToInteractions(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)
	out := []byte(`{"model":"","input":[]}`)
	out, _ = sjson.SetBytes(out, "model", firstNonEmpty(modelName, root.Get("model").String()))
	if streamValue, ok := claudeRequestStreamValue(root, stream); ok {
		out, _ = sjson.SetBytes(out, "stream", streamValue)
	}
	out = copyClaudeSystemToInteractions(out, root)
	out = copyClaudeGenerationConfigToInteractions(out, root)
	out = appendClaudeMessagesToInteractions(out, root.Get("messages"))
	out = copyClaudeToolsToInteractions(out, root)
	return out
}

func claudeRequestStreamValue(root gjson.Result, stream bool) (bool, bool) {
	if value := root.Get("stream"); value.Exists() {
		return value.Bool(), true
	}
	if stream {
		return true, true
	}
	return false, false
}

func copyClaudeSystemToInteractions(out []byte, root gjson.Result) []byte {
	text := claudeText(root.Get("system"))
	if text == "" {
		return out
	}
	out, _ = sjson.SetBytes(out, "system_instruction", text)
	return out
}

func copyClaudeGenerationConfigToInteractions(out []byte, root gjson.Result) []byte {
	out = copyClaudeJSONField(out, root, "max_tokens", "generation_config.max_output_tokens")
	out = copyClaudeJSONField(out, root, "temperature", "generation_config.temperature")
	out = copyClaudeJSONField(out, root, "top_p", "generation_config.top_p")
	out = copyClaudeJSONField(out, root, "stop_sequences", "generation_config.stop_sequences")
	out = copyClaudeThinkingToInteractions(out, root)
	return copyClaudeToolChoiceToInteractions(out, root.Get("tool_choice"))
}

func copyClaudeJSONField(out []byte, root gjson.Result, from, to string) []byte {
	value := root.Get(from)
	if !value.Exists() {
		return out
	}
	out, _ = sjson.SetRawBytes(out, to, []byte(value.Raw))
	return out
}

func copyClaudeThinkingToInteractions(out []byte, root gjson.Result) []byte {
	thinking := root.Get("thinking")
	if thinking.Exists() {
		switch strings.ToLower(strings.TrimSpace(thinking.Get("type").String())) {
		case "disabled":
			out, _ = sjson.SetBytes(out, "generation_config.thinking_level", "none")
		case "enabled":
			if budget := thinking.Get("budget_tokens"); budget.Exists() {
				out, _ = sjson.SetRawBytes(out, "generation_config.thinking_config.thinking_budget", []byte(budget.Raw))
			} else {
				out, _ = sjson.SetBytes(out, "generation_config.thinking_level", "high")
			}
		case "adaptive":
			out, _ = sjson.SetBytes(out, "generation_config.thinking_level", "auto")
		}
	}
	if effort := root.Get("output_config.effort"); effort.Exists() && effort.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "generation_config.thinking_level", strings.ToLower(strings.TrimSpace(effort.String())))
	}
	return out
}

func copyClaudeToolChoiceToInteractions(out []byte, toolChoice gjson.Result) []byte {
	if !toolChoice.Exists() {
		return out
	}
	switch toolChoice.Type {
	case gjson.String:
		switch strings.ToLower(strings.TrimSpace(toolChoice.String())) {
		case "auto":
			out, _ = sjson.SetBytes(out, "generation_config.tool_choice", "auto")
		case "any", "required":
			out, _ = sjson.SetBytes(out, "generation_config.tool_choice", "required")
		}
	case gjson.JSON:
		toolType := strings.ToLower(strings.TrimSpace(toolChoice.Get("type").String()))
		switch toolType {
		case "auto":
			out, _ = sjson.SetBytes(out, "generation_config.tool_choice", "auto")
		case "any", "required":
			out, _ = sjson.SetBytes(out, "generation_config.tool_choice", "required")
		case "tool":
			name := strings.TrimSpace(toolChoice.Get("name").String())
			if name != "" {
				choice := []byte(`{"type":"function","name":""}`)
				choice, _ = sjson.SetBytes(choice, "name", name)
				out, _ = sjson.SetRawBytes(out, "generation_config.tool_choice", choice)
			}
		}
	}
	return out
}

func appendClaudeMessagesToInteractions(out []byte, messages gjson.Result) []byte {
	if !messages.Exists() || !messages.IsArray() {
		return out
	}
	messages.ForEach(func(_, message gjson.Result) bool {
		out = appendClaudeMessageToInteractions(out, message)
		return true
	})
	return out
}

func appendClaudeMessageToInteractions(out []byte, message gjson.Result) []byte {
	role := strings.ToLower(strings.TrimSpace(message.Get("role").String()))
	defaultStepType := "user_input"
	if role == "assistant" {
		defaultStepType = "model_output"
	}
	content := message.Get("content")
	if content.Type == gjson.String {
		step := []byte(`{"type":"","content":[{"type":"text","text":""}]}`)
		step, _ = sjson.SetBytes(step, "type", defaultStepType)
		step, _ = sjson.SetBytes(step, "content.0.text", content.String())
		out, _ = sjson.SetRawBytes(out, "input.-1", step)
		return out
	}
	if !content.IsArray() {
		return out
	}
	stepContent := []byte(`[]`)
	flushContent := func() {
		if len(gjson.ParseBytes(stepContent).Array()) == 0 {
			return
		}
		step := []byte(`{"type":"","content":[]}`)
		step, _ = sjson.SetBytes(step, "type", defaultStepType)
		step, _ = sjson.SetRawBytes(step, "content", stepContent)
		out, _ = sjson.SetRawBytes(out, "input.-1", step)
		stepContent = []byte(`[]`)
	}
	content.ForEach(func(_, part gjson.Result) bool {
		partType := strings.ToLower(strings.TrimSpace(part.Get("type").String()))
		switch partType {
		case "text":
			if text := part.Get("text").String(); text != "" {
				contentPart := []byte(`{"type":"text","text":""}`)
				contentPart, _ = sjson.SetBytes(contentPart, "text", text)
				stepContent, _ = sjson.SetRawBytes(stepContent, "-1", contentPart)
			}
		case "thinking":
			flushContent()
			if text := part.Get("thinking").String(); text != "" {
				step := []byte(`{"type":"thought","content":[{"type":"text","text":""}]}`)
				step, _ = sjson.SetBytes(step, "content.0.text", text)
				out, _ = sjson.SetRawBytes(out, "input.-1", step)
			}
		case "image", "document":
			if mediaPart, ok := claudeMediaPartToInteractions(part, partType); ok {
				stepContent, _ = sjson.SetRawBytes(stepContent, "-1", mediaPart)
			}
		case "tool_use":
			flushContent()
			out = appendClaudeToolUseToInteractions(out, part)
		case "tool_result":
			flushContent()
			out = appendClaudeToolResultToInteractions(out, part)
		}
		return true
	})
	flushContent()
	return out
}

func claudeMediaPartToInteractions(part gjson.Result, partType string) ([]byte, bool) {
	source := part.Get("source")
	mimeType := source.Get("media_type").String()
	data := source.Get("data").String()
	if mimeType == "" || data == "" {
		return nil, false
	}
	out := []byte(`{"type":"","mime_type":"","data":""}`)
	out, _ = sjson.SetBytes(out, "type", partType)
	out, _ = sjson.SetBytes(out, "mime_type", mimeType)
	out, _ = sjson.SetBytes(out, "data", data)
	return out, true
}

func appendClaudeToolUseToInteractions(out []byte, part gjson.Result) []byte {
	step := []byte(`{"type":"function_call","name":"","arguments":{}}`)
	step, _ = sjson.SetBytes(step, "name", part.Get("name").String())
	if id := part.Get("id").String(); id != "" {
		step, _ = sjson.SetBytes(step, "id", id)
		step, _ = sjson.SetBytes(step, "call_id", id)
	}
	input := part.Get("input")
	if input.Exists() && input.IsObject() {
		step, _ = sjson.SetRawBytes(step, "arguments", []byte(input.Raw))
	}
	out, _ = sjson.SetRawBytes(out, "input.-1", step)
	return out
}

func appendClaudeToolResultToInteractions(out []byte, part gjson.Result) []byte {
	step := []byte(`{"type":"function_result","call_id":"","result":""}`)
	if id := part.Get("tool_use_id").String(); id != "" {
		step, _ = sjson.SetBytes(step, "id", id)
		step, _ = sjson.SetBytes(step, "call_id", id)
	}
	result := part.Get("content")
	if result.Exists() {
		switch {
		case result.Type == gjson.String:
			step, _ = sjson.SetBytes(step, "result", result.String())
		case result.IsArray():
			converted := []byte(`[]`)
			result.ForEach(func(_, item gjson.Result) bool {
				if item.Get("type").String() == "text" {
					contentPart := []byte(`{"type":"text","text":""}`)
					contentPart, _ = sjson.SetBytes(contentPart, "text", item.Get("text").String())
					converted, _ = sjson.SetRawBytes(converted, "-1", contentPart)
				}
				return true
			})
			step, _ = sjson.SetRawBytes(step, "result", converted)
		default:
			step, _ = sjson.SetRawBytes(step, "result", []byte(result.Raw))
		}
	}
	out, _ = sjson.SetRawBytes(out, "input.-1", step)
	return out
}

func copyClaudeToolsToInteractions(out []byte, root gjson.Result) []byte {
	tools := root.Get("tools")
	if !tools.Exists() || !tools.IsArray() {
		return out
	}
	converted := []byte(`[]`)
	tools.ForEach(func(_, tool gjson.Result) bool {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			return true
		}
		item := []byte(`{"type":"function","name":"","parameters":{}}`)
		item, _ = sjson.SetBytes(item, "name", name)
		if desc := tool.Get("description"); desc.Exists() {
			item, _ = sjson.SetBytes(item, "description", desc.String())
		}
		if schema := tool.Get("input_schema"); schema.Exists() && schema.IsObject() {
			item, _ = sjson.SetRawBytes(item, "parameters", []byte(schema.Raw))
		}
		converted, _ = sjson.SetRawBytes(converted, "-1", item)
		return true
	})
	if len(gjson.ParseBytes(converted).Array()) > 0 {
		out, _ = sjson.SetRawBytes(out, "tools", converted)
	}
	return out
}

func claudeText(value gjson.Result) string {
	if !value.Exists() {
		return ""
	}
	if value.Type == gjson.String {
		return value.String()
	}
	if text := value.Get("text"); text.Exists() {
		return text.String()
	}
	if value.IsArray() {
		var builder strings.Builder
		value.ForEach(func(_, item gjson.Result) bool {
			text := claudeText(item)
			if text == "" {
				return true
			}
			if builder.Len() > 0 {
				builder.WriteByte('\n')
			}
			builder.WriteString(text)
			return true
		})
		return builder.String()
	}
	return ""
}
