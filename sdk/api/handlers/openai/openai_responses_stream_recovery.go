package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type responsesRecoveredMessage struct {
	ItemID      string
	OutputIndex int64
	Text        strings.Builder
}

type responsesRecoveredFunctionCall struct {
	ItemID      string
	OutputIndex int64
	ItemType    string
	CallID      string
	Name        string
	Arguments   strings.Builder
	Input       strings.Builder
}

type responsesRecoveredOutput struct {
	OutputIndex int64
	Raw         []byte
}

type responsesStreamRecovery struct {
	responseID    string
	createdAt     int64
	model         string
	lastSequence  int64
	sequenceSeen  bool
	completedSeen bool
	usageRaw      []byte

	outputItemsByIndex  map[int64][]byte
	outputItemsFallback [][]byte

	messagesByID    map[string]*responsesRecoveredMessage
	messagesByIndex map[int64]*responsesRecoveredMessage

	functionCallsByID    map[string]*responsesRecoveredFunctionCall
	functionCallsByIndex map[int64]*responsesRecoveredFunctionCall
}

func newResponsesStreamRecovery() *responsesStreamRecovery {
	return &responsesStreamRecovery{
		outputItemsByIndex:   make(map[int64][]byte),
		messagesByID:         make(map[string]*responsesRecoveredMessage),
		messagesByIndex:      make(map[int64]*responsesRecoveredMessage),
		functionCallsByID:    make(map[string]*responsesRecoveredFunctionCall),
		functionCallsByIndex: make(map[int64]*responsesRecoveredFunctionCall),
		outputItemsFallback:  make([][]byte, 0),
	}
}

func (r *responsesStreamRecovery) normalizeChunk(chunk []byte) []byte {
	if len(chunk) == 0 {
		return chunk
	}

	lines := bytes.SplitAfter(chunk, []byte("\n"))
	if len(lines) == 0 {
		lines = [][]byte{chunk}
	}

	sawStructuredLine := false
	changed := false
	var rebuilt bytes.Buffer

	for i := range lines {
		line := lines[i]
		if len(line) == 0 {
			continue
		}
		lineEnding := lineEndingBytes(line)
		content := bytes.TrimRight(line, "\r\n")
		trimmed := bytes.TrimSpace(content)
		if len(trimmed) == 0 {
			rebuilt.Write(line)
			continue
		}

		switch {
		case bytes.HasPrefix(trimmed, []byte("event:")):
			sawStructuredLine = true
			eventName := strings.TrimSpace(string(trimmed[len("event:"):]))
			if eventName == "response.done" {
				rebuilt.WriteString("event: response.completed")
				rebuilt.Write(lineEnding)
				changed = true
				continue
			}
		case bytes.HasPrefix(trimmed, []byte("data:")):
			sawStructuredLine = true
			payload := bytes.TrimSpace(trimmed[len("data:"):])
			if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !json.Valid(payload) {
				rebuilt.Write(line)
				continue
			}

			normalized := r.normalizePayload(payload)
			if !bytes.Equal(normalized, payload) {
				changed = true
			}
			rebuilt.WriteString("data: ")
			rebuilt.Write(normalized)
			rebuilt.Write(lineEnding)
			continue
		}

		rebuilt.Write(line)
	}

	if sawStructuredLine {
		if changed {
			return rebuilt.Bytes()
		}
		return chunk
	}

	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return chunk
	}

	normalized := r.normalizePayload(trimmed)
	if bytes.Equal(normalized, trimmed) {
		return chunk
	}
	return normalized
}

