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

	// Build a lookup of reasoning_content from original assistant messages.
	type reasonEntry struct {
		text    string
		isEmpty bool // true if field existed but was empty
	}
	origReasoning := make(map[int]reasonEntry)
	for i, msg := range origMsgs.Array() {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}
		if rc := msg.Get("reasoning_content"); rc.Exists() {
			origReasoning[i] = reasonEntry{text: rc.String(), isEmpty: false}
		}
	}

	if len(origReasoning) == 0 {
		return translated, nil
	}

	transMsgs := gjson.GetBytes(translated, "messages")
	if !transMsgs.Exists() || !transMsgs.IsArray() {
		return translated, nil
	}

	out := translated
	lastReasoning := ""
	for i, msg := range transMsgs.Array() {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}

		if entry, ok := origReasoning[i]; ok {
			// Original had reasoning_content — preserve it exactly (including empty string).
			path := fmt.Sprintf("messages.%d.reasoning_content", i)
			next, err := sjson.SetBytes(out, path, entry.text)
			if err != nil {
				return translated, fmt.Errorf("preserveReasoningContent: failed to set reasoning_content at index %d: %w", i, err)
			}
			out = next
			if strings.TrimSpace(entry.text) != "" {
				lastReasoning = entry.text
			}
			continue
		}

		// No reasoning_content in original for this index.
		// If the translated payload already has one, keep it.
		if msg.Get("reasoning_content").Exists() {
			continue
		}

		// Inherit from the most recent reasoning if available.
		if lastReasoning != "" {
			path := fmt.Sprintf("messages.%d.reasoning_content", i)
			next, err := sjson.SetBytes(out, path, lastReasoning)
			if err != nil {
				return translated, fmt.Errorf("preserveReasoningContent: failed to set inherited reasoning_content at index %d: %w", i, err)
			}
			out = next
		}
	}

	return out, nil
}
