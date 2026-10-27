package claude

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiResponseToClaude_VertexStreamingPartialArgs(t *testing.T) {
	ctx := context.Background()
	var param any
	requestJSON := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"weather?"}],"tools":[{"name":"get_weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]}`)
	chunks := [][]byte{
		[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"New ","willContinue":true}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-claude-partial-1"}`),
		[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.city","stringValue":"York"}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-claude-partial-1"}`),
		[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-2.5-pro","responseId":"resp-claude-partial-1"}`),
		[]byte("[DONE]"),
	}

	var partialJSON strings.Builder
	var firstChunkDelta string
	for chunkIdx, chunk := range chunks {
		outs := ConvertGeminiResponseToClaude(ctx, "gemini-2.5-pro", requestJSON, requestJSON, chunk, &param)
		raw := string(bytes.Join(outs, nil))
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if gjson.Get(payload, "type").String() == "content_block_delta" && gjson.Get(payload, "delta.type").String() == "input_json_delta" {
				piece := gjson.Get(payload, "delta.partial_json").String()
				partialJSON.WriteString(piece)
				if chunkIdx == 0 {
					firstChunkDelta += piece
				}
			}
		}
	}
	if !strings.Contains(firstChunkDelta, "New ") {
		t.Fatalf("expected first chunk to stream 'New ' in input_json_delta, got %q (total=%q)", firstChunkDelta, partialJSON.String())
	}
	if got := partialJSON.String(); got != `{"city":"New York"}` {
		t.Fatalf("concatenated Claude partial_json = %q, want %q", got, `{"city":"New York"}`)
	}

	t.Run("multiple same-name calls and interrupted stream", func(t *testing.T) {
		var p any
		multiChunks := [][]byte{
			[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"Par","willContinue":true}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-claude-multi"}`),
			[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.city","stringValue":"is"}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-claude-multi"}`),
			[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{}},{"functionCall":{"name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"Lyon"}],"willContinue":false}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-2.5-pro","responseId":"resp-claude-multi"}`),
			[]byte("[DONE]"),
		}
		byBlock := make(map[int64]*strings.Builder)
		var toolIDs []string
		for _, ch := range multiChunks {
			outs := ConvertGeminiResponseToClaude(ctx, "gemini-2.5-pro", requestJSON, requestJSON, ch, &p)
			for _, line := range strings.Split(string(bytes.Join(outs, nil)), "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, "data:") {
					continue
				}
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				switch gjson.Get(payload, "type").String() {
				case "content_block_start":
					if gjson.Get(payload, "content_block.type").String() == "tool_use" {
						toolIDs = append(toolIDs, gjson.Get(payload, "content_block.id").String())
					}
				case "content_block_delta":
					if gjson.Get(payload, "delta.type").String() == "input_json_delta" {
						idx := gjson.Get(payload, "index").Int()
						if byBlock[idx] == nil {
							byBlock[idx] = &strings.Builder{}
						}
						byBlock[idx].WriteString(gjson.Get(payload, "delta.partial_json").String())
					}
				}
			}
		}
		if len(toolIDs) != 2 || toolIDs[0] == toolIDs[1] {
			t.Fatalf("expected 2 distinct tool_use blocks, got %v", toolIDs)
		}
		if got0 := byBlock[0].String(); got0 != `{"city":"Paris"}` {
			t.Fatalf("block 0 partial_json = %q, want %q", got0, `{"city":"Paris"}`)
		}
		if got1 := byBlock[1].String(); got1 != `{"city":"Lyon"}` {
			t.Fatalf("block 1 partial_json = %q, want %q", got1, `{"city":"Lyon"}`)
		}

		// Interrupted stream should not emit message_stop or content_block_stop on [DONE].
		var pTrunc any
		truncOut := ConvertGeminiResponseToClaude(ctx, "gemini-2.5-pro", requestJSON, requestJSON, multiChunks[0], &pTrunc)
		truncOut = append(truncOut, ConvertGeminiResponseToClaude(ctx, "gemini-2.5-pro", requestJSON, requestJSON, []byte("[DONE]"), &pTrunc)...)
		truncText := string(bytes.Join(truncOut, nil))
		if strings.Contains(truncText, `"type":"message_stop"`) || strings.Contains(truncText, `"type":"content_block_stop"`) {
			t.Fatalf("interrupted stream must not emit stop events: %s", truncText)
		}
	})

	t.Run("standalone thoughtSignature during streaming tool call does not split block", func(t *testing.T) {
		var p any
		sigChunks := [][]byte{
			[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"Ber","willContinue":true}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-claude-sig"}`),
			[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":""}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-claude-sig"}`),
			[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"","thoughtSignature":"sig-mid-1"}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-claude-sig"}`),
			[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"lin"}],"willContinue":true},"thoughtSignature":"sig-mid-2"}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-claude-sig"}`),
			[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-2.5-pro","responseId":"resp-claude-sig"}`),
			[]byte("[DONE]"),
		}
		var block0JSON strings.Builder
		var deferredSigs []string
		var toolStopped bool
		for _, ch := range sigChunks {
			outs := ConvertGeminiResponseToClaude(ctx, "gemini-2.5-pro", requestJSON, requestJSON, ch, &p)
			for _, line := range strings.Split(string(bytes.Join(outs, nil)), "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, "data:") {
					continue
				}
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				switch gjson.Get(payload, "type").String() {
				case "content_block_delta":
					if gjson.Get(payload, "delta.type").String() == "input_json_delta" {
						if gotIdx := gjson.Get(payload, "index").Int(); gotIdx != 0 {
							t.Fatalf("input_json_delta index = %d, want 0; payload=%s", gotIdx, payload)
						}
						if toolStopped {
							t.Fatalf("input_json_delta emitted after tool block stopped: %s", payload)
						}
						block0JSON.WriteString(gjson.Get(payload, "delta.partial_json").String())
					} else if gjson.Get(payload, "delta.type").String() == "signature_delta" {
						if !toolStopped {
							t.Fatalf("signature_delta emitted before tool block finished: %s", payload)
						}
						deferredSigs = append(deferredSigs, gjson.Get(payload, "delta.signature").String())
					}
				case "content_block_stop":
					if gjson.Get(payload, "index").Int() == 0 {
						toolStopped = true
					}
				}
			}
		}
		if got := block0JSON.String(); got != `{"city":"Berlin"}` {
			t.Fatalf("block 0 JSON = %q, want %q", got, `{"city":"Berlin"}`)
		}
		if len(deferredSigs) != 2 || deferredSigs[0] != "sig-mid-1" || deferredSigs[1] != "sig-mid-2" {
			t.Fatalf("expected deferred signatures [sig-mid-1 sig-mid-2], got %v", deferredSigs)
		}
	})

	t.Run("strips nested injected placeholder fields while streaming real declared reason and dotted keys", func(t *testing.T) {
		origReq := []byte(`{"tools":[{"name":"complex_tool","input_schema":{"type":"object","properties":{"options":{"type":"object","additionalProperties":false},"a":{"type":"object","properties":{"b":{"type":"object","properties":{"keep":{"type":"number"}}}}},"a.b":{"type":"object","additionalProperties":false},"items":{"type":"array","items":{"type":"object","properties":{"id":{"type":"number"}},"additionalProperties":false}},"real":{"type":"object","properties":{"reason":{"type":"string"}}}},"additionalProperties":false}}]}`)
		transReq := []byte(`{"tools":[{"functionDeclarations":[{"name":"complex_tool","parameters":{"type":"object","properties":{"options":{"type":"object","properties":{"reason":{"type":"string"}},"required":["reason"]},"a":{"type":"object","properties":{"b":{"type":"object","properties":{"keep":{"type":"number"}}}}},"a.b":{"type":"object","properties":{"reason":{"type":"string"}},"required":["reason"]},"items":{"type":"array","items":{"type":"object","properties":{"id":{"type":"number"},"_":{"type":"boolean"}},"required":["_"]}},"real":{"type":"object","properties":{"reason":{"type":"string"}}}}}}]}]}`)

		chunks := [][]byte{
			[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"complex_tool","partialArgs":[{"jsonPath":"$.real.reason","stringValue":"ke","willContinue":true},{"jsonPath":"$.a.b.keep","numberValue":1},{"jsonPath":"$['a.b'].junk","stringValue":"drop"},{"jsonPath":"$.options.reason","stringValue":"injected","willContinue":true},{"jsonPath":"$.items[0]._","boolValue":true},{"jsonPath":"$.items[0].id","numberValue":7}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-clean"}`),
			[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.real.reason","stringValue":"pt"},{"jsonPath":"$.options.reason","stringValue":"-more"}],"willContinue":false}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-2.5-pro","responseId":"resp-clean"}`),
			[]byte("[DONE]"),
		}
		var p any
		var argBuf strings.Builder
		var chunk0Delta string
		for chunkIdx, ch := range chunks {
			outs := ConvertGeminiResponseToClaude(ctx, "gemini-2.5-pro", origReq, transReq, ch, &p)
			for _, line := range strings.Split(string(bytes.Join(outs, nil)), "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, "data:") {
					continue
				}
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if gjson.Get(payload, "type").String() == "content_block_delta" && gjson.Get(payload, "delta.type").String() == "input_json_delta" {
					d := gjson.Get(payload, "delta.partial_json").String()
					if chunkIdx == 0 {
						chunk0Delta += d
					}
					argBuf.WriteString(d)
				}
			}
		}
		if !strings.Contains(chunk0Delta, `"real":{"reason":"ke`) {
			t.Fatalf("expected declared real.reason to stream in chunk 0, got chunk0Delta=%s", chunk0Delta)
		}
		want := `{"real":{"reason":"kept"},"a":{"b":{"keep":1}},"a.b":{},"options":{},"items":[{"id":7}]}`
		if got := argBuf.String(); got != want {
			t.Fatalf("cleaned streaming args = %s, want %s", got, want)
		}
	})

	t.Run("single-chunk willContinue false parameterless call and empty partialArgs with complete args", func(t *testing.T) {
		var p1 any
		cPing := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"ping","willContinue":false}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-claude-ping"}`)
		outs1 := ConvertGeminiResponseToClaude(ctx, "gemini-2.5-pro", nil, nil, cPing, &p1)
		text1 := string(bytes.Join(outs1, nil))
		if !strings.Contains(text1, `"partial_json":"{}"`) || !strings.Contains(text1, `"type":"content_block_stop","index":0`) {
			t.Fatalf("expected immediate {} and content_block_stop for single-chunk willContinue:false, got: %s", text1)
		}

		var p2 any
		cArgs := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"},"partialArgs":[]}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7},"modelVersion":"gemini-2.5-pro","responseId":"resp-claude-args"}`)
		outs2 := ConvertGeminiResponseToClaude(ctx, "gemini-2.5-pro", nil, nil, cArgs, &p2)
		text2 := string(bytes.Join(outs2, nil))
		if !strings.Contains(text2, `{\"city\":\"Paris\"}`) {
			t.Fatalf("expected Paris args to be preserved when partialArgs is empty array, got: %s", text2)
		}
	})

	t.Run("replaced unfinished tool call does not emit content_block_stop for incomplete block", func(t *testing.T) {
		var p any
		c1 := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_a","name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"New ","willContinue":true}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-claude-replace"}`)
		c2 := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_b","name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"LA"}],"willContinue":false}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-2.5-pro","responseId":"resp-claude-replace"}`)
		var outs [][]byte
		outs = append(outs, ConvertGeminiResponseToClaude(ctx, "gemini-2.5-pro", requestJSON, requestJSON, c1, &p)...)
		outs = append(outs, ConvertGeminiResponseToClaude(ctx, "gemini-2.5-pro", requestJSON, requestJSON, c2, &p)...)
		outs = append(outs, ConvertGeminiResponseToClaude(ctx, "gemini-2.5-pro", requestJSON, requestJSON, []byte("[DONE]"), &p)...)
		full := string(bytes.Join(outs, nil))
		if strings.Contains(full, `{"type":"content_block_stop","index":0}`) {
			t.Fatalf("must not emit content_block_stop for unfinished block 0: %s", full)
		}
		if strings.Contains(full, `"type":"message_stop"`) {
			t.Fatalf("must not emit message_stop when block 0 was incomplete: %s", full)
		}
	})
}

