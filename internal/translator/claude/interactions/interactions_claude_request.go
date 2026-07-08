package interactions

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertInteractionsRequestToClaude(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)
	out := []byte(`{"model":"","max_tokens":32000,"messages":[]}`)
	out, _ = sjson.SetBytes(out, "model", modelName)
	if stream || root.Get("stream").Bool() {
		out, _ = sjson.SetBytes(out, "stream", true)
	}
	out = copyInteractionsSystemToClaude(out, root)
	out = copyInteractionsGenerationConfigToClaude(out, root)
	out = appendInteractionsInputToClaudeMessages(out, root.Get("input"))
	out = copyInteractionsToolsToClaude(out, root)
	return out
}

func copyInteractionsSystemToClaude(out []byte, root gjson.Result) []byte {
	sys := root.Get("system_instruction")
	if !sys.Exists() {
		sys = root.Get("systemInstruction")
	}
	text := interactionsClaudeText(sys)
	if text == "" {
		return out
	}
	out, _ = sjson.SetBytes(out, "system", text)
	return out
}

func copyInteractionsGenerationConfigToClaude(out []byte, root gjson.Result) []byte {
	cfg := root.Get("generation_config")
	if !cfg.Exists() {
		cfg = root.Get("generationConfig")
	}
	if cfg.Exists() {
		out = copyJSONField(out, cfg, "max_output_tokens", "max_tokens")
		out = copyJSONField(out, cfg, "maxOutputTokens", "max_tokens")
		out = copyJSONField(out, cfg, "top_p", "top_p")
		out = copyJSONField(out, cfg, "topP", "top_p")
		out = copyJSONField(out, cfg, "temperature", "temperature")
		out = copyJSONField(out, cfg, "stop_sequences", "stop_sequences")
		out = copyJSONField(out, cfg, "stopSequences", "stop_sequences")
		out = copyInteractionsThinkingConfigToClaude(out, cfg)
		out = copyInteractionsToolChoiceToClaude(out, cfg.Get("tool_choice"))
		out = copyInteractionsToolChoiceToClaude(out, cfg.Get("toolChoice"))
	}
	out = copyInteractionsReasoningToClaude(out, root.Get("reasoning"))
	out = copyInteractionsToolChoiceToClaude(out, root.Get("tool_choice"))
	out = copyInteractionsToolChoiceToClaude(out, root.Get("toolChoice"))
	return out
}

func copyJSONField(out []byte, root gjson.Result, from, to string) []byte {
	value := root.Get(from)
	if !value.Exists() {
		return out
	}
	out, _ = sjson.SetRawBytes(out, to, []byte(value.Raw))
	return out
}

func copyInteractionsThinkingConfigToClaude(out []byte, cfg gjson.Result) []byte {
	level := firstClaudeInteractionsExisting(cfg, "thinking_level", "thinkingLevel", "reasoning.effort")
	if !level.Exists() {
		return out
	}
	return setClaudeThinkingFromLevel(out, level.String())
}

func copyInteractionsReasoningToClaude(out []byte, reasoning gjson.Result) []byte {
	if !reasoning.Exists() {
		return out
	}
	if effort := reasoning.Get("effort"); effort.Exists() {
		return setClaudeThinkingFromLevel(out, effort.String())
	}
	if level := reasoning.Get("thinking_level"); level.Exists() {
		return setClaudeThinkingFromLevel(out, level.String())
	}
	return out
}

func setClaudeThinkingFromLevel(out []byte, level string) []byte {
	normalized := strings.ToLower(strings.TrimSpace(level))
	if normalized == "" {
		return out
	}
	switch normalized {
	case "none", "disabled", "off", "false":
		out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
		out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
		return out
	case "auto", "adaptive":
		out, _ = sjson.SetBytes(out, "thinking.type", "adaptive")
		out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
		return out
	}
	if budget, ok := thinking.ConvertLevelToBudget(normalized); ok {
		switch {
		case budget == 0:
			out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
		case budget < 0:
			out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
		default:
			out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
			out, _ = sjson.SetBytes(out, "thinking.budget_tokens", budget)
		}
		return out
	}
	out, _ = sjson.SetBytes(out, "thinking.type", "adaptive")
	out, _ = sjson.SetBytes(out, "output_config.effort", normalized)
	return out
}

