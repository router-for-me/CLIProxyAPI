package responsestools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Stream event types carried on the Responses SSE channel.
const (
	eventOutputItemAdded       = "response.output_item.added"
	eventFunctionCallArgsDelta = "response.function_call_arguments.delta"
	eventFunctionCallArgsDone  = "response.function_call_arguments.done"
	eventOutputItemDone        = "response.output_item.done"
	eventCustomInputDelta      = "response.custom_tool_call_input.delta"
	eventCustomInputDone       = "response.custom_tool_call_input.done"
	eventResponseCompleted     = "response.completed"
	eventResponseFailed        = "response.failed"
	eventResponseIncomplete    = "response.incomplete"
	eventContentPartAdded      = "response.content_part.added"
	eventOutputTextDelta       = "response.output_text.delta"
	eventOutputTextDone        = "response.output_text.done"
	eventContentPartDone       = "response.content_part.done"
)

// callKind classifies one tracked output item.
type callKind int

const (
	callKindOrdinary callKind = iota
	callKindSearch
	callKindCustom
)

// trackedCall is the per-item state of one bridged output item. The map key
// is the attempt-local item_id; output_index and call_id only verify
// consistency and never select the entry.
type trackedCall struct {
	kind        callKind
	identity    ToolIdentity
	itemID      string
	outputIndex *int
	callID      string
	alias       string
	buffer      strings.Builder
	bufferBytes int
	argsDone    bool
	itemDone    bool
	doneEmitted bool
	searchArgs  any
}

// StreamFeed adapts one upstream event stream for a bridge attempt. Feed
// returns zero events while argument fragments accumulate; that means
// buffered, never pass-through. Finish flushes补齐 items that carry complete
// arguments in their terminal payload and rejects incomplete tails. Close
// releases every reservation.
type StreamFeed struct {
	contract *ToolContract
	bridge   *CustomBridge
	lease    *Lease
	limits   Limits
	calls    map[string]*trackedCall
	sequence int
	finished bool
	closed   bool
}

// NewStreamFeed creates the stream adapter for one attempt.
func NewStreamFeed(contract *ToolContract, bridge *CustomBridge, lease *Lease, limits Limits) *StreamFeed {
	return &StreamFeed{
		contract: contract,
		bridge:   bridge,
		lease:    lease,
		limits:   limits,
		calls:    make(map[string]*trackedCall),
	}
}

// Feed adapts one decoded SSE event or plain JSON frame. Supported shapes:
// {"type": ...} event objects and {"event": ..., "data": ...} envelopes. The
// SSE event name and the JSON type travel together; mismatched envelopes are
// rejected rather than guessed.
func (f *StreamFeed) Feed(frame []byte) ([][]byte, error) {
	if f == nil {
		return nil, nil
	}
	if f.finished {
		return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("event after stream finish"))
	}
	event, payload, ok := splitStreamFrame(frame)
	if !ok {
		// Non-event frames (comments, blank keepalives) pass through.
		return [][]byte{frame}, nil
	}
	itemType := stringField(payload, "type")
	kind := event
	if kind == "" {
		kind = itemType
	}
	switch {
	case kind == eventResponseFailed || kind == eventResponseIncomplete ||
		event == eventResponseFailed || event == eventResponseIncomplete:
		f.finished = true
		return [][]byte{frame}, nil
	case kind == eventResponseCompleted || event == eventResponseCompleted:
		events, err := f.complete(payload)
		if err != nil {
			return nil, err
		}
		f.finished = true
		return events, nil
	case kind == eventOutputItemAdded || event == eventOutputItemAdded:
		return f.addItem(payload)
	case kind == eventFunctionCallArgsDelta || event == eventFunctionCallArgsDelta:
		return f.appendDelta(payload)
	case kind == eventFunctionCallArgsDone || event == eventFunctionCallArgsDone:
		return f.finishArgs(payload)
	case kind == eventOutputItemDone || event == eventOutputItemDone:
		return f.finishItem(payload)
	default:
		return [][]byte{frame}, nil
	}
}

