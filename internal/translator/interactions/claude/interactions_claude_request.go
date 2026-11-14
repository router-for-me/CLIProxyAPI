package claude

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

func ConvertClaudeRequestToInteractions(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertClaudeRequestToInteractions(modelName, inputRawJSON, stream, false)

}

// ConvertClaudeRequestToInteractionsWithCompat preserves empty assistant
// thinking blocks for configured compatibility endpoints.
func ConvertClaudeRequestToInteractionsWithCompat(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertClaudeRequestToInteractions(modelName, inputRawJSON, stream, true)
}

func convertClaudeRequestToInteractions(modelName string, inputRawJSON []byte, stream, preserveEmptyThinkingBlocks bool) ([]byte, error) {
	// The internal commentary marker must be restored as assistant text before
	// any thinking handling, so it is never dropped or mapped to thought.
	restoredRawJSON, _ := sigcompat.RestoreCodexCommentaryMarkers(inputRawJSON)
	root := util.ParseGJSONBytesNoCopy(restoredRawJSON)
	outDoc := ojson.NewDocString(`{"model":"","input":[]}`)
	_ = outDoc.Set("model", firstNonEmpty(modelName, root.Get("model").String()))
	if streamValue, ok := claudeRequestStreamValue(root, stream); ok {
		_ = outDoc.Set("stream", streamValue)
	}
	copyClaudeSystemToInteractionsDoc(outDoc, root)
	copyClaudeGenerationConfigToInteractionsDoc(outDoc, root)
	errDrops := appendClaudeMessagesToInteractionsDoc(outDoc, root.Get("messages"), preserveEmptyThinkingBlocks)
	copyClaudeToolsToInteractionsDoc(outDoc, root)
	return outDoc.Bytes(), errDrops
}

func claudeRequestStreamValue(root ojson.Result, stream bool) (bool, bool) {
	if value := root.Get("stream"); value.Exists() {
		return value.Bool(), true
	}
	if stream {
		return true, true
	}
	return false, false
}

func copyClaudeSystemToInteractionsDoc(outDoc *ojson.Doc, root ojson.Result) {
	text := claudeText(root.Get("system"))
	if text == "" {
		return
	}
	_ = outDoc.Set("system_instruction", text)
}

func copyClaudeGenerationConfigToInteractionsDoc(outDoc *ojson.Doc, root ojson.Result) {
	copyClaudeJSONFieldDoc(outDoc, root, "max_tokens", "generation_config.max_output_tokens")
	copyClaudeJSONFieldDoc(outDoc, root, "temperature", "generation_config.temperature")
	copyClaudeJSONFieldDoc(outDoc, root, "top_p", "generation_config.top_p")
	copyClaudeJSONFieldDoc(outDoc, root, "stop_sequences", "generation_config.stop_sequences")
	copyClaudeThinkingToInteractionsDoc(outDoc, root)
	copyClaudeToolChoiceToInteractionsDoc(outDoc, root.Get("tool_choice"))
}

func copyClaudeJSONFieldDoc(outDoc *ojson.Doc, root ojson.Result, from, to string) {
	value := root.Get(from)
	if !value.Exists() {
		return
	}
	_ = outDoc.SetRaw(to, value.Raw)
}

func copyClaudeThinkingToInteractionsDoc(outDoc *ojson.Doc, root ojson.Result) {
	thinking := root.Get("thinking")
	if thinking.Exists() {
		switch strings.ToLower(strings.TrimSpace(thinking.Get("type").String())) {
		case "disabled":
			_ = outDoc.Set("generation_config.thinking_level", "none")
		case "enabled":
			if budget := thinking.Get("budget_tokens"); budget.Exists() {
				_ = outDoc.SetRaw("generation_config.thinking_config.thinking_budget", budget.Raw)
			} else {
				_ = outDoc.Set("generation_config.thinking_level", "high")
			}
		case "adaptive":
			_ = outDoc.Set("generation_config.thinking_level", "auto")
		}
	}
	if effort := root.Get("output_config.effort"); effort.Exists() && effort.Type == ojson.String {
		_ = outDoc.Set("generation_config.thinking_level", strings.ToLower(strings.TrimSpace(effort.String())))
	}
}