func TestConvertGeminiResponseToClaude_SignatureOnlyPartDoesNotOpenEmptyTextBlock(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-test","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	thinkingChunk := []byte(`{
		"candidates": [{
			"content": {
				"parts": [{"text": "thinking text", "thought": true}]
			}
		}],
		"modelVersion": "gemini-test",
		"responseId": "resp-test"
	}`)
	signatureChunk := []byte(`{
		"candidates": [{
			"content": {
				"parts": [{"text": "", "thoughtSignature": "sig-test"}]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 10,
			"thoughtsTokenCount": 2,
			"totalTokenCount": 12
		},
		"modelVersion": "gemini-test",
		"responseId": "resp-test"
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-test", requestJSON, requestJSON, thinkingChunk, &param), nil)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-test", requestJSON, requestJSON, signatureChunk, &param), nil)...)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-test", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	if strings.Contains(outputText, `"content_block":{"type":"text"`) {
		t.Fatalf("signature-only part must not open an empty text block: %s", outputText)
	}
	if strings.Contains(outputText, `"type":"content_block_stop","index":1`) {
		t.Fatalf("signature-only part must not produce a stop for unopened index 1: %s", outputText)
	}
	if !strings.Contains(outputText, `"type":"signature_delta"`) || !strings.Contains(outputText, `"signature":"sig-test"`) {
		t.Fatalf("signature-only part must be emitted as a thinking signature delta: %s", outputText)
	}
	if got := strings.Count(outputText, `"type":"content_block_stop","index":0`); got != 1 {
		t.Fatalf("expected exactly one stop for thinking index 0, got %d: %s", got, outputText)
	}
	if !strings.Contains(outputText, `"type":"message_delta"`) || !strings.Contains(outputText, `"output_tokens":2`) {
		t.Fatalf("finish chunk without candidatesTokenCount must still emit final message_delta: %s", outputText)
	}
	if !strings.Contains(outputText, `"type":"message_stop"`) {
		t.Fatalf("DONE chunk must still emit message_stop after final events: %s", outputText)
	}
}

func TestConvertGeminiResponseToClaudeNonStream_PreservesThoughtSignature(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]}`)
	geminiResponse := []byte(`{
		"candidates": [{
			"content": {
				"parts": [
					{"text": "thinking step 1\n", "thought": true},
					{"text": "thinking step 2", "thought": true, "thoughtSignature": "sig-xyz-123"},
					{"text": "visible answer"}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 10,
			"candidatesTokenCount": 5
		},
		"modelVersion": "gemini-2.5-pro",
		"responseId": "resp-non-stream"
	}`)

	ctx := context.Background()
	output := ConvertGeminiResponseToClaudeNonStream(ctx, "gemini-2.5-pro", requestJSON, requestJSON, geminiResponse, nil)
	outputJSON := gjson.ParseBytes(output)

	blocks := outputJSON.Get("content").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks (thinking + text), got %d: %s", len(blocks), string(output))
	}

	thinkingBlock := blocks[0]
	if thinkingBlock.Get("type").String() != "thinking" {
		t.Fatalf("expected first block to be thinking, got %s", thinkingBlock.Get("type").String())
	}
	if thinkingBlock.Get("thinking").String() != "thinking step 1\nthinking step 2" {
		t.Fatalf("unexpected thinking content: %s", thinkingBlock.Get("thinking").String())
	}
	if thinkingBlock.Get("signature").String() != "sig-xyz-123" {
		t.Fatalf("expected signature 'sig-xyz-123', got %q. Output: %s", thinkingBlock.Get("signature").String(), string(output))
	}

	textBlock := blocks[1]
	if textBlock.Get("type").String() != "text" || textBlock.Get("text").String() != "visible answer" {
		t.Fatalf("unexpected text block: %s", textBlock.Raw)
	}
}

