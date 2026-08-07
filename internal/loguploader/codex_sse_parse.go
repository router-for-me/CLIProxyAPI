package loguploader

import (
	"encoding/json"
	"sort"
	"strings"
)

// sseContentKey is the map key for delta text buffers, combining an output
// item index with a content or summary sub-index.
type sseContentKey struct {
	index    int
	subIndex int
}

// sseAssembler holds the incremental state for reconstructing output items
// from an SSE event stream.  Port of the Python parse_sse() closure.
type sseAssembler struct {
	outputItems      map[int]map[string]any
	itemIndexes      map[string]int
	completedIndexes map[int]bool

	outputTextBuf    map[sseContentKey][]string
	reasoningTextBuf map[sseContentKey][]string
	customInputBuf   map[int][]string
	funcArgBuf       map[int][]string

	completedResponse map[string]any
	terminalResponse  map[string]any
	terminalType      string
	latestResponse    map[string]any
	lastJSON          any
	responseMetadata  map[string]any

	eventCount       int
	doneMarkerCount  int
	jsonDecodeErrors int
	typeCounts       map[string]int
}

// sseStats holds diagnostics returned by parseSSEPayload.
type sseStats struct {
	EventCount        int
	DoneMarkerCount   int
	JSONDecodeErrors  int
	TerminalType      string
	Reconstructed     int
	FinalOutputCount  int
	UsedReconstructed bool
	EventTypeCounts   map[string]int
	RawSSE            string
}

func newSSEAssembler() *sseAssembler {
	return &sseAssembler{
		outputItems:      make(map[int]map[string]any),
		itemIndexes:      make(map[string]int),
		completedIndexes: make(map[int]bool),
		outputTextBuf:    make(map[sseContentKey][]string),
		reasoningTextBuf: make(map[sseContentKey][]string),
		customInputBuf:   make(map[int][]string),
		funcArgBuf:       make(map[int][]string),
		responseMetadata: make(map[string]any),
		typeCounts:       make(map[string]int),
	}
}

// eventOutputIndex resolves the output_index for an SSE event value.
func (a *sseAssembler) eventOutputIndex(v map[string]any) int {
	if rawIdx, ok := v["output_index"]; ok {
		switch idx := rawIdx.(type) {
		case float64:
			return int(idx)
		case int:
			return idx
		}
	}
	if itemID, ok := v["item_id"].(string); ok {
		if idx, found := a.itemIndexes[itemID]; found {
			return idx
		}
	}
	if item, ok := v["item"].(map[string]any); ok {
		if itemID, ok := item["id"].(string); ok {
			if idx, found := a.itemIndexes[itemID]; found {
				return idx
			}
		}
	}
	maxIdx := -1
	for k := range a.outputItems {
		if k > maxIdx {
			maxIdx = k
		}
	}
	return maxIdx + 1
}

func (a *sseAssembler) ensureItem(v map[string]any) (int, map[string]any) {
	index := a.eventOutputIndex(v)
	item, ok := a.outputItems[index]
	if !ok {
		item = make(map[string]any)
		a.outputItems[index] = item
	}
	if itemID, ok := v["item_id"].(string); ok {
		if _, has := item["id"]; !has {
			item["id"] = itemID
		}
		a.itemIndexes[itemID] = index
	}
	return index, item
}

func (a *sseAssembler) rememberItem(index int, item map[string]any) {
	a.outputItems[index] = item
	if itemID, ok := item["id"].(string); ok {
		a.itemIndexes[itemID] = index
	}
}

// ensureIndexedPart ensures item[field] is a list with at least index+1
// entries, each being a map. Returns the part map at the given index.
func ensureIndexedPart(item map[string]any, field string, index int, defaultType string) map[string]any {
	parts, _ := item[field].([]any)
	for len(parts) <= index {
		parts = append(parts, map[string]any{})
	}
	item[field] = parts
	part, ok := parts[index].(map[string]any)
	if !ok {
		part = map[string]any{}
		parts[index] = part
	}
	if _, has := part["type"]; !has {
		part["type"] = defaultType
	}
	return part
}

func seedBuffer(buf []string, value any) []string {
	if len(buf) == 0 {
		if s, ok := value.(string); ok && s != "" {
			return append(buf, s)
		}
	}
	return buf
}

