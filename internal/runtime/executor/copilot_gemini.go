package executor

import (
	"fmt"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type geminiReasoningCache struct {
	mu    sync.RWMutex
	cache map[string]*geminiReasoning
}

type geminiReasoning struct {
	Opaque    string
	Text      string
	createdAt time.Time
}

const geminiReasoningTTL = 30 * time.Minute

var (
	sharedGeminiReasoningMu sync.Mutex
	sharedGeminiReasoning   = make(map[string]*geminiReasoningCache)
)

func newGeminiReasoningCache() *geminiReasoningCache {
	return &geminiReasoningCache{
		cache: make(map[string]*geminiReasoning),
	}
}

// getSharedGeminiReasoningCache returns a cache keyed by authID to preserve
// reasoning data across executor re-creations (e.g., after reauth).
func getSharedGeminiReasoningCache(authID string) *geminiReasoningCache {
	if authID == "" {
		return newGeminiReasoningCache()
	}
	sharedGeminiReasoningMu.Lock()
	defer sharedGeminiReasoningMu.Unlock()
	if cache, ok := sharedGeminiReasoning[authID]; ok && cache != nil {
		return cache
	}
	cache := newGeminiReasoningCache()
	sharedGeminiReasoning[authID] = cache
	return cache
}

// EvictCopilotGeminiReasoningCache removes the shared cache for an auth ID when the auth is removed.
func EvictCopilotGeminiReasoningCache(authID string) {
	if authID == "" {
		return
	}
	sharedGeminiReasoningMu.Lock()
	delete(sharedGeminiReasoning, authID)
	sharedGeminiReasoningMu.Unlock()
}

// InjectReasoning inserts cached reasoning fields back into assistant messages
// for tool calls (required by Gemini 3 models).
func (c *geminiReasoningCache) InjectReasoning(body []byte) []byte {
	// Find assistant messages with tool_calls that are missing reasoning fields
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.cache) == 0 {
		log.Debug("copilot executor: no cached Gemini reasoning available")
		return body
	}

	var modified bool
	var msgIdx int
	messages.ForEach(func(_, msg gjson.Result) bool {
		defer func() { msgIdx++ }()
		if msg.Get("role").String() != "assistant" {
			return true
		}
		toolCalls := msg.Get("tool_calls")
		if !toolCalls.Exists() || !toolCalls.IsArray() {
			return true
		}
		// Check if reasoning fields are missing
		if msg.Get("reasoning_opaque").Exists() || msg.Get("reasoning_text").Exists() {
			return true
		}

		// Look up reasoning by the first tool_call's id
		var callID string
		toolCalls.ForEach(func(_, tc gjson.Result) bool {
			if id := tc.Get("id").String(); id != "" {
				callID = id
				return false // stop after first
			}
			return true
		})

		if callID == "" {
			return true
		}

		reasoning := c.cache[callID]
		if reasoning == nil || (reasoning.Opaque == "" && reasoning.Text == "") {
			log.Debugf("copilot executor: no cached reasoning for call_id %s", callID)
			return true
		}

		// Check TTL
		if time.Since(reasoning.createdAt) > geminiReasoningTTL {
			log.Debugf("copilot executor: cached reasoning for call_id %s expired", callID)
			return true
		}

		log.Debugf("copilot executor: injecting reasoning for call_id %s (opaque=%d chars, text=%d chars)", callID, len(reasoning.Opaque), len(reasoning.Text))

		msgPath := fmt.Sprintf("messages.%d", msgIdx)
		if reasoning.Opaque != "" {
			body, _ = sjson.SetBytes(body, msgPath+".reasoning_opaque", reasoning.Opaque)
			modified = true
		}
		if reasoning.Text != "" {
			body, _ = sjson.SetBytes(body, msgPath+".reasoning_text", reasoning.Text)
			modified = true
		}
		return true
	})

	if modified {
		log.Debug("copilot executor: injected cached Gemini reasoning into request")
	}
	return body
}

// CacheReasoning captures reasoning fields from streaming deltas.
func (c *geminiReasoningCache) CacheReasoning(data []byte) {
	delta := gjson.GetBytes(data, "choices.0.delta")
	if !delta.Exists() {
		return
	}

	// Get the call_id from the first tool_call in the delta
	callID := gjson.GetBytes(data, "choices.0.delta.tool_calls.0.id").String()

	opaque := delta.Get("reasoning_opaque").String()
	text := delta.Get("reasoning_text").String()

	if opaque == "" && text == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Lazy eviction: simple random cleanup if cache gets too big
	if len(c.cache) > 1000 {
		now := time.Now()
		for k, v := range c.cache {
			if now.Sub(v.createdAt) > geminiReasoningTTL {
				delete(c.cache, k)
			}
		}
	}

	if callID == "" {
		return
	}

	log.Debugf("copilot executor: caching Gemini reasoning for call_id %s (opaque=%d chars, text=%d chars)", callID, len(opaque), len(text))

	if c.cache[callID] == nil {
		c.cache[callID] = &geminiReasoning{
			createdAt: time.Now(),
		}
	}

	// Only update if we got new values
	if opaque != "" {
		c.cache[callID].Opaque = opaque
	}
	if text != "" {
		// Append text since it comes in chunks
		c.cache[callID].Text += text
	}
	c.cache[callID].createdAt = time.Now()
}