func TestConvertGeminiResponseToClaudeNonStream_PartWithThoughtSignatureWithoutThoughtBool(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]}`)
	geminiResponse := []byte(`{
		"candidates": [{
			"content": {
				"parts": [
					{"text": "inferred reasoning", "thought_signature": "sig-snake-case"},
					{"text": "final answer"}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 10,
			"candidatesTokenCount": 5
		},
		"modelVersion": "gemini-2.5-pro",
		"responseId": "resp-non-stream-2"
	}`)

	ctx := context.Background()
	output := ConvertGeminiResponseToClaudeNonStream(ctx, "gemini-2.5-pro", requestJSON, requestJSON, geminiResponse, nil)
	outputJSON := gjson.ParseBytes(output)

	blocks := outputJSON.Get("content").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks (thinking + text), got %d: %s", len(blocks), string(output))
	}

	thinkingBlock := blocks[0]
	if thinkingBlock.Get("type").String() != "thinking" {
		t.Fatalf("expected first block to be thinking, got %s", thinkingBlock.Get("type").String())
	}
	if thinkingBlock.Get("thinking").String() != "" {
		t.Fatalf("carrier thinking block must have empty thinking, got %q", thinkingBlock.Get("thinking").String())
	}
	if thinkingBlock.Get("signature").String() != "sig-snake-case" {
		t.Fatalf("expected signature 'sig-snake-case', got %q. Output: %s", thinkingBlock.Get("signature").String(), string(output))
	}

	textBlock := blocks[1]
	if textBlock.Get("type").String() != "text" || textBlock.Get("text").String() != "inferred reasoningfinal answer" {
		t.Fatalf("unexpected text block: %s", textBlock.Raw)
	}
}

func TestConvertGeminiResponseToClaudeNonStream_TrailingSignatureOnlyPart(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]}`)
	geminiResponse := []byte(`{
		"candidates": [{
			"content": {
				"parts": [
					{"text": "thinking step 1\n", "thought": true},
					{"text": "", "thoughtSignature": "sig-trailing"},
					{"text": "visible answer"}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 10,
			"candidatesTokenCount": 5
		},
		"modelVersion": "gemini-2.5-pro",
		"responseId": "resp-non-stream-trailing"
	}`)

	ctx := context.Background()
	output := ConvertGeminiResponseToClaudeNonStream(ctx, "gemini-2.5-pro", requestJSON, requestJSON, geminiResponse, nil)
	outputJSON := gjson.ParseBytes(output)

	blocks := outputJSON.Get("content").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks (thinking + text), got %d: %s", len(blocks), string(output))
	}

	thinkingBlock := blocks[0]
	if thinkingBlock.Get("type").String() != "thinking" {
		t.Fatalf("expected first block to be thinking, got %s", thinkingBlock.Get("type").String())
	}
	if thinkingBlock.Get("thinking").String() != "thinking step 1\n" {
		t.Fatalf("unexpected thinking content: %s", thinkingBlock.Get("thinking").String())
	}
	if thinkingBlock.Get("signature").String() != "sig-trailing" {
		t.Fatalf("expected signature 'sig-trailing', got %q. Output: %s", thinkingBlock.Get("signature").String(), string(output))
	}

	textBlock := blocks[1]
	if textBlock.Get("type").String() != "text" || textBlock.Get("text").String() != "visible answer" {
		t.Fatalf("unexpected text block: %s", textBlock.Raw)
	}
}

