package signature

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// StripInvalidClaudeThinkingBlocks removes Claude thinking blocks whose
// signatures are empty or not valid Claude thinking signatures after stripping
// an optional cache prefix, unless the validation options allow an empty
// thinking placeholder.
func StripInvalidClaudeThinkingBlocks(payload []byte, opts ...ClaudeSignatureValidationOptions) []byte {
	return stripInvalidClaudeThinkingBlocks(payload, false, opts...)
}

// StripInvalidClaudeThinkingBlocksAndEmptyMessages also removes messages whose
// content becomes empty after invalid thinking blocks are removed.
func StripInvalidClaudeThinkingBlocksAndEmptyMessages(payload []byte, opts ...ClaudeSignatureValidationOptions) []byte {
	return stripInvalidClaudeThinkingBlocks(payload, true, opts...)
}

func stripInvalidClaudeThinkingBlocks(payload []byte, dropEmptyMessages bool, opts ...ClaudeSignatureValidationOptions) []byte {
	messages := ojson.GetBytesNoCopy(payload, "messages")
	if !messages.IsArray() {
		return payload
	}
	opt := claudeSignatureValidationOptions(opts)
	messageResults := messages.Array()
	doc := ojson.NewDoc(payload)
	modified := false
	var droppedMessages []int
	for i, msg := range messageResults {
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		contentResults := content.Array()
		var droppedParts []int
		for j, part := range contentResults {
			if part.Get("type").String() == "thinking" && shouldStripClaudeThinkingBlock(part, opt) {
				droppedParts = append(droppedParts, j)
			}
		}
		if dropEmptyMessages && len(droppedParts) == len(contentResults) {
			droppedMessages = append(droppedMessages, i)
			if len(droppedParts) > 0 {
				modified = true
			}
			continue
		}
		if len(droppedParts) == 0 {
			continue
		}
		modified = true
		for k := len(droppedParts) - 1; k >= 0; k-- {
			_ = doc.Delete(fmt.Sprintf("messages.%d.content.%d", i, droppedParts[k]))
		}
	}
	if !modified {
		return payload
	}
	for k := len(droppedMessages) - 1; k >= 0; k-- {
		_ = doc.Delete(fmt.Sprintf("messages.%d", droppedMessages[k]))
	}
	return doc.Bytes()
}

func shouldStripClaudeThinkingBlock(part ojson.Result, opt ClaudeSignatureValidationOptions) bool {
	if opt.AllowEmptySignatureWithEmptyText && isEmptyClaudeThinkingPlaceholder(part) {
		return false
	}
	return !IsValidClaudeThinkingSignature(part.Get("signature").String(), opt)
}

func isEmptyClaudeThinkingPlaceholder(part ojson.Result) bool {
	if strings.TrimSpace(part.Get("signature").String()) != "" {
		return false
	}
	return strings.TrimSpace(claudeThinkingBlockText(part)) == ""
}

func claudeThinkingBlockText(part ojson.Result) string {
	if text := part.Get("text"); text.Exists() && text.Type == ojson.String {
		return text.String()
	}

	thinkingField := part.Get("thinking")
	if !thinkingField.Exists() {
		return ""
	}
	if thinkingField.Type == ojson.String {
		return thinkingField.String()
	}
	if thinkingField.IsObject() {
		if inner := thinkingField.Get("text"); inner.Exists() && inner.Type == ojson.String {
			return inner.String()
		}
		if inner := thinkingField.Get("thinking"); inner.Exists() && inner.Type == ojson.String {
			return inner.String()
		}
	}
	return ""
}
