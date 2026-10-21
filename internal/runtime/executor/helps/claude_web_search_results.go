package helps

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var claudeWebSearchToolResultMarker = []byte(`"web_search_tool_result"`)

// RecordClaudeWebSearchResults stores the web_search_tool_result blocks of a
// Claude response for sessionKey. data is either a complete Messages response
// or SSE text (one or more lines); the result block arrives whole in its
// content_block_start event.
func RecordClaudeWebSearchResults(sessionKey string, data []byte, ttl time.Duration, limit int) {
	RecordClaudeWebSearchResultsContext(context.Background(), sessionKey, data, ttl, limit)
}

// RecordClaudeWebSearchResultsContext stores the web_search_tool_result blocks
// of a Claude response for sessionKey using ctx for Home KV operations.
func RecordClaudeWebSearchResultsContext(ctx context.Context, sessionKey string, data []byte, ttl time.Duration, limit int) {
	if strings.TrimSpace(sessionKey) == "" || !bytes.Contains(data, claudeWebSearchToolResultMarker) {
		return
	}
	if content := gjson.GetBytes(data, "content"); content.IsArray() {
		content.ForEach(func(_, block gjson.Result) bool {
			recordClaudeWebSearchResultBlock(ctx, sessionKey, block, ttl, limit)
			return true
		})
		return
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		event := gjson.ParseBytes(bytes.TrimSpace(line[len("data:"):]))
		if event.Get("type").String() == "content_block_start" {
			recordClaudeWebSearchResultBlock(ctx, sessionKey, event.Get("content_block"), ttl, limit)
		}
	}
}

func recordClaudeWebSearchResultBlock(ctx context.Context, sessionKey string, block gjson.Result, ttl time.Duration, limit int) {
	if block.Get("type").String() != "web_search_tool_result" {
		return
	}
	content := block.Get("content")
	if !content.Exists() || claudeWebSearchResultContentEmpty(content) {
		return
	}
	cache.CacheClaudeWebSearchResultBestEffort(ctx, sessionKey, block.Get("tool_use_id").String(), []byte(content.Raw), ttl, limit)
}

// RestoreClaudeWebSearchResults fills assistant web_search_tool_result blocks
// whose content is missing or empty with the content stored for sessionKey.
// Blocks that already carry content, and blocks without a stored entry, are
// left unchanged.
func RestoreClaudeWebSearchResults(body []byte, sessionKey string, ttl ...time.Duration) []byte {
	return RestoreClaudeWebSearchResultsContext(context.Background(), body, sessionKey, ttl...)
}

// RestoreClaudeWebSearchResultsContext fills assistant web_search_tool_result
// blocks whose content is missing or empty with the content stored for
// sessionKey using ctx for Home KV operations.
func RestoreClaudeWebSearchResultsContext(ctx context.Context, body []byte, sessionKey string, ttl ...time.Duration) []byte {
	if strings.TrimSpace(sessionKey) == "" || !bytes.Contains(body, claudeWebSearchToolResultMarker) {
		return body
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	restored := 0
	for i, message := range messages.Array() {
		if message.Get("role").String() != "assistant" {
			continue
		}
		for j, block := range message.Get("content").Array() {
			if block.Get("type").String() != "web_search_tool_result" || !claudeWebSearchResultContentEmpty(block.Get("content")) {
				continue
			}
			cached, ok, errGet := cache.GetClaudeWebSearchResultRequired(ctx, sessionKey, block.Get("tool_use_id").String(), ttl...)
			if errGet != nil || !ok {
				continue
			}
			updated, errSet := sjson.SetRawBytes(body, fmt.Sprintf("messages.%d.content.%d.content", i, j), cached)
			if errSet != nil {
				log.Debugf("claude web search results: restore messages.%d.content.%d failed: %v", i, j, errSet)
				continue
			}
			body = updated
			restored++
		}
	}
	if restored > 0 {
		log.Debugf("claude web search results: restored %d result block(s) from cache", restored)
	}
	return body
}

func claudeWebSearchResultContentEmpty(content gjson.Result) bool {
	if !content.Exists() || content.Type == gjson.Null {
		return true
	}
	return content.IsArray() && len(content.Array()) == 0
}
