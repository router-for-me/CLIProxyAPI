// Package claude provides translation between Kiro and Claude formats.
// Since Kiro executor generates Claude-compatible SSE format internally (with event: prefix),
// translations are pass-through for streaming, but responses need proper formatting.
package claude

import (
	"context"
)

// ConvertKiroStreamToClaude converts Kiro streaming response to Claude format.
// Kiro executor already generates complete SSE format with "event:" prefix,
// so this is a simple pass-through.
func ConvertKiroStreamToClaude(ctx context.Context, model string, originalRequest, request, rawResponse []byte, param *any) []string {
	return []string{string(rawResponse)}
}

// ConvertKiroNonStreamToClaude converts Kiro non-streaming response to Claude format.
// The response is already in Claude format, so this is a pass-through.
func ConvertKiroNonStreamToClaude(ctx context.Context, model string, originalRequest, request, rawResponse []byte, param *any) string {
	return string(rawResponse)
}
