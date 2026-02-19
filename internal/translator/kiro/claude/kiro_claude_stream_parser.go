package claude

import (
	"encoding/json"
	"strings"

	log "github.com/sirupsen/logrus"
)

// sseEvent represents a Server-Sent Event
type sseEvent struct {
	Event string
	Data  interface{}
}

// ToSSEString converts the event to SSE wire format
func (e *sseEvent) ToSSEString() string {
	dataBytes, _ := json.Marshal(e.Data)
	return "event: " + e.Event + "\ndata: " + string(dataBytes) + "\n\n"
}

// AdjustStreamIndices adjusts content block indices in SSE event data by adding an offset.
// It also suppresses duplicate message_start events (returns shouldForward=false).
// This is used to combine search indicator events (indices 0,1) with Kiro model response events.
//
// The data parameter is a single SSE "data:" line payload (JSON).
// Returns: adjusted data, shouldForward (false = skip this event).
func AdjustStreamIndices(data []byte, offset int) ([]byte, bool) {
	if len(data) == 0 {
		return data, true
	}

	// Quick check: parse the JSON
	var event map[string]interface{}
	if err := json.Unmarshal(data, &event); err != nil {
		// Not valid JSON, pass through
		return data, true
	}

	eventType, _ := event["type"].(string)

	// Suppress duplicate message_start events
	if eventType == "message_start" {
		return data, false
	}

	// Adjust index for content_block events
	switch eventType {
	case "content_block_start", "content_block_delta", "content_block_stop":
		if idx, ok := event["index"].(float64); ok {
			event["index"] = int(idx) + offset
			adjusted, err := json.Marshal(event)
			if err != nil {
				return data, true
			}
			return adjusted, true
		}
	}

	// Pass through all other events unchanged (message_delta, message_stop, ping, etc.)
	return data, true
}

// AdjustSSEChunk processes a raw SSE chunk (potentially containing multiple "event:/data:" pairs)
// and adjusts content block indices. Suppresses duplicate message_start events.
// Returns the adjusted chunk and whether it should be forwarded.
func AdjustSSEChunk(chunk []byte, offset int) ([]byte, bool) {
	chunkStr := string(chunk)

	// Fast path: if no "data:" prefix, pass through
	if !strings.Contains(chunkStr, "data: ") {
		return chunk, true
	}

	var result strings.Builder
	hasContent := false

	lines := strings.Split(chunkStr, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]

		if strings.HasPrefix(line, "data: ") {
			dataPayload := strings.TrimPrefix(line, "data: ")
			dataPayload = strings.TrimSpace(dataPayload)

			if dataPayload == "[DONE]" {
				result.WriteString(line + "\n")
				hasContent = true
				continue
			}

			adjusted, shouldForward := AdjustStreamIndices([]byte(dataPayload), offset)
			if !shouldForward {
				// Skip this event and its preceding "event:" line
				// Also skip the trailing empty line
				continue
			}

			result.WriteString("data: " + string(adjusted) + "\n")
			hasContent = true
		} else if strings.HasPrefix(line, "event: ") {
			// Check if the next data line will be suppressed
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "data: ") {
				dataPayload := strings.TrimPrefix(lines[i+1], "data: ")
				dataPayload = strings.TrimSpace(dataPayload)

				var event map[string]interface{}
				if err := json.Unmarshal([]byte(dataPayload), &event); err == nil {
					if eventType, ok := event["type"].(string); ok && eventType == "message_start" {
						// Skip both the event: and data: lines
						i++ // skip the data: line too
						continue
					}
				}
			}
			result.WriteString(line + "\n")
			hasContent = true
		} else {
			result.WriteString(line + "\n")
			if strings.TrimSpace(line) != "" {
				hasContent = true
			}
		}
	}

	if !hasContent {
		return nil, false
	}

	return []byte(result.String()), true
}

// BufferedStreamResult contains the analysis of buffered SSE chunks from a Kiro API response.
type BufferedStreamResult struct {
	// StopReason is the detected stop_reason from the stream (e.g., "end_turn", "tool_use")
	StopReason string
	// WebSearchQuery is the extracted query if the model requested another web_search
	WebSearchQuery string
	// WebSearchToolUseId is the tool_use ID from the model's response (needed for toolResults)
	WebSearchToolUseId string
	// HasWebSearchToolUse indicates whether the model requested web_search
	HasWebSearchToolUse bool
	// WebSearchToolUseIndex is the content_block index of the web_search tool_use
	WebSearchToolUseIndex int
}

