package chat_completions

import (
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertOpenAIRequestToInteractions(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)
	out := []byte(`{"model":"","input":[]}`)
	out, _ = sjson.SetBytes(out, "model", firstNonEmpty(modelName, root.Get("model").String()))
	if streamValue, ok := openAIRequestStreamValue(root, stream); ok {
		out, _ = sjson.SetBytes(out, "stream", streamValue)
	}
	out = appendOpenAIMessagesToInteractions(out, root.Get("messages"))
	out = copyOpenAIChatGenerationConfigToInteractions(out, root)
	out = appendOpenAIChatToolsToInteractions(out, root.Get("tools"))
	return out
}

func openAIRequestStreamValue(root gjson.Result, stream bool) (bool, bool) {
	if value := root.Get("stream"); value.Exists() {
		return value.Bool(), true
	}
	if stream {
		return true, true
	}
	return false, false
}

func appendOpenAIMessagesToInteractions(out []byte, messages gjson.Result) []byte {
	if !messages.Exists() || !messages.IsArray() {
		return out
	}
	inputItems := translatorcommon.NewRawArrayItems(messages.Get("#").Int())
	var systemBuilder strings.Builder
	messages.ForEach(func(_, message gjson.Result) bool {
		role := strings.ToLower(strings.TrimSpace(message.Get("role").String()))
		switch role {
		case "system", "developer":
			if text := openAIChatContentText(message.Get("content")); text != "" {
				if systemBuilder.Len() > 0 {
					systemBuilder.WriteByte('\n')
				}
				systemBuilder.WriteString(text)
			}
		default:
			appendOpenAIMessageToInteractions(&inputItems, message)
		}
		return true
	})
	if systemBuilder.Len() > 0 {
		out, _ = sjson.SetBytes(out, "system_instruction", systemBuilder.String())
	}
	out = translatorcommon.SetRawArrayItems(out, "input", inputItems)
	return out
}

func appendOpenAIMessageToInteractions(items *[][]byte, message gjson.Result) {
	role := strings.ToLower(strings.TrimSpace(message.Get("role").String()))
	switch role {
	case "assistant":
		if reasoning := message.Get("reasoning_content"); reasoning.Exists() {
			for _, text := range openAIReasoningTexts(reasoning) {
				*items = append(*items, interactionsTextStep("thought", text))
			}
		}
		if step, ok := openAIChatContentStep("model_output", message.Get("content")); ok {
			*items = append(*items, step)
		}
		if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
			toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
				if step, ok := openAIToolCallToInteractionsStep(toolCall); ok {
					*items = append(*items, step)
				}
				return true
			})
		}
	case "tool", "function":
		*items = append(*items, openAIToolResultToInteractions(message))
	default:
		if step, ok := openAIChatContentStep("user_input", message.Get("content")); ok {
			*items = append(*items, step)
		}
	}
}

func openAIChatContentStep(stepType string, content gjson.Result) ([]byte, bool) {
	contentItems := make([][]byte, 0, 4)
	if content.Type == gjson.String {
		if content.String() == "" {
			return nil, false
		}
		part := []byte(`{"type":"text","text":""}`)
		part, _ = sjson.SetBytes(part, "text", content.String())
		contentItems = append(contentItems, part)
	} else {
		appendPart := func(part gjson.Result) {
			if converted, ok := openAIChatContentPartToInteractions(part); ok {
				contentItems = append(contentItems, converted)
			}
		}
		if content.IsArray() {
			content.ForEach(func(_, part gjson.Result) bool {
				appendPart(part)
				return true
			})
		} else if content.IsObject() {
			appendPart(content)
		}
	}
	if len(contentItems) == 0 {
		return nil, false
	}
	step := []byte(`{"type":"","content":[]}`)
	step, _ = sjson.SetBytes(step, "type", stepType)
	step, _ = sjson.SetRawBytes(step, "content", translatorcommon.JoinRawArray(contentItems))
	return step, true
}

