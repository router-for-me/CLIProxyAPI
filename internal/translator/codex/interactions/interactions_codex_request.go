package interactions

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertInteractionsRequestToCodex(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)
	out := []byte(`{"model":"","instructions":"","input":[]}`)
	out, _ = sjson.SetBytes(out, "model", modelName)
	if stream || root.Get("stream").Bool() {
		out, _ = sjson.SetBytes(out, "stream", true)
	}
	out = copyInteractionsSystemToCodex(out, root)
	out = copyInteractionsGenerationConfigToCodex(out, root)
	inputItems := translatorcommon.NewRawArrayItems(root.Get("input.#").Int())
	appendInteractionsInputToCodex(&inputItems, root.Get("input"))
	out = translatorcommon.SetRawArrayItems(out, "input", inputItems)
	out = copyInteractionsToolsToCodex(out, root)
	out = copyInteractionsCodexTopLevel(out, root)
	return out
}

func copyInteractionsSystemToCodex(out []byte, root gjson.Result) []byte {
	systemInstruction := root.Get("system_instruction")
	if !systemInstruction.Exists() {
		systemInstruction = root.Get("systemInstruction")
	}
	if !systemInstruction.Exists() {
		return out
	}
	if systemInstruction.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "instructions", systemInstruction.String())
		return out
	}
	if text := systemInstruction.Get("text"); text.Exists() && text.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "instructions", text.String())
		return out
	}
	if parts := systemInstruction.Get("parts"); parts.Exists() && parts.IsArray() {
		var builder strings.Builder
		parts.ForEach(func(_, part gjson.Result) bool {
			text := part.Get("text").String()
			if text == "" {
				return true
			}
			if builder.Len() > 0 {
				builder.WriteByte('\n')
			}
			builder.WriteString(text)
			return true
		})
		if builder.Len() > 0 {
			out, _ = sjson.SetBytes(out, "instructions", builder.String())
		}
	}
	return out
}

func copyInteractionsGenerationConfigToCodex(out []byte, root gjson.Result) []byte {
	cfg := root.Get("generation_config")
	if !cfg.Exists() {
		cfg = root.Get("generationConfig")
	}
	if !cfg.Exists() {
		if reasoning := root.Get("reasoning"); reasoning.Exists() {
			out, _ = sjson.SetRawBytes(out, "reasoning", []byte(reasoning.Raw))
		}
		return out
	}
	if reasoning := cfg.Get("reasoning"); reasoning.Exists() {
		out, _ = sjson.SetRawBytes(out, "reasoning", []byte(reasoning.Raw))
	}
	if effort := interactionsCodexReasoningEffort(cfg); effort != "" {
		out, _ = sjson.SetBytes(out, "reasoning.effort", effort)
	}
	if summary := interactionsCodexReasoningSummary(cfg); summary != "" {
		out, _ = sjson.SetBytes(out, "reasoning.summary", summary)
	}
	copyRawPaths := map[string]string{
		"max_output_tokens":   "max_output_tokens",
		"maxOutputTokens":     "max_output_tokens",
		"max_tokens":          "max_output_tokens",
		"temperature":         "temperature",
		"top_p":               "top_p",
		"topP":                "top_p",
		"presence_penalty":    "presence_penalty",
		"presencePenalty":     "presence_penalty",
		"frequency_penalty":   "frequency_penalty",
		"frequencyPenalty":    "frequency_penalty",
		"parallel_tool_calls": "parallel_tool_calls",
		"parallelToolCalls":   "parallel_tool_calls",
		"response_format":     "response_format",
		"responseFormat":      "response_format",
		"text":                "text",
		"verbosity":           "text.verbosity",
		"truncation":          "truncation",
		"tool_choice":         "tool_choice",
		"toolChoice":          "tool_choice",
		"service_tier":        "service_tier",
		"serviceTier":         "service_tier",
	}
	for sourcePath, targetPath := range copyRawPaths {
		if value := cfg.Get(sourcePath); value.Exists() {
			out, _ = sjson.SetRawBytes(out, targetPath, []byte(value.Raw))
		}
	}
	return out
}

