package routing

import (
	"strings"

	"github.com/tidwall/gjson"
)

// RequestSignals holds extracted features from a request body for rule evaluation.
type RequestSignals struct {
	MessageCount        int
	LastUserMessage     string
	LastUserMessageLen  int
	TotalMessageLength  int
	HasCodeBlocks       bool
	HasToolBlocks       bool
	SystemPrompt        string
	RequestedModel      string
}

// Analyze extracts routing signals from a raw JSON request body.
func Analyze(rawJSON []byte, modelName string) RequestSignals {
	signals := RequestSignals{
		RequestedModel: modelName,
	}

	messages := gjson.GetBytes(rawJSON, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return signals
	}

	msgs := messages.Array()
	signals.MessageCount = len(msgs)

	for _, msg := range msgs {
		role := msg.Get("role").String()
		content := extractContentText(msg)

		signals.TotalMessageLength += len(content)

		if role == "system" && signals.SystemPrompt == "" {
			signals.SystemPrompt = content
		}

		if role == "user" {
			signals.LastUserMessage = content
			signals.LastUserMessageLen = len(content)
		}

		// Check for tool blocks in content array
		if !signals.HasToolBlocks {
			contentArr := msg.Get("content")
			if contentArr.IsArray() {
				for _, block := range contentArr.Array() {
					blockType := block.Get("type").String()
					if blockType == "tool_use" || blockType == "tool_result" ||
						blockType == "tool_call" || blockType == "function" {
						signals.HasToolBlocks = true
						break
					}
				}
			}
		}
	}

	signals.HasCodeBlocks = strings.Contains(signals.LastUserMessage, "```")

	return signals
}

// extractContentText gets the text content from a message, handling both
// string content and array-of-blocks content formats.
func extractContentText(msg gjson.Result) string {
	content := msg.Get("content")
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var sb strings.Builder
		for _, block := range content.Array() {
			if block.Get("type").String() == "text" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(block.Get("text").String())
			}
		}
		return sb.String()
	}
	return ""
}
