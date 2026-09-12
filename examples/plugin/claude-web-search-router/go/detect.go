package main

import (
	"strings"

	"github.com/tidwall/gjson"
)

const (
	claudeWebSearchToolTypeA = "web_search_20250305"
	claudeWebSearchToolTypeB = "web_search_20260209"
)

// isClaudeSourceFormat reports whether the inbound protocol is Claude / Anthropic Messages.
func isClaudeSourceFormat(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "claude", "anthropic":
		return true
	default:
		return false
	}
}

func isClaudeTypedWebSearchToolType(toolType string) bool {
	return toolType == claudeWebSearchToolTypeA || toolType == claudeWebSearchToolTypeB
}

func hasClaudeTypedWebSearchTool(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if isClaudeTypedWebSearchToolType(tool.Get("type").String()) {
			return true
		}
	}
	return false
}

func hasOnlyClaudeTypedWebSearchTools(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	hasWebSearch := false
	for _, tool := range tools.Array() {
		if isClaudeTypedWebSearchToolType(tool.Get("type").String()) {
			hasWebSearch = true
			continue
		}
		if tool.Get("type").String() != "" || tool.Get("name").String() != "" {
			return false
		}
	}
	return hasWebSearch
}

func looksLikeClaudeCodeWebSearchAssistant(body []byte) bool {
	system := gjson.GetBytes(body, "system")
	if system.IsArray() {
		for _, block := range system.Array() {
			text := strings.ToLower(block.Get("text").String())
			if strings.Contains(text, "web search tool use") ||
				strings.Contains(text, "performing a web search") {
				return true
			}
		}
	}
	if system.Type == gjson.String {
		text := strings.ToLower(system.String())
		if strings.Contains(text, "web search tool use") {
			return true
		}
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return false
	}
	for _, message := range messages.Array() {
		if message.Get("role").String() != "user" {
			continue
		}
		text := strings.ToLower(extractClaudeMessageText(message.Get("content")))
		if strings.HasPrefix(text, "perform a web search for the query:") {
			return true
		}
	}
	return false
}

func isClaudeCodeBuiltinWebSearchRequest(body []byte, requireWebSearchOnly bool) bool {
	if !hasClaudeTypedWebSearchTool(body) {
		return false
	}
	if requireWebSearchOnly && !hasOnlyClaudeTypedWebSearchTools(body) {
		return false
	}
	return looksLikeClaudeCodeWebSearchAssistant(body) || hasOnlyClaudeTypedWebSearchTools(body)
}

func extractClaudeWebSearchQuery(body []byte) string {
	if q := extractQueryFromPerformPrefix(body); q != "" {
		return q
	}
	return extractQueryFromUserMessages(body)
}

func extractQueryFromPerformPrefix(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return ""
	}
	const prefix = "perform a web search for the query:"
	for _, message := range messages.Array() {
		if message.Get("role").String() != "user" {
			continue
		}
		text := strings.TrimSpace(extractClaudeMessageText(message.Get("content")))
		lower := strings.ToLower(text)
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(text[len(prefix):])
		}
	}
	return ""
}

func extractQueryFromUserMessages(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return ""
	}
	arr := messages.Array()
	for i := len(arr) - 1; i >= 0; i-- {
		message := arr[i]
		role := message.Get("role").String()
		if role != "" && role != "user" {
			continue
		}
		if query := strings.TrimSpace(extractClaudeMessageText(message.Get("content"))); query != "" {
			return query
		}
	}
	return ""
}

func extractClaudeMessageText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var parts []string
	for _, block := range content.Array() {
		if block.Get("type").String() != "text" {
			continue
		}
		if text := strings.TrimSpace(block.Get("text").String()); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func extractClaudeWebSearchMaxUses(body []byte, defaultMax int) int {
	if defaultMax <= 0 {
		defaultMax = 5
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return defaultMax
	}
	for _, tool := range tools.Array() {
		if !isClaudeTypedWebSearchToolType(tool.Get("type").String()) {
			continue
		}
		if maxUses := int(tool.Get("max_uses").Int()); maxUses > 0 {
			return maxUses
		}
	}
	return defaultMax
}
