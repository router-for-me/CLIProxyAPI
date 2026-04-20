package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestCollectOutputItem_WithIndex(t *testing.T) {
	eventData := []byte(`{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"hi"}]},"output_index":0}`)
	itemsByIndex := make(map[int64][]byte)
	var fallback [][]byte

	collectOutputItem(eventData, itemsByIndex, &fallback)

	if len(itemsByIndex) != 1 {
		t.Fatalf("expected 1 indexed item, got %d", len(itemsByIndex))
	}
	if len(fallback) != 0 {
		t.Fatalf("expected 0 fallback items, got %d", len(fallback))
	}
	if gjson.GetBytes(itemsByIndex[0], "type").String() != "message" {
		t.Fatalf("expected message type, got %s", gjson.GetBytes(itemsByIndex[0], "type").String())
	}
}

func TestCollectOutputItem_WithoutIndex(t *testing.T) {
	eventData := []byte(`{"type":"response.output_item.done","item":{"type":"message"}}`)
	itemsByIndex := make(map[int64][]byte)
	var fallback [][]byte

	collectOutputItem(eventData, itemsByIndex, &fallback)

	if len(itemsByIndex) != 0 {
		t.Fatalf("expected 0 indexed items, got %d", len(itemsByIndex))
	}
	if len(fallback) != 1 {
		t.Fatalf("expected 1 fallback item, got %d", len(fallback))
	}
}

func TestCollectOutputItem_NoItem(t *testing.T) {
	eventData := []byte(`{"type":"response.output_item.done"}`)
	itemsByIndex := make(map[int64][]byte)
	var fallback [][]byte

	collectOutputItem(eventData, itemsByIndex, &fallback)

	if len(itemsByIndex) != 0 || len(fallback) != 0 {
		t.Fatalf("expected no items collected for missing item field")
	}
}

func TestPatchCompletedOutput_EmptyOutput(t *testing.T) {
	data := []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[]}}`)
	item := []byte(`{"type":"message","content":[{"type":"output_text","text":"hello"}]}`)
	itemsByIndex := map[int64][]byte{0: item}

	patched := patchCompletedOutput(data, itemsByIndex, nil)

	text := gjson.GetBytes(patched, "response.output.0.content.0.text").String()
	if text != "hello" {
		t.Fatalf("output text = %q, want %q", text, "hello")
	}
}

func TestPatchCompletedOutput_AlreadyPopulated(t *testing.T) {
	data := []byte(`{"type":"response.completed","response":{"output":[{"type":"message"}]}}`)
	item := []byte(`{"type":"message","content":[{"type":"output_text","text":"new"}]}`)

	patched := patchCompletedOutput(data, map[int64][]byte{0: item}, nil)

	if string(patched) != string(data) {
		t.Fatalf("expected no change for already-populated output")
	}
}

func TestPatchCompletedOutput_NoItems(t *testing.T) {
	data := []byte(`{"type":"response.completed","response":{"output":[]}}`)

	patched := patchCompletedOutput(data, nil, nil)

	if string(patched) != string(data) {
		t.Fatalf("expected no change when no items collected")
	}
}

func TestPatchCompletedOutput_MultipleItemsSorted(t *testing.T) {
	data := []byte(`{"type":"response.completed","response":{"output":[]}}`)
	itemsByIndex := map[int64][]byte{
		1: []byte(`{"type":"function_call","name":"search"}`),
		0: []byte(`{"type":"message","content":[{"type":"output_text","text":"hi"}]}`),
	}

	patched := patchCompletedOutput(data, itemsByIndex, nil)

	output := gjson.GetBytes(patched, "response.output")
	if len(output.Array()) != 2 {
		t.Fatalf("expected 2 output items, got %d", len(output.Array()))
	}
	if gjson.GetBytes(patched, "response.output.0.type").String() != "message" {
		t.Fatalf("expected first item to be message")
	}
	if gjson.GetBytes(patched, "response.output.1.type").String() != "function_call" {
		t.Fatalf("expected second item to be function_call")
	}
}

func TestPatchCompletedOutputInSSELine_EmptyOutput(t *testing.T) {
	line := []byte(`data: {"type":"response.completed","response":{"output":[]}}`)
	item := []byte(`{"type":"message","content":[{"type":"output_text","text":"hello"}]}`)

	patched := patchCompletedOutputInSSELine(line, map[int64][]byte{0: item}, nil)

	data := patched[6:] // strip "data: "
	text := gjson.GetBytes(data, "response.output.0.content.0.text").String()
	if text != "hello" {
		t.Fatalf("output text = %q, want %q", text, "hello")
	}
}

func TestPatchCompletedOutputInSSELine_AlreadyPopulated(t *testing.T) {
	line := []byte(`data: {"type":"response.completed","response":{"output":[{"type":"message"}]}}`)

	patched := patchCompletedOutputInSSELine(line, map[int64][]byte{0: []byte(`{"type":"new"}`)}, nil)

	if string(patched) != string(line) {
		t.Fatalf("expected no change for already-populated output")
	}
}

func TestPatchCompletedOutputInSSELine_NonDataLine(t *testing.T) {
	line := []byte(`event: response.completed`)

	patched := patchCompletedOutputInSSELine(line, map[int64][]byte{0: []byte(`{}`)}, nil)

	if string(patched) != string(line) {
		t.Fatalf("expected no change for non-data line")
	}
}

func TestPatchCompletedOutputInSSELine_NoItems(t *testing.T) {
	line := []byte(`data: {"type":"response.completed","response":{"output":[]}}`)

	patched := patchCompletedOutputInSSELine(line, nil, nil)

	if string(patched) != string(line) {
		t.Fatalf("expected no change when no items collected")
	}
}
