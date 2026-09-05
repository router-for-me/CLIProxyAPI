package claude

import (
	"bytes"
	"context"
	"fmt"
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

func TestConvertCodexResponseToClaude_StreamCyberPolicyError(t *testing.T) {
	ctx := context.Background()
	var param any

	outputs := ConvertCodexResponseToClaude(ctx, "", []byte(`{"messages":[]}`), nil, []byte(`data: {"type":"error","error":{"type":"invalid_request","code":"cyber_policy","message":"This content was flagged for possible cybersecurity risk.","param":null},"sequence_number":3}`), &param)
	if len(outputs) != 1 {
		t.Fatalf("expected one error chunk, got %d: %q", len(outputs), outputs)
	}
	out := string(outputs[0])
	if !strings.Contains(out, "event: error\n") {
		t.Fatalf("expected Claude SSE error event, got: %q", out)
	}

	payload, ok := firstClaudeStreamPayloadForEvent(out, "error")
	if !ok {
		t.Fatalf("missing error event payload: %q", out)
	}
	if got := payload.Get("type").String(); got != "error" {
		t.Fatalf("type = %q, want error. Payload: %s", got, payload.Raw)
	}
	if got := payload.Get("error.type").String(); got != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error. Payload: %s", got, payload.Raw)
	}
	if got := payload.Get("error.message").String(); got != "This content was flagged for possible cybersecurity risk." {
		t.Fatalf("error.message = %q. Payload: %s", got, payload.Raw)
	}
}

func TestConvertCodexResponseToClaude_StreamErrorTypeFallbackMessage(t *testing.T) {
	ctx := context.Background()
	var param any

	outputs := ConvertCodexResponseToClaude(ctx, "", []byte(`{"messages":[]}`), nil, []byte(`data: {"type":"error","error":{},"error_type":"overloaded_error"}`), &param)
	if len(outputs) != 1 {
		t.Fatalf("expected one error chunk, got %d: %q", len(outputs), outputs)
	}

	payload, ok := firstClaudeStreamPayloadForEvent(string(outputs[0]), "error")
	if !ok {
		t.Fatalf("missing error event payload: %q", outputs[0])
	}
	if got := payload.Get("error.type").String(); got != "overloaded_error" {
		t.Fatalf("error.type = %q, want overloaded_error. Payload: %s", got, payload.Raw)
	}
	if got := payload.Get("error.message").String(); got != "overloaded_error" {
		t.Fatalf("error.message = %q, want overloaded_error. Payload: %s", got, payload.Raw)
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

// codexThinkingStreamDigest collects the thinking-related events produced by a Codex
// stream so tests can assert block/signature counts and the reassembled thinking text.
type codexThinkingStreamDigest struct {
	Starts     int
	Stops      int
	Signatures []string
	Thinking   string
	Raw        string
}

func digestCodexThinkingStream(t *testing.T, chunks [][]byte) codexThinkingStreamDigest {
	t.Helper()

	ctx := context.Background()
	originalRequest := []byte(`{"messages":[]}`)
	var param any

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}

	var digest codexThinkingStreamDigest
	var thinking strings.Builder
	var raw strings.Builder
	for _, out := range outputs {
		raw.Write(out)
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			switch data.Get("type").String() {
			case "content_block_start":
				if data.Get("content_block.type").String() == "thinking" {
					digest.Starts++
				}
			case "content_block_delta":
				switch data.Get("delta.type").String() {
				case "thinking_delta":
					thinking.WriteString(data.Get("delta.thinking").String())
				case "signature_delta":
					digest.Signatures = append(digest.Signatures, data.Get("delta.signature").String())
				}
			case "content_block_stop":
				digest.Stops++
			}
		}
	}
	digest.Thinking = thinking.String()
	digest.Raw = raw.String()

	return digest
}

func TestConvertCodexResponseToClaude_StreamThinkingKeepsSingleBlockAcrossSummaryParts(t *testing.T) {
	digest := digestCodexThinkingStream(t, [][]byte{
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"First part\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.done\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Second part\"}"),
	})

	if digest.Starts != 1 {
		t.Fatalf("expected a single thinking block start for one reasoning item, got %d", digest.Starts)
	}
	if digest.Stops != 0 {
		t.Fatalf("expected the thinking block to stay open until output_item.done, got %d stops", digest.Stops)
	}
	if want := "First part\n\nSecond part"; digest.Thinking != want {
		t.Fatalf("thinking text = %q, want %q", digest.Thinking, want)
	}
}

