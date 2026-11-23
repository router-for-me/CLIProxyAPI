package chat_completions

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

func ConvertInteractionsRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	root := util.ParseGJSONBytesNoCopy(inputRawJSON)
	outDoc := ojson.NewDocString(`{"model":"","messages":[]}`)
	model := firstNonEmpty(modelName, root.Get("model").String())
	_ = outDoc.Set("model", model)
	if stream || root.Get("stream").Bool() {
		_ = outDoc.Set("stream", true)
	}
	messageCapacity := root.Get("input.#").Int()
	if interactionsText(root.Get("system_instruction")) != "" {
		messageCapacity++
	}
	messageItems := translatorcommon.NewRawArrayItems(messageCapacity)
	appendInteractionsSystemToOpenAI(&messageItems, root)
	forAntigravity := isAntigravityModel(model)
	appendInteractionsInputToOpenAIMessages(&messageItems, root.Get("input"), forAntigravity)
	translatorcommon.SetRawArrayItemsDoc(outDoc, "messages", messageItems)
	copyInteractionsToolsToOpenAIDoc(outDoc, root, forAntigravity)
	copyInteractionsGenerationConfigToOpenAIDoc(outDoc, root)
	copyInteractionsOpenAITopLevelDoc(outDoc, root)
	return outDoc.Bytes(), nil
}

func appendInteractionsSystemToOpenAI(items *[][]byte, root ojson.Result) {
	text := interactionsText(root.Get("system_instruction"))
	if text == "" {
		return
	}
	msg := []byte(`{"role":"system","content":""}`)
	msg, _ = ojson.SetBytes(msg, "content", text)
	*items = append(*items, msg)
}

func appendInteractionsInputToOpenAIMessages(items *[][]byte, input ojson.Result, forAntigravity bool) {
	if input.Type == ojson.String {
		msg := []byte(`{"role":"user","content":""}`)
		msg, _ = ojson.SetBytes(msg, "content", input.String())
		*items = append(*items, msg)
		return
	}
	if input.IsArray() {
		input.ForEach(func(_, step ojson.Result) bool {
			appendInteractionsStepToOpenAI(items, step, "user", forAntigravity)
			return true
		})
		return
	}
	if input.IsObject() {
		appendInteractionsStepToOpenAI(items, input, "user", forAntigravity)
	}
}

func appendInteractionsStepToOpenAI(items *[][]byte, step ojson.Result, defaultRole string, forAntigravity bool) {
	switch step.Get("type").String() {
	case "user_input":
		appendInteractionsMessageToOpenAI(items, step, "user")
	case "model_output":
		appendInteractionsMessageToOpenAI(items, step, "assistant")
	case "thought":
		appendInteractionsThoughtToOpenAI(items, step)
	case "function_call":
		appendInteractionsFunctionCallToOpenAI(items, step, forAntigravity)
	case "function_result":
		appendInteractionsFunctionResultToOpenAI(items, step)
	default:
		if step.Type == ojson.String {
			msgDoc := ojson.NewDocString(`{"role":"","content":""}`)
			_ = msgDoc.Set("role", defaultRole)
			_ = msgDoc.Set("content", step.String())
			*items = append(*items, msgDoc.Bytes())
		}
	}
}

func appendInteractionsMessageToOpenAI(items *[][]byte, step ojson.Result, role string) {
	msgDoc := ojson.NewDocString(`{"role":"","content":""}`)
	_ = msgDoc.Set("role", role)
	content := step.Get("content")
	if content.Type == ojson.String {
		_ = msgDoc.Set("content", content.String())
	} else {
		appendInteractionsContentToOpenAIMessageDoc(msgDoc, content, role)
	}
	*items = append(*items, msgDoc.Bytes())
}

func appendInteractionsThoughtToOpenAI(items *[][]byte, step ojson.Result) {
	msg := []byte(`{"role":"assistant","content":"","reasoning_content":""}`)
	msg, _ = ojson.SetBytes(msg, "reasoning_content", interactionsText(step.Get("content")))
	*items = append(*items, msg)
}

func appendInteractionsContentToOpenAIMessageDoc(msgDoc *ojson.Doc, content ojson.Result, role string) {
	if !content.Exists() {
		return
	}
	if content.Type == ojson.String {
		_ = msgDoc.Set("content", content.String())
		return
	}
	contentItems := make([][]byte, 0, 4)
	textOnly := true
	var textBuilder strings.Builder
	appendPart := func(part ojson.Result) {
		converted, ok := interactionsContentPartToOpenAI(part, role)
		if !ok {
			return
		}
		if ojson.GetBytesNoCopy(converted, "type").String() == "text" {
			textBuilder.WriteString(ojson.GetBytesNoCopy(converted, "text").String())
		} else {
			textOnly = false
		}
		contentItems = append(contentItems, converted)
	}
	if content.IsArray() {
		content.ForEach(func(_, part ojson.Result) bool {
			appendPart(part)
			return true
		})
	} else if content.IsObject() {
		appendPart(content)
	}
	if len(contentItems) > 0 {
		if textOnly {
			_ = msgDoc.Set("content", textBuilder.String())
		} else {
			_ = msgDoc.SetRawBytes("content", translatorcommon.JoinRawArray(contentItems))
		}
	}
}

