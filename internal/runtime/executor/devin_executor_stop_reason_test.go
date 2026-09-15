package executor

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

// buildTruncatedToolCallStream builds a Devin Connect stream that mimics the
// real-world failure: a thought, some text, then a tool call whose arguments
// are cut off because the model hit its output budget (StopReason=3 MAX_TOKENS).
func buildTruncatedToolCallStream(stopReason uint64) *bytes.Buffer {
	var f1 []byte
	f1 = protowire.AppendTag(f1, 9, protowire.BytesType)
	f1 = protowire.AppendString(f1, "Planning the edit.")

	var f2 []byte
	f2 = protowire.AppendTag(f2, 3, protowire.BytesType)
	f2 = protowire.AppendString(f2, "Let me apply the change.")

	var tc []byte
	tc = protowire.AppendTag(tc, 1, protowire.BytesType)
	tc = protowire.AppendString(tc, "edit_0")
	tc = protowire.AppendTag(tc, 2, protowire.BytesType)
	tc = protowire.AppendString(tc, "edit")
	tc = protowire.AppendTag(tc, 3, protowire.BytesType)
	tc = protowire.AppendString(tc, `{"path":"a.go","content":"partial`)
	tc = protowire.AppendTag(tc, 4, protowire.VarintType)
	tc = protowire.AppendVarint(tc, 0)
	var f3 []byte
	f3 = protowire.AppendTag(f3, 6, protowire.BytesType)
	f3 = protowire.AppendBytes(f3, tc)

	var f4 []byte
	f4 = protowire.AppendTag(f4, 5, protowire.VarintType)
	f4 = protowire.AppendVarint(f4, stopReason)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelope(f2))
	buf.Write(helps.WrapConnectEnvelope(f3))
	buf.Write(helps.WrapConnectEnvelope(f4))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
	return &buf
}

func collectDevinStreamEvents(t *testing.T, buf *bytes.Buffer, sourceFormat, responseFormat sdktranslator.Format) []gjson.Result {
	t.Helper()
	exec := NewDevinExecutor(&config.Config{})
	out := make(chan cliproxyexecutor.StreamChunk, 100)
	opts := cliproxyexecutor.Options{SourceFormat: sourceFormat}
	go func() {
		defer close(out)
		exec.streamDevinFrames(context.Background(), buf, cliproxyexecutor.Request{Model: "devin/swe-2"}, opts, "chat-model-uid", responseFormat, nil, out)
	}()
	var events []gjson.Result
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		// Payloads may be bare JSON, "data: {...}" or full SSE frames
		// ("event: x\ndata: {...}"); pick the data line in every case.
		for _, line := range strings.Split(string(chunk.Payload), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "event:") || line == "" {
				continue
			}
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if line == "" || line == "[DONE]" {
				continue
			}
			if parsed := gjson.Parse(line); parsed.Exists() {
				events = append(events, parsed)
			}
		}
	}
	return events
}

func TestStreamDevinFrames_MaxTokensStopReasonPropagatesToInteractionsCompleted(t *testing.T) {
	events := collectDevinStreamEvents(t, buildTruncatedToolCallStream(helps.DevinStopReasonMaxTokens), sdktranslator.FormatInteractions, sdktranslator.FormatInteractions)
	var completed gjson.Result
	for _, ev := range events {
		if ev.Get("event_type").String() == "interaction.completed" {
			completed = ev
		}
	}
	if !completed.Exists() {
		t.Fatalf("interaction.completed not found in %d events", len(events))
	}
	if got := completed.Get("interaction.status").String(); got != "incomplete" {
		t.Fatalf("interaction.status = %q, want incomplete. Payload: %s", got, completed.Raw)
	}
	if got := completed.Get("interaction.stop_reason").String(); got != "length" {
		t.Fatalf("interaction.stop_reason = %q, want length. Payload: %s", got, completed.Raw)
	}
	if got := completed.Get("interaction.stop_reason_code").Int(); got != int64(helps.DevinStopReasonMaxTokens) {
		t.Fatalf("interaction.stop_reason_code = %d, want %d. Payload: %s", got, helps.DevinStopReasonMaxTokens, completed.Raw)
	}
}

func TestStreamDevinFrames_FunctionCallStopReasonKeepsCompleted(t *testing.T) {
	events := collectDevinStreamEvents(t, buildTruncatedToolCallStream(helps.DevinStopReasonFunctionCall), sdktranslator.FormatInteractions, sdktranslator.FormatInteractions)
	for _, ev := range events {
		if ev.Get("event_type").String() != "interaction.completed" {
			continue
		}
		if got := ev.Get("interaction.status").String(); got != "completed" {
			t.Fatalf("interaction.status = %q, want completed. Payload: %s", got, ev.Raw)
		}
		if got := ev.Get("interaction.stop_reason").String(); got != "tool_calls" {
			t.Fatalf("interaction.stop_reason = %q, want tool_calls. Payload: %s", got, ev.Raw)
		}
		return
	}
	t.Fatalf("interaction.completed not found")
}