func TestConvertCodexResponseToClaude_StreamThinkingEmitsSingleSignatureAcrossMultipartReasoning(t *testing.T) {
	digest := digestCodexThinkingStream(t, [][]byte{
		[]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"enc_sig_multipart\"}}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"First part\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.done\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Second part\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.done\"}"),
		[]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\"}}"),
	})

	if digest.Starts != 1 || digest.Stops != 1 {
		t.Fatalf("expected exactly one thinking block, got %d starts and %d stops", digest.Starts, digest.Stops)
	}
	if len(digest.Signatures) != 1 {
		t.Fatalf("expected one signature_delta for one reasoning item, got %d: %v", len(digest.Signatures), digest.Signatures)
	}
	// output_item.done omitted encrypted_content here, so the pre-content fallback is expected.
	if digest.Signatures[0] != "enc_sig_multipart" {
		t.Fatalf("unexpected signature delta: %q", digest.Signatures[0])
	}
	if want := "First part\n\nSecond part"; digest.Thinking != want {
		t.Fatalf("thinking text = %q, want %q", digest.Thinking, want)
	}
}

// TestConvertCodexResponseToClaude_StreamThinkingNeverEmitsPreContentEncryptedContent guards the
// real-world shape earlier tests missed: output_item.added carries a fixed-size pre-content
// snapshot of encrypted_content that always differs from the final value on output_item.done.
// Emitting that snapshot makes the client replay bogus reasoning items for the rest of the session.
func TestConvertCodexResponseToClaude_StreamThinkingNeverEmitsPreContentEncryptedContent(t *testing.T) {
	digest := digestCodexThinkingStream(t, [][]byte{
		[]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"enc_sig_pre_content_snapshot\"}}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Part A\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.done\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Part B\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.done\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Part C\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.done\"}"),
		[]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"enc_sig_final\"}}"),
	})

	if digest.Starts != 1 || digest.Stops != 1 {
		t.Fatalf("expected one thinking block for one reasoning item with three summary parts, got %d starts and %d stops", digest.Starts, digest.Stops)
	}
	if len(digest.Signatures) != 1 || digest.Signatures[0] != "enc_sig_final" {
		t.Fatalf("expected exactly one signature_delta carrying the final encrypted_content, got %v", digest.Signatures)
	}
	if strings.Contains(digest.Raw, "enc_sig_pre_content_snapshot") {
		t.Fatal("pre-content encrypted_content snapshot leaked into the Claude stream")
	}
	if want := "Part A\n\nPart B\n\nPart C"; digest.Thinking != want {
		t.Fatalf("thinking text = %q, want %q", digest.Thinking, want)
	}
}

