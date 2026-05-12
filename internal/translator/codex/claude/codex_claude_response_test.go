package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToClaude_StreamThinkingIncludesSignature(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"messages":[]}`)
	var param any

	chunks := [][]byte{
		[]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_123\",\"model\":\"gpt-5\"}}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Let me think\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.done\"}"),
		[]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"enc_sig_123\"}}"),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}

	startFound := false
	signatureDeltaFound := false
	stopFound := false

	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			switch data.Get("type").String() {
			case "content_block_start":
				if data.Get("content_block.type").String() == "thinking" {
					startFound = true
					if data.Get("content_block.signature").Exists() {
						t.Fatalf("thinking start block should NOT have signature field when signature is unknown: %s", line)
					}
				}
			case "content_block_delta":
				if data.Get("delta.type").String() == "signature_delta" {
					signatureDeltaFound = true
					if got := data.Get("delta.signature").String(); got != "enc_sig_123" {
						t.Fatalf("unexpected signature delta: %q", got)
					}
				}
			case "content_block_stop":
				stopFound = true
			}
		}
	}

	if !startFound {
		t.Fatal("expected thinking content_block_start event")
	}
	if !signatureDeltaFound {
		t.Fatal("expected signature_delta event for thinking block")
	}
	if !stopFound {
		t.Fatal("expected content_block_stop event for thinking block")
	}
}

func TestConvertCodexResponseToClaude_StreamThinkingWithoutReasoningItemStillIncludesSignatureField(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"messages":[]}`)
	var param any

	chunks := [][]byte{
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Let me think\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.done\"}"),
		[]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}"),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}

	thinkingStartFound := false
	thinkingStopFound := false
	signatureDeltaFound := false

	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if data.Get("type").String() == "content_block_start" && data.Get("content_block.type").String() == "thinking" {
				thinkingStartFound = true
				if data.Get("content_block.signature").Exists() {
					t.Fatalf("thinking start block should NOT have signature field without encrypted_content: %s", line)
				}
			}
			if data.Get("type").String() == "content_block_stop" && data.Get("index").Int() == 0 {
				thinkingStopFound = true
			}
			if data.Get("type").String() == "content_block_delta" && data.Get("delta.type").String() == "signature_delta" {
				signatureDeltaFound = true
			}
		}
	}

	if !thinkingStartFound {
		t.Fatal("expected thinking content_block_start event")
	}
	if !thinkingStopFound {
		t.Fatal("expected thinking content_block_stop event")
	}
	if signatureDeltaFound {
		t.Fatal("did not expect signature_delta without encrypted_content")
	}
}

func TestConvertCodexResponseToClaude_StreamThinkingFinalizesPendingBlockBeforeNextSummaryPart(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"messages":[]}`)
	var param any

	chunks := [][]byte{
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"First part\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.done\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}

	startCount := 0
	stopCount := 0
	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if data.Get("type").String() == "content_block_start" && data.Get("content_block.type").String() == "thinking" {
				startCount++
			}
			if data.Get("type").String() == "content_block_stop" {
				stopCount++
			}
		}
	}

	if startCount != 2 {
		t.Fatalf("expected 2 thinking block starts, got %d", startCount)
	}
	if stopCount != 1 {
		t.Fatalf("expected pending thinking block to be finalized before second start, got %d stops", stopCount)
	}
}

func TestConvertCodexResponseToClaude_StreamThinkingRetainsSignatureAcrossMultipartReasoning(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"messages":[]}`)
	var param any

	chunks := [][]byte{
		[]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"enc_sig_multipart\"}}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"First part\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.done\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Second part\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.done\"}"),
		[]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\"}}"),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}

	signatureDeltaCount := 0
	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if data.Get("type").String() == "content_block_delta" && data.Get("delta.type").String() == "signature_delta" {
				signatureDeltaCount++
				if got := data.Get("delta.signature").String(); got != "enc_sig_multipart" {
					t.Fatalf("unexpected signature delta: %q", got)
				}
			}
		}
	}

	if signatureDeltaCount != 2 {
		t.Fatalf("expected signature_delta for both multipart thinking blocks, got %d", signatureDeltaCount)
	}
}

