package claude

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	claudeinput "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/input"
	"github.com/tidwall/gjson"
)

type claudeCodexValidationState struct {
	thinkingTurns int
	toolNames     map[string]string
	toolResults   []string
}

// ValidateClaudeRequestForCodex rejects request features that the Codex
// translation would otherwise silently discard.
func ValidateClaudeRequestForCodex(inputRawJSON []byte) error {
	request := claudeinput.Parse("", inputRawJSON)
	if err := request.Validate(); err != nil {
		return err
	}
	root := request.Root()
	if err := validateClaudeCodexControls(root); err != nil {
		return err
	}
	state, err := validateClaudeCodexMessages(root.Get("messages"))
	if err != nil {
		return err
	}
	return validateClaudeCodexContextManagement(root.Get("context_management"), state)
}

func validateClaudeCodexControls(root gjson.Result) error {
	if stopSequences := root.Get("stop_sequences"); stopSequences.Exists() && stopSequences.Type != gjson.Null {
		if !stopSequences.IsArray() {
			return fmt.Errorf("stop_sequences must be an array")
		}
		if len(stopSequences.Array()) > 0 {
			return fmt.Errorf("stop_sequences are not supported by the Codex Responses upstream")
		}
	}
	if root.Get("top_k").Exists() {
		return fmt.Errorf("top_k is not supported by the Codex Responses upstream")
	}
	if outputConfig := root.Get("output_config"); outputConfig.Exists() && outputConfig.Type != gjson.Null {
		if !outputConfig.IsObject() {
			return fmt.Errorf("output_config must be an object")
		}
		if format := outputConfig.Get("format"); format.Exists() && format.Type != gjson.Null {
			if !format.IsObject() || format.Get("type").String() != "json_schema" || !format.Get("schema").IsObject() {
				return fmt.Errorf("output_config.format must be a json_schema object with an object schema")
			}
			if field := firstUnsupportedClaudeCodexField(format, "type", "schema"); field != "" {
				return fmt.Errorf("unsupported output_config.format field: %s", boundedClaudeCodexLabel(field))
			}
		}
	}
	return nil
}

func validateClaudeCodexMessages(messages gjson.Result) (claudeCodexValidationState, error) {
	state := claudeCodexValidationState{toolNames: make(map[string]string)}
	if !messages.IsArray() || len(messages.Array()) == 0 {
		return state, fmt.Errorf("messages must be a non-empty array")
	}
	for _, message := range messages.Array() {
		if !message.IsObject() {
			return state, fmt.Errorf("each message must be an object")
		}
		role := message.Get("role").String()
		if role != "user" && role != "assistant" && role != "system" {
			return state, fmt.Errorf("message role must be user, assistant, or system")
		}
		if role == "system" {
			continue
		}
		content := message.Get("content")
		if content.Type == gjson.String {
			continue
		}
		if !content.IsArray() || len(content.Array()) == 0 {
			return state, fmt.Errorf("message content must be text or a non-empty array")
		}
		turnHasThinking := false
		for _, block := range content.Array() {
			if !block.IsObject() {
				return state, fmt.Errorf("message content blocks must be objects")
			}
			blockType := block.Get("type").String()
			switch blockType {
			case "text":
				if block.Get("text").Type != gjson.String {
					return state, fmt.Errorf("text block text must be a string")
				}
			case "image":
				if role != "user" {
					return state, fmt.Errorf("image blocks require a user message")
				}
				if err := validateClaudeCodexBase64Source(block.Get("source"), "image", false); err != nil {
					return state, err
				}
			case "document":
				if role != "user" {
					return state, fmt.Errorf("document blocks require a user message")
				}
				if err := validateClaudeCodexBase64Source(block.Get("source"), "document", true); err != nil {
					return state, err
				}
			case "thinking":
				if role != "assistant" {
					return state, fmt.Errorf("thinking blocks require an assistant message")
				}
				turnHasThinking = true
			case "tool_use":
				if role != "assistant" {
					return state, fmt.Errorf("tool_use blocks require an assistant message")
				}
				id, err := requiredClaudeCodexString(block.Get("id"), "tool_use id")
				if err != nil {
					return state, err
				}
				name, err := requiredClaudeCodexString(block.Get("name"), "tool_use name")
				if err != nil {
					return state, err
				}
				if input := block.Get("input"); !input.IsObject() {
					return state, fmt.Errorf("tool_use input must be an object")
				}
				state.toolNames[id] = name
			case "tool_result":
				if role != "user" {
					return state, fmt.Errorf("tool_result blocks require a user message")
				}
				id, err := requiredClaudeCodexString(block.Get("tool_use_id"), "tool_result tool_use_id")
				if err != nil {
					return state, err
				}
				if err := validateClaudeCodexToolResult(block.Get("content")); err != nil {
					return state, err
				}
				state.toolResults = append(state.toolResults, id)
			default:
				return state, fmt.Errorf("unsupported Claude content block: %s", boundedClaudeCodexLabel(blockType))
			}
		}
		if turnHasThinking {
			state.thinkingTurns++
		}
	}
	return state, nil
}

