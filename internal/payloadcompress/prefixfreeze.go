package payloadcompress

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
)

// This file ports upstream OmniRoute's two cache-protection signals:
//
//   - cachingAware: a provider that caches a prompt prefix (or a body carrying
//     explicit cache_control markers) must not have that prefix rewritten.
//   - prefixFreeze: a system prompt seen repeatedly is a stable cached prefix
//     even for a provider the static check does not recognize.
//
// Both exist only to answer one question, upstream's preserveSystemPrompt:
// may the pipeline rewrite the system prompt on this request? Nothing else in
// upstream is skipped for cache reasons, so neither is anything else here.

// prefixFreezeMaxEntries bounds the observation map. A long-lived proxy sees many
// distinct system prompts; an unbounded map is a leak. Oldest entry is evicted
// first, mirroring the upstream cap.
const prefixFreezeMaxEntries = 5_000

var prefixFreeze = struct {
	sync.Mutex
	count map[string]int
	order []string
}{count: make(map[string]int, 64)}

// shouldPreserveSystemPrompt resolves upstream's preserveSystemPromptMode against
// the cache signals for this request. True means the system prompt is part of a
// cached prefix and must reach the provider byte for byte.
func (c Config) shouldPreserveSystemPrompt(model string, body []byte) bool {
	hasCache := hasCacheControl(body) || modelCachesPrefix(model)
	if c.PrefixFreeze.Enabled {
		if hash := systemPromptHash(body); hash != "" && observePrefix(hash, c.PrefixFreeze.Threshold) {
			hasCache = true
		}
	}
	switch c.PreserveSystemPrompt {
	case PreserveSystemPromptNever:
		return false
	case PreserveSystemPromptWhenNoCache:
		return hasCache
	default:
		return true
	}
}

// hasCacheControl reports whether the client marked any breakpoint. Upstream
// walks system/tools/messages/input/contents structurally; a substring probe finds
// the same markers in one pass and cannot miss one hidden in a nested part.
func hasCacheControl(body []byte) bool {
	return bytes.Contains(body, []byte(cacheControlMarker))
}

const cacheControlMarker = `"cache_control"`

// cachingModelPrefixes ports upstream's CACHING_PROVIDERS set for a proxy that
// knows only the model name. It includes the automatic-prefix-cache providers
// (OpenAI, Codex, Azure) that send no cache_control markers at all yet still cache
// the longest matching prefix, which is why upstream added them in #3955.
var cachingModelPrefixes = []string{
	"claude", "anthropic", "zai", "deepseek", "kimi", "mimo",
	"gpt-", "o1-", "o3-", "o4-", "codex", "azure",
	"alibaba", "qwen",
}

// modelCachesPrefix reports whether this model is served by a provider that caches
// a prompt prefix. A miss here only costs the protection, never correctness, so the
// list errs toward recognizing too much.
func modelCachesPrefix(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndexByte(name, '/'); idx >= 0 {
		name = name[idx+1:]
	}
	for _, prefix := range cachingModelPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// systemPromptHash returns a stable hash of the request's system prompt across the
// shapes this proxy receives: OpenAI/Claude `messages[].role=="system"`, Claude
// `system`, and Gemini `systemInstruction`. Empty means there is nothing to freeze.
func systemPromptHash(body []byte) string {
	var b strings.Builder
	appendSystemText(&b, gjson.GetBytes(body, "system"))
	appendSystemText(&b, gjson.GetBytes(body, "systemInstruction"))
	appendSystemText(&b, gjson.GetBytes(body, "system_instruction"))
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() == "system" {
			appendSystemText(&b, msg.Get("content"))
		}
		return true
	})
	if b.Len() == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// appendSystemText collects the text of a bare string, an array of parts, or an
// object carrying text/parts, skipping blank values so an empty prompt never hashes.
func appendSystemText(b *strings.Builder, value gjson.Result) {
	switch {
	case value.Type == gjson.String:
		if text := strings.TrimSpace(value.String()); text != "" {
			b.WriteString(text)
			b.WriteByte('\n')
		}
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			appendSystemText(b, item)
			return true
		})
	case value.IsObject():
		appendSystemText(b, value.Get("text"))
		if parts := value.Get("parts"); parts.Exists() {
			appendSystemText(b, parts)
		}
	}
}

// observePrefix records one sighting and reports whether the prefix is now stable,
// meaning it has been seen at least threshold times.
func observePrefix(hash string, threshold int) bool {
	prefixFreeze.Lock()
	defer prefixFreeze.Unlock()
	if _, seen := prefixFreeze.count[hash]; !seen {
		if len(prefixFreeze.order) >= prefixFreezeMaxEntries {
			oldest := prefixFreeze.order[0]
			prefixFreeze.order = prefixFreeze.order[1:]
			delete(prefixFreeze.count, oldest)
		}
		prefixFreeze.order = append(prefixFreeze.order, hash)
	}
	prefixFreeze.count[hash]++
	return prefixFreeze.count[hash] >= threshold
}

// resetPrefixFreeze clears the observations. Tests and operator resets only.
func resetPrefixFreeze() {
	prefixFreeze.Lock()
	defer prefixFreeze.Unlock()
	prefixFreeze.count = make(map[string]int, 64)
	prefixFreeze.order = nil
}
