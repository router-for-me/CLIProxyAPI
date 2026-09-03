package handlers

import (
	"bytes"
	"encoding/json"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestResponsesSSELifecycleEnabledOnlyForCompatibleSelectedModel(t *testing.T) {
	if responsesSSELifecycleEnabled(nil) {
		t.Fatal("lifecycle normalizer enabled without selected model metadata")
	}
	metadata := map[string]any{coreexecutor.SelectedModelCompatibilityMetadataKey: true}
	if !responsesSSELifecycleEnabled(metadata) {
		t.Fatal("lifecycle normalizer disabled for compatible selected model")
	}
}

func TestResponsesSSELifecycleSynthesizesReasoningToMessageTransition(t *testing.T) {
	state := &responsesSSELifecycleState{}
	chunks := []string{
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"r1","output_index":0,"summary_index":0,"delta":"plan"}` + "\n\n",
		`data: {"type":"response.output_text.delta","item_id":"m1","output_index":1,"delta":"answer"}` + "\n\n",
		`data: {"type":"response.completed","response":{"id":"resp_1"}}` + "\n\n",
	}

	var output []byte
	for _, chunk := range chunks {
		normalized, err := state.AddChunk([]byte(chunk))
		if err != nil {
			t.Fatalf("AddChunk() error = %v", err)
		}
		output = append(output, normalized...)
	}
	if err := state.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	events := responsesSSETestEvents(t, output)
	wantTypes := []string{
		"response.output_item.added",
		"response.reasoning_summary_text.delta",
		"response.output_item.done",
		"response.output_item.added",
		"response.output_text.delta",
		"response.output_item.done",
		"response.completed",
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d; output=%s", len(events), len(wantTypes), output)
	}
	for index, wantType := range wantTypes {
		if gotType, _ := events[index]["type"].(string); gotType != wantType {
			t.Fatalf("event[%d].type = %q, want %q; output=%s", index, gotType, wantType, output)
		}
	}
	reasoningDone := events[2]["item"].(map[string]any)
	summary := reasoningDone["summary"].([]any)[0].(map[string]any)
	if summary["text"] != "plan" {
		t.Fatalf("reasoning summary = %#v, want plan", summary["text"])
	}
	messageDone := events[5]["item"].(map[string]any)
	content := messageDone["content"].([]any)[0].(map[string]any)
	if content["text"] != "answer" {
		t.Fatalf("message text = %#v, want answer", content["text"])
	}
}

func TestResponsesSSELifecycleSynthesizesAddedBeforeOrphanDone(t *testing.T) {
	state := &responsesSSELifecycleState{}
	output, err := state.AddChunk([]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}` + "\n\n"))
	if err != nil {
		t.Fatalf("AddChunk() error = %v", err)
	}
	events := responsesSSETestEvents(t, output)
	if len(events) != 2 || events[0]["type"] != "response.output_item.added" || events[1]["type"] != "response.output_item.done" {
		t.Fatalf("events = %#v, want synthesized added then original done", events)
	}
}

