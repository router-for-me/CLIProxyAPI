package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

var responsesReasoningLifecycleEvents = map[string]struct{}{
	"response.reasoning_summary_part.added": {},
	"response.reasoning_summary_text.delta": {},
	"response.reasoning_summary_text.done":  {},
	"response.reasoning_text.delta":         {},
	"response.reasoning_text.done":          {},
}

var responsesMessageLifecycleEvents = map[string]struct{}{
	"response.output_text.delta": {},
	"response.output_text.done":  {},
	"response.refusal.delta":     {},
	"response.refusal.done":      {},
}

type responsesSSEActiveItem struct {
	id          string
	itemType    string
	outputIndex any
	summaries   map[int]string
	messageText string
	messageType string
}

// responsesSSELifecycleState repairs provider-compatible Responses streams that
// omit item start/end events around reasoning or text deltas. Ambiguous overlap
// fails closed instead of guessing which item owns a delta.
type responsesSSELifecycleState struct {
	pending               []byte
	active                *responsesSSEActiveItem
	activeExplicit        map[string]*responsesSSEActiveItem
	activeExplicitByIndex map[string]string
	closed                map[string]struct{}
	nextSequenceNumber    int64
	sawTerminal           bool
}

func responsesSSELifecycleEnabled(metadata map[string]any) bool {
	compatible, _ := metadata[coreexecutor.SelectedModelCompatibilityMetadataKey].(bool)
	return compatible
}

func (s *responsesSSELifecycleState) AddChunk(chunk []byte) ([]byte, error) {
	if len(chunk) == 0 {
		return nil, nil
	}
	chunk = bytes.ReplaceAll(chunk, []byte("\r\n"), []byte("\n"))
	chunk = bytes.ReplaceAll(chunk, []byte("\r"), []byte("\n"))
	if len(s.pending) > 0 && !bytes.HasSuffix(s.pending, []byte("\n")) && !bytes.HasPrefix(chunk, []byte("\n")) && responsesSSEChunkStartsField(chunk) {
		s.pending = append(s.pending, '\n')
	}
	s.pending = append(s.pending, chunk...)

	var output []byte
	for {
		frameEnd := bytes.Index(s.pending, []byte("\n\n"))
		if frameEnd < 0 {
			break
		}
		frameEnd += 2
		frame := bytes.Clone(s.pending[:frameEnd])
		copy(s.pending, s.pending[frameEnd:])
		s.pending = s.pending[:len(s.pending)-frameEnd]
		normalized, err := s.normalizeFrame(frame)
		if err != nil {
			return nil, err
		}
		output = append(output, normalized...)
	}

	return output, nil
}

func (s *responsesSSELifecycleState) Finish() error {
	if len(bytes.TrimSpace(s.pending)) != 0 {
		return fmt.Errorf("responses SSE lifecycle stream ended with an incomplete event")
	}
	s.pending = nil
	if s.active != nil {
		return fmt.Errorf("responses SSE lifecycle stream ended with active item %s", s.active.id)
	}
	if len(s.activeExplicit) != 0 {
		return fmt.Errorf("responses SSE lifecycle stream ended with %d active explicit items", len(s.activeExplicit))
	}
	if !s.sawTerminal {
		return fmt.Errorf("responses SSE lifecycle stream ended before a terminal response event")
	}
	return nil
}

func (s *responsesSSELifecycleState) normalizeFrame(frame []byte) ([]byte, error) {
	payload, found := sseJSONValidationDataPayload(frame)
	payload = bytes.TrimSpace(payload)
	if !found || len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return frame, nil
	}
	var event map[string]any
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, fmt.Errorf("invalid Responses SSE event: %w", err)
	}
	events, err := s.normalizeEvent(event)
	if err != nil {
		return nil, err
	}
	var output []byte
	originalEventName := responsesSSEEventName(frame)
	originalForwardedFields := responsesSSEForwardedFields(frame)
	for index, normalized := range events {
		normalized["sequence_number"] = s.nextSequenceNumber
		s.nextSequenceNumber++
		data, errMarshal := json.Marshal(normalized)
		if errMarshal != nil {
			return nil, fmt.Errorf("encode normalized Responses SSE event: %w", errMarshal)
		}
		eventName, _ := normalized["type"].(string)
		if index == len(events)-1 && originalEventName != "" {
			eventName = originalEventName
		}
		if index == len(events)-1 {
			for _, field := range originalForwardedFields {
				output = append(output, field...)
				output = append(output, '\n')
			}
		}
		if eventName != "" {
			output = append(output, []byte("event: ")...)
			output = append(output, eventName...)
			output = append(output, '\n')
		}
		output = append(output, []byte("data: ")...)
		output = append(output, data...)
		output = append(output, '\n', '\n')
	}
	return output, nil
}

