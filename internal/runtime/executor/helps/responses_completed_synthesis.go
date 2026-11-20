package helps

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// ResponsesStreamOutputTracker tracks streamed Responses output items and synthesizes
// missing message streaming events before a terminal response.completed or
// response.incomplete event when upstream omitted item-level SSE frames.
type ResponsesStreamOutputTracker struct {
	addedIDs        map[string]bool
	seenIDs         map[string]bool
	addedIndices    map[int64]bool
	seenIndices     map[int64]bool
	sawUnkeyedDelta bool
	maxSeq          int64
	hasSeq          bool
}

func (t *ResponsesStreamOutputTracker) nextSeq() int64 {
	if !t.hasSeq {
		t.hasSeq = true
		t.maxSeq = 0
		return 0
	}
	t.maxSeq++
	return t.maxSeq
}

func (t *ResponsesStreamOutputTracker) newEventDoc(eventType string) *ojson.Doc {
	doc := ojson.NewDocString(`{"type":""}`)
	_ = doc.Set("type", eventType)
	_ = doc.Set("sequence_number", t.nextSeq())
	return doc
}

func markTrackerKey(idMap *map[string]bool, idxMap *map[int64]bool, itemID string, hasIdx bool, idx int64) bool {
	if itemID != "" {
		if *idMap == nil {
			*idMap = make(map[string]bool)
		}
		(*idMap)[strings.Clone(itemID)] = true
		return true
	}
	if hasIdx {
		if *idxMap == nil {
			*idxMap = make(map[int64]bool)
		}
		(*idxMap)[idx] = true
		return true
	}
	return false
}

// Observe records streamed events so terminal synthesis skips items already streamed.
func (t *ResponsesStreamOutputTracker) Observe(eventType string, eventData []byte) {
	if t == nil || len(eventData) == 0 {
		return
	}
	root := util.ParseGJSONBytesNoCopy(eventData)
	if seq := root.Get("sequence_number"); seq.Exists() && seq.Type == ojson.Number {
		if v := seq.Int(); !t.hasSeq || v > t.maxSeq {
			t.maxSeq = v
			t.hasSeq = true
		}
	}
	outIdx := root.Get("output_index")
	hasIdx := outIdx.Exists() && outIdx.Type == ojson.Number
	idx := outIdx.Int()

	switch eventType {
	case "response.output_item.added":
		if itemType := root.Get("item.type").String(); itemType != "" && itemType != "message" {
			return
		}
		itemID := root.Get("item.id").String()
		markTrackerKey(&t.addedIDs, &t.addedIndices, itemID, hasIdx, idx)
	case "response.output_item.done":
		if itemType := root.Get("item.type").String(); itemType != "" && itemType != "message" {
			return
		}
		itemID := root.Get("item.id").String()
		markTrackerKey(&t.seenIDs, &t.seenIndices, itemID, hasIdx, idx)
	case "response.output_text.delta",
		"response.refusal.delta":
		if root.Get("delta").String() == "" {
			return
		}
		itemID := root.Get("item_id").String()
		if !markTrackerKey(&t.seenIDs, &t.seenIndices, itemID, hasIdx, idx) {
			t.sawUnkeyedDelta = true
		}
	}
}

// SynthesizeMissingOutputEvents emits synthetic streaming events for unstreamed
// message items present in a terminal response.completed or response.incomplete event.
func (t *ResponsesStreamOutputTracker) SynthesizeMissingOutputEvents(eventData []byte) ([][]byte, []byte) {
	if t == nil || len(eventData) == 0 {
		return nil, eventData
	}
	root := util.ParseGJSONBytesNoCopy(eventData)
	output := root.Get("response.output")
	if !output.Exists() || !output.IsArray() {
		return nil, eventData
	}

	var synthesized [][]byte
	for i, item := range output.Array() {
		if item.Get("type").String() != "message" {
			continue
		}
		idx := int64(i)
		itemID := item.Get("id").String()
		if (itemID != "" && t.seenIDs[itemID]) || t.seenIndices[idx] || t.sawUnkeyedDelta {
			continue
		}
		alreadyAdded := (itemID != "" && t.addedIDs[itemID]) || t.addedIndices[idx]
		if !alreadyAdded {
			addedItem := ojson.NewDocString(item.Raw)
			_ = addedItem.Set("status", "in_progress")
			_ = addedItem.SetRawBytes("content", []byte("[]"))
			doc := t.newEventDoc("response.output_item.added")
			_ = doc.Set("output_index", idx)
			_ = doc.SetRawBytes("item", addedItem.Bytes())
			synthesized = append(synthesized, doc.Bytes())
		}

		for ci, part := range item.Get("content").Array() {
			partType := part.Get("type").String()
			if partType != "output_text" && partType != "refusal" {
				continue
			}
			partAdded := t.newEventDoc("response.content_part.added")
			if itemID != "" {
				_ = partAdded.Set("item_id", itemID)
			}
			_ = partAdded.Set("output_index", idx)
			_ = partAdded.Set("content_index", ci)
			if partType == "refusal" {
				_ = partAdded.SetRawBytes("part", []byte(`{"type":"refusal","refusal":""}`))
			} else {
				_ = partAdded.SetRawBytes("part", []byte(`{"type":"output_text","text":"","annotations":[]}`))
			}
			synthesized = append(synthesized, partAdded.Bytes())

			var value string
			if partType == "refusal" {
				value = part.Get("refusal").String()
				if value == "" {
					value = part.Get("text").String()
				}
			} else {
				value = part.Get("text").String()
			}

			deltaType, doneType, doneField := "response.output_text.delta", "response.output_text.done", "text"
			if partType == "refusal" {
				deltaType, doneType, doneField = "response.refusal.delta", "response.refusal.done", "refusal"
			}
			if value != "" {
				deltaDoc := t.newEventDoc(deltaType)
				if itemID != "" {
					_ = deltaDoc.Set("item_id", itemID)
				}
				_ = deltaDoc.Set("output_index", idx)
				_ = deltaDoc.Set("content_index", ci)
				_ = deltaDoc.Set("delta", value)
				synthesized = append(synthesized, deltaDoc.Bytes())
			}

			doneDoc := t.newEventDoc(doneType)
			if itemID != "" {
				_ = doneDoc.Set("item_id", itemID)
			}
			_ = doneDoc.Set("output_index", idx)
			_ = doneDoc.Set("content_index", ci)
			_ = doneDoc.Set(doneField, value)
			synthesized = append(synthesized, doneDoc.Bytes())

			partDone := t.newEventDoc("response.content_part.done")
			if itemID != "" {
				_ = partDone.Set("item_id", itemID)
			}
			_ = partDone.Set("output_index", idx)
			_ = partDone.Set("content_index", ci)
			_ = partDone.SetRaw("part", part.Raw)
			synthesized = append(synthesized, partDone.Bytes())
		}

		itemDone := t.newEventDoc("response.output_item.done")
		_ = itemDone.Set("output_index", idx)
		_ = itemDone.SetRaw("item", item.Raw)
		synthesized = append(synthesized, itemDone.Bytes())
	}

	if len(synthesized) > 0 {
		if seq := root.Get("sequence_number"); seq.Exists() && seq.Type == ojson.Number && seq.Int() <= t.maxSeq {
			doc := ojson.NewDoc(eventData)
			_ = doc.Set("sequence_number", t.nextSeq())
			eventData = doc.Bytes()
		}
	}
	return synthesized, eventData
}