func interactionsCodexReasoningEffort(cfg gjson.Result) string {
	for _, path := range []string{
		"thinking_level",
		"thinkingLevel",
		"thinking_config.thinking_level",
		"thinking_config.thinkingLevel",
		"thinkingConfig.thinking_level",
		"thinkingConfig.thinkingLevel",
		"reasoning.effort",
	} {
		if value := cfg.Get(path); value.Exists() {
			effort := strings.ToLower(strings.TrimSpace(value.String()))
			if effort != "" {
				return effort
			}
		}
	}
	for _, path := range []string{
		"thinking_budget",
		"thinkingBudget",
		"thinking_config.thinking_budget",
		"thinking_config.thinkingBudget",
		"thinkingConfig.thinking_budget",
		"thinkingConfig.thinkingBudget",
	} {
		if value := cfg.Get(path); value.Exists() {
			if effort, ok := thinking.ConvertBudgetToLevel(int(value.Int())); ok {
				return effort
			}
		}
	}
	return ""
}

func interactionsCodexReasoningSummary(cfg gjson.Result) string {
	for _, path := range []string{
		"thinking_summaries",
		"thinkingSummaries",
		"reasoning.summary",
	} {
		if value := cfg.Get(path); value.Exists() {
			switch value.Type {
			case gjson.True:
				return "auto"
			case gjson.False:
				return "none"
			case gjson.String:
				summary := strings.ToLower(strings.TrimSpace(value.String()))
				if summary != "" {
					return summary
				}
			}
		}
	}
	for _, path := range []string{
		"include_thoughts",
		"includeThoughts",
		"thinking_config.include_thoughts",
		"thinking_config.includeThoughts",
		"thinkingConfig.include_thoughts",
		"thinkingConfig.includeThoughts",
	} {
		if value := cfg.Get(path); value.Exists() {
			if value.Bool() {
				return "auto"
			}
			return "none"
		}
	}
	return ""
}

func appendInteractionsInputToCodex(items *[][]byte, input gjson.Result) {
	if !input.Exists() {
		return
	}
	if input.Type == gjson.String {
		appendInteractionsTextToCodex(items, "user", input.String())
		return
	}
	if input.IsArray() {
		input.ForEach(func(_, step gjson.Result) bool {
			appendInteractionsStepToCodex(items, step, "user")
			return true
		})
		return
	}
	if steps := input.Get("steps"); steps.Exists() && steps.IsArray() {
		defaultRole := interactionsCodexDefaultRole(input.Get("role").String(), "user")
		steps.ForEach(func(_, step gjson.Result) bool {
			appendInteractionsStepToCodex(items, step, defaultRole)
			return true
		})
		return
	}
	appendInteractionsStepToCodex(items, input, "user")
}

func appendInteractionsStepToCodex(items *[][]byte, step gjson.Result, defaultRole string) {
	if step.Type == gjson.String {
		appendInteractionsTextToCodex(items, defaultRole, step.String())
		return
	}
	if steps := step.Get("steps"); steps.Exists() && steps.IsArray() {
		role := interactionsCodexDefaultRole(step.Get("role").String(), defaultRole)
		steps.ForEach(func(_, nested gjson.Result) bool {
			appendInteractionsStepToCodex(items, nested, role)
			return true
		})
		return
	}
	stepType := strings.ToLower(strings.TrimSpace(step.Get("type").String()))
	switch stepType {
	case "function_call":
		appendInteractionsFunctionCallToCodex(items, step)
	case "function_result", "function_call_output":
		appendInteractionsFunctionResultToCodex(items, step)
	case "model_output", "assistant":
		appendInteractionsContentToCodexItem(items, step.Get("content"), "assistant")
	case "thought", "reasoning":
		appendInteractionsThoughtToCodex(items, step)
	case "user_input", "message", "":
		role := interactionsCodexDefaultRole(step.Get("role").String(), defaultRole)
		if content := step.Get("content"); content.Exists() {
			appendInteractionsContentToCodexItem(items, content, role)
		} else if text := step.Get("text"); text.Exists() {
			appendInteractionsTextToCodex(items, role, text.String())
		}
	default:
		role := interactionsCodexDefaultRole(step.Get("role").String(), defaultRole)
		if content := step.Get("content"); content.Exists() {
			appendInteractionsContentToCodexItem(items, content, role)
		} else if text := step.Get("text"); text.Exists() {
			appendInteractionsTextToCodex(items, role, text.String())
		}
	}
}