func TestResponsesSSELifecycleRejectsOverlappingExplicitItems(t *testing.T) {
	state := &responsesSSELifecycleState{}
	first := []byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"m1","type":"message","role":"assistant","content":[]}}` + "\n\n")
	if _, err := state.AddChunk(first); err != nil {
		t.Fatalf("first AddChunk() error = %v", err)
	}
	second := []byte(`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"r1","type":"reasoning","summary":[]}}` + "\n\n")
	if _, err := state.AddChunk(second); err == nil {
		t.Fatal("overlapping explicit output items were accepted")
	}
}

func TestResponsesSSELifecycleAllowsParallelExplicitToolItems(t *testing.T) {
	state := &responsesSSELifecycleState{}
	chunks := []string{
		`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc1","type":"function_call","call_id":"call1","name":"read","arguments":""}}` + "\n\n",
		`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":1,"item":{"id":"fc2","type":"function_call","call_id":"call2","name":"write","arguments":""}}` + "\n\n",
		`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fc2","type":"function_call","call_id":"call2","name":"write","arguments":"{}"}}` + "\n\n",
		`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc1","type":"function_call","call_id":"call1","name":"read","arguments":"{}"}}` + "\n\n",
		`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"id":"resp1","status":"completed"}}` + "\n\n",
	}

	var output []byte
	for _, chunk := range chunks {
		normalized, err := state.AddChunk([]byte(chunk))
		if err != nil {
			t.Fatalf("AddChunk() error = %v", err)
		}
		output = append(output, normalized...)
	}
	if err := state.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	events := responsesSSETestEvents(t, output)
	if len(events) != 5 {
		t.Fatalf("event count = %d, want 5; output=%s", len(events), output)
	}
	for _, itemID := range []string{"fc1", "fc2"} {
		if !state.closedContains(itemID) {
			t.Fatalf("tool item %s was not closed", itemID)
		}
	}
}

func TestResponsesSSELifecyclePreservesAndGeneratesEventFields(t *testing.T) {
	state := &responsesSSELifecycleState{}
	chunks := []string{
		`id: provider-event-42` + "\n" + `retry: 1500` + "\n" + `event: provider.output.delta` + "\n" + `data: {"type":"response.output_text.delta","item_id":"m1","output_index":0,"delta":"answer"}` + "\n\n",
		`data: {"type":"response.completed","response":{"id":"resp1","status":"completed"}}` + "\n\n",
	}

	var output []byte
	for _, chunk := range chunks {
		normalized, err := state.AddChunk([]byte(chunk))
		if err != nil {
			t.Fatalf("AddChunk() error = %v", err)
		}
		output = append(output, normalized...)
	}
	if err := state.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	want := []string{
		"response.output_item.added",
		"provider.output.delta",
		"response.output_item.done",
		"response.completed",
	}
	if got := responsesSSETestEventNames(output); !equalStrings(got, want) {
		t.Fatalf("event fields = %#v, want %#v; output=%s", got, want, output)
	}
	frames := bytes.Split(output, []byte("\n\n"))
	if bytes.Contains(frames[0], []byte("id:")) || bytes.Contains(frames[0], []byte("retry:")) {
		t.Fatalf("synthesized event copied reconnection fields: %s", frames[0])
	}
	if !bytes.Contains(frames[1], []byte("id: provider-event-42")) || !bytes.Contains(frames[1], []byte("retry: 1500")) {
		t.Fatalf("forwarded event lost reconnection fields: %s", frames[1])
	}
}

func TestResponsesSSELifecyclePreservesSplitSSEFields(t *testing.T) {
	state := &responsesSSELifecycleState{}
	chunks := []string{
		"event: response.",
		"completed",
		"id: provider-event-",
		"42",
		"retry: 15",
		"00",
		`data: {"type":"response.completed","response":{"id":"resp1","status":"completed"}}` + "\n\n",
	}

	var output []byte
	for _, chunk := range chunks {
		normalized, err := state.AddChunk([]byte(chunk))
		if err != nil {
			t.Fatalf("AddChunk() error = %v", err)
		}
		output = append(output, normalized...)
	}
	if err := state.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if got := responsesSSETestEventNames(output); !equalStrings(got, []string{"response.completed"}) {
		t.Fatalf("event fields = %#v, want response.completed; output=%s", got, output)
	}
	if !bytes.Contains(output, []byte("id: provider-event-42")) || !bytes.Contains(output, []byte("retry: 1500")) {
		t.Fatalf("split SSE fields were not preserved: %s", output)
	}
}

