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
	clientType  string
	clientID    string
	outputIndex *int
	callID      string
	alias       string
	wireType    string
	buffer      strings.Builder
	bufferBytes int
	argsDone    bool
	itemDone    bool
	doneEmitted bool
	searchArgs  any
}

const trackedCallStateOverhead = 192

func trackedCallStateBytes(tracked *trackedCall) int {
	if tracked == nil {
		return 0
	}
	return trackedCallStateOverhead + len(tracked.itemID) + len(tracked.alias) +
		len(tracked.callID) + len(tracked.clientID) + len(tracked.clientType)
}

// clientItemID is the id the client sees for one tracked item. It is computed
// once, when the item is first tracked, and reused by every later reference:
// recomputing it from a type that has already been rewritten is how a stream
// ends up announcing one identity and then streaming another.
func clientItemID(tracked *trackedCall) string {
	if tracked == nil {
		return ""
	}
	return tracked.clientID
}

// clientIdentityFor computes the client-visible type and id of one tracked
// item. Bridged items change type on the way out, so their id has to move with
// it; ordinary items keep the identity the upstream gave them.
func clientIdentityFor(kind callKind, wireType, itemID string) (string, string, error) {
	switch kind {
	case callKindSearch:
		clientID, err := ConvertedItemID(wireType, "tool_search_call", itemID)
		if err != nil {
			return "", "", err
		}
		return "tool_search_call", clientID, nil
	case callKindCustom:
		clientID, err := ConvertedItemID(wireType, "custom_tool_call", itemID)
		if err != nil {
			return "", "", err
		}
		return "custom_tool_call", clientID, nil
	default:
		return wireType, itemID, nil
	}
}

// StreamFeed adapts one upstream event stream for a bridge attempt. Feed
// returns zero events while argument fragments accumulate; that means
// buffered, never pass-through. Finish flushes remaining items that carry complete
// arguments in their terminal payload and rejects incomplete tails. Close
// releases every reservation.
type StreamFeed struct {
	contract  *ToolContract
	bridge    *CustomBridge
	lease     *Lease
	limits    Limits
	calls     map[string]*trackedCall
	ids       ItemIDRegistry
	sseBuffer []byte
	sseBytes  int
	sequence  int
	finished  bool
	closed    bool
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
	if f.sseBuffer != nil || isSSEFrame(frame) {
		return f.feedSSE(frame)
	}
	trimmed := bytes.TrimSpace(frame)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		if _, ok := decodeValue(trimmed); !ok {
			f.sseBuffer = bytes.Clone(frame)
			return nil, nil
		}
	}
	event, payload, ok, err := parseStreamFrame(frame)
	if err != nil {
		return nil, err
	}
	if !ok {
		if len(bytes.TrimSpace(frame)) == 0 {
			return nil, nil
		}
		return [][]byte{frame}, nil
	}
	return f.feedPayload(frame, event, payload)
}

func (f *StreamFeed) feedSSE(chunk []byte) ([][]byte, error) {
	if len(chunk) > f.limits.MaxAttemptBytes-f.sseBytes {
		return nil, budgetError(ReasonAttemptBudget, fmt.Errorf("stream SSE buffer exceeded"))
	}
	if err := f.lease.Grow(len(chunk)); err != nil {
		return nil, err
	}
	f.sseBuffer = append(f.sseBuffer, chunk...)
	f.sseBytes += len(chunk)
	var output [][]byte
	for {
		frameEnd, delimiterEnd, ok := findSSEFrameEnd(f.sseBuffer)
		if !ok {
			return output, nil
		}
		frame := normalizeSSELineEndings(bytes.Clone(f.sseBuffer[:frameEnd]))
		consumed := delimiterEnd
		f.sseBuffer = bytes.Clone(f.sseBuffer[consumed:])
		f.sseBytes -= consumed
		f.lease.Shrink(consumed)
		events, err := f.feedSSEFrame(frame)
		if err != nil {
			return nil, err
		}
		output = append(output, events...)
		if f.finished && len(f.sseBuffer) > 0 {
			f.releaseSSEBuffer()
			return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("event data followed a terminal response event"))
		}
	}
}

