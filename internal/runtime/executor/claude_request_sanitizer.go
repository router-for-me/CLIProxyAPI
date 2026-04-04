package executor

import (
	"fmt"
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
	for msgIdx, msg := range messages.Array() {
		if msg.Get("role").String() != "assistant" {
			continue
		}

		content := msg.Get("content")
		if !content.Exists() || !content.IsArray() {
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
			continue
		}

		contentPath := fmt.Sprintf("messages.%d.content", msgIdx)
		var err error
		if len(keepBlocks) == 0 {
			body, err = sjson.SetBytes(body, contentPath, []any{})
		} else {
			body, err = sjson.SetBytes(body, contentPath, keepBlocks)
		}
		if err != nil {
			log.Warnf("Claude RequestSanitizer: failed to sanitize message %d: %v", msgIdx, err)
			continue
		}

		modified = true
		log.Debugf("Claude RequestSanitizer: removed %d invalid thinking blocks from message %d", removedCount, msgIdx)
	}

	if modified {
		log.Debug("Claude RequestSanitizer: sanitized request body")
	}

	return body
}