func validateClaudeCodexBase64Source(source gjson.Result, label string, requirePDF bool) error {
	if !source.IsObject() {
		return fmt.Errorf("%s source must be a base64 object", label)
	}
	if sourceType := source.Get("type"); sourceType.Exists() && sourceType.String() != "base64" {
		return fmt.Errorf("unsupported %s source type: %s", label, boundedClaudeCodexLabel(sourceType.String()))
	}
	if requirePDF && source.Get("media_type").String() != "application/pdf" {
		return fmt.Errorf("document media type must be application/pdf")
	}
	data := source.Get("data")
	if data.Type != gjson.String || data.String() == "" {
		data = source.Get("base64")
	}
	if data.Type != gjson.String || data.String() == "" {
		return fmt.Errorf("%s data must be a non-empty base64 string", label)
	}
	return nil
}

func validateClaudeCodexToolResult(content gjson.Result) error {
	if content.Type == gjson.String {
		return nil
	}
	if !content.IsArray() {
		return fmt.Errorf("tool_result content must be text or an array")
	}
	for _, block := range content.Array() {
		if !block.IsObject() {
			return fmt.Errorf("tool_result content blocks must be objects")
		}
		switch block.Get("type").String() {
		case "text":
			if block.Get("text").Type != gjson.String {
				return fmt.Errorf("tool_result text must be a string")
			}
		case "image":
			source := block.Get("source")
			switch source.Get("type").String() {
			case "base64", "":
				if err := validateClaudeCodexBase64Source(source, "tool_result image", false); err != nil {
					return err
				}
			case "url":
				if url := source.Get("url"); url.Type != gjson.String || url.String() == "" {
					return fmt.Errorf("tool_result image URL must be a non-empty string")
				}
			default:
				return fmt.Errorf("unsupported tool_result image source type: %s", boundedClaudeCodexLabel(source.Get("type").String()))
			}
		case "document":
			if err := validateClaudeCodexToolResultDocument(block); err != nil {
				return err
			}
		case "search_result":
			if err := validateClaudeCodexSearchResult(block); err != nil {
				return err
			}
		case "tool_reference":
			if toolName := block.Get("tool_name"); toolName.Type != gjson.String || toolName.String() == "" {
				return fmt.Errorf("tool_reference tool_name must be a non-empty string")
			}
		default:
			return fmt.Errorf("unsupported tool_result content block: %s", boundedClaudeCodexLabel(block.Get("type").String()))
		}
	}
	return nil
}

func validateClaudeCodexToolResultDocument(block gjson.Result) error {
	source := block.Get("source")
	if !source.IsObject() {
		return fmt.Errorf("document source must be an object")
	}
	switch source.Get("type").String() {
	case "base64":
		return validateClaudeCodexBase64Source(source, "document", true)
	case "text":
		if source.Get("media_type").String() != "text/plain" {
			return fmt.Errorf("document text source media type must be text/plain")
		}
		if data := source.Get("data"); data.Type != gjson.String || data.String() == "" {
			return fmt.Errorf("document text source data must be a non-empty string")
		}
	case "url":
		if url := source.Get("url"); url.Type != gjson.String || url.String() == "" {
			return fmt.Errorf("document source URL must be a non-empty string")
		}
	case "content":
		if err := validateClaudeCodexDocumentContentSource(source.Get("content")); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported document source type: %s", boundedClaudeCodexLabel(source.Get("type").String()))
	}
	return nil
}

func validateClaudeCodexDocumentContentSource(content gjson.Result) error {
	if content.Type == gjson.String {
		return nil
	}
	if !content.IsArray() {
		return fmt.Errorf("document content source must be text or an array")
	}
	for _, block := range content.Array() {
		if !block.IsObject() {
			return fmt.Errorf("document content source blocks must be objects")
		}
		switch block.Get("type").String() {
		case "text":
			if block.Get("text").Type != gjson.String {
				return fmt.Errorf("document content source text must be a string")
			}
		case "image":
			source := block.Get("source")
			switch source.Get("type").String() {
			case "base64", "":
				if err := validateClaudeCodexBase64Source(source, "document image", false); err != nil {
					return err
				}
			case "url":
				if url := source.Get("url"); url.Type != gjson.String || url.String() == "" {
					return fmt.Errorf("document image source URL must be a non-empty string")
				}
			default:
				return fmt.Errorf("unsupported document image source type: %s", boundedClaudeCodexLabel(source.Get("type").String()))
			}
		default:
			return fmt.Errorf("unsupported document content source block: %s", boundedClaudeCodexLabel(block.Get("type").String()))
		}
	}
	return nil
}