func splitStreamFrame(frame []byte) (string, map[string]any, bool) {
	trimmed := bytes.TrimSpace(frame)
	if len(trimmed) == 0 || bytes.HasPrefix(trimmed, []byte(":")) {
		return "", nil, false
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	value, ok := decodeValue(trimmed)
	if !ok {
		return "", nil, false
	}
	payload, ok := value.(map[string]any)
	if !ok {
		return "", nil, false
	}
	event, _ := payload["event"].(string)
	itemType, _ := payload["type"].(string)
	if event == "" && !strings.HasPrefix(itemType, "response.") {
		return "", nil, false
	}
	return event, payload, true
}

func (f *StreamFeed) classify(name string) (callKind, ToolIdentity) {
	if f.contract != nil && name == f.contract.SearchAlias {
		return callKindSearch, ToolIdentity{}
	}
	if f.bridge != nil {
		if identity, ok := f.bridge.Resolve(name); ok && identity.Kind == ToolKindCustom {
			return callKindCustom, identity
		}
	}
	if f.contract != nil {
		if identity, ok := f.contract.Resolve(name); ok {
			return callKindOrdinary, identity
		}
	}
	return callKindOrdinary, ToolIdentity{}
}

func (f *StreamFeed) track(item map[string]any) (*trackedCall, error) {
	itemID := stringField(item, "id")
	name := stringField(item, "name")
	if itemID == "" {
		return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("output item without id"))
	}
	kind, identity := f.classify(name)
	tracked := &trackedCall{kind: kind, identity: identity, itemID: itemID, alias: name}
	if index, ok := item["output_index"]; ok {
		if number, okNumber := toInt(index); okNumber {
			value := number
			tracked.outputIndex = &value
		}
	}
	if callID := stringField(item, "call_id"); callID != "" {
		tracked.callID = callID
	}
	if previous, exists := f.calls[itemID]; exists {
		if previous.kind != tracked.kind || previous.alias != tracked.alias {
			return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s changed identity", itemID))
		}
		if tracked.outputIndex != nil {
			previous.outputIndex = tracked.outputIndex
		}
		if tracked.callID != "" {
			if previous.callID != "" && previous.callID != tracked.callID {
				return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s changed call_id", itemID))
			}
			previous.callID = tracked.callID
		}
		return previous, nil
	}
	f.calls[itemID] = tracked
	return tracked, nil
}

func toInt(value any) (int, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	default:
		return 0, false
	}
}

func (f *StreamFeed) verifyConsistency(tracked *trackedCall, item map[string]any) error {
	if tracked == nil {
		return nil
	}
	if callID := stringField(item, "call_id"); callID != "" && tracked.callID != "" && callID != tracked.callID {
		return upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s changed call_id", tracked.itemID))
	}
	if index, ok := item["output_index"]; ok {
		if number, okNumber := toInt(index); okNumber && tracked.outputIndex != nil && number != *tracked.outputIndex {
			return upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s changed output_index", tracked.itemID))
		}
	}
	return nil
}

func (f *StreamFeed) addItem(payload map[string]any) ([][]byte, error) {
	item, _ := payload["item"].(map[string]any)
	if item == nil {
		return [][]byte{encodeFrame(payload)}, nil
	}
	tracked, err := f.track(item)
	if err != nil {
		return nil, err
	}
	if tracked.kind == callKindOrdinary {
		return [][]byte{encodeFrame(payload)}, nil
	}
	restored := cloneMap(payload)
	restoredItem := cloneMap(item)
	switch tracked.kind {
	case callKindSearch:
		restoredItem["type"] = "tool_search_call"
		restoredItem["execution"] = "client"
		restoredItem["status"] = "in_progress"
		restoredItem["arguments"] = map[string]any{}
		delete(restoredItem, "name")
		delete(restoredItem, "namespace")
	case callKindCustom:
		restoredItem["type"] = "custom_tool_call"
		restoredItem["name"] = tracked.identity.Name
		if tracked.identity.Namespace != "" {
			restoredItem["namespace"] = tracked.identity.Namespace
		} else {
			delete(restoredItem, "namespace")
		}
		restoredItem["input"] = ""
		delete(restoredItem, "arguments")
	}
	restored["item"] = restoredItem
	return [][]byte{encodeFrame(restored)}, nil
}