func TestConvertGeminiResponseToClaude_UsageWithCachedContentTokenCount(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]}`)
	chunk := []byte(`{
		"candidates": [{
			"content": {
				"parts": [{"text": "Hello world"}]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 100,
			"candidatesTokenCount": 7,
			"cachedContentTokenCount": 91
		},
		"modelVersion": "gemini-2.5-pro",
		"responseId": "resp-usage-cache"
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-2.5-pro", requestJSON, requestJSON, chunk, &param), nil)
	outputText := string(output)

	if !strings.Contains(outputText, `"type":"message_delta"`) {
		t.Fatalf("expected message_delta event in output, got: %s", outputText)
	}

	foundMessageDelta := false
	// Find the message_delta event data
	for _, line := range strings.Split(outputText, "\n") {
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"type":"message_delta"`) {
			foundMessageDelta = true
			deltaJSON := gjson.Parse(strings.TrimPrefix(line, "data: "))
			inputTokens := deltaJSON.Get("usage.input_tokens").Int()
			if inputTokens != 9 {
				t.Fatalf("expected usage.input_tokens = 9 (100 - 91), got %d. Payload: %s", inputTokens, line)
			}
			cacheReadTokens := deltaJSON.Get("usage.cache_read_input_tokens").Int()
			if cacheReadTokens != 91 {
				t.Fatalf("expected usage.cache_read_input_tokens = 91, got %d. Payload: %s", cacheReadTokens, line)
			}
			outputTokens := deltaJSON.Get("usage.output_tokens").Int()
			if outputTokens != 7 {
				t.Fatalf("expected usage.output_tokens = 7, got %d. Payload: %s", outputTokens, line)
			}
		}
	}
	if !foundMessageDelta {
		t.Fatalf("failed to locate parsed message_delta event in payload: %s", outputText)
	}
}

func TestConvertGeminiResponseToClaudeNonStream_UsageWithCachedContentTokenCount(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]}`)
	geminiResponse := []byte(`{
		"candidates": [{
			"content": {
				"parts": [{"text": "Hello world"}]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 100,
			"candidatesTokenCount": 7,
			"cachedContentTokenCount": 91
		},
		"modelVersion": "gemini-2.5-pro",
		"responseId": "resp-usage-cache-nonstream"
	}`)

	ctx := context.Background()
	output := ConvertGeminiResponseToClaudeNonStream(ctx, "gemini-2.5-pro", requestJSON, requestJSON, geminiResponse, nil)
	outputJSON := gjson.ParseBytes(output)

	inputTokens := outputJSON.Get("usage.input_tokens").Int()
	if inputTokens != 9 {
		t.Fatalf("expected usage.input_tokens = 9 (100 - 91), got %d. Output: %s", inputTokens, string(output))
	}
	cacheReadTokens := outputJSON.Get("usage.cache_read_input_tokens").Int()
	if cacheReadTokens != 91 {
		t.Fatalf("expected usage.cache_read_input_tokens = 91, got %d. Output: %s", cacheReadTokens, string(output))
	}
	outputTokens := outputJSON.Get("usage.output_tokens").Int()
	if outputTokens != 7 {
		t.Fatalf("expected usage.output_tokens = 7, got %d. Output: %s", outputTokens, string(output))
	}
}

func TestConvertGeminiResponseToClaudeStream_PartlessSafetyClosesMessageWithRefusal(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`)
	chunk := []byte(`{
		"candidates": [{
			"content": {"role": "model", "parts": []},
			"index": 0,
			"finishReason": "SAFETY"
		}],
		"modelVersion": "m",
		"usageMetadata": {
			"promptTokenCount": 120,
			"candidatesTokenCount": 37,
			"totalTokenCount": 157
		}
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertGeminiResponseToClaude(ctx, "claude-opus-5-5", requestJSON, requestJSON, chunk, &param), nil)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "claude-opus-5-5", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	lastIndex := -1
	for _, eventName := range []string{"message_start", "content_block_start", "content_block_stop", "message_delta", "message_stop"} {
		index := strings.Index(outputText, "event: "+eventName+"\n")
		if index < 0 {
			t.Fatalf("event %q not found in output:\n%s", eventName, outputText)
		}
		if index <= lastIndex {
			t.Fatalf("event %q is out of order in output:\n%s", eventName, outputText)
		}
		lastIndex = index
	}

	var foundDelta bool
	for _, line := range strings.Split(outputText, "\n") {
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"type":"message_delta"`) {
			foundDelta = true
			deltaJSON := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if got := deltaJSON.Get("delta.stop_reason").String(); got != "refusal" {
				t.Fatalf("stop_reason = %q, want refusal. Payload: %s", got, line)
			}
			if got := deltaJSON.Get("usage.input_tokens").Int(); got != 120 {
				t.Fatalf("input_tokens = %d, want 120. Payload: %s", got, line)
			}
			if got := deltaJSON.Get("usage.output_tokens").Int(); got != 37 {
				t.Fatalf("output_tokens = %d, want 37. Payload: %s", got, line)
			}
		}
	}
	if !foundDelta {
		t.Fatalf("failed to find message_delta event in output:\n%s", outputText)
	}
}

func TestConvertGeminiResponseToClaudeStream_PartlessMalformedFunctionCallClosesMessage(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`)
	chunk := []byte(`{
		"candidates": [{
			"content": {"role": "model", "parts": []},
			"index": 0,
			"finishReason": "MALFORMED_FUNCTION_CALL"
		}],
		"modelVersion": "m",
		"usageMetadata": {
			"promptTokenCount": 50,
			"candidatesTokenCount": 10,
			"totalTokenCount": 60
		}
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertGeminiResponseToClaude(ctx, "claude-opus-5-5", requestJSON, requestJSON, chunk, &param), nil)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "claude-opus-5-5", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	if strings.Contains(outputText, `"stop_reason":"refusal"`) {
		t.Fatalf("unexpected refusal stop_reason in output:\n%s", outputText)
	}
	if strings.Contains(outputText, `"type":"message_delta"`) {
		t.Fatalf("unexpected message_delta in output:\n%s", outputText)
	}
	if strings.Contains(outputText, `"type":"message_stop"`) {
		t.Fatalf("unexpected message_stop in output:\n%s", outputText)
	}
	if !strings.Contains(outputText, "event: error\n") {
		t.Fatalf("expected SSE error event in output:\n%s", outputText)
	}

	var foundError bool
	for _, line := range strings.Split(outputText, "\n") {
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"type":"error"`) {
			foundError = true
			errJSON := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if got := errJSON.Get("error.type").String(); got != "api_error" {
				t.Fatalf("error.type = %q, want api_error. Payload: %s", got, line)
			}
			if !strings.Contains(errJSON.Get("error.message").String(), "MALFORMED_FUNCTION_CALL") {
				t.Fatalf("error.message = %q, want mention of MALFORMED_FUNCTION_CALL. Payload: %s", errJSON.Get("error.message").String(), line)
			}
		}
	}
	if !foundError {
		t.Fatalf("failed to find error event data in output:\n%s", outputText)
	}
}

func TestConvertGeminiResponseToClaudeStream_MalformedFunctionCallAfterThinkingEmitsError(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`)
	chunk1 := []byte(`{
		"candidates": [{
			"content": {"role": "model", "parts": [{"thought": true, "text": "planning tool call"}]},
			"index": 0
		}],
		"modelVersion": "m"
	}`)
	chunk2 := []byte(`{
		"candidates": [{
			"content": {"role": "model", "parts": []},
			"index": 0,
			"finishReason": "MALFORMED_FUNCTION_CALL"
		}],
		"modelVersion": "m",
		"usageMetadata": {
			"promptTokenCount": 50,
			"candidatesTokenCount": 10,
			"thoughtsTokenCount": 313,
			"totalTokenCount": 373
		}
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertGeminiResponseToClaude(ctx, "claude-opus-5-5", requestJSON, requestJSON, chunk1, &param), nil)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "claude-opus-5-5", requestJSON, requestJSON, chunk2, &param), nil)...)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "claude-opus-5-5", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	stopIdx := strings.Index(outputText, "event: content_block_stop\n")
	errIdx := strings.Index(outputText, "event: error\n")
	if stopIdx < 0 || errIdx < 0 || stopIdx >= errIdx {
		t.Fatalf("expected content_block_stop before error event in output:\n%s", outputText)
	}
	if strings.Contains(outputText, `"type":"message_delta"`) || strings.Contains(outputText, `"type":"message_stop"`) {
		t.Fatalf("unexpected normal completion events after MALFORMED_FUNCTION_CALL:\n%s", outputText)
	}
}

