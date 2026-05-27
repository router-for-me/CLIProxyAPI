package helps

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	deepSeekReasoningCacheTTL     = 30 * time.Minute
	deepSeekReasoningCacheMaxSize = 2048
)

type deepSeekReasoningCacheEntry struct {
	reasoning  string
	expiresAt  time.Time
	lastAccess time.Time
}

var deepSeekReasoningCache = struct {
	sync.Mutex
	entries map[string]deepSeekReasoningCacheEntry
}{
	entries: make(map[string]deepSeekReasoningCacheEntry),
}

// DeepSeekReasoningRecorder captures reasoning_content from DeepSeek V4 responses
// so clients that omit it on the next tool-result request can still be repaired.
type DeepSeekReasoningRecorder struct {
	modelName   string
	reasoning   strings.Builder
	toolCallIDs map[string]struct{}
}

func NewDeepSeekReasoningRecorder(modelName string) *DeepSeekReasoningRecorder {
	if !IsDeepSeekReasoningModel(modelName) {
		return nil
	}
	return &DeepSeekReasoningRecorder{
		modelName:   modelName,
		toolCallIDs: make(map[string]struct{}),
	}
}

func RestoreCachedDeepSeekReasoningContent(modelName string, payload []byte) []byte {
	if !IsDeepSeekReasoningModel(modelName) || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}

	out := payload
	for messageIndex, message := range messages.Array() {
		if strings.TrimSpace(message.Get("role").String()) != "assistant" {
			continue
		}
		if existing := strings.TrimSpace(message.Get("reasoning_content").String()); existing != "" {
			continue
		}
		toolCalls := message.Get("tool_calls")
		if !toolCalls.Exists() || !toolCalls.IsArray() {
			continue
		}
		for _, toolCall := range toolCalls.Array() {
			callID := strings.TrimSpace(toolCall.Get("id").String())
			if callID == "" {
				continue
			}
			if reasoning := lookupDeepSeekReasoning(callID); strings.TrimSpace(reasoning) != "" {
				updated, errSet := sjson.SetBytes(out, fmt.Sprintf("messages.%d.reasoning_content", messageIndex), reasoning)
				if errSet == nil {
					out = updated
				}
				break
			}
		}
	}
	return out
}

func (r *DeepSeekReasoningRecorder) RecordChatCompletionResponse(body []byte) {
	if r == nil || len(body) == 0 || !gjson.ValidBytes(body) {
		return
	}
	for _, choice := range gjson.GetBytes(body, "choices").Array() {
		message := choice.Get("message")
		if reasoning := message.Get("reasoning_content").String(); strings.TrimSpace(reasoning) != "" {
			r.appendReasoning(reasoning)
		}
		r.recordToolCalls(message.Get("tool_calls"))
	}
	r.flush()
}

func (r *DeepSeekReasoningRecorder) RecordChatCompletionStreamLine(line []byte) {
	if r == nil {
		return
	}
	trimmed := bytes.TrimSpace(line)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
	}
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) || !gjson.ValidBytes(trimmed) {
		return
	}
	for _, choice := range gjson.GetBytes(trimmed, "choices").Array() {
		delta := choice.Get("delta")
		if reasoning := delta.Get("reasoning_content").String(); strings.TrimSpace(reasoning) != "" {
			r.appendReasoning(reasoning)
		}
		r.recordToolCalls(delta.Get("tool_calls"))
	}
	r.flush()
}

func (r *DeepSeekReasoningRecorder) appendReasoning(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	r.reasoning.WriteString(text)
}

func (r *DeepSeekReasoningRecorder) recordToolCalls(toolCalls gjson.Result) {
	if !toolCalls.Exists() || !toolCalls.IsArray() {
		return
	}
	for _, toolCall := range toolCalls.Array() {
		callID := strings.TrimSpace(toolCall.Get("id").String())
		if callID != "" {
			r.toolCallIDs[callID] = struct{}{}
		}
	}
}

func (r *DeepSeekReasoningRecorder) flush() {
	reasoning := r.reasoning.String()
	if strings.TrimSpace(reasoning) == "" || len(r.toolCallIDs) == 0 {
		return
	}
	for callID := range r.toolCallIDs {
		storeDeepSeekReasoning(callID, reasoning)
	}
}

func lookupDeepSeekReasoning(callID string) string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return ""
	}
	now := time.Now()
	deepSeekReasoningCache.Lock()
	defer deepSeekReasoningCache.Unlock()
	entry, ok := deepSeekReasoningCache.entries[callID]
	if !ok || now.After(entry.expiresAt) {
		delete(deepSeekReasoningCache.entries, callID)
		return ""
	}
	entry.lastAccess = now
	deepSeekReasoningCache.entries[callID] = entry
	return entry.reasoning
}

func storeDeepSeekReasoning(callID, reasoning string) {
	callID = strings.TrimSpace(callID)
	if callID == "" || strings.TrimSpace(reasoning) == "" {
		return
	}
	now := time.Now()
	deepSeekReasoningCache.Lock()
	defer deepSeekReasoningCache.Unlock()
	deepSeekReasoningCache.entries[callID] = deepSeekReasoningCacheEntry{
		reasoning:  reasoning,
		expiresAt:  now.Add(deepSeekReasoningCacheTTL),
		lastAccess: now,
	}
	pruneDeepSeekReasoningCacheLocked(now)
}

func pruneDeepSeekReasoningCacheLocked(now time.Time) {
	for callID, entry := range deepSeekReasoningCache.entries {
		if now.After(entry.expiresAt) {
			delete(deepSeekReasoningCache.entries, callID)
		}
	}
	for len(deepSeekReasoningCache.entries) > deepSeekReasoningCacheMaxSize {
		var oldestID string
		var oldest time.Time
		for callID, entry := range deepSeekReasoningCache.entries {
			if oldestID == "" || entry.lastAccess.Before(oldest) {
				oldestID = callID
				oldest = entry.lastAccess
			}
		}
		if oldestID == "" {
			return
		}
		delete(deepSeekReasoningCache.entries, oldestID)
	}
}
