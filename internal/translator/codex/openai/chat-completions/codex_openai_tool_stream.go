package chat_completions

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type toolCallStreamState struct {
	chatIndex        int
	itemID           string
	outputIndex      int64
	hasOutputIndex   bool
	callID           string
	name             string
	family           toolFamily
	announced        bool
	emittedInput     string
	bufferedInput    string
	completeInput    string
	hasCompleteInput bool
	itemDone         bool
}

type streamToolCallTracker struct {
	nextChatIndex int
	byItemID      map[string]*toolCallStreamState
	byOutputIndex map[int64]*toolCallStreamState
	ordered       []*toolCallStreamState
}

func newStreamToolCallTracker() *streamToolCallTracker {
	return &streamToolCallTracker{
		byItemID:      make(map[string]*toolCallStreamState),
		byOutputIndex: make(map[int64]*toolCallStreamState),
	}
}

func (tracker *streamToolCallTracker) ensure() {
	if tracker.byItemID == nil {
		tracker.byItemID = make(map[string]*toolCallStreamState)
	}
	if tracker.byOutputIndex == nil {
		tracker.byOutputIndex = make(map[int64]*toolCallStreamState)
	}
}

func (tracker *streamToolCallTracker) newState(family toolFamily) *toolCallStreamState {
	tracker.ensure()
	state := &toolCallStreamState{
		chatIndex: tracker.nextChatIndex,
		family:    family,
	}
	tracker.nextChatIndex++
	tracker.ordered = append(tracker.ordered, state)
	return state
}

func (tracker *streamToolCallTracker) stateForEvent(root, item gjson.Result, family toolFamily, create, forceNew bool) *toolCallStreamState {
	tracker.ensure()
	itemID := root.Get("item_id").String()
	if itemID == "" {
		itemID = item.Get("id").String()
	}
	outputIndexResult := root.Get("output_index")
	callID := item.Get("call_id").String()

	var state *toolCallStreamState
	if itemID != "" {
		state = tracker.byItemID[itemID]
	}
	if state == nil && outputIndexResult.Exists() {
		state = tracker.byOutputIndex[outputIndexResult.Int()]
	}
	if state == nil && callID != "" {
		state = tracker.uniqueStateByCallID(callID)
	}
	if state == nil && !forceNew {
		activeState, activeCount := tracker.activeState(family)
		if activeCount == 1 {
			state = activeState
		} else if activeCount > 1 && itemID == "" && !outputIndexResult.Exists() && callID == "" {
			return nil
		}
	}
	if state == nil && create {
		state = tracker.newState(family)
	}
	if state == nil {
		return nil
	}

	state.family = family
	if itemID != "" {
		state.itemID = itemID
		tracker.byItemID[itemID] = state
	}
	if outputIndexResult.Exists() {
		state.outputIndex = outputIndexResult.Int()
		state.hasOutputIndex = true
		tracker.byOutputIndex[state.outputIndex] = state
	}
	if callID != "" {
		state.callID = callID
	}
	if name := item.Get("name").String(); name != "" {
		state.name = name
	}
	return state
}

func (tracker *streamToolCallTracker) stateForCompletedItem(item gjson.Result, outputIndex int64, family toolFamily) *toolCallStreamState {
	tracker.ensure()
	itemID := item.Get("id").String()
	callID := item.Get("call_id").String()

	var state *toolCallStreamState
	if itemID != "" {
		state = tracker.byItemID[itemID]
	}
	if state == nil {
		state = tracker.byOutputIndex[outputIndex]
	}
	if state == nil && callID != "" {
		state = tracker.uniqueStateByCallID(callID)
	}
	if state == nil {
		state = tracker.newState(family)
	}

	state.family = family
	state.outputIndex = outputIndex
	state.hasOutputIndex = true
	tracker.byOutputIndex[outputIndex] = state
	if itemID != "" {
		state.itemID = itemID
		tracker.byItemID[itemID] = state
	}
	if callID != "" {
		state.callID = callID
	}
	if name := item.Get("name").String(); name != "" {
		state.name = name
	}
	return state
}

func (tracker *streamToolCallTracker) uniqueStateByCallID(callID string) *toolCallStreamState {
	var matched *toolCallStreamState
	for _, state := range tracker.ordered {
		if state.callID != callID {
			continue
		}
		if matched != nil && matched != state {
			return nil
		}
		matched = state
	}
	return matched
}