func (a *sseAssembler) handleOutputEvent(v map[string]any, eventType string) {
	switch eventType {
	case "response.output_item.added":
		if item, ok := v["item"].(map[string]any); ok {
			a.rememberItem(a.eventOutputIndex(v), item)
		}
	case "response.output_item.done":
		if item, ok := v["item"].(map[string]any); ok {
			idx := a.eventOutputIndex(v)
			a.rememberItem(idx, item)
			a.completedIndexes[idx] = true
		}

	case "response.content_part.added", "response.content_part.done":
		idx, item := a.ensureItem(v)
		ci := intFieldDefault(v, "content_index", 0)
		if part, ok := v["part"].(map[string]any); ok {
			target := ensureIndexedPart(item, "content", ci, "output_text")
			for k := range target {
				delete(target, k)
			}
			for k, val := range part {
				target[k] = val
			}
			if strings.HasSuffix(eventType, ".done") {
				delete(a.outputTextBuf, sseContentKey{idx, ci})
			}
		}

	case "response.output_text.delta", "response.output_text.done":
		idx, item := a.ensureItem(v)
		ci := intFieldDefault(v, "content_index", 0)
		part := ensureIndexedPart(item, "content", ci, "output_text")
		key := sseContentKey{idx, ci}
		if strings.HasSuffix(eventType, ".delta") {
			a.outputTextBuf[key] = seedBuffer(a.outputTextBuf[key], part["text"])
			if delta, ok := v["delta"].(string); ok {
				a.outputTextBuf[key] = append(a.outputTextBuf[key], delta)
			}
		} else {
			if text, ok := v["text"].(string); ok {
				part["text"] = text
			}
			delete(a.outputTextBuf, key)
		}

	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		idx, item := a.ensureItem(v)
		si := intFieldDefault(v, "summary_index", 0)
		if part, ok := v["part"].(map[string]any); ok {
			target := ensureIndexedPart(item, "summary", si, "summary_text")
			for k := range target {
				delete(target, k)
			}
			for k, val := range part {
				target[k] = val
			}
			if strings.HasSuffix(eventType, ".done") {
				delete(a.reasoningTextBuf, sseContentKey{idx, si})
			}
		}

	case "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
		idx, item := a.ensureItem(v)
		si := intFieldDefault(v, "summary_index", 0)
		part := ensureIndexedPart(item, "summary", si, "summary_text")
		key := sseContentKey{idx, si}
		if strings.HasSuffix(eventType, ".delta") {
			a.reasoningTextBuf[key] = seedBuffer(a.reasoningTextBuf[key], part["text"])
			if delta, ok := v["delta"].(string); ok {
				a.reasoningTextBuf[key] = append(a.reasoningTextBuf[key], delta)
			}
		} else {
			if text, ok := v["text"].(string); ok {
				part["text"] = text
			}
			delete(a.reasoningTextBuf, key)
		}

	case "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
		idx, item := a.ensureItem(v)
		if _, has := item["type"]; !has {
			item["type"] = "custom_tool_call"
		}
		if strings.HasSuffix(eventType, ".delta") {
			a.customInputBuf[idx] = seedBuffer(a.customInputBuf[idx], item["input"])
			if delta, ok := v["delta"].(string); ok {
				a.customInputBuf[idx] = append(a.customInputBuf[idx], delta)
			}
		} else {
			if input, ok := v["input"].(string); ok {
				item["input"] = input
			}
			delete(a.customInputBuf, idx)
		}

	case "response.function_call_arguments.delta", "response.function_call_arguments.done":
		idx, item := a.ensureItem(v)
		if _, has := item["type"]; !has {
			item["type"] = "function_call"
		}
		if strings.HasSuffix(eventType, ".delta") {
			a.funcArgBuf[idx] = seedBuffer(a.funcArgBuf[idx], item["arguments"])
			if delta, ok := v["delta"].(string); ok {
				a.funcArgBuf[idx] = append(a.funcArgBuf[idx], delta)
			}
		} else {
			if args, ok := v["arguments"].(string); ok {
				item["arguments"] = args
			}
			delete(a.funcArgBuf, idx)
		}

	case "response.metadata":
		if meta, ok := v["metadata"].(map[string]any); ok {
			for k, val := range meta {
				a.responseMetadata[k] = val
			}
		}
	}
}

func (a *sseAssembler) applyUnfinishedBuffers() {
	for key, chunks := range a.outputTextBuf {
		if a.completedIndexes[key.index] {
			continue
		}
		item, ok := a.outputItems[key.index]
		if !ok {
			item = make(map[string]any)
			a.outputItems[key.index] = item
		}
		part := ensureIndexedPart(item, "content", key.subIndex, "output_text")
		part["text"] = strings.Join(chunks, "")
	}
	for key, chunks := range a.reasoningTextBuf {
		if a.completedIndexes[key.index] {
			continue
		}
		item, ok := a.outputItems[key.index]
		if !ok {
			item = make(map[string]any)
			a.outputItems[key.index] = item
		}
		part := ensureIndexedPart(item, "summary", key.subIndex, "summary_text")
		part["text"] = strings.Join(chunks, "")
	}
	for idx, chunks := range a.customInputBuf {
		if !a.completedIndexes[idx] {
			item, ok := a.outputItems[idx]
			if !ok {
				item = make(map[string]any)
				a.outputItems[idx] = item
			}
			item["input"] = strings.Join(chunks, "")
		}
	}
	for idx, chunks := range a.funcArgBuf {
		if !a.completedIndexes[idx] {
			item, ok := a.outputItems[idx]
			if !ok {
				item = make(map[string]any)
				a.outputItems[idx] = item
			}
			item["arguments"] = strings.Join(chunks, "")
		}
	}
}