// Full pipeline for the reported opencode/zcode symptom: OpenAI chat-completions
// client, Devin upstream. The tool call must be tool_calls[0] even though it is
// the third interactions step, and the truncation must surface as finish_reason
// "length" instead of "stop"/"tool_calls".
func TestStreamDevinFrames_OpenAIChatTruncatedToolCallHasOrdinalIndexAndLengthFinish(t *testing.T) {
	events := collectDevinStreamEvents(t, buildTruncatedToolCallStream(helps.DevinStopReasonMaxTokens), sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAI)
	sawToolChunk := false
	finish := ""
	for _, ev := range events {
		if tc := ev.Get("choices.0.delta.tool_calls"); tc.Exists() {
			sawToolChunk = true
			tc.ForEach(func(_, call gjson.Result) bool {
				if got := call.Get("index").Int(); got != 0 {
					t.Fatalf("tool_calls index = %d, want 0. Payload: %s", got, ev.Raw)
				}
				return true
			})
		}
		if fr := ev.Get("choices.0.finish_reason"); fr.Exists() && fr.Type != gjson.Null {
			finish = fr.String()
		}
	}
	if !sawToolChunk {
		t.Fatalf("no tool_calls chunk emitted; events=%d", len(events))
	}
	if finish != "length" {
		t.Fatalf("finish_reason = %q, want length", finish)
	}
}

// Same pipeline for OpenAI Responses clients (ZCode): the function_call
// output_item.done must carry a status, and because the call was cut mid-
// arguments it must be "incomplete" (never an executable "completed"); the
// truncation itself surfaces as response.incomplete / max_output_tokens.
func TestStreamDevinFrames_OpenAIResponsesTruncatedToolCallHasStatusAndIncomplete(t *testing.T) {
	events := collectDevinStreamEvents(t, buildTruncatedToolCallStream(helps.DevinStopReasonMaxTokens), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAIResponse)
	var doneStatus, outputStatus, terminalType, incompleteReason string
	for _, ev := range events {
		switch ev.Get("type").String() {
		case "response.output_item.done":
			if ev.Get("item.type").String() == "function_call" {
				doneStatus = ev.Get("item.status").String()
			}
		case "response.completed", "response.incomplete":
			terminalType = ev.Get("type").String()
			incompleteReason = ev.Get("response.incomplete_details.reason").String()
			ev.Get("response.output").ForEach(func(_, item gjson.Result) bool {
				if item.Get("type").String() == "function_call" {
					outputStatus = item.Get("status").String()
				}
				return true
			})
		}
	}
	if doneStatus != "incomplete" {
		t.Fatalf("function_call output_item.done status = %q, want incomplete (events=%d)", doneStatus, len(events))
	}
	if outputStatus != "incomplete" {
		t.Fatalf("terminal output function_call status = %q, want incomplete", outputStatus)
	}
	if terminalType != "response.incomplete" {
		t.Fatalf("terminal event = %q, want response.incomplete", terminalType)
	}
	if incompleteReason != "max_output_tokens" {
		t.Fatalf("incomplete_details.reason = %q, want max_output_tokens", incompleteReason)
	}
}

// A complete tool call that ends with FUNCTION_CALL stays "completed" end to end.
func TestStreamDevinFrames_OpenAIResponsesCompleteToolCallStaysCompleted(t *testing.T) {
	var tc []byte
	tc = protowire.AppendTag(tc, 1, protowire.BytesType)
	tc = protowire.AppendString(tc, "edit_0")
	tc = protowire.AppendTag(tc, 2, protowire.BytesType)
	tc = protowire.AppendString(tc, "edit")
	tc = protowire.AppendTag(tc, 3, protowire.BytesType)
	tc = protowire.AppendString(tc, `{"path":"a.go","content":"ok"}`)
	tc = protowire.AppendTag(tc, 4, protowire.VarintType)
	tc = protowire.AppendVarint(tc, 0)
	var f1 []byte
	f1 = protowire.AppendTag(f1, 6, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, tc)
	var f2 []byte
	f2 = protowire.AppendTag(f2, 5, protowire.VarintType)
	f2 = protowire.AppendVarint(f2, helps.DevinStopReasonFunctionCall)
	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelope(f2))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	events := collectDevinStreamEvents(t, &buf, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAIResponse)
	var doneStatus, terminalType string
	for _, ev := range events {
		switch ev.Get("type").String() {
		case "response.output_item.done":
			if ev.Get("item.type").String() == "function_call" {
				doneStatus = ev.Get("item.status").String()
			}
		case "response.completed", "response.incomplete":
			terminalType = ev.Get("type").String()
		}
	}
	if doneStatus != "completed" {
		t.Fatalf("function_call output_item.done status = %q, want completed", doneStatus)
	}
	if terminalType != "response.completed" {
		t.Fatalf("terminal event = %q, want response.completed", terminalType)
	}
}

func TestConsumeDevinFramesToInteractions_MaxTokensStopReason(t *testing.T) {
	out, respLog, err := consumeDevinFramesToInteractions(buildTruncatedToolCallStream(helps.DevinStopReasonMaxTokens), "devin/swe-2", "chat-model-uid")
	if err != nil {
		t.Fatalf("unexpected error: %v (log=%+v)", err, respLog)
	}
	if got := gjson.GetBytes(out, "status").String(); got != "incomplete" {
		t.Fatalf("status = %q, want incomplete. Output: %s", got, out)
	}
	if got := gjson.GetBytes(out, "stop_reason").String(); got != "length" {
		t.Fatalf("stop_reason = %q, want length. Output: %s", got, out)
	}
}
