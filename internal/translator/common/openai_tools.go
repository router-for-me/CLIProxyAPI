package common

import (
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

// AlignOpenAIToolCallMessages reorders tool result messages to immediately follow
// the assistant message that issued their matching tool_calls by tool_call_id.
// It preserves original message order, content parts, reasoning fields, and numeric
// precision, while leaving ambiguous, orphan, and incomplete histories untouched.
func AlignOpenAIToolCallMessages(messages [][]byte, extraAmbiguousIDs ...string) [][]byte {
	if len(messages) <= 1 {
		return messages
	}

	type assistantRecord struct {
		msgIndex            int
		callIDs             []string
		hasInvalidOrEmptyID bool
	}

	assistants := make([]assistantRecord, 0)
	assistantByCallID := make(map[string]int)
	ambiguousCallIDs := make(map[string]bool)
	for _, id := range extraAmbiguousIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			ambiguousCallIDs[trimmed] = true
		}
	}
	toolMsgIndicesByCallID := make(map[string][]int)

	for i, raw := range messages {
		role := gjson.GetBytes(raw, "role").String()
		switch role {
		case "assistant":
			toolCalls := gjson.GetBytes(raw, "tool_calls")
			if toolCalls.Exists() && toolCalls.IsArray() {
				rawCalls := toolCalls.Array()
				if len(rawCalls) > 0 {
					callIDs := make([]string, 0, len(rawCalls))
					hasEmptyCallID := false
					for _, tc := range rawCalls {
						callID := tc.Get("id").String()
						if callID == "" {
							// Empty tool_call_id cannot be safely matched.
							ambiguousCallIDs[""] = true
							hasEmptyCallID = true
							continue
						}
						if _, exists := assistantByCallID[callID]; exists {
							ambiguousCallIDs[callID] = true
						}
						assistantByCallID[callID] = i
						callIDs = append(callIDs, callID)
					}
					if len(callIDs) > 0 || hasEmptyCallID {
						assistants = append(assistants, assistantRecord{
							msgIndex:            i,
							callIDs:             callIDs,
							hasInvalidOrEmptyID: hasEmptyCallID,
						})
					}
				}
			}

		case "tool":
			callID := gjson.GetBytes(raw, "tool_call_id").String()
			if callID == "" {
				ambiguousCallIDs[""] = true
			} else {
				toolMsgIndicesByCallID[callID] = append(toolMsgIndicesByCallID[callID], i)
				if len(toolMsgIndicesByCallID[callID]) > 1 {
					ambiguousCallIDs[callID] = true
				}
			}
		}
	}

	if len(assistants) == 0 {
		return messages
	}

	type reorderGroup struct {
		assistantIndex int
		toolIndices    []int
	}

	groups := make([]reorderGroup, 0)
	needsReorder := false

	for _, ast := range assistants {
		if ast.hasInvalidOrEmptyID {
			continue
		}
		// Verify completeness and ambiguity for this assistant.
		isEligible := true
		matchedToolIndices := make([]int, 0, len(ast.callIDs))

		for _, callID := range ast.callIDs {
			if ambiguousCallIDs[callID] {
				isEligible = false
				break
			}
			indices := toolMsgIndicesByCallID[callID]
			if len(indices) != 1 {
				// Incomplete or orphan: exactly one tool message must match.
				isEligible = false
				break
			}
			toolIdx := indices[0]
			if toolIdx <= ast.msgIndex {
				// Causal order violation: tool result before assistant.
				isEligible = false
				break
			}
			matchedToolIndices = append(matchedToolIndices, toolIdx)
		}

		if !isEligible {
			continue
		}

		// Sort tool message indices to preserve their relative order.
		sort.Ints(matchedToolIndices)

		// Check if tool messages already immediately follow this assistant.
		alreadyAdjacent := true
		for offset, toolIdx := range matchedToolIndices {
			if toolIdx != ast.msgIndex+offset+1 {
				alreadyAdjacent = false
				break
			}
		}

		if !alreadyAdjacent {
			needsReorder = true
			groups = append(groups, reorderGroup{
				assistantIndex: ast.msgIndex,
				toolIndices:    matchedToolIndices,
			})
		}
	}

	if !needsReorder {
		return messages
	}

	movedToolIndices := make(map[int]bool)
	toolsToInsert := make(map[int][][]byte)

	for _, g := range groups {
		toolList := make([][]byte, 0, len(g.toolIndices))
		for _, idx := range g.toolIndices {
			movedToolIndices[idx] = true
			toolList = append(toolList, messages[idx])
		}
		toolsToInsert[g.assistantIndex] = toolList
	}

	reordered := make([][]byte, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		if movedToolIndices[i] {
			continue
		}
		reordered = append(reordered, messages[i])
		if tools, ok := toolsToInsert[i]; ok {
			reordered = append(reordered, tools...)
		}
	}

	return reordered
}
