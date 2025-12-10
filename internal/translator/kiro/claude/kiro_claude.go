// Package claude provides translation between Kiro and Claude formats.
// Since Kiro uses Claude-compatible format internally, translations are mostly pass-through.
// However, SSE events require proper "event: <type>" prefix for Claude clients.
package claude

import (
	"bytes"
	"context"
	"strings"

	"github.com/tidwall/gjson"
)

// ConvertClaudeRequestToKiro converts Claude request to Kiro format.
// Since Kiro uses Claude format internally, this is mostly a pass-through.
func ConvertClaudeRequestToKiro(modelName string, inputRawJSON []byte, stream bool) []byte {
	return bytes.Clone(inputRawJSON)
}

// ConvertKiroResponseToClaude converts Kiro streaming response to Claude format.
// It adds the required "event: <type>" prefix for SSE compliance with Claude clients.
// Input format: "data: {\"type\":\"message_start\",...}"
// Output format: "event: message_start\ndata: {\"type\":\"message_start\",...}"
func ConvertKiroResponseToClaude(ctx context.Context, model string, originalRequest, request, rawResponse []byte, param *any) []string {
	raw := string(rawResponse)

	// Handle multiple data blocks (e.g., message_delta + message_stop)
	lines := strings.Split(raw, "\n\n")
	var results []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Extract event type from JSON and add "event:" prefix
		formatted := addEventPrefix(line)
		if formatted != "" {
			results = append(results, formatted)
		}
	}

	if len(results) == 0 {
		return []string{raw}
	}

	return results
}

// addEventPrefix extracts the event type from the data line and adds the event: prefix.
// Input: "data: {\"type\":\"message_start\",...}"
// Output: "event: message_start\ndata: {\"type\":\"message_start\",...}"
func addEventPrefix(dataLine string) string {
	if !strings.HasPrefix(dataLine, "data: ") {
		return dataLine
	}

	jsonPart := strings.TrimPrefix(dataLine, "data: ")
	eventType := gjson.Get(jsonPart, "type").String()

	if eventType == "" {
		return dataLine
	}

	return "event: " + eventType + "\n" + dataLine
}

// ConvertKiroResponseToClaudeNonStream converts Kiro non-streaming response to Claude format.
func ConvertKiroResponseToClaudeNonStream(ctx context.Context, model string, originalRequest, request, rawResponse []byte, param *any) string {
	return string(rawResponse)
}
