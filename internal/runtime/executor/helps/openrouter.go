package helps

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	openRouterSessionHeader       = "X-Session-Id"
	openRouterCacheControl        = `{"type":"ephemeral"}`
	openRouterCacheControlOneHour = `{"type":"ephemeral","ttl":"1h"}`
)

// ApplyOpenRouterCacheControlDoc adds Anthropic-style cache_control breakpoints to chat-completions requests sent to
// OpenRouter. OpenRouter caches Claude (and other explicit-cache models) only when the request marks what to cache,
// and the Claude-to-OpenAI translation drops the client's markers, so without this no such request through an
// OpenRouter entry is ever cached. OpenRouter ignores the markers for models that cache automatically.
//
// Placement follows CPA's own Claude breakpoints: the last system message and the latest user or tool message, which
// covers the whole conversation prefix. Requests that already carry cache_control on a message or a tool are left
// alone. A Claude-format client (claudeSource) asking for the 1-hour lifetime in sourcePayloads keeps it.
//
// An OpenAI-format client asking for a cache lifetime (prompt_cache_options.ttl, e.g. "12h") keeps it too: see
// applyOpenRouterPromptCacheOptionsDoc.
func ApplyOpenRouterCacheControlDoc(doc *ojson.Doc, baseURL string, claudeSource bool, sourcePayloads ...[]byte) {
	if doc == nil || !util.IsOpenRouterURL(baseURL) {
		return
	}
	messages := doc.Get("messages")
	if !messages.IsArray() {
		return
	}
	defer applyOpenRouterPromptCacheOptionsDoc(doc, sourcePayloads...)
	if openAIRequestHasCacheControl(doc) {
		return
	}
	cacheControl := openRouterCacheControl
	if claudeSource && ClaudeRequestAsksOneHourCache(sourcePayloads...) {
		cacheControl = openRouterCacheControlOneHour
	}
	lastSystem, lastTurn := -1, -1
	for i, msg := range messages.Array() {
		switch msg.Get("role").String() {
		case "system", "developer":
			lastSystem = i
		case "user", "tool":
			lastTurn = i
		}
	}
	for _, i := range []int{lastSystem, lastTurn} {
		if i >= 0 {
			markOpenAIMessageCacheControlDoc(doc, i, cacheControl)
		}
	}
}

// applyOpenRouterPromptCacheOptionsDoc carries the client's OpenAI cache lifetime to OpenRouter. OpenRouter applies
// prompt_cache_options only to OpenAI models (GPT-5.6 and newer) and only in explicit mode, where the cache_control
// breakpoints decide what is cached; without mode "explicit" the ttl is silently ignored (billed as the 30-minute
// default). The Responses-to-Chat-Completions translation drops the field, so the ttl is read from the translated
// request first and then from the client's original payloads. Nothing is added without a breakpoint to cache.
func applyOpenRouterPromptCacheOptionsDoc(doc *ojson.Doc, sourcePayloads ...[]byte) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(doc.Get("model").String())), "openai/") {
		return
	}
	if !openAIRequestHasCacheControl(doc) {
		return
	}
	ttl := strings.TrimSpace(doc.Get("prompt_cache_options.ttl").String())
	if ttl == "" {
		ttl = RequestPromptCacheTTL(sourcePayloads...)
	}
	if ttl == "" {
		return
	}
	_ = doc.Set("prompt_cache_options.ttl", ttl)
	if strings.TrimSpace(doc.Get("prompt_cache_options.mode").String()) == "" {
		_ = doc.Set("prompt_cache_options.mode", "explicit")
	}
}

// RequestPromptCacheTTL returns the first OpenAI prompt_cache_options.ttl found in payloads, or "".
func RequestPromptCacheTTL(payloads ...[]byte) string {
	for _, payload := range payloads {
		if len(payload) == 0 || !ojson.ValidBytes(payload) {
			continue
		}
		if ttl := strings.TrimSpace(ojson.GetBytes(payload, "prompt_cache_options.ttl").String()); ttl != "" {
			return ttl
		}
	}
	return ""
}

