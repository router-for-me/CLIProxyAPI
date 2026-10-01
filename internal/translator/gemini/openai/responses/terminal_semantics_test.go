package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const (
	terminalContentFrame = `data: {"candidates":[{"content":{"parts":[{"text":"answer"}]}}],"responseId":"terminal"}`
	terminalUsageObject  = `"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":10,"thoughtsTokenCount":50,"totalTokenCount":160}`
	terminalDoneFrame    = "data: [DONE]"
)

func terminalFrame(finishReason, usage string) string {
	return `data: {"candidates":[{"finishReason":"` + finishReason + `"}]` + usage + `,"responseId":"terminal"}`
}

type terminalObservation struct {
	Terminals    []gjson.Result
	ItemDone     []gjson.Result
	SequenceNums []int64
	OutputDones  int
	TextDones    int
}

// observeTerminalStream pushes every frame through the streaming converter and
// collects the terminal and item-closure events of the whole stream.
func observeTerminalStream(t *testing.T, frames ...string) terminalObservation {
	t.Helper()

	var param any
	observed := terminalObservation{}
	for _, frame := range frames {
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(frame), &param) {
			event, data := parseSSEEvent(t, chunk)
			observed.SequenceNums = append(observed.SequenceNums, data.Get("sequence_number").Int())
			switch event {
			case "response.completed", "response.incomplete":
				observed.Terminals = append(observed.Terminals, data)
			case "response.output_item.done":
				observed.ItemDone = append(observed.ItemDone, data)
			case "response.output_text.done":
				observed.TextDones++
			}
		}
	}
	return observed
}

func requireSingleTerminal(t *testing.T, observed terminalObservation, eventType string) gjson.Result {
	t.Helper()
	if len(observed.Terminals) != 1 {
		t.Fatalf("terminal events = %d, want 1", len(observed.Terminals))
	}
	terminal := observed.Terminals[0]
	if terminal.Get("type").String() != eventType {
		t.Fatalf("terminal type = %q, want %q: %s", terminal.Get("type").String(), eventType, terminal.Raw)
	}
	return terminal
}

func requireStrictlyIncreasing(t *testing.T, observed terminalObservation) {
	t.Helper()
	for i := 1; i < len(observed.SequenceNums); i++ {
		if observed.SequenceNums[i] <= observed.SequenceNums[i-1] {
			t.Fatalf("sequence numbers not strictly increasing at %d: %v", i, observed.SequenceNums)
		}
	}
}

// requireValidFrame rejects malformed fixtures. gjson parses them leniently and
// silently drops trailing keys, which would hide a missing finishReason.
func requireValidFrame(t *testing.T, frame string) string {
	t.Helper()
	payload := strings.TrimSpace(strings.TrimPrefix(frame, "data:"))
	if !gjson.Valid(payload) {
		t.Fatalf("fixture is not valid JSON: %s", frame)
	}
	return frame
}

func requireValidJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	if !gjson.ValidBytes(raw) {
		t.Fatalf("fixture is not valid JSON: %s", raw)
	}
	return raw
}

func TestTerminalSameFrameUsageFinalizesImmediately(t *testing.T) {
	observed := observeTerminalStream(t,
		requireValidFrame(t, terminalContentFrame),
		requireValidFrame(t, terminalFrame("STOP", ","+terminalUsageObject)))
	terminal := requireSingleTerminal(t, observed, "response.completed")
	if terminal.Get("response.status").String() != "completed" {
		t.Fatalf("status = %q, want completed", terminal.Get("response.status").String())
	}
	usage := terminal.Get("response.usage")
	if usage.Get("input_tokens").Int() != 100 || usage.Get("output_tokens").Int() != 60 ||
		usage.Get("output_tokens_details.reasoning_tokens").Int() != 50 || usage.Get("total_tokens").Int() != 160 {
		t.Fatalf("usage = %s", usage.Raw)
	}
	requireStrictlyIncreasing(t, observed)
}

