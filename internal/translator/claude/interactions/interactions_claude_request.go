package interactions

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

func ConvertInteractionsRequestToClaude(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertInteractionsRequestToClaude(modelName, inputRawJSON, stream)

}

// convertInteractionsRequestToClaude also reports a user turn that was left empty
// because its only media part has no Claude equivalent.
func convertInteractionsRequestToClaude(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	var run interactionsClaudeUserRun
	root := util.ParseGJSONBytesNoCopy(inputRawJSON)
	outDoc := ojson.NewDoc([]byte(`{"model":"","max_tokens":32000,"messages":[]}`))
	_ = outDoc.Set("model", modelName)
	if stream || root.Get("stream").Bool() {
		_ = outDoc.Set("stream", true)
	}
	copyInteractionsSystemToClaudeDoc(outDoc, root)
	copyInteractionsGenerationConfigToClaudeDoc(outDoc, root)
	messageAccumulator := translatorcommon.NewClaudeMessageAccumulator(int(root.Get("input.#").Int()))
	appendInteractionsInputToClaudeMessages(messageAccumulator, root.Get("input"), &run)
	run.end()
	translatorcommon.SetRawArrayItemsDoc(outDoc, "messages", messageAccumulator.Messages())
	copyInteractionsToolsToClaudeDoc(outDoc, root)
	return outDoc.Bytes(), run.drops.Err()
}

// interactionsClaudeUserRun follows the consecutive user content that the message
// accumulator merges into one Claude user message. A media part Claude cannot
// carry is refused only when that whole turn is left with nothing to send, so
// text or a tool result in a neighbouring step still keeps the turn alive.
type interactionsClaudeUserRun struct {
	drops    translatorcommon.UserTurnDrops
	sendable int
}

func (r *interactionsClaudeUserRun) add() {
	r.sendable++
}

func (r *interactionsClaudeUserRun) drop(partType string) {
	r.drops.Drop(partType)
}

// end closes the current user turn; call it when a non-user message starts or the input ends.
func (r *interactionsClaudeUserRun) end() {
	r.drops.EndTurn(r.sendable)
	r.sendable = 0
}

func copyInteractionsSystemToClaudeDoc(outDoc *ojson.Doc, root ojson.Result) {
	sys := root.Get("system_instruction")
	if !sys.Exists() {
		sys = root.Get("systemInstruction")
	}
	text := interactionsClaudeText(sys)
	if text == "" {
		return
	}
	_ = outDoc.Set("system", text)
}

func copyInteractionsGenerationConfigToClaudeDoc(outDoc *ojson.Doc, root ojson.Result) {
	cfg := root.Get("generation_config")
	if !cfg.Exists() {
		cfg = root.Get("generationConfig")
	}
	if cfg.Exists() {
		copyJSONFieldDoc(outDoc, cfg, "max_output_tokens", "max_tokens")
		copyJSONFieldDoc(outDoc, cfg, "maxOutputTokens", "max_tokens")
		copyJSONFieldDoc(outDoc, cfg, "top_p", "top_p")
		copyJSONFieldDoc(outDoc, cfg, "topP", "top_p")
		copyJSONFieldDoc(outDoc, cfg, "temperature", "temperature")
		copyJSONFieldDoc(outDoc, cfg, "stop_sequences", "stop_sequences")
		copyJSONFieldDoc(outDoc, cfg, "stopSequences", "stop_sequences")
		copyInteractionsThinkingConfigToClaudeDoc(outDoc, cfg)
		copyInteractionsToolChoiceToClaudeDoc(outDoc, cfg.Get("tool_choice"))
		copyInteractionsToolChoiceToClaudeDoc(outDoc, cfg.Get("toolChoice"))
	}
	copyInteractionsReasoningToClaudeDoc(outDoc, root.Get("reasoning"))
	copyInteractionsToolChoiceToClaudeDoc(outDoc, root.Get("tool_choice"))
	copyInteractionsToolChoiceToClaudeDoc(outDoc, root.Get("toolChoice"))
}

func copyJSONFieldDoc(outDoc *ojson.Doc, root ojson.Result, from, to string) {
	value := root.Get(from)
	if !value.Exists() {
		return
	}
	_ = outDoc.SetRaw(to, value.Raw)
}