func appendInteractionsFunctionCallToOpenAI(items *[][]byte, step ojson.Result, forAntigravity bool) {
	msg := []byte(`{"role":"assistant","content":"","tool_calls":[]}`)
	toolCallDoc := ojson.NewDocString(`{"id":"","type":"function","function":{"name":"","arguments":"{}"}}`)
	callID := firstNonEmpty(step.Get("call_id").String(), step.Get("id").String(), "call_0")
	_ = toolCallDoc.Set("id", callID)
	name := step.Get("name").String()
	if forAntigravity {
		name = translatorcommon.AntigravityUpstreamToolNameToClient(name)
	}
	_ = toolCallDoc.Set("function.name", name)
	_ = toolCallDoc.Set("function.arguments", jsonStringValue(step.Get("arguments"), "{}"))
	msg = translatorcommon.SetRawArrayItems(msg, "tool_calls", [][]byte{toolCallDoc.Bytes()})
	*items = append(*items, msg)
}

func appendInteractionsFunctionResultToOpenAI(items *[][]byte, step ojson.Result) {
	msgDoc := ojson.NewDocString(`{"role":"tool","tool_call_id":"","content":""}`)
	_ = msgDoc.Set("tool_call_id", firstNonEmpty(step.Get("call_id").String(), step.Get("id").String()))
	_ = msgDoc.Set("content", jsonStringValue(firstExisting(step.Get("result"), step.Get("output")), ""))
	*items = append(*items, msgDoc.Bytes())
}

func copyInteractionsToolsToOpenAIDoc(outDoc *ojson.Doc, root ojson.Result, forAntigravity bool) {
	tools := root.Get("tools")
	if !tools.Exists() || !tools.IsArray() {
		return
	}
	var toolItems [][]byte
	tools.ForEach(func(_, tool ojson.Result) bool {
		if converted, ok := openAIToolFromInteractionsTool(tool, forAntigravity); ok {
			toolItems = append(toolItems, converted)
		}
		if decls := firstExisting(tool.Get("function_declarations"), tool.Get("functionDeclarations")); decls.Exists() && decls.IsArray() {
			decls.ForEach(func(_, decl ojson.Result) bool {
				if converted, ok := openAIToolFromInteractionsTool(decl, forAntigravity); ok {
					toolItems = append(toolItems, converted)
				}
				return true
			})
		}
		return true
	})
	if len(toolItems) > 0 {
		_ = outDoc.SetRawBytes("tools", translatorcommon.JoinRawArray(toolItems))
	}
}

func copyInteractionsGenerationConfigToOpenAIDoc(outDoc *ojson.Doc, root ojson.Result) {
	gen := root.Get("generation_config")
	if !gen.Exists() {
		gen = root.Get("generationConfig")
	}
	copyNumberDoc(outDoc, "temperature", firstExisting(gen.Get("temperature"), root.Get("temperature")))
	copyNumberDoc(outDoc, "max_tokens", firstExisting(gen.Get("max_output_tokens"), gen.Get("maxOutputTokens"), root.Get("max_tokens"), root.Get("max_completion_tokens")))
	copyNumberDoc(outDoc, "top_p", firstExisting(gen.Get("top_p"), gen.Get("topP"), root.Get("top_p")))
	copyNumberDoc(outDoc, "top_k", firstExisting(gen.Get("top_k"), gen.Get("topK")))
	copyNumberDoc(outDoc, "n", firstExisting(gen.Get("candidate_count"), gen.Get("candidateCount"), root.Get("n")))
	if stop := firstExisting(gen.Get("stop_sequences"), gen.Get("stopSequences"), root.Get("stop")); stop.Exists() {
		_ = outDoc.SetRaw("stop", stop.Raw)
	}
	if toolChoice := firstExisting(gen.Get("tool_choice"), root.Get("tool_choice")); toolChoice.Exists() {
		_ = outDoc.SetRaw("tool_choice", toolChoice.Raw)
	}
	if effort := interactionsReasoningEffort(root, gen); effort != "" {
		_ = outDoc.Set("reasoning_effort", effort)
	}
	if responseModalities := root.Get("response_modalities"); responseModalities.Exists() {
		_ = outDoc.SetRaw("modalities", responseModalities.Raw)
	}
}

