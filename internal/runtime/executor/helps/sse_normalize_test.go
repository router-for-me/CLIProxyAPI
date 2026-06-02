package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSSENormalizer_NormalStream(t *testing.T) {
	// A correctly ordered stream should pass through unchanged.
	var n SSENormalizer

	lines := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1"}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Hello"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"4"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":1}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":50}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}

	var output [][]byte
	for _, line := range lines {
		result := n.ProcessLine([]byte(line))
		for _, r := range result {
			output = append(output, r)
		}
	}
	flushed := n.Flush()
	output = append(output, flushed...)

	// message_stop should be the last event
	lastEventType := ""
	for i := len(output) - 1; i >= 0; i-- {
		et := sseEventType(output[i])
		if et != "" {
			lastEventType = et
			break
		}
	}
	if lastEventType != "message_stop" {
		t.Errorf("expected last event to be message_stop, got %q", lastEventType)
	}

	// Should have same number of event: lines as input
	inputEvents := 0
	for _, l := range lines {
		if sseEventType([]byte(l)) != "" {
			inputEvents++
		}
	}
	outputEvents := 0
	for _, l := range output {
		if sseEventType(l) != "" {
			outputEvents++
		}
	}
	if inputEvents != outputEvents {
		t.Errorf("expected %d events, got %d", inputEvents, outputEvents)
	}
}

func TestSSENormalizer_ReorderBrokenStream(t *testing.T) {
	// Baidu-glm-5.1 pattern: message_stop followed by extra content blocks
	var n SSENormalizer

	lines := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1"}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":1}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":50}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
		// Extra content block AFTER message_stop (the bug)
		"event: content_block_start",
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"2 + 2 = 4"}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":55}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":2}`,
		"",
	}

	var output [][]byte
	for _, line := range lines {
		result := n.ProcessLine([]byte(line))
		for _, r := range result {
			output = append(output, r)
		}
	}
	flushed := n.Flush()
	output = append(output, flushed...)

	// After normalization, message_stop must be the last event
	lastEventType := ""
	for i := len(output) - 1; i >= 0; i-- {
		et := sseEventType(output[i])
		if et != "" {
			lastEventType = et
			break
		}
	}
	if lastEventType != "message_stop" {
		t.Errorf("expected last event to be message_stop, got %q", lastEventType)
	}

	// All content_block events should appear before any message_delta/message_stop
	lastContentBlockIdx := -1
	firstDeltaIdx := -1
	firstStopIdx := -1
	for i, line := range output {
		et := sseEventType(line)
		switch et {
		case "content_block_start", "content_block_delta", "content_block_stop":
			lastContentBlockIdx = i
		case "message_delta":
			if firstDeltaIdx == -1 {
				firstDeltaIdx = i
			}
		case "message_stop":
			if firstStopIdx == -1 {
				firstStopIdx = i
			}
		}
	}

	if firstDeltaIdx == -1 {
		t.Fatal("expected to find message_delta event")
	}
	if firstStopIdx == -1 {
		t.Fatal("expected to find message_stop event")
	}
	if lastContentBlockIdx > firstDeltaIdx {
		t.Errorf("content block at index %d should come before message_delta at index %d", lastContentBlockIdx, firstDeltaIdx)
	}
	if lastContentBlockIdx > firstStopIdx {
		t.Errorf("content block at index %d should come before message_stop at index %d", lastContentBlockIdx, firstStopIdx)
	}

	// Should preserve all events (no data loss)
	inputEvents := 0
	for _, l := range lines {
		if sseEventType([]byte(l)) != "" {
			inputEvents++
		}
	}
	outputEvents := 0
	for _, l := range output {
		if sseEventType(l) != "" {
			outputEvents++
		}
	}
	if inputEvents != outputEvents {
		t.Errorf("expected %d events, got %d (data loss)", inputEvents, outputEvents)
	}
}

func TestSSENormalizer_ToolUseStream(t *testing.T) {
	// Tool use streams end with stop_reason=tool_use — no events after message_stop.
	var n SSENormalizer

	lines := []string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1"}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"get_weather","input":{}}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":1}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":30}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}

	var output [][]byte
	for _, line := range lines {
		result := n.ProcessLine([]byte(line))
		for _, r := range result {
			output = append(output, r)
		}
	}
	flushed := n.Flush()
	output = append(output, flushed...)

	eventCount := 0
	for _, line := range output {
		if sseEventType(line) != "" {
			eventCount++
		}
	}
	if eventCount != 7 {
		t.Errorf("expected 7 events, got %d", eventCount)
	}
}

func TestSSENormalizer_BlankLinesPassThrough(t *testing.T) {
	var n SSENormalizer

	result := n.ProcessLine([]byte(""))
	if len(result) != 1 || string(result[0]) != "" {
		t.Errorf("expected blank line to pass through, got %v", result)
	}
}

func TestNormalizeNonStreamContentOrder_CorrectOrder(t *testing.T) {
	input := []byte(`{"content":[{"type":"thinking","thinking":"..."},{"type":"text","text":"4"}],"stop_reason":"end_turn"}`)
	result := NormalizeNonStreamContentOrder(input)
	if string(result) != string(input) {
		t.Errorf("expected no change for correct order, got %s", result)
	}
}

func TestNormalizeNonStreamContentOrder_WrongOrder(t *testing.T) {
	input := []byte(`{"content":[{"type":"text","text":"4"},{"type":"thinking","thinking":"..."}],"stop_reason":"end_turn"}`)
	result := NormalizeNonStreamContentOrder(input)

	firstType := gjson.GetBytes(result, "content.0.type").String()
	if firstType != "thinking" {
		t.Errorf("expected first block to be thinking, got %q", firstType)
	}
	secondType := gjson.GetBytes(result, "content.1.type").String()
	if secondType != "text" {
		t.Errorf("expected second block to be text, got %q", secondType)
	}
}

func TestNormalizeNonStreamContentOrder_WithToolUse(t *testing.T) {
	input := []byte(`{"content":[{"type":"text","text":"4"},{"type":"thinking","thinking":"..."},{"type":"tool_use","id":"c1","name":"fn","input":{}}],"stop_reason":"tool_use"}`)
	result := NormalizeNonStreamContentOrder(input)

	types := []string{
		gjson.GetBytes(result, "content.0.type").String(),
		gjson.GetBytes(result, "content.1.type").String(),
		gjson.GetBytes(result, "content.2.type").String(),
	}
	expected := []string{"thinking", "text", "tool_use"}
	for i, exp := range expected {
		if types[i] != exp {
			t.Errorf("content[%d]: expected %q, got %q", i, exp, types[i])
		}
	}
}

func TestNormalizeNonStreamContentOrder_SingleBlock(t *testing.T) {
	input := []byte(`{"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"}`)
	result := NormalizeNonStreamContentOrder(input)
	if string(result) != string(input) {
		t.Errorf("expected no change for single block, got %s", result)
	}
}

func TestNormalizeNonStreamContentOrder_NoContent(t *testing.T) {
	input := []byte(`{"stop_reason":"end_turn"}`)
	result := NormalizeNonStreamContentOrder(input)
	if string(result) != string(input) {
		t.Errorf("expected no change for missing content, got %s", result)
	}
}