func copyInteractionsThinkingConfigToClaudeDoc(outDoc *ojson.Doc, cfg ojson.Result) {
	level := firstClaudeInteractionsExisting(cfg, "thinking_level", "thinkingLevel", "reasoning.effort")
	if !level.Exists() {
		return
	}
	setClaudeThinkingFromLevelDoc(outDoc, level.String())
}

func copyInteractionsReasoningToClaudeDoc(outDoc *ojson.Doc, reasoning ojson.Result) {
	if !reasoning.Exists() {
		return
	}
	if effort := reasoning.Get("effort"); effort.Exists() {
		setClaudeThinkingFromLevelDoc(outDoc, effort.String())
		return
	}
	if level := reasoning.Get("thinking_level"); level.Exists() {
		setClaudeThinkingFromLevelDoc(outDoc, level.String())
	}
}

func setClaudeThinkingFromLevelDoc(outDoc *ojson.Doc, level string) {
	normalized := strings.ToLower(strings.TrimSpace(level))
	if normalized == "" {
		return
	}
	switch normalized {
	case "none", "disabled", "off", "false":
		_ = outDoc.Set("thinking.type", "disabled")
		_ = outDoc.Delete("thinking.budget_tokens")
		return
	case "auto", "adaptive":
		_ = outDoc.Set("thinking.type", "adaptive")
		_ = outDoc.Delete("thinking.budget_tokens")
		return
	}
	if budget, ok := thinking.ConvertLevelToBudget(normalized); ok {
		switch {
		case budget == 0:
			_ = outDoc.Set("thinking.type", "disabled")
		case budget < 0:
			_ = outDoc.Set("thinking.type", "enabled")
		default:
			_ = outDoc.Set("thinking.type", "enabled")
			_ = outDoc.Set("thinking.budget_tokens", budget)
		}
		return
	}
	_ = outDoc.Set("thinking.type", "adaptive")
	_ = outDoc.Set("output_config.effort", normalized)
}

func appendInteractionsInputToClaudeMessages(accumulator *translatorcommon.ClaudeMessageAccumulator, input ojson.Result, run *interactionsClaudeUserRun) {
	if !input.Exists() {
		return
	}
	if input.Type == ojson.String {
		step := []byte(`{"type":"user_input","content":[{"type":"text","text":""}]}`)
		step, _ = ojson.SetBytes(step, "content.0.text", input.String())
		appendInteractionsStepToClaude(accumulator, util.ParseGJSONBytesNoCopy(step), "user", false, run)
		return
	}
	if input.IsObject() {
		appendInteractionsInputItemToClaude(accumulator, input, run)
		return
	}
	input.ForEach(func(_, step ojson.Result) bool {
		appendInteractionsInputItemToClaude(accumulator, step, run)
		return true
	})
}

func appendInteractionsInputItemToClaude(accumulator *translatorcommon.ClaudeMessageAccumulator, step ojson.Result, run *interactionsClaudeUserRun) {
	if step.Get("steps").IsArray() {
		defaultRole := "user"
		if role := step.Get("role").String(); role == "model" || role == "assistant" {
			defaultRole = "assistant"
		}
		instruction := translatorcommon.IsInteractionsInstructionStep(step, false)
		step.Get("steps").ForEach(func(_, nestedStep ojson.Result) bool {
			appendInteractionsStepToClaude(accumulator, nestedStep, defaultRole, instruction, run)
			return true
		})
		return
	}
	if step.Get("parts").Exists() {
		wrappedDoc := ojson.NewDoc([]byte(`{"type":"user_input","content":[]}`))
		if role := step.Get("role").String(); role == "model" || role == "assistant" {
			_ = wrappedDoc.Set("type", "model_output")
		}
		_ = wrappedDoc.SetRaw("content", step.Get("parts").Raw)
		appendInteractionsStepToClaude(accumulator, util.ParseGJSONBytesNoCopy(wrappedDoc.Bytes()), "user", translatorcommon.IsInteractionsInstructionStep(step, false), run)
		return
	}
	stepType := step.Get("type").String()
	switch stepType {
	case "function_call":
		appendInteractionsFunctionCallToClaude(accumulator, step, run)
	case "function_result":
		appendInteractionsFunctionResultToClaude(accumulator, step, run)
	case "model_output", "thought":
		appendInteractionsStepToClaude(accumulator, step, "assistant", false, run)
	default:
		appendInteractionsStepToClaude(accumulator, step, "user", false, run)
	}
}