// TestConvertCodexResponseToClaude_StreamThinkingEmitsOneBlockPerReasoningItem checks that two
// consecutive reasoning items stay separate blocks, each signed with its own final value.
func TestConvertCodexResponseToClaude_StreamThinkingEmitsOneBlockPerReasoningItem(t *testing.T) {
	digest := digestCodexThinkingStream(t, [][]byte{
		[]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"enc_pre_1\"}}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"First item\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.done\"}"),
		[]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"enc_final_1\"}}"),
		[]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"enc_pre_2\"}}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.added\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Second item\"}"),
		[]byte("data: {\"type\":\"response.reasoning_summary_part.done\"}"),
		[]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"enc_final_2\"}}"),
	})

	if digest.Starts != 2 || digest.Stops != 2 {
		t.Fatalf("expected two thinking blocks for two reasoning items, got %d starts and %d stops", digest.Starts, digest.Stops)
	}
	if len(digest.Signatures) != 2 || digest.Signatures[0] != "enc_final_1" || digest.Signatures[1] != "enc_final_2" {
		t.Fatalf("expected each block signed with its own final encrypted_content, got %v", digest.Signatures)
	}
	if strings.Contains(digest.Raw, "enc_pre_1") || strings.Contains(digest.Raw, "enc_pre_2") {
		t.Fatal("pre-content encrypted_content snapshot leaked into the Claude stream")
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

func TestConvertCodexResponseToClaude_StreamTextBeforeToolCallsDoesNotEmitGhostStop(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[{"name":"Read","description":"read"}]}`)
	var param any

	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"grok-composer-2.5-fast"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","status":"in_progress"},"output_index":1}`),
		[]byte(`data: {"type":"response.content_part.added","part":{"type":"output_text"},"content_index":0,"output_index":1}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"查看项目的 README 和核心入口，以便准确说明项目用途。\n","output_index":1}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_a","name":"Read","status":"in_progress"},"output_index":2}`),
		[]byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"path\":\"/tmp/README.md\"}","output_index":2}`),
		[]byte(`data: {"type":"response.function_call_arguments.done","arguments":"{\"path\":\"/tmp/README.md\"}","output_index":2}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{\"path\":\"/tmp/README.md\"}"},"output_index":2}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_b","name":"Read","status":"in_progress"},"output_index":3}`),
		[]byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"path\":\"/tmp/main.go\"}","output_index":3}`),
		[]byte(`data: {"type":"response.content_part.done","part":{"type":"output_text"},"content_index":0,"output_index":1}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","status":"completed"},"output_index":1}`),
		[]byte(`data: {"type":"response.function_call_arguments.done","arguments":"{\"path\":\"/tmp/main.go\"}","output_index":3}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_b","name":"Read","arguments":"{\"path\":\"/tmp/main.go\"}"},"output_index":3}`),
		[]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}

	var startIndices []int64
	var stopIndices []int64
	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			switch data.Get("type").String() {
			case "content_block_start":
				startIndices = append(startIndices, data.Get("index").Int())
			case "content_block_stop":
				stopIndices = append(stopIndices, data.Get("index").Int())
			}
		}
	}

	if len(startIndices) != 3 {
		t.Fatalf("expected 3 content_block_start events (text + 2 tools), got %v", startIndices)
	}
	if len(stopIndices) != 3 {
		t.Fatalf("expected 3 content_block_stop events, got %v", stopIndices)
	}
	if startIndices[0] != 0 || startIndices[1] != 1 || startIndices[2] != 2 {
		t.Fatalf("unexpected start indices: %v", startIndices)
	}
	if stopIndices[0] != 0 || stopIndices[1] != 1 || stopIndices[2] != 2 {
		t.Fatalf("unexpected stop indices: %v", stopIndices)
	}
}