func TestTerminalFinishWithoutUsageDefersTerminalUntilUsageTail(t *testing.T) {
	finishOnly := observeTerminalStream(t, requireValidFrame(t, terminalContentFrame), requireValidFrame(t, terminalFrame("STOP", "")))
	if len(finishOnly.Terminals) != 0 {
		t.Fatalf("finish reason without usage emitted %d terminal events", len(finishOnly.Terminals))
	}
	if finishOnly.TextDones != 0 {
		t.Fatalf("pending terminal closed the message early: %d text done events", finishOnly.TextDones)
	}

	// The tail must be consumed, not dropped by the completed guard.
	var param any
	for _, frame := range []string{terminalContentFrame, terminalFrame("STOP", "")} {
		ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(frame), &param)
	}
	if tail := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(`data: {`+terminalUsageObject+`,"responseId":"terminal"}`), &param); len(tail) != 0 {
		t.Fatalf("usage-only tail emitted %d events before [DONE]", len(tail))
	}

	done := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(terminalDoneFrame), &param)
	observed := terminalObservation{}
	for _, chunk := range done {
		event, data := parseSSEEvent(t, chunk)
		observed.SequenceNums = append(observed.SequenceNums, data.Get("sequence_number").Int())
		switch event {
		case "response.completed", "response.incomplete":
			observed.Terminals = append(observed.Terminals, data)
		case "response.output_item.done":
			observed.ItemDone = append(observed.ItemDone, data)
		}
	}
	terminal := requireSingleTerminal(t, observed, "response.completed")
	usage := terminal.Get("response.usage")
	if usage.Get("input_tokens").Int() != 100 || usage.Get("output_tokens").Int() != 60 ||
		usage.Get("output_tokens_details.reasoning_tokens").Int() != 50 || usage.Get("total_tokens").Int() != 160 {
		t.Fatalf("split-frame usage = %s", usage.Raw)
	}
}

func TestTerminalUsesInternalUsageCarrier(t *testing.T) {
	observed := observeTerminalStream(t,
		terminalContentFrame,
		terminalFrame("STOP", ""),
		`data: {"cpaUsageMetadata":{"promptTokenCount":8,"candidatesTokenCount":2,"totalTokenCount":10},"responseId":"terminal"}`,
		terminalDoneFrame,
	)
	terminal := requireSingleTerminal(t, observed, "response.completed")
	if terminal.Get("response.usage.total_tokens").Int() != 10 {
		t.Fatalf("cpaUsageMetadata carrier was not consumed: %s", terminal.Raw)
	}
	if strings.Contains(terminal.Raw, "cpaUsageMetadata") {
		t.Fatalf("internal usage carrier leaked downstream: %s", terminal.Raw)
	}
}

func TestTerminalNativeUsageWinsOverInternalCarrier(t *testing.T) {
	observed := observeTerminalStream(t,
		terminalContentFrame,
		`data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":10,"totalTokenCount":110},"cpaUsageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},"responseId":"terminal"}`,
	)
	terminal := requireSingleTerminal(t, observed, "response.completed")
	if terminal.Get("response.usage.input_tokens").Int() != 100 || terminal.Get("response.usage.total_tokens").Int() != 110 {
		t.Fatalf("native usageMetadata did not win: %s", terminal.Get("response.usage").Raw)
	}
}