func (s *responsesSSELifecycleState) normalizeEvent(event map[string]any) ([]map[string]any, error) {
	eventType, _ := event["type"].(string)
	switch eventType {
	case "response.output_item.added":
		return s.onItemAdded(event)
	case "response.output_item.done":
		return s.onItemDone(event)
	case "response.content_part.added":
		return s.onContentPartAdded(event)
	case "response.completed", "response.done":
		out, err := s.closeActive("completed")
		if err != nil {
			return nil, err
		}
		if err = s.rejectActiveExplicitItems(); err != nil {
			return nil, err
		}
		s.sawTerminal = true
		return append(out, event), nil
	case "response.incomplete":
		out, err := s.closeActive("incomplete")
		if err != nil {
			return nil, err
		}
		if err = s.rejectActiveExplicitItems(); err != nil {
			return nil, err
		}
		s.sawTerminal = true
		return append(out, event), nil
	case "response.failed", "response.error", "error":
		s.active = nil
		s.activeExplicit = nil
		s.activeExplicitByIndex = nil
		s.sawTerminal = true
		return []map[string]any{event}, nil
	}
	if _, ok := responsesReasoningLifecycleEvents[eventType]; ok {
		return s.onDelta(event, "reasoning")
	}
	if _, ok := responsesMessageLifecycleEvents[eventType]; ok {
		return s.onDelta(event, "message")
	}
	return []map[string]any{event}, nil
}

func (s *responsesSSELifecycleState) onContentPartAdded(event map[string]any) ([]map[string]any, error) {
	part, _ := event["part"].(map[string]any)
	partType, _ := part["type"].(string)
	switch partType {
	case "output_text", "refusal":
		return s.onDelta(event, "message")
	case "reasoning_text", "summary_text":
		return s.onDelta(event, "reasoning")
	default:
		return []map[string]any{event}, nil
	}
}

func (s *responsesSSELifecycleState) onItemAdded(event map[string]any) ([]map[string]any, error) {
	item, itemID, itemType, ok := responsesSSEItem(event)
	if !ok {
		return nil, fmt.Errorf("invalid response.output_item.added event")
	}
	if s.closedContains(itemID) {
		return nil, nil
	}
	if !responsesSSESemanticItemType(itemType) {
		return s.onExplicitItemAdded(event, item, itemID, itemType)
	}
	if explicit := s.activeExplicit[itemID]; explicit != nil {
		return nil, fmt.Errorf("conflicting active Responses output item %s", itemID)
	}
	if existingID := s.activeExplicitByIndex[responsesSSEOutputIndexKey(event["output_index"])]; existingID != "" {
		return nil, fmt.Errorf("Responses output index %v is active for both %s and %s", event["output_index"], existingID, itemID)
	}
	if s.active != nil {
		if s.active.id == itemID && s.active.itemType == itemType {
			return nil, nil
		}
		return nil, fmt.Errorf("overlapping active Responses output items: %s and %s", s.active.id, itemID)
	}
	s.active = responsesSSEActiveFromItem(item, event["output_index"])
	return []map[string]any{event}, nil
}

