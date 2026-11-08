package helps

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// isMistralUpstreamURL reports whether rawURL targets Mistral's API.
// Mistral validates chat messages with Pydantic extra="forbid", so any
// non-standard assistant field (e.g. reasoning_content) yields HTTP 422.
func isMistralUpstreamURL(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	return host == "mistral.ai" || strings.HasSuffix(host, ".mistral.ai")
}

// upstreamForbidsAssistantReasoningContent reports whether the Chat Completions
// upstream at baseURL rejects reasoning_content on assistant messages.
func upstreamForbidsAssistantReasoningContent(baseURL string) bool {
	return isMistralUpstreamURL(baseURL)
}

// SanitizeAssistantReasoningContentForUpstreamDoc strips reasoning_content from
// assistant messages when the upstream is known to reject it.
func SanitizeAssistantReasoningContentForUpstreamDoc(ctx context.Context, doc *ojson.Doc, baseURL string) {
	if !upstreamForbidsAssistantReasoningContent(baseURL) {
		return
	}
	if stripped, dropped := stripAssistantReasoningContentDoc(doc); stripped > 0 {
		LogWithRequestID(ctx).Debugf(
			"openai compat executor: removed reasoning_content from %d assistant message(s) (%d reasoning-only message(s) dropped) for strict upstream",
			stripped, dropped)
	}
}

// stripAssistantReasoningContentDoc removes reasoning_content from every
// assistant message. Assistant messages that carried nothing but reasoning
// (empty content, no tool_calls, no function_call) are removed entirely,
// because {"role":"assistant","content":""} is itself invalid upstream.
//
// It returns the number of messages touched and how many of those were dropped.
func stripAssistantReasoningContentDoc(doc *ojson.Doc) (stripped, dropped int) {
	if doc == nil {
		return 0, 0
	}
	messages := doc.Get("messages")
	if !messages.IsArray() {
		return 0, 0
	}
	items := messages.Array()
	// Iterate backwards so deleting messages.N never shifts a pending index.
	for i := len(items) - 1; i >= 0; i-- {
		msg := items[i]
		if !strings.EqualFold(strings.TrimSpace(msg.Get("role").String()), "assistant") {
			continue
		}
		if !msg.Get("reasoning_content").Exists() {
			continue
		}
		stripped++
		if isReasoningOnlyAssistantMessage(msg) {
			_ = doc.Delete(fmt.Sprintf("messages.%d", i))
			dropped++
			continue
		}
		_ = doc.Delete(fmt.Sprintf("messages.%d.reasoning_content", i))
	}
	return stripped, dropped
}

// isReasoningOnlyAssistantMessage reports whether msg would be an empty
// assistant turn once reasoning_content is removed.
func isReasoningOnlyAssistantMessage(msg ojson.Result) bool {
	if tc := msg.Get("tool_calls"); tc.IsArray() && len(tc.Array()) > 0 {
		return false
	}
	if fc := msg.Get("function_call"); fc.Exists() && fc.Type != ojson.Null && strings.TrimSpace(fc.Raw) != "" && strings.TrimSpace(fc.Raw) != "{}" {
		return false
	}
	return isEmptyChatMessageContent(msg.Get("content"))
}

func isEmptyChatMessageContent(content ojson.Result) bool {
	switch {
	case !content.Exists() || content.Type == ojson.Null:
		return true
	case content.IsArray():
		for _, part := range content.Array() {
			if !isEmptyChatContentPart(part) {
				return false
			}
		}
		return true
	case content.IsObject():
		return false
	default:
		return strings.TrimSpace(content.String()) == ""
	}
}

func isEmptyChatContentPart(part ojson.Result) bool {
	if !part.Exists() || part.Type == ojson.Null {
		return true
	}
	if part.Type == ojson.String {
		return strings.TrimSpace(part.String()) == ""
	}
	if !part.IsObject() {
		return false
	}
	if text := part.Get("text"); text.Exists() {
		return strings.TrimSpace(text.String()) == ""
	}
	if strings.TrimSpace(part.Get("type").String()) == "text" {
		return true
	}
	return strings.TrimSpace(part.Raw) == "{}"
}
