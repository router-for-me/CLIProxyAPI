package executor

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const reasoningContentKey = "reasoning_content"

// preserveReasoningContent ensures assistant messages in the translated OpenAI-format
// payload retain reasoning_content from the original source payload.
//
// DeepSeek and other providers that support thinking mode require reasoning_content
// to be passed back verbatim in multi-turn conversations. Without this, the API returns
// a 400 error: "The reasoning_content in the thinking mode must be passed back to the API."
//
// Matching strategy: instead of requiring identical message counts (which breaks when
// translation inserts/splits messages like Claude tool_result → tool role), we match
// assistant messages by their ordinal position within the assistant-only sequence.
// This is robust because translation never reorders or drops assistant messages —
// it only inserts non-assistant messages (tool, system) around them.
//
// When the translated payload already carries reasoning_content at a given assistant
// ordinal (e.g. from a payload override or from translation), that value is preserved —
// the user or translator has explicitly set it and their intent takes precedence.
// Only when reasoning_content is missing do we fall back to the original value.
//
// Error contract: on sjson.SetBytes failure, the function discards any partial writes
// and returns the unmodified translated input along with the error, so the caller never
// receives a partially-patched payload.
func preserveReasoningContent(original, translated []byte) ([]byte, error) {
	if len(original) == 0 || len(translated) == 0 {
		return translated, nil
	}
	if !gjson.ValidBytes(original) || !gjson.ValidBytes(translated) {
		return translated, nil
	}

	origMsgs := gjson.GetBytes(original, "messages")
	if !origMsgs.Exists() || !origMsgs.IsArray() {
		return translated, nil
	}
	origMsgArr := origMsgs.Array()

	transMsgs := gjson.GetBytes(translated, "messages")
	if !transMsgs.Exists() || !transMsgs.IsArray() {
		return translated, nil
	}
	transMsgArr := transMsgs.Array()

	origReasoning := collectAssistantReasoning(origMsgArr)
	if len(origReasoning) == 0 {
		return translated, nil
	}

	out := translated
	assistantOrdinal := 0
	patches := make(map[int]string)
	for i, msg := range transMsgArr {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}

		origText, origOK := origReasoning[assistantOrdinal]
		transRC := msg.Get(reasoningContentKey)
		if origOK && !transRC.Exists() {
			patches[i] = origText
		}
		assistantOrdinal++
	}

	if len(patches) > 0 {
		msgsArray := []byte("[]")
		for i, msg := range transMsgArr {
			msgBytes := []byte(msg.Raw)
			if rc, ok := patches[i]; ok {
				patched, err := sjson.SetBytes(msgBytes, reasoningContentKey, rc)
				if err != nil {
					return translated, fmt.Errorf("preserveReasoningContent: failed to set reasoning_content at index %d: %w", i, err)
				}
				msgBytes = patched
			}
			var errAppend error
			msgsArray, errAppend = sjson.SetRawBytes(msgsArray, "-1", msgBytes)
			if errAppend != nil {
				return translated, fmt.Errorf("preserveReasoningContent: failed to append message at index %d: %w", i, errAppend)
			}
		}
		result, err := sjson.SetRawBytes(out, "messages", msgsArray)
		if err != nil {
			return translated, fmt.Errorf("preserveReasoningContent: failed to update messages: %w", err)
		}
		out = result
	}

	return out, nil
}