func (s *responsesSSELifecycleState) onItemDone(event map[string]any) ([]map[string]any, error) {
	item, itemID, itemType, ok := responsesSSEItem(event)
	if !ok {
		return nil, fmt.Errorf("invalid response.output_item.done event")
	}
	if s.closedContains(itemID) {
		return nil, nil
	}
	if !responsesSSESemanticItemType(itemType) {
		return s.onExplicitItemDone(event, item, itemID, itemType)
	}
	var output []map[string]any
	if s.active == nil {
		if explicit := s.activeExplicit[itemID]; explicit != nil {
			return nil, fmt.Errorf("conflicting active Responses output item %s", itemID)
		}
		if existingID := s.activeExplicitByIndex[responsesSSEOutputIndexKey(event["output_index"])]; existingID != "" {
			return nil, fmt.Errorf("Responses output index %v is active for both %s and %s", event["output_index"], existingID, itemID)
		}
		s.active = responsesSSEActiveFromItem(responsesSSEEmptyItem(itemType, itemID), event["output_index"])
		output = append(output, responsesSSEAddedEvent(s.active))
	}
	if s.active.id != itemID || s.active.itemType != itemType {
		return nil, fmt.Errorf("response.output_item.done does not match active item %s", s.active.id)
	}
	output = append(output, event)
	s.markClosed(itemID)
	s.active = nil
	return output, nil
}

func (s *responsesSSELifecycleState) onDelta(event map[string]any, requiredType string) ([]map[string]any, error) {
	itemID, hasItemID := event["item_id"].(string)
	if hasItemID && strings.TrimSpace(itemID) == "" {
		return nil, fmt.Errorf("invalid empty item_id on Responses delta")
	}
	if itemID != "" && s.closedContains(itemID) {
		return nil, fmt.Errorf("Responses delta references closed item %s", itemID)
	}

	var output []map[string]any
	if s.active != nil && (s.active.itemType != requiredType || (itemID != "" && s.active.id != itemID)) {
		closed, err := s.closeActive("completed")
		if err != nil {
			return nil, err
		}
		output = append(output, closed...)
	}
	if s.active == nil {
		outputIndex := event["output_index"]
		if outputIndex == nil {
			outputIndex = len(s.closed)
		}
		if itemID == "" {
			itemID = fmt.Sprintf("normalized_%s_%v", requiredType, outputIndex)
		}
		if explicit := s.activeExplicit[itemID]; explicit != nil {
			return nil, fmt.Errorf("conflicting active Responses output item %s", itemID)
		}
		if existingID := s.activeExplicitByIndex[responsesSSEOutputIndexKey(outputIndex)]; existingID != "" {
			return nil, fmt.Errorf("Responses output index %v is active for both %s and %s", outputIndex, existingID, itemID)
		}
		s.active = responsesSSEActiveFromItem(responsesSSEEmptyItem(requiredType, itemID), outputIndex)
		output = append(output, responsesSSEAddedEvent(s.active))
	}
	event["item_id"] = s.active.id
	s.accumulate(event)
	return append(output, event), nil
}

