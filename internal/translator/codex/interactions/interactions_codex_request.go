package interactions

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

func ConvertInteractionsRequestToCodex(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertInteractionsRequestToCodex(modelName, inputRawJSON, stream)

}

// convertInteractionsRequestToCodex also reports a user turn that was left empty
// because its only attachment has no Codex equivalent.
func convertInteractionsRequestToCodex(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	root := util.ParseGJSONBytesNoCopy(inputRawJSON)
	outDoc := ojson.NewDocString(`{"model":"","instructions":"","store":false,"input":[]}`)
	_ = outDoc.Set("model", modelName)
	if stream || root.Get("stream").Bool() {
		_ = outDoc.Set("stream", true)
	}
	copyInteractionsSystemToCodexDoc(outDoc, root)
	hasToolChoice := copyInteractionsGenerationConfigToCodexDoc(outDoc, root)
	signatureTargetModel := translatorcommon.ResolveCodexReplayTargetModel(modelName, inputRawJSON)
	inputItems := translatorcommon.NewRawArrayItems(root.Get("input.#").Int())
	errInput := appendInteractionsInputToCodex(&inputItems, root.Get("input"), signatureTargetModel)
	translatorcommon.SetRawArrayItemsDoc(outDoc, "input", inputItems)
	copyInteractionsToolsToCodexDoc(outDoc, root, hasToolChoice)
	copyInteractionsCodexTopLevelDoc(outDoc, root)
	ensureInteractionsCodexEncryptedContentIncludeDoc(outDoc, root)
	return outDoc.Bytes(), errInput
}

