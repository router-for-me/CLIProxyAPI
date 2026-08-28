package common

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ClaudeMessageAccumulator groups consecutive Claude messages by role.
type ClaudeMessageAccumulator struct {
	messages     [][]byte
	role         string
	content      [][]byte
	toolUseParts [][]byte
}

// NewClaudeMessageAccumulator creates an accumulator sized for the expected messages.
func NewClaudeMessageAccumulator(capacity int) *ClaudeMessageAccumulator {
	return &ClaudeMessageAccumulator{
		messages: NewRawArrayItems(int64(capacity)),
	}
}

// Append adds one Claude-shaped message to the current role turn.
func (a *ClaudeMessageAccumulator) Append(message []byte) {
	if len(message) == 0 {
		return
	}
	root := gjson.ParseBytes(message)
	role := root.Get("role").String()
	if role != "user" && role != "assistant" {
		return
	}
	parts := claudeMessageContentParts(root.Get("content"))
	if len(parts) == 0 {
		return
	}
	if a.role != "" && a.role != role {
		a.Flush()
	}
	a.role = role
	for _, part := range parts {
		if role == "assistant" && gjson.GetBytes(part, "type").String() == "tool_use" {
			a.toolUseParts = append(a.toolUseParts, part)
			continue
		}
		a.content = append(a.content, part)
	}
}

// Flush closes the current role turn while keeping accumulated messages.
func (a *ClaudeMessageAccumulator) Flush() {
	if a.role == "" {
		return
	}
	parts := a.content
	if len(a.toolUseParts) > 0 {
		combined := make([][]byte, 0, len(a.content)+len(a.toolUseParts))
		combined = append(combined, a.content...)
		combined = append(combined, a.toolUseParts...)
		parts = combined
	}
	if len(parts) > 0 {
		message := []byte(`{"role":"","content":[]}`)
		message, _ = sjson.SetBytes(message, "role", a.role)
		message, _ = sjson.SetRawBytes(message, "content", JoinRawArray(parts))
		a.messages = append(a.messages, message)
	}
	a.role = ""
	a.content = nil
	a.toolUseParts = nil
}

// Messages flushes the final turn and returns all accumulated messages.
func (a *ClaudeMessageAccumulator) Messages() [][]byte {
	a.Flush()
	return a.messages
}

// AlignClaudeToolResults orders tool_result blocks by the preceding tool_use IDs.
// Other content blocks retain their relative order after the tool results. If a
// complete one-to-one match is unavailable, the original content is returned.
func AlignClaudeToolResults(content gjson.Result, toolUseIDs []string) gjson.Result {
	if !content.IsArray() || len(toolUseIDs) == 0 {
		return content
	}

	parts := content.Array()
	toolResults := make([]gjson.Result, 0, len(toolUseIDs))
	otherParts := make([]gjson.Result, 0, len(parts))
	for _, part := range parts {
		if part.Get("type").String() == "tool_result" {
			toolResults = append(toolResults, part)
			continue
		}
		otherParts = append(otherParts, part)
	}
	if len(toolResults) != len(toolUseIDs) {
		return content
	}

	ordered := make([][]byte, 0, len(parts))
	used := make([]bool, len(toolResults))
	for _, toolUseID := range toolUseIDs {
		matched := -1
		for resultIndex, toolResult := range toolResults {
			if !used[resultIndex] && toolUseID != "" && toolResult.Get("tool_use_id").String() == toolUseID {
				matched = resultIndex
				break
			}
		}
		if matched < 0 {
			return content
		}
		used[matched] = true
		ordered = append(ordered, []byte(toolResults[matched].Raw))
	}
	for _, part := range otherParts {
		ordered = append(ordered, []byte(part.Raw))
	}
	return gjson.ParseBytes(JoinRawArray(ordered))
}

func claudeMessageContentParts(content gjson.Result) [][]byte {
	if !content.Exists() || content.Type == gjson.Null {
		return nil
	}
	if content.Type == gjson.String {
		if content.String() == "" {
			return nil
		}
		part := []byte(`{"type":"text","text":""}`)
		part, _ = sjson.SetBytes(part, "text", content.String())
		return [][]byte{part}
	}
	if !content.IsArray() {
		return nil
	}
	parts := make([][]byte, 0, len(content.Array()))
	content.ForEach(func(_, part gjson.Result) bool {
		if part.IsObject() {
			parts = append(parts, []byte(part.Raw))
		}
		return true
	})
	return parts
}