func (s *responsesSSELifecycleState) closeActive(status string) ([]map[string]any, error) {
	if s.active == nil {
		return nil, nil
	}
	active := s.active
	if active.itemType != "reasoning" && active.itemType != "message" {
		return nil, fmt.Errorf("cannot synthesize completion for active Responses item type %s", active.itemType)
	}
	item := responsesSSEEmptyItem(active.itemType, active.id)
	switch active.itemType {
	case "reasoning":
		indexes := make([]int, 0, len(active.summaries))
		for index := range active.summaries {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		summaries := make([]any, 0, len(indexes))
		for _, index := range indexes {
			summaries = append(summaries, map[string]any{"type": "summary_text", "text": active.summaries[index]})
		}
		item["summary"] = summaries
	case "message":
		item["status"] = status
		contentType := active.messageType
		if contentType == "" {
			contentType = "output_text"
		}
		content := map[string]any{"type": contentType}
		if contentType == "refusal" {
			content["refusal"] = active.messageText
		} else {
			content["text"] = active.messageText
		}
		item["content"] = []any{content}
	}
	event := map[string]any{
		"type":         "response.output_item.done",
		"output_index": active.outputIndex,
		"item":         item,
	}
	s.markClosed(active.id)
	s.active = nil
	return []map[string]any{event}, nil
}

func (s *responsesSSELifecycleState) rejectActiveExplicitItems() error {
	if len(s.activeExplicit) == 0 {
		return nil
	}
	return fmt.Errorf("cannot synthesize completion with %d active explicit Responses items", len(s.activeExplicit))
}

func (s *responsesSSELifecycleState) onExplicitItemAdded(event, item map[string]any, itemID, itemType string) ([]map[string]any, error) {
	outputIndex := event["output_index"]
	if existing := s.activeExplicit[itemID]; existing != nil {
		if existing.itemType == itemType && responsesSSEOutputIndexKey(existing.outputIndex) == responsesSSEOutputIndexKey(outputIndex) {
			return nil, nil
		}
		return nil, fmt.Errorf("conflicting active Responses output item %s", itemID)
	}
	if s.active != nil {
		if s.active.id == itemID {
			return nil, fmt.Errorf("conflicting active Responses output item %s", itemID)
		}
		if responsesSSEOutputIndexKey(s.active.outputIndex) == responsesSSEOutputIndexKey(outputIndex) {
			return nil, fmt.Errorf("Responses output index %v is active for both %s and %s", outputIndex, s.active.id, itemID)
		}
	}
	indexKey := responsesSSEOutputIndexKey(outputIndex)
	if existingID := s.activeExplicitByIndex[indexKey]; indexKey != "" && existingID != "" && existingID != itemID {
		return nil, fmt.Errorf("Responses output index %v is active for both %s and %s", outputIndex, existingID, itemID)
	}
	if s.activeExplicit == nil {
		s.activeExplicit = make(map[string]*responsesSSEActiveItem)
	}
	if s.activeExplicitByIndex == nil {
		s.activeExplicitByIndex = make(map[string]string)
	}
	s.activeExplicit[itemID] = responsesSSEActiveFromItem(item, outputIndex)
	if indexKey != "" {
		s.activeExplicitByIndex[indexKey] = itemID
	}
	return []map[string]any{event}, nil
}

func (s *responsesSSELifecycleState) onExplicitItemDone(event, item map[string]any, itemID, itemType string) ([]map[string]any, error) {
	active := s.activeExplicit[itemID]
	if active == nil {
		if s.active != nil {
			if s.active.id == itemID {
				return nil, fmt.Errorf("conflicting active Responses output item %s", itemID)
			}
			if responsesSSEOutputIndexKey(s.active.outputIndex) == responsesSSEOutputIndexKey(event["output_index"]) {
				return nil, fmt.Errorf("Responses output index %v is active for both %s and %s", event["output_index"], s.active.id, itemID)
			}
		}
		if existingID := s.activeExplicitByIndex[responsesSSEOutputIndexKey(event["output_index"])]; existingID != "" {
			return nil, fmt.Errorf("Responses output index %v is active for both %s and %s", event["output_index"], existingID, itemID)
		}
		active = responsesSSEActiveFromItem(item, event["output_index"])
		s.markClosed(itemID)
		return []map[string]any{responsesSSEAddedEvent(active), event}, nil
	}
	if active.itemType != itemType || responsesSSEOutputIndexKey(active.outputIndex) != responsesSSEOutputIndexKey(event["output_index"]) {
		return nil, fmt.Errorf("response.output_item.done does not match active item %s", itemID)
	}
	delete(s.activeExplicit, itemID)
	delete(s.activeExplicitByIndex, responsesSSEOutputIndexKey(active.outputIndex))
	s.markClosed(itemID)
	return []map[string]any{event}, nil
}

func (s *responsesSSELifecycleState) accumulate(event map[string]any) {
	if s.active == nil {
		return
	}
	eventType, _ := event["type"].(string)
	summaryIndex := responsesSSEInt(event["summary_index"])
	switch eventType {
	case "response.reasoning_summary_part.added":
		if _, ok := s.active.summaries[summaryIndex]; !ok {
			s.active.summaries[summaryIndex] = ""
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		delta, _ := event["delta"].(string)
		s.active.summaries[summaryIndex] += delta
	case "response.reasoning_summary_text.done", "response.reasoning_text.done":
		text, _ := event["text"].(string)
		s.active.summaries[summaryIndex] = text
	case "response.output_text.delta":
		delta, _ := event["delta"].(string)
		s.active.messageText += delta
	case "response.output_text.done":
		text, _ := event["text"].(string)
		s.active.messageText = text
		s.active.messageType = "output_text"
	case "response.refusal.delta":
		delta, _ := event["delta"].(string)
		s.active.messageText += delta
		s.active.messageType = "refusal"
	case "response.refusal.done":
		text, _ := event["refusal"].(string)
		if text == "" {
			text, _ = event["text"].(string)
		}
		s.active.messageText = text
		s.active.messageType = "refusal"
	case "response.content_part.added":
		part, _ := event["part"].(map[string]any)
		partType, _ := part["type"].(string)
		if partType == "refusal" || partType == "output_text" {
			s.active.messageType = partType
		}
	}
}

func responsesSSEItem(event map[string]any) (map[string]any, string, string, bool) {
	item, ok := event["item"].(map[string]any)
	if !ok {
		return nil, "", "", false
	}
	itemID, _ := item["id"].(string)
	itemType, _ := item["type"].(string)
	if strings.TrimSpace(itemID) == "" || strings.TrimSpace(itemType) == "" {
		return nil, "", "", false
	}
	return item, itemID, itemType, true
}

func responsesSSEActiveFromItem(item map[string]any, outputIndex any) *responsesSSEActiveItem {
	active := &responsesSSEActiveItem{
		id:          item["id"].(string),
		itemType:    item["type"].(string),
		outputIndex: outputIndex,
		summaries:   make(map[int]string),
	}
	if summaries, ok := item["summary"].([]any); ok {
		for index, rawSummary := range summaries {
			summary, _ := rawSummary.(map[string]any)
			text, _ := summary["text"].(string)
			active.summaries[index] = text
		}
	}
	if content, ok := item["content"].([]any); ok {
		for _, rawPart := range content {
			part, _ := rawPart.(map[string]any)
			if part["type"] == "output_text" {
				text, _ := part["text"].(string)
				active.messageText += text
			}
		}
	}
	return active
}

func responsesSSEEmptyItem(itemType, itemID string) map[string]any {
	item := map[string]any{"id": itemID, "type": itemType}
	switch itemType {
	case "reasoning":
		item["summary"] = []any{}
	case "message":
		item["role"] = "assistant"
		item["status"] = "in_progress"
		item["content"] = []any{}
	}
	return item
}

func responsesSSEAddedEvent(active *responsesSSEActiveItem) map[string]any {
	return map[string]any{
		"type":         "response.output_item.added",
		"output_index": active.outputIndex,
		"item":         responsesSSEEmptyItem(active.itemType, active.id),
	}
}

func responsesSSESemanticItemType(itemType string) bool {
	return itemType == "reasoning" || itemType == "message"
}

func responsesSSEOutputIndexKey(outputIndex any) string {
	if outputIndex == nil {
		return ""
	}
	return fmt.Sprintf("%T:%v", outputIndex, outputIndex)
}

func responsesSSEEventName(frame []byte) string {
	for _, line := range bytes.Split(frame, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("event:")) {
			return strings.TrimSpace(string(line[len("event:"):]))
		}
	}
	return ""
}

func responsesSSEChunkStartsField(chunk []byte) bool {
	firstLine, _, _ := bytes.Cut(chunk, []byte("\n"))
	firstLine = bytes.TrimSpace(firstLine)
	return bytes.HasPrefix(firstLine, []byte("data:")) ||
		bytes.HasPrefix(firstLine, []byte("event:")) ||
		bytes.HasPrefix(firstLine, []byte("id:")) ||
		bytes.HasPrefix(firstLine, []byte("retry:")) ||
		bytes.HasPrefix(firstLine, []byte(":"))
}

func responsesSSEForwardedFields(frame []byte) [][]byte {
	var fields [][]byte
	for _, line := range bytes.Split(frame, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("id:")) || bytes.HasPrefix(line, []byte("retry:")) {
			fields = append(fields, bytes.Clone(line))
		}
	}
	return fields
}

func (s *responsesSSELifecycleState) closedContains(itemID string) bool {
	if s.closed == nil {
		return false
	}
	_, ok := s.closed[itemID]
	return ok
}

func (s *responsesSSELifecycleState) markClosed(itemID string) {
	if s.closed == nil {
		s.closed = make(map[string]struct{})
	}
	s.closed[itemID] = struct{}{}
}

func responsesSSEInt(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	default:
		return 0
	}
}
