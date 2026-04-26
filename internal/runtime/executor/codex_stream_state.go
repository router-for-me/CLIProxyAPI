package executor

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var dataTag = []byte("data:")

type codexStreamFunctionCallState struct {
	ItemID           string
	CallID           string
	Name             string
	Arguments        string
	argumentsBuilder strings.Builder
	OutputIndex      int64
}

type codexStreamCompletionState struct {
	outputItemsByIndex  map[int64][]byte
	outputItemsFallback [][]byte
	functionCallsByItem map[string]*codexStreamFunctionCallState
}

type codexCompletedStreamEvent struct {
	data           []byte
	recoveredCount int
}

func newCodexStreamCompletionState() *codexStreamCompletionState {
	return &codexStreamCompletionState{
		outputItemsByIndex:  make(map[int64][]byte),
		functionCallsByItem: make(map[string]*codexStreamFunctionCallState),
	}
}

func (s *codexStreamFunctionCallState) appendArgumentsDelta(delta string) {
	if s == nil || delta == "" {
		return
	}
	if s.Arguments != "" && s.argumentsBuilder.Len() == 0 {
		s.argumentsBuilder.WriteString(s.Arguments)
		s.Arguments = ""
	}
	s.argumentsBuilder.WriteString(delta)
}

func (s *codexStreamFunctionCallState) setArguments(arguments string) {
	if s == nil {
		return
	}
	s.Arguments = arguments
	if s.argumentsBuilder.Len() > 0 {
		s.argumentsBuilder.Reset()
	}
}

func (s *codexStreamFunctionCallState) arguments() string {
	if s == nil {
		return ""
	}
	if s.Arguments != "" {
		return s.Arguments
	}
	if s.argumentsBuilder.Len() > 0 {
		return s.argumentsBuilder.String()
	}
	return ""
}

func (s *codexStreamCompletionState) functionCallByItem(itemID string, outputIndex int64) *codexStreamFunctionCallState {
	if s == nil {
		return nil
	}
	itemID = strings.TrimSpace(itemID)
	if itemID != "" {
		if state, ok := s.functionCallsByItem[itemID]; ok && state != nil {
			return state
		}
	}
	if outputIndex < 0 {
		return nil
	}
	for _, state := range s.functionCallsByItem {
		if state != nil && state.OutputIndex == outputIndex {
			return state
		}
	}
	return nil
}

func codexEventData(line []byte) ([]byte, bool) {
	if !bytes.HasPrefix(line, dataTag) {
		return nil, false
	}
	return bytes.TrimSpace(line[len(dataTag):]), true
}

func codexSSEDataLine(data []byte) []byte {
	line := make([]byte, 0, len(dataTag)+1+len(data))
	line = append(line, dataTag...)
	line = append(line, ' ')
	line = append(line, data...)
	return line
}

func codexEventType(eventData []byte) string {
	if len(eventData) == 0 {
		return ""
	}
	return gjson.GetBytes(eventData, "type").String()
}

func (s *codexStreamCompletionState) recordEvent(eventData []byte) {
	s.recordEventWithType(codexEventType(eventData), eventData)
}

func (s *codexStreamCompletionState) recordEventWithType(eventType string, eventData []byte) {
	if s == nil || len(eventData) == 0 {
		return
	}

	switch eventType {
	case "response.output_item.done":
		itemResult := gjson.GetBytes(eventData, "item")
		if !itemResult.Exists() || itemResult.Type != gjson.JSON {
			return
		}
		itemBytes := []byte(itemResult.Raw)
		outputIndexResult := gjson.GetBytes(eventData, "output_index")
		if outputIndexResult.Exists() {
			s.outputItemsByIndex[outputIndexResult.Int()] = itemBytes
			return
		}
		s.outputItemsFallback = append(s.outputItemsFallback, itemBytes)
	case "response.output_item.added":
		item := gjson.GetBytes(eventData, "item")
		if !item.Exists() || item.Get("type").String() != "function_call" {
			return
		}
		itemID := strings.TrimSpace(item.Get("id").String())
		if itemID == "" {
			return
		}
		state := s.functionCallByItem(itemID, gjson.GetBytes(eventData, "output_index").Int())
		if state == nil {
			state = &codexStreamFunctionCallState{
				ItemID:      itemID,
				OutputIndex: gjson.GetBytes(eventData, "output_index").Int(),
			}
			s.functionCallsByItem[itemID] = state
		}
		if callID := strings.TrimSpace(item.Get("call_id").String()); callID != "" {
			state.CallID = callID
		}
		if name := strings.TrimSpace(item.Get("name").String()); name != "" {
			state.Name = name
		}
	case "response.function_call_arguments.delta":
		itemID := strings.TrimSpace(gjson.GetBytes(eventData, "item_id").String())
		outputIndex := gjson.GetBytes(eventData, "output_index").Int()
		state := s.functionCallByItem(itemID, outputIndex)
		if state == nil {
			return
		}
		state.appendArgumentsDelta(gjson.GetBytes(eventData, "delta").String())
	case "response.function_call_arguments.done":
		itemID := strings.TrimSpace(gjson.GetBytes(eventData, "item_id").String())
		outputIndex := gjson.GetBytes(eventData, "output_index").Int()
		state := s.functionCallByItem(itemID, outputIndex)
		if state == nil {
			return
		}
		if arguments := gjson.GetBytes(eventData, "arguments").String(); arguments != "" {
			state.setArguments(arguments)
		}
	}
}

