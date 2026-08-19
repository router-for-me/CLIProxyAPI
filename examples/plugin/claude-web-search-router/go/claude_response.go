package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type claudeStreamBuilder struct {
	model       string
	messageID   string
	toolUseID   string
	index       int
	inputTokens int
}

func newClaudeStreamBuilder(model string) *claudeStreamBuilder {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	now := time.Now().UnixNano()
	return &claudeStreamBuilder{
		model:       model,
		messageID:   fmt.Sprintf("msg_%x", now),
		toolUseID:   fmt.Sprintf("srvtoolu_%d", now),
		inputTokens: 85,
	}
}

func (b *claudeStreamBuilder) buildStreamWithQuery(query string, hits []claudeWebSearchHit, answer string) []byte {
	var chunks []string
	chunks = append(chunks, b.event("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": b.messageID, "type": "message", "role": "assistant", "content": []any{},
			"model": b.model, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": b.inputTokens, "output_tokens": 0},
		},
	}))
	chunks = append(chunks, b.blockStart(b.index, map[string]any{
		"type": "server_tool_use", "id": b.toolUseID, "name": "web_search", "input": map[string]any{},
	}))
	partial, _ := json.Marshal(map[string]string{"query": query})
	chunks = append(chunks, b.event("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": b.index,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": string(partial)},
	}))
	chunks = append(chunks, b.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": b.index}))
	b.index++

	resultContent := webSearchResultBlocks(hits)
	chunks = append(chunks, b.blockStart(b.index, map[string]any{
		"type": "web_search_tool_result", "tool_use_id": b.toolUseID, "content": resultContent,
	}))
	chunks = append(chunks, b.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": b.index}))
	b.index++

	text := composeAnswerText(answer, hits)
	outputTokens := estimateTokens(text)
	chunks = append(chunks, b.blockStart(b.index, map[string]any{"type": "text", "text": ""}))
	chunks = append(chunks, b.event("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": b.index,
		"delta": map[string]any{"type": "text_delta", "text": text},
	}))
	chunks = append(chunks, b.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": b.index}))

	chunks = append(chunks, b.event("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{
			"input_tokens": b.inputTokens, "output_tokens": outputTokens,
			"server_tool_use": map[string]any{"web_search_requests": 1},
		},
	}))
	chunks = append(chunks, b.event("message_stop", map[string]any{"type": "message_stop"}))
	return []byte(strings.Join(chunks, ""))
}

func (b *claudeStreamBuilder) buildMessageJSON(query string, hits []claudeWebSearchHit, answer string) []byte {
	text := composeAnswerText(answer, hits)
	content := []map[string]any{
		{"type": "server_tool_use", "id": b.toolUseID, "name": "web_search", "input": map[string]string{"query": query}},
		{"type": "web_search_tool_result", "tool_use_id": b.toolUseID, "content": webSearchResultBlocks(hits)},
		{"type": "text", "text": text},
	}
	out := map[string]any{
		"id": b.messageID, "type": "message", "role": "assistant", "model": b.model,
		"content": content, "stop_reason": "end_turn", "stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens": b.inputTokens, "output_tokens": estimateTokens(text),
			"server_tool_use": map[string]any{"web_search_requests": 1},
		},
	}
	raw, _ := json.Marshal(out)
	return raw
}

func webSearchResultBlocks(hits []claudeWebSearchHit) []map[string]any {
	resultContent := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		title := hit.Title
		if title == "" {
			title = hostFromURL(hit.URL)
		}
		resultContent = append(resultContent, map[string]any{
			"type": "web_search_result", "title": title, "url": hit.URL, "page_age": nil,
		})
	}
	return resultContent
}

func (b *claudeStreamBuilder) event(eventType string, data map[string]any) string {
	raw, _ := json.Marshal(data)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(raw))
}

func (b *claudeStreamBuilder) blockStart(index int, block map[string]any) string {
	return b.event("content_block_start", map[string]any{
		"type": "content_block_start", "index": index, "content_block": block,
	})
}

func composeAnswerText(answer string, hits []claudeWebSearchHit) string {
	if strings.TrimSpace(answer) != "" {
		return answer
	}
	if len(hits) == 0 {
		return "No web search results were returned."
	}
	var buf strings.Builder
	for i, hit := range hits {
		if i > 0 {
			buf.WriteString("\n\n")
		}
		if hit.Title != "" {
			buf.WriteString(hit.Title)
			buf.WriteString("\n")
		}
		if hit.URL != "" {
			buf.WriteString(hit.URL)
			buf.WriteString("\n")
		}
		if hit.Snippet != "" {
			buf.WriteString(hit.Snippet)
		}
	}
	return buf.String()
}

func hostFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	withoutScheme := raw
	if idx := strings.Index(raw, "://"); idx >= 0 {
		withoutScheme = raw[idx+3:]
	}
	if slash := strings.Index(withoutScheme, "/"); slash >= 0 {
		return withoutScheme[:slash]
	}
	return withoutScheme
}

func estimateTokens(text string) int {
	n := len([]rune(text)) / 4
	if n < 1 {
		return 1
	}
	return n
}