func TestConvertCodexResponseToClaude_StreamFunctionCallDefersStartUntilDoneName(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[{"name":"web_search","description":"search"}]}`)
	var param any

	_ = ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, []byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`), &param)
	addedOutputs := ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1"},"output_index":1}`), &param)
	argumentsOutputs := ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, []byte(`data: {"type":"response.function_call_arguments.done","arguments":"{\"query\":\"example\"}","output_index":1}`), &param)
	doneOutputs := ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"web_search","arguments":"{\"query\":\"example\"}"},"output_index":1}`), &param)

	if bytes.Contains(bytes.Join(addedOutputs, nil), []byte(`"content_block_start"`)) {
		t.Fatalf("function_call without name must not emit content_block_start: %q", addedOutputs)
	}
	if bytes.Contains(bytes.Join(argumentsOutputs, nil), []byte(`"input_json_delta"`)) {
		t.Fatalf("arguments must be buffered until the tool name is available: %q", argumentsOutputs)
	}

	var toolStartCount int
	var toolStopCount int
	var argumentDeltas []string
	for _, out := range doneOutputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			switch data.Get("type").String() {
			case "content_block_start":
				if data.Get("content_block.type").String() != "tool_use" {
					continue
				}
				toolStartCount++
				if got := data.Get("content_block.name").String(); got != "web_search" {
					t.Fatalf("unexpected tool_use name %q in %s", got, data.Raw)
				}
			case "content_block_delta":
				if data.Get("delta.type").String() == "input_json_delta" {
					argumentDeltas = append(argumentDeltas, data.Get("delta.partial_json").String())
				}
			case "content_block_stop":
				toolStopCount++
			}
		}
	}

	if toolStartCount != 1 {
		t.Fatalf("expected one deferred tool_use start, got %d in %q", toolStartCount, doneOutputs)
	}
	if len(argumentDeltas) != 1 || argumentDeltas[0] != `{"query":"example"}` {
		t.Fatalf("unexpected buffered argument deltas: %v", argumentDeltas)
	}
	if toolStopCount != 1 {
		t.Fatalf("expected one deferred tool_use stop, got %d in %q", toolStopCount, doneOutputs)
	}
}

func TestConvertCodexResponseToClaude_StreamUnnamedFunctionCallDoneByCallIDKeepsPendingSlots(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[{"name":"lookup","description":"lookup"}]}`)
	var param any

	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_first"},"output_index":1}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_second"},"output_index":2}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_first","name":"lookup","arguments":"{\"id\":1}"}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_second","name":"lookup","arguments":"{\"id\":2}"}}`),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}

	var toolIDs []string
	var startIndices []int64
	var stopIndices []int64
	var argumentDeltas []string
	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			switch data.Get("type").String() {
			case "content_block_start":
				if data.Get("content_block.type").String() == "tool_use" {
					toolIDs = append(toolIDs, data.Get("content_block.id").String())
					startIndices = append(startIndices, data.Get("index").Int())
				}
			case "content_block_delta":
				if data.Get("delta.type").String() == "input_json_delta" {
					argumentDeltas = append(argumentDeltas, data.Get("delta.partial_json").String())
				}
			case "content_block_stop":
				stopIndices = append(stopIndices, data.Get("index").Int())
			}
		}
	}

	if len(toolIDs) != 2 || toolIDs[0] != "call_first" || toolIDs[1] != "call_second" {
		t.Fatalf("unexpected tool IDs: %v; outputs=%q", toolIDs, outputs)
	}
	if len(startIndices) != 2 || startIndices[0] != 0 || startIndices[1] != 1 {
		t.Fatalf("unexpected start indices: %v; outputs=%q", startIndices, outputs)
	}
	if len(stopIndices) != 2 || stopIndices[0] != 0 || stopIndices[1] != 1 {
		t.Fatalf("unexpected stop indices: %v; outputs=%q", stopIndices, outputs)
	}
	if len(argumentDeltas) != 2 || argumentDeltas[0] != `{"id":1}` || argumentDeltas[1] != `{"id":2}` {
		t.Fatalf("unexpected argument deltas: %v; outputs=%q", argumentDeltas, outputs)
	}
}

func TestConvertCodexResponseToClaude_StreamDeferredUnnamedFunctionCallDoesNotReserveBlockIndex(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[{"name":"lookup","description":"lookup"}]}`)
	var param any

	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_hidden"},"output_index":1}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]},"output_index":2}`),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}

	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if data.Get("type").String() == "content_block_start" && data.Get("content_block.type").String() == "text" {
				if got := data.Get("index").Int(); got != 0 {
					t.Fatalf("text block index = %d, want 0; outputs=%q", got, outputs)
				}
				return
			}
		}
	}

	t.Fatalf("missing text content_block_start; outputs=%q", outputs)
}