func appendInteractionsContentToCodexItem(items *[][]byte, content gjson.Result, role string) {
	if !content.Exists() {
		return
	}
	if content.Type == gjson.String {
		appendInteractionsTextToCodex(items, role, content.String())
		return
	}
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			if item := interactionsCodexMessagePart(part, role); len(item) > 0 {
				appendInteractionsMessagePartToCodex(items, role, item)
			}
			return true
		})
		return
	}
	if content.IsObject() {
		if item := interactionsCodexMessagePart(content, role); len(item) > 0 {
			appendInteractionsMessagePartToCodex(items, role, item)
		}
	}
}

func appendInteractionsFunctionCallToCodex(items *[][]byte, step gjson.Result) {
	item := []byte(`{"type":"function_call"}`)
	if name := step.Get("name"); name.Exists() {
		item, _ = sjson.SetBytes(item, "name", shortenCodexToolNameIfNeeded(name.String()))
	}
	if callID := interactionsCodexCallID(step); callID != "" {
		item, _ = sjson.SetBytes(item, "call_id", callID)
	}
	if args := step.Get("arguments"); args.Exists() {
		item, _ = sjson.SetBytes(item, "arguments", interactionsCodexJSONString(args))
	} else if args := step.Get("args"); args.Exists() {
		item, _ = sjson.SetBytes(item, "arguments", interactionsCodexJSONString(args))
	}
	*items = append(*items, item)
}

func appendInteractionsFunctionResultToCodex(items *[][]byte, step gjson.Result) {
	item := []byte(`{"type":"function_call_output"}`)
	if callID := interactionsCodexCallID(step); callID != "" {
		item, _ = sjson.SetBytes(item, "call_id", callID)
	}
	if result := step.Get("result"); result.Exists() {
		item, _ = sjson.SetBytes(item, "output", interactionsCodexOutputString(result))
	} else if output := step.Get("output"); output.Exists() {
		item, _ = sjson.SetBytes(item, "output", interactionsCodexOutputString(output))
	}
	*items = append(*items, item)
}

func copyInteractionsToolsToCodex(out []byte, root gjson.Result) []byte {
	tools := root.Get("tools")
	if !tools.Exists() {
		return out
	}
	if !tools.IsArray() {
		out, _ = sjson.SetRawBytes(out, "tools", []byte(tools.Raw))
		return out
	}
	normalized := make([]map[string]any, 0)
	tools.ForEach(func(_, tool gjson.Result) bool {
		if decls := tool.Get("function_declarations"); decls.Exists() {
			appendCodexToolDeclarations(&normalized, decls)
			return true
		}
		if decls := tool.Get("functionDeclarations"); decls.Exists() {
			appendCodexToolDeclarations(&normalized, decls)
			return true
		}
		if name := tool.Get("name"); name.Exists() {
			normalized = append(normalized, codexToolFromDeclaration(tool))
		}
		return true
	})
	if len(normalized) == 0 {
		out, _ = sjson.SetRawBytes(out, "tools", []byte(tools.Raw))
		return out
	}
	raw, errMarshal := json.Marshal(normalized)
	if errMarshal != nil {
		out, _ = sjson.SetRawBytes(out, "tools", []byte(tools.Raw))
		return out
	}
	out, _ = sjson.SetRawBytes(out, "tools", raw)
	if !gjson.GetBytes(out, "tool_choice").Exists() {
		out, _ = sjson.SetBytes(out, "tool_choice", "auto")
	}
	return out
}