// appendInteractionsStepToClaude adds one step. instruction says the step sits in
// a developer or system wrapper. Developer and system content is sent as user
// content, but it is not the user's own turn: it closes the open user turn and
// never counts toward keeping an emptied one alive.
func appendInteractionsStepToClaude(accumulator *translatorcommon.ClaudeMessageAccumulator, step ojson.Result, defaultRole string, instruction bool, run *interactionsClaudeUserRun) {
	role := defaultRole
	if stepRole := step.Get("role").String(); stepRole == "user" || stepRole == "assistant" {
		role = stepRole
	}
	userContent := role == "user" && !translatorcommon.IsInteractionsInstructionStep(step, instruction)
	contentItems := make([][]byte, 0, 4)
	appendPart := func(part ojson.Result) {
		converted := interactionsContentToClaude(part, role)
		if len(converted) == 0 {
			if userContent {
				if droppedType := interactionsClaudeDroppedPart(part); droppedType != "" {
					run.drop(droppedType)
				}
			}
			return
		}
		contentItems = append(contentItems, converted)
		if userContent && interactionsClaudePartIsSendable(converted) {
			run.add()
		}
	}
	stepContent := step.Get("content")
	if stepContent.Type == ojson.String {
		part := []byte(`{"type":"text","text":""}`)
		part, _ = ojson.SetBytes(part, "text", stepContent.String())
		appendPart(util.ParseGJSONBytesNoCopy(part))
	} else if stepContent.IsArray() {
		stepContent.ForEach(func(_, part ojson.Result) bool {
			appendPart(part)
			return true
		})
	} else if text := step.Get("text"); text.Exists() {
		part := []byte(`{"type":"text","text":""}`)
		part, _ = ojson.SetBytes(part, "text", text.String())
		appendPart(util.ParseGJSONBytesNoCopy(part))
	} else if interactionsClaudeDroppedMedia(step) != "" {
		// A bare media content object stands for a step of its own.
		appendPart(step)
	}
	if len(contentItems) == 0 {
		return
	}
	if !userContent {
		run.end()
	}
	msgDoc := ojson.NewDoc([]byte(`{"role":"","content":[]}`))
	_ = msgDoc.Set("role", role)
	_ = msgDoc.SetRawBytes("content", translatorcommon.JoinRawArray(contentItems))
	accumulator.Append(msgDoc.Bytes())
}

func interactionsContentToClaude(part ojson.Result, role string) []byte {
	partType := part.Get("type").String()
	if partType == "" && part.Get("text").Exists() {
		partType = "text"
	}
	switch partType {
	case "text":
		textPart := []byte(`{"type":"text","text":""}`)
		textPart, _ = ojson.SetBytes(textPart, "text", part.Get("text").String())
		return textPart
	case "thinking", "reasoning":
		if role != "assistant" {
			return nil
		}
		thinkingPart := []byte(`{"type":"thinking","thinking":""}`)
		thinkingPart, _ = ojson.SetBytes(thinkingPart, "thinking", interactionsClaudeText(part))
		return thinkingPart
	case "image":
		imagePart, _ := interactionsClaudeMediaPart(part, "image")
		return imagePart
	case "document", "file":
		documentPart, _ := interactionsClaudeMediaPart(part, "document")
		return documentPart
	default:
		if text := interactionsClaudeText(part); text != "" {
			textPart := []byte(`{"type":"text","text":""}`)
			textPart, _ = ojson.SetBytes(textPart, "text", text)
			return textPart
		}
		// A user attachment Claude cannot carry is never replaced by placeholder text;
		// the caller records it as dropped. Assistant and tool result content only
		// echoes earlier output, so it keeps the placeholder.
		if role != "user" && (part.Get("data").String() != "" || part.Get("file_data").String() != "") {
			textPart := []byte(`{"type":"text","text":""}`)
			textPart, _ = ojson.SetBytes(textPart, "text", fmt.Sprintf("[%s content omitted]", partType))
			return textPart
		}
	}
	return nil
}