func TestConvertCodexResponseToClaude_StreamTerminalOutputHydratesOpenFunctionCallArguments(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[{"name":"lookup","description":"lookup"}]}`)
	var param any

	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","name":"lookup"},"output_index":1}`),
		[]byte(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"query\":\"example\"}"}]}}`),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}

	var finalArgumentPosition = -1
	var stopPosition = -1
	var messageDeltaPosition = -1
	position := 0
	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			position++
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			switch data.Get("type").String() {
			case "content_block_delta":
				if data.Get("delta.type").String() == "input_json_delta" && data.Get("delta.partial_json").String() == `{"query":"example"}` {
					finalArgumentPosition = position
				}
			case "content_block_stop":
				if data.Get("index").Int() == 0 {
					stopPosition = position
				}
			case "message_delta":
				messageDeltaPosition = position
			}
		}
	}

	if finalArgumentPosition == -1 {
		t.Fatalf("missing terminal argument delta; outputs=%q", outputs)
	}
	if stopPosition == -1 {
		t.Fatalf("missing content_block_stop for open function call; outputs=%q", outputs)
	}
	if messageDeltaPosition == -1 {
		t.Fatalf("missing message_delta; outputs=%q", outputs)
	}
	if !(finalArgumentPosition < stopPosition && stopPosition < messageDeltaPosition) {
		t.Fatalf("unexpected event order: args=%d stop=%d message_delta=%d; outputs=%q", finalArgumentPosition, stopPosition, messageDeltaPosition, outputs)
	}
}

func TestConvertCodexResponseToClaude_StreamTerminalOutputEmitsPendingUnnamedFunctionCall(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[{"name":"lookup","description":"lookup"}]}`)
	var param any

	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1"},"output_index":1}`),
		[]byte(`data: {"type":"response.function_call_arguments.done","arguments":"{\"query\":\"example\"}","output_index":1}`),
		[]byte(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"query\":\"example\"}"}]}}`),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}
	outputText := string(bytes.Join(outputs, nil))

	if strings.Count(outputText, `"type":"tool_use"`) != 1 {
		t.Fatalf("expected one terminal tool_use block, got output:\n%s", outputText)
	}
	if !strings.Contains(outputText, `"name":"lookup"`) || !strings.Contains(outputText, `"partial_json":"{\"query\":\"example\"}"`) {
		t.Fatalf("expected terminal tool name and arguments, got output:\n%s", outputText)
	}
	gotReason, ok := findClaudeStreamStopReason(outputs)
	if !ok {
		t.Fatalf("missing message_delta; outputs=%q", outputs)
	}
	if gotReason != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use. Outputs=%q", gotReason, outputs)
	}
	toolUsePosition := strings.Index(outputText, `"type":"tool_use"`)
	messageDeltaPosition := strings.Index(outputText, `"type":"message_delta"`)
	if toolUsePosition < 0 || messageDeltaPosition < 0 || toolUsePosition > messageDeltaPosition {
		t.Fatalf("terminal tool_use must be emitted before message_delta:\n%s", outputText)
	}
}

func TestConvertCodexResponseToClaude_StreamUnresolvedPendingFunctionCallDoesNotForceToolUseStopReason(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[{"name":"lookup","description":"lookup"}]}`)
	var param any

	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_hidden"},"output_index":1}`),
		[]byte(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1},"output":[]}}`),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}
	outputText := string(bytes.Join(outputs, nil))

	if strings.Contains(outputText, `"type":"tool_use"`) {
		t.Fatalf("unresolved pending function_call must not emit tool_use:\n%s", outputText)
	}
	gotReason, ok := findClaudeStreamStopReason(outputs)
	if !ok {
		t.Fatalf("missing message_delta; outputs=%q", outputs)
	}
	if gotReason != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn. Outputs=%q", gotReason, outputs)
	}
	params, ok := param.(*ConvertCodexResponseToClaudeParams)
	if !ok || len(params.FunctionCalls) != 0 || len(params.FunctionCallQueue) != 0 || params.LastFunctionCall != nil {
		t.Fatalf("pending function calls were not cleared: %#v", param)
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

func TestConvertCodexResponseToClaude_StreamWebSearchCallEmitsClaudeServerToolBlocks(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{
		"tools":[{"type":"web_search_20250305","name":"web_search"}],
		"messages":[{"role":"user","content":"search weather"}]
	}`)
	var param any

	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"id":"ws_123","type":"web_search_call","status":"in_progress"}}`),
		[]byte(`data: {"type":"response.web_search_call.searching","item_id":"ws_123"}`),
		[]byte(`data: {"type":"response.web_search_call.completed","item_id":"ws_123"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"id":"ws_123","type":"web_search_call","status":"completed","action":{"type":"search","query":"search weather"}}}`),
		[]byte(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":{"input_tokens":3,"output_tokens":2}}}`),
	}
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}
	outputText := string(bytes.Join(outputs, nil))

	for _, needle := range []string{
		`"type":"server_tool_use"`,
		`"id":"ws_123"`,
		`"type":"web_search_tool_result"`,
		`event: message_stop`,
	} {
		if !strings.Contains(outputText, needle) {
			t.Fatalf("stream output missing %s:\n%s", needle, outputText)
		}
	}
	serverToolIndex := strings.Index(outputText, `"type":"server_tool_use"`)
	resultIndex := strings.Index(outputText, `"type":"web_search_tool_result"`)
	if serverToolIndex < 0 || resultIndex < 0 || resultIndex < serverToolIndex {
		t.Fatalf("web_search_tool_result must follow server_tool_use:\n%s", outputText)
	}
	if !strings.Contains(outputText, `partial_json`) || !strings.Contains(outputText, "search weather") {
		t.Fatalf("expected web search query delta after populated output_item.done:\n%s", outputText)
	}
}

func TestConvertCodexResponseToClaude_StreamWebSearchCallReusesFallbackToolUseID(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"search weather"}]}`)
	var param any

	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"web_search_call","status":"in_progress"}}`),
		[]byte(`data: {"type":"response.web_search_call.completed","item_id":"ws_from_upstream"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"id":"ws_from_upstream","type":"web_search_call","status":"completed","action":{"type":"search","query":"search weather"}}}`),
		[]byte(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":{"input_tokens":3,"output_tokens":2}}}`),
	}
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}
	outputText := string(bytes.Join(outputs, nil))

	if strings.Count(outputText, `"type":"server_tool_use"`) != 1 {
		t.Fatalf("expected exactly one server_tool_use block, got output:\n%s", outputText)
	}
	if !strings.Contains(outputText, `"tool_use_id":"ws_from_upstream"`) {
		t.Fatalf("expected web_search_tool_result to reuse fallback tool_use_id:\n%s", outputText)
	}
}

