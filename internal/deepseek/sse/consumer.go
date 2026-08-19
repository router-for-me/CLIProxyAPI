package sse

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/protocol"
)

// CollectResult holds the aggregated text and thinking content from a
// DeepSeek SSE stream, consumed to completion (non-streaming use case).
type CollectResult struct {
	Text                  string
	Thinking              string
	ToolDetectionThinking string
	ContentFilter         bool
	UpstreamError         string
	CitationLinks         map[int]string
	ResponseMessageID     int
}

// CollectStream fully consumes a DeepSeek SSE response and separates
// thinking content from text content.
//
// The caller is responsible for closing resp.Body unless closeBody is true.
func CollectStream(resp *http.Response, thinkingEnabled bool, closeBody bool) CollectResult {
	if closeBody {
		defer func() { _ = resp.Body.Close() }()
	}
	text := strings.Builder{}
	thinking := strings.Builder{}
	toolDetectionThinking := strings.Builder{}
	contentFilter := false
	upstreamError := ""
	stopped := false
	collector := newCitationLinkCollector()
	responseMessageID := 0
	currentType := "text"
	if thinkingEnabled {
		currentType = "thinking"
	}
	_ = protocol.ScanSSELines(resp, func(line []byte) bool {
		chunk, done, parsed := ParseDeepSeekSSELine(line)
		if parsed && !done {
			collector.ingestChunk(chunk)
			observeResponseMessageID(chunk, &responseMessageID)
		}
		if done {
			return false
		}
		if stopped {
			return true
		}
		result := ParseDeepSeekContentLine(line, thinkingEnabled, currentType)
		currentType = result.NextType
		if !result.Parsed {
			return true
		}
		if result.Stop {
			if result.ContentFilter {
				contentFilter = true
			}
			if result.ErrorMessage != "" && upstreamError == "" {
				upstreamError = result.ErrorMessage
			}
			// Keep scanning to collect late-arriving citation metadata lines
			// that can appear after response/status=FINISHED, but stop as soon
			// as [DONE] arrives.
			stopped = true
			return true
		}
		for _, p := range result.Parts {
			if p.Type == "thinking" {
				trimmed := TrimContinuationOverlap(thinking.String(), p.Text)
				thinking.WriteString(trimmed)
			} else {
				trimmed := TrimContinuationOverlap(text.String(), p.Text)
				text.WriteString(trimmed)
			}
		}
		for _, p := range result.ToolDetectionThinkingParts {
			trimmed := TrimContinuationOverlap(toolDetectionThinking.String(), p.Text)
			toolDetectionThinking.WriteString(trimmed)
		}
		return true
	})
	return CollectResult{
		Text:                  text.String(),
		Thinking:              thinking.String(),
		ToolDetectionThinking: toolDetectionThinking.String(),
		ContentFilter:         contentFilter,
		UpstreamError:         upstreamError,
		CitationLinks:         collector.build(),
		ResponseMessageID:     responseMessageID,
	}
}

// observeResponseMessageID extracts the response_message_id from a parsed SSE
// chunk. It checks top-level response_message_id, v.response.message_id, and
// message.response.message_id.
func observeResponseMessageID(chunk map[string]any, out *int) {
	if chunk == nil || out == nil {
		return
	}
	if id := intFrom(chunk["response_message_id"]); id > 0 {
		*out = id
	}
	v, _ := chunk["v"].(map[string]any)
	if response, _ := v["response"].(map[string]any); response != nil {
		if id := intFrom(response["message_id"]); id > 0 {
			*out = id
		}
	}
	if message, _ := chunk["message"].(map[string]any); message != nil {
		if response, _ := message["response"].(map[string]any); response != nil {
			if id := intFrom(response["message_id"]); id > 0 {
				*out = id
			}
		}
	}
}

// intFrom coerces common JSON numeric representations into an int.
// Local to the sse package (replaces ds2api's util.IntFrom).
func intFrom(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}
