package helps

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
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
	if content := ojson.GetBytesNoCopy(data, "content"); content.IsArray() {
		content.ForEach(func(_, block ojson.Result) bool {
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
		event := util.ParseGJSONBytesNoCopy(bytes.TrimSpace(line[len("data:"):]))
		if event.Get("type").String() == "content_block_start" {
			recordClaudeWebSearchResultBlock(ctx, sessionKey, event.Get("content_block"), ttl, limit)
		}
	}
}

func recordClaudeWebSearchResultBlock(ctx context.Context, sessionKey string, block ojson.Result, ttl time.Duration, limit int) {
	if block.Get("type").String() != "web_search_tool_result" {
		return
	}
	content := block.Get("content")
	if !content.Exists() || claudeWebSearchResultContentEmpty(content) {
		return
	}
	cache.CacheClaudeWebSearchResultBestEffort(ctx, sessionKey, strings.Clone(block.Get("tool_use_id").String()), []byte(content.Raw), ttl, limit)
}

// RestoreClaudeWebSearchResults fills assistant web_search_tool_result blocks
// whose content is missing or empty with the content stored for sessionKey.
// Blocks that already carry content, and blocks without a stored entry, are
// left unchanged.
func RestoreClaudeWebSearchResults(body []byte, sessionKey string, ttl ...time.Duration) []byte {
	return RestoreClaudeWebSearchResultsContext(context.Background(), body, sessionKey, ttl...)
}

// RestoreClaudeWebSearchResultsDoc fills assistant web_search_tool_result blocks
// in doc whose content is missing or empty with the content stored for sessionKey.
func RestoreClaudeWebSearchResultsDoc(doc *ojson.Doc, sessionKey string, ttl ...time.Duration) bool {
	return RestoreClaudeWebSearchResultsContextDoc(context.Background(), doc, sessionKey, ttl...)
}

// RestoreClaudeWebSearchResultsContext fills assistant web_search_tool_result
// blocks whose content is missing or empty with the content stored for
// sessionKey using ctx for Home KV operations.
func RestoreClaudeWebSearchResultsContext(ctx context.Context, body []byte, sessionKey string, ttl ...time.Duration) []byte {
	if strings.TrimSpace(sessionKey) == "" || !bytes.Contains(body, claudeWebSearchToolResultMarker) {
		return body
	}
	doc := ojson.NewDoc(body)
	if !RestoreClaudeWebSearchResultsContextDoc(ctx, doc, sessionKey, ttl...) {
		return body
	}
	return doc.Bytes()
}

// RestoreClaudeWebSearchResultsContextDoc fills assistant web_search_tool_result
// blocks in doc whose content is missing or empty with the content stored for
// sessionKey using ctx for Home KV operations.
func RestoreClaudeWebSearchResultsContextDoc(ctx context.Context, doc *ojson.Doc, sessionKey string, ttl ...time.Duration) bool {
	if doc == nil || doc.Len() == 0 || strings.TrimSpace(sessionKey) == "" {
		return false
	}
	messages := doc.Get("messages")
	if !messages.IsArray() {
		return false
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
			if errSet := doc.SetRawBytes(fmt.Sprintf("messages.%d.content.%d.content", i, j), cached); errSet != nil {
				log.Debugf("claude web search results: restore messages.%d.content.%d failed: %v", i, j, errSet)
				continue
			}
			restored++
		}
	}
	if restored > 0 {
		log.Debugf("claude web search results: restored %d result block(s) from cache", restored)
		return true
	}
	return false
}

func claudeWebSearchResultContentEmpty(content ojson.Result) bool {
	if !content.Exists() || content.Type == ojson.Null {
		return true
	}
	return content.IsArray() && len(content.Array()) == 0
}