func TestConvertCodexResponseToClaude_ShortensLongToolUseIDs(t *testing.T) {
	longCallID := "call_" + strings.Repeat("a", 62)
	if len(longCallID) <= 64 {
		t.Fatalf("test setup error: longCallID length = %d, want > 64", len(longCallID))
	}

	t.Run("stream", func(t *testing.T) {
		ctx := context.Background()
		originalRequest := []byte(`{"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{}}}]}`)
		var param any

		outputs := ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"`+longCallID+`","name":"lookup"}}`), &param)

		toolID := ""
		for _, out := range outputs {
			for _, line := range strings.Split(string(out), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				data := gjson.Parse(strings.TrimPrefix(line, "data: "))
				if data.Get("type").String() == "content_block_start" && data.Get("content_block.type").String() == "tool_use" {
					toolID = data.Get("content_block.id").String()
				}
			}
		}

		if toolID == "" {
			t.Fatalf("missing stream tool_use block. Outputs=%q", outputs)
		}
		if len(toolID) > 64 {
			t.Fatalf("stream tool_use id length = %d, want <= 64: %q", len(toolID), toolID)
		}
		if toolID == longCallID {
			t.Fatalf("stream tool_use id was not shortened: %q", toolID)
		}
	})

	t.Run("nonstream", func(t *testing.T) {
		ctx := context.Background()
		originalRequest := []byte(`{"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{}}}]}`)
		response := []byte(`{
			"type":"response.completed",
			"response":{
				"id":"resp_1",
				"model":"gpt-5",
				"usage":{"input_tokens":1,"output_tokens":1},
				"output":[{"type":"function_call","call_id":"` + longCallID + `","name":"lookup","arguments":"{}"}]
			}
		}`)

		out := ConvertCodexResponseToClaudeNonStream(ctx, "", originalRequest, nil, response, nil)
		toolID := gjson.GetBytes(out, "content.0.id").String()
		if toolID == "" {
			t.Fatalf("missing nonstream tool_use id. Output: %s", string(out))
		}
		if len(toolID) > 64 {
			t.Fatalf("nonstream tool_use id length = %d, want <= 64: %q", len(toolID), toolID)
		}
		if toolID == longCallID {
			t.Fatalf("nonstream tool_use id was not shortened: %q", toolID)
		}
	})
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

func TestConvertCodexResponseToClaudeNonStream_WebSearchCallEmitsServerToolBlocks(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"search weather"}]}`)
	response := []byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.3-codex-spark","stop_reason":"stop","usage":{"input_tokens":3,"output_tokens":2},"output":[{"type":"web_search_call","id":"ws_123","status":"completed","action":{"type":"search","query":"search weather"}},{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}`)
	out := ConvertCodexResponseToClaudeNonStream(ctx, "", originalRequest, nil, response, nil)
	parsed := gjson.ParseBytes(out)
	types := []string{}
	parsed.Get("content").ForEach(func(_, value gjson.Result) bool {
		types = append(types, value.Get("type").String())
		return true
	})
	for _, want := range []string{"server_tool_use", "web_search_tool_result", "text"} {
		found := false
		for _, got := range types {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			found = strings.Contains(string(out), `"type":"`+want+`"`)
		}
		if !found {
			t.Fatalf("missing content type %s in %s", want, string(out))
		}
	}
	if parsed.Get("content.0.input.query").String() != "search weather" {
		if !strings.Contains(string(out), "search weather") {
			t.Fatalf("expected web search query in non-stream output: %s", string(out))
		}
	}
}

func TestConvertCodexResponseToClaudeNonStream_WebSearchStopReasonEndTurn(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"search weather"}]}`)
	response := []byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.3-codex-spark","stop_reason":"stop","usage":{"input_tokens":3,"output_tokens":2},"output":[{"type":"web_search_call","id":"ws_123","status":"completed","action":{"type":"search","query":"search weather"}},{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}`)
	out := ConvertCodexResponseToClaudeNonStream(ctx, "", originalRequest, nil, response, nil)
	parsed := gjson.ParseBytes(out)
	if got := parsed.Get("stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn when only server web_search and text are present", got)
	}
}

