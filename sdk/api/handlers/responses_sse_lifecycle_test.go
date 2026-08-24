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
