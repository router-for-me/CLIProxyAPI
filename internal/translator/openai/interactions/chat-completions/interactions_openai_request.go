package chat_completions

import (
	"fmt"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertInteractionsRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)
	out := []byte(`{"model":"","messages":[]}`)
	out, _ = sjson.SetBytes(out, "model", firstNonEmpty(modelName, root.Get("model").String()))
	if stream || root.Get("stream").Bool() {
		out, _ = sjson.SetBytes(out, "stream", true)
	}
	messageCapacity := root.Get("input.#").Int()
	if interactionsText(root.Get("system_instruction")) != "" {
		messageCapacity++
	}
	messageItems := translatorcommon.NewRawArrayItems(messageCapacity)
	appendInteractionsSystemToOpenAI(&messageItems, root)
	appendInteractionsInputToOpenAIMessages(&messageItems, root.Get("input"))
	out = translatorcommon.SetRawArrayItems(out, "messages", messageItems)
	out = copyInteractionsToolsToOpenAI(out, root)
	out = copyInteractionsGenerationConfigToOpenAI(out, root)
	out = copyInteractionsOpenAITopLevel(out, root)
	return out
}

func appendInteractionsSystemToOpenAI(items *[][]byte, root gjson.Result) {
	text := interactionsText(root.Get("system_instruction"))
	if text == "" {
		return
	}
	msg := []byte(`{"role":"system","content":""}`)
	msg, _ = sjson.SetBytes(msg, "content", text)
	*items = append(*items, msg)
}

func appendInteractionsInputToOpenAIMessages(items *[][]byte, input gjson.Result) {
	if input.Type == gjson.String {
		msg := []byte(`{"role":"user","content":""}`)
		msg, _ = sjson.SetBytes(msg, "content", input.String())
		*items = append(*items, msg)
		return
	}
	if input.IsArray() {
		input.ForEach(func(_, step gjson.Result) bool {
			appendInteractionsStepToOpenAI(items, step, "user")
			return true
		})
		return
	}
	if input.IsObject() {
		appendInteractionsStepToOpenAI(items, input, "user")
	}
}

func appendInteractionsStepToOpenAI(items *[][]byte, step gjson.Result, defaultRole string) {
	switch step.Get("type").String() {
	case "user_input":
		appendInteractionsMessageToOpenAI(items, step, "user")
	case "model_output":
		appendInteractionsMessageToOpenAI(items, step, "assistant")
	case "thought":
		appendInteractionsThoughtToOpenAI(items, step)
	case "function_call":
		appendInteractionsFunctionCallToOpenAI(items, step)
	case "function_result":
		appendInteractionsFunctionResultToOpenAI(items, step)
	default:
		if step.Type == gjson.String {
			msg := []byte(`{"role":"","content":""}`)
			msg, _ = sjson.SetBytes(msg, "role", defaultRole)
			msg, _ = sjson.SetBytes(msg, "content", step.String())
			*items = append(*items, msg)
		}
	}
}

func appendInteractionsMessageToOpenAI(items *[][]byte, step gjson.Result, role string) {
	msg := []byte(`{"role":"","content":""}`)
	msg, _ = sjson.SetBytes(msg, "role", role)
	content := step.Get("content")
	if content.Type == gjson.String {
		msg, _ = sjson.SetBytes(msg, "content", content.String())
	} else {
		msg = appendInteractionsContentToOpenAIMessage(msg, content, role)
	}
	*items = append(*items, msg)
}

func appendInteractionsThoughtToOpenAI(items *[][]byte, step gjson.Result) {
	msg := []byte(`{"role":"assistant","content":"","reasoning_content":""}`)
	msg, _ = sjson.SetBytes(msg, "reasoning_content", interactionsText(step.Get("content")))
	*items = append(*items, msg)
}

func appendInteractionsContentToOpenAIMessage(msg []byte, content gjson.Result, role string) []byte {
	if !content.Exists() {
		return msg
	}
	if content.Type == gjson.String {
		msg, _ = sjson.SetBytes(msg, "content", content.String())
		return msg
	}
	contentItems := make([][]byte, 0, 4)
	textOnly := true
	var textBuilder strings.Builder
	appendPart := func(part gjson.Result) {
		converted, ok := interactionsContentPartToOpenAI(part, role)
		if !ok {
			return
		}
		if gjson.GetBytes(converted, "type").String() == "text" {
			textBuilder.WriteString(gjson.GetBytes(converted, "text").String())
		} else {
			textOnly = false
		}
		contentItems = append(contentItems, converted)
	}
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			appendPart(part)
			return true
		})
	} else if content.IsObject() {
		appendPart(content)
	}
	if len(contentItems) > 0 {
		if textOnly {
			msg, _ = sjson.SetBytes(msg, "content", textBuilder.String())
		} else {
			msg, _ = sjson.SetRawBytes(msg, "content", translatorcommon.JoinRawArray(contentItems))
		}
	}
	return msg
}