func TestConvertGeminiResponseToClaudeStream_MalformedFunctionCallWithoutUsageAndDuplicateTerminalChunk(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`)
	chunkWithoutUsage := []byte(`{
		"candidates": [{
			"content": {"role": "model", "parts": []},
			"index": 0,
			"finishReason": "MALFORMED_FUNCTION_CALL"
		}],
		"modelVersion": "m"
	}`)
	duplicateTerminalChunk := []byte(`{
		"candidates": [{
			"content": {"role": "model", "parts": [{"text": "late text"}]},
			"index": 0,
			"finishReason": "MALFORMED_FUNCTION_CALL"
		}],
		"modelVersion": "m",
		"usageMetadata": {
			"promptTokenCount": 50,
			"candidatesTokenCount": 10,
			"totalTokenCount": 60
		}
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertGeminiResponseToClaude(ctx, "claude-opus-5-5", requestJSON, requestJSON, chunkWithoutUsage, &param), nil)
	lateOutput := bytes.Join(ConvertGeminiResponseToClaude(ctx, "claude-opus-5-5", requestJSON, requestJSON, duplicateTerminalChunk, &param), nil)
	doneOutput := bytes.Join(ConvertGeminiResponseToClaude(ctx, "claude-opus-5-5", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)
	if len(lateOutput) != 0 {
		t.Fatalf("expected duplicate terminal chunk after error to emit nothing, got:\n%s", string(lateOutput))
	}
	if len(doneOutput) != 0 {
		t.Fatalf("expected [DONE] after error to emit nothing, got:\n%s", string(doneOutput))
	}
	outputText := string(output)
	if got := strings.Count(outputText, "event: error\n"); got != 1 {
		t.Fatalf("error event count = %d, want 1. Output:\n%s", got, outputText)
	}
}

func TestConvertGeminiResponseToClaude_MalformedFunctionCallWithToolUsePreservesToolUse(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`)
	rawJSON := []byte(`{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"functionCall": {"name": "Bash", "args": {"command": "ls"}}}]
			},
			"index": 0,
			"finishReason": "MALFORMED_FUNCTION_CALL"
		}],
		"usageMetadata": {
			"promptTokenCount": 50,
			"candidatesTokenCount": 10,
			"totalTokenCount": 60
		},
		"modelVersion": "m",
		"responseId": "resp-tool"
	}`)

	ctx := context.Background()
	nonStream := ConvertGeminiResponseToClaudeNonStream(ctx, "claude-opus-5-5", requestJSON, requestJSON, rawJSON, nil)
	nonStreamJSON := gjson.ParseBytes(nonStream)
	if got := nonStreamJSON.Get("stop_reason").String(); got != "tool_use" {
		t.Fatalf("non-stream stop_reason = %q, want tool_use. Output: %s", got, string(nonStream))
	}

	var param any
	streamOut := bytes.Join(ConvertGeminiResponseToClaude(ctx, "claude-opus-5-5", requestJSON, requestJSON, rawJSON, &param), nil)
	streamOut = append(streamOut, bytes.Join(ConvertGeminiResponseToClaude(ctx, "claude-opus-5-5", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	streamText := string(streamOut)
	if strings.Contains(streamText, "event: error\n") {
		t.Fatalf("unexpected error event when valid functionCall is present:\n%s", streamText)
	}
	if !strings.Contains(streamText, `"stop_reason":"tool_use"`) {
		t.Fatalf("expected stop_reason tool_use in stream output:\n%s", streamText)
	}
}

func TestConvertGeminiResponseToClaudeStream_PartlessStopClosesMessageWithEndTurn(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`)
	chunk := []byte(`{
		"candidates": [{
			"content": {"role": "model", "parts": [{"text": ""}]},
			"index": 0,
			"finishReason": "STOP"
		}],
		"modelVersion": "m",
		"usageMetadata": {
			"promptTokenCount": 80,
			"candidatesTokenCount": 0,
			"totalTokenCount": 80
		}
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertGeminiResponseToClaude(ctx, "claude-opus-5-5", requestJSON, requestJSON, chunk, &param), nil)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "claude-opus-5-5", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	lastIndex := -1
	for _, eventName := range []string{"message_start", "content_block_start", "content_block_stop", "message_delta", "message_stop"} {
		index := strings.Index(outputText, "event: "+eventName+"\n")
		if index < 0 {
			t.Fatalf("event %q not found in output:\n%s", eventName, outputText)
		}
		if index <= lastIndex {
			t.Fatalf("event %q is out of order in output:\n%s", eventName, outputText)
		}
		lastIndex = index
	}

	var foundDelta bool
	for _, line := range strings.Split(outputText, "\n") {
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"type":"message_delta"`) {
			foundDelta = true
			deltaJSON := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if got := deltaJSON.Get("delta.stop_reason").String(); got != "end_turn" {
				t.Fatalf("stop_reason = %q, want end_turn. Payload: %s", got, line)
			}
		}
	}
	if !foundDelta {
		t.Fatalf("failed to find message_delta in output:\n%s", outputText)
	}
}

func TestConvertGeminiResponseToClaudeNonStream_SafetyAndMalformedFunctionCallRefusal(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`)
	testCases := []struct {
		finishReason string
		wantReason   string
	}{
		{finishReason: "SAFETY", wantReason: "refusal"},
		{finishReason: "RECITATION", wantReason: "refusal"},
		{finishReason: "PROHIBITED_CONTENT", wantReason: "refusal"},
		{finishReason: "SPII", wantReason: "refusal"},
		{finishReason: "BLOCKLIST", wantReason: "refusal"},
		{finishReason: "IMAGE_SAFETY", wantReason: "refusal"},
		{finishReason: "MALFORMED_RESPONSE", wantReason: "refusal"},
		{finishReason: "MAX_TOKENS", wantReason: "max_tokens"},
		{finishReason: "STOP", wantReason: "end_turn"},
	}

	ctx := context.Background()
	for _, tc := range testCases {
		t.Run(tc.finishReason, func(t *testing.T) {
			rawJSON := []byte(`{
				"candidates": [{
					"content": {"role": "model", "parts": []},
					"finishReason": "` + tc.finishReason + `"
				}],
				"usageMetadata": {
					"promptTokenCount": 120,
					"candidatesTokenCount": 37,
					"totalTokenCount": 157
				},
				"modelVersion": "m",
				"responseId": "resp-test"
			}`)
			output := ConvertGeminiResponseToClaudeNonStream(ctx, "claude-opus-5-5", requestJSON, requestJSON, rawJSON, nil)
			outputJSON := gjson.ParseBytes(output)
			if got := outputJSON.Get("stop_reason").String(); got != tc.wantReason {
				t.Fatalf("stop_reason = %q, want %q. Output: %s", got, tc.wantReason, string(output))
			}
		})
	}
}

func TestConvertGeminiResponseToClaudeNonStream_MalformedFunctionCallReturnsAPIError(t *testing.T) {
	requestJSON := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`)
	rawJSON := []byte(`{
		"candidates": [{
			"content": {"role": "model", "parts": []},
			"finishReason": "MALFORMED_FUNCTION_CALL"
		}],
		"usageMetadata": {
			"promptTokenCount": 120,
			"candidatesTokenCount": 37,
			"totalTokenCount": 157
		},
		"modelVersion": "m",
		"responseId": "resp-test"
	}`)

	output := ConvertGeminiResponseToClaudeNonStream(context.Background(), "claude-opus-5-5", requestJSON, requestJSON, rawJSON, nil)
	outputJSON := gjson.ParseBytes(output)
	if got := outputJSON.Get("stop_reason").String(); got == "refusal" {
		t.Fatalf("unexpected stop_reason = %q. Output: %s", got, string(output))
	}
	if got := outputJSON.Get("type").String(); got != "error" {
		t.Fatalf("type = %q, want error. Output: %s", got, string(output))
	}
	if got := outputJSON.Get("error.type").String(); got != "api_error" {
		t.Fatalf("error.type = %q, want api_error. Output: %s", got, string(output))
	}
	if !strings.Contains(outputJSON.Get("error.message").String(), "MALFORMED_FUNCTION_CALL") {
		t.Fatalf("error.message = %q, want mention of MALFORMED_FUNCTION_CALL. Output: %s", outputJSON.Get("error.message").String(), string(output))
	}
}

func TestConvertGeminiResponseToClaudeNonStream_Issue6409_SignedVisibleText(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-3.5-flash-lite","messages":[{"role":"user","content":"hi"}]}`)
	geminiResponse := []byte(`{
		"candidates": [{
			"content": {
				"parts": [
					{"text": "ok", "thoughtSignature": "EmAKXgFpFH0Tb/MkBw="}
				],
				"role": "model"
			},
			"finishReason": "STOP",
			"index": 0
		}],
		"usageMetadata": {
			"promptTokenCount": 5,
			"candidatesTokenCount": 1
		},
		"modelVersion": "gemini-3.5-flash-lite",
		"responseId": "5Qm-as2qBuLI-sAP76KVkAk"
	}`)

	ctx := context.Background()
	output := ConvertGeminiResponseToClaudeNonStream(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, geminiResponse, nil)
	outputJSON := gjson.ParseBytes(output)

	blocks := outputJSON.Get("content").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks (carrier thinking + text), got %d: %s", len(blocks), string(output))
	}

	thinkingBlock := blocks[0]
	if thinkingBlock.Get("type").String() != "thinking" {
		t.Fatalf("expected block 0 to be thinking carrier, got %s", thinkingBlock.Get("type").String())
	}
	if got := thinkingBlock.Get("thinking").String(); got != "" {
		t.Fatalf("carrier thinking block must have empty thinking, got %q", got)
	}
	if got := thinkingBlock.Get("signature").String(); got != "EmAKXgFpFH0Tb/MkBw=" {
		t.Fatalf("expected signature 'EmAKXgFpFH0Tb/MkBw=', got %q", got)
	}

	textBlock := blocks[1]
	if textBlock.Get("type").String() != "text" || textBlock.Get("text").String() != "ok" {
		t.Fatalf("expected visible text block with 'ok', got: %s", textBlock.Raw)
	}
}