func copyInteractionsCodexTopLevel(out []byte, root gjson.Result) []byte {
	if serviceTier := normalizeInteractionsCodexServiceTier(root.Get("service_tier")); serviceTier != "" {
		out, _ = sjson.SetBytes(out, "service_tier", serviceTier)
	}
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(toolChoice.Raw))
	}
	for _, path := range []string{"parallel_tool_calls", "store", "metadata", "include", "truncation"} {
		if value := root.Get(path); value.Exists() {
			out, _ = sjson.SetRawBytes(out, path, []byte(value.Raw))
		}
	}
	return out
}

func appendInteractionsThoughtToCodex(items *[][]byte, step gjson.Result) {
	text := interactionsCodexContentText(step.Get("content"))
	if text == "" {
		text = step.Get("text").String()
	}
	item := []byte(`{"type":"reasoning"}`)
	if text != "" {
		item, _ = sjson.SetBytes(item, "content", text)
	}
	if id := step.Get("id"); id.Exists() {
		item, _ = sjson.SetBytes(item, "id", id.String())
	}
	*items = append(*items, item)
}

func appendInteractionsTextToCodex(items *[][]byte, role, text string) {
	part := []byte(`{"type":"","text":""}`)
	if role == "assistant" {
		part, _ = sjson.SetBytes(part, "type", "output_text")
	} else {
		part, _ = sjson.SetBytes(part, "type", "input_text")
	}
	part, _ = sjson.SetBytes(part, "text", text)
	appendInteractionsMessagePartToCodex(items, role, part)
}

func appendInteractionsMessagePartToCodex(items *[][]byte, role string, part []byte) {
	message := []byte(`{"type":"message","role":"","content":[]}`)
	message, _ = sjson.SetBytes(message, "role", role)
	message, _ = sjson.SetRawBytes(message, "content", translatorcommon.JoinRawArray([][]byte{part}))
	*items = append(*items, message)
}

func interactionsCodexMessagePart(part gjson.Result, role string) []byte {
	if text := part.Get("text"); text.Exists() {
		item := []byte(`{"type":"","text":""}`)
		if role == "assistant" {
			item, _ = sjson.SetBytes(item, "type", "output_text")
		} else {
			item, _ = sjson.SetBytes(item, "type", "input_text")
		}
		item, _ = sjson.SetBytes(item, "text", text.String())
		return item
	}
	partType := strings.ToLower(strings.TrimSpace(part.Get("type").String()))
	switch partType {
	case "text", "":
		return nil
	case "image":
		return interactionsCodexImagePart(part)
	case "image_url":
		item := []byte(`{"type":"input_image","image_url":""}`)
		item, _ = sjson.SetBytes(item, "image_url", part.Get("image_url.url").String())
		return item
	case "audio":
		return interactionsCodexAudioPart(part)
	case "input_audio":
		item := []byte(`{"type":"input_audio","input_audio":{}}`)
		if audio := part.Get("input_audio"); audio.Exists() {
			item, _ = sjson.SetRawBytes(item, "input_audio", []byte(audio.Raw))
		}
		return item
	case "video", "document", "file":
		return interactionsCodexFilePart(part)
	default:
		if inline := part.Get("inline_data"); inline.Exists() {
			return interactionsCodexInlinePart(inline)
		}
		if inline := part.Get("inlineData"); inline.Exists() {
			return interactionsCodexInlinePart(inline)
		}
		if file := part.Get("file_data"); file.Exists() {
			return interactionsCodexFileDataPart(file)
		}
		if file := part.Get("fileData"); file.Exists() {
			return interactionsCodexFileDataPart(file)
		}
	}
	return nil
}