func appendInteractionsInputToClaudeMessages(out []byte, input gjson.Result) []byte {
	if !input.Exists() {
		return out
	}
	if input.Type == gjson.String {
		step := []byte(`{"type":"user_input","content":[{"type":"text","text":""}]}`)
		step, _ = sjson.SetBytes(step, "content.0.text", input.String())
		return appendInteractionsStepToClaude(out, gjson.ParseBytes(step), "user")
	}
	if input.IsObject() {
		return appendInteractionsInputItemToClaude(out, input)
	}
	input.ForEach(func(_, step gjson.Result) bool {
		out = appendInteractionsInputItemToClaude(out, step)
		return true
	})
	return out
}

func appendInteractionsInputItemToClaude(out []byte, step gjson.Result) []byte {
	if step.Get("steps").IsArray() {
		defaultRole := "user"
		if role := step.Get("role").String(); role == "model" || role == "assistant" {
			defaultRole = "assistant"
		}
		step.Get("steps").ForEach(func(_, nestedStep gjson.Result) bool {
			out = appendInteractionsStepToClaude(out, nestedStep, defaultRole)
			return true
		})
		return out
	}
	if step.Get("parts").Exists() {
		wrapped := []byte(`{"type":"user_input","content":[]}`)
		if role := step.Get("role").String(); role == "model" || role == "assistant" {
			wrapped, _ = sjson.SetBytes(wrapped, "type", "model_output")
		}
		wrapped, _ = sjson.SetRawBytes(wrapped, "content", []byte(step.Get("parts").Raw))
		return appendInteractionsStepToClaude(out, gjson.ParseBytes(wrapped), "user")
	}
	stepType := step.Get("type").String()
	switch stepType {
	case "function_call":
		return appendInteractionsFunctionCallToClaude(out, step)
	case "function_result":
		return appendInteractionsFunctionResultToClaude(out, step)
	case "model_output", "thought":
		return appendInteractionsStepToClaude(out, step, "assistant")
	default:
		return appendInteractionsStepToClaude(out, step, "user")
	}
}

func appendInteractionsStepToClaude(out []byte, step gjson.Result, defaultRole string) []byte {
	role := defaultRole
	if stepRole := step.Get("role").String(); stepRole == "user" || stepRole == "assistant" {
		role = stepRole
	}
	content := []byte(`[]`)
	stepContent := step.Get("content")
	if stepContent.Type == gjson.String {
		part := []byte(`{"type":"text","text":""}`)
		part, _ = sjson.SetBytes(part, "text", stepContent.String())
		content, _ = sjson.SetRawBytes(content, "-1", part)
	} else if stepContent.IsArray() {
		stepContent.ForEach(func(_, part gjson.Result) bool {
			content = appendInteractionsContentToClaude(content, part, role)
			return true
		})
	} else if text := step.Get("text"); text.Exists() {
		part := []byte(`{"type":"text","text":""}`)
		part, _ = sjson.SetBytes(part, "text", text.String())
		content, _ = sjson.SetRawBytes(content, "-1", part)
	}
	if len(gjson.ParseBytes(content).Array()) == 0 {
		return out
	}
	msg := []byte(`{"role":"","content":[]}`)
	msg, _ = sjson.SetBytes(msg, "role", role)
	msg, _ = sjson.SetRawBytes(msg, "content", content)
	out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
	return out
}

