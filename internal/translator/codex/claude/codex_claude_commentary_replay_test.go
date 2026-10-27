package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// The internal commentary marker is an implementation contract between the
// Codex-to-Claude response translator and the Claude-to-Codex request
// translator. It must never be forwarded to an upstream as a signature.
const testCommentaryMarkerSignature = codexCommentarySignatureMarker

func TestConvertCodexResponseToClaudeNonStream_CommentaryCarriesInternalMarker(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5","output":[
		{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"COMMENTARY_SENTINEL"}]},
		{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"FINAL_SENTINEL"}]}
	],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	var param any
	out := ConvertCodexResponseToClaudeNonStream(context.Background(), "", []byte(`{"messages":[]}`), nil, raw, &param)
	content := gjson.GetBytes(out, "content").Array()
	if len(content) != 2 {
		t.Fatalf("expected 2 content blocks, got %s", out)
	}
	if content[0].Get("type").String() != "thinking" || content[0].Get("thinking").String() != "COMMENTARY_SENTINEL" {
		t.Fatalf("commentary should be a thinking block, got %s", content[0].Raw)
	}
	if got := content[0].Get("signature").String(); got != testCommentaryMarkerSignature {
		t.Fatalf("commentary thinking block signature = %q, want internal marker %q", got, testCommentaryMarkerSignature)
	}
	if content[1].Get("type").String() != "text" || content[1].Get("text").String() != "FINAL_SENTINEL" {
		t.Fatalf("final_answer should be a text block, got %s", content[1].Raw)
	}
}