func (r *responsesStreamRecovery) synthesizeCompletedPayload() []byte {
	if r == nil || r.completedSeen {
		return nil
	}

	recoveredOutput := r.buildRecoveredOutput()
	if len(recoveredOutput) == 0 {
		return nil
	}

	responseID := strings.TrimSpace(r.responseID)
	if responseID == "" {
		responseID = fmt.Sprintf("resp_recovered_%d", time.Now().UnixNano())
	}
	createdAt := r.createdAt
	if createdAt == 0 {
		createdAt = time.Now().Unix()
	}

	payload := []byte(`{"type":"response.completed","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":[]}}`)
	payload, _ = sjson.SetBytes(payload, "response.id", responseID)
	payload, _ = sjson.SetBytes(payload, "response.created_at", createdAt)
	if strings.TrimSpace(r.model) != "" {
		payload, _ = sjson.SetBytes(payload, "response.model", r.model)
	}
	if r.sequenceSeen {
		payload, _ = sjson.SetBytes(payload, "sequence_number", r.lastSequence+1)
	}
	if len(r.usageRaw) > 0 && json.Valid(r.usageRaw) {
		payload, _ = sjson.SetRawBytes(payload, "response.usage", r.usageRaw)
	}
	for i := range recoveredOutput {
		payload, _ = sjson.SetRawBytes(payload, "response.output.-1", recoveredOutput[i].Raw)
	}

	r.completedSeen = true
	return payload
}

func (r *responsesStreamRecovery) normalizeFrame(frame []byte) []byte {
	if r == nil || len(frame) == 0 {
		return frame
	}

	eventName, payload := responsesSSEFrameEventAndPayload(frame)
	if eventName != "error" || len(payload) == 0 || !json.Valid(payload) {
		return frame
	}

	normalized := r.normalizePayload(payload)
	if gjson.GetBytes(normalized, "type").String() != "response.completed" {
		return frame
	}

	rebuilt := append([]byte("data: "), normalized...)
	if bytes.HasSuffix(frame, []byte("\r\n\r\n")) {
		return append(rebuilt, []byte("\r\n\r\n")...)
	}
	return append(rebuilt, []byte("\n\n")...)
}

func (r *responsesStreamRecovery) normalizePayload(payload []byte) []byte {
	if r == nil {
		return payload
	}

	payload = bytes.TrimSpace(bytes.Clone(payload))
	if len(payload) == 0 || !json.Valid(payload) {
		return payload
	}

	eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	if eventType == "response.done" {
		if updated, err := sjson.SetBytes(payload, "type", "response.completed"); err == nil && len(updated) > 0 {
			payload = updated
			eventType = "response.completed"
		}
	}

	r.captureMeta(payload)

	switch eventType {
	case "response.output_item.added":
		r.recordOutputItemAdded(payload)
	case "response.output_item.done":
		r.recordOutputItemDone(payload)
	case "response.output_text.delta":
		r.recordOutputTextDelta(payload)
	case "response.output_text.done":
		r.recordOutputTextDone(payload)
	case "response.function_call_arguments.delta":
		r.recordFunctionCallArgumentsDelta(payload)
	case "response.function_call_arguments.done":
		r.recordFunctionCallArgumentsDone(payload)
	case "response.custom_tool_call_input.delta":
		r.recordCustomToolCallInputDelta(payload)
	case "response.custom_tool_call_input.done":
		r.recordCustomToolCallInputDone(payload)
	case "response.completed":
		payload = r.patchCompletedPayload(payload)
		r.captureMeta(payload)
		r.completedSeen = true
	case "error":
		if recovered := r.recoverErrorPayload(payload); len(recovered) > 0 {
			payload = recovered
		}
	}

	return payload
}

func (r *responsesStreamRecovery) recoverErrorPayload(payload []byte) []byte {
	if r == nil || r.completedSeen || len(payload) == 0 || !json.Valid(payload) {
		return nil
	}
	if strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "error" {
		return nil
	}
	code := strings.TrimSpace(gjson.GetBytes(payload, "code").String())
	message := strings.TrimSpace(gjson.GetBytes(payload, "message").String())
	if !responsesErrorLooksRecoverable(code, message) {
		return nil
	}
	return r.synthesizeCompletedPayload()
}

func (r *responsesStreamRecovery) recoverTerminalErrorPayload(errMsg *interfaces.ErrorMessage) []byte {
	if r == nil || errMsg == nil {
		return nil
	}

	status := errMsg.StatusCode
	if status <= 0 {
		status = 500
	}

	errText := ""
	if errMsg.Error != nil {
		errText = errMsg.Error.Error()
	}

	payload := handlers.BuildOpenAIResponsesStreamErrorChunk(status, errText, 0)
	return r.recoverErrorPayload(payload)
}