func copyClaudeToolChoiceToInteractionsDoc(outDoc *ojson.Doc, toolChoice ojson.Result) {
	if !toolChoice.Exists() {
		return
	}
	switch toolChoice.Type {
	case ojson.String:
		switch strings.ToLower(strings.TrimSpace(toolChoice.String())) {
		case "auto":
			_ = outDoc.Set("generation_config.tool_choice", "auto")
		case "any", "required":
			_ = outDoc.Set("generation_config.tool_choice", "required")
		}
	case ojson.JSON:
		toolType := strings.ToLower(strings.TrimSpace(toolChoice.Get("type").String()))
		switch toolType {
		case "auto":
			_ = outDoc.Set("generation_config.tool_choice", "auto")
		case "any", "required":
			_ = outDoc.Set("generation_config.tool_choice", "required")
		case "tool":
			name := strings.TrimSpace(toolChoice.Get("name").String())
			if name != "" {
				choice := []byte(`{"type":"function","name":""}`)
				choice, _ = ojson.SetBytes(choice, "name", name)
				_ = outDoc.SetRawBytes("generation_config.tool_choice", choice)
			}
		}
	}
}

func appendClaudeMessagesToInteractionsDoc(outDoc *ojson.Doc, messages ojson.Result, preserveEmptyThinkingBlocks bool) error {
	if !messages.Exists() || !messages.IsArray() {
		return nil
	}
	var drops translatorcommon.UserTurnDrops
	inputItems := translatorcommon.NewRawArrayItems(messages.Get("#").Int())
	var pendingToolUseIDs []string
	var pendingSystemReminders [][]byte
	toolNamesByID := make(map[string]string)

	messages.ForEach(func(_, message ojson.Result) bool {
		role := strings.ToLower(strings.TrimSpace(message.Get("role").String()))
		content := message.Get("content")
		if role == "system" {
			if reminderText, ok := translatorcommon.ClaudeMessageSystemReminderText(content); ok {
				step := []byte(`{"type":"user_input","content":[{"type":"text","text":""}]}`)
				step, _ = ojson.SetBytes(step, "content.0.text", reminderText)
				if len(pendingToolUseIDs) > 0 {
					pendingSystemReminders = append(pendingSystemReminders, step)
				} else {
					inputItems = append(inputItems, step)
				}
			}
			return true
		}

		if role == "user" && len(pendingToolUseIDs) > 0 && content.IsArray() {
			content = translatorcommon.AlignClaudeToolResults(content, pendingToolUseIDs)
		}
		pendingToolUseIDs = nil

		sendable := appendClaudeMessageToInteractions(&inputItems, &pendingToolUseIDs, &pendingSystemReminders, toolNamesByID, role, content, preserveEmptyThinkingBlocks, &drops)
		if role == "user" {
			drops.EndTurn(sendable)
		}
		if len(pendingSystemReminders) > 0 {
			inputItems = append(inputItems, pendingSystemReminders...)
			pendingSystemReminders = nil
		}
		return true
	})
	if len(pendingSystemReminders) > 0 {
		inputItems = append(inputItems, pendingSystemReminders...)
	}
	translatorcommon.SetRawArrayItemsDoc(outDoc, "input", inputItems)
	return drops.Err()
}