// convertReasoningToThinkingContent transforms top-level reasoning_content on assistant
// messages into content-array thinking blocks when the payload has thinking mode enabled,
// while preserving the top-level reasoning_content field for backward compatibility.
//
// Providers like MiMo, DeepSeek require assistant messages with reasoning_content to
// also express it as a structured content part:
//
//	{"type": "thinking", "thinking": "<reasoning text>"}
//
// Without this, these APIs return 400:
//   - MiMo/DeepSeek: "messages.N.content.M.thinking.thinking: Field required"
//   - DeepSeek: "The reasoning_content in the thinking mode must be passed back to the API."
//
// Other providers (standard OpenAI, OpenRouter) only recognize the top-level
// reasoning_content field and would reject a content-array thinking block.
// To maintain compatibility with both camps, this function emits BOTH formats:
//
//	{
//	  "content": [{"type":"thinking","thinking":"..."}, {"type":"text","text":"..."}],
//	  "reasoning_content": "..."
//	}
//
// The function checks for thinking-mode indicators in the payload:
//   - "reasoning_effort" (OpenAI/MiMo/DeepSeek level-based thinking)
//   - "thinking.type" == "enabled" (MiMo/DeepSeek explicit thinking toggle)
//
// When thinking mode is NOT active, the payload is returned unchanged.
//
// Conversion rules for each assistant message that has reasoning_content:
//  1. Build a new content array: [{"type":"thinking","thinking": rc}, ...existing content parts...]
//  2. If existing content is a string, wrap it as {"type":"text","text": content}
//     (unless it's empty, in which case only the thinking block is emitted).
//  3. If existing content is already an array, prepend the thinking block.
//  4. Keep the top-level reasoning_content field (dual-format compatibility).
//
// Error contract: on any sjson failure, the function returns the unmodified input
// payload along with the error, so the caller never receives a partially-patched payload.
func convertReasoningToThinkingContent(payload []byte) ([]byte, error) {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload, nil
	}

	if !isThinkingModeActive(payload) {
		return payload, nil
	}

	msgs := gjson.GetBytes(payload, "messages")
	if !msgs.Exists() || !msgs.IsArray() {
		return payload, nil
	}

	msgArr := msgs.Array()
	needsRewrite := false

	for _, msg := range msgArr {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}
		rc := msg.Get(reasoningContentKey)
		if !rc.Exists() {
			continue
		}
		reasoningText := rc.String()
		if strings.TrimSpace(reasoningText) == "" {
			continue
		}
		needsRewrite = true
		break
	}

	if !needsRewrite {
		return payload, nil
	}

	outMsgs := []byte("[]")
	for _, msg := range msgArr {
		msgBytes := []byte(msg.Raw)

		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			var errAppend error
			outMsgs, errAppend = sjson.SetRawBytes(outMsgs, "-1", msgBytes)
			if errAppend != nil {
				return payload, fmt.Errorf("convertReasoningToThinkingContent: failed to append non-assistant message: %w", errAppend)
			}
			continue
		}

		rc := msg.Get(reasoningContentKey)
		if !rc.Exists() || strings.TrimSpace(rc.String()) == "" {
			var errAppend error
			outMsgs, errAppend = sjson.SetRawBytes(outMsgs, "-1", msgBytes)
			if errAppend != nil {
				return payload, fmt.Errorf("convertReasoningToThinkingContent: failed to append assistant message without reasoning: %w", errAppend)
			}
			continue
		}

		reasoningText := rc.String()

		thinkingBlock := []byte(`{"type":"thinking","thinking":""}`)
		var errSet error
		thinkingBlock, errSet = sjson.SetBytes(thinkingBlock, "thinking", reasoningText)
		if errSet != nil {
			return payload, fmt.Errorf("convertReasoningToThinkingContent: failed to set thinking text: %w", errSet)
		}

		content := msg.Get("content")
		var newContent []byte

		if content.Exists() && content.IsArray() {
			newContent = []byte("[]")
			var errAppend error
			newContent, errAppend = sjson.SetRawBytes(newContent, "-1", thinkingBlock)
			if errAppend != nil {
				return payload, fmt.Errorf("convertReasoningToThinkingContent: failed to append thinking block to content array: %w", errAppend)
			}
			for _, part := range content.Array() {
				newContent, errAppend = sjson.SetRawBytes(newContent, "-1", []byte(part.Raw))
				if errAppend != nil {
					return payload, fmt.Errorf("convertReasoningToThinkingContent: failed to append content part: %w", errAppend)
				}
			}
		} else if content.Exists() && content.Type == gjson.String {
			text := content.String()
			newContent = []byte("[]")
			var errAppend error
			newContent, errAppend = sjson.SetRawBytes(newContent, "-1", thinkingBlock)
			if errAppend != nil {
				return payload, fmt.Errorf("convertReasoningToThinkingContent: failed to append thinking block to string content: %w", errAppend)
			}
			if text != "" {
				textBlock := []byte(`{"type":"text","text":""}`)
				var errSetText error
				textBlock, errSetText = sjson.SetBytes(textBlock, "text", text)
				if errSetText != nil {
					return payload, fmt.Errorf("convertReasoningToThinkingContent: failed to set text block: %w", errSetText)
				}
				newContent, errAppend = sjson.SetRawBytes(newContent, "-1", textBlock)
				if errAppend != nil {
					return payload, fmt.Errorf("convertReasoningToThinkingContent: failed to append text block: %w", errAppend)
				}
			}
		} else {
			newContent = []byte("[]")
			var errAppend error
			newContent, errAppend = sjson.SetRawBytes(newContent, "-1", thinkingBlock)
			if errAppend != nil {
				return payload, fmt.Errorf("convertReasoningToThinkingContent: failed to append thinking block to empty content: %w", errAppend)
			}
		}

		updated, err := sjson.SetRawBytes(msgBytes, "content", newContent)
		if err != nil {
			return payload, fmt.Errorf("convertReasoningToThinkingContent: failed to set content on message: %w", err)
		}
		var errAppend error
		outMsgs, errAppend = sjson.SetRawBytes(outMsgs, "-1", updated)
		if errAppend != nil {
			return payload, fmt.Errorf("convertReasoningToThinkingContent: failed to append updated message: %w", errAppend)
		}
	}

	result, err := sjson.SetRawBytes(payload, "messages", outMsgs)
	if err != nil {
		return payload, fmt.Errorf("convertReasoningToThinkingContent: failed to update messages: %w", err)
	}
	return result, nil
}

// isThinkingModeActive checks whether the payload has thinking mode enabled
// via either reasoning_effort or thinking.type=enabled.
func isThinkingModeActive(payload []byte) bool {
	if re := gjson.GetBytes(payload, "reasoning_effort"); re.Exists() && re.Type == gjson.String {
		effort := strings.ToLower(strings.TrimSpace(re.String()))
		if effort != "" && effort != "none" {
			return true
		}
	}
	if tt := gjson.GetBytes(payload, "thinking.type"); tt.Exists() && tt.Type == gjson.String {
		t := strings.ToLower(strings.TrimSpace(tt.String()))
		if t == "enabled" || t == "adaptive" || t == "auto" {
			return true
		}
	}
	return false
}

// collectAssistantReasoning extracts reasoning_content from assistant messages,
// keyed by their ordinal position in the assistant-only sequence (0, 1, 2, ...).
// Empty-string reasoning_content is preserved because DeepSeek requires it.
func collectAssistantReasoning(messages []gjson.Result) map[int]string {
	reasoning := make(map[int]string)
	ordinal := 0
	for _, msg := range messages {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}
		if rc := msg.Get(reasoningContentKey); rc.Exists() {
			reasoning[ordinal] = rc.String()
		}
		ordinal++
	}
	return reasoning
}
