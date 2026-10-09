package interactions

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertInteractionsRequestToClaude(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertInteractionsRequestToClaude(modelName, inputRawJSON, stream)

}

// convertInteractionsRequestToClaude also reports a user turn that was left empty
// because its only media part has no Claude equivalent.
func convertInteractionsRequestToClaude(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	var run interactionsClaudeUserRun
	root := gjson.ParseBytes(inputRawJSON)
	out := []byte(`{"model":"","max_tokens":32000,"messages":[]}`)
	out, _ = sjson.SetBytes(out, "model", modelName)
	if stream || root.Get("stream").Bool() {
		out, _ = sjson.SetBytes(out, "stream", true)
	}
	out = copyInteractionsSystemToClaude(out, root)
	out = copyInteractionsGenerationConfigToClaude(out, root)
	messageAccumulator := translatorcommon.NewClaudeMessageAccumulator(int(root.Get("input.#").Int()))
	appendInteractionsInputToClaudeMessages(messageAccumulator, root.Get("input"), &run)
	run.end()
	out = translatorcommon.SetRawArrayItems(out, "messages", messageAccumulator.Messages())
	out = copyInteractionsToolsToClaude(out, root)
	return out, run.drops.Err()
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

func appendInteractionsInputToClaudeMessages(accumulator *translatorcommon.ClaudeMessageAccumulator, input gjson.Result, run *interactionsClaudeUserRun) {
	if !input.Exists() {
		return
	}
	if input.Type == gjson.String {
		step := []byte(`{"type":"user_input","content":[{"type":"text","text":""}]}`)
		step, _ = sjson.SetBytes(step, "content.0.text", input.String())
		appendInteractionsStepToClaude(accumulator, gjson.ParseBytes(step), "user", false, run)
		return
	}
	if input.IsObject() {
		appendInteractionsInputItemToClaude(accumulator, input, run)
		return
	}
	input.ForEach(func(_, step gjson.Result) bool {
		appendInteractionsInputItemToClaude(accumulator, step, run)
		return true
	})
}

func appendInteractionsInputItemToClaude(accumulator *translatorcommon.ClaudeMessageAccumulator, step gjson.Result, run *interactionsClaudeUserRun) {
	if step.Get("steps").IsArray() {
		defaultRole := "user"
		if role := step.Get("role").String(); role == "model" || role == "assistant" {
			defaultRole = "assistant"
		}
		instruction := translatorcommon.IsInteractionsInstructionStep(step, false)
		step.Get("steps").ForEach(func(_, nestedStep gjson.Result) bool {
			appendInteractionsStepToClaude(accumulator, nestedStep, defaultRole, instruction, run)
			return true
		})
		return
	}
	if step.Get("parts").Exists() {
		wrapped := []byte(`{"type":"user_input","content":[]}`)
		if role := step.Get("role").String(); role == "model" || role == "assistant" {
			wrapped, _ = sjson.SetBytes(wrapped, "type", "model_output")
		}
		wrapped, _ = sjson.SetRawBytes(wrapped, "content", []byte(step.Get("parts").Raw))
		appendInteractionsStepToClaude(accumulator, gjson.ParseBytes(wrapped), "user", translatorcommon.IsInteractionsInstructionStep(step, false), run)
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
func appendInteractionsStepToClaude(accumulator *translatorcommon.ClaudeMessageAccumulator, step gjson.Result, defaultRole string, instruction bool, run *interactionsClaudeUserRun) {
	role := defaultRole
	if stepRole := step.Get("role").String(); stepRole == "user" || stepRole == "assistant" {
		role = stepRole
	}
	userContent := role == "user" && !translatorcommon.IsInteractionsInstructionStep(step, instruction)
	contentItems := make([][]byte, 0, 4)
	appendPart := func(part gjson.Result) {
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
	if stepContent.Type == gjson.String {
		part := []byte(`{"type":"text","text":""}`)
		part, _ = sjson.SetBytes(part, "text", stepContent.String())
		appendPart(gjson.ParseBytes(part))
	} else if stepContent.IsArray() {
		stepContent.ForEach(func(_, part gjson.Result) bool {
			appendPart(part)
			return true
		})
	} else if text := step.Get("text"); text.Exists() {
		part := []byte(`{"type":"text","text":""}`)
		part, _ = sjson.SetBytes(part, "text", text.String())
		appendPart(gjson.ParseBytes(part))
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
	msg := []byte(`{"role":"","content":[]}`)
	msg, _ = sjson.SetBytes(msg, "role", role)
	msg, _ = sjson.SetRawBytes(msg, "content", translatorcommon.JoinRawArray(contentItems))
	accumulator.Append(msg)
}

func interactionsContentToClaude(part gjson.Result, role string) []byte {
	partType := part.Get("type").String()
	if partType == "" && part.Get("text").Exists() {
		partType = "text"
	}
	switch partType {
	case "text":
		textPart := []byte(`{"type":"text","text":""}`)
		textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
		return textPart
	case "thinking", "reasoning":
		if role != "assistant" {
			return nil
		}
		thinkingPart := []byte(`{"type":"thinking","thinking":""}`)
		thinkingPart, _ = sjson.SetBytes(thinkingPart, "thinking", interactionsClaudeText(part))
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
			textPart, _ = sjson.SetBytes(textPart, "text", text)
			return textPart
		}
		// A user attachment Claude cannot carry is never replaced by placeholder text;
		// the caller records it as dropped. Assistant and tool result content only
		// echoes earlier output, so it keeps the placeholder.
		if role != "user" && (part.Get("data").String() != "" || part.Get("file_data").String() != "") {
			textPart := []byte(`{"type":"text","text":""}`)
			textPart, _ = sjson.SetBytes(textPart, "text", fmt.Sprintf("[%s content omitted]", partType))
			return textPart
		}
	}
	return nil
}

func appendInteractionsFunctionCallToClaude(accumulator *translatorcommon.ClaudeMessageAccumulator, step gjson.Result, run *interactionsClaudeUserRun) {
	toolUse := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
	toolUse, _ = sjson.SetBytes(toolUse, "id", interactionsClaudeToolID(step))
	toolUse, _ = sjson.SetBytes(toolUse, "name", util.SanitizeClaudeFunctionName(step.Get("name").String()))
	args := step.Get("arguments")
	if !args.Exists() {
		args = step.Get("args")
	}
	if args.Exists() && args.IsObject() {
		toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(args.Raw))
	}
	msg := []byte(`{"role":"assistant","content":[]}`)
	msg, _ = sjson.SetRawBytes(msg, "content", translatorcommon.JoinRawArray([][]byte{toolUse}))
	run.end()
	accumulator.Append(msg)
}

func appendInteractionsFunctionResultToClaude(accumulator *translatorcommon.ClaudeMessageAccumulator, step gjson.Result, run *interactionsClaudeUserRun) {
	toolResult := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)
	toolResult, _ = sjson.SetBytes(toolResult, "tool_use_id", interactionsClaudeToolID(step))
	if isError := step.Get("is_error"); isError.Exists() && isError.Bool() {
		toolResult, _ = sjson.SetBytes(toolResult, "is_error", true)
	}
	result := step.Get("result")
	if !result.Exists() {
		result = step.Get("output")
	}
	switch {
	case result.IsArray():
		contentItems := make([][]byte, 0, 4)
		result.ForEach(func(_, part gjson.Result) bool {
			if converted := interactionsContentToClaude(part, "tool_result"); len(converted) > 0 {
				contentItems = append(contentItems, converted)
			}
			return true
		})
		toolResult, _ = sjson.SetRawBytes(toolResult, "content", translatorcommon.JoinRawArray(contentItems))
	case result.Exists() && result.Raw != "":
		toolResult, _ = sjson.SetBytes(toolResult, "content", result.Raw)
	default:
		toolResult, _ = sjson.SetBytes(toolResult, "content", "")
	}
	msg := []byte(`{"role":"user","content":[]}`)
	msg, _ = sjson.SetRawBytes(msg, "content", translatorcommon.JoinRawArray([][]byte{toolResult}))
	// A tool result is content the model reads, so it keeps the surrounding user turn.
	run.add()
	accumulator.Append(msg)
}

func copyInteractionsToolsToClaude(out []byte, root gjson.Result) []byte {
	tools := root.Get("tools")
	if !tools.Exists() || !tools.IsArray() {
		return out
	}
	var toolItems [][]byte
	tools.ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("function_declarations").IsArray() {
			tool.Get("function_declarations").ForEach(func(_, decl gjson.Result) bool {
				if converted := interactionsClaudeTool(decl); len(converted) > 0 {
					toolItems = append(toolItems, converted)
				}
				return true
			})
			return true
		}
		if tool.Get("functionDeclarations").IsArray() {
			tool.Get("functionDeclarations").ForEach(func(_, decl gjson.Result) bool {
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
		out, _ = sjson.SetRawBytes(out, "tools", translatorcommon.JoinRawArray(toolItems))
	}
	return out
}

func interactionsClaudeTool(tool gjson.Result) []byte {
	name := tool.Get("name").String()
	if name == "" {
		name = tool.Get("function.name").String()
	}
	if name == "" {
		return nil
	}
	converted := []byte(`{"name":"","input_schema":{"type":"object","properties":{}}}`)
	converted, _ = sjson.SetBytes(converted, "name", util.SanitizeClaudeFunctionName(name))
	if desc := tool.Get("description"); desc.Exists() {
		converted, _ = sjson.SetBytes(converted, "description", desc.String())
	} else if desc := tool.Get("function.description"); desc.Exists() {
		converted, _ = sjson.SetBytes(converted, "description", desc.String())
	}
	params := firstClaudeInteractionsExisting(tool, "parameters", "parametersJsonSchema", "parameters_json_schema", "input_schema")
	if params.Exists() && params.IsObject() {
		converted, _ = sjson.SetRawBytes(converted, "input_schema", util.NormalizeClaudeToolInputSchema([]byte(params.Raw)))
	}
	return converted
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
				choice, _ = sjson.SetBytes(choice, "name", util.SanitizeClaudeFunctionName(name))
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

// interactionsClaudeMediaPart maps an Interactions image or document onto a Claude
// block. Inline bytes become a base64 source and an http(s) uri becomes a url
// source. It reports false when the part carries neither.
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
	if mimeType != "" && data != "" {
		out := []byte(`{"type":"","source":{"type":"base64","media_type":"","data":""}}`)
		out, _ = sjson.SetBytes(out, "type", claudeType)
		out, _ = sjson.SetBytes(out, "source.media_type", mimeType)
		out, _ = sjson.SetBytes(out, "source.data", data)
		return out, true
	}
	for _, path := range []string{"uri", "file_uri", "fileUri", "url"} {
		if uri := strings.TrimSpace(part.Get(path).String()); translatorcommon.IsHTTPURL(uri) {
			out := []byte(`{"type":"","source":{"type":"url","url":""}}`)
			out, _ = sjson.SetBytes(out, "type", claudeType)
			out, _ = sjson.SetBytes(out, "source.url", uri)
			return out, true
		}
	}
	return nil, false
}

// interactionsClaudeDroppedMedia names the media type of a part that
// interactionsContentToClaude left out, or returns "" when the part is not media.
func interactionsClaudeDroppedMedia(part gjson.Result) string {
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
func interactionsClaudeDroppedPart(part gjson.Result) string {
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
	block := gjson.ParseBytes(converted)
	return block.Get("type").String() != "text" || strings.TrimSpace(block.Get("text").String()) != ""
}

func firstClaudeInteractionsExisting(root gjson.Result, paths ...string) gjson.Result {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}