func appendClaudeMessageToInteractions(items *[][]byte, pendingToolUseIDs *[]string, pendingSystemReminders *[][]byte, toolNamesByID map[string]string, role string, content ojson.Result, preserveEmptyThinkingBlocks bool, drops *translatorcommon.UserTurnDrops) int {
	defaultStepType := "user_input"
	if role == "assistant" {
		defaultStepType = "model_output"
	}
	if content.Type == ojson.String {
		if pendingSystemReminders != nil && len(*pendingSystemReminders) > 0 {
			*items = append(*items, *pendingSystemReminders...)
			*pendingSystemReminders = nil
		}
		stepDoc := ojson.NewDocString(`{"type":"","content":[{"type":"text","text":""}]}`)
		_ = stepDoc.Set("type", defaultStepType)
		_ = stepDoc.Set("content.0.text", content.String())
		*items = append(*items, stepDoc.Bytes())
		return 1
	}
	if !content.IsArray() {
		return 0
	}
	// Counts only what this turn itself sends, not system reminders flushed beside it.
	// Whitespace-only text is forwarded but never keeps an emptied turn alive.
	sendable := 0
	stepContent := make([][]byte, 0, 4)
	flushContent := func() {
		if len(stepContent) == 0 {
			return
		}
		stepDoc := ojson.NewDocString(`{"type":"","content":[]}`)
		_ = stepDoc.Set("type", defaultStepType)
		_ = stepDoc.SetRawBytes("content", translatorcommon.JoinRawArray(stepContent))
		*items = append(*items, stepDoc.Bytes())
		stepContent = stepContent[:0]
	}
	content.ForEach(func(_, part ojson.Result) bool {
		partType := strings.ToLower(strings.TrimSpace(part.Get("type").String()))
		switch partType {
		case "text":
			if text := part.Get("text").String(); text != "" {
				if pendingSystemReminders != nil && len(*pendingSystemReminders) > 0 {
					flushContent()
					*items = append(*items, *pendingSystemReminders...)
					*pendingSystemReminders = nil
				}
				contentPart := []byte(`{"type":"text","text":""}`)
				contentPart, _ = ojson.SetBytes(contentPart, "text", text)
				stepContent = append(stepContent, contentPart)
				if strings.TrimSpace(text) != "" {
					sendable++
				}
			}
		case "thinking":
			flushContent()
			text := part.Get("thinking").String()
			if text != "" || preserveEmptyThinkingBlocks {
				step := []byte(`{"type":"thought","content":[{"type":"text","text":""}]}`)
				step, _ = ojson.SetBytes(step, "content.0.text", text)
				*items = append(*items, step)
				sendable++
			}
		case "image", "document", "container_upload":
			if mediaPart, ok := claudeMediaPartToInteractions(part, partType); ok {
				if pendingSystemReminders != nil && len(*pendingSystemReminders) > 0 {
					flushContent()
					*items = append(*items, *pendingSystemReminders...)
					*pendingSystemReminders = nil
				}
				stepContent = append(stepContent, mediaPart)
				sendable++
			} else if drops != nil && role == "user" {
				drops.Drop(partType)
			}
		case "tool_use":
			flushContent()
			if id := part.Get("id").String(); id != "" {
				if pendingToolUseIDs != nil {
					*pendingToolUseIDs = append(*pendingToolUseIDs, id)
				}
				if toolNamesByID != nil {
					if name := part.Get("name").String(); name != "" {
						toolNamesByID[id] = name
					}
				}
			}
			*items = append(*items, claudeToolUseToInteractions(part))
			sendable++
		case "tool_result":
			flushContent()
			*items = append(*items, claudeToolResultToInteractions(part, toolNamesByID))
			sendable++
		}
		return true
	})
	flushContent()
	return sendable
}

func claudeMediaPartToInteractions(part ojson.Result, partType string) ([]byte, bool) {
	source := part.Get("source")
	mimeType := source.Get("media_type").String()
	data := source.Get("data").String()
	if data == "" {
		data = part.Get("data").String()
	}
	if mimeType == "" {
		mimeType = part.Get("mime_type").String()
	}
	if mimeType == "" || data == "" {
		return nil, false
	}
	outDoc := ojson.NewDocString(`{"type":"","mime_type":"","data":""}`)
	_ = outDoc.Set("type", partType)
	_ = outDoc.Set("mime_type", mimeType)
	_ = outDoc.Set("data", data)
	return outDoc.Bytes(), true
}

