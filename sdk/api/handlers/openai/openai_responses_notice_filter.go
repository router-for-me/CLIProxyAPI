package openai

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type responsesNoticeFilter struct {
	suppressedItemIDs map[string]struct{}
}

var responsesNoticeFilterMarkers = [][]byte{
	[]byte("weekly"),
	[]byte("Weekly"),
	[]byte("/status"),
	[]byte("/STATUS"),
}

func newResponsesNoticeFilter() *responsesNoticeFilter {
	return &responsesNoticeFilter{
		suppressedItemIDs: make(map[string]struct{}),
	}
}

func (f *responsesNoticeFilter) FilterPayload(payload []byte) []byte {
	if len(payload) == 0 || !json.Valid(payload) {
		return payload
	}
	if f == nil {
		return payload
	}

	itemID := strings.TrimSpace(gjson.GetBytes(payload, "item_id").String())
	if itemID != "" {
		if _, ok := f.suppressedItemIDs[itemID]; ok {
			return nil
		}
	}

	itemResult := gjson.GetBytes(payload, "item")
	if itemResult.Exists() && itemResult.Type == gjson.JSON {
		payloadItemID := strings.TrimSpace(itemResult.Get("id").String())
		if payloadItemID != "" {
			if _, ok := f.suppressedItemIDs[payloadItemID]; ok {
				return nil
			}
		}
	}

	switch strings.TrimSpace(gjson.GetBytes(payload, "type").String()) {
	case "response.output_text.delta":
		if responsesUsageWarningText(gjson.GetBytes(payload, "delta").String()) {
			f.markSuppressedItem(itemID)
			return nil
		}
	case "response.output_text.done":
		if responsesUsageWarningText(gjson.GetBytes(payload, "text").String()) {
			f.markSuppressedItem(itemID)
			return nil
		}
	case "response.content_part.added", "response.content_part.done":
		if responsesUsageWarningPart(gjson.GetBytes(payload, "part")) {
			f.markSuppressedItem(itemID)
			return nil
		}
	case "response.output_item.added", "response.output_item.done":
		if itemResult.Exists() && responsesUsageWarningItem(itemResult) {
			f.markSuppressedItem(strings.TrimSpace(itemResult.Get("id").String()))
			return nil
		}
	case "response.completed":
		return f.filterOutputPayload(payload, "response.output")
	}

	return payload
}

func (f *responsesNoticeFilter) FilterResponseObject(payload []byte) []byte {
	if len(payload) == 0 || !json.Valid(payload) {
		return payload
	}
	if f == nil {
		return payload
	}
	return f.filterOutputPayload(payload, "output")
}

func (f *responsesNoticeFilter) FilterSSEFrame(frame []byte) []byte {
	if len(frame) == 0 {
		return frame
	}
	if f == nil {
		return frame
	}

	trimmed := bytes.TrimRight(frame, "\r\n")
	if len(trimmed) == 0 {
		return nil
	}

	lines := bytes.Split(trimmed, []byte("\n"))
	out := make([][]byte, 0, len(lines))
	dataLines := 0
	for i := range lines {
		line := bytes.TrimRight(lines[i], "\r")
		trimmedLine := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmedLine, []byte("data:")) {
			out = append(out, line)
			continue
		}

		data := bytes.TrimSpace(trimmedLine[len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte(wsDoneMarker)) || !json.Valid(data) {
			out = append(out, line)
			dataLines++
			continue
		}

		filtered := f.FilterPayload(data)
		if len(filtered) == 0 {
			continue
		}
		out = append(out, append([]byte("data: "), filtered...))
		dataLines++
	}
	if dataLines == 0 {
		return nil
	}
	return append(bytes.Join(out, []byte("\n")), []byte("\n\n")...)
}

func (f *responsesNoticeFilter) CanBypassSSEChunk(chunk []byte) bool {
	if f == nil {
		return true
	}
	if len(f.suppressedItemIDs) != 0 {
		return false
	}
	return !responsesNoticeMayNeedFiltering(chunk)
}

func (f *responsesNoticeFilter) markSuppressedItem(itemID string) {
	if f == nil {
		return
	}
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return
	}
	f.suppressedItemIDs[itemID] = struct{}{}
}

func (f *responsesNoticeFilter) filterOutputPayload(payload []byte, path string) []byte {
	output := gjson.GetBytes(payload, path)
	if !output.Exists() || !output.IsArray() {
		return payload
	}

	filteredItems := make([]json.RawMessage, 0)
	output.ForEach(func(_, item gjson.Result) bool {
		itemID := strings.TrimSpace(item.Get("id").String())
		if itemID != "" {
			if _, ok := f.suppressedItemIDs[itemID]; ok {
				return true
			}
		}
		if responsesUsageWarningItem(item) {
			f.markSuppressedItem(itemID)
			return true
		}
		filteredItems = append(filteredItems, json.RawMessage(item.Raw))
		return true
	})

	filteredJSON, err := json.Marshal(filteredItems)
	if err != nil {
		return payload
	}
	updated, err := sjson.SetRawBytes(payload, path, filteredJSON)
	if err != nil {
		return payload
	}
	return updated
}

func responsesUsageWarningItem(item gjson.Result) bool {
	if !item.Exists() || item.Type != gjson.JSON {
		return false
	}
	if responsesUsageWarningText(item.Get("text").String()) {
		return true
	}
	content := item.Get("content")
	if !content.Exists() || !content.IsArray() {
		return false
	}
	warning := false
	content.ForEach(func(_, part gjson.Result) bool {
		if responsesUsageWarningPart(part) {
			warning = true
			return false
		}
		return true
	})
	return warning
}

func responsesUsageWarningPart(part gjson.Result) bool {
	if !part.Exists() || part.Type != gjson.JSON {
		return false
	}
	return responsesUsageWarningText(part.Get("text").String())
}

func responsesUsageWarningText(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return false
	}
	if !strings.Contains(normalized, "weekly limit left") {
		return false
	}
	if !strings.Contains(normalized, "run /status for a breakdown") {
		return false
	}
	return true
}

func responsesNoticeMayNeedFiltering(chunk []byte) bool {
	if len(chunk) == 0 {
		return false
	}
	for _, marker := range responsesNoticeFilterMarkers {
		if bytes.Contains(chunk, marker) {
			return true
		}
	}
	return false
}