func TestConvertCodexResponseToClaude_StreamThinkingUsesEarlyCapturedSignatureWhenDoneOmitsIt(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"messages":[]}`)
	var param any

	chunks := [][]byte{
		[]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"enc_sig_early\"}}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Let me think\"}"),
		[]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\"}}"),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}

	signatureDeltaCount := 0
	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if data.Get("type").String() == "content_block_delta" && data.Get("delta.type").String() == "signature_delta" {
				signatureDeltaCount++
				if got := data.Get("delta.signature").String(); got != "enc_sig_early" {
					t.Fatalf("unexpected signature delta: %q", got)
				}
			}
		}
	}

	if signatureDeltaCount != 1 {
		t.Fatalf("expected signature_delta from early-captured signature, got %d", signatureDeltaCount)
	}
}

func TestConvertCodexResponseToClaude_StreamThinkingUsesFinalDoneSignature(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"messages":[]}`)
	var param any

	chunks := [][]byte{
		[]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"enc_sig_initial\"}}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Let me think\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.done\"}"),
		[]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"enc_sig_final\"}}"),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}

	signatureDeltaCount := 0
	events := []string{}
	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if data.Get("type").String() == "content_block_start" && data.Get("content_block.type").String() == "thinking" {
				events = append(events, "thinking_start")
			}
			if data.Get("type").String() == "content_block_delta" && data.Get("delta.type").String() == "thinking_delta" {
				events = append(events, "thinking_delta")
			}
			if data.Get("type").String() == "content_block_stop" && data.Get("index").Int() == 0 {
				events = append(events, "thinking_stop")
			}
			if data.Get("type").String() != "content_block_delta" || data.Get("delta.type").String() != "signature_delta" {
				continue
			}
			events = append(events, "signature_delta")
			signatureDeltaCount++
			if got := data.Get("delta.signature").String(); got != "enc_sig_final" {
				t.Fatalf("signature delta = %q, want final done signature", got)
			}
		}
	}

	if signatureDeltaCount != 1 {
		t.Fatalf("expected one signature_delta, got %d", signatureDeltaCount)
	}
	if got, want := strings.Join(events, ","), "thinking_start,thinking_delta,signature_delta,thinking_stop"; got != want {
		t.Fatalf("thinking event order = %s, want %s", got, want)
	}
}

func TestConvertCodexResponseToClaude_StreamSignatureOnlyReasoningEmitsThinkingSignature(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"messages":[]}`)
	var param any

	chunks := [][]byte{
		[]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_123\",\"model\":\"gpt-5\"}}"),
		[]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"enc_sig_initial\"}}"),
		[]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"enc_sig_only\"}}"),
		[]byte("data: {\"type\":\"response.content_part.added\"}"),
		[]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}"),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}

	thinkingStartFound := false
	thinkingDeltaFound := false
	signatureDeltaFound := false
	thinkingStopFound := false
	textStartIndex := int64(-1)
	events := []string{}

	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			switch data.Get("type").String() {
			case "content_block_start":
				if data.Get("content_block.type").String() == "thinking" {
					events = append(events, "thinking_start")
					thinkingStartFound = true
					if got := data.Get("index").Int(); got != 0 {
						t.Fatalf("thinking block index = %d, want 0", got)
					}
				}
				if data.Get("content_block.type").String() == "text" {
					events = append(events, "text_start")
					textStartIndex = data.Get("index").Int()
				}
			case "content_block_delta":
				switch data.Get("delta.type").String() {
				case "thinking_delta":
					thinkingDeltaFound = true
				case "signature_delta":
					events = append(events, "signature_delta")
					signatureDeltaFound = true
					if got := data.Get("index").Int(); got != 0 {
						t.Fatalf("signature delta index = %d, want 0", got)
					}
					if got := data.Get("delta.signature").String(); got != "enc_sig_only" {
						t.Fatalf("unexpected signature delta: %q", got)
					}
				}
			case "content_block_stop":
				if data.Get("index").Int() == 0 {
					events = append(events, "thinking_stop")
					thinkingStopFound = true
				}
			}
		}
	}

	if !thinkingStartFound {
		t.Fatal("expected signature-only reasoning to start a thinking block")
	}
	if thinkingDeltaFound {
		t.Fatal("did not expect thinking_delta when upstream omitted summary text")
	}
	if !signatureDeltaFound {
		t.Fatal("expected signature_delta from encrypted_content-only reasoning")
	}
	if !thinkingStopFound {
		t.Fatal("expected signature-only thinking block to stop")
	}
	if textStartIndex != 1 {
		t.Fatalf("text block index = %d, want 1 after signature-only thinking block", textStartIndex)
	}
	if got, want := strings.Join(events, ","), "thinking_start,signature_delta,thinking_stop,text_start"; got != want {
		t.Fatalf("signature-only event order = %s, want %s", got, want)
	}
}

func TestConvertCodexResponseToClaudeNonStream_ThinkingIncludesSignature(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"messages":[]}`)
	response := []byte(`{
		"type":"response.completed",
		"response":{
			"id":"resp_123",
			"model":"gpt-5",
			"usage":{"input_tokens":10,"output_tokens":20},
			"output":[
				{
					"type":"reasoning",
					"encrypted_content":"enc_sig_nonstream",
					"summary":[{"type":"summary_text","text":"internal reasoning"}]
				},
				{
					"type":"message",
					"content":[{"type":"output_text","text":"final answer"}]
				}
			]
		}
	}`)

	out := ConvertCodexResponseToClaudeNonStream(ctx, "", originalRequest, nil, response, nil)
	parsed := gjson.ParseBytes(out)

	thinking := parsed.Get("content.0")
	if thinking.Get("type").String() != "thinking" {
		t.Fatalf("expected first content block to be thinking, got %s", thinking.Raw)
	}
	if got := thinking.Get("signature").String(); got != "enc_sig_nonstream" {
		t.Fatalf("expected signature to be preserved, got %q", got)
	}
	if got := thinking.Get("thinking").String(); got != "internal reasoning" {
		t.Fatalf("unexpected thinking text: %q", got)
	}
}