func openAIChatContentPartToInteractions(part gjson.Result) ([]byte, bool) {
	partType := strings.ToLower(strings.TrimSpace(part.Get("type").String()))
	if partType == "" && part.Get("text").Exists() {
		partType = "text"
	}
	switch partType {
	case "text", "input_text", "output_text":
		out := []byte(`{"type":"text","text":""}`)
		out, _ = sjson.SetBytes(out, "text", part.Get("text").String())
		return out, true
	case "image_url", "input_image", "image":
		return openAIChatImagePartToInteractions(part), true
	case "input_audio", "audio":
		out := []byte(`{"type":"audio","data":""}`)
		audio := part.Get("input_audio")
		data := firstNonEmpty(audio.Get("data").String(), part.Get("data").String())
		if data == "" {
			return nil, false
		}
		out, _ = sjson.SetBytes(out, "data", data)
		if format := firstNonEmpty(audio.Get("format").String(), part.Get("format").String()); format != "" {
			out, _ = sjson.SetBytes(out, "mime_type", openAIInputAudioMIMEType(format))
		}
		return out, true
	case "file", "input_file", "document":
		file := part.Get("file")
		filename := firstNonEmpty(file.Get("filename").String(), part.Get("filename").String())
		fallbackMIMEType := firstNonEmpty(file.Get("mime_type").String(), file.Get("mimeType").String(), part.Get("mime_type").String(), part.Get("mimeType").String())
		fileData := firstNonEmpty(file.Get("file_data").String(), part.Get("file_data").String(), part.Get("data").String())
		fileURL := firstNonEmpty(file.Get("file_url").String(), part.Get("file_url").String(), part.Get("url").String())
		out := []byte(`{"type":"document"}`)
		if filename != "" {
			out, _ = sjson.SetBytes(out, "filename", filename)
		}
		hasContent := false
		if mimeType, data, ok := translatorcommon.NormalizeOpenAIFileData(filename, fallbackMIMEType, fileData); ok {
			out, _ = sjson.SetBytes(out, "mime_type", mimeType)
			out, _ = sjson.SetBytes(out, "data", data)
			hasContent = true
		}
		if fileURL != "" {
			out, _ = sjson.SetBytes(out, "file_url", fileURL)
			hasContent = true
		}
		return out, hasContent
	}
	return nil, false
}

func openAIChatImagePartToInteractions(part gjson.Result) []byte {
	out := []byte(`{"type":"image"}`)
	imageURL := firstNonEmpty(part.Get("image_url.url").String(), part.Get("image_url").String(), part.Get("url").String())
	if mimeType, data, ok := openAIChatParseDataURL(imageURL); ok {
		out, _ = sjson.SetBytes(out, "mime_type", mimeType)
		out, _ = sjson.SetBytes(out, "data", data)
		return out
	}
	if data := part.Get("data").String(); data != "" {
		out, _ = sjson.SetBytes(out, "data", data)
		if mimeType := part.Get("mime_type").String(); mimeType != "" {
			out, _ = sjson.SetBytes(out, "mime_type", mimeType)
		}
		return out
	}
	if imageURL != "" {
		out, _ = sjson.SetBytes(out, "image_url", imageURL)
	}
	return out
}

func openAIToolResultToInteractions(message gjson.Result) []byte {
	out := []byte(`{"type":"function_result","result":""}`)
	if callID := firstNonEmpty(message.Get("tool_call_id").String(), message.Get("id").String()); callID != "" {
		out, _ = sjson.SetBytes(out, "id", callID)
		out, _ = sjson.SetBytes(out, "call_id", callID)
	}
	if name := message.Get("name").String(); name != "" {
		out, _ = sjson.SetBytes(out, "name", name)
	}
	content := message.Get("content")
	if content.Exists() && content.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "result", content.String())
	} else if content.Exists() {
		out, _ = sjson.SetRawBytes(out, "result", []byte(content.Raw))
	}
	return out
}