func validateClaudeCodexSearchResult(block gjson.Result) error {
	if source := block.Get("source"); source.Type != gjson.String || source.String() == "" {
		return fmt.Errorf("search_result source must be a non-empty string")
	}
	if title := block.Get("title"); title.Type != gjson.String || title.String() == "" {
		return fmt.Errorf("search_result title must be a non-empty string")
	}
	content := block.Get("content")
	if !content.IsArray() {
		return fmt.Errorf("search_result content must be an array")
	}
	for _, text := range content.Array() {
		if !text.IsObject() || text.Get("type").String() != "text" {
			return fmt.Errorf("search_result content blocks must be text objects")
		}
		if text.Get("text").Type != gjson.String {
			return fmt.Errorf("search_result content text must be a string")
		}
	}
	return nil
}

func validateClaudeCodexContextManagement(value gjson.Result, state claudeCodexValidationState) error {
	if !value.Exists() || value.Type == gjson.Null {
		return nil
	}
	if !value.IsObject() {
		return fmt.Errorf("context_management must be an object")
	}
	if field := firstUnsupportedClaudeCodexField(value, "edits"); field != "" {
		return fmt.Errorf("unsupported context_management field: %s", boundedClaudeCodexLabel(field))
	}
	edits := value.Get("edits")
	if !edits.Exists() {
		return nil
	}
	if !edits.IsArray() {
		return fmt.Errorf("context_management.edits must be an array")
	}
	sawNonThinkingEdit := false
	for _, edit := range edits.Array() {
		if !edit.IsObject() {
			return fmt.Errorf("context_management edits must be objects with a type")
		}
		editType, err := requiredClaudeCodexString(edit.Get("type"), "context_management edit type")
		if err != nil {
			return err
		}
		switch editType {
		case "clear_thinking_20251015":
			if sawNonThinkingEdit {
				return fmt.Errorf("clear_thinking_20251015 must precede other context_management edits")
			}
			if err := validateNoopClaudeCodexClearThinking(edit, state.thinkingTurns); err != nil {
				return err
			}
		case "clear_tool_uses_20250919":
			sawNonThinkingEdit = true
			if err := validateNoopClaudeCodexClearToolUses(edit, state); err != nil {
				return err
			}
		case "compact_20260112":
			return fmt.Errorf("compact_20260112 has no exact single-request Responses mapping")
		default:
			return fmt.Errorf("unsupported context_management edit type: %s", boundedClaudeCodexLabel(editType))
		}
	}
	return nil
}

func validateNoopClaudeCodexClearThinking(edit gjson.Result, thinkingTurns int) error {
	if field := firstUnsupportedClaudeCodexField(edit, "type", "keep"); field != "" {
		return fmt.Errorf("unsupported clear_thinking_20251015 field: %s", boundedClaudeCodexLabel(field))
	}
	keep := edit.Get("keep")
	if !keep.Exists() {
		if thinkingTurns > 0 {
			return fmt.Errorf("clear_thinking_20251015 could alter supplied history and has no exact Responses equivalent")
		}
		return nil
	}
	if keep.Type == gjson.String && keep.String() == "all" {
		return nil
	}
	if !keep.IsObject() {
		return fmt.Errorf("clear_thinking_20251015.keep must be all or a thinking_turns object")
	}
	keepType := keep.Get("type").String()
	if keepType == "all" {
		if field := firstUnsupportedClaudeCodexField(keep, "type"); field != "" {
			return fmt.Errorf("unsupported clear_thinking_20251015.keep field: %s", boundedClaudeCodexLabel(field))
		}
		return nil
	}
	if keepType != "thinking_turns" {
		return fmt.Errorf("clear_thinking_20251015.keep must select thinking_turns")
	}
	if field := firstUnsupportedClaudeCodexField(keep, "type", "value"); field != "" {
		return fmt.Errorf("unsupported clear_thinking_20251015.keep field: %s", boundedClaudeCodexLabel(field))
	}
	count, err := claudeCodexInteger(keep.Get("value"), "clear_thinking_20251015.keep.value", 1)
	if err != nil {
		return err
	}
	if int64(thinkingTurns) > count {
		return fmt.Errorf("clear_thinking_20251015 could alter supplied history and has no exact Responses equivalent")
	}
	return nil
}