func TestConvertCodexResponseToClaude_StreamEmptyOutputUsesOutputItemDoneMessageFallback(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[]}`)
	var param any

	chunks := [][]byte{
		[]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5\"}}"),
		[]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]},\"output_index\":0}"),
		[]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}"),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}

	foundText := false
	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if data.Get("type").String() == "content_block_delta" && data.Get("delta.type").String() == "text_delta" && data.Get("delta.text").String() == "ok" {
				foundText = true
				break
			}
		}
		if foundText {
			break
		}
	}
	if !foundText {
		t.Fatalf("expected fallback content from response.output_item.done message; outputs=%q", outputs)
	}
}

func TestConvertCodexResponseToClaude_StreamStopReasonMapping(t *testing.T) {
	tests := []struct {
		name       string
		chunks     [][]byte
		wantReason string
	}{
		{
			name: "Stop maps to end_turn",
			chunks: [][]byte{
				[]byte("data: {\"type\":\"response.completed\",\"response\":{\"stop_reason\":\"stop\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}"),
			},
			wantReason: "end_turn",
		},
		{
			name: "Incomplete max output maps to max_tokens",
			chunks: [][]byte{
				[]byte("data: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}"),
			},
			wantReason: "max_tokens",
		},
		{
			name: "Tool call wins over stop",
			chunks: [][]byte{
				[]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"lookup\"}}"),
				[]byte("data: {\"type\":\"response.completed\",\"response\":{\"stop_reason\":\"stop\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}"),
			},
			wantReason: "tool_use",
		},
		{
			name: "Content filter maps to Claude refusal",
			chunks: [][]byte{
				[]byte("data: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"content_filter\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}"),
			},
			wantReason: "refusal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			originalRequest := []byte(`{"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{}}}]}`)
			var param any
			var outputs [][]byte

			for _, chunk := range tt.chunks {
				outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
			}

			got, ok := findClaudeStreamStopReason(outputs)
			if !ok {
				t.Fatalf("did not find message_delta stop_reason; outputs=%q", outputs)
			}
			if got != tt.wantReason {
				t.Fatalf("stop_reason = %q, want %q. Outputs=%q", got, tt.wantReason, outputs)
			}
		})
	}
}

