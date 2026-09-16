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

	thinkingStop := strings.Index(outputText, `"type":"content_block_stop","index":0`)
	textStart := strings.Index(outputText, `"content_block":{"type":"text"`)
	if textStart >= 0 && (thinkingStop < 0 || textStart < thinkingStop) {
		t.Fatalf("signature-only part must not open a text block before thinking is closed: %s", outputText)
	}
	if !strings.Contains(outputText, `"type":"signature_delta"`) || !strings.Contains(outputText, `"signature":"sig-test"`) {
		t.Fatalf("signature-only part must be emitted as a thinking signature delta: %s", outputText)
	}
	if got := strings.Count(outputText, `"type":"content_block_stop","index":0`); got != 1 {
		t.Fatalf("expected exactly one stop for thinking index 0, got %d: %s", got, outputText)
	}
	assertClaudeEmptyTextPadBeforeMessageDelta(t, outputText)
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
	if thinkingBlock.Get("thinking").String() != "inferred reasoning" {
		t.Fatalf("unexpected thinking content: %s", thinkingBlock.Get("thinking").String())
	}
	if thinkingBlock.Get("signature").String() != "sig-snake-case" {
		t.Fatalf("expected signature 'sig-snake-case', got %q. Output: %s", thinkingBlock.Get("signature").String(), string(output))
	}

	textBlock := blocks[1]
	if textBlock.Get("type").String() != "text" || textBlock.Get("text").String() != "final answer" {
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

func assertClaudeEmptyTextPadBeforeMessageDelta(t *testing.T, output string) {
	t.Helper()
	emptyTextAt := strings.Index(output, `"content_block":{"type":"text","text":""}`)
	deltaAt := strings.Index(output, `"type":"message_delta"`)
	if emptyTextAt < 0 {
		t.Fatalf("expected empty text content block:\n%s", output)
	}
	if deltaAt < 0 {
		t.Fatalf("expected message_delta:\n%s", output)
	}
	if emptyTextAt > deltaAt {
		t.Fatalf("empty text block must appear before message_delta:\n%s", output)
	}
	if strings.Contains(output, `"stop_reason":"max_tokens"`) {
		t.Fatalf("must not forge max_tokens:\n%s", output)
	}
}

func TestConvertGeminiResponseToClaudeStream_ThinkingOnlyStopPadsEmptyText(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]}`)
	thinkingChunk := []byte(`{
		"candidates": [{"content": {"parts": [{"text": "hidden reasoning", "thought": true}]}}],
		"modelVersion": "gemini-2.5-pro",
		"responseId": "resp-thinking-only"
	}`)
	finishChunk := []byte(`{
		"candidates": [{"content": {"parts": []}, "finishReason": "STOP"}],
		"usageMetadata": {"promptTokenCount": 10, "thoughtsTokenCount": 4, "totalTokenCount": 14},
		"modelVersion": "gemini-2.5-pro",
		"responseId": "resp-thinking-only"
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-2.5-pro", requestJSON, requestJSON, thinkingChunk, &param), nil)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-2.5-pro", requestJSON, requestJSON, finishChunk, &param), nil)...)
	output = append(output, bytes.Join(ConvertGeminiResponseToClaude(ctx, "gemini-2.5-pro", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	assertClaudeEmptyTextPadBeforeMessageDelta(t, outputText)
	if !strings.Contains(outputText, `"stop_reason":"end_turn"`) {
		t.Fatalf("stop_reason want end_turn:\n%s", outputText)
	}
}

func TestConvertGeminiResponseToClaudeStream_ThinkingOnlyMaxTokensDoesNotPad(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]}`)
	chunk := []byte(`{
		"candidates": [{
			"content": {"parts": [{"text": "hidden reasoning", "thought": true}]},
			"finishReason": "MAX_TOKENS"
		}],
		"usageMetadata": {"promptTokenCount": 10, "thoughtsTokenCount": 4, "totalTokenCount": 14},
		"modelVersion": "gemini-2.5-pro",
		"responseId": "resp-thinking-max"
	}`)

	var param any
	output := bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-2.5-pro", requestJSON, requestJSON, chunk, &param), nil)
	outputText := string(output)
	if strings.Contains(outputText, `"content_block":{"type":"text"`) {
		t.Fatalf("MAX_TOKENS thinking-only stop must not pad empty text:\n%s", outputText)
	}
	if !strings.Contains(outputText, `"stop_reason":"max_tokens"`) {
		t.Fatalf("stop_reason want max_tokens:\n%s", outputText)
	}
}

func TestConvertGeminiResponseToClaudeNonStream_ThinkingOnlyStopPadsEmptyText(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]}`)
	geminiResponse := []byte(`{
		"candidates": [{
			"content": {"parts": [{"text": "hidden reasoning", "thought": true}]},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 10, "thoughtsTokenCount": 4, "totalTokenCount": 14},
		"modelVersion": "gemini-2.5-pro",
		"responseId": "resp-thinking-only-ns"
	}`)

	output := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-2.5-pro", requestJSON, requestJSON, geminiResponse, nil)
	blocks := gjson.GetBytes(output, "content").Array()
	if len(blocks) != 2 {
		t.Fatalf("content blocks = %d, want thinking + empty text: %s", len(blocks), output)
	}
	if blocks[0].Get("type").String() != "thinking" || blocks[0].Get("thinking").String() != "hidden reasoning" {
		t.Fatalf("unexpected thinking block: %s", output)
	}
	if blocks[1].Get("type").String() != "text" || blocks[1].Get("text").String() != "" {
		t.Fatalf("expected empty text pad, got: %s", output)
	}
	if got := gjson.GetBytes(output, "stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn: %s", got, output)
	}
}

func TestConvertGeminiResponseToClaudeNonStream_ThinkingOnlyMaxTokensDoesNotPad(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]}`)
	geminiResponse := []byte(`{
		"candidates": [{
			"content": {"parts": [{"text": "hidden reasoning", "thought": true}]},
			"finishReason": "MAX_TOKENS"
		}],
		"usageMetadata": {"promptTokenCount": 10, "thoughtsTokenCount": 4, "totalTokenCount": 14},
		"modelVersion": "gemini-2.5-pro",
		"responseId": "resp-thinking-max-ns"
	}`)

	output := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-2.5-pro", requestJSON, requestJSON, geminiResponse, nil)
	if got := gjson.GetBytes(output, "content.#").Int(); got != 1 {
		t.Fatalf("MAX_TOKENS must not pad empty text, content.# = %d: %s", got, output)
	}
	if got := gjson.GetBytes(output, "content.0.type").String(); got != "thinking" {
		t.Fatalf("content.0.type = %q, want thinking: %s", got, output)
	}
	if got := gjson.GetBytes(output, "stop_reason").String(); got != "max_tokens" {
		t.Fatalf("stop_reason = %q, want max_tokens: %s", got, output)
	}
}