func copyInteractionsSystemToCodexDoc(doc *ojson.Doc, root ojson.Result) {
	systemInstruction := root.Get("system_instruction")
	if !systemInstruction.Exists() {
		systemInstruction = root.Get("systemInstruction")
	}
	if !systemInstruction.Exists() {
		return
	}
	if systemInstruction.Type == ojson.String {
		_ = doc.Set("instructions", systemInstruction.String())
		return
	}
	if text := systemInstruction.Get("text"); text.Exists() && text.Type == ojson.String {
		_ = doc.Set("instructions", text.String())
		return
	}
	if parts := systemInstruction.Get("parts"); parts.Exists() && parts.IsArray() {
		var builder strings.Builder
		parts.ForEach(func(_, part ojson.Result) bool {
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
			_ = doc.Set("instructions", builder.String())
		}
	}
}

func copyInteractionsGenerationConfigToCodexDoc(doc *ojson.Doc, root ojson.Result) bool {
	cfg := root.Get("generation_config")
	if !cfg.Exists() {
		cfg = root.Get("generationConfig")
	}
	if !cfg.Exists() {
		if reasoning := root.Get("reasoning"); reasoning.Exists() {
			_ = doc.SetRawBytes("reasoning", []byte(reasoning.Raw))
		}
		return false
	}
	if reasoning := cfg.Get("reasoning"); reasoning.Exists() {
		_ = doc.SetRawBytes("reasoning", []byte(reasoning.Raw))
	}
	if effort := interactionsCodexReasoningEffort(cfg); effort != "" {
		_ = doc.Set("reasoning.effort", effort)
	}
	if summary := interactionsCodexReasoningSummary(cfg); summary != "" {
		_ = doc.Set("reasoning.summary", summary)
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
	hasToolChoice := false
	for sourcePath, targetPath := range copyRawPaths {
		if value := cfg.Get(sourcePath); value.Exists() {
			_ = doc.SetRawBytes(targetPath, []byte(value.Raw))
			if targetPath == "tool_choice" {
				hasToolChoice = true
			}
		}
	}
	return hasToolChoice
}

func interactionsCodexReasoningEffort(cfg ojson.Result) string {
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

func interactionsCodexReasoningSummary(cfg ojson.Result) string {
	for _, path := range []string{
		"thinking_summaries",
		"thinkingSummaries",
		"reasoning.summary",
	} {
		if value := cfg.Get(path); value.Type == ojson.String {
			summary := strings.ToLower(strings.TrimSpace(value.String()))
			switch summary {
			case "auto", "none":
				return summary
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
		switch value := cfg.Get(path); value.Type {
		case ojson.True:
			return "auto"
		case ojson.False:
			return "none"
		}
	}
	return ""
}

// appendInteractionsInputToCodex adds the input steps and reports a user turn that
// was left with nothing to send because of an attachment Codex cannot carry.
func appendInteractionsInputToCodex(items *[][]byte, input ojson.Result, modelName string) error {
	if !input.Exists() {
		return nil
	}
	if input.Type == ojson.String {
		appendInteractionsTextToCodex(items, "user", input.String())
		return nil
	}
	var run translatorcommon.UserRun
	emittedSignatures := make(map[string]bool)
	if input.IsArray() {
		input.ForEach(func(_, step ojson.Result) bool {
			appendInteractionsStepToCodex(items, step, "user", &run, modelName, emittedSignatures)
			return true
		})
	} else if steps := input.Get("steps"); steps.Exists() && steps.IsArray() {
		defaultRole := interactionsCodexStepRole(input, "user")
		steps.ForEach(func(_, step ojson.Result) bool {
			appendInteractionsStepToCodex(items, step, defaultRole, &run, modelName, emittedSignatures)
			return true
		})
	} else {
		appendInteractionsStepToCodex(items, input, "user", &run, modelName, emittedSignatures)
	}
	run.End()
	return run.Err()
}

// interactionsCodexUserRun returns run for user content. Content of any other
// role closes the open user turn and has no tracker.
func interactionsCodexUserRun(run *translatorcommon.UserRun, role string) *translatorcommon.UserRun {
	if role == "user" {
		return run
	}
	run.End()
	return nil
}

func appendInteractionsStepToCodex(items *[][]byte, step ojson.Result, defaultRole string, run *translatorcommon.UserRun, modelName string, emittedSignatures map[string]bool) {
	if step.Type == ojson.String {
		appendInteractionsRoleTextToCodex(items, defaultRole, step.String(), run)
		return
	}
	if steps := step.Get("steps"); steps.Exists() && steps.IsArray() {
		role := interactionsCodexStepRole(step, defaultRole)
		steps.ForEach(func(_, nested ojson.Result) bool {
			appendInteractionsStepToCodex(items, nested, role, run, modelName, emittedSignatures)
			return true
		})
		return
	}
	stepType := strings.ToLower(strings.TrimSpace(step.Get("type").String()))
	switch stepType {
	case "function_call":
		run.End()
		appendInteractionsFunctionCallToCodex(items, step)
	case "function_result", "function_call_output":
		// A tool result is content the model reads, so it keeps the surrounding user turn.
		run.Add()
		appendInteractionsFunctionResultToCodex(items, step)
	case "model_output", "assistant":
		run.End()
		appendInteractionsContentToCodexItem(items, step.Get("content"), "assistant", run)
	case "thought", "reasoning":
		run.End()
		appendInteractionsThoughtToCodex(items, step, modelName, emittedSignatures)
	case "user_input", "message", "":
		role := interactionsCodexStepRole(step, defaultRole)
		if content := step.Get("content"); content.Exists() {
			appendInteractionsContentToCodexItem(items, content, role, run)
		} else if text := step.Get("text"); text.Exists() {
			appendInteractionsRoleTextToCodex(items, role, text.String(), run)
		}
	default:
		role := interactionsCodexStepRole(step, defaultRole)
		if content := step.Get("content"); content.Exists() {
			appendInteractionsContentToCodexItem(items, content, role, run)
		} else if text := step.Get("text"); text.Exists() {
			appendInteractionsRoleTextToCodex(items, role, text.String(), run)
		}
	}
}

// appendInteractionsRoleTextToCodex adds a text message; blank text does not keep a user turn alive.
func appendInteractionsRoleTextToCodex(items *[][]byte, role, text string, run *translatorcommon.UserRun) {
	appendInteractionsTextToCodex(items, role, text)
	userRun := interactionsCodexUserRun(run, role)
	if strings.TrimSpace(text) != "" {
		userRun.Add()
	}
}

func appendInteractionsContentToCodexItem(items *[][]byte, content ojson.Result, role string, run *translatorcommon.UserRun) {
	if !content.Exists() {
		return
	}
	if content.Type == ojson.String {
		appendInteractionsRoleTextToCodex(items, role, content.String(), run)
		return
	}
	userRun := interactionsCodexUserRun(run, role)
	if content.IsArray() {
		content.ForEach(func(_, part ojson.Result) bool {
			appendInteractionsContentPartToCodex(items, part, role, userRun)
			return true
		})
		return
	}
	if content.IsObject() {
		appendInteractionsContentPartToCodex(items, content, role, userRun)
	}
}

// appendInteractionsContentPartToCodex adds one content part. run is nil for
// content that is not user content; for user content it records what is sent and
// what cannot be.
func appendInteractionsContentPartToCodex(items *[][]byte, part ojson.Result, role string, run *translatorcommon.UserRun) {
	item := interactionsCodexMessagePart(part, role)
	if len(item) == 0 {
		if droppedType := translatorcommon.InteractionsAttachmentType(part); droppedType != "" {
			run.Drop(droppedType)
		}
		return
	}
	appendInteractionsMessagePartToCodex(items, role, item)
	if !interactionsCodexIsBlankText(item) {
		run.Add()
	}
}

// interactionsCodexIsBlankText reports whether a Codex message part is a text part
// holding only whitespace.
func interactionsCodexIsBlankText(item []byte) bool {
	parsed := util.ParseGJSONBytesNoCopy(item)
	partType := parsed.Get("type").String()
	return (partType == "input_text" || partType == "output_text") && strings.TrimSpace(parsed.Get("text").String()) == ""
}

func appendInteractionsFunctionCallToCodex(items *[][]byte, step ojson.Result) {
	itemDoc := ojson.NewDocString(`{"type":"function_call"}`)
	if name := step.Get("name"); name.Exists() {
		_ = itemDoc.Set("name", shortenCodexToolNameIfNeeded(name.String()))
	}
	if callID := interactionsCodexCallID(step); callID != "" {
		_ = itemDoc.Set("call_id", callID)
	}
	if args := step.Get("arguments"); args.Exists() {
		_ = itemDoc.Set("arguments", interactionsCodexJSONString(args))
	} else if args := step.Get("args"); args.Exists() {
		_ = itemDoc.Set("arguments", interactionsCodexJSONString(args))
	}
	*items = append(*items, itemDoc.Bytes())
}

func appendInteractionsFunctionResultToCodex(items *[][]byte, step ojson.Result) {
	itemDoc := ojson.NewDocString(`{"type":"function_call_output"}`)
	if callID := interactionsCodexCallID(step); callID != "" {
		_ = itemDoc.Set("call_id", callID)
	}
	if result := step.Get("result"); result.Exists() {
		_ = itemDoc.Set("output", interactionsCodexOutputString(result))
	} else if output := step.Get("output"); output.Exists() {
		_ = itemDoc.Set("output", interactionsCodexOutputString(output))
	}
	*items = append(*items, itemDoc.Bytes())
}

func copyInteractionsToolsToCodexDoc(doc *ojson.Doc, root ojson.Result, hasToolChoice bool) {
	tools := root.Get("tools")
	if !tools.Exists() {
		return
	}
	if !tools.IsArray() {
		_ = doc.SetRawBytes("tools", []byte(tools.Raw))
		return
	}
	normalized := make([]map[string]any, 0)
	tools.ForEach(func(_, tool ojson.Result) bool {
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
		_ = doc.SetRawBytes("tools", []byte(tools.Raw))
		return
	}
	raw, errMarshal := json.Marshal(normalized)
	if errMarshal != nil {
		_ = doc.SetRawBytes("tools", []byte(tools.Raw))
		return
	}
	_ = doc.SetRawBytes("tools", raw)
	if !hasToolChoice {
		_ = doc.Set("tool_choice", "auto")
	}
}

func copyInteractionsCodexTopLevelDoc(doc *ojson.Doc, root ojson.Result) {
	if serviceTier := normalizeInteractionsCodexServiceTier(root.Get("service_tier")); serviceTier != "" {
		_ = doc.Set("service_tier", serviceTier)
	}
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		_ = doc.SetRawBytes("tool_choice", []byte(toolChoice.Raw))
	}
	for _, path := range []string{"parallel_tool_calls", "store", "metadata", "include", "truncation"} {
		if value := root.Get(path); value.Exists() {
			_ = doc.SetRawBytes(path, []byte(value.Raw))
		}
	}
}

func setInteractionsCodexRawIfDifferent(out []byte, path string, value ojson.Result) []byte {
	current := ojson.GetBytesNoCopy(out, path)
	if current.Exists() && current.Raw == value.Raw {
		return out
	}
	updated, errSet := ojson.SetRawBytes(out, path, []byte(value.Raw))
	if errSet != nil {
		return out
	}
	return updated
}

// ensureInteractionsCodexEncryptedContentIncludeDoc makes the outbound request
// ask the upstream for encrypted reasoning content so a signature can come
// back for replay.
func ensureInteractionsCodexEncryptedContentIncludeDoc(doc *ojson.Doc, root ojson.Result) {
	const reasoningEncryptedContent = "reasoning.encrypted_content"
	include := root.Get("include")
	if !include.Exists() {
		_ = doc.SetRawBytes("include", []byte(`["reasoning.encrypted_content"]`))
		return
	}
	if !include.IsArray() {
		return
	}
	for _, value := range include.Array() {
		if value.Type == ojson.String && value.String() == reasoningEncryptedContent {
			return
		}
	}
	_ = doc.Set("include.-1", reasoningEncryptedContent)
}

// appendInteractionsThoughtToCodex adds a thought step. A client-echoed
// signature that CodexReplaySignatureForTarget accepts for this request's
// target model is restored as Responses reasoning encrypted_content, with
// the thought text carried as a summary; identical signatures already
// emitted in this request are replayed only once. The step id is
// deliberately not copied onto replay items: replay is stateless and the
// executor strips orphan ids. Every other thought step is dropped entirely:
// a reasoning item without accepted encrypted_content carries no replayable
// state, and the legacy text fallback shape is rejected by both the native
// Meta upstream (missing summary) and routers (content expected array).
func appendInteractionsThoughtToCodex(items *[][]byte, step ojson.Result, modelName string, emittedSignatures map[string]bool) {
	text := interactionsCodexContentText(step.Get("content"))
	if text == "" {
		text = step.Get("text").String()
	}
	if raw := interactionsThoughtStepSignature(step); raw != "" {
		if normalized, ok := translatorcommon.CodexReplaySignatureForTarget(modelName, raw); ok && !emittedSignatures[normalized] {
			emittedSignatures[normalized] = true
			itemDoc := ojson.NewDocString(`{"type":"reasoning","encrypted_content":"","summary":[]}`)
			_ = itemDoc.Set("encrypted_content", normalized)
			if text != "" {
				summaryPart := []byte(`{"type":"summary_text","text":""}`)
				summaryPart, _ = ojson.SetBytes(summaryPart, "text", text)
				_ = itemDoc.SetRawBytes("summary", translatorcommon.JoinRawArray([][]byte{summaryPart}))
			}
			*items = append(*items, itemDoc.Bytes())
		}
	}
}

// interactionsThoughtStepSignature extracts a thought step's signature under
// any of the field spellings seen on the wire.
func interactionsThoughtStepSignature(step ojson.Result) string {
	for _, path := range []string{"signature", "thought_signature", "thoughtSignature"} {
		if signature := strings.TrimSpace(step.Get(path).String()); signature != "" {
			return signature
		}
	}
	return ""
}

func appendInteractionsTextToCodex(items *[][]byte, role, text string) {
	partDoc := ojson.NewDocString(`{"type":"","text":""}`)
	if role == "assistant" {
		_ = partDoc.Set("type", "output_text")
	} else {
		_ = partDoc.Set("type", "input_text")
	}
	_ = partDoc.Set("text", text)
	appendInteractionsMessagePartToCodex(items, role, partDoc.Bytes())
}

func appendInteractionsMessagePartToCodex(items *[][]byte, role string, part []byte) {
	messageDoc := ojson.NewDocString(`{"type":"message","role":"","content":[]}`)
	_ = messageDoc.Set("role", role)
	_ = messageDoc.SetRawBytes("content", translatorcommon.JoinRawArray([][]byte{part}))
	*items = append(*items, messageDoc.Bytes())
}

func interactionsCodexMessagePart(part ojson.Result, role string) []byte {
	if text := part.Get("text"); text.Exists() {
		itemDoc := ojson.NewDocString(`{"type":"","text":""}`)
		if role == "assistant" {
			_ = itemDoc.Set("type", "output_text")
		} else {
			_ = itemDoc.Set("type", "input_text")
		}
		_ = itemDoc.Set("text", text.String())
		return itemDoc.Bytes()
	}
	partType := strings.ToLower(strings.TrimSpace(part.Get("type").String()))
	switch partType {
	case "text", "":
		return nil
	case "image":
		return interactionsCodexImagePart(part)
	case "image_url":
		item := []byte(`{"type":"input_image","image_url":""}`)
		item, _ = ojson.SetBytes(item, "image_url", part.Get("image_url.url").String())
		return item
	case "audio":
		return interactionsCodexAudioPart(part)
	case "input_audio":
		item := []byte(`{"type":"input_audio","input_audio":{}}`)
		if audio := part.Get("input_audio"); audio.Exists() {
			item, _ = ojson.SetRawBytes(item, "input_audio", []byte(audio.Raw))
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

func interactionsCodexImagePart(part ojson.Result) []byte {
	// uri is the Interactions spelling of file_uri.
	if imageURL := firstNonBlankString(part, "url", "file_uri", "fileUri", "uri"); imageURL != "" {
		item := []byte(`{"type":"input_image","image_url":""}`)
		item, _ = ojson.SetBytes(item, "image_url", imageURL)
		return item
	}
	mimeType := firstString(part, "mime_type", "mimeType")
	data := part.Get("data").String()
	if mimeType == "" || data == "" {
		return nil
	}
	item := []byte(`{"type":"input_image","image_url":""}`)
	item, _ = ojson.SetBytes(item, "image_url", fmt.Sprintf("data:%s;base64,%s", mimeType, data))
	return item
}

func interactionsCodexAudioPart(part ojson.Result) []byte {
	mimeType := firstString(part, "mime_type", "mimeType")
	data := part.Get("data").String()
	if mimeType == "" || data == "" {
		return nil
	}
	itemDoc := ojson.NewDocString(`{"type":"input_audio","input_audio":{"data":"","format":""}}`)
	_ = itemDoc.Set("input_audio.data", data)
	_ = itemDoc.Set("input_audio.format", codexInputAudioFormatFromMIME(mimeType))
	return itemDoc.Bytes()
}

func interactionsCodexFilePart(part ojson.Result) []byte {
	if fileData := part.Get("file.file_data").String(); fileData != "" {
		itemDoc := ojson.NewDocString(`{"type":"input_file","file_data":"","filename":""}`)
		_ = itemDoc.Set("file_data", fileData)
		_ = itemDoc.Set("filename", part.Get("file.filename").String())
		return itemDoc.Bytes()
	}
	mimeType := firstString(part, "mime_type", "mimeType")
	// uri is the Interactions spelling of file_uri.
	if fileURI := firstNonBlankString(part, "file_uri", "fileUri", "uri", "url"); fileURI != "" {
		itemDoc := ojson.NewDocString(`{"type":"input_file","file_url":"","filename":""}`)
		_ = itemDoc.Set("file_url", fileURI)
		_ = itemDoc.Set("filename", codexFileNameFromMIME(mimeType))
		return itemDoc.Bytes()
	}
	data := part.Get("data").String()
	if mimeType == "" || data == "" {
		return nil
	}
	itemDoc := ojson.NewDocString(`{"type":"input_file","file_data":"","filename":""}`)
	_ = itemDoc.Set("file_data", data)
	_ = itemDoc.Set("filename", codexFileNameFromMIME(mimeType))
	return itemDoc.Bytes()
}

func interactionsCodexInlinePart(inline ojson.Result) []byte {
	mimeType := firstString(inline, "mime_type", "mimeType")
	data := inline.Get("data").String()
	if mimeType == "" || data == "" {
		return nil
	}
	switch {
	case strings.HasPrefix(strings.ToLower(mimeType), "image/"):
		return interactionsCodexImagePart(ojson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, data)))
	case strings.HasPrefix(strings.ToLower(mimeType), "audio/"):
		return interactionsCodexAudioPart(ojson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, data)))
	default:
		return interactionsCodexFilePart(ojson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, data)))
	}
}

func interactionsCodexFileDataPart(fileData ojson.Result) []byte {
	mimeType := firstString(fileData, "mime_type", "mimeType")
	fileURI := firstString(fileData, "file_uri", "fileUri")
	if fileURI == "" {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		item := []byte(`{"type":"input_image","image_url":""}`)
		item, _ = ojson.SetBytes(item, "image_url", fileURI)
		return item
	}
	itemDoc := ojson.NewDocString(`{"type":"input_file","file_url":"","filename":""}`)
	_ = itemDoc.Set("file_url", fileURI)
	_ = itemDoc.Set("filename", codexFileNameFromMIME(mimeType))
	return itemDoc.Bytes()
}

func appendCodexToolDeclarations(normalized *[]map[string]any, declarations ojson.Result) {
	if !declarations.IsArray() {
		return
	}
	declarations.ForEach(func(_, declaration ojson.Result) bool {
		if declaration.Get("name").Exists() {
			*normalized = append(*normalized, codexToolFromDeclaration(declaration))
		}
		return true
	})
}

func codexToolFromDeclaration(declaration ojson.Result) map[string]any {
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

func cleanedCodexToolParameters(params ojson.Result) json.RawMessage {
	hasSchema := params.Get("$schema").Exists()
	needsAdditionalProps := params.Get("additionalProperties").Type != ojson.False
	if !hasSchema && !needsAdditionalProps {
		return json.RawMessage(params.Raw)
	}
	doc := ojson.NewDocString(params.Raw)
	if hasSchema {
		_ = doc.Delete("$schema")
	}
	if needsAdditionalProps {
		_ = doc.Set("additionalProperties", false)
	}
	return json.RawMessage(doc.Bytes())
}

func interactionsCodexContentText(content ojson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == ojson.String {
		return content.String()
	}
	if content.IsObject() {
		return content.Get("text").String()
	}
	if content.IsArray() {
		var builder strings.Builder
		content.ForEach(func(_, part ojson.Result) bool {
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

func interactionsCodexCallID(step ojson.Result) string {
	if callID := strings.TrimSpace(step.Get("call_id").String()); callID != "" {
		return callID
	}
	return strings.TrimSpace(step.Get("id").String())
}

func interactionsCodexJSONString(value ojson.Result) string {
	if value.Type == ojson.String {
		return value.String()
	}
	if value.Exists() {
		return value.Raw
	}
	return "{}"
}

func interactionsCodexOutputString(value ojson.Result) string {
	if value.Type == ojson.String {
		return value.String()
	}
	if value.Exists() {
		return value.Raw
	}
	return ""
}

// interactionsCodexStepRole resolves the Codex role of a step. A step that
// carries system or developer content, named by its role or by its type, is
// developer content: it closes the open user turn and never keeps an emptied one
// alive. Any other step uses its own role, then the role it inherits.
func interactionsCodexStepRole(step ojson.Result, inherited string) string {
	if translatorcommon.IsInteractionsInstructionStep(step, inherited == "developer") {
		return "developer"
	}
	return interactionsCodexDefaultRole(step.Get("role").String(), inherited)
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

func normalizeInteractionsCodexServiceTier(serviceTier ojson.Result) string {
	if !serviceTier.Exists() || serviceTier.Type != ojson.String {
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

// firstNonBlankString returns the first non-blank string found at paths.
func firstNonBlankString(root ojson.Result, paths ...string) string {
	for _, path := range paths {
		if value := strings.TrimSpace(root.Get(path).String()); value != "" {
			return value
		}
	}
	return ""
}

func firstString(root ojson.Result, paths ...string) string {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value.String()
		}
	}
	return ""
}