func TestResponsesSSELifecycleWaitsForFrameDelimiterBeforeForwarding(t *testing.T) {
	state := &responsesSSELifecycleState{}
	first := []byte(`data: {"type":"response.completed","response":{"id":"resp1","status":"completed"}}` + "\n")
	output, err := state.AddChunk(first)
	if err != nil {
		t.Fatalf("first AddChunk() error = %v", err)
	}
	if len(output) != 0 {
		t.Fatalf("data was forwarded before the frame delimiter: %s", output)
	}

	output, err = state.AddChunk([]byte("id: provider-event-42\nretry: 1500\n\n"))
	if err != nil {
		t.Fatalf("second AddChunk() error = %v", err)
	}
	if !bytes.Contains(output, []byte("id: provider-event-42")) || !bytes.Contains(output, []byte("retry: 1500")) {
		t.Fatalf("trailing fields were detached from their data frame: %s", output)
	}
	if err := state.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
}

func TestResponsesSSELifecycleSynthesizesMessageBeforeContentPart(t *testing.T) {
	state := &responsesSSELifecycleState{}
	chunks := []string{
		`data: {"type":"response.content_part.added","item_id":"m1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}` + "\n\n",
		`data: {"type":"response.output_text.delta","item_id":"m1","output_index":0,"content_index":0,"delta":"answer"}` + "\n\n",
		`data: {"type":"response.completed","response":{"id":"resp1","status":"completed"}}` + "\n\n",
	}

	var output []byte
	for _, chunk := range chunks {
		normalized, err := state.AddChunk([]byte(chunk))
		if err != nil {
			t.Fatalf("AddChunk() error = %v", err)
		}
		output = append(output, normalized...)
	}
	if err := state.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	events := responsesSSETestEvents(t, output)
	wantTypes := []string{
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_item.done",
		"response.completed",
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d; output=%s", len(events), len(wantTypes), output)
	}
	for index, wantType := range wantTypes {
		if gotType, _ := events[index]["type"].(string); gotType != wantType {
			t.Fatalf("event[%d].type = %q, want %q; output=%s", index, gotType, wantType, output)
		}
	}
}

func TestResponsesSSELifecyclePreservesRefusalContent(t *testing.T) {
	state := &responsesSSELifecycleState{}
	chunks := []string{
		`data: {"type":"response.content_part.added","item_id":"m1","output_index":0,"content_index":0,"part":{"type":"refusal","refusal":""}}` + "\n\n",
		`data: {"type":"response.refusal.delta","item_id":"m1","output_index":0,"content_index":0,"delta":"cannot "}` + "\n\n",
		`data: {"type":"response.refusal.done","item_id":"m1","output_index":0,"content_index":0,"refusal":"cannot comply"}` + "\n\n",
		`data: {"type":"response.completed","response":{"id":"resp1","status":"completed"}}` + "\n\n",
	}

	var output []byte
	for _, chunk := range chunks {
		normalized, err := state.AddChunk([]byte(chunk))
		if err != nil {
			t.Fatalf("AddChunk() error = %v", err)
		}
		output = append(output, normalized...)
	}
	if err := state.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	events := responsesSSETestEvents(t, output)
	done := events[len(events)-2]
	item := done["item"].(map[string]any)
	content := item["content"].([]any)[0].(map[string]any)
	if content["type"] != "refusal" || content["refusal"] != "cannot comply" {
		t.Fatalf("refusal content = %#v, want preserved refusal", content)
	}
}

func TestResponsesSSELifecycleRenumbersSynthesizedEvents(t *testing.T) {
	state := &responsesSSELifecycleState{}
	chunks := []string{
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp1","status":"in_progress"}}` + "\n\n",
		`data: {"type":"response.output_text.delta","sequence_number":1,"item_id":"m1","output_index":0,"delta":"answer"}` + "\n\n",
		`data: {"type":"response.completed","sequence_number":2,"response":{"id":"resp1","status":"completed"}}` + "\n\n",
	}

	var output []byte
	for _, chunk := range chunks {
		normalized, err := state.AddChunk([]byte(chunk))
		if err != nil {
			t.Fatalf("AddChunk() error = %v", err)
		}
		output = append(output, normalized...)
	}
	if err := state.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	events := responsesSSETestEvents(t, output)
	for index, event := range events {
		if got := int(event["sequence_number"].(float64)); got != index {
			t.Fatalf("event[%d].sequence_number = %d, want %d; output=%s", index, got, index, output)
		}
	}
}