func (s *codexStreamCompletionState) processEventData(eventData []byte, patchCompleted bool) (codexCompletedStreamEvent, bool) {
	return s.processEventDataWithType(codexEventType(eventData), eventData, patchCompleted)
}

func (s *codexStreamCompletionState) processEventDataWithType(eventType string, eventData []byte, patchCompleted bool) (codexCompletedStreamEvent, bool) {
	if s == nil || len(eventData) == 0 {
		return codexCompletedStreamEvent{}, false
	}

	s.recordEventWithType(eventType, eventData)
	if eventType != "response.completed" {
		return codexCompletedStreamEvent{}, false
	}

	completed := codexCompletedStreamEvent{data: eventData}
	if patchCompleted {
		if patched, recoveredCount := s.patchCompletedOutputIfEmpty(eventData); recoveredCount > 0 {
			completed.data = patched
			completed.recoveredCount = recoveredCount
		}
	}
	return completed, true
}

func (s *codexStreamCompletionState) patchCompletedOutputIfEmpty(completedData []byte) ([]byte, int) {
	if s == nil || len(completedData) == 0 {
		return completedData, 0
	}

	outputResult := gjson.GetBytes(completedData, "response.output")
	if outputResult.Exists() && outputResult.IsArray() && outputResult.Get("#").Int() > 0 {
		return completedData, 0
	}

	if len(s.functionCallsByItem) == 0 {
		return s.patchCompletedOutputFromRecordedItemsOnly(completedData)
	}

	type recoveredItem struct {
		outputIndex int64
		raw         []byte
	}

	recovered := make([]recoveredItem, 0, len(s.outputItemsByIndex)+len(s.outputItemsFallback)+len(s.functionCallsByItem))
	seenCallIDs := make(map[string]struct{}, len(s.functionCallsByItem))
	seenItemIDs := make(map[string]struct{}, len(s.functionCallsByItem))

	indexes := make([]int64, 0, len(s.outputItemsByIndex))
	for idx := range s.outputItemsByIndex {
		indexes = append(indexes, idx)
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
	for _, idx := range indexes {
		raw := s.outputItemsByIndex[idx]
		recovered = append(recovered, recoveredItem{outputIndex: idx, raw: raw})
		if callID := strings.TrimSpace(gjson.GetBytes(raw, "call_id").String()); callID != "" {
			seenCallIDs[callID] = struct{}{}
		}
		if itemID := strings.TrimSpace(gjson.GetBytes(raw, "id").String()); itemID != "" {
			seenItemIDs[itemID] = struct{}{}
		}
	}
	for _, raw := range s.outputItemsFallback {
		recovered = append(recovered, recoveredItem{outputIndex: int64(len(indexes) + len(recovered)), raw: raw})
		if callID := strings.TrimSpace(gjson.GetBytes(raw, "call_id").String()); callID != "" {
			seenCallIDs[callID] = struct{}{}
		}
		if itemID := strings.TrimSpace(gjson.GetBytes(raw, "id").String()); itemID != "" {
			seenItemIDs[itemID] = struct{}{}
		}
	}

	if len(s.functionCallsByItem) > 0 {
		keys := make([]string, 0, len(s.functionCallsByItem))
		for key := range s.functionCallsByItem {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			left := s.functionCallsByItem[keys[i]]
			right := s.functionCallsByItem[keys[j]]
			if left == nil || right == nil {
				return keys[i] < keys[j]
			}
			if left.OutputIndex != right.OutputIndex {
				return left.OutputIndex < right.OutputIndex
			}
			return keys[i] < keys[j]
		})
		for _, key := range keys {
			state := s.functionCallsByItem[key]
			if state == nil || strings.TrimSpace(state.CallID) == "" {
				continue
			}
			if _, ok := seenCallIDs[state.CallID]; ok {
				continue
			}
			if _, ok := seenItemIDs[state.ItemID]; ok {
				continue
			}

			args := state.arguments()
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			itemID := state.ItemID
			if strings.TrimSpace(itemID) == "" {
				itemID = fmt.Sprintf("fc_%s", state.CallID)
			}

			item := buildCodexCompletedFunctionCallItem(itemID, state.CallID, state.Name, args)
			recovered = append(recovered, recoveredItem{outputIndex: state.OutputIndex, raw: item})
			seenCallIDs[state.CallID] = struct{}{}
			seenItemIDs[itemID] = struct{}{}
		}
	}

	if len(recovered) == 0 {
		return completedData, 0
	}

	sort.SliceStable(recovered, func(i, j int) bool {
		return recovered[i].outputIndex < recovered[j].outputIndex
	})

	patched := completedData
	outputArray := []byte("[]")
	if len(recovered) > 0 {
		var buf bytes.Buffer
		totalLen := 2
		for _, item := range recovered {
			totalLen += len(item.raw)
		}
		if len(recovered) > 1 {
			totalLen += len(recovered) - 1
		}
		buf.Grow(totalLen)
		buf.WriteByte('[')
		for i, item := range recovered {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.Write(item.raw)
		}
		buf.WriteByte(']')
		outputArray = buf.Bytes()
	}
	patched, _ = sjson.SetRawBytes(patched, "response.output", outputArray)
	return patched, len(recovered)
}