func (f *StreamFeed) releaseSSEBuffer() {
	if f == nil {
		return
	}
	if f.sseBytes > 0 {
		f.lease.Shrink(f.sseBytes)
	}
	f.sseBuffer = nil
	f.sseBytes = 0
}

// findSSEFrameEnd returns a frame boundary at a blank line. CRLF is treated
// as one line ending even when its bytes arrive in separate chunks.
func findSSEFrameEnd(buffer []byte) (frameEnd, delimiterEnd int, ok bool) {
	firstLineEndStart, firstLineEndEnd := -1, -1
	for index := 0; index < len(buffer); {
		switch buffer[index] {
		case '\r', '\n':
			start := index
			if buffer[start] == '\r' && start+1 == len(buffer) {
				return 0, 0, false
			}
			index++
			if buffer[start] == '\r' && index < len(buffer) && buffer[index] == '\n' {
				index++
			}
			if firstLineEndStart >= 0 && firstLineEndEnd == start {
				return firstLineEndStart, index, true
			}
			firstLineEndStart, firstLineEndEnd = start, index
		default:
			index++
			firstLineEndStart, firstLineEndEnd = -1, -1
		}
	}
	return 0, 0, false
}

func (f *StreamFeed) feedSSEFrame(frame []byte) ([][]byte, error) {
	if len(bytes.TrimSpace(frame)) == 0 {
		return nil, nil
	}
	event, payload, ok, err := parseStreamFrame(frame)
	if err != nil {
		return nil, err
	}
	if !ok {
		return [][]byte{frame}, nil
	}
	return f.feedPayload(frame, event, payload)
}

func (f *StreamFeed) feedPayload(frame []byte, event string, payload map[string]any) ([][]byte, error) {
	itemType := stringField(payload, "type")
	if itemType == "" && event != "" {
		payload = cloneMap(payload)
		payload["type"] = event
		itemType = event
	}
	kind := event
	if kind == "" {
		kind = itemType
	}
	switch {
	case kind == eventResponseFailed || kind == eventResponseIncomplete ||
		event == eventResponseFailed || event == eventResponseIncomplete:
		f.finished = true
		return [][]byte{f.encodeOutput(payload)}, nil
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
		if strings.HasPrefix(itemType, "response.") {
			return [][]byte{f.encodeOutput(payload)}, nil
		}
		return [][]byte{frame}, nil
	}
}

func parseStreamFrame(frame []byte) (string, map[string]any, bool, error) {
	trimmed := bytes.TrimSpace(frame)
	if len(trimmed) == 0 {
		return "", nil, false, nil
	}
	event := ""
	var payloadBytes []byte
	hasData := false
	if isSSEFrame(frame) {
		for _, line := range bytes.Split(normalizeSSELineEndings(bytes.Clone(frame)), []byte("\n")) {
			line = bytes.TrimSuffix(line, []byte("\r"))
			if len(line) == 0 || line[0] == ':' {
				continue
			}
			field, value, found := bytes.Cut(line, []byte(":"))
			if !found {
				field = line
				value = nil
			} else if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
			switch string(field) {
			case "event":
				event = string(value)
			case "data":
				if hasData {
					payloadBytes = append(payloadBytes, '\n')
				}
				payloadBytes = append(payloadBytes, value...)
				hasData = true
			}
		}
		if !hasData {
			return "", nil, false, nil
		}
		trimmed = bytes.TrimSpace(payloadBytes)
	}
	value, ok := decodeValue(trimmed)
	if !ok {
		return "", nil, false, nil
	}
	payload, ok := value.(map[string]any)
	if !ok {
		return "", nil, false, nil
	}
	if event == "" {
		event, _ = payload["event"].(string)
	}
	itemType, _ := payload["type"].(string)
	if event == "" && !strings.HasPrefix(itemType, "response.") {
		return "", nil, false, nil
	}
	if event != "" && itemType != "" && event != itemType {
		return "", nil, false, upstreamError(ReasonUpstreamContract, fmt.Errorf("SSE event %s does not match payload type %s", event, itemType))
	}
	return event, payload, true, nil
}

func isSSEFrame(frame []byte) bool {
	return matchesSSEField(frame)
}

