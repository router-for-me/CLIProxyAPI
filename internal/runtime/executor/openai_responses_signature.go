package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func sanitizeOpenAIResponsesReasoningEncryptedContent(ctx context.Context, provider string, body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = "openai responses upstream"
	}

	updated := body
	for index, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
			continue
		}

		encryptedContentPath := fmt.Sprintf("input.%d.encrypted_content", index)
		encryptedContent := gjson.GetBytes(updated, encryptedContentPath)
		if !encryptedContent.Exists() {
			continue
		}

		reason := ""
		switch encryptedContent.Type {
		case gjson.String:
			rawSignature := encryptedContent.String()
			if rawSignature != strings.TrimSpace(rawSignature) {
				reason = "encrypted_content has leading or trailing whitespace"
			} else if _, err := signature.InspectGPTReasoningSignature(rawSignature); err != nil {
				reason = err.Error()
			}
		case gjson.Null:
			reason = "encrypted_content is null"
		default:
			reason = fmt.Sprintf("encrypted_content must be a string, got %s", encryptedContent.Type.String())
		}
		if reason == "" {
			continue
		}

		next, err := sjson.DeleteBytes(updated, encryptedContentPath)
		if err != nil {
			helps.LogWithRequestID(ctx).Debugf("%s: failed to drop invalid reasoning encrypted_content at input[%d]: %v", provider, index, err)
			continue
		}
		updated = next

		itemID := strings.TrimSpace(gjson.GetBytes(updated, fmt.Sprintf("input.%d.id", index)).String())
		if itemID == "" {
			itemID = fmt.Sprintf("input[%d]", index)
		}
		helps.LogWithRequestID(ctx).Debugf("%s: dropped invalid reasoning encrypted_content at input[%d] item_id=%q reason=%s", provider, index, itemID, reason)
	}
	return updated
}