func appendInteractionsFunctionCallToClaude(accumulator *translatorcommon.ClaudeMessageAccumulator, step ojson.Result, run *interactionsClaudeUserRun) {
	toolUseDoc := ojson.NewDoc([]byte(`{"type":"tool_use","id":"","name":"","input":{}}`))
	_ = toolUseDoc.Set("id", interactionsClaudeToolID(step))
	_ = toolUseDoc.Set("name", util.SanitizeClaudeFunctionName(step.Get("name").String()))
	args := step.Get("arguments")
	if !args.Exists() {
		args = step.Get("args")
	}
	if args.Exists() && args.IsObject() {
		_ = toolUseDoc.SetRaw("input", args.Raw)
	}
	msg := []byte(`{"role":"assistant","content":[]}`)
	msg, _ = ojson.SetRawBytes(msg, "content", translatorcommon.JoinRawArray([][]byte{toolUseDoc.Bytes()}))
	run.end()
	accumulator.Append(msg)
}

func appendInteractionsFunctionResultToClaude(accumulator *translatorcommon.ClaudeMessageAccumulator, step ojson.Result, run *interactionsClaudeUserRun) {
	toolResultDoc := ojson.NewDoc([]byte(`{"type":"tool_result","tool_use_id":"","content":""}`))
	_ = toolResultDoc.Set("tool_use_id", interactionsClaudeToolID(step))
	if isError := step.Get("is_error"); isError.Exists() && isError.Bool() {
		_ = toolResultDoc.Set("is_error", true)
	}
	result := step.Get("result")
	if !result.Exists() {
		result = step.Get("output")
	}
	switch {
	case result.IsArray():
		contentItems := make([][]byte, 0, 4)
		result.ForEach(func(_, part ojson.Result) bool {
			if converted := interactionsContentToClaude(part, "tool_result"); len(converted) > 0 {
				contentItems = append(contentItems, converted)
			}
			return true
		})
		_ = toolResultDoc.SetRawBytes("content", translatorcommon.JoinRawArray(contentItems))
	case result.Exists() && result.Raw != "":
		_ = toolResultDoc.Set("content", result.Raw)
	default:
		_ = toolResultDoc.Set("content", "")
	}
	msg := []byte(`{"role":"user","content":[]}`)
	msg, _ = ojson.SetRawBytes(msg, "content", translatorcommon.JoinRawArray([][]byte{toolResultDoc.Bytes()}))
	// A tool result is content the model reads, so it keeps the surrounding user turn.
	run.add()
	accumulator.Append(msg)
}

func copyInteractionsToolsToClaudeDoc(outDoc *ojson.Doc, root ojson.Result) {
	tools := root.Get("tools")
	if !tools.Exists() || !tools.IsArray() {
		return
	}
	var toolItems [][]byte
	tools.ForEach(func(_, tool ojson.Result) bool {
		if tool.Get("function_declarations").IsArray() {
			tool.Get("function_declarations").ForEach(func(_, decl ojson.Result) bool {
				if converted := interactionsClaudeTool(decl); len(converted) > 0 {
					toolItems = append(toolItems, converted)
				}
				return true
			})
			return true
		}
		if tool.Get("functionDeclarations").IsArray() {
			tool.Get("functionDeclarations").ForEach(func(_, decl ojson.Result) bool {
				if converted := interactionsClaudeTool(decl); len(converted) > 0 {
					toolItems = append(toolItems, converted)
				}
				return true
			})
			return true
		}
		if converted := interactionsClaudeTool(tool); len(converted) > 0 {
			toolItems = append(toolItems, converted)
		}
		return true
	})
	if len(toolItems) > 0 {
		_ = outDoc.SetRawBytes("tools", translatorcommon.JoinRawArray(toolItems))
	}
}

func interactionsClaudeTool(tool ojson.Result) []byte {
	name := tool.Get("name").String()
	if name == "" {
		name = tool.Get("function.name").String()
	}
	if name == "" {
		return nil
	}
	convertedDoc := ojson.NewDoc([]byte(`{"name":"","input_schema":{"type":"object","properties":{}}}`))
	_ = convertedDoc.Set("name", util.SanitizeClaudeFunctionName(name))
	if desc := tool.Get("description"); desc.Exists() {
		_ = convertedDoc.Set("description", desc.String())
	} else if desc := tool.Get("function.description"); desc.Exists() {
		_ = convertedDoc.Set("description", desc.String())
	}
	params := firstClaudeInteractionsExisting(tool, "parameters", "parametersJsonSchema", "parameters_json_schema", "input_schema")
	if params.Exists() && params.IsObject() {
		_ = convertedDoc.SetRawBytes("input_schema", util.NormalizeClaudeToolInputSchema([]byte(params.Raw)))
	}
	return convertedDoc.Bytes()
}