func appendInteractionsContentToClaude(content []byte, part gjson.Result, role string) []byte {
	partType := part.Get("type").String()
	if partType == "" && part.Get("text").Exists() {
		partType = "text"
	}
	switch partType {
	case "text":
		textPart := []byte(`{"type":"text","text":""}`)
		textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
		content, _ = sjson.SetRawBytes(content, "-1", textPart)
	case "thinking", "reasoning":
		if role != "assistant" {
			return content
		}
		thinkingPart := []byte(`{"type":"thinking","thinking":""}`)
		thinkingPart, _ = sjson.SetBytes(thinkingPart, "thinking", interactionsClaudeText(part))
		content, _ = sjson.SetRawBytes(content, "-1", thinkingPart)
	case "image":
		if imagePart, ok := interactionsClaudeMediaPart(part, "image"); ok {
			content, _ = sjson.SetRawBytes(content, "-1", imagePart)
		}
	case "document", "file":
		if documentPart, ok := interactionsClaudeMediaPart(part, "document"); ok {
			content, _ = sjson.SetRawBytes(content, "-1", documentPart)
		}
	default:
		if text := interactionsClaudeText(part); text != "" {
			textPart := []byte(`{"type":"text","text":""}`)
			textPart, _ = sjson.SetBytes(textPart, "text", text)
			content, _ = sjson.SetRawBytes(content, "-1", textPart)
		} else if part.Get("data").String() != "" || part.Get("file_data").String() != "" {
			textPart := []byte(`{"type":"text","text":""}`)
			textPart, _ = sjson.SetBytes(textPart, "text", fmt.Sprintf("[%s content omitted]", partType))
			content, _ = sjson.SetRawBytes(content, "-1", textPart)
		}
	}
	return content
}

func appendInteractionsFunctionCallToClaude(out []byte, step gjson.Result) []byte {
	toolUse := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
	toolUse, _ = sjson.SetBytes(toolUse, "id", interactionsClaudeToolID(step))
	toolUse, _ = sjson.SetBytes(toolUse, "name", step.Get("name").String())
	args := step.Get("arguments")
	if !args.Exists() {
		args = step.Get("args")
	}
	if args.Exists() && args.IsObject() {
		toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(args.Raw))
	}
	msg := []byte(`{"role":"assistant","content":[]}`)
	msg, _ = sjson.SetRawBytes(msg, "content.-1", toolUse)
	out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
	return out
}

func appendInteractionsFunctionResultToClaude(out []byte, step gjson.Result) []byte {
	toolResult := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)
	toolResult, _ = sjson.SetBytes(toolResult, "tool_use_id", interactionsClaudeToolID(step))
	result := step.Get("result")
	if !result.Exists() {
		result = step.Get("output")
	}
	switch {
	case result.IsArray():
		content := []byte(`[]`)
		result.ForEach(func(_, part gjson.Result) bool {
			content = appendInteractionsContentToClaude(content, part, "user")
			return true
		})
		toolResult, _ = sjson.SetRawBytes(toolResult, "content", content)
	case result.Exists() && result.Raw != "":
		toolResult, _ = sjson.SetBytes(toolResult, "content", result.Raw)
	default:
		toolResult, _ = sjson.SetBytes(toolResult, "content", "")
	}
	msg := []byte(`{"role":"user","content":[]}`)
	msg, _ = sjson.SetRawBytes(msg, "content.-1", toolResult)
	out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
	return out
}

func copyInteractionsToolsToClaude(out []byte, root gjson.Result) []byte {
	tools := root.Get("tools")
	if !tools.Exists() || !tools.IsArray() {
		return out
	}
	claudeTools := []byte(`[]`)
	tools.ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("function_declarations").IsArray() {
			tool.Get("function_declarations").ForEach(func(_, decl gjson.Result) bool {
				claudeTools = appendInteractionsClaudeTool(claudeTools, decl)
				return true
			})
			return true
		}
		if tool.Get("functionDeclarations").IsArray() {
			tool.Get("functionDeclarations").ForEach(func(_, decl gjson.Result) bool {
				claudeTools = appendInteractionsClaudeTool(claudeTools, decl)
				return true
			})
			return true
		}
		claudeTools = appendInteractionsClaudeTool(claudeTools, tool)
		return true
	})
	if len(gjson.ParseBytes(claudeTools).Array()) > 0 {
		out, _ = sjson.SetRawBytes(out, "tools", claudeTools)
	}
	return out
}