func copyInteractionsOpenAITopLevelDoc(outDoc *ojson.Doc, root ojson.Result) {
	if format := root.Get("response_format"); format.Exists() {
		_ = outDoc.SetRaw("response_format", format.Raw)
	}
	if serviceTier := root.Get("service_tier"); serviceTier.Exists() && serviceTier.Type == ojson.String {
		_ = outDoc.Set("service_tier", serviceTier.String())
	}
	if previousInteractionID := firstNonEmpty(root.Get("previous_interaction_id").String(), root.Get("previous_response_id").String()); previousInteractionID != "" {
		_ = outDoc.Set("previous_response_id", previousInteractionID)
	}
	if environmentID := firstNonEmpty(root.Get("environment_id").String(), root.Get("environment.id").String()); environmentID != "" {
		_ = outDoc.Set("environment_id", environmentID)
	}
	if agentConfig := root.Get("agent_config"); agentConfig.Exists() {
		_ = outDoc.SetRaw("agent_config", agentConfig.Raw)
	}
	for _, key := range []string{"parallel_tool_calls", "seed", "user"} {
		if value := root.Get(key); value.Exists() {
			_ = outDoc.SetRaw(key, value.Raw)
		}
	}
}

func interactionsContentPartToOpenAI(part ojson.Result, role string) ([]byte, bool) {
	partType := part.Get("type").String()
	if partType == "" && part.Get("text").Exists() {
		partType = "text"
	}
	switch partType {
	case "text":
		out := []byte(`{"type":"text","text":""}`)
		out, _ = ojson.SetBytes(out, "text", part.Get("text").String())
		return out, true
	case "image":
		out := []byte(`{"type":"image_url","image_url":{"url":""}}`)
		out, _ = ojson.SetBytes(out, "image_url.url", interactionsMediaDataURL(part, "application/octet-stream"))
		return out, true
	case "audio":
		outDoc := ojson.NewDocString(`{"type":"input_audio","input_audio":{"data":"","format":""}}`)
		_ = outDoc.Set("input_audio.data", part.Get("data").String())
		_ = outDoc.Set("input_audio.format", openAIInputAudioFormatFromMIME(part.Get("mime_type").String()))
		return outDoc.Bytes(), true
	case "video":
		out := []byte(`{"type":"video_url","video_url":{"url":""}}`)
		out, _ = ojson.SetBytes(out, "video_url.url", interactionsMediaDataURL(part, "video/mp4"))
		return out, true
	case "document", "file":
		outDoc := ojson.NewDocString(`{"type":"file","file":{"filename":"","file_data":""}}`)
		_ = outDoc.Set("file.filename", firstNonEmpty(part.Get("filename").String(), openAIFileNameFromMIME(part.Get("mime_type").String())))
		_ = outDoc.Set("file.file_data", part.Get("data").String())
		if url := firstNonEmpty(part.Get("file_url").String(), part.Get("url").String()); url != "" {
			_ = outDoc.Delete("file.file_data")
			_ = outDoc.Set("file.file_url", url)
		}
		return outDoc.Bytes(), true
	default:
		_ = role
	}
	return nil, false
}

func openAIToolFromInteractionsTool(tool ojson.Result, forAntigravity bool) ([]byte, bool) {
	name := firstNonEmpty(tool.Get("name").String(), tool.Get("function.name").String())
	if name == "" {
		return nil, false
	}
	if forAntigravity {
		name = translatorcommon.AntigravityUpstreamToolNameToClient(name)
	}
	outDoc := ojson.NewDocString(`{"type":"function","function":{"name":""}}`)
	_ = outDoc.Set("function.name", name)
	if desc := firstExisting(tool.Get("description"), tool.Get("function.description")); desc.Exists() {
		_ = outDoc.Set("function.description", desc.String())
	}
	if params := firstExisting(tool.Get("parameters"), tool.Get("function.parameters"), tool.Get("parametersJsonSchema")); params.Exists() {
		_ = outDoc.SetRaw("function.parameters", params.Raw)
	}
	return outDoc.Bytes(), true
}

func interactionsText(value ojson.Result) string {
	if !value.Exists() {
		return ""
	}
	if value.Type == ojson.String {
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
		parts.ForEach(func(_, part ojson.Result) bool {
			builder.WriteString(firstNonEmpty(part.Get("text").String(), part.Get("content.text").String()))
			return true
		})
		return builder.String()
	}
	return ""
}

func interactionsReasoningEffort(root, gen ojson.Result) string {
	for _, value := range []ojson.Result{
		gen.Get("reasoning_effort"),
		gen.Get("thinking_level"),
		gen.Get("thinkingLevel"),
		gen.Get("thinking_config.thinking_level"),
		gen.Get("thinkingConfig.thinkingLevel"),
		root.Get("reasoning_effort"),
	} {
		if value.Exists() && value.Type == ojson.String {
			return strings.ToLower(strings.TrimSpace(value.String()))
		}
	}
	return ""
}

func interactionsMediaDataURL(part ojson.Result, fallbackMimeType string) string {
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

func copyNumberDoc(outDoc *ojson.Doc, path string, value ojson.Result) {
	if value.Exists() {
		_ = outDoc.SetRaw(path, value.Raw)
	}
}

func jsonStringValue(value ojson.Result, fallback string) string {
	if !value.Exists() {
		return fallback
	}
	if value.Type == ojson.String {
		return value.String()
	}
	return value.Raw
}

func firstExisting(values ...ojson.Result) ojson.Result {
	for _, value := range values {
		if value.Exists() {
			return value
		}
	}
	return ojson.Result{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