func TestConvertGeminiResponseToClaudeNonStream_Issue6409_ThinkingFollowedBySignedVisibleText(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-3.5-flash-lite","messages":[{"role":"user","content":"hi"}]}`)
	geminiResponse := []byte(`{
		"candidates": [{
			"content": {
				"parts": [
					{"text": "reasoning step", "thought": true, "thoughtSignature": "sig-think"},
					{"text": "final answer", "thoughtSignature": "sig-visible"}
				],
				"role": "model"
			},
			"finishReason": "STOP"
		}],
		"modelVersion": "gemini-3.5-flash-lite",
		"responseId": "resp-mixed"
	}`)

	ctx := context.Background()
	output := ConvertGeminiResponseToClaudeNonStream(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, geminiResponse, nil)
	outputJSON := gjson.ParseBytes(output)

	blocks := outputJSON.Get("content").Array()
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks (thinking, carrier thinking, text), got %d: %s", len(blocks), string(output))
	}

	if blocks[0].Get("type").String() != "thinking" || blocks[0].Get("thinking").String() != "reasoning step" || blocks[0].Get("signature").String() != "sig-think" {
		t.Fatalf("block 0 must be thinking with its own signature, got: %s", blocks[0].Raw)
	}
	if blocks[1].Get("type").String() != "thinking" || blocks[1].Get("thinking").String() != "" || blocks[1].Get("signature").String() != "sig-visible" {
		t.Fatalf("block 1 must be detached carrier thinking for visible text signature, got: %s", blocks[1].Raw)
	}
	if blocks[2].Get("type").String() != "text" || blocks[2].Get("text").String() != "final answer" {
		t.Fatalf("block 2 must be text block with visible answer, got: %s", blocks[2].Raw)
	}
}

func TestConvertGeminiResponseToClaudeNonStream_Issue6409_ConsecutiveSignedVisibleTexts(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-3.5-flash-lite","messages":[{"role":"user","content":"hi"}]}`)
	geminiResponse := []byte(`{
		"candidates": [{
			"content": {
				"parts": [
					{"text": "part A", "thoughtSignature": "sig-A"},
					{"text": "part B", "thoughtSignature": "sig-B"}
				],
				"role": "model"
			},
			"finishReason": "STOP"
		}],
		"modelVersion": "gemini-3.5-flash-lite",
		"responseId": "resp-consecutive"
	}`)

	ctx := context.Background()
	output := ConvertGeminiResponseToClaudeNonStream(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, geminiResponse, nil)
	outputJSON := gjson.ParseBytes(output)

	blocks := outputJSON.Get("content").Array()
	if len(blocks) != 4 {
		t.Fatalf("expected 4 blocks (carrier A, text A, carrier B, text B), got %d: %s", len(blocks), string(output))
	}

	if blocks[0].Get("type").String() != "thinking" || blocks[0].Get("signature").String() != "sig-A" {
		t.Fatalf("block 0 must be carrier for sig-A, got: %s", blocks[0].Raw)
	}
	if blocks[1].Get("type").String() != "text" || blocks[1].Get("text").String() != "part A" {
		t.Fatalf("block 1 must be text part A, got: %s", blocks[1].Raw)
	}
	if blocks[2].Get("type").String() != "thinking" || blocks[2].Get("signature").String() != "sig-B" {
		t.Fatalf("block 2 must be carrier for sig-B, got: %s", blocks[2].Raw)
	}
	if blocks[3].Get("type").String() != "text" || blocks[3].Get("text").String() != "part B" {
		t.Fatalf("block 3 must be text part B, got: %s", blocks[3].Raw)
	}
}

func TestConvertGeminiResponseToClaudeStream_Issue6409_SignedVisibleText(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-3.5-flash-lite","messages":[{"role":"user","content":"hi"}]}`)
	chunk := []byte(`{
		"candidates": [{
			"content": {
				"parts": [
					{"text": "ok", "thoughtSignature": "sig-stream-1"}
				],
				"role": "model"
			},
			"finishReason": "STOP",
			"index": 0
		}],
		"usageMetadata": {
			"promptTokenCount": 5,
			"candidatesTokenCount": 1
		},
		"modelVersion": "gemini-3.5-flash-lite",
		"responseId": "resp-s1"
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, chunk, &param), nil)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	if strings.Contains(outputText, `"thinking_delta"`) {
		t.Fatalf("signed visible text must not emit thinking_delta, got: %s", outputText)
	}

	// Verify block 0 is carrier thinking
	if !strings.Contains(outputText, `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`) {
		t.Fatalf("expected block 0 to be carrier thinking, got: %s", outputText)
	}
	if !strings.Contains(outputText, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-stream-1"}}`) {
		t.Fatalf("expected block 0 to have signature_delta, got: %s", outputText)
	}
	if !strings.Contains(outputText, `{"type":"content_block_stop","index":0}`) {
		t.Fatalf("expected block 0 stop, got: %s", outputText)
	}

	// Verify block 1 is visible text
	if !strings.Contains(outputText, `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`) {
		t.Fatalf("expected block 1 to be text block, got: %s", outputText)
	}
	if !strings.Contains(outputText, `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`) {
		t.Fatalf("expected block 1 to have text_delta with 'ok', got: %s", outputText)
	}
	if !strings.Contains(outputText, `{"type":"content_block_stop","index":1}`) {
		t.Fatalf("expected block 1 stop, got: %s", outputText)
	}
}

func TestConvertGeminiResponseToClaudeStream_Issue6409_SplitTrailingSignature(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-3.5-flash-lite","messages":[{"role":"user","content":"hi"}]}`)
	textChunk := []byte(`{
		"candidates": [{
			"content": {
				"parts": [{"text": "ok"}]
			}
		}],
		"modelVersion": "gemini-3.5-flash-lite",
		"responseId": "resp-s2"
	}`)
	sigChunk := []byte(`{
		"candidates": [{
			"content": {
				"parts": [{"text": "", "thoughtSignature": "sig-stream-trailing"}]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 5,
			"candidatesTokenCount": 1
		},
		"modelVersion": "gemini-3.5-flash-lite",
		"responseId": "resp-s2"
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, textChunk, &param), nil)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, sigChunk, &param), nil)...)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	if strings.Contains(outputText, `"thinking_delta"`) {
		t.Fatalf("unexpected thinking_delta in stream: %s", outputText)
	}

	// Block 0: text block with "ok"
	if !strings.Contains(outputText, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) {
		t.Fatalf("expected block 0 text start, got: %s", outputText)
	}
	if !strings.Contains(outputText, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`) {
		t.Fatalf("expected block 0 text delta, got: %s", outputText)
	}
	if !strings.Contains(outputText, `{"type":"content_block_stop","index":0}`) {
		t.Fatalf("expected block 0 text stop, got: %s", outputText)
	}

	// Block 1: trailing carrier thinking block
	if !strings.Contains(outputText, `{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`) {
		t.Fatalf("expected block 1 trailing carrier thinking, got: %s", outputText)
	}
	if !strings.Contains(outputText, `{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"sig-stream-trailing"}}`) {
		t.Fatalf("expected block 1 signature_delta, got: %s", outputText)
	}
	if !strings.Contains(outputText, `{"type":"content_block_stop","index":1}`) {
		t.Fatalf("expected block 1 stop, got: %s", outputText)
	}
}

