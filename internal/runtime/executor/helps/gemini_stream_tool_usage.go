package helps

import (
	"fmt"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/tidwall/gjson"
)

type geminiFunctionCallCounter struct {
	activeByCandidate map[int64]*translatorcommon.VertexStreamingToolCall
	seenCallIDs       map[string]struct{}
}

// observePart counts logical Gemini/Vertex function calls, deduplicating streamed
// partialArgs continuations, repeated call IDs, and empty separator objects.
func (c *geminiFunctionCallCounter) observePart(candidateIdx int64, call gjson.Result) int64 {
	if c == nil || !call.Exists() || !call.IsObject() {
		return 0
	}
	rawName := strings.TrimSpace(call.Get("name").String())
	upstreamID := strings.TrimSpace(call.Get("id").String())

	if active := c.activeByCandidate[candidateIdx]; active != nil {
		ownedCall := gjson.Parse(strings.Clone(call.Raw))
		if active.CanContinueWith(ownedCall) {
			_, done := active.Assembler.PushFunctionCall(ownedCall)
			if active.Assembler.Err() != nil || done {
				delete(c.activeByCandidate, candidateIdx)
			}
			return 0
		}
	}

	delete(c.activeByCandidate, candidateIdx)

	if translatorcommon.IsVertexStreamingFunctionCall(call) {
		if rawName == "" {
			return 0
		}
		ownCall := gjson.Parse(strings.Clone(call.Raw))
		clonedName := strings.Clone(rawName)
		clonedID := strings.Clone(upstreamID)
		active := translatorcommon.NewVertexStreamingToolCall(clonedName, clonedName, clonedID, clonedID, 0)
		_, done := active.Assembler.PushFunctionCall(ownCall)
		if active.Assembler.Err() != nil {
			return 0
		}
		if !done {
			if c.activeByCandidate == nil {
				c.activeByCandidate = make(map[int64]*translatorcommon.VertexStreamingToolCall)
			}
			c.activeByCandidate[candidateIdx] = active
		}
		return c.countLogicalCall(candidateIdx, clonedID)
	}

	if rawName == "" && upstreamID == "" && !call.Get("args").Exists() {
		return 0
	}
	return c.countLogicalCall(candidateIdx, strings.Clone(upstreamID))
}

func (c *geminiFunctionCallCounter) countLogicalCall(candidateIdx int64, clonedID string) int64 {
	if clonedID != "" {
		key := fmt.Sprintf("%d:%s", candidateIdx, clonedID)
		if c.seenCallIDs == nil {
			c.seenCallIDs = make(map[string]struct{})
		}
		if _, exists := c.seenCallIDs[key]; exists {
			return 0
		}
		c.seenCallIDs[key] = struct{}{}
	}
	return 1
}