func (r *responsesStreamRecovery) captureMeta(payload []byte) {
	if r == nil || len(payload) == 0 {
		return
	}

	if seq := gjson.GetBytes(payload, "sequence_number"); seq.Exists() {
		r.sequenceSeen = true
		r.lastSequence = seq.Int()
	}

	response := gjson.GetBytes(payload, "response")
	if response.Exists() {
		if id := strings.TrimSpace(response.Get("id").String()); id != "" {
			r.responseID = id
		}
		if createdAt := response.Get("created_at").Int(); createdAt > 0 {
			r.createdAt = createdAt
		}
		if model := strings.TrimSpace(response.Get("model").String()); model != "" {
			r.model = model
		}
		if usage := response.Get("usage"); usage.Exists() && usage.Type == gjson.JSON && json.Valid([]byte(usage.Raw)) {
			r.usageRaw = []byte(usage.Raw)
		}
	}
}

func (r *responsesStreamRecovery) recordOutputItemAdded(payload []byte) {
	item := gjson.GetBytes(payload, "item")
	if !item.Exists() || item.Type != gjson.JSON {
		return
	}

	outputIndex := gjson.GetBytes(payload, "output_index").Int()
	switch item.Get("type").String() {
	case "message":
		message := r.ensureMessage(strings.TrimSpace(item.Get("id").String()), outputIndex)
		if message != nil && message.ItemID == "" {
			message.ItemID = strings.TrimSpace(item.Get("id").String())
		}
	case "function_call", "custom_tool_call":
		call := r.ensureFunctionCall(strings.TrimSpace(item.Get("id").String()), outputIndex)
		if call == nil {
			return
		}
		if itemType := strings.TrimSpace(item.Get("type").String()); itemType != "" {
			call.ItemType = itemType
		}
		if callID := strings.TrimSpace(item.Get("call_id").String()); callID != "" {
			call.CallID = callID
		}
		if name := strings.TrimSpace(item.Get("name").String()); name != "" {
			call.Name = name
		}
	}
}

func (r *responsesStreamRecovery) recordOutputItemDone(payload []byte) {
	item := gjson.GetBytes(payload, "item")
	if !item.Exists() || item.Type != gjson.JSON || !json.Valid([]byte(item.Raw)) {
		return
	}

	raw := []byte(item.Raw)
	if outputIndex := gjson.GetBytes(payload, "output_index"); outputIndex.Exists() {
		r.outputItemsByIndex[outputIndex.Int()] = raw
		return
	}
	r.outputItemsFallback = append(r.outputItemsFallback, raw)
}

func (r *responsesStreamRecovery) recordOutputTextDelta(payload []byte) {
	message := r.ensureMessage(
		strings.TrimSpace(gjson.GetBytes(payload, "item_id").String()),
		gjson.GetBytes(payload, "output_index").Int(),
	)
	if message == nil {
		return
	}
	if delta := gjson.GetBytes(payload, "delta").String(); delta != "" {
		message.Text.WriteString(delta)
	}
}

func (r *responsesStreamRecovery) recordOutputTextDone(payload []byte) {
	message := r.ensureMessage(
		strings.TrimSpace(gjson.GetBytes(payload, "item_id").String()),
		gjson.GetBytes(payload, "output_index").Int(),
	)
	if message == nil {
		return
	}
	if text := gjson.GetBytes(payload, "text").String(); text != "" {
		message.Text.Reset()
		message.Text.WriteString(text)
	}
}

func (r *responsesStreamRecovery) recordFunctionCallArgumentsDelta(payload []byte) {
	call := r.ensureFunctionCall(
		strings.TrimSpace(gjson.GetBytes(payload, "item_id").String()),
		gjson.GetBytes(payload, "output_index").Int(),
	)
	if call == nil {
		return
	}
	call.ItemType = "function_call"
	if delta := gjson.GetBytes(payload, "delta").String(); delta != "" {
		call.Arguments.WriteString(delta)
	}
}

func (r *responsesStreamRecovery) recordFunctionCallArgumentsDone(payload []byte) {
	call := r.ensureFunctionCall(
		strings.TrimSpace(gjson.GetBytes(payload, "item_id").String()),
		gjson.GetBytes(payload, "output_index").Int(),
	)
	if call == nil {
		return
	}
	call.ItemType = "function_call"
	if args := gjson.GetBytes(payload, "arguments").String(); args != "" {
		call.Arguments.Reset()
		call.Arguments.WriteString(args)
	}
}

