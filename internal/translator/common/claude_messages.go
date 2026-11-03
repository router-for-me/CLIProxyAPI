package common

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
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
	root := util.ParseGJSONBytesNoCopy(message)
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
	a.role = strings.Clone(role)
	for _, part := range parts {
		if role == "assistant" && ojson.GetBytesNoCopy(part, "type").String() == "tool_use" {
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
		messageDoc := ojson.NewDoc([]byte(`{"role":"","content":[]}`))
		_ = messageDoc.Set("role", a.role)
		_ = messageDoc.SetRawBytes("content", JoinRawArray(parts))
		a.messages = append(a.messages, messageDoc.Bytes())
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

// AlignClaudeToolResults orders tool_result blocks by the preceding tool_use IDs,
// preserving non-result content blocks at their existing indexes. If a complete
// one-to-one match is unavailable, the original content is returned.
func AlignClaudeToolResults(content ojson.Result, toolUseIDs []string) ojson.Result {
	if !content.IsArray() || len(toolUseIDs) == 0 {
		return content
	}

	parts := content.Array()
	toolResults := make([]ojson.Result, 0, len(toolUseIDs))
	toolResultIndices := make([]int, 0, len(toolUseIDs))
	for i, part := range parts {
		if part.Get("type").String() == "tool_result" {
			toolResults = append(toolResults, part)
			toolResultIndices = append(toolResultIndices, i)
		}
	}
	if len(toolResults) != len(toolUseIDs) {
		return content
	}

	reorderedResults := make([]ojson.Result, 0, len(toolUseIDs))
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
		reorderedResults = append(reorderedResults, toolResults[matched])
	}

	ordered := make([][]byte, len(parts))
	for i, part := range parts {
		ordered[i] = []byte(part.Raw)
	}
	for i, slotIndex := range toolResultIndices {
		ordered[slotIndex] = []byte(reorderedResults[i].Raw)
	}
	return util.ParseGJSONBytesNoCopy(JoinRawArray(ordered))
}

func claudeMessageContentParts(content ojson.Result) [][]byte {
	if !content.Exists() || content.Type == ojson.Null {
		return nil
	}
	if content.Type == ojson.String {
		if content.String() == "" {
			return nil
		}
		part := []byte(`{"type":"text","text":""}`)
		part, _ = ojson.SetBytes(part, "text", content.String())
		return [][]byte{part}
	}
	if !content.IsArray() {
		return nil
	}
	parts := make([][]byte, 0, len(content.Array()))
	content.ForEach(func(_, part ojson.Result) bool {
		if part.IsObject() {
			parts = append(parts, []byte(part.Raw))
		}
		return true
	})
	return parts
}