func claudeToolUseToInteractions(part ojson.Result) []byte {
	stepDoc := ojson.NewDocString(`{"type":"function_call","name":"","arguments":{}}`)
	_ = stepDoc.Set("name", part.Get("name").String())
	if id := part.Get("id").String(); id != "" {
		_ = stepDoc.Set("id", id)
	}
	input := part.Get("input")
	if input.Exists() && input.IsObject() {
		_ = stepDoc.SetRaw("arguments", input.Raw)
	}
	return stepDoc.Bytes()
}

func claudeToolResultToInteractions(part ojson.Result, toolNamesByID map[string]string) []byte {
	stepDoc := ojson.NewDocString(`{"type":"function_result","call_id":"","result":""}`)
	id := part.Get("tool_use_id").String()
	if id != "" {
		_ = stepDoc.Set("call_id", id)
	}
	name := part.Get("name").String()
	if name == "" && id != "" && toolNamesByID != nil {
		name = toolNamesByID[id]
	}
	if name != "" {
		_ = stepDoc.Set("name", name)
	}
	if isError := part.Get("is_error"); isError.Exists() && isError.Bool() {
		_ = stepDoc.Set("is_error", true)
	}
	result := part.Get("content")
	if result.Exists() {
		switch {
		case result.Type == ojson.String:
			_ = stepDoc.Set("result", result.String())
		case result.IsArray():
			contentItems := make([][]byte, 0, 4)
			result.ForEach(func(_, item ojson.Result) bool {
				itemType := item.Get("type").String()
				switch itemType {
				case "text":
					isPureText := true
					if item.IsObject() {
						item.ForEach(func(k, _ ojson.Result) bool {
							key := k.String()
							if key != "type" && key != "text" && key != "cache_control" {
								isPureText = false
								return false
							}
							return true
						})
					}
					if isPureText {
						contentPart := []byte(`{"type":"text","text":""}`)
						contentPart, _ = ojson.SetBytes(contentPart, "text", item.Get("text").String())
						contentItems = append(contentItems, contentPart)
					} else if raw := strings.TrimSpace(item.Raw); raw != "" {
						contentItems = append(contentItems, []byte(raw))
					}
				case "image", "document", "container_upload":
					if mediaPart, ok := claudeMediaPartToInteractions(item, itemType); ok {
						contentItems = append(contentItems, mediaPart)
					} else if raw := strings.TrimSpace(item.Raw); raw != "" {
						contentItems = append(contentItems, []byte(raw))
					}
				default:
					if raw := strings.TrimSpace(item.Raw); raw != "" {
						contentItems = append(contentItems, []byte(raw))
					}
				}
				return true
			})
			_ = stepDoc.SetRawBytes("result", translatorcommon.JoinRawArray(contentItems))
		default:
			_ = stepDoc.SetRaw("result", result.Raw)
		}
	}
	return stepDoc.Bytes()
}

func copyClaudeToolsToInteractionsDoc(outDoc *ojson.Doc, root ojson.Result) {
	tools := root.Get("tools")
	if !tools.Exists() || !tools.IsArray() {
		return
	}
	var toolItems [][]byte
	tools.ForEach(func(_, tool ojson.Result) bool {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			return true
		}
		itemDoc := ojson.NewDocString(`{"type":"function","name":"","parameters":{}}`)
		_ = itemDoc.Set("name", name)
		if desc := tool.Get("description"); desc.Exists() {
			_ = itemDoc.Set("description", desc.String())
		}
		if schema := tool.Get("input_schema"); schema.Exists() && schema.IsObject() {
			_ = itemDoc.SetRaw("parameters", schema.Raw)
		}
		toolItems = append(toolItems, itemDoc.Bytes())
		return true
	})
	if len(toolItems) > 0 {
		_ = outDoc.SetRawBytes("tools", translatorcommon.JoinRawArray(toolItems))
	}
}

func claudeText(value ojson.Result) string {
	if !value.Exists() {
		return ""
	}
	if value.Type == ojson.String {
		return value.String()
	}
	if text := value.Get("text"); text.Exists() {
		return text.String()
	}
	if value.IsArray() {
		var builder strings.Builder
		value.ForEach(func(_, item ojson.Result) bool {
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