func appendInteractionsFunctionCallToOpenAI(items *[][]byte, step gjson.Result) {
	msg := []byte(`{"role":"assistant","content":"","tool_calls":[]}`)
	toolCall := []byte(`{"id":"","type":"function","function":{"name":"","arguments":"{}"}}`)
	callID := firstNonEmpty(step.Get("call_id").String(), step.Get("id").String(), "call_0")
	toolCall, _ = sjson.SetBytes(toolCall, "id", callID)
	toolCall, _ = sjson.SetBytes(toolCall, "function.name", step.Get("name").String())
	toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", jsonStringValue(step.Get("arguments"), "{}"))
	msg = translatorcommon.SetRawArrayItems(msg, "tool_calls", [][]byte{toolCall})
	*items = append(*items, msg)
}

func appendInteractionsFunctionResultToOpenAI(items *[][]byte, step gjson.Result) {
	msg := []byte(`{"role":"tool","tool_call_id":"","content":""}`)
	msg, _ = sjson.SetBytes(msg, "tool_call_id", firstNonEmpty(step.Get("call_id").String(), step.Get("id").String()))
	msg, _ = sjson.SetBytes(msg, "content", jsonStringValue(firstExisting(step.Get("result"), step.Get("output")), ""))
	*items = append(*items, msg)
}

func copyInteractionsToolsToOpenAI(out []byte, root gjson.Result) []byte {
	tools := root.Get("tools")
	if !tools.Exists() || !tools.IsArray() {
		return out
	}
	var toolItems [][]byte
	tools.ForEach(func(_, tool gjson.Result) bool {
		if converted, ok := openAIToolFromInteractionsTool(tool); ok {
			toolItems = append(toolItems, converted)
		}
		if decls := firstExisting(tool.Get("function_declarations"), tool.Get("functionDeclarations")); decls.Exists() && decls.IsArray() {
			decls.ForEach(func(_, decl gjson.Result) bool {
				if converted, ok := openAIToolFromInteractionsTool(decl); ok {
					toolItems = append(toolItems, converted)
				}
				return true
			})
		}
		return true
	})
	if len(toolItems) > 0 {
		out, _ = sjson.SetRawBytes(out, "tools", translatorcommon.JoinRawArray(toolItems))
	}
	return out
}

func copyInteractionsGenerationConfigToOpenAI(out []byte, root gjson.Result) []byte {
	gen := root.Get("generation_config")
	if !gen.Exists() {
		gen = root.Get("generationConfig")
	}
	copyNumber(&out, "temperature", firstExisting(gen.Get("temperature"), root.Get("temperature")))
	copyNumber(&out, "max_tokens", firstExisting(gen.Get("max_output_tokens"), gen.Get("maxOutputTokens"), root.Get("max_tokens"), root.Get("max_completion_tokens")))
	copyNumber(&out, "top_p", firstExisting(gen.Get("top_p"), gen.Get("topP"), root.Get("top_p")))
	copyNumber(&out, "top_k", firstExisting(gen.Get("top_k"), gen.Get("topK")))
	copyNumber(&out, "n", firstExisting(gen.Get("candidate_count"), gen.Get("candidateCount"), root.Get("n")))
	if stop := firstExisting(gen.Get("stop_sequences"), gen.Get("stopSequences"), root.Get("stop")); stop.Exists() {
		out, _ = sjson.SetRawBytes(out, "stop", []byte(stop.Raw))
	}
	if toolChoice := firstExisting(gen.Get("tool_choice"), root.Get("tool_choice")); toolChoice.Exists() {
		out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(toolChoice.Raw))
	}
	if effort := interactionsReasoningEffort(root, gen); effort != "" {
		out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
	}
	if responseModalities := root.Get("response_modalities"); responseModalities.Exists() {
		out, _ = sjson.SetRawBytes(out, "modalities", []byte(responseModalities.Raw))
	}
	return out
}

func copyInteractionsOpenAITopLevel(out []byte, root gjson.Result) []byte {
	if format := root.Get("response_format"); format.Exists() {
		out, _ = sjson.SetRawBytes(out, "response_format", []byte(format.Raw))
	}
	if serviceTier := root.Get("service_tier"); serviceTier.Exists() && serviceTier.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "service_tier", serviceTier.String())
	}
	for _, key := range []string{"parallel_tool_calls", "seed", "user"} {
		if value := root.Get(key); value.Exists() {
			out, _ = sjson.SetRawBytes(out, key, []byte(value.Raw))
		}
	}
	return out
}