// AnalyzeBufferedStream scans buffered SSE chunks to detect stop_reason and web_search tool_use.
// This is used in the search loop to determine if the model wants another search round.
func AnalyzeBufferedStream(chunks [][]byte) BufferedStreamResult {
	result := BufferedStreamResult{WebSearchToolUseIndex: -1}

	// Track tool use state across chunks
	var currentToolName string
	var currentToolIndex int = -1
	var toolInputBuilder strings.Builder

	for _, chunk := range chunks {
		chunkStr := string(chunk)
		lines := strings.Split(chunkStr, "\n")
		for _, line := range lines {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			dataPayload := strings.TrimPrefix(line, "data: ")
			dataPayload = strings.TrimSpace(dataPayload)
			if dataPayload == "[DONE]" || dataPayload == "" {
				continue
			}

			var event map[string]interface{}
			if err := json.Unmarshal([]byte(dataPayload), &event); err != nil {
				continue
			}

			eventType, _ := event["type"].(string)

			switch eventType {
			case "message_delta":
				// Extract stop_reason from message_delta
				if delta, ok := event["delta"].(map[string]interface{}); ok {
					if sr, ok := delta["stop_reason"].(string); ok && sr != "" {
						result.StopReason = sr
					}
				}

			case "content_block_start":
				// Detect tool_use content blocks
				if cb, ok := event["content_block"].(map[string]interface{}); ok {
					if cbType, ok := cb["type"].(string); ok && cbType == "tool_use" {
						if name, ok := cb["name"].(string); ok {
							currentToolName = strings.ToLower(name)
							if idx, ok := event["index"].(float64); ok {
								currentToolIndex = int(idx)
							}
							// Capture tool use ID for toolResults handshake
							if id, ok := cb["id"].(string); ok {
								result.WebSearchToolUseId = id
							}
							toolInputBuilder.Reset()
						}
					}
				}

			case "content_block_delta":
				// Accumulate tool input JSON
				if currentToolName != "" {
					if delta, ok := event["delta"].(map[string]interface{}); ok {
						if deltaType, ok := delta["type"].(string); ok && deltaType == "input_json_delta" {
							if partial, ok := delta["partial_json"].(string); ok {
								toolInputBuilder.WriteString(partial)
							}
						}
					}
				}

			case "content_block_stop":
				// Finalize tool use detection
				if currentToolName == "web_search" || currentToolName == "websearch" || currentToolName == "remote_web_search" {
					result.HasWebSearchToolUse = true
					result.WebSearchToolUseIndex = currentToolIndex
					// Extract query from accumulated input JSON
					inputJSON := toolInputBuilder.String()
					var input map[string]string
					if err := json.Unmarshal([]byte(inputJSON), &input); err == nil {
						if q, ok := input["query"]; ok {
							result.WebSearchQuery = q
						}
					}
					log.Debugf("kiro/websearch: detected web_search tool_use")
				}
				currentToolName = ""
				currentToolIndex = -1
				toolInputBuilder.Reset()
			}
		}
	}

	return result
}

// FilterChunksForClient processes buffered SSE chunks and removes web_search tool_use
// content blocks. This prevents the client from seeing "Tool use" prompts for web_search
// when the proxy is handling the search loop internally.
// Also suppresses message_start and message_delta/message_stop events since those
// are managed by the outer handleWebSearchStream.
func FilterChunksForClient(chunks [][]byte, wsToolIndex int, indexOffset int) [][]byte {
	var filtered [][]byte

	for _, chunk := range chunks {
		chunkStr := string(chunk)
		lines := strings.Split(chunkStr, "\n")

		var resultBuilder strings.Builder
		hasContent := false

		for i := 0; i < len(lines); i++ {
			line := lines[i]

			if strings.HasPrefix(line, "data: ") {
				dataPayload := strings.TrimPrefix(line, "data: ")
				dataPayload = strings.TrimSpace(dataPayload)

				if dataPayload == "[DONE]" {
					// Skip [DONE] — the outer loop manages stream termination
					continue
				}

				var event map[string]interface{}
				if err := json.Unmarshal([]byte(dataPayload), &event); err != nil {
					resultBuilder.WriteString(line + "\n")
					hasContent = true
					continue
				}

				eventType, _ := event["type"].(string)

				// Skip message_start (outer loop sends its own)
				if eventType == "message_start" {
					continue
				}

				// Skip message_delta and message_stop (outer loop manages these)
				if eventType == "message_delta" || eventType == "message_stop" {
					continue
				}

				// Check if this event belongs to the web_search tool_use block
				if wsToolIndex >= 0 {
					if idx, ok := event["index"].(float64); ok && int(idx) == wsToolIndex {
						// Skip events for the web_search tool_use block
						continue
					}
				}

				// Apply index offset for remaining events
				if indexOffset > 0 {
					switch eventType {
					case "content_block_start", "content_block_delta", "content_block_stop":
						if idx, ok := event["index"].(float64); ok {
							event["index"] = int(idx) + indexOffset
							adjusted, err := json.Marshal(event)
							if err == nil {
								resultBuilder.WriteString("data: " + string(adjusted) + "\n")
								hasContent = true
								continue
							}
						}
					}
				}

				resultBuilder.WriteString(line + "\n")
				hasContent = true
			} else if strings.HasPrefix(line, "event: ") {
				// Check if the next data line will be suppressed
				if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "data: ") {
					nextData := strings.TrimPrefix(lines[i+1], "data: ")
					nextData = strings.TrimSpace(nextData)

					var nextEvent map[string]interface{}
					if err := json.Unmarshal([]byte(nextData), &nextEvent); err == nil {
						nextType, _ := nextEvent["type"].(string)
						if nextType == "message_start" || nextType == "message_delta" || nextType == "message_stop" {
							i++ // skip the data line
							continue
						}
						if wsToolIndex >= 0 {
							if idx, ok := nextEvent["index"].(float64); ok && int(idx) == wsToolIndex {
								i++ // skip the data line
								continue
							}
						}
					}
				}
				resultBuilder.WriteString(line + "\n")
				hasContent = true
			} else {
				resultBuilder.WriteString(line + "\n")
				if strings.TrimSpace(line) != "" {
					hasContent = true
				}
			}
		}

		if hasContent {
			filtered = append(filtered, []byte(resultBuilder.String()))
		}
	}

	return filtered
}
