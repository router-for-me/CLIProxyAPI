package common

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// DropUnsupportedClaudeAssistantPrefill removes a trailing assistant message for
// Claude model families that reject assistant message prefill (e.g., Fable,
// Opus 5, Sonnet 4.6). Anthropic answers such a request with 400 "This model
// does not support assistant message prefill. The conversation must end with a
// user message."
func DropUnsupportedClaudeAssistantPrefill(modelName string, messages [][]byte) [][]byte {
	if !ClaudeModelRejectsAssistantPrefill(modelName) || len(messages) == 0 {
		return messages
	}
	last := gjson.ParseBytes(messages[len(messages)-1])
	if !strings.EqualFold(strings.TrimSpace(last.Get("role").String()), "assistant") {
		return messages
	}
	return messages[:len(messages)-1]
}

// ClaudeModelRejectsAssistantPrefill reports whether a Claude model family
// disallows trailing assistant prefill in its conversation history.
func ClaudeModelRejectsAssistantPrefill(modelName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	// Provider namespaces are not part of the model family.
	if index := strings.LastIndexByte(normalized, '/'); index >= 0 {
		normalized = normalized[index+1:]
	}
	normalized = strings.TrimPrefix(normalized, "claude-")
	tokens := strings.Split(strings.ReplaceAll(normalized, ".", "-"), "-")
	if tokens[0] == "fable" {
		return true
	}
	if len(tokens) < 2 || (tokens[0] != "opus" && tokens[0] != "sonnet") {
		return false
	}
	parseVersion := func(token string) int {
		// Eight-digit snapshot dates must never be treated as versions.
		if token == "" || len(token) >= 8 {
			return -1
		}
		for _, digit := range token {
			if digit < '0' || digit > '9' {
				return -1
			}
		}
		version, errAtoi := strconv.Atoi(token)
		if errAtoi != nil {
			return -1
		}
		return version
	}
	major := parseVersion(tokens[1])
	if major >= 5 {
		return true
	}
	return tokens[0] == "sonnet" && major == 4 && len(tokens) > 2 && parseVersion(tokens[2]) >= 6
}