func validateNoopClaudeCodexClearToolUses(edit gjson.Result, state claudeCodexValidationState) error {
	if field := firstUnsupportedClaudeCodexField(edit, "type", "clear_at_least", "clear_tool_inputs", "exclude_tools", "keep", "trigger"); field != "" {
		return fmt.Errorf("unsupported clear_tool_uses_20250919 field: %s", boundedClaudeCodexLabel(field))
	}
	keep := int64(3)
	if value := edit.Get("clear_at_least"); value.Exists() && value.Type != gjson.Null {
		if _, err := claudeCodexTypedCount(value, "input_tokens", "clear_tool_uses_20250919.clear_at_least", 1); err != nil {
			return err
		}
	}
	if trigger := edit.Get("trigger"); trigger.Exists() {
		triggerType := trigger.Get("type").String()
		if triggerType != "input_tokens" && triggerType != "tool_uses" {
			return fmt.Errorf("clear_tool_uses_20250919.trigger must select input_tokens or tool_uses")
		}
		if _, err := claudeCodexTypedCount(trigger, triggerType, "clear_tool_uses_20250919.trigger", 1); err != nil {
			return err
		}
	}
	if keepValue := edit.Get("keep"); keepValue.Exists() {
		if keepValue.Get("type").String() != "tool_uses" {
			return fmt.Errorf("clear_tool_uses_20250919.keep must be a tool_uses object")
		}
		if field := firstUnsupportedClaudeCodexField(keepValue, "type", "value"); field != "" {
			return fmt.Errorf("unsupported clear_tool_uses_20250919.keep field: %s", boundedClaudeCodexLabel(field))
		}
		value, err := claudeCodexInteger(keepValue.Get("value"), "clear_tool_uses_20250919.keep.value", 0)
		if err != nil {
			return err
		}
		keep = value
	}
	excluded := make(map[string]struct{})
	if excludeTools := edit.Get("exclude_tools"); excludeTools.Exists() && excludeTools.Type != gjson.Null {
		values, err := claudeCodexStringList(excludeTools, "clear_tool_uses_20250919.exclude_tools")
		if err != nil {
			return err
		}
		for _, name := range values {
			excluded[name] = struct{}{}
		}
	}
	if clearInputs := edit.Get("clear_tool_inputs"); clearInputs.Exists() && clearInputs.Type != gjson.Null && clearInputs.Type != gjson.True && clearInputs.Type != gjson.False {
		if _, err := claudeCodexStringList(clearInputs, "clear_tool_uses_20250919.clear_tool_inputs"); err != nil {
			return err
		}
	}
	clearable := int64(0)
	for _, id := range state.toolResults {
		if _, skip := excluded[state.toolNames[id]]; !skip {
			clearable++
		}
	}
	if clearable > keep {
		return fmt.Errorf("clear_tool_uses_20250919 could alter supplied history and has no exact Responses equivalent")
	}
	return nil
}

func claudeCodexTypedCount(value gjson.Result, wantType, label string, minimum int64) (int64, error) {
	if !value.IsObject() || value.Get("type").String() != wantType {
		return 0, fmt.Errorf("%s must have type %s", label, wantType)
	}
	if field := firstUnsupportedClaudeCodexField(value, "type", "value"); field != "" {
		return 0, fmt.Errorf("unsupported %s field: %s", label, boundedClaudeCodexLabel(field))
	}
	return claudeCodexInteger(value.Get("value"), label+".value", minimum)
}

func claudeCodexInteger(value gjson.Result, label string, minimum int64) (int64, error) {
	if value.Type != gjson.Number {
		return 0, fmt.Errorf("%s must be an integer of at least %d", label, minimum)
	}
	integer, err := strconv.ParseInt(value.Raw, 10, 64)
	if err != nil || integer < minimum {
		return 0, fmt.Errorf("%s must be an integer of at least %d", label, minimum)
	}
	return integer, nil
}

func claudeCodexStringList(value gjson.Result, label string) ([]string, error) {
	if !value.IsArray() {
		return nil, fmt.Errorf("%s must be an array of non-empty strings", label)
	}
	values := make([]string, 0, len(value.Array()))
	for _, item := range value.Array() {
		if item.Type != gjson.String || item.String() == "" {
			return nil, fmt.Errorf("%s must be an array of non-empty strings", label)
		}
		values = append(values, item.String())
	}
	return values, nil
}

func requiredClaudeCodexString(value gjson.Result, label string) (string, error) {
	if value.Type != gjson.String || strings.TrimSpace(value.String()) == "" {
		return "", fmt.Errorf("%s must be a non-empty string", label)
	}
	return value.String(), nil
}

func firstUnsupportedClaudeCodexField(object gjson.Result, allowed ...string) string {
	allowedFields := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedFields[field] = struct{}{}
	}
	unsupported := ""
	object.ForEach(func(key, _ gjson.Result) bool {
		if _, ok := allowedFields[key.String()]; !ok {
			unsupported = key.String()
			return false
		}
		return true
	})
	return unsupported
}

func boundedClaudeCodexLabel(value string) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, strings.TrimSpace(value))
	runes := []rune(value)
	if len(runes) > 64 {
		value = string(runes[:64]) + "…"
	}
	return strconv.Quote(value)
}