func (f *StreamFeed) appendDelta(payload map[string]any) ([][]byte, error) {
	itemID := stringField(payload, "item_id")
	tracked := f.calls[itemID]
	if tracked == nil || tracked.kind == callKindOrdinary {
		return [][]byte{encodeFrame(payload)}, nil
	}
	if err := f.verifyConsistency(tracked, payload); err != nil {
		return nil, err
	}
	delta := stringField(payload, "delta")
	if delta == "" {
		return nil, nil
	}
	if err := f.lease.Grow(len(delta)); err != nil {
		return nil, err
	}
	tracked.buffer.WriteString(delta)
	tracked.bufferBytes += len(delta)
	if tracked.bufferBytes > f.limits.MaxAttemptBytes {
		return nil, budgetError(ReasonAttemptBudget, fmt.Errorf("stream argument buffer exceeded"))
	}
	// Fragments stay buffered; emitting the wrapped shape too early would hand
	// the client an executable custom call before its input validates.
	return nil, nil
}

func (f *StreamFeed) finishArgs(payload map[string]any) ([][]byte, error) {
	itemID := stringField(payload, "item_id")
	tracked := f.calls[itemID]
	if tracked == nil || tracked.kind == callKindOrdinary {
		return [][]byte{encodeFrame(payload)}, nil
	}
	if err := f.verifyConsistency(tracked, payload); err != nil {
		return nil, err
	}
	if tracked.argsDone {
		return nil, nil
	}
	tracked.argsDone = true
	if complete, ok := payload["arguments"].(string); ok && complete != "" {
		if tracked.buffer.Len() == 0 {
			if err := f.lease.Grow(len(complete)); err != nil {
				return nil, err
			}
			tracked.buffer.WriteString(complete)
			tracked.bufferBytes += len(complete)
		}
	}
	return f.emitArgsDone(tracked)
}

func (f *StreamFeed) emitArgsDone(tracked *trackedCall) ([][]byte, error) {
	switch tracked.kind {
	case callKindSearch:
		var decoded any
		if buffered := strings.TrimSpace(tracked.buffer.String()); buffered != "" {
			value, ok := decodeValue([]byte(buffered))
			if !ok {
				return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("search arguments are not valid JSON"))
			}
			decoded = value
		} else {
			decoded = map[string]any{}
		}
		tracked.searchArgs = decoded
		// The official protocol has no tool_search_arguments.delta event, so
		// the accumulated object is stored and emitted with the terminal item.
		return nil, nil
	case callKindCustom:
		input, err := UnpackCustomArguments(tracked.buffer.String())
		if err != nil {
			return nil, err
		}
		delta := cloneMap(map[string]any{
			"type":    eventCustomInputDelta,
			"item_id": tracked.itemID,
			"delta":   input,
		})
		done := cloneMap(map[string]any{
			"type":    eventCustomInputDone,
			"item_id": tracked.itemID,
			"input":   input,
		})
		sequenceEvents(delta, done, f.nextSequence())
		return [][]byte{encodeFrame(delta), encodeFrame(done)}, nil
	default:
		return nil, nil
	}
}

func (f *StreamFeed) finishItem(payload map[string]any) ([][]byte, error) {
	item, _ := payload["item"].(map[string]any)
	if item == nil {
		return [][]byte{encodeFrame(payload)}, nil
	}
	itemID := stringField(item, "id")
	if itemID == "" {
		itemID = stringField(payload, "item_id")
	}
	tracked := f.calls[itemID]
	if tracked == nil || tracked.kind == callKindOrdinary {
		return [][]byte{encodeFrame(payload)}, nil
	}
	if err := f.verifyConsistency(tracked, item); err != nil {
		return nil, err
	}
	if err := f.verifyConsistency(tracked, payload); err != nil {
		return nil, err
	}
	// An upstream that skips arguments.done but carries complete arguments in
	// the terminal item may still complete; anything else is rejected.
	if !tracked.argsDone {
		if complete, ok := item["arguments"].(string); ok && complete != "" && tracked.buffer.Len() == 0 {
			if err := f.lease.Grow(len(complete)); err != nil {
				return nil, err
			}
			tracked.buffer.WriteString(complete)
			tracked.bufferBytes += len(complete)
			tracked.argsDone = true
			if events, err := f.emitArgsDone(tracked); err != nil {
				return nil, err
			} else if len(events) > 0 {
				done, err := f.emitItemDone(tracked, item, payload)
				if err != nil {
					return nil, err
				}
				return append(events, done...), nil
			}
		} else if !tracked.argsDone && tracked.buffer.Len() == 0 && tracked.kind == callKindSearch {
			tracked.argsDone = true
			tracked.searchArgs = map[string]any{}
		} else if !tracked.argsDone {
			return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s completed without arguments", tracked.itemID))
		}
	}
	return f.emitItemDone(tracked, item, payload)
}