func TestConvertGeminiResponseToClaudeStream_Issue6409_SignedFunctionCall(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-3.5-flash-lite","messages":[{"role":"user","content":"hi"}]}`)
	chunk := []byte(`{
		"candidates": [{
			"content": {
				"parts": [
					{"thoughtSignature": "sig-fc-stream", "functionCall": {"name": "test_tool", "args": {}}}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 5,
			"candidatesTokenCount": 1
		},
		"modelVersion": "gemini-3.5-flash-lite",
		"responseId": "resp-fc"
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, chunk, &param), nil)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	// Block 0: carrier thinking
	if !strings.Contains(outputText, `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`) {
		t.Fatalf("expected block 0 carrier thinking, got: %s", outputText)
	}
	if !strings.Contains(outputText, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-fc-stream"}}`) {
		t.Fatalf("expected block 0 signature_delta, got: %s", outputText)
	}
	if !strings.Contains(outputText, `{"type":"content_block_stop","index":0}`) {
		t.Fatalf("expected block 0 stop, got: %s", outputText)
	}

	// Block 1: tool_use
	if !strings.Contains(outputText, `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use"`) {
		t.Fatalf("expected block 1 tool_use, got: %s", outputText)
	}
	if !strings.Contains(outputText, `{"type":"content_block_stop","index":1}`) {
		t.Fatalf("expected block 1 tool_use stop, got: %s", outputText)
	}
}

func TestConvertGeminiResponseToClaudeStream_Issue6409_FunctionCallContinuationSignature(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-3.5-flash-lite","messages":[{"role":"user","content":"hi"}]}`)
	chunk1 := []byte(`{
		"candidates": [{
			"content": {
				"parts": [
					{"functionCall": {"name": "test_tool", "args": {}}}
				]
			}
		}],
		"modelVersion": "gemini-3.5-flash-lite",
		"responseId": "resp-fc-cont"
	}`)
	chunk2 := []byte(`{
		"candidates": [{
			"content": {
				"parts": [
					{"thoughtSignature": "sig-fc-cont-done", "functionCall": {"args": {"key":"val"}}}
				]
			},
			"finishReason": "STOP"
		}],
		"modelVersion": "gemini-3.5-flash-lite",
		"responseId": "resp-fc-cont"
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, chunk1, &param), nil)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, chunk2, &param), nil)...)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	if !strings.Contains(outputText, `"type":"input_json_delta"`) {
		t.Fatalf("expected input_json_delta, got: %s", outputText)
	}
	if !strings.Contains(outputText, `"signature":"sig-fc-cont-done"`) {
		t.Fatalf("expected signature_delta with 'sig-fc-cont-done', got: %s", outputText)
	}
}

func TestConvertGeminiResponseToClaudeStream_Issue6409_ThinkingFollowedBySignedVisibleText(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-3.5-flash-lite","messages":[{"role":"user","content":"hi"}]}`)
	chunk1 := []byte(`{
		"candidates": [{
			"content": {
				"parts": [
					{"text": "thinking step", "thought": true, "thoughtSignature": "sig-stream-think"}
				]
			}
		}],
		"modelVersion": "gemini-3.5-flash-lite",
		"responseId": "resp-mixed-stream"
	}`)
	chunk2 := []byte(`{
		"candidates": [{
			"content": {
				"parts": [
					{"text": "visible text", "thoughtSignature": "sig-stream-visible"}
				]
			},
			"finishReason": "STOP"
		}],
		"modelVersion": "gemini-3.5-flash-lite",
		"responseId": "resp-mixed-stream"
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, chunk1, &param), nil)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, chunk2, &param), nil)...)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3.5-flash-lite", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	// Block 0: thinking with "sig-stream-think"
	if !strings.Contains(outputText, `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`) {
		t.Fatalf("expected block 0 thinking start, got: %s", outputText)
	}
	if !strings.Contains(outputText, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-stream-think"}}`) {
		t.Fatalf("expected block 0 thinking signature, got: %s", outputText)
	}

	// Block 1: detached carrier thinking for visible text signature "sig-stream-visible"
	if !strings.Contains(outputText, `{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`) {
		t.Fatalf("expected block 1 detached carrier thinking start, got: %s", outputText)
	}
	if !strings.Contains(outputText, `{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"sig-stream-visible"}}`) {
		t.Fatalf("expected block 1 signature_delta 'sig-stream-visible', got: %s", outputText)
	}

	// Block 2: text block with "visible text"
	if !strings.Contains(outputText, `{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`) {
		t.Fatalf("expected block 2 text start, got: %s", outputText)
	}
	if !strings.Contains(outputText, `{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"visible text"}}`) {
		t.Fatalf("expected block 2 text_delta, got: %s", outputText)
	}
}