func (s *codexStreamCompletionState) patchCompletedOutputFromRecordedItemsOnly(completedData []byte) ([]byte, int) {
	totalItems := len(s.outputItemsByIndex) + len(s.outputItemsFallback)
	if totalItems == 0 {
		return completedData, 0
	}

	if len(s.outputItemsFallback) == 0 && len(s.outputItemsByIndex) == 1 {
		for _, raw := range s.outputItemsByIndex {
			return patchCodexCompletedOutputWithSingleItem(completedData, raw), 1
		}
	}
	if len(s.outputItemsByIndex) == 0 {
		return patchCodexCompletedOutputWithItems(completedData, s.outputItemsFallback), len(s.outputItemsFallback)
	}

	items := make([][]byte, 0, totalItems)
	indexes := make([]int64, 0, len(s.outputItemsByIndex))
	for idx := range s.outputItemsByIndex {
		indexes = append(indexes, idx)
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
	for _, idx := range indexes {
		items = append(items, s.outputItemsByIndex[idx])
	}
	items = append(items, s.outputItemsFallback...)
	return patchCodexCompletedOutputWithItems(completedData, items), totalItems
}

func patchCodexCompletedOutputWithSingleItem(completedData []byte, item []byte) []byte {
	outputArray := make([]byte, 0, len(item)+2)
	outputArray = append(outputArray, '[')
	outputArray = append(outputArray, item...)
	outputArray = append(outputArray, ']')
	patched, _ := sjson.SetRawBytes(completedData, "response.output", outputArray)
	return patched
}

func patchCodexCompletedOutputWithItems(completedData []byte, items [][]byte) []byte {
	if len(items) == 0 {
		return completedData
	}
	if len(items) == 1 {
		return patchCodexCompletedOutputWithSingleItem(completedData, items[0])
	}

	totalLen := 2 + len(items) - 1
	for _, item := range items {
		totalLen += len(item)
	}
	outputArray := make([]byte, 0, totalLen)
	outputArray = append(outputArray, '[')
	for i, item := range items {
		if i > 0 {
			outputArray = append(outputArray, ',')
		}
		outputArray = append(outputArray, item...)
	}
	outputArray = append(outputArray, ']')
	patched, _ := sjson.SetRawBytes(completedData, "response.output", outputArray)
	return patched
}

func buildCodexCompletedFunctionCallItem(itemID string, callID string, name string, args string) []byte {
	buf := make([]byte, 0, len(itemID)+len(callID)+len(name)+len(args)+80)
	buf = append(buf, `{"id":`...)
	buf = strconv.AppendQuote(buf, itemID)
	buf = append(buf, `,"type":"function_call","status":"completed","arguments":`...)
	buf = strconv.AppendQuote(buf, args)
	buf = append(buf, `,"call_id":`...)
	buf = strconv.AppendQuote(buf, callID)
	buf = append(buf, `,"name":`...)
	buf = strconv.AppendQuote(buf, name)
	buf = append(buf, '}')
	return buf
}