func TestTerminalUsageMergesByFieldPresenceWithoutSumming(t *testing.T) {
	observed := observeTerminalStream(t,
		`data: {"candidates":[{"content":{"parts":[{"text":"a"}]}}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":5,"thoughtsTokenCount":7,"totalTokenCount":62},"responseId":"terminal"}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"b"}]}}],"usageMetadata":{"candidatesTokenCount":9},"responseId":"terminal"}`,
		terminalFrame("STOP", ""),
		terminalDoneFrame,
	)
	terminal := requireSingleTerminal(t, observed, "response.completed")
	usage := terminal.Get("response.usage")
	// Fields absent from the later frame keep their previous value, and the
	// candidates count is replaced rather than summed.
	if usage.Get("input_tokens").Int() != 50 {
		t.Fatalf("input_tokens = %d, want 50 (absent field must not be cleared)", usage.Get("input_tokens").Int())
	}
	if usage.Get("output_tokens").Int() != 16 || usage.Get("output_tokens_details.reasoning_tokens").Int() != 7 {
		t.Fatalf("output_tokens = %s, want 16 with 7 reasoning", usage.Raw)
	}
	if usage.Get("total_tokens").Int() != 62 {
		t.Fatalf("total_tokens = %d, want 62 (upstream count, not a local sum)", usage.Get("total_tokens").Int())
	}
}

func TestTerminalExplicitZeroUsageOverwritesPreviousValue(t *testing.T) {
	observed := observeTerminalStream(t,
		`data: {"candidates":[{"content":{"parts":[{"text":"a"}]}}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":5,"thoughtsTokenCount":7,"totalTokenCount":62},"responseId":"terminal"}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"b"}]}}],"usageMetadata":{"thoughtsTokenCount":0},"responseId":"terminal"}`,
		terminalFrame("STOP", ""),
		terminalDoneFrame,
	)
	terminal := requireSingleTerminal(t, observed, "response.completed")
	usage := terminal.Get("response.usage")
	if usage.Get("output_tokens_details.reasoning_tokens").Int() != 0 || usage.Get("output_tokens").Int() != 5 {
		t.Fatalf("explicit zero did not overwrite the previous value: %s", usage.Raw)
	}
}

func TestTerminalNoUsageAnywhereEmitsNoUsageObject(t *testing.T) {
	observed := observeTerminalStream(t, terminalContentFrame, terminalFrame("STOP", ""), terminalDoneFrame)
	terminal := requireSingleTerminal(t, observed, "response.completed")
	if terminal.Get("response.usage").Exists() {
		t.Fatalf("usage object was fabricated without upstream usage: %s", terminal.Raw)
	}
}

func TestTerminalRepeatFinishUsageAndDoneStayIdempotent(t *testing.T) {
	observed := observeTerminalStream(t,
		terminalContentFrame,
		terminalFrame("STOP", ""),
		terminalFrame("STOP", ""),
		`data: {`+terminalUsageObject+`,"responseId":"terminal"}`,
		terminalDoneFrame,
		terminalDoneFrame,
		`data: {"candidates":[{"content":{"parts":[{"text":"late"}]}}],"responseId":"terminal"}`,
	)
	requireSingleTerminal(t, observed, "response.completed")
	if len(observed.ItemDone) != 1 {
		t.Fatalf("output_item.done events = %d, want 1", len(observed.ItemDone))
	}
	if observed.TextDones != 1 {
		t.Fatalf("output_text.done events = %d, want 1", observed.TextDones)
	}
	requireStrictlyIncreasing(t, observed)
}

func TestTerminalNonStreamStopFinalizesCompleted(t *testing.T) {
	stopRaw := requireValidJSON(t, []byte(`{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":4,"thoughtsTokenCount":1,"totalTokenCount":10},"responseId":"terminal"}`))
	stopOut := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, stopRaw, nil)
	if gjson.GetBytes(stopOut, "status").String() != "completed" {
		t.Fatalf("non-stream STOP status = %s", gjson.GetBytes(stopOut, "status").Raw)
	}
	if gjson.GetBytes(stopOut, "incomplete_details.reason").Exists() {
		t.Fatalf("non-stream STOP must not report incomplete details: %s", stopOut)
	}
	if gjson.GetBytes(stopOut, "usage.output_tokens").Int() != 5 {
		t.Fatalf("non-stream usage = %s", gjson.GetBytes(stopOut, "usage").Raw)
	}
}