func TestConvertCodexResponseToClaudeNonStream_WebSearchDedupesEmptyOpenPageItems(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"q"}]}`)
	response := []byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.3-codex-spark","stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"open_page"}},{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"weather"}},{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`)
	out := ConvertCodexResponseToClaudeNonStream(ctx, "", originalRequest, nil, response, nil)
	if strings.Count(string(out), `"type":"server_tool_use"`) != 1 {
		t.Fatalf("expected one server_tool_use after dedupe, got %s", string(out))
	}
	if !strings.Contains(string(out), "weather") {
		t.Fatalf("expected populated query item to be kept: %s", string(out))
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

func firstClaudeStreamPayloadForEvent(output, event string) (gjson.Result, bool) {
	var currentEvent string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}
		if currentEvent != event || !strings.HasPrefix(line, "data: ") {
			continue
		}
		return gjson.Parse(strings.TrimPrefix(line, "data: ")), true
	}
	return gjson.Result{}, false
}

func TestConvertCodexResponseToClaude_StreamPreservesCacheWriteUsage(t *testing.T) {
	tests := []struct {
		name                 string
		terminalUsageJSON    string
		wantInputTokens      int64
		wantOutputTokens     int64
		wantCacheReadTokens  int64
		wantCacheWriteTokens int64
	}{
		{
			name:                 "cache_write_tokens field",
			terminalUsageJSON:    `{"input_tokens":1000,"output_tokens":200,"input_tokens_details":{"cached_tokens":800,"cache_write_tokens":150}}`,
			wantInputTokens:      200,
			wantOutputTokens:     200,
			wantCacheReadTokens:  800,
			wantCacheWriteTokens: 150,
		},
		{
			name:                 "cache_creation_tokens field alias",
			terminalUsageJSON:    `{"input_tokens":1000,"output_tokens":200,"input_tokens_details":{"cached_tokens":800,"cache_creation_tokens":150}}`,
			wantInputTokens:      200,
			wantOutputTokens:     200,
			wantCacheReadTokens:  800,
			wantCacheWriteTokens: 150,
		},
		{
			name:                 "cached_tokens greater than input_tokens clamps input_tokens to zero",
			terminalUsageJSON:    `{"input_tokens":500,"output_tokens":100,"input_tokens_details":{"cached_tokens":800,"cache_write_tokens":50}}`,
			wantInputTokens:      0,
			wantOutputTokens:     100,
			wantCacheReadTokens:  800,
			wantCacheWriteTokens: 50,
		},
		{
			name:                 "zero cache_write_tokens does not emit cache_creation_input_tokens",
			terminalUsageJSON:    `{"input_tokens":1000,"output_tokens":200,"input_tokens_details":{"cached_tokens":800,"cache_write_tokens":0}}`,
			wantInputTokens:      200,
			wantOutputTokens:     200,
			wantCacheReadTokens:  800,
			wantCacheWriteTokens: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			originalRequest := []byte(`{"messages":[]}`)
			var param any

			chunks := [][]byte{
				[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`),
				[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`),
				[]byte(fmt.Sprintf(`data: {"type":"response.completed","response":{"stop_reason":"stop","usage":%s}}`, tt.terminalUsageJSON)),
			}

			var outputs [][]byte
			for _, chunk := range chunks {
				outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
			}

			delta, ok := findClaudeStreamMessageDelta(outputs)
			if !ok {
				t.Fatalf("missing message_delta event; outputs=%q", outputs)
			}

			usage := delta.Get("usage")
			if got := usage.Get("input_tokens").Int(); got != tt.wantInputTokens {
				t.Fatalf("input_tokens = %d, want %d", got, tt.wantInputTokens)
			}
			if got := usage.Get("output_tokens").Int(); got != tt.wantOutputTokens {
				t.Fatalf("output_tokens = %d, want %d", got, tt.wantOutputTokens)
			}
			if got := usage.Get("cache_read_input_tokens").Int(); got != tt.wantCacheReadTokens {
				t.Fatalf("cache_read_input_tokens = %d, want %d", got, tt.wantCacheReadTokens)
			}
			if tt.wantCacheWriteTokens == 0 {
				if usage.Get("cache_creation_input_tokens").Exists() {
					t.Fatalf("cache_creation_input_tokens should not be emitted when zero; got %v", usage.Get("cache_creation_input_tokens").Raw)
				}
			} else if got := usage.Get("cache_creation_input_tokens").Int(); got != tt.wantCacheWriteTokens {
				t.Fatalf("cache_creation_input_tokens = %d, want %d", got, tt.wantCacheWriteTokens)
			}
		})
	}
}

