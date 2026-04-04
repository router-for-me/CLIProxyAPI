package executor

import (
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// sanitizeClaudeRequestBody removes assistant thinking blocks that do not carry a
// valid Claude signature. Anthropic rejects unsigned thinking blocks when a mixed
// session returns from non-Claude providers back to Claude.
func sanitizeClaudeRequestBody(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body
	}

	modified := false
	sanitizedMessages := make([]any, 0, len(messages.Array()))
	for msgIdx, msg := range messages.Array() {
		msgValue := msg.Value()
		if msg.Get("role").String() != "assistant" {
			sanitizedMessages = append(sanitizedMessages, msgValue)
			continue
		}

		content := msg.Get("content")
		if !content.Exists() || !content.IsArray() {
			sanitizedMessages = append(sanitizedMessages, msgValue)
			continue
		}

		blocks := content.Array()
		keepBlocks := make([]any, 0, len(blocks))
		removedCount := 0

		for _, block := range blocks {
			if block.Get("type").String() == "thinking" {
				sig := block.Get("signature")
				if !sig.Exists() || sig.Type != gjson.String || strings.TrimSpace(sig.String()) == "" {
					removedCount++
					continue
				}
			}
			keepBlocks = append(keepBlocks, block.Value())
		}

		if removedCount == 0 {
			sanitizedMessages = append(sanitizedMessages, msgValue)
			continue
		}

		if len(keepBlocks) == 0 {
			modified = true
			log.Warnf("Claude RequestSanitizer: removed assistant message %d after stripping %d invalid thinking blocks", msgIdx, removedCount)
			continue
		}

		msgObject, ok := msgValue.(map[string]any)
		if !ok {
			log.Warnf("Claude RequestSanitizer: failed to sanitize message %d: unexpected message shape %T", msgIdx, msgValue)
			sanitizedMessages = append(sanitizedMessages, msgValue)
			continue
		}
		msgObject["content"] = keepBlocks
		sanitizedMessages = append(sanitizedMessages, msgObject)

		modified = true
		log.Debugf("Claude RequestSanitizer: removed %d invalid thinking blocks from message %d", removedCount, msgIdx)
	}

	if !modified {
		return body
	}

	sanitizedBody, err := sjson.SetBytes(body, "messages", sanitizedMessages)
	if err != nil {
		log.Warnf("Claude RequestSanitizer: failed to rewrite messages array: %v", err)
		return body
	}

	log.Debug("Claude RequestSanitizer: sanitized request body")
	return sanitizedBody
}