func appendInteractionsClaudeTool(tools []byte, tool gjson.Result) []byte {
	name := tool.Get("name").String()
	if name == "" {
		name = tool.Get("function.name").String()
	}
	if name == "" {
		return tools
	}
	converted := []byte(`{"name":"","input_schema":{}}`)
	converted, _ = sjson.SetBytes(converted, "name", name)
	if desc := tool.Get("description"); desc.Exists() {
		converted, _ = sjson.SetBytes(converted, "description", desc.String())
	} else if desc := tool.Get("function.description"); desc.Exists() {
		converted, _ = sjson.SetBytes(converted, "description", desc.String())
	}
	params := firstClaudeInteractionsExisting(tool, "parameters", "parametersJsonSchema", "parameters_json_schema", "input_schema")
	if params.Exists() && params.IsObject() {
		converted, _ = sjson.SetRawBytes(converted, "input_schema", []byte(params.Raw))
	}
	tools, _ = sjson.SetRawBytes(tools, "-1", converted)
	return tools
}

func copyInteractionsToolChoiceToClaude(out []byte, toolChoice gjson.Result) []byte {
	if !toolChoice.Exists() {
		return out
	}
	switch toolChoice.Type {
	case gjson.String:
		switch strings.ToLower(strings.TrimSpace(toolChoice.String())) {
		case "auto":
			out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"auto"}`))
		case "required", "any":
			out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"any"}`))
		}
	case gjson.JSON:
		toolType := strings.ToLower(strings.TrimSpace(toolChoice.Get("type").String()))
		switch toolType {
		case "auto":
			out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"auto"}`))
		case "required", "any":
			out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"any"}`))
		case "function", "tool":
			name := toolChoice.Get("name").String()
			if name == "" {
				name = toolChoice.Get("function.name").String()
			}
			if name != "" {
				choice := []byte(`{"type":"tool","name":""}`)
				choice, _ = sjson.SetBytes(choice, "name", name)
				out, _ = sjson.SetRawBytes(out, "tool_choice", choice)
			}
		}
	}
	return out
}

func interactionsClaudeToolID(step gjson.Result) string {
	for _, path := range []string{"call_id", "id", "tool_use_id"} {
		if value := step.Get(path).String(); value != "" {
			return util.SanitizeClaudeToolID(value)
		}
	}
	if name := step.Get("name").String(); name != "" {
		return util.SanitizeClaudeToolID("toolu_" + name)
	}
	return "toolu_interactions"
}

func interactionsClaudeText(value gjson.Result) string {
	if !value.Exists() {
		return ""
	}
	if value.Type == gjson.String {
		return value.String()
	}
	if text := value.Get("text"); text.Exists() {
		return text.String()
	}
	if thinking := value.Get("thinking"); thinking.Exists() {
		return thinking.String()
	}
	if content := value.Get("content"); content.Exists() {
		return interactionsClaudeText(content)
	}
	if parts := value.Get("parts"); parts.Exists() && parts.IsArray() {
		var builder strings.Builder
		parts.ForEach(func(_, part gjson.Result) bool {
			text := interactionsClaudeText(part)
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

func interactionsClaudeMediaPart(part gjson.Result, claudeType string) ([]byte, bool) {
	mimeType := firstClaudeInteractionsExisting(part, "mime_type", "mimeType", "media_type", "mediaType").String()
	data := firstClaudeInteractionsExisting(part, "data", "file_data", "fileData").String()
	if source := part.Get("source"); source.Exists() {
		if mimeType == "" {
			mimeType = source.Get("media_type").String()
		}
		if data == "" {
			data = source.Get("data").String()
		}
	}
	if mimeType == "" || data == "" {
		return nil, false
	}
	out := []byte(`{"type":"","source":{"type":"base64","media_type":"","data":""}}`)
	out, _ = sjson.SetBytes(out, "type", claudeType)
	out, _ = sjson.SetBytes(out, "source.media_type", mimeType)
	out, _ = sjson.SetBytes(out, "source.data", data)
	return out, true
}

func firstClaudeInteractionsExisting(root gjson.Result, paths ...string) gjson.Result {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}