func interactionsCodexImagePart(part gjson.Result) []byte {
	if url := part.Get("url"); url.Exists() {
		item := []byte(`{"type":"input_image","image_url":""}`)
		item, _ = sjson.SetBytes(item, "image_url", url.String())
		return item
	}
	if fileURI := firstString(part, "file_uri", "fileUri"); fileURI != "" {
		item := []byte(`{"type":"input_image","image_url":""}`)
		item, _ = sjson.SetBytes(item, "image_url", fileURI)
		return item
	}
	mimeType := firstString(part, "mime_type", "mimeType")
	data := part.Get("data").String()
	if mimeType == "" || data == "" {
		return nil
	}
	item := []byte(`{"type":"input_image","image_url":""}`)
	item, _ = sjson.SetBytes(item, "image_url", fmt.Sprintf("data:%s;base64,%s", mimeType, data))
	return item
}

func interactionsCodexAudioPart(part gjson.Result) []byte {
	mimeType := firstString(part, "mime_type", "mimeType")
	data := part.Get("data").String()
	if mimeType == "" || data == "" {
		return nil
	}
	item := []byte(`{"type":"input_audio","input_audio":{"data":"","format":""}}`)
	item, _ = sjson.SetBytes(item, "input_audio.data", data)
	item, _ = sjson.SetBytes(item, "input_audio.format", codexInputAudioFormatFromMIME(mimeType))
	return item
}

func interactionsCodexFilePart(part gjson.Result) []byte {
	if fileData := part.Get("file.file_data").String(); fileData != "" {
		item := []byte(`{"type":"input_file","file_data":"","filename":""}`)
		item, _ = sjson.SetBytes(item, "file_data", fileData)
		item, _ = sjson.SetBytes(item, "filename", part.Get("file.filename").String())
		return item
	}
	mimeType := firstString(part, "mime_type", "mimeType")
	if fileURI := firstString(part, "file_uri", "fileUri", "url"); fileURI != "" {
		item := []byte(`{"type":"input_file","file_url":"","filename":""}`)
		item, _ = sjson.SetBytes(item, "file_url", fileURI)
		item, _ = sjson.SetBytes(item, "filename", codexFileNameFromMIME(mimeType))
		return item
	}
	data := part.Get("data").String()
	if mimeType == "" || data == "" {
		return nil
	}
	item := []byte(`{"type":"input_file","file_data":"","filename":""}`)
	item, _ = sjson.SetBytes(item, "file_data", data)
	item, _ = sjson.SetBytes(item, "filename", codexFileNameFromMIME(mimeType))
	return item
}

func interactionsCodexInlinePart(inline gjson.Result) []byte {
	mimeType := firstString(inline, "mime_type", "mimeType")
	data := inline.Get("data").String()
	if mimeType == "" || data == "" {
		return nil
	}
	switch {
	case strings.HasPrefix(strings.ToLower(mimeType), "image/"):
		return interactionsCodexImagePart(gjson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, data)))
	case strings.HasPrefix(strings.ToLower(mimeType), "audio/"):
		return interactionsCodexAudioPart(gjson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, data)))
	default:
		return interactionsCodexFilePart(gjson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, data)))
	}
}

func interactionsCodexFileDataPart(fileData gjson.Result) []byte {
	mimeType := firstString(fileData, "mime_type", "mimeType")
	fileURI := firstString(fileData, "file_uri", "fileUri")
	if fileURI == "" {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		item := []byte(`{"type":"input_image","image_url":""}`)
		item, _ = sjson.SetBytes(item, "image_url", fileURI)
		return item
	}
	item := []byte(`{"type":"input_file","file_url":"","filename":""}`)
	item, _ = sjson.SetBytes(item, "file_url", fileURI)
	item, _ = sjson.SetBytes(item, "filename", codexFileNameFromMIME(mimeType))
	return item
}