func TestConvertCodexResponseToClaude_StreamCommentaryCarriesInternalMarker(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"commentary"}}`),
		[]byte(`data: {"type":"response.content_part.added","part":{"type":"output_text"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"COMMENTARY_SENTINEL"}`),
		[]byte(`data: {"type":"response.content_part.done","part":{"type":"output_text","text":"COMMENTARY_SENTINEL"}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"COMMENTARY_SENTINEL"}]}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"final_answer"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"FINAL_SENTINEL"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"FINAL_SENTINEL"}]}}`),
		[]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	got := collectCodexClaudeBlocks(t, chunks)
	if len(got.thinking) != 1 || got.thinking[0] != "COMMENTARY_SENTINEL" {
		t.Fatalf("commentary should be one thinking block, got %q", got.thinking)
	}
	if len(got.signatures) != 1 || got.signatures[0] != testCommentaryMarkerSignature {
		t.Fatalf("commentary should carry exactly the internal marker signature, got %v", got.signatures)
	}
	if len(got.text) != 1 || got.text[0] != "FINAL_SENTINEL" {
		t.Fatalf("final_answer should be the only text block, got %q", got.text)
	}
}

func TestConvertClaudeRequestToCodex_CommentaryMarkerRestoresCommentaryMessage(t *testing.T) {
	payload := []byte(`{"model":"gpt-6.1-sol","max_tokens":128,"messages":[
		{"role":"user","content":"First turn"},
		{"role":"assistant","content":[
			{"type":"thinking","thinking":"COMMENTARY_SENTINEL","signature":"` + testCommentaryMarkerSignature + `"},
			{"type":"text","text":"FINAL_SENTINEL"}
		]},
		{"role":"user","content":"Next turn"}
	]}`)

	for _, stream := range []bool{false, true} {
		result, errConvert := ConvertClaudeRequestToCodex("gpt-6.1-sol", payload, stream)
		if errConvert != nil {
			t.Fatalf("stream=%v: conversion error: %v", stream, errConvert)
		}
		if strings.Contains(string(result), testCommentaryMarkerSignature) {
			t.Fatalf("stream=%v: internal marker leaked into upstream request: %s", stream, result)
		}
		inputs := gjson.GetBytes(result, "input").Array()
		if len(inputs) != 4 {
			t.Fatalf("stream=%v: got %d input items, want 4: %s", stream, len(inputs), result)
		}
		commentary := inputs[1]
		if commentary.Get("type").String() != "message" || commentary.Get("role").String() != "assistant" || commentary.Get("phase").String() != "commentary" {
			t.Fatalf("stream=%v: second input should be an assistant commentary message, got %s", stream, commentary.Raw)
		}
		if got := commentary.Get("content.0.type").String(); got != "output_text" {
			t.Fatalf("stream=%v: commentary content type = %q, want output_text", stream, got)
		}
		if got := commentary.Get("content.0.text").String(); got != "COMMENTARY_SENTINEL" {
			t.Fatalf("stream=%v: commentary text = %q, want COMMENTARY_SENTINEL", stream, got)
		}
		final := inputs[2]
		if final.Get("type").String() != "message" || final.Get("role").String() != "assistant" || final.Get("phase").Exists() {
			t.Fatalf("stream=%v: third input should be a plain assistant message, got %s", stream, final.Raw)
		}
		if got := final.Get("content.0.text").String(); got != "FINAL_SENTINEL" {
			t.Fatalf("stream=%v: final text = %q, want FINAL_SENTINEL", stream, got)
		}
		for _, item := range inputs {
			if item.Get("type").String() == "reasoning" {
				t.Fatalf("stream=%v: commentary must not become a reasoning item: %s", stream, result)
			}
		}
	}
}

func TestConvertClaudeRequestToCodex_CommentaryMarkerBeforeToolUseSurvivesToolRoundTrip(t *testing.T) {
	payload := []byte(`{"model":"gpt-6.1-sol","messages":[
		{"role":"user","content":"First turn"},
		{"role":"assistant","content":[
			{"type":"thinking","thinking":"COMMENTARY_SENTINEL","signature":"` + testCommentaryMarkerSignature + `"},
			{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}
		]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}
	]}`)

	result, errConvert := ConvertClaudeRequestToCodex("gpt-6.1-sol", payload, false)
	if errConvert != nil {
		t.Fatalf("conversion error: %v", errConvert)
	}
	if strings.Contains(string(result), testCommentaryMarkerSignature) {
		t.Fatalf("internal marker leaked into upstream request: %s", result)
	}
	inputs := gjson.GetBytes(result, "input").Array()
	if len(inputs) != 4 {
		t.Fatalf("got %d input items, want 4: %s", len(inputs), result)
	}
	if inputs[1].Get("phase").String() != "commentary" || inputs[1].Get("content.0.text").String() != "COMMENTARY_SENTINEL" {
		t.Fatalf("commentary message missing before tool call: %s", result)
	}
	if inputs[2].Get("type").String() != "function_call" {
		t.Fatalf("third input should be function_call, got %s", inputs[2].Raw)
	}
	if inputs[3].Get("type").String() != "function_call_output" {
		t.Fatalf("fourth input should be function_call_output, got %s", inputs[3].Raw)
	}
}

func TestConvertClaudeRequestToCodex_CommentaryMarkerRoundTripFromResponse(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5","output":[
		{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"COMMENTARY_SENTINEL"}]},
		{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"FINAL_SENTINEL"}]}
	],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	var param any
	claudeResponse := ConvertCodexResponseToClaudeNonStream(context.Background(), "", []byte(`{"messages":[]}`), nil, raw, &param)
	content := gjson.GetBytes(claudeResponse, "content").Raw

	replay := []byte(`{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"First turn"},{"role":"assistant","content":` + content + `},{"role":"user","content":"Next turn"}]}`)
	result, errConvert := ConvertClaudeRequestToCodex("gpt-6.1-sol", replay, false)
	if errConvert != nil {
		t.Fatalf("conversion error: %v", errConvert)
	}
	if !strings.Contains(string(result), "COMMENTARY_SENTINEL") || !strings.Contains(string(result), "FINAL_SENTINEL") {
		t.Fatalf("round trip lost assistant history: %s", result)
	}
	if strings.Contains(string(result), testCommentaryMarkerSignature) {
		t.Fatalf("internal marker leaked into upstream request: %s", result)
	}
}

func TestConvertClaudeRequestToCodexWithCompat_CommentaryMarkerRestoresTextNotReasoning(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"COMMENTARY_SENTINEL","signature":"` + testCommentaryMarkerSignature + `"}]}]}`)

	result, errConvert := ConvertClaudeRequestToCodexWithCompat("deepseek-v4", payload, false)
	if errConvert != nil {
		t.Fatalf("conversion error: %v", errConvert)
	}
	if strings.Contains(string(result), testCommentaryMarkerSignature) {
		t.Fatalf("internal marker leaked into upstream request: %s", result)
	}
	inputs := gjson.GetBytes(result, "input").Array()
	if len(inputs) != 1 {
		t.Fatalf("got %d input items, want 1: %s", len(inputs), result)
	}
	if inputs[0].Get("type").String() != "message" || inputs[0].Get("phase").String() != "commentary" || inputs[0].Get("content.0.text").String() != "COMMENTARY_SENTINEL" {
		t.Fatalf("compat replay should restore a commentary message, got %s", inputs[0].Raw)
	}
}