// openAIRequestHasCacheControl reports whether the caller already placed cache_control on a tool (tools[i] or
// tools[i].function) or on a message or one of its parts.
func openAIRequestHasCacheControl(doc *ojson.Doc) bool {
	for _, tool := range doc.Get("tools").Array() {
		if tool.Get("cache_control").Exists() || tool.Get("function.cache_control").Exists() {
			return true
		}
	}
	for _, msg := range doc.Get("messages").Array() {
		if msg.Get("cache_control").Exists() {
			return true
		}
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, part := range content.Array() {
			if part.Get("cache_control").Exists() {
				return true
			}
		}
	}
	return false
}

// markOpenAIMessageCacheControlDoc puts cache_control on the message's last text, image or file part; string content
// becomes a single text part. Messages with nothing to mark stay unchanged.
func markOpenAIMessageCacheControlDoc(doc *ojson.Doc, index int, cacheControl string) {
	path := "messages." + strconv.Itoa(index) + ".content"
	content := doc.Get(path)
	switch {
	case content.Type == ojson.String:
		text := content.String()
		if strings.TrimSpace(text) == "" {
			return
		}
		if doc.SetRaw(path, `[{"type":"text","text":""}]`) != nil {
			return
		}
		_ = doc.Set(path+".0.text", text)
		_ = doc.SetRaw(path+".0.cache_control", cacheControl)
	case content.IsArray():
		parts := content.Array()
		for j := len(parts) - 1; j >= 0; j-- {
			switch parts[j].Get("type").String() {
			case "text":
				if strings.TrimSpace(parts[j].Get("text").String()) == "" {
					continue
				}
			case "image_url", "file":
			default:
				continue
			}
			_ = doc.SetRaw(path+"."+strconv.Itoa(j)+".cache_control", cacheControl)
			return
		}
	}
}

// ClaudeRequestAsksOneHourCache reports whether a Claude Messages request marks the request, a tool, a system block
// or a message block with cache_control ttl "1h".
func ClaudeRequestAsksOneHourCache(payloads ...[]byte) bool {
	oneHour := func(cacheControl ojson.Result) bool {
		return strings.EqualFold(strings.TrimSpace(cacheControl.Get("ttl").String()), "1h")
	}
	for _, payload := range payloads {
		if len(payload) == 0 || !ojson.ValidBytes(payload) {
			continue
		}
		root := ojson.ParseBytes(payload)
		if oneHour(root.Get("cache_control")) {
			return true
		}
		for _, tool := range root.Get("tools").Array() {
			if oneHour(tool.Get("cache_control")) {
				return true
			}
		}
		for _, block := range root.Get("system").Array() {
			if oneHour(block.Get("cache_control")) {
				return true
			}
		}
		for _, msg := range root.Get("messages").Array() {
			for _, block := range msg.Get("content").Array() {
				if oneHour(block.Get("cache_control")) {
					return true
				}
			}
		}
	}
	return false
}

// ApplyOpenRouterSessionHeader sends OpenRouter's session id (X-Session-Id) so the requests of one conversation stay
// on the same upstream provider and keep hitting its cache. A header already on the request or sent by the client
// wins; otherwise the id is CPA's provider session (execution or derived) or, for clients that name their own
// session, a stable id from the canonical session.
func ApplyOpenRouterSessionHeader(r *http.Request, baseURL, provider string, clientHeaders http.Header, metadataSets ...map[string]any) {
	if r == nil || !util.IsOpenRouterURL(baseURL) || strings.TrimSpace(r.Header.Get(openRouterSessionHeader)) != "" {
		return
	}
	if clientID := strings.TrimSpace(clientHeaders.Get(openRouterSessionHeader)); clientID != "" {
		r.Header.Set(openRouterSessionHeader, clientID)
		return
	}
	sessionID := ProviderSessionUUID(provider, metadataSets...)
	if sessionID == "" {
		for _, metadata := range metadataSets {
			if canonical := metadataString(metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey); canonical != "" {
				sessionID = stableProviderSessionUUID(provider, "canonical-session", canonical)
				break
			}
		}
	}
	if sessionID != "" {
		r.Header.Set(openRouterSessionHeader, sessionID)
	}
}
