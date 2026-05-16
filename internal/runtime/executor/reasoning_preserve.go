package executor

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

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
// When both original and translated carry reasoning_content at the same assistant ordinal,
// the original value always wins — it is the authoritative source the provider expects
// to receive back verbatim.
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
	for i, msg := range transMsgArr {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}

		text, ok := origReasoning[assistantOrdinal]
		if ok {
			path := fmt.Sprintf("messages.%d.reasoning_content", i)
			next, err := sjson.SetBytes(out, path, text)
			if err != nil {
				return translated, fmt.Errorf("preserveReasoningContent: failed to set reasoning_content at index %d: %w", i, err)
			}
			out = next
		}
		assistantOrdinal++
	}

	return out, nil
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
		if rc := msg.Get("reasoning_content"); rc.Exists() {
			reasoning[ordinal] = rc.String()
		}
		ordinal++
	}
	return reasoning
}