// parseSSEPayload parses an SSE event stream text and reconstructs the
// response body.  Port of Python parse_sse().
func parseSSEPayload(payload string) (map[string]any, sseStats) {
	a := newSSEAssembler()
	stats := sseStats{RawSSE: payload, EventTypeCounts: make(map[string]int)}

	var eventName string
	var dataParts []string

	flush := func() {
		if eventName == "" && len(dataParts) == 0 {
			return
		}
		a.eventCount++
		dataText := strings.TrimSpace(strings.Join(dataParts, "\n"))
		if dataText == "[DONE]" {
			a.doneMarkerCount++
		} else if dataText != "" {
			var value any
			if err := json.Unmarshal([]byte(dataText), &value); err != nil {
				a.jsonDecodeErrors++
			} else {
				a.lastJSON = value
				vMap, isMap := value.(map[string]any)
				var eventType string
				if isMap {
					if t, ok := vMap["type"].(string); ok {
						eventType = t
					}
				}
				if eventType == "" {
					eventType = eventName
				}
				if eventType == "" {
					eventType = "<unknown>"
				}
				a.typeCounts[eventType]++
				if isMap {
					if resp, ok := vMap["response"].(map[string]any); ok {
						a.latestResponse = resp
					}
					a.handleOutputEvent(vMap, eventType)
					switch eventType {
					case "response.completed", "response.failed", "response.incomplete":
						a.terminalType = eventType
						if resp, ok := vMap["response"].(map[string]any); ok {
							a.terminalResponse = resp
						}
						if eventType == "response.completed" {
							if resp, ok := vMap["response"].(map[string]any); ok {
								a.completedResponse = resp
							}
						}
					}
				}
			}
		}
		eventName = ""
		dataParts = dataParts[:0]
	}

	for _, rawLine := range strings.Split(payload, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if line == "" {
			flush()
		} else if strings.HasPrefix(line, "event:") {
			if eventName != "" || len(dataParts) > 0 {
				flush()
			}
			eventName = strings.TrimSpace(line[6:])
		} else if strings.HasPrefix(line, "data:") {
			dataParts = append(dataParts, strings.TrimLeft(line[5:], " "))
		}
	}
	flush()

	a.applyUnfinishedBuffers()

	// Collect reconstructed output items sorted by index.
	keys := make([]int, 0, len(a.outputItems))
	for k := range a.outputItems {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	var reconstructedOutput []any
	for _, k := range keys {
		item := a.outputItems[k]
		if len(item) > 0 {
			reconstructedOutput = append(reconstructedOutput, item)
		}
	}

	// Choose response source.
	responseSource := a.completedResponse
	if responseSource == nil {
		responseSource = a.terminalResponse
	}
	if responseSource == nil {
		responseSource = a.latestResponse
	}

	usedReconstructed := false
	var body map[string]any
	if responseSource != nil {
		body = make(map[string]any, len(responseSource))
		for k, v := range responseSource {
			body[k] = v
		}
		existingOutput := body["output"]
		if existingOutput == nil {
			existingOutput = body["outputs"]
		}
		if len(reconstructedOutput) > 0 && existingOutput == nil {
			body["output"] = reconstructedOutput
			usedReconstructed = true
		}
		if len(a.responseMetadata) > 0 {
			existingMeta, _ := body["metadata"].(map[string]any)
			if existingMeta == nil {
				existingMeta = make(map[string]any)
			}
			merged := make(map[string]any, len(existingMeta)+len(a.responseMetadata))
			for k, v := range existingMeta {
				merged[k] = v
			}
			for k, v := range a.responseMetadata {
				merged[k] = v
			}
			body["metadata"] = merged
		}
	} else if len(reconstructedOutput) > 0 {
		body = map[string]any{"output": reconstructedOutput}
		usedReconstructed = true
	} else if a.lastJSON != nil {
		if m, ok := a.lastJSON.(map[string]any); ok {
			body = m
		}
	}

	finalOutputCount := 0
	if body != nil {
		if out, ok := body["output"].([]any); ok {
			finalOutputCount = len(out)
		} else if out, ok := body["outputs"].([]any); ok {
			finalOutputCount = len(out)
		}
	}

	stats.EventCount = a.eventCount
	stats.DoneMarkerCount = a.doneMarkerCount
	stats.JSONDecodeErrors = a.jsonDecodeErrors
	stats.TerminalType = a.terminalType
	stats.Reconstructed = len(reconstructedOutput)
	stats.FinalOutputCount = finalOutputCount
	stats.UsedReconstructed = usedReconstructed
	for k, v := range a.typeCounts {
		stats.EventTypeCounts[k] = v
	}
	return body, stats
}

// intFieldDefault extracts an integer field from a map with a default value.
func intFieldDefault(m map[string]any, key string, def int) int {
	v, ok := m[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return def
	}
}