func TestConvertCodexResponseToClaude_StreamStopSequenceMapping(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"messages":[]}`)
	var param any

	outputs := ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, []byte("data: {\"type\":\"response.completed\",\"response\":{\"stop_reason\":\"stop\",\"stop_sequence\":\"\\nEND\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}"), &param)
	messageDelta, ok := findClaudeStreamMessageDelta(outputs)
	if !ok {
		t.Fatalf("did not find message_delta; outputs=%q", outputs)
	}
	if got := messageDelta.Get("delta.stop_reason").String(); got != "stop_sequence" {
		t.Fatalf("stop_reason = %q, want stop_sequence. Outputs=%q", got, outputs)
	}
	if got := messageDelta.Get("delta.stop_sequence").String(); got != "\nEND" {
		t.Fatalf("stop_sequence = %q, want newline END. Outputs=%q", got, outputs)
	}
}

func TestConvertCodexResponseToClaudeNonStream_StopReasonMapping(t *testing.T) {
	tests := []struct {
		name       string
		response   []byte
		wantReason string
	}{
		{
			name: "Stop maps to end_turn",
			response: []byte(`{
				"type":"response.completed",
				"response":{
					"id":"resp_1",
					"model":"gpt-5",
					"stop_reason":"stop",
					"usage":{"input_tokens":1,"output_tokens":1},
					"output":[]
				}
			}`),
			wantReason: "end_turn",
		},
		{
			name: "Incomplete max output maps to max_tokens",
			response: []byte(`{
				"type":"response.incomplete",
				"response":{
					"id":"resp_1",
					"model":"gpt-5",
					"incomplete_details":{"reason":"max_output_tokens"},
					"usage":{"input_tokens":1,"output_tokens":1},
					"output":[]
				}
			}`),
			wantReason: "max_tokens",
		},
		{
			name: "Tool call wins over stop",
			response: []byte(`{
				"type":"response.completed",
				"response":{
					"id":"resp_1",
					"model":"gpt-5",
					"stop_reason":"stop",
					"usage":{"input_tokens":1,"output_tokens":1},
					"output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}]
				}
			}`),
			wantReason: "tool_use",
		},
		{
			name: "Content filter maps to Claude refusal",
			response: []byte(`{
				"type":"response.incomplete",
				"response":{
					"id":"resp_1",
					"model":"gpt-5",
					"incomplete_details":{"reason":"content_filter"},
					"usage":{"input_tokens":1,"output_tokens":1},
					"output":[]
				}
			}`),
			wantReason: "refusal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			originalRequest := []byte(`{"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{}}}]}`)
			out := ConvertCodexResponseToClaudeNonStream(ctx, "", originalRequest, nil, tt.response, nil)
			parsed := gjson.ParseBytes(out)

			if got := parsed.Get("stop_reason").String(); got != tt.wantReason {
				t.Fatalf("stop_reason = %q, want %q. Output: %s", got, tt.wantReason, string(out))
			}
		})
	}
}

func TestConvertCodexResponseToClaudeNonStream_StopSequenceMapping(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"messages":[]}`)
	response := []byte(`{
		"type":"response.completed",
		"response":{
			"id":"resp_1",
			"model":"gpt-5",
			"stop_reason":"stop",
			"stop_sequence":"\nEND",
			"usage":{"input_tokens":1,"output_tokens":1},
			"output":[]
		}
	}`)

	out := ConvertCodexResponseToClaudeNonStream(ctx, "", originalRequest, nil, response, nil)
	parsed := gjson.ParseBytes(out)

	if got := parsed.Get("stop_reason").String(); got != "stop_sequence" {
		t.Fatalf("stop_reason = %q, want stop_sequence. Output: %s", got, string(out))
	}
	if got := parsed.Get("stop_sequence").String(); got != "\nEND" {
		t.Fatalf("stop_sequence = %q, want newline END. Output: %s", got, string(out))
	}
}

func findClaudeStreamStopReason(outputs [][]byte) (string, bool) {
	messageDelta, ok := findClaudeStreamMessageDelta(outputs)
	if !ok {
		return "", false
	}
	return messageDelta.Get("delta.stop_reason").String(), true
}

func findClaudeStreamMessageDelta(outputs [][]byte) (gjson.Result, bool) {
	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if data.Get("type").String() == "message_delta" {
				return data, true
			}
		}
	}
	return gjson.Result{}, false
}
