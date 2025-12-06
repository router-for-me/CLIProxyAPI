// Package claude provides translation between Kiro and Claude formats.
// Since Kiro uses Claude-compatible format internally, translations are mostly pass-through.
package claude

import (
	"bytes"
	"context"
)

// ConvertClaudeRequestToKiro converts Claude request to Kiro format.
// Since Kiro uses Claude format internally, this is mostly a pass-through.
func ConvertClaudeRequestToKiro(modelName string, inputRawJSON []byte, stream bool) []byte {
	return bytes.Clone(inputRawJSON)
}

// ConvertKiroResponseToClaude converts Kiro streaming response to Claude format.
func ConvertKiroResponseToClaude(ctx context.Context, model string, originalRequest, request, rawResponse []byte, param *any) []string {
	return []string{string(rawResponse)}
}

// ConvertKiroResponseToClaudeNonStream converts Kiro non-streaming response to Claude format.
func ConvertKiroResponseToClaudeNonStream(ctx context.Context, model string, originalRequest, request, rawResponse []byte, param *any) string {
	return string(rawResponse)
}