func TestConvertCodexResponseToClaudeNonStream_PreservesCacheWriteUsage(t *testing.T) {
	tests := []struct {
		name                 string
		responseJSON         string
		wantInputTokens      int64
		wantOutputTokens     int64
		wantCacheReadTokens  int64
		wantCacheWriteTokens int64
	}{
		{
			name: "cache_write_tokens field",
			responseJSON: `{
				"type":"response.completed",
				"response":{
					"id":"resp_1",
					"model":"gpt-5",
					"stop_reason":"stop",
					"usage":{"input_tokens":1000,"output_tokens":200,"input_tokens_details":{"cached_tokens":800,"cache_write_tokens":150}},
					"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]
				}
			}`,
			wantInputTokens:      200,
			wantOutputTokens:     200,
			wantCacheReadTokens:  800,
			wantCacheWriteTokens: 150,
		},
		{
			name: "cache_creation_tokens alias",
			responseJSON: `{
				"type":"response.completed",
				"response":{
					"id":"resp_1",
					"model":"gpt-5",
					"stop_reason":"stop",
					"usage":{"input_tokens":1000,"output_tokens":200,"input_tokens_details":{"cached_tokens":800,"cache_creation_tokens":150}},
					"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]
				}
			}`,
			wantInputTokens:      200,
			wantOutputTokens:     200,
			wantCacheReadTokens:  800,
			wantCacheWriteTokens: 150,
		},
		{
			name: "cached_tokens greater than input_tokens clamps input_tokens to zero",
			responseJSON: `{
				"type":"response.completed",
				"response":{
					"id":"resp_1",
					"model":"gpt-5",
					"stop_reason":"stop",
					"usage":{"input_tokens":500,"output_tokens":100,"input_tokens_details":{"cached_tokens":800,"cache_write_tokens":50}},
					"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]
				}
			}`,
			wantInputTokens:      0,
			wantOutputTokens:     100,
			wantCacheReadTokens:  800,
			wantCacheWriteTokens: 50,
		},
		{
			name: "zero cache_write_tokens does not emit cache_creation_input_tokens",
			responseJSON: `{
				"type":"response.completed",
				"response":{
					"id":"resp_1",
					"model":"gpt-5",
					"stop_reason":"stop",
					"usage":{"input_tokens":1000,"output_tokens":200,"input_tokens_details":{"cached_tokens":800,"cache_write_tokens":0}},
					"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]
				}
			}`,
			wantInputTokens:      200,
			wantOutputTokens:     200,
			wantCacheReadTokens:  800,
			wantCacheWriteTokens: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			originalRequest := []byte(`{"messages":[]}`)
			out := ConvertCodexResponseToClaudeNonStream(ctx, "", originalRequest, nil, []byte(tt.responseJSON), nil)
			parsed := gjson.ParseBytes(out)

			usage := parsed.Get("usage")
			if got := usage.Get("input_tokens").Int(); got != tt.wantInputTokens {
				t.Fatalf("input_tokens = %d, want %d", got, tt.wantInputTokens)
			}
			if got := usage.Get("output_tokens").Int(); got != tt.wantOutputTokens {
				t.Fatalf("output_tokens = %d, want %d", got, tt.wantOutputTokens)
			}
			if got := usage.Get("cache_read_input_tokens").Int(); got != tt.wantCacheReadTokens {
				t.Fatalf("cache_read_input_tokens = %d, want %d", got, tt.wantCacheReadTokens)
			}
			if tt.wantCacheWriteTokens == 0 {
				if usage.Get("cache_creation_input_tokens").Exists() {
					t.Fatalf("cache_creation_input_tokens should not be emitted when zero; got %v", usage.Get("cache_creation_input_tokens").Raw)
				}
			} else if got := usage.Get("cache_creation_input_tokens").Int(); got != tt.wantCacheWriteTokens {
				t.Fatalf("cache_creation_input_tokens = %d, want %d", got, tt.wantCacheWriteTokens)
			}
		})
	}
}