func (f *StreamFeed) emitItemDone(tracked *trackedCall, item, payload map[string]any) ([][]byte, error) {
	if tracked.doneEmitted {
		return nil, nil
	}
	tracked.doneEmitted = true
	tracked.itemDone = true
	restored := cloneMap(payload)
	restoredItem := cloneMap(item)
	switch tracked.kind {
	case callKindSearch:
		restoredItem["type"] = "tool_search_call"
		restoredItem["execution"] = "client"
		restoredItem["status"] = "completed"
		if tracked.searchArgs != nil {
			restoredItem["arguments"] = tracked.searchArgs
		}
		delete(restoredItem, "name")
		delete(restoredItem, "namespace")
	case callKindCustom:
		input, err := UnpackCustomArguments(tracked.buffer.String())
		if err != nil {
			return nil, err
		}
		restoredItem["type"] = "custom_tool_call"
		restoredItem["name"] = tracked.identity.Name
		if tracked.identity.Namespace != "" {
			restoredItem["namespace"] = tracked.identity.Namespace
		} else {
			delete(restoredItem, "namespace")
		}
		restoredItem["input"] = input
		delete(restoredItem, "arguments")
	}
	restored["item"] = restoredItem
	return [][]byte{encodeFrame(restored)}, nil
}

func (f *StreamFeed) complete(payload map[string]any) ([][]byte, error) {
	output, _ := payload["output"].([]any)
	var out [][]byte
	for _, rawItem := range output {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemID := stringField(item, "id")
		tracked := f.calls[itemID]
		if tracked == nil || tracked.kind == callKindOrdinary {
			continue
		}
		if tracked.doneEmitted {
			continue
		}
		if !tracked.argsDone {
			if complete, okArgs := item["arguments"].(string); okArgs && complete != "" && tracked.buffer.Len() == 0 {
				if err := f.lease.Grow(len(complete)); err != nil {
					return nil, err
				}
				tracked.buffer.WriteString(complete)
				tracked.bufferBytes += len(complete)
				tracked.argsDone = true
				if events, err := f.emitArgsDone(tracked); err != nil {
					return nil, err
				} else if len(events) > 0 {
					out = append(out, events...)
				}
			} else {
				return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("response completed with incomplete call %s", tracked.itemID))
			}
		}
		done, err := f.emitItemDone(tracked, item, map[string]any{"type": eventOutputItemDone, "item": item})
		if err != nil {
			return nil, err
		}
		out = append(out, done...)
	}
	for _, tracked := range f.calls {
		if tracked.kind != callKindOrdinary && !tracked.doneEmitted {
			return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("response completed with unfinished call %s", tracked.itemID))
		}
	}
	out = append(out, encodeFrame(payload))
	return out, nil
}

// Finish validates the tail of the stream: buffered fragments without any
// terminal arguments are rejected, never silently dropped as success.
func (f *StreamFeed) Finish() ([][]byte, error) {
	if f == nil || f.finished {
		return nil, nil
	}
	f.finished = true
	for _, tracked := range f.calls {
		if tracked.kind == callKindOrdinary || tracked.doneEmitted {
			continue
		}
		return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("stream ended with unfinished call %s", tracked.itemID))
	}
	return nil, nil
}

func (f *StreamFeed) nextSequence() int {
	f.sequence++
	return f.sequence
}

func sequenceEvents(events ...any) {
	_ = events
}

func cloneMap(value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func encodeFrame(payload map[string]any) []byte {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// SortedCallIDs returns tracked item ids in stable order for tests.
func (f *StreamFeed) SortedCallIDs() []string {
	if f == nil {
		return nil
	}
	ids := make([]string, 0, len(f.calls))
	for id := range f.calls {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
