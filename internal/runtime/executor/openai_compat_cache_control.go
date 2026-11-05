package executor

import (
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// applyOpenAICompatCacheControlDoc adds Anthropic-style cache_control breakpoints to a chat-completions request for
// providers that opted in (support-cache-control). Upstreams such as OpenRouter cache Claude only when the request
// marks what to cache, and the Claude-to-OpenAI translation drops the client's markers, so without this no
// request through such a provider is ever cached.
//
// Placement follows CPA's own Claude breakpoints: the last system message and the latest user or tool message, which
// covers the whole conversation prefix. Requests that already carry cache_control are left alone. A Claude-format
// client asking for the 1-hour lifetime keeps it.
func applyOpenAICompatCacheControlDoc(compat *config.OpenAICompatibility, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, doc *ojson.Doc) {
	if doc == nil || compat == nil || !compat.SupportCacheControl {
		return
	}
	messages := doc.Get("messages")
	if !messages.IsArray() || openAIMessagesHaveCacheControl(messages) {
		return
	}
	cacheControl := `{"type":"ephemeral"}`
	if sourceFormatEqual(from, sdktranslator.FormatClaude) && claudeRequestAsksOneHourCache(req.Payload, opts.OriginalRequest) {
		cacheControl = `{"type":"ephemeral","ttl":"1h"}`
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

func openAIMessagesHaveCacheControl(messages ojson.Result) bool {
	for _, msg := range messages.Array() {
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

// markOpenAIMessageCacheControlDoc puts cache_control on the message's last text or image part; string content
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
		part, err := ojson.SetBytes([]byte(`{"type":"text","text":""}`), "text", text)
		if err != nil {
			return
		}
		part, err = ojson.SetRawBytes(part, "cache_control", []byte(cacheControl))
		if err != nil {
			return
		}
		_ = doc.SetRawBytes(path, append(append([]byte("["), part...), ']'))
	case content.IsArray():
		parts := content.Array()
		for j := len(parts) - 1; j >= 0; j-- {
			switch parts[j].Get("type").String() {
			case "text":
				if strings.TrimSpace(parts[j].Get("text").String()) == "" {
					continue
				}
			case "image_url":
			default:
				continue
			}
			_ = doc.SetRawBytes(path+"."+strconv.Itoa(j)+".cache_control", []byte(cacheControl))
			return
		}
	}
}

// claudeRequestAsksOneHourCache reports whether a Claude-format request marks any block with ttl "1h".
func claudeRequestAsksOneHourCache(payloads ...[]byte) bool {
	for _, payload := range payloads {
		if len(payload) == 0 || !ojson.ValidBytes(payload) {
			continue
		}
		found := false
		forEachClaudeCacheControlPathDoc(ojson.NewDoc(payload), func(_ string, cacheControl ojson.Result) bool {
			if strings.EqualFold(strings.TrimSpace(cacheControl.Get("ttl").String()), "1h") {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}