func appendCodexToolDeclarations(normalized *[]map[string]any, declarations gjson.Result) {
	if !declarations.IsArray() {
		return
	}
	declarations.ForEach(func(_, declaration gjson.Result) bool {
		if declaration.Get("name").Exists() {
			*normalized = append(*normalized, codexToolFromDeclaration(declaration))
		}
		return true
	})
}

func codexToolFromDeclaration(declaration gjson.Result) map[string]any {
	tool := map[string]any{
		"type":   "function",
		"name":   shortenCodexToolNameIfNeeded(declaration.Get("name").String()),
		"strict": false,
	}
	if desc := declaration.Get("description"); desc.Exists() {
		tool["description"] = desc.String()
	}
	if params := declaration.Get("parameters"); params.Exists() {
		tool["parameters"] = cleanedCodexToolParameters(params)
	} else if params := declaration.Get("parametersJsonSchema"); params.Exists() {
		tool["parameters"] = cleanedCodexToolParameters(params)
	} else if params := declaration.Get("parameters_json_schema"); params.Exists() {
		tool["parameters"] = cleanedCodexToolParameters(params)
	}
	return tool
}

func cleanedCodexToolParameters(params gjson.Result) json.RawMessage {
	cleaned := []byte(params.Raw)
	cleaned, _ = sjson.DeleteBytes(cleaned, "$schema")
	cleaned, _ = sjson.SetBytes(cleaned, "additionalProperties", false)
	return json.RawMessage(cleaned)
}

func interactionsCodexContentText(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsObject() {
		return content.Get("text").String()
	}
	if content.IsArray() {
		var builder strings.Builder
		content.ForEach(func(_, part gjson.Result) bool {
			text := part.Get("text").String()
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

func interactionsCodexCallID(step gjson.Result) string {
	if callID := strings.TrimSpace(step.Get("call_id").String()); callID != "" {
		return callID
	}
	return strings.TrimSpace(step.Get("id").String())
}

func interactionsCodexJSONString(value gjson.Result) string {
	if value.Type == gjson.String {
		return value.String()
	}
	if value.Exists() {
		return value.Raw
	}
	return "{}"
}

func interactionsCodexOutputString(value gjson.Result) string {
	if value.Type == gjson.String {
		return value.String()
	}
	if value.Exists() {
		return value.Raw
	}
	return ""
}

func interactionsCodexDefaultRole(role, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "model", "assistant":
		return "assistant"
	case "developer", "system":
		return "developer"
	case "user":
		return "user"
	}
	if fallback == "assistant" || fallback == "developer" {
		return fallback
	}
	return "user"
}

func normalizeInteractionsCodexServiceTier(serviceTier gjson.Result) string {
	if !serviceTier.Exists() || serviceTier.Type != gjson.String {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(serviceTier.String())) {
	case "priority", "fast":
		return "priority"
	}
	return ""
}

func codexInputAudioFormatFromMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "audio/wav", "audio/wave", "audio/x-wav":
		return "wav"
	case "audio/flac":
		return "flac"
	case "audio/opus", "audio/ogg":
		return "opus"
	case "audio/pcm", "audio/l16":
		return "pcm16"
	default:
		return "mp3"
	}
}

func codexFileNameFromMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "application/pdf":
		return "document.pdf"
	case "text/plain":
		return "document.txt"
	case "text/csv":
		return "document.csv"
	case "application/json":
		return "document.json"
	case "application/xml", "text/xml":
		return "document.xml"
	default:
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(mimeType)), "video/") {
			return "video"
		}
		return "document"
	}
}

func shortenCodexToolNameIfNeeded(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	if strings.HasPrefix(name, "mcp__") {
		idx := strings.LastIndex(name, "__")
		if idx > 0 {
			candidate := "mcp__" + name[idx+2:]
			if len(candidate) > limit {
				return candidate[:limit]
			}
			return candidate
		}
	}
	return name[:limit]
}

func firstString(root gjson.Result, paths ...string) string {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value.String()
		}
	}
	return ""
}
