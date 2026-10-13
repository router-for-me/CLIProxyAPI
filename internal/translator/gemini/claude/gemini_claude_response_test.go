package claude

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

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

	if !strings.Contains(outputText, `"type":"message_delta"`) {
		t.Fatalf("expected message_delta in output:\n%s", outputText)
	}
	if !strings.Contains(outputText, `"type":"message_stop"`) {
		t.Fatalf("expected message_stop in output:\n%s", outputText)
	}

	var foundDelta bool
	for _, line := range strings.Split(outputText, "\n") {
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"type":"message_delta"`) {
			foundDelta = true
			deltaJSON := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if got := deltaJSON.Get("delta.stop_reason").String(); got != "refusal" {
				t.Fatalf("stop_reason = %q, want refusal. Payload: %s", got, line)
			}
		}
	}
	if !foundDelta {
		t.Fatalf("failed to find message_delta in output:\n%s", outputText)
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
		{finishReason: "MALFORMED_FUNCTION_CALL", wantReason: "refusal"},
		{finishReason: "RECITATION", wantReason: "refusal"},
		{finishReason: "PROHIBITED_CONTENT", wantReason: "refusal"},
		{finishReason: "SPII", wantReason: "refusal"},
		{finishReason: "BLOCKLIST", wantReason: "refusal"},
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