func (r *responsesStreamRecovery) recordCustomToolCallInputDelta(payload []byte) {
	call := r.ensureFunctionCall(
		strings.TrimSpace(gjson.GetBytes(payload, "item_id").String()),
		gjson.GetBytes(payload, "output_index").Int(),
	)
	if call == nil {
		return
	}
	call.ItemType = "custom_tool_call"
	if delta := gjson.GetBytes(payload, "delta").String(); delta != "" {
		call.Input.WriteString(delta)
	}
}

func (r *responsesStreamRecovery) recordCustomToolCallInputDone(payload []byte) {
	call := r.ensureFunctionCall(
		strings.TrimSpace(gjson.GetBytes(payload, "item_id").String()),
		gjson.GetBytes(payload, "output_index").Int(),
	)
	if call == nil {
		return
	}
	call.ItemType = "custom_tool_call"
	if input := gjson.GetBytes(payload, "input").String(); input != "" {
		call.Input.Reset()
		call.Input.WriteString(input)
	}
}

func (r *responsesStreamRecovery) patchCompletedPayload(payload []byte) []byte {
	output := gjson.GetBytes(payload, "response.output")
	if output.Exists() && output.IsArray() && len(output.Array()) > 0 {
		return payload
	}

	recoveredOutput := r.buildRecoveredOutput()
	if len(recoveredOutput) == 0 {
		return payload
	}

	patched := payload
	patched, _ = sjson.SetRawBytes(patched, "response.output", []byte(`[]`))
	for i := range recoveredOutput {
		patched, _ = sjson.SetRawBytes(patched, "response.output.-1", recoveredOutput[i].Raw)
	}
	if gjson.GetBytes(patched, "response.id").String() == "" && strings.TrimSpace(r.responseID) != "" {
		patched, _ = sjson.SetBytes(patched, "response.id", r.responseID)
	}
	if gjson.GetBytes(patched, "response.created_at").Int() == 0 && r.createdAt > 0 {
		patched, _ = sjson.SetBytes(patched, "response.created_at", r.createdAt)
	}
	if gjson.GetBytes(patched, "response.model").String() == "" && strings.TrimSpace(r.model) != "" {
		patched, _ = sjson.SetBytes(patched, "response.model", r.model)
	}
	return patched
}