func (tracker *streamToolCallTracker) activeState(family toolFamily) (*toolCallStreamState, int) {
	var matched *toolCallStreamState
	count := 0
	for _, state := range tracker.ordered {
		if state.family != family || state.itemDone {
			continue
		}
		count++
		if matched == nil {
			matched = state
		}
	}
	return matched, count
}

func (tracker *streamToolCallTracker) hasAnnouncedCall() bool {
	for _, state := range tracker.ordered {
		if state.announced {
			return true
		}
	}
	return false
}

func toolFamilyFromItem(item gjson.Result) (toolFamily, bool) {
	switch item.Get("type").String() {
	case "function_call":
		return toolFamilyFunction, true
	case "custom_tool_call":
		return toolFamilyCustom, true
	default:
		return toolFamilyFunction, false
	}
}

func toolInputFromItem(item gjson.Result, family toolFamily) (string, bool) {
	path := "arguments"
	if family == toolFamilyCustom {
		path = "input"
	}
	input := item.Get(path)
	return input.String(), input.Exists()
}

func remainingToolInput(emitted, complete string) (string, bool) {
	if emitted == "" {
		return complete, true
	}
	if complete == emitted {
		return "", true
	}
	if strings.HasPrefix(complete, emitted) {
		return complete[len(emitted):], true
	}
	return "", false
}

func announceToolCall(template []byte, state *toolCallStreamState, input string) []byte {
	if state == nil || state.announced || state.callID == "" || state.name == "" {
		return nil
	}

	toolCall := []byte(`{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}`)
	toolCall, _ = sjson.SetBytes(toolCall, "index", state.chatIndex)
	toolCall, _ = sjson.SetBytes(toolCall, "id", state.callID)
	toolCall, _ = sjson.SetBytes(toolCall, "function.name", state.name)
	toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", input)

	chunk := template
	chunk, _ = sjson.SetBytes(chunk, "choices.0.delta.role", "assistant")
	chunk, _ = sjson.SetRawBytes(chunk, "choices.0.delta.tool_calls", []byte(`[]`))
	chunk, _ = sjson.SetRawBytes(chunk, "choices.0.delta.tool_calls.-1", toolCall)

	state.announced = true
	state.emittedInput += input
	state.bufferedInput = ""
	return chunk
}

func emitToolInputDelta(template []byte, state *toolCallStreamState, delta string) []byte {
	if state == nil || !state.announced || delta == "" {
		return nil
	}

	toolCall := []byte(`{"index":0,"function":{"arguments":""}}`)
	toolCall, _ = sjson.SetBytes(toolCall, "index", state.chatIndex)
	toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", delta)

	chunk := template
	chunk, _ = sjson.SetRawBytes(chunk, "choices.0.delta.tool_calls", []byte(`[]`))
	chunk, _ = sjson.SetRawBytes(chunk, "choices.0.delta.tool_calls.-1", toolCall)
	state.emittedInput += delta
	return chunk
}

func emitAvailableToolCall(template []byte, state *toolCallStreamState, complete string, hasComplete bool) [][]byte {
	if state == nil {
		return nil
	}
	if hasComplete {
		state.completeInput = complete
		state.hasCompleteInput = true
	}

	value := state.bufferedInput
	valueIsComplete := false
	if state.hasCompleteInput {
		value = state.completeInput
		valueIsComplete = true
	}

	if !state.announced {
		chunk := announceToolCall(template, state, value)
		if chunk == nil {
			return nil
		}
		return [][]byte{chunk}
	}

	if valueIsComplete {
		remaining, ok := remainingToolInput(state.emittedInput, value)
		if !ok {
			return nil
		}
		if chunk := emitToolInputDelta(template, state, remaining); chunk != nil {
			return [][]byte{chunk}
		}
		return nil
	}
	if state.bufferedInput != "" {
		if chunk := emitToolInputDelta(template, state, state.bufferedInput); chunk != nil {
			state.bufferedInput = ""
			return [][]byte{chunk}
		}
	}
	return nil
}

func emitCompletedToolCalls(template []byte, tracker *streamToolCallTracker, catalog toolCatalog, output gjson.Result) [][]byte {
	if tracker == nil || !output.IsArray() {
		return nil
	}

	var chunks [][]byte
	for index, item := range output.Array() {
		family, ok := toolFamilyFromItem(item)
		if !ok {
			continue
		}
		state := tracker.stateForCompletedItem(item, int64(index), family)
		state.name = catalog.restore(state.name)
		input, hasInput := toolInputFromItem(item, family)
		chunks = append(chunks, emitAvailableToolCall(template, state, input, hasInput)...)
		state.itemDone = true
	}
	return chunks
}