func copyInteractionsToolChoiceToClaudeDoc(outDoc *ojson.Doc, toolChoice ojson.Result) {
	if !toolChoice.Exists() {
		return
	}
	switch toolChoice.Type {
	case ojson.String:
		switch strings.ToLower(strings.TrimSpace(toolChoice.String())) {
		case "auto":
			_ = outDoc.SetRaw("tool_choice", `{"type":"auto"}`)
		case "required", "any":
			_ = outDoc.SetRaw("tool_choice", `{"type":"any"}`)
		}
	case ojson.JSON:
		toolType := strings.ToLower(strings.TrimSpace(toolChoice.Get("type").String()))
		switch toolType {
		case "auto":
			_ = outDoc.SetRaw("tool_choice", `{"type":"auto"}`)
		case "required", "any":
			_ = outDoc.SetRaw("tool_choice", `{"type":"any"}`)
		case "function", "tool":
			name := toolChoice.Get("name").String()
			if name == "" {
				name = toolChoice.Get("function.name").String()
			}
			if name != "" {
				choice := []byte(`{"type":"tool","name":""}`)
				choice, _ = ojson.SetBytes(choice, "name", util.SanitizeClaudeFunctionName(name))
				_ = outDoc.SetRawBytes("tool_choice", choice)
			}
		}
	}
}

func interactionsClaudeToolID(step ojson.Result) string {
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

func interactionsClaudeText(value ojson.Result) string {
	if !value.Exists() {
		return ""
	}
	if value.Type == ojson.String {
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
		parts.ForEach(func(_, part ojson.Result) bool {
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

// interactionsClaudeMediaPart maps an Interactions image or document onto a Claude
// block. Inline bytes become a base64 source and an http(s) uri becomes a url
// source. It reports false when the part carries neither.
func interactionsClaudeMediaPart(part ojson.Result, claudeType string) ([]byte, bool) {
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
	if mimeType != "" && data != "" {
		outDoc := ojson.NewDoc([]byte(`{"type":"","source":{"type":"base64","media_type":"","data":""}}`))
		_ = outDoc.Set("type", claudeType)
		_ = outDoc.Set("source.media_type", mimeType)
		_ = outDoc.Set("source.data", data)
		return outDoc.Bytes(), true
	}
	for _, path := range []string{"uri", "file_uri", "fileUri", "url"} {
		if uri := strings.TrimSpace(part.Get(path).String()); translatorcommon.IsHTTPURL(uri) {
			outDoc := ojson.NewDoc([]byte(`{"type":"","source":{"type":"url","url":""}}`))
			_ = outDoc.Set("type", claudeType)
			_ = outDoc.Set("source.url", uri)
			return outDoc.Bytes(), true
		}
	}
	return nil, false
}

// interactionsClaudeDroppedMedia names the media type of a part that
// interactionsContentToClaude left out, or returns "" when the part is not media.
func interactionsClaudeDroppedMedia(part ojson.Result) string {
	switch partType := part.Get("type").String(); partType {
	case "image", "audio", "video", "document":
		return partType
	case "file":
		return "document"
	}
	return ""
}

// interactionsClaudeDroppedPart names the type of a user part that
// interactionsContentToClaude left out: media, or any other part that carries
// bytes. It returns "" when the part holds nothing to report.
func interactionsClaudeDroppedPart(part ojson.Result) string {
	if mediaType := interactionsClaudeDroppedMedia(part); mediaType != "" {
		return mediaType
	}
	if part.Get("data").String() != "" || part.Get("file_data").String() != "" {
		return translatorcommon.InteractionsAttachmentType(part)
	}
	return ""
}

// interactionsClaudePartIsSendable reports whether a converted Claude block gives
// the model something to read. Blank text does not.
func interactionsClaudePartIsSendable(converted []byte) bool {
	block := util.ParseGJSONBytesNoCopy(converted)
	return block.Get("type").String() != "text" || strings.TrimSpace(block.Get("text").String()) != ""
}

func firstClaudeInteractionsExisting(root ojson.Result, paths ...string) ojson.Result {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value
		}
	}
	return ojson.Result{}
}