func (r *responsesStreamRecovery) buildRecoveredOutput() []responsesRecoveredOutput {
	recovered := make([]responsesRecoveredOutput, 0, len(r.outputItemsByIndex)+len(r.outputItemsFallback)+len(r.messagesByID)+len(r.functionCallsByID))
	seenItemIDs := make(map[string]struct{})
	seenCallIDs := make(map[string]struct{})

	indexes := make([]int64, 0, len(r.outputItemsByIndex))
	for idx := range r.outputItemsByIndex {
		indexes = append(indexes, idx)
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
	for _, idx := range indexes {
		raw := r.outputItemsByIndex[idx]
		recovered = append(recovered, responsesRecoveredOutput{OutputIndex: idx, Raw: raw})
		recordRecoveredIdentifiers(raw, seenItemIDs, seenCallIDs)
	}

	nextFallbackIndex := r.maxKnownOutputIndex() + 1
	for i := range r.outputItemsFallback {
		raw := r.outputItemsFallback[i]
		recovered = append(recovered, responsesRecoveredOutput{OutputIndex: nextFallbackIndex, Raw: raw})
		nextFallbackIndex++
		recordRecoveredIdentifiers(raw, seenItemIDs, seenCallIDs)
	}

	messageIndexes := make([]int64, 0, len(r.messagesByIndex))
	for idx := range r.messagesByIndex {
		messageIndexes = append(messageIndexes, idx)
	}
	sort.Slice(messageIndexes, func(i, j int) bool { return messageIndexes[i] < messageIndexes[j] })

	functionIndexes := make([]int64, 0, len(r.functionCallsByIndex))
	for idx := range r.functionCallsByIndex {
		functionIndexes = append(functionIndexes, idx)
	}
	sort.Slice(functionIndexes, func(i, j int) bool { return functionIndexes[i] < functionIndexes[j] })

	for _, idx := range functionIndexes {
		call := r.functionCallsByIndex[idx]
		if call == nil {
			continue
		}
		itemID := strings.TrimSpace(call.ItemID)
		callID := strings.TrimSpace(call.CallID)
		if itemID != "" {
			if _, exists := seenItemIDs[itemID]; exists {
				continue
			}
		}
		if callID != "" {
			if _, exists := seenCallIDs[callID]; exists {
				continue
			}
		}
		raw := buildRecoveredFunctionCallRaw(call)
		if len(raw) == 0 {
			continue
		}
		recovered = append(recovered, responsesRecoveredOutput{OutputIndex: idx, Raw: raw})
		recordRecoveredIdentifiers(raw, seenItemIDs, seenCallIDs)
	}

	for _, idx := range messageIndexes {
		message := r.messagesByIndex[idx]
		if message == nil {
			continue
		}
		itemID := strings.TrimSpace(message.ItemID)
		if itemID != "" {
			if _, exists := seenItemIDs[itemID]; exists {
				continue
			}
		}
		raw := buildRecoveredMessageRaw(r.responseID, message)
		if len(raw) == 0 {
			continue
		}
		recovered = append(recovered, responsesRecoveredOutput{OutputIndex: idx, Raw: raw})
		recordRecoveredIdentifiers(raw, seenItemIDs, seenCallIDs)
	}

	sort.SliceStable(recovered, func(i, j int) bool {
		return recovered[i].OutputIndex < recovered[j].OutputIndex
	})
	return recovered
}

func (r *responsesStreamRecovery) ensureMessage(itemID string, outputIndex int64) *responsesRecoveredMessage {
	itemID = strings.TrimSpace(itemID)
	if itemID != "" {
		if message, ok := r.messagesByID[itemID]; ok {
			if message.OutputIndex < 0 && outputIndex >= 0 {
				message.OutputIndex = outputIndex
				r.messagesByIndex[outputIndex] = message
			}
			return message
		}
	}
	if outputIndex >= 0 {
		if message, ok := r.messagesByIndex[outputIndex]; ok {
			if message.ItemID == "" && itemID != "" {
				message.ItemID = itemID
				r.messagesByID[itemID] = message
			}
			return message
		}
	}

	message := &responsesRecoveredMessage{ItemID: itemID, OutputIndex: outputIndex}
	if itemID != "" {
		r.messagesByID[itemID] = message
	}
	if outputIndex >= 0 {
		r.messagesByIndex[outputIndex] = message
	}
	return message
}

func (r *responsesStreamRecovery) ensureFunctionCall(itemID string, outputIndex int64) *responsesRecoveredFunctionCall {
	itemID = strings.TrimSpace(itemID)
	if itemID != "" {
		if call, ok := r.functionCallsByID[itemID]; ok {
			if call.OutputIndex < 0 && outputIndex >= 0 {
				call.OutputIndex = outputIndex
				r.functionCallsByIndex[outputIndex] = call
			}
			return call
		}
	}
	if outputIndex >= 0 {
		if call, ok := r.functionCallsByIndex[outputIndex]; ok {
			if call.ItemID == "" && itemID != "" {
				call.ItemID = itemID
				r.functionCallsByID[itemID] = call
			}
			return call
		}
	}

	call := &responsesRecoveredFunctionCall{ItemID: itemID, OutputIndex: outputIndex}
	if itemID != "" {
		r.functionCallsByID[itemID] = call
	}
	if outputIndex >= 0 {
		r.functionCallsByIndex[outputIndex] = call
	}
	return call
}

func (r *responsesStreamRecovery) maxKnownOutputIndex() int64 {
	maxIndex := int64(-1)
	for idx := range r.outputItemsByIndex {
		if idx > maxIndex {
			maxIndex = idx
		}
	}
	for idx := range r.messagesByIndex {
		if idx > maxIndex {
			maxIndex = idx
		}
	}
	for idx := range r.functionCallsByIndex {
		if idx > maxIndex {
			maxIndex = idx
		}
	}
	return maxIndex
}

func buildRecoveredMessageRaw(responseID string, message *responsesRecoveredMessage) []byte {
	if message == nil {
		return nil
	}
	text := message.Text.String()
	if text == "" {
		return nil
	}
	item := []byte(`{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`)
	itemID := strings.TrimSpace(message.ItemID)
	if itemID == "" {
		itemID = fmt.Sprintf("msg_%s_%d", strings.TrimSpace(responseID), message.OutputIndex)
	}
	item, _ = sjson.SetBytes(item, "id", itemID)
	item, _ = sjson.SetBytes(item, "content.0.text", text)
	return item
}

func buildRecoveredFunctionCallRaw(call *responsesRecoveredFunctionCall) []byte {
	if call == nil {
		return nil
	}
	itemType := strings.TrimSpace(call.ItemType)
	if itemType == "" {
		itemType = "function_call"
	}
	callID := strings.TrimSpace(call.CallID)
	name := strings.TrimSpace(call.Name)
	if callID == "" && name == "" {
		return nil
	}

	itemID := strings.TrimSpace(call.ItemID)
	if itemID == "" {
		switch itemType {
		case "custom_tool_call":
			if callID != "" {
				itemID = fmt.Sprintf("ctc_%s", callID)
			} else {
				itemID = fmt.Sprintf("ctc_%d", call.OutputIndex)
			}
		default:
			if callID != "" {
				itemID = fmt.Sprintf("fc_%s", callID)
			} else {
				itemID = fmt.Sprintf("fc_%d", call.OutputIndex)
			}
		}
	}

	switch itemType {
	case "custom_tool_call":
		input := call.Input.String()
		item := []byte(`{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}`)
		item, _ = sjson.SetBytes(item, "id", itemID)
		item, _ = sjson.SetBytes(item, "input", input)
		item, _ = sjson.SetBytes(item, "call_id", callID)
		item, _ = sjson.SetBytes(item, "name", name)
		return item
	default:
		arguments := call.Arguments.String()
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		item := []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
		item, _ = sjson.SetBytes(item, "id", itemID)
		item, _ = sjson.SetBytes(item, "arguments", arguments)
		item, _ = sjson.SetBytes(item, "call_id", callID)
		item, _ = sjson.SetBytes(item, "name", name)
		return item
	}
}

func recordRecoveredIdentifiers(raw []byte, seenItemIDs, seenCallIDs map[string]struct{}) {
	if len(raw) == 0 {
		return
	}
	if itemID := strings.TrimSpace(gjson.GetBytes(raw, "id").String()); itemID != "" {
		seenItemIDs[itemID] = struct{}{}
	}
	if callID := strings.TrimSpace(gjson.GetBytes(raw, "call_id").String()); callID != "" {
		seenCallIDs[callID] = struct{}{}
	}
}

func lineEndingBytes(line []byte) []byte {
	switch {
	case bytes.HasSuffix(line, []byte("\r\n")):
		return []byte("\r\n")
	case bytes.HasSuffix(line, []byte("\n")):
		return []byte("\n")
	default:
		return nil
	}
}

func responsesSSEFrameEventAndPayload(frame []byte) (string, []byte) {
	lines := bytes.Split(frame, []byte("\n"))
	eventName := ""
	dataLines := make([][]byte, 0, 1)
	for i := range lines {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			eventName = strings.TrimSpace(string(line[len("event:"):]))
		case bytes.HasPrefix(line, []byte("data:")):
			data := bytes.TrimSpace(line[len("data:"):])
			if len(data) > 0 {
				dataLines = append(dataLines, bytes.Clone(data))
			}
		}
	}
	if len(dataLines) == 0 {
		return eventName, nil
	}
	return eventName, bytes.Join(dataLines, []byte("\n"))
}

func responsesErrorLooksRecoverable(code, message string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	message = strings.ToLower(strings.TrimSpace(message))
	if message == "" {
		return false
	}
	if code != "internal_server_error" && code != "server_error" {
		return false
	}
	return strings.Contains(message, "stream error:") ||
		strings.Contains(message, "received from peer") ||
		strings.Contains(message, "unexpected eof") ||
		strings.Contains(message, "stream closed") ||
		strings.Contains(message, "response.completed")
}