func TestConvertGeminiResponseToClaudeNonStream_UniqueToolUseIDsAcrossTurns(t *testing.T) {
	atomic.StoreUint64(&toolUseIDCounter, 0)

	requestJSON := []byte(`{"model":"gemini-4-argon","messages":[{"role":"user","content":"run command"}]}`)
	rawResponse := []byte(`{
		"candidates": [{
			"content": {
				"parts": [
					{"functionCall": {"name": "Bash", "args": {"command": "ls"}}}
				],
				"role": "model"
			},
			"finishReason": "STOP"
		}],
		"modelVersion": "gemini-4-argon",
		"responseId": "resp-tool"
	}`)

	ctx := context.Background()

	var param any
	streamBytes := bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-4-argon", requestJSON, requestJSON, rawResponse, &param), nil)
	streamBytes = append(streamBytes, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-4-argon", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	var streamToolID string
	for _, line := range strings.Split(string(streamBytes), "\n") {
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"type":"tool_use"`) {
			streamToolID = gjson.Get(strings.TrimPrefix(line, "data: "), "content_block.id").String()
			break
		}
	}
	if streamToolID == "" {
		t.Fatalf("expected tool_use id in streamed response, got: %s", string(streamBytes))
	}

	nonStream1 := ConvertGeminiResponseToClaudeNonStream(ctx, "gemini-4-argon", requestJSON, requestJSON, rawResponse, nil)
	nonStreamID1 := gjson.GetBytes(nonStream1, "content.0.id").String()
	if nonStreamID1 == "" {
		t.Fatalf("expected tool_use id in first non-stream response, got: %s", string(nonStream1))
	}

	nonStream2 := ConvertGeminiResponseToClaudeNonStream(ctx, "gemini-4-argon", requestJSON, requestJSON, rawResponse, nil)
	nonStreamID2 := gjson.GetBytes(nonStream2, "content.0.id").String()
	if nonStreamID2 == "" {
		t.Fatalf("expected tool_use id in second non-stream response, got: %s", string(nonStream2))
	}

	if nonStreamID1 == streamToolID {
		t.Fatalf("non-stream tool_use id %q collided with prior streamed tool_use id %q", nonStreamID1, streamToolID)
	}
	if nonStreamID1 == nonStreamID2 {
		t.Fatalf("non-stream tool_use ids repeated across turns: first=%q second=%q", nonStreamID1, nonStreamID2)
	}
}

func geminiClaudeInputJSONDeltas(t *testing.T, streamText string) string {
	t.Helper()
	var builder strings.Builder
	for _, line := range strings.Split(streamText, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if gjson.Get(payload, "delta.type").String() == "input_json_delta" {
			builder.WriteString(gjson.Get(payload, "delta.partial_json").String())
		}
	}
	return builder.String()
}

func TestConvertGeminiResponseToClaude_SanitizesParameterlessToolArguments(t *testing.T) {
	origRequest := []byte(`{"model":"gemini-3-flash","tools":[{"name":"EnterPlanMode","input_schema":{"type":"object","properties":{}}}]}`)
	rawResponse := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"EnterPlanMode","args":{"reason":"Plan mode switch"}}}],"role":"model"},"finishReason":"STOP"}],"modelVersion":"gemini-3-flash","responseId":"resp-zero-arg"}`)

	ctx := context.Background()

	nonStream := ConvertGeminiResponseToClaudeNonStream(ctx, "gemini-3-flash", origRequest, origRequest, rawResponse, nil)
	if got := gjson.GetBytes(nonStream, "content.0.input").Raw; got != "{}" {
		t.Fatalf("expected non-streaming input {}, got %s", got)
	}

	var param any
	streamBytes := bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3-flash", origRequest, origRequest, rawResponse, &param), nil)
	if got := geminiClaudeInputJSONDeltas(t, string(streamBytes)); got != "{}" {
		t.Fatalf("expected streaming partial_json {}, got %q (stream: %s)", got, string(streamBytes))
	}
}

func TestConvertGeminiResponseToClaude_CanonicalNameCollisionKeepsExactToolArguments(t *testing.T) {
	origRequest := []byte(`{"model":"gemini-3-flash","tools":[{"name":"Read","input_schema":{"type":"object","properties":{}}},{"name":"read","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`)
	readWithArgs := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read","args":{"path":"/tmp/a"}}}],"role":"model"},"finishReason":"STOP"}],"modelVersion":"gemini-3-flash","responseId":"resp-collision"}`)
	readParameterless := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"Read","args":{"reason":"x"}}}],"role":"model"},"finishReason":"STOP"}],"modelVersion":"gemini-3-flash","responseId":"resp-collision-2"}`)

	ctx := context.Background()

	nonStream := ConvertGeminiResponseToClaudeNonStream(ctx, "gemini-3-flash", origRequest, origRequest, readWithArgs, nil)
	if got := gjson.GetBytes(nonStream, "content.0.input").Raw; got != `{"path":"/tmp/a"}` {
		t.Fatalf("expected exact read tool args preserved, got %s (%s)", got, string(nonStream))
	}
	if got := gjson.GetBytes(nonStream, "content.0.name").String(); got != "read" {
		t.Fatalf("expected exact tool name read, got %q (%s)", got, string(nonStream))
	}

	var param any
	streamBytes := bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3-flash", origRequest, origRequest, readWithArgs, &param), nil)
	if got := geminiClaudeInputJSONDeltas(t, string(streamBytes)); got != `{"path":"/tmp/a"}` {
		t.Fatalf("expected streaming exact read args preserved, got %q (stream: %s)", got, string(streamBytes))
	}

	nonStreamParameterless := ConvertGeminiResponseToClaudeNonStream(ctx, "gemini-3-flash", origRequest, origRequest, readParameterless, nil)
	if got := gjson.GetBytes(nonStreamParameterless, "content.0.input").Raw; got != "{}" {
		t.Fatalf("expected exact Read tool normalized to {}, got %s", got)
	}
}

func TestConvertGeminiResponseToClaude_AmbiguousCanonicalNameKeepsUpstreamNameAndArguments(t *testing.T) {
	origRequest := []byte(`{"model":"gemini-3-flash","tools":[{"name":"Read","input_schema":{"type":"object","properties":{}}},{"name":"read","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`)
	ambiguousResponse := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"READ","args":{"path":"/tmp/a"}}}],"role":"model"},"finishReason":"STOP"}],"modelVersion":"gemini-3-flash","responseId":"resp-ambiguous"}`)

	nonStream := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-3-flash", origRequest, origRequest, ambiguousResponse, nil)
	if got := gjson.GetBytes(nonStream, "content.0.name").String(); got != "READ" {
		t.Fatalf("expected ambiguous upstream name READ preserved, got %q (%s)", got, string(nonStream))
	}
	if got := gjson.GetBytes(nonStream, "content.0.input").Raw; got != `{"path":"/tmp/a"}` {
		t.Fatalf("expected ambiguous tool args preserved, got %s", got)
	}
}

func TestConvertGeminiResponseToClaude_KeepsReasonWhenTranslatedSchemaHasNoPlaceholder(t *testing.T) {
	origRequest := []byte(`{"model":"gemini-3-flash","tools":[{"name":"Bash","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}]}`)
	ctx := context.Background()
	translatedRequest, errConvert := ConvertClaudeRequestToGemini("gemini-3-flash", origRequest, false)
	if errConvert != nil {
		t.Fatalf("ConvertClaudeRequestToGemini error: %v", errConvert)
	}
	rawResponse := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"Bash","args":{"command":"ls","reason":"why"}}}],"role":"model"},"finishReason":"STOP"}],"modelVersion":"gemini-3-flash","responseId":"resp-bash-reason"}`)

	nonStream := ConvertGeminiResponseToClaudeNonStream(ctx, "gemini-3-flash", origRequest, translatedRequest, rawResponse, nil)
	if got := gjson.GetBytes(nonStream, "content.0.input").Raw; got != `{"command":"ls","reason":"why"}` {
		t.Fatalf("expected non-streaming input preserved, got %s (translated: %s)", got, string(translatedRequest))
	}
}

func TestConvertGeminiResponseToClaude_ParameterlessToolContinuationDoesNotDuplicateEmptyObject(t *testing.T) {
	origRequest := []byte(`{"model":"gemini-3-flash","tools":[{"name":"EnterPlanMode","input_schema":{"type":"object","properties":{}}}]}`)
	firstChunk := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"EnterPlanMode","args":{"reason":"Plan mode switch"}}}],"role":"model"}}],"modelVersion":"gemini-3-flash","responseId":"resp-zero-arg"}`)
	continuationChunk := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"args":{"reason":"more"}}}],"role":"model"},"finishReason":"STOP"}],"modelVersion":"gemini-3-flash","responseId":"resp-zero-arg"}`)

	ctx := context.Background()
	var param any
	streamBytes := bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3-flash", origRequest, origRequest, firstChunk, &param), nil)
	streamBytes = append(streamBytes, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3-flash", origRequest, origRequest, continuationChunk, &param), nil)...)
	if got := geminiClaudeInputJSONDeltas(t, string(streamBytes)); got != "{}" {
		t.Fatalf("expected concatenated partial_json {}, got %q (stream: %s)", got, string(streamBytes))
	}
}

func TestConvertGeminiResponseToClaude_ImplicitOpenToolContinuationOrdering(t *testing.T) {
	origRequest := []byte(`{"model":"gemini-3-flash","tools":[{"name":"echo","input_schema":{"type":"object","properties":{}}}]}`)

	for _, tc := range []struct {
		name      string
		firstArgs string
		contArgs  string
		want      string
	}{
		{"empty_then_real_args", `{}`, `{"user":"v"}`, `{"user":"v"}`},
		{"placeholder_then_real_args", `{"reason":"thinking"}`, `{"user":"v"}`, `{"user":"v"}`},
		{"real_args_then_empty", `{"user":"v"}`, `{}`, `{"user":"v"}`},
		{"real_args_then_placeholder", `{"user":"v"}`, `{"reason":"done"}`, `{"user":"v"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			firstChunk := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"echo","args":` + tc.firstArgs + `}}],"role":"model"}}],"modelVersion":"gemini-3-flash","responseId":"resp-echo"}`)
			continuationChunk := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"args":` + tc.contArgs + `}}],"role":"model"},"finishReason":"STOP"}],"modelVersion":"gemini-3-flash","responseId":"resp-echo"}`)

			ctx := context.Background()
			var param any
			streamBytes := bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3-flash", origRequest, origRequest, firstChunk, &param), nil)
			streamBytes = append(streamBytes, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-3-flash", origRequest, origRequest, continuationChunk, &param), nil)...)
			if got := geminiClaudeInputJSONDeltas(t, string(streamBytes)); got != tc.want {
				t.Fatalf("expected concatenated partial_json %s, got %q (stream: %s)", tc.want, got, string(streamBytes))
			}
		})
	}
}