func TestResponsesSSELifecycleRecognizesAllTerminalEvents(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
	}{
		{name: "done", eventType: "response.done"},
		{name: "response error", eventType: "response.error"},
		{name: "top-level error", eventType: "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &responsesSSELifecycleState{}
			if _, err := state.AddChunk([]byte(`data: {"type":"response.output_text.delta","item_id":"m1","output_index":0,"delta":"partial"}` + "\n\n")); err != nil {
				t.Fatalf("delta AddChunk() error = %v", err)
			}
			if _, err := state.AddChunk([]byte(`data: {"type":"` + tt.eventType + `"}` + "\n\n")); err != nil {
				t.Fatalf("terminal AddChunk() error = %v", err)
			}
			if err := state.Finish(); err != nil {
				t.Fatalf("Finish() error = %v", err)
			}
		})
	}
}

func TestResponsesSSELifecycleRejectsCleanCloseWithoutTerminalEvent(t *testing.T) {
	state := &responsesSSELifecycleState{}
	chunk := []byte(`data: {"type":"response.output_text.delta","item_id":"m1","output_index":0,"delta":"partial"}` + "\n\n")
	if _, err := state.AddChunk(chunk); err != nil {
		t.Fatalf("AddChunk() error = %v", err)
	}
	if err := state.Finish(); err == nil {
		t.Fatal("Finish() accepted a stream with an active item and no terminal response event")
	}
}

func TestResponsesSSELifecycleRejectsSyntheticCompletionForToolItem(t *testing.T) {
	state := &responsesSSELifecycleState{}
	added := []byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc1","type":"function_call","call_id":"call1","name":"read","arguments":""}}` + "\n\n")
	if _, err := state.AddChunk(added); err != nil {
		t.Fatalf("added AddChunk() error = %v", err)
	}
	completed := []byte(`data: {"type":"response.completed","response":{"id":"resp1","status":"completed"}}` + "\n\n")
	if _, err := state.AddChunk(completed); err == nil {
		t.Fatal("normalizer synthesized an incomplete function_call item")
	}
}

func TestResponsesSSELifecycleSuppressesLateDuplicateDone(t *testing.T) {
	state := &responsesSSELifecycleState{}
	frame := []byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"m1","type":"message","role":"assistant","content":[]}}` + "\n\n")
	first, errFirst := state.AddChunk(frame)
	if errFirst != nil {
		t.Fatalf("first AddChunk() error = %v", errFirst)
	}
	second, errSecond := state.AddChunk(frame)
	if errSecond != nil {
		t.Fatalf("second AddChunk() error = %v", errSecond)
	}
	if len(responsesSSETestEvents(t, first)) != 2 || len(bytes.TrimSpace(second)) != 0 {
		t.Fatalf("duplicate suppression failed: first=%s second=%s", first, second)
	}
}

func responsesSSETestEvents(t *testing.T, stream []byte) []map[string]any {
	t.Helper()
	frames := bytes.Split(stream, []byte("\n\n"))
	events := make([]map[string]any, 0, len(frames))
	for _, frame := range frames {
		payload, found := sseJSONValidationDataPayload(frame)
		if !found || len(bytes.TrimSpace(payload)) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatalf("decode event: %v; frame=%s", err, frame)
		}
		events = append(events, event)
	}
	return events
}

func responsesSSETestEventNames(stream []byte) []string {
	frames := bytes.Split(stream, []byte("\n\n"))
	names := make([]string, 0, len(frames))
	for _, frame := range frames {
		if name := responsesSSEEventName(frame); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