func interactionsContentPartToOpenAI(part gjson.Result, role string) ([]byte, bool) {
	partType := part.Get("type").String()
	if partType == "" && part.Get("text").Exists() {
		partType = "text"
	}
	switch partType {
	case "text":
		out := []byte(`{"type":"text","text":""}`)
		out, _ = sjson.SetBytes(out, "text", part.Get("text").String())
		return out, true
	case "image":
		out := []byte(`{"type":"image_url","image_url":{"url":""}}`)
		out, _ = sjson.SetBytes(out, "image_url.url", interactionsMediaDataURL(part, "application/octet-stream"))
		return out, true
	case "audio":
		out := []byte(`{"type":"input_audio","input_audio":{"data":"","format":""}}`)
		out, _ = sjson.SetBytes(out, "input_audio.data", part.Get("data").String())
		out, _ = sjson.SetBytes(out, "input_audio.format", openAIInputAudioFormatFromMIME(part.Get("mime_type").String()))
		return out, true
	case "video":
		out := []byte(`{"type":"video_url","video_url":{"url":""}}`)
		out, _ = sjson.SetBytes(out, "video_url.url", interactionsMediaDataURL(part, "video/mp4"))
		return out, true
	case "document", "file":
		out := []byte(`{"type":"file","file":{"filename":"","file_data":""}}`)
		out, _ = sjson.SetBytes(out, "file.filename", firstNonEmpty(part.Get("filename").String(), openAIFileNameFromMIME(part.Get("mime_type").String())))
		out, _ = sjson.SetBytes(out, "file.file_data", part.Get("data").String())
		if url := firstNonEmpty(part.Get("file_url").String(), part.Get("url").String()); url != "" {
			out, _ = sjson.DeleteBytes(out, "file.file_data")
			out, _ = sjson.SetBytes(out, "file.file_url", url)
		}
		return out, true
	default:
		_ = role
	}
	return nil, false
}

func openAIToolFromInteractionsTool(tool gjson.Result) ([]byte, bool) {
	name := firstNonEmpty(tool.Get("name").String(), tool.Get("function.name").String())
	if name == "" {
		return nil, false
	}
	out := []byte(`{"type":"function","function":{"name":""}}`)
	out, _ = sjson.SetBytes(out, "function.name", name)
	if desc := firstExisting(tool.Get("description"), tool.Get("function.description")); desc.Exists() {
		out, _ = sjson.SetBytes(out, "function.description", desc.String())
	}
	if params := firstExisting(tool.Get("parameters"), tool.Get("function.parameters"), tool.Get("parametersJsonSchema")); params.Exists() {
		out, _ = sjson.SetRawBytes(out, "function.parameters", []byte(params.Raw))
	}
	return out, true
}

func interactionsText(value gjson.Result) string {
	if !value.Exists() {
		return ""
	}
	if value.Type == gjson.String {
		return value.String()
	}
	if text := value.Get("text"); text.Exists() {
		return text.String()
	}
	for _, path := range []string{"content", "parts"} {
		parts := value.Get(path)
		if !parts.Exists() || !parts.IsArray() {
			continue
		}
		var builder strings.Builder
		parts.ForEach(func(_, part gjson.Result) bool {
			builder.WriteString(firstNonEmpty(part.Get("text").String(), part.Get("content.text").String()))
			return true
		})
		return builder.String()
	}
	return ""
}

func interactionsReasoningEffort(root, gen gjson.Result) string {
	for _, value := range []gjson.Result{
		gen.Get("reasoning_effort"),
		gen.Get("thinking_level"),
		gen.Get("thinkingLevel"),
		gen.Get("thinking_config.thinking_level"),
		gen.Get("thinkingConfig.thinkingLevel"),
		root.Get("reasoning_effort"),
	} {
		if value.Exists() && value.Type == gjson.String {
			return strings.ToLower(strings.TrimSpace(value.String()))
		}
	}
	return ""
}

func interactionsMediaDataURL(part gjson.Result, fallbackMimeType string) string {
	if url := firstNonEmpty(part.Get("image_url").String(), part.Get("file_data").String(), part.Get("url").String()); url != "" {
		return url
	}
	data := part.Get("data").String()
	if data == "" {
		return ""
	}
	mimeType := firstNonEmpty(part.Get("mime_type").String(), fallbackMimeType)
	return "data:" + mimeType + ";base64," + data
}

func openAIInputAudioFormatFromMIME(mimeType string) string {
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

func openAIFileNameFromMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "application/pdf":
		return "document.pdf"
	case "text/plain":
		return "document.txt"
	case "text/csv":
		return "document.csv"
	case "application/json":
		return "document.json"
	default:
		if _, suffix, ok := strings.Cut(mimeType, "/"); ok && suffix != "" {
			return fmt.Sprintf("document.%s", strings.ReplaceAll(suffix, "+", "."))
		}
		return "document.bin"
	}
}

func copyNumber(out *[]byte, path string, value gjson.Result) {
	if value.Exists() {
		*out, _ = sjson.SetRawBytes(*out, path, []byte(value.Raw))
	}
}

func jsonStringValue(value gjson.Result, fallback string) string {
	if !value.Exists() {
		return fallback
	}
	if value.Type == gjson.String {
		return value.String()
	}
	return value.Raw
}

func firstExisting(values ...gjson.Result) gjson.Result {
	for _, value := range values {
		if value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