func matchesSSEField(frame []byte) bool {
	trimmed := bytes.TrimSpace(frame)
	if len(trimmed) == 0 {
		return false
	}
	for _, prefix := range [][]byte{
		[]byte("data:"), []byte("event:"), []byte("id:"), []byte("retry:"), []byte(":"),
	} {
		if bytes.HasPrefix(trimmed, prefix) || bytes.HasPrefix(prefix, trimmed) {
			return true
		}
	}
	return false
}

func normalizeSSELineEndings(frame []byte) []byte {
	normalized := bytes.ReplaceAll(frame, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(normalized, []byte("\r"), []byte("\n"))
}

func (f *StreamFeed) classify(name string) (callKind, ToolIdentity) {
	if f.contract != nil && f.contract.SearchBridged && name == f.contract.SearchAlias {
		return callKindSearch, ToolIdentity{}
	}
	if f.bridge != nil {
		if identity, ok := f.bridge.ResolveWireAlias(name); ok && identity.Kind == ToolKindCustom {
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
	wireType := stringField(item, "type")
	if kind != callKindOrdinary && wireType != "function_call" {
		return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("bridged output item %s has unexpected type %q", itemID, wireType))
	}
	tracked := &trackedCall{kind: kind, identity: identity, itemID: itemID, alias: name, wireType: wireType}
	clientType, clientID, err := clientIdentityFor(kind, wireType, itemID)
	if err != nil {
		return nil, upstreamError(ReasonUpstreamContract, err)
	}
	tracked.clientType = clientType
	tracked.clientID = clientID
	if index, ok := item["output_index"]; ok {
		number, okNumber := toInt(index)
		if !okNumber {
			return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s has invalid output_index", itemID))
		}
		tracked.outputIndex = &number
	}
	if callID := stringField(item, "call_id"); callID != "" {
		tracked.callID = callID
	}
	if previous, exists := f.calls[itemID]; exists {
		if previous.kind != tracked.kind || previous.identity != tracked.identity ||
			previous.alias != tracked.alias || previous.wireType != tracked.wireType {
			return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s changed identity", itemID))
		}
		if err := f.verifyConsistency(previous, item); err != nil {
			return nil, err
		}
		return previous, nil
	}
	if err := f.lease.Grow(trackedCallStateBytes(tracked)); err != nil {
		return nil, err
	}
	if err := f.ids.Register(tracked.clientID, ItemIDOwner{
		WireID:   tracked.itemID,
		WireType: tracked.wireType,
		CallID:   tracked.callID,
	}); err != nil {
		f.lease.Shrink(trackedCallStateBytes(tracked))
		return nil, upstreamError(ReasonUpstreamContract, err)
	}
	f.calls[itemID] = tracked
	return tracked, nil
}

func (f *StreamFeed) setTrackedCallID(tracked *trackedCall, callID string) error {
	if tracked == nil || callID == "" {
		return nil
	}
	if tracked.callID != "" {
		if tracked.callID != callID {
			return upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s changed call_id", tracked.itemID))
		}
		return nil
	}
	if err := f.lease.Grow(len(callID)); err != nil {
		return err
	}
	tracked.callID = callID
	return nil
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
	if itemID := strings.TrimSpace(stringField(item, "item_id")); itemID != "" && itemID != tracked.itemID {
		return upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s changed item_id", tracked.itemID))
	}
	itemType := stringField(item, "type")
	isOutputItem := stringField(item, "id") != "" && !strings.HasPrefix(itemType, "response.")
	if isOutputItem && tracked.alias != "" {
		if _, exists := item["name"]; !exists {
			return upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s omitted its name", tracked.itemID))
		}
	}
	if isOutputItem && tracked.wireType != "" && itemType == "" {
		return upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s omitted its type", tracked.itemID))
	}
	if rawName, exists := item["name"]; exists {
		name, ok := rawName.(string)
		if !ok || name == "" || name != tracked.alias {
			return upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s changed name", tracked.itemID))
		}
		kind, identity := f.classify(name)
		if kind != tracked.kind || identity != tracked.identity {
			return upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s changed tool identity", tracked.itemID))
		}
	}
	if rawType, exists := item["type"]; exists {
		typeName, ok := rawType.(string)
		if !ok || (!strings.HasPrefix(typeName, "response.") && tracked.wireType != "" && typeName != tracked.wireType) {
			return upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s changed type", tracked.itemID))
		}
	}
	if callID := stringField(item, "call_id"); callID != "" {
		if err := f.setTrackedCallID(tracked, callID); err != nil {
			return err
		}
	}
	if index, ok := item["output_index"]; ok {
		number, okNumber := toInt(index)
		if !okNumber {
			return upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s has invalid output_index", tracked.itemID))
		}
		if tracked.outputIndex != nil && number != *tracked.outputIndex {
			return upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s changed output_index", tracked.itemID))
		}
		if tracked.outputIndex == nil {
			tracked.outputIndex = &number
		}
	}
	return nil
}

func (f *StreamFeed) addItem(payload map[string]any) ([][]byte, error) {
	item, _ := payload["item"].(map[string]any)
	if item == nil {
		return [][]byte{f.encodeOutput(payload)}, nil
	}
	tracked, err := f.track(item)
	if err != nil {
		return nil, err
	}
	if err := f.verifyConsistency(tracked, payload); err != nil {
		return nil, err
	}
	if tracked.kind == callKindOrdinary {
		restored := cloneMap(payload)
		restoredItem := cloneMap(item)
		if _, err := RestoreFunctionCallIdentityChecked(restoredItem, f.contract); err != nil {
			return nil, err
		}
		restoreSyntheticSearchNulls(restoredItem, f.contract)
		restored["item"] = restoredItem
		return [][]byte{f.encodeOutput(restored)}, nil
	}
	restored := cloneMap(payload)
	restoredItem := cloneMap(item)
	switch tracked.kind {
	case callKindSearch:
		restoredItem["type"] = tracked.clientType
		restoredItem["execution"] = "client"
		restoredItem["status"] = "in_progress"
		restoredItem["arguments"] = map[string]any{}
		restoredItem["id"] = clientItemID(tracked)
		delete(restoredItem, "name")
		delete(restoredItem, "namespace")
	case callKindCustom:
		restoredItem["type"] = tracked.clientType
		restoredItem["id"] = clientItemID(tracked)
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
	return [][]byte{f.encodeOutput(restored)}, nil
}

func (f *StreamFeed) appendDelta(payload map[string]any) ([][]byte, error) {
	itemID := stringField(payload, "item_id")
	tracked := f.calls[itemID]
	if tracked == nil || tracked.kind == callKindOrdinary {
		return [][]byte{f.encodeOutput(payload)}, nil
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
		return [][]byte{f.encodeOutput(payload)}, nil
	}
	if err := f.verifyConsistency(tracked, payload); err != nil {
		return nil, err
	}
	if tracked.argsDone {
		if complete, exists := payload["arguments"]; exists {
			value, ok := complete.(string)
			if !ok || value != tracked.buffer.String() {
				return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s arguments.done changed arguments", tracked.itemID))
			}
		}
		return nil, nil
	}
	if complete, exists := payload["arguments"]; exists {
		value, ok := complete.(string)
		if !ok {
			return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s arguments.done has non-string arguments", tracked.itemID))
		}
		if tracked.buffer.Len() > 0 && value != tracked.buffer.String() {
			return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s arguments.done changed arguments", tracked.itemID))
		}
		if tracked.buffer.Len() == 0 && value != "" {
			if err := f.replaceArguments(tracked, value); err != nil {
				return nil, err
			}
		}
	}
	tracked.argsDone = true
	return f.emitArgsDone(tracked)
}

func (f *StreamFeed) replaceArguments(tracked *trackedCall, value string) error {
	oldSize := tracked.bufferBytes
	switch delta := len(value) - oldSize; {
	case delta > 0:
		if err := f.lease.Grow(delta); err != nil {
			return err
		}
	case delta < 0:
		f.lease.Shrink(-delta)
	}
	tracked.buffer.Reset()
	tracked.buffer.WriteString(value)
	tracked.bufferBytes = len(value)
	if tracked.bufferBytes > f.limits.MaxAttemptBytes {
		return budgetError(ReasonAttemptBudget, fmt.Errorf("stream argument buffer exceeded"))
	}
	return nil
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
		if arguments, ok := decoded.(map[string]any); ok {
			restoreSyntheticSearchArgumentNulls(arguments, f.contract)
		}
		tracked.searchArgs = decoded
		// The official protocol has no tool_search_arguments.delta event, so
		// the accumulated object is stored and emitted with the terminal item.
		return nil, nil
	case callKindCustom:
		input, err := unpackUpstreamCustomArguments(tracked.buffer.String())
		if err != nil {
			return nil, err
		}
		delta := cloneMap(map[string]any{
			"type":    eventCustomInputDelta,
			"item_id": clientItemID(tracked),
			"delta":   input,
		})
		done := cloneMap(map[string]any{
			"type":    eventCustomInputDone,
			"item_id": clientItemID(tracked),
			"input":   input,
		})
		f.copyOutputIndex(tracked, delta)
		f.copyOutputIndex(tracked, done)
		return [][]byte{f.encodeOutput(delta), f.encodeOutput(done)}, nil
	default:
		return nil, nil
	}
}

func (f *StreamFeed) finishItem(payload map[string]any) ([][]byte, error) {
	item, _ := payload["item"].(map[string]any)
	if item == nil {
		return [][]byte{f.encodeOutput(payload)}, nil
	}
	itemID := stringField(item, "id")
	if itemID == "" {
		itemID = stringField(payload, "item_id")
	}
	tracked := f.calls[itemID]
	if tracked == nil {
		return [][]byte{f.encodeOutput(payload)}, nil
	}
	if err := f.verifyConsistency(tracked, item); err != nil {
		return nil, err
	}
	if err := f.verifyConsistency(tracked, payload); err != nil {
		return nil, err
	}
	if tracked.kind == callKindOrdinary {
		return f.emitItemDone(tracked, item, payload)
	}
	var prelude [][]byte
	// A terminal item may fill in omitted or incomplete deltas when no
	// arguments.done event already established the canonical value.
	if !tracked.argsDone {
		if complete, exists := item["arguments"]; exists {
			value, ok := complete.(string)
			if !ok {
				return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s completed with non-string arguments", tracked.itemID))
			}
			if err := f.replaceArguments(tracked, value); err != nil {
				return nil, err
			}
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
		} else if tracked.buffer.Len() > 0 {
			tracked.argsDone = true
			events, err := f.emitArgsDone(tracked)
			if err != nil {
				return nil, err
			}
			prelude = append(prelude, events...)
		} else if tracked.kind == callKindSearch {
			tracked.argsDone = true
			tracked.searchArgs = map[string]any{}
		} else {
			return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s completed without arguments", tracked.itemID))
		}
	} else if complete, exists := item["arguments"]; exists {
		value, ok := complete.(string)
		if !ok || value != tracked.buffer.String() {
			return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("output item %s done changed arguments", tracked.itemID))
		}
	}
	done, err := f.emitItemDone(tracked, item, payload)
	if err != nil {
		return nil, err
	}
	return append(prelude, done...), nil
}

func (f *StreamFeed) emitItemDone(tracked *trackedCall, item, payload map[string]any) ([][]byte, error) {
	if tracked.doneEmitted {
		return nil, nil
	}
	tracked.doneEmitted = true
	tracked.itemDone = true
	restored := cloneMap(payload)
	f.copyOutputIndex(tracked, restored)
	restoredItem := cloneMap(item)
	switch tracked.kind {
	case callKindSearch:
		restoredItem["type"] = tracked.clientType
		restoredItem["execution"] = "client"
		restoredItem["status"] = "completed"
		restoredItem["id"] = clientItemID(tracked)
		if tracked.searchArgs != nil {
			restoredItem["arguments"] = tracked.searchArgs
		}
		delete(restoredItem, "name")
		delete(restoredItem, "namespace")
	case callKindCustom:
		input, err := unpackUpstreamCustomArguments(tracked.buffer.String())
		if err != nil {
			return nil, err
		}
		restoredItem["type"] = tracked.clientType
		restoredItem["id"] = clientItemID(tracked)
		restoredItem["name"] = tracked.identity.Name
		if tracked.identity.Namespace != "" {
			restoredItem["namespace"] = tracked.identity.Namespace
		} else {
			delete(restoredItem, "namespace")
		}
		restoredItem["input"] = input
		delete(restoredItem, "arguments")
	case callKindOrdinary:
		if _, err := RestoreFunctionCallIdentityChecked(restoredItem, f.contract); err != nil {
			return nil, err
		}
		restoreSyntheticSearchNulls(restoredItem, f.contract)
	}
	restored["item"] = restoredItem
	return [][]byte{f.encodeOutput(restored)}, nil
}

func unpackUpstreamCustomArguments(arguments string) (string, error) {
	input, err := UnpackCustomArguments(arguments)
	if err != nil {
		return "", upstreamError(ReasonUpstreamContract, fmt.Errorf("invalid custom function arguments: %w", err))
	}
	return input, nil
}

func (f *StreamFeed) complete(payload map[string]any) ([][]byte, error) {
	output, _ := payload["output"].([]any)
	if output == nil {
		if response, ok := payload["response"].(map[string]any); ok {
			output, _ = response["output"].([]any)
		}
	}
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
		if err := f.verifyConsistency(tracked, item); err != nil {
			return nil, err
		}
		if complete, exists := item["arguments"]; exists && tracked.argsDone {
			value, ok := complete.(string)
			if !ok || value != tracked.buffer.String() {
				return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("response completed changed arguments for item %s", tracked.itemID))
			}
		}
		if tracked.doneEmitted {
			continue
		}
		if !tracked.argsDone {
			if complete, exists := item["arguments"]; exists {
				value, okArgs := complete.(string)
				if !okArgs {
					return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("response completed with non-string arguments for item %s", tracked.itemID))
				}
				if err := f.replaceArguments(tracked, value); err != nil {
					return nil, err
				}
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
	wireIDs := make([]string, len(output))
	for index, rawItem := range output {
		if item, ok := rawItem.(map[string]any); ok {
			wireIDs[index] = stringField(item, "id")
		}
	}
	if _, err := RewriteResponseBodyChecked(payload, f.contract, f.bridge); err != nil {
		return nil, err
	}
	if err := f.verifyCompletedIdentities(output, wireIDs); err != nil {
		return nil, err
	}
	out = append(out, f.encodeOutput(payload))
	return out, nil
}

// verifyCompletedIdentities confirms that the rewritten terminal output names
// every tracked item the way the added and delta events already did. The
// terminal array is rewritten from the upstream item, so this is where a second,
// independent identity decision would show up.
func (f *StreamFeed) verifyCompletedIdentities(items []any, wireIDs []string) error {
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		if index >= len(wireIDs) {
			continue
		}
		tracked := f.calls[wireIDs[index]]
		if tracked == nil {
			continue
		}
		if id := stringField(item, "id"); id != tracked.clientID {
			return upstreamError(ReasonUpstreamContract,
				fmt.Errorf("response completed changed the client id of output item %s", tracked.itemID))
		}
		if itemType := stringField(item, "type"); itemType != tracked.clientType {
			return upstreamError(ReasonUpstreamContract,
				fmt.Errorf("response completed changed the client type of output item %s", tracked.itemID))
		}
	}
	return nil
}

// Finish validates the tail of the stream: buffered fragments without any
// terminal arguments are rejected, never silently dropped as success.
func (f *StreamFeed) Finish() ([][]byte, error) {
	if f == nil {
		return nil, nil
	}
	if len(f.sseBuffer) > 0 {
		f.releaseSSEBuffer()
		f.finished = true
		return nil, upstreamError(ReasonUpstreamContract, fmt.Errorf("stream ended with incomplete SSE frame"))
	}
	if f.finished {
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

func (f *StreamFeed) copyOutputIndex(tracked *trackedCall, payload map[string]any) {
	if tracked != nil && tracked.outputIndex != nil {
		payload["output_index"] = *tracked.outputIndex
	}
}

func (f *StreamFeed) encodeOutput(payload map[string]any) []byte {
	if payload == nil {
		return encodeFrame(nil)
	}
	output := cloneMap(payload)
	if f != nil {
		if eventType := stringField(output, "type"); strings.HasPrefix(eventType, "response.") &&
			f.contract != nil && (f.contract.ClientSearch || f.bridge != nil) {
			output["sequence_number"] = f.sequence
			f.sequence++
		}
	}
	return encodeFrame(output)
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
		encoded = []byte("{}")
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
