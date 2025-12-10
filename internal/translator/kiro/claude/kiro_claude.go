// Package claude provides translation between Kiro and Claude formats.
// Since Kiro executor generates Claude-compatible SSE format internally (with event: prefix),
// translations are pass-through.
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
// Kiro executor already generates complete SSE format with "event:" prefix,
// so this is a simple pass-through.
func ConvertKiroResponseToClaude(ctx context.Context, model string, originalRequest, request, rawResponse []byte, param *any) []string {
	return []string{string(rawResponse)}
}

// ConvertKiroResponseToClaudeNonStream converts Kiro non-streaming response to Claude format.
func ConvertKiroResponseToClaudeNonStream(ctx context.Context, model string, originalRequest, request, rawResponse []byte, param *any) string {
	return string(rawResponse)
}