func copyOpenAIChatGenerationConfigToInteractions(out []byte, root gjson.Result) []byte {
	copyNumber(&out, "generation_config.max_output_tokens", firstExisting(root.Get("max_completion_tokens"), root.Get("max_tokens")))
	copyNumber(&out, "generation_config.temperature", root.Get("temperature"))
	copyNumber(&out, "generation_config.top_p", root.Get("top_p"))
	copyNumber(&out, "generation_config.presence_penalty", root.Get("presence_penalty"))
	copyNumber(&out, "generation_config.frequency_penalty", root.Get("frequency_penalty"))
	copyNumber(&out, "generation_config.candidate_count", root.Get("n"))
	if stop := root.Get("stop"); stop.Exists() {
		out, _ = sjson.SetRawBytes(out, "generation_config.stop_sequences", []byte(stop.Raw))
	}
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		out, _ = sjson.SetRawBytes(out, "generation_config.tool_choice", []byte(toolChoice.Raw))
	}
	if effort := root.Get("reasoning_effort"); effort.Exists() && effort.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "generation_config.thinking_level", strings.ToLower(strings.TrimSpace(effort.String())))
	}
	if responseFormat := root.Get("response_format"); responseFormat.Exists() {
		out, _ = sjson.SetRawBytes(out, "response_format", []byte(responseFormat.Raw))
	}
	if modalities := root.Get("modalities"); modalities.Exists() {
		out, _ = sjson.SetRawBytes(out, "response_modalities", []byte(modalities.Raw))
	}
	if serviceTier := root.Get("service_tier"); serviceTier.Exists() && serviceTier.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "service_tier", serviceTier.String())
	}
	return out
}

func appendOpenAIChatToolsToInteractions(out []byte, tools gjson.Result) []byte {
	if !tools.Exists() || !tools.IsArray() {
		return out
	}
	var toolItems [][]byte
	tools.ForEach(func(_, tool gjson.Result) bool {
		if converted, ok := openAIChatToolToInteractions(tool); ok {
			toolItems = append(toolItems, converted)
		}
		return true
	})
	if len(toolItems) > 0 {
		out, _ = sjson.SetRawBytes(out, "tools", translatorcommon.JoinRawArray(toolItems))
	}
	return out
}

func openAIChatToolToInteractions(tool gjson.Result) ([]byte, bool) {
	toolType := strings.ToLower(strings.TrimSpace(tool.Get("type").String()))
	if toolType != "" && toolType != "function" {
		return nil, false
	}
	name := firstNonEmpty(tool.Get("function.name").String(), tool.Get("name").String())
	if name == "" {
		return nil, false
	}
	out := []byte(`{"type":"function","name":""}`)
	out, _ = sjson.SetBytes(out, "name", name)
	if desc := firstExisting(tool.Get("function.description"), tool.Get("description")); desc.Exists() {
		out, _ = sjson.SetBytes(out, "description", desc.String())
	}
	if parameters := firstExisting(tool.Get("function.parameters"), tool.Get("parameters")); parameters.Exists() {
		out, _ = sjson.SetRawBytes(out, "parameters", []byte(parameters.Raw))
	}
	return out, true
}

func openAIChatContentText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsObject() {
		return content.Get("text").String()
	}
	if !content.IsArray() {
		return ""
	}
	var builder strings.Builder
	content.ForEach(func(_, part gjson.Result) bool {
		if text := part.Get("text").String(); text != "" {
			builder.WriteString(text)
		}
		return true
	})
	return builder.String()
}

func openAIInputAudioMIMEType(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "wav":
		return "audio/wav"
	case "flac":
		return "audio/flac"
	case "opus":
		return "audio/opus"
	case "pcm16":
		return "audio/pcm"
	default:
		return "audio/mpeg"
	}
}

func openAIChatParseDataURL(value string) (string, string, bool) {
	if !strings.HasPrefix(value, "data:") {
		return "", "", false
	}
	meta, data, ok := strings.Cut(strings.TrimPrefix(value, "data:"), ",")
	if !ok {
		return "", "", false
	}
	mimeType, encoding, _ := strings.Cut(meta, ";")
	if !strings.EqualFold(encoding, "base64") || strings.TrimSpace(mimeType) == "" || data == "" {
		return "", "", false
	}
	return mimeType, data, true
}
