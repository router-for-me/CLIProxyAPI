package claude

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ============================================================================
// Signature Caching Tests
// ============================================================================

func TestConvertAntigravityResponseToClaudeNonStream_WebSearchGrounding(t *testing.T) {
	requestJSON := []byte(`{
		"model": "gemini-3.1-flash-lite",
		"tools": [{"type": "web_search_20250305", "name": "web_search"}]
	}`)
	translatedRequestJSON := []byte(`{"model":"gemini-3.1-flash-lite","request":{"tools":[{"googleSearch":{}}]}}`)
	responseJSON := testAntigravityGroundingResponse()

	output := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "gemini-3.1-flash-lite", requestJSON, translatedRequestJSON, responseJSON, nil)

	if got := gjson.GetBytes(output, "content.0.type").String(); got != "server_tool_use" {
		t.Fatalf("first content block = %q, want server_tool_use: %s", got, output)
	}
	if got := gjson.GetBytes(output, "content.1.type").String(); got != "web_search_tool_result" {
		t.Fatalf("second content block = %q, want web_search_tool_result: %s", got, output)
	}
	if got := gjson.GetBytes(output, "usage.server_tool_use.web_search_requests").Int(); got != 1 {
		t.Fatalf("web_search_requests = %d, want 1: %s", got, output)
	}
	if got := gjson.GetBytes(output, "content.1.content.0.url").String(); got != "https://example.com/weather" {
		t.Fatalf("search result url = %q: %s", got, output)
	}
	if got := gjson.GetBytes(output, "content.2.citations.0.url").String(); got != "https://example.com/weather" {
		t.Fatalf("citation url = %q: %s", got, output)
	}
}

func TestConvertAntigravityResponseToClaudeNonStream_WebSearchGroundingRequiresNativeGoogleSearch(t *testing.T) {
	requestJSON := []byte(`{
		"model": "gemini-3-flash-agent",
		"tools": [{"type": "web_search_20250305", "name": "web_search"}]
	}`)
	translatedRequestJSON := []byte(`{"model":"gemini-3-flash-agent","request":{"contents":[]}}`)
	responseJSON := testAntigravityGroundingResponse()

	output := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "gemini-3-flash-agent", requestJSON, translatedRequestJSON, responseJSON, nil)

	if got := gjson.GetBytes(output, "content.0.type").String(); got == "server_tool_use" {
		t.Fatalf("non-native translated request should not synthesize server_tool_use: %s", output)
	}
	if got := gjson.GetBytes(output, "usage.server_tool_use.web_search_requests").Int(); got != 0 {
		t.Fatalf("web_search_requests = %d, want 0: %s", got, output)
	}
}

func TestConvertAntigravityResponseToClaudeStream_WebSearchGrounding(t *testing.T) {
	requestJSON := []byte(`{
		"model": "gemini-3.1-flash-lite",
		"tools": [{"type": "web_search_20250305", "name": "web_search"}]
	}`)
	translatedRequestJSON := []byte(`{"model":"gemini-3.1-flash-lite","request":{"tools":[{"googleSearch":{}}]}}`)

	var param any
	output := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3.1-flash-lite", requestJSON, translatedRequestJSON, testAntigravityGroundingResponse(), &param), nil)
	output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3.1-flash-lite", requestJSON, translatedRequestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	for _, needle := range []string{
		`"type":"server_tool_use"`,
		`"type":"web_search_tool_result"`,
		`"web_search_requests":1`,
		`"type":"citations_delta"`,
		`event: message_stop`,
	} {
		if !strings.Contains(outputText, needle) {
			t.Fatalf("stream output missing %s:\n%s", needle, outputText)
		}
	}
}

func TestConvertAntigravityResponseToClaudeStream_WebSearchBuffersTextUntilGrounding(t *testing.T) {
	requestJSON := []byte(`{
		"model": "gemini-3.1-flash-lite",
		"tools": [{"type": "web_search_20250305", "name": "web_search"}]
	}`)
	translatedRequestJSON := []byte(`{"model":"gemini-3.1-flash-lite","request":{"tools":[{"googleSearch":{}}]}}`)

	var param any
	firstChunk := []byte(`{
		"response": {
			"modelVersion": "gemini-3.1-flash-lite",
			"responseId": "resp-web-search-stream",
			"candidates": [{
				"content": {
					"parts": [{"text": "Beijing weather "}]
				}
			}],
			"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 2, "totalTokenCount": 12}
		}
	}`)
	finalChunk := []byte(`{
		"response": {
			"modelVersion": "gemini-3.1-flash-lite",
			"responseId": "resp-web-search-stream",
			"candidates": [{
				"content": {
					"parts": [{"text": "is clear today."}]
				},
				"groundingMetadata": {
					"webSearchQueries": ["Beijing weather"],
					"groundingChunks": [{"web": {"uri": "https://example.com/weather", "title": "Beijing Weather"}}],
					"groundingSupports": [{
						"segment": {"startIndex": 0, "endIndex": 31, "text": "Beijing weather is clear today."},
						"groundingChunkIndices": [0]
					}]
				},
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 6, "totalTokenCount": 16}
		}
	}`)

	output := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3.1-flash-lite", requestJSON, translatedRequestJSON, firstChunk, &param), nil)
	output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3.1-flash-lite", requestJSON, translatedRequestJSON, finalChunk, &param), nil)...)
	output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3.1-flash-lite", requestJSON, translatedRequestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	textStart := strings.Index(outputText, `"content_block":{"type":"text"`)
	serverToolStart := strings.Index(outputText, `"content_block":{"type":"server_tool_use"`)
	if serverToolStart < 0 {
		t.Fatalf("stream output missing server_tool_use:\n%s", outputText)
	}
	if textStart >= 0 && textStart < serverToolStart {
		t.Fatalf("text block was emitted before server_tool_use:\n%s", outputText)
	}
	if strings.Contains(outputText, `"index":0,"content_block":{"type":"text"`) {
		t.Fatalf("index 0 must be reserved for server_tool_use:\n%s", outputText)
	}
	if !strings.Contains(outputText, `"index":0,"content_block":{"type":"server_tool_use"`) {
		t.Fatalf("server_tool_use must use index 0:\n%s", outputText)
	}
	if !strings.Contains(outputText, `"index":1,"content_block":{"type":"web_search_tool_result"`) {
		t.Fatalf("web_search_tool_result must use index 1:\n%s", outputText)
	}
	if !strings.Contains(outputText, `Beijing weather is clear today.`) {
		t.Fatalf("buffered text was not emitted after web search blocks:\n%s", outputText)
	}
}

func TestConvertAntigravityResponseToClaudeStream_WebSearchMessageStartOutputTokensZero(t *testing.T) {
	requestJSON := []byte(`{
		"model": "gemini-3.1-flash-lite",
		"tools": [{"type": "web_search_20250305", "name": "web_search"}]
	}`)
	translatedRequestJSON := []byte(`{"model":"gemini-3.1-flash-lite","request":{"tools":[{"googleSearch":{}}]}}`)
	responseJSON := []byte(`{
		"response": {
			"modelVersion": "gemini-3.1-flash-lite",
			"responseId": "resp-web-search-start",
			"candidates": [{
				"content": {"parts": [{"text": "Beijing weather"}]}
			}],
			"cpaUsageMetadata": {"promptTokenCount": 85, "candidatesTokenCount": 43}
		}
	}`)

	var param any
	output := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3.1-flash-lite", requestJSON, translatedRequestJSON, responseJSON, &param), nil)
	messageStart := sseDataForEvent(t, string(output), "message_start")

	if got := gjson.Get(messageStart, "message.usage.output_tokens").Int(); got != 0 {
		t.Fatalf("message_start output_tokens = %d, want 0: %s", got, messageStart)
	}
}

func TestConvertAntigravityResponseToClaudeNonStream_EmptyCandidateReturnsContentArray(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-3-flash-agent"}`)
	output := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "gemini-3-flash-agent", requestJSON, requestJSON, testEmptyAntigravityResponse(), nil)

	content := gjson.GetBytes(output, "content")
	if !content.IsArray() || len(content.Array()) != 0 {
		t.Fatalf("content = %s, want empty array: %s", content.Raw, output)
	}
	if got := gjson.GetBytes(output, "stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn: %s", got, output)
	}
}

func TestConvertAntigravityResponseToClaudeStream_EmptyCandidateClosesMessage(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-3-flash-agent"}`)
	responseJSON := testEmptyAntigravityResponse()

	var param any
	output := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3-flash-agent", requestJSON, requestJSON, responseJSON, &param), nil)
	output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3-flash-agent", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	lastIndex := -1
	for _, eventName := range []string{"message_start", "content_block_start", "content_block_stop", "message_delta", "message_stop"} {
		index := strings.Index(outputText, "event: "+eventName+"\n")
		if index < 0 {
			t.Fatalf("event %q not found in:\n%s", eventName, outputText)
		}
		if index <= lastIndex {
			t.Fatalf("event %q is out of order in:\n%s", eventName, outputText)
		}
		lastIndex = index
	}

	contentBlockStart := sseDataForEvent(t, outputText, "content_block_start")
	if got := gjson.Get(contentBlockStart, "content_block.type").String(); got != "text" {
		t.Fatalf("empty content block type = %q, want text: %s", got, contentBlockStart)
	}
	if text := gjson.Get(contentBlockStart, "content_block.text"); !text.Exists() || text.String() != "" {
		t.Fatalf("empty content block text = %s, want empty string: %s", text.Raw, contentBlockStart)
	}

	messageDelta := sseDataForEvent(t, outputText, "message_delta")
	if got := gjson.Get(messageDelta, "delta.stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn: %s", got, messageDelta)
	}
	if got := gjson.Get(messageDelta, "usage.input_tokens").Int(); got != 64214 {
		t.Fatalf("input_tokens = %d, want 64214: %s", got, messageDelta)
	}
	if got := gjson.Get(messageDelta, "usage.output_tokens").Int(); got != 0 {
		t.Fatalf("output_tokens = %d, want 0: %s", got, messageDelta)
	}
}

func testEmptyAntigravityResponse() []byte {
	return []byte(`{
		"response": {
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": ""}]},
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 64214, "totalTokenCount": 64214},
			"modelVersion": "gemini-3-flash-a",
			"responseId": "eBNcat8X5evPsg_lhqyQAg"
		}
	}`)
}

func TestWebSearchResultsFromGrounding_DeduplicatesAndSkipsEmptyURLs(t *testing.T) {
	groundingMetadata := gjson.Parse(`{
		"groundingChunks": [
			{"web": {"uri": "https://example.com/a", "title": "A"}},
			{"web": {"uri": "https://example.com/b", "title": "B"}},
			{"web": {"uri": "https://example.com/a", "title": "A duplicate"}},
			{"web": {"uri": "", "title": "Empty"}}
		]
	}`)

	results := webSearchResultsFromGrounding(groundingMetadata)

	if got := gjson.GetBytes(results, "#").Int(); got != 2 {
		t.Fatalf("result count = %d, want 2: %s", got, string(results))
	}
	if got := gjson.GetBytes(results, "0.url").String(); got != "https://example.com/a" {
		t.Fatalf("first url = %q: %s", got, string(results))
	}
	if got := gjson.GetBytes(results, "1.url").String(); got != "https://example.com/b" {
		t.Fatalf("second url = %q: %s", got, string(results))
	}
}

func TestBuildWebSearchCitedTextBlocks_TrimsOverlappingGroundingSupports(t *testing.T) {
	first := "北京今天晴"
	second := "北京今天晴，气温19到31度"
	textContent := second + "。"

	blocks := buildWebSearchCitedTextBlocks(textContent, []webSearchGroundingSupport{
		{
			StartIndex: 0,
			EndIndex:   int64(len([]byte(first))),
			Text:       first,
			ChunkURLs:  []string{"https://example.com/weather"},
			ChunkTitle: "Weather",
		},
		{
			StartIndex: 0,
			EndIndex:   int64(len([]byte(second))),
			Text:       second,
			ChunkURLs:  []string{"https://example.com/weather"},
			ChunkTitle: "Weather",
		},
	})

	var got strings.Builder
	for _, block := range blocks {
		got.WriteString(block.Text)
	}
	if got.String() != textContent {
		t.Fatalf("joined text = %q, want %q", got.String(), textContent)
	}
	if len(blocks) < 2 || blocks[1].Text != "，气温19到31度" {
		t.Fatalf("overlap suffix block not trimmed correctly: %#v", blocks)
	}
	if gotCitation := blocks[1].Citations[0]["cited_text"]; gotCitation != blocks[1].Text {
		t.Fatalf("cited_text = %q, want emitted text %q", gotCitation, blocks[1].Text)
	}
}

func sseDataForEvent(t *testing.T, output string, eventName string) string {
	t.Helper()

	currentEvent := ""
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}
		if currentEvent == eventName && strings.HasPrefix(line, "data: ") {
			return strings.TrimPrefix(line, "data: ")
		}
	}

	t.Fatalf("event %q not found in:\n%s", eventName, output)
	return ""
}

func testAntigravityGroundingResponse() []byte {
	resp := map[string]any{
		"response": map[string]any{
			"responseId":   "resp-web-search",
			"modelVersion": "gemini-3.1-flash-lite",
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{
							map[string]any{"text": "Beijing weather is clear today."},
						},
					},
					"groundingMetadata": map[string]any{
						"webSearchQueries": []any{"Beijing weather June 10 2026"},
						"groundingChunks": []any{
							map[string]any{
								"web": map[string]any{
									"uri":   "https://example.com/weather",
									"title": "Beijing Weather",
								},
							},
						},
						"groundingSupports": []any{
							map[string]any{
								"segment": map[string]any{
									"startIndex": int64(0),
									"endIndex":   int64(31),
									"text":       "Beijing weather is clear today.",
								},
								"groundingChunkIndices": []any{0},
							},
						},
					},
					"finishReason": "STOP",
				},
			},
			"usageMetadata": map[string]any{
				"promptTokenCount":     10,
				"candidatesTokenCount": 6,
				"totalTokenCount":      16,
			},
		},
	}
	raw, _ := json.Marshal(resp)
	return raw
}

func TestConvertAntigravityResponseToClaude_ParamsInitialized(t *testing.T) {
	cache.ClearSignatureCache("")

	// Request with user message - should initialize params
	requestJSON := []byte(`{
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Hello world"}]}
		]
	}`)

	// First response chunk with thinking
	responseJSON := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "Let me think...", "thought": true}]
				}
			}]
		}
	}`)

	var param any
	ctx := context.Background()
	ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, responseJSON, &param)

	params := param.(*Params)
	if !params.HasFirstResponse {
		t.Error("HasFirstResponse should be set after first chunk")
	}
	if params.CurrentThinkingText.Len() == 0 {
		t.Error("Thinking text should be accumulated")
	}
}

func TestConvertAntigravityResponseToClaude_ThinkingTextAccumulated(t *testing.T) {
	cache.ClearSignatureCache("")

	requestJSON := []byte(`{
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Test"}]}]
	}`)

	// First thinking chunk
	chunk1 := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "First part of thinking...", "thought": true}]
				}
			}]
		}
	}`)

	// Second thinking chunk (continuation)
	chunk2 := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": " Second part of thinking...", "thought": true}]
				}
			}]
		}
	}`)

	var param any
	ctx := context.Background()

	// Process first chunk - starts new thinking block
	ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, chunk1, &param)
	params := param.(*Params)

	if params.CurrentThinkingText.Len() == 0 {
		t.Error("Thinking text should be accumulated after first chunk")
	}

	// Process second chunk - continues thinking block
	ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, chunk2, &param)

	text := params.CurrentThinkingText.String()
	if !strings.Contains(text, "First part") || !strings.Contains(text, "Second part") {
		t.Errorf("Thinking text should accumulate both parts, got: %s", text)
	}
}

func TestConvertAntigravityResponseToClaude_SignatureCached(t *testing.T) {
	cache.ClearSignatureCache("")

	requestJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Cache test"}]}]
	}`)

	// Thinking chunk
	thinkingChunk := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "My thinking process here", "thought": true}]
				}
			}]
		}
	}`)

	// Signature chunk
	validSignature := "abc123validSignature1234567890123456789012345678901234567890"
	signatureChunk := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "", "thought": true, "thoughtSignature": "` + validSignature + `"}]
				}
			}]
		}
	}`)

	var param any
	ctx := context.Background()

	// Process thinking chunk
	ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, thinkingChunk, &param)
	params := param.(*Params)
	thinkingText := params.CurrentThinkingText.String()

	if thinkingText == "" {
		t.Fatal("Thinking text should be accumulated")
	}

	// Process signature chunk - should cache the signature
	ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, signatureChunk, &param)

	// Verify signature was cached
	cachedSig := cache.GetCachedSignature("claude-sonnet-4-5-thinking", thinkingText)
	if cachedSig != validSignature {
		t.Errorf("Expected cached signature '%s', got '%s'", validSignature, cachedSig)
	}

	// Verify thinking text was reset after caching
	if params.CurrentThinkingText.Len() != 0 {
		t.Error("Thinking text should be reset after signature is cached")
	}
}

func TestConvertAntigravityResponseToClaude_MultipleThinkingBlocks(t *testing.T) {
	cache.ClearSignatureCache("")

	requestJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Multi block test"}]}]
	}`)

	validSig1 := "signature1_12345678901234567890123456789012345678901234567"
	validSig2 := "signature2_12345678901234567890123456789012345678901234567"

	// First thinking block with signature
	block1Thinking := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "First thinking block", "thought": true}]
				}
			}]
		}
	}`)
	block1Sig := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "", "thought": true, "thoughtSignature": "` + validSig1 + `"}]
				}
			}]
		}
	}`)

	// Text content (breaks thinking)
	textBlock := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "Regular text output"}]
				}
			}]
		}
	}`)

	// Second thinking block with signature
	block2Thinking := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "Second thinking block", "thought": true}]
				}
			}]
		}
	}`)
	block2Sig := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "", "thought": true, "thoughtSignature": "` + validSig2 + `"}]
				}
			}]
		}
	}`)

	var param any
	ctx := context.Background()

	// Process first thinking block
	ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, block1Thinking, &param)
	params := param.(*Params)
	firstThinkingText := params.CurrentThinkingText.String()

	ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, block1Sig, &param)

	// Verify first signature cached
	if cache.GetCachedSignature("claude-sonnet-4-5-thinking", firstThinkingText) != validSig1 {
		t.Error("First thinking block signature should be cached")
	}

	// Process text (transitions out of thinking)
	ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, textBlock, &param)

	// Process second thinking block
	ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, block2Thinking, &param)
	secondThinkingText := params.CurrentThinkingText.String()

	ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, block2Sig, &param)

	// Verify second signature cached
	if cache.GetCachedSignature("claude-sonnet-4-5-thinking", secondThinkingText) != validSig2 {
		t.Error("Second thinking block signature should be cached")
	}
}

func TestConvertAntigravityResponseToClaude_TextAndSignatureInSameChunk(t *testing.T) {
	cache.ClearSignatureCache("")

	requestJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Test"}]}]
	}`)

	validSignature := "RtestSig1234567890123456789012345678901234567890123456789"

	// Chunk 1: thinking text only (no signature)
	chunk1 := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "First part.", "thought": true}]
				}
			}]
		}
	}`)

	// Chunk 2: thinking text AND signature in the same part
	chunk2 := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": " Second part.", "thought": true, "thoughtSignature": "` + validSignature + `"}]
				}
			}]
		}
	}`)

	var param any
	ctx := context.Background()

	result1 := ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, chunk1, &param)
	result2 := ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, chunk2, &param)

	allOutput := string(bytes.Join(result1, nil)) + string(bytes.Join(result2, nil))

	// The text " Second part." must appear as a thinking_delta, not be silently dropped
	if !strings.Contains(allOutput, "Second part.") {
		t.Error("Text co-located with signature must be emitted as thinking_delta before the signature")
	}

	// The signature must also be emitted
	if !strings.Contains(allOutput, "signature_delta") {
		t.Error("Signature delta must still be emitted")
	}

	// Verify the cached signature covers the FULL text (both parts)
	fullText := "First part. Second part."
	cachedSig := cache.GetCachedSignature("claude-sonnet-4-5-thinking", fullText)
	if cachedSig != validSignature {
		t.Errorf("Cached signature should cover full text %q, got sig=%q", fullText, cachedSig)
	}
}

func TestConvertAntigravityResponseToClaude_SignatureOnlyChunk(t *testing.T) {
	cache.ClearSignatureCache("")

	requestJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Test"}]}]
	}`)

	validSignature := "RtestSig1234567890123456789012345678901234567890123456789"

	// Chunk 1: thinking text
	chunk1 := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "Full thinking text.", "thought": true}]
				}
			}]
		}
	}`)

	// Chunk 2: signature only (empty text) — the normal case
	chunk2 := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "", "thought": true, "thoughtSignature": "` + validSignature + `"}]
				}
			}]
		}
	}`)

	var param any
	ctx := context.Background()

	ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, chunk1, &param)
	ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, chunk2, &param)

	cachedSig := cache.GetCachedSignature("claude-sonnet-4-5-thinking", "Full thinking text.")
	if cachedSig != validSignature {
		t.Errorf("Signature-only chunk should still cache correctly, got %q", cachedSig)
	}
}

func TestConvertAntigravityResponseToClaude_SignatureOnlyChunkWithoutThoughtFlag(t *testing.T) {
	cache.ClearSignatureCache("")

	requestJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Test"}]}]
	}`)

	validSignature := "RtestSig1234567890123456789012345678901234567890123456789"

	chunk1 := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "Full thinking text.", "thought": true}]
				}
			}],
			"modelVersion": "claude-sonnet-4-5-thinking",
			"responseId": "resp-test"
		}
	}`)

	chunk2 := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "", "thoughtSignature": "` + validSignature + `"}]
				},
				"finishReason": "STOP"
			}],
			"usageMetadata": {
				"promptTokenCount": 10,
				"thoughtsTokenCount": 2,
				"totalTokenCount": 12
			},
			"modelVersion": "claude-sonnet-4-5-thinking",
			"responseId": "resp-test"
		}
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, chunk1, &param), nil)
	output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, chunk2, &param), nil)...)
	output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5-thinking", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	if strings.Contains(outputText, `"content_block":{"type":"text"`) {
		t.Fatalf("signature-only part must not open an empty text block: %s", outputText)
	}
	if strings.Contains(outputText, `"type":"content_block_stop","index":1`) {
		t.Fatalf("signature-only part must not produce a stop for unopened index 1: %s", outputText)
	}
	if !strings.Contains(outputText, `"type":"signature_delta"`) {
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

	cachedSig := cache.GetCachedSignature("claude-sonnet-4-5-thinking", "Full thinking text.")
	if cachedSig != validSignature {
		t.Fatalf("signature-only chunk without thought flag should still cache correctly, got %q", cachedSig)
	}
}

func TestConvertAntigravityResponseToClaude_VisibleGeminiSignatureUsesLeadingCarrier(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-3.6-flash-high"}`)
	validSignature := testGeminiEPrefixSignature(t)
	chunk := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"visible answer","thoughtSignature":"` + validSignature + `"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.6-flash","responseId":"resp-visible-sig"}}`)
	var param any
	output := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, chunk, &param), nil)
	outputText := string(output)
	carrierPos := strings.Index(outputText, `"content_block":{"type":"thinking","thinking":""}`)
	carrierSignature := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierNext, geminiClaudeCarrierText)
	signaturePos := strings.Index(outputText, `"type":"signature_delta","signature":"`+carrierSignature+`"`)
	textPos := strings.Index(outputText, `"type":"text_delta","text":"visible answer"`)
	if carrierPos < 0 || signaturePos < carrierPos || textPos < signaturePos {
		t.Fatalf("visible signature carrier must precede text: %s", output)
	}
}

func TestConvertAntigravityResponseToClaude_ThoughtThenSignedFunctionUsesOneThinkingBlock(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-3.6-flash-high"}`)
	validSignature := testGeminiEPrefixSignature(t)
	chunks := [][]byte{
		[]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hidden thought","thought":true}]}}],"modelVersion":"gemini-3.6-flash","responseId":"resp-thought-tool"}}`),
		[]byte(`{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + validSignature + `","functionCall":{"name":"run_command","args":{"command":"true"}}}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.6-flash","responseId":"resp-thought-tool"}}`),
	}
	var param any
	var output []byte
	for _, chunk := range chunks {
		output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, chunk, &param), nil)...)
	}
	outputText := string(output)
	if got := strings.Count(outputText, `"content_block":{"type":"thinking"`); got != 1 {
		t.Fatalf("thinking block count = %d, want one signed thought block: %s", got, output)
	}
	carrierSignature := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierNext, geminiClaudeCarrierFunction)
	signaturePos := strings.Index(outputText, `"type":"signature_delta","signature":"`+carrierSignature+`"`)
	toolPos := strings.Index(outputText, `"content_block":{"type":"tool_use"`)
	if signaturePos < 0 || toolPos < signaturePos {
		t.Fatalf("signed thinking block must precede tool: %s", output)
	}
}

func TestConvertAntigravityResponseToClaude_DetachedGeminiSignatureAfterVisibleText(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-3.6-flash-high","messages":[{"role":"user","content":[{"type":"text","text":"Test"}]}]}`)
	validSignature := testGeminiEPrefixSignature(t)
	chunks := [][]byte{
		[]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"visible answer"}]}}],"modelVersion":"gemini-3.6-flash","responseId":"resp-detached"}}`),
		[]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + validSignature + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15},"modelVersion":"gemini-3.6-flash","responseId":"resp-detached"}}`),
	}

	var param any
	var output []byte
	for _, chunk := range chunks {
		output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, chunk, &param), nil)...)
	}
	output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	if !strings.Contains(outputText, `"content_block":{"type":"text","text":""}`) {
		t.Fatalf("missing visible text block: %s", outputText)
	}
	if !strings.Contains(outputText, `"content_block":{"type":"thinking","thinking":""}`) {
		t.Fatalf("missing detached thinking carrier: %s", outputText)
	}
	carrierSignature := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierPrevious, geminiClaudeCarrierText)
	if !strings.Contains(outputText, `"type":"signature_delta","signature":"`+carrierSignature+`"`) {
		t.Fatalf("missing detached Gemini signature: %s", outputText)
	}
	if got := strings.Count(outputText, `"type":"content_block_stop"`); got != 2 {
		t.Fatalf("content block stops = %d, want text + detached thinking; output=%s", got, outputText)
	}
}

func TestConvertAntigravityResponseToClaude_GeminiToolSignature(t *testing.T) {
	requestJSON := []byte(`{"model":"gemini-3.6-flash-high","messages":[{"role":"user","content":[{"type":"text","text":"Test"}]}]}`)
	validSignature := testGeminiEPrefixSignature(t)
	chunk := []byte(`{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + validSignature + `","functionCall":{"id":"native-id","name":"run_command","args":{"command":"true"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15},"modelVersion":"gemini-3.6-flash","responseId":"resp-tool"}}`)

	var param any
	output := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, chunk, &param), nil)
	outputText := string(output)
	carrierPos := strings.Index(outputText, `"content_block":{"type":"thinking","thinking":""}`)
	carrierSignature := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierNext, geminiClaudeCarrierFunction)
	signaturePos := strings.Index(outputText, `"type":"signature_delta","signature":"`+carrierSignature+`"`)
	toolPos := strings.Index(outputText, `"content_block":{"type":"tool_use"`)
	if carrierPos < 0 || signaturePos < carrierPos || toolPos < signaturePos {
		t.Fatalf("tool signature carrier must precede tool_use: %s", output)
	}
}

func differentClaudeGeminiSignature(t *testing.T) string {
	t.Helper()
	raw, errDecode := base64.StdEncoding.DecodeString(testGeminiEPrefixSignature(t))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	raw[len(raw)-1] ^= 1
	return base64.StdEncoding.EncodeToString(raw)
}

func TestConvertAntigravityResponseToClaude_PreservesClaudeThoughtAndToolSignatures(t *testing.T) {
	previousCache := cache.SignatureCacheEnabled()
	cache.SetSignatureCacheEnabled(false)
	t.Cleanup(func() { cache.SetSignatureCacheEnabled(previousCache) })

	_, upstreamSig1 := testAntigravityClaudeSignature(t)
	nativePayload2 := buildClaudeSignaturePayload(t, 13, uint64Ptr(2), "claude-opus-4-6", true)
	nativeSig2 := base64.StdEncoding.EncodeToString(nativePayload2)
	upstreamSig2 := base64.StdEncoding.EncodeToString([]byte(nativeSig2))
	requestJSON := []byte(`{"model":"claude-sonnet-4-6"}`)
	responseJSON := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hidden","thought":true,"thoughtSignature":"` + upstreamSig1 + `"},{"functionCall":{"id":"native-id","name":"run_command","args":{"command":"true"}},"thoughtSignature":"` + upstreamSig2 + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"thoughtsTokenCount":1,"totalTokenCount":3},"modelVersion":"claude-sonnet-4-6-thinking","responseId":"resp-claude-thought-tool"}}`)

	nonStream := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "claude-sonnet-4-6", requestJSON, requestJSON, responseJSON, nil)
	content := gjson.GetBytes(nonStream, "content").Array()
	if len(content) != 2 {
		t.Fatalf("content blocks = %d, want thinking + tool; output=%s", len(content), nonStream)
	}
	thinkingCarrierSig := content[0].Get("signature").String()
	toolCarrierSig := content[1].Get("signature").String()
	if thinkingCarrierSig == "" || toolCarrierSig == "" || thinkingCarrierSig == toolCarrierSig {
		t.Fatalf("Claude signatures were not kept on distinct native blocks: %s", nonStream)
	}

	replayRequest := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"assistant","content":[]},{"role":"user","content":[{"type":"text","text":"continue"}]}]}`)
	replayRequest, _ = sjson.SetRawBytes(replayRequest, "messages.0.content", []byte(gjson.GetBytes(nonStream, "content").Raw))
	replayRequest = StripEmptySignatureThinkingBlocks(replayRequest)
	translated := ConvertClaudeRequestToAntigravity("claude-sonnet-4-6", replayRequest, false)
	parts := gjson.GetBytes(translated, "request.contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("thoughtSignature").String() != upstreamSig1 || parts[1].Get("thoughtSignature").String() != upstreamSig2 {
		t.Fatalf("Claude thought/tool signatures did not round-trip: %s", translated)
	}

	var param any
	stream := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "claude-sonnet-4-6", requestJSON, requestJSON, responseJSON, &param), nil)
	streamText := string(stream)
	if got := strings.Count(streamText, `"content_block":{"type":"thinking"`); got != 1 {
		t.Fatalf("stream thinking block count = %d, want 1; output=%s", got, stream)
	}
	if !strings.Contains(streamText, `"content_block":{"type":"tool_use"`) || !strings.Contains(streamText, `"signature":"`+toolCarrierSig+`"`) {
		t.Fatalf("stream tool signature missing: %s", stream)
	}
}

func TestConvertAntigravityResponseToClaudeNonStream_SignedThoughtBeforeUnsignedTextKeepsTarget(t *testing.T) {
	signature := testGeminiEPrefixSignature(t)
	requestJSON := []byte(`{"model":"gemini-3.6-flash-high"}`)
	responseJSON := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hidden","thought":true,"thoughtSignature":"` + signature + `"},{"text":"visible"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.6-flash","responseId":"signed-thought-unsigned-text"}}`)

	output := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, responseJSON, nil)
	replayRequest := []byte(`{"model":"gemini-3.6-flash-high","messages":[{"role":"assistant","content":[]}]}`)
	replayRequest, _ = sjson.SetRawBytes(replayRequest, "messages.0.content", []byte(gjson.GetBytes(output, "content").Raw))
	replayRequest = StripInvalidGeminiSignatureThinkingBlocks(replayRequest)
	translated := ConvertClaudeRequestToAntigravity("gemini-3.6-flash-high", replayRequest, false)
	parts := gjson.GetBytes(translated, "request.contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("text").String() != "hidden" || !parts[0].Get("thought").Bool() || parts[0].Get("thoughtSignature").String() != signature || parts[1].Get("text").String() != "visible" || parts[1].Get("thoughtSignature").String() != "" {
		t.Fatalf("signed thought target changed: output=%s translated=%s", output, translated)
	}
}

func TestConvertAntigravityResponseToClaudeNonStream_PreviousCarrierDoesNotCrossFollowingText(t *testing.T) {
	signature1 := testGeminiEPrefixSignature(t)
	signature2 := differentClaudeGeminiSignature(t)
	requestJSON := []byte(`{"model":"gemini-3.6-flash-high"}`)
	responseJSON := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"A","thoughtSignature":"` + signature1 + `"},{"text":"","thoughtSignature":"` + signature2 + `"},{"text":"B"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.6-flash","responseId":"previous-carrier-boundary"}}`)

	output := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, responseJSON, nil)
	replayRequest := []byte(`{"model":"gemini-3.6-flash-high","messages":[{"role":"assistant","content":[]}]}`)
	replayRequest, _ = sjson.SetRawBytes(replayRequest, "messages.0.content", []byte(gjson.GetBytes(output, "content").Raw))
	replayRequest = StripInvalidGeminiSignatureThinkingBlocks(replayRequest)
	translated := ConvertClaudeRequestToAntigravity("gemini-3.6-flash-high", replayRequest, false)
	parts := gjson.GetBytes(translated, "request.contents.0.parts").Array()
	if len(parts) != 3 || parts[0].Get("text").String() != "A" || parts[0].Get("thoughtSignature").String() != signature1 || !parts[1].Get("text").Exists() || parts[1].Get("text").String() != "" || parts[1].Get("thoughtSignature").String() != signature2 || parts[2].Get("text").String() != "B" || parts[2].Get("thoughtSignature").String() != "" {
		t.Fatalf("previous carrier crossed following text: output=%s translated=%s", output, translated)
	}
}

func TestConvertAntigravityResponseToClaudeNonStream_PreservesDistinctThoughtAndTextSignatures(t *testing.T) {
	sig1 := testGeminiEPrefixSignature(t)
	sig2 := differentClaudeGeminiSignature(t)
	requestJSON := []byte(`{"model":"gemini-3.6-flash-high"}`)
	responseJSON := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hidden","thought":true,"thoughtSignature":"` + sig1 + `"},{"text":"visible","thoughtSignature":"` + sig2 + `"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.6-flash","responseId":"resp-distinct-signatures"}}`)

	output := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, responseJSON, nil)
	content := gjson.GetBytes(output, "content").Array()
	if len(content) != 3 {
		t.Fatalf("content blocks = %d, want signed thought + carrier + text; output=%s", len(content), output)
	}
	if got := content[0].Get("thinking").String(); got != "hidden" {
		t.Fatalf("thought text = %q; output=%s", got, output)
	}
	wantThoughtCarrier := encodeGeminiClaudeCarrierSignature(sig1, geminiClaudeCarrierStandalone, geminiClaudeCarrierText)
	if got := content[0].Get("signature").String(); got != wantThoughtCarrier {
		t.Fatalf("thought signature = %q, want standalone carrier %q; output=%s", got, wantThoughtCarrier, output)
	}
	wantCarrier := encodeGeminiClaudeCarrierSignature(sig2, geminiClaudeCarrierNext, geminiClaudeCarrierText)
	if got := content[1].Get("signature").String(); got != wantCarrier || content[1].Get("thinking").String() != "" {
		t.Fatalf("visible carrier malformed: %s; output=%s", content[1].Raw, output)
	}
	if got := content[2].Get("text").String(); got != "visible" {
		t.Fatalf("visible text = %q; output=%s", got, output)
	}

	replayRequest := []byte(`{"model":"gemini-3.6-flash-high","messages":[{"role":"assistant","content":[]},{"role":"user","content":[{"type":"text","text":"continue"}]}]}`)
	replayRequest, _ = sjson.SetRawBytes(replayRequest, "messages.0.content", []byte(gjson.GetBytes(output, "content").Raw))
	replayRequest = StripInvalidGeminiSignatureThinkingBlocks(replayRequest)
	translated := ConvertClaudeRequestToAntigravity("gemini-3.6-flash-high", replayRequest, false)
	parts := gjson.GetBytes(translated, "request.contents.0.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("replayed parts = %d, want thought + text; translated=%s", len(parts), translated)
	}
	if got := parts[0].Get("thoughtSignature").String(); got != sig1 {
		t.Fatalf("replayed thought signature = %q, want %q; translated=%s", got, sig1, translated)
	}
	if got := parts[1].Get("thoughtSignature").String(); got != sig2 {
		t.Fatalf("replayed text signature = %q, want %q; translated=%s", got, sig2, translated)
	}
}

func TestConvertAntigravityResponseToClaudeStream_PreservesDistinctThoughtAndTextSignatures(t *testing.T) {
	sig1 := testGeminiEPrefixSignature(t)
	sig2 := differentClaudeGeminiSignature(t)
	requestJSON := []byte(`{"model":"gemini-3.6-flash-high"}`)
	chunk := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hidden","thought":true,"thoughtSignature":"` + sig1 + `"},{"text":"visible","thoughtSignature":"` + sig2 + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"thoughtsTokenCount":1,"totalTokenCount":3},"modelVersion":"gemini-3.6-flash","responseId":"resp-distinct-signatures"}}`)
	var param any
	output := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, chunk, &param), nil)
	outputText := string(output)
	if got := strings.Count(outputText, `"content_block":{"type":"thinking"`); got != 2 {
		t.Fatalf("thinking block count = %d, want 2; output=%s", got, output)
	}
	if got := strings.Count(outputText, `"type":"signature_delta"`); got != 2 {
		t.Fatalf("signature delta count = %d, want 2; output=%s", got, output)
	}
	firstCarrier := encodeGeminiClaudeCarrierSignature(sig1, geminiClaudeCarrierStandalone, geminiClaudeCarrierText)
	firstSignature := strings.Index(outputText, `"signature":"`+firstCarrier+`"`)
	secondCarrier := encodeGeminiClaudeCarrierSignature(sig2, geminiClaudeCarrierNext, geminiClaudeCarrierText)
	secondSignature := strings.Index(outputText, `"signature":"`+secondCarrier+`"`)
	visibleText := strings.Index(outputText, `"text":"visible"`)
	if firstSignature < 0 || secondSignature < firstSignature || visibleText < secondSignature {
		t.Fatalf("signature/text order is wrong; output=%s", output)
	}
}

func TestConvertAntigravityResponseToClaude_PreservesConsecutiveDetachedCarriers(t *testing.T) {
	sig1 := testGeminiEPrefixSignature(t)
	sig2 := differentClaudeGeminiSignature(t)
	requestJSON := []byte(`{"model":"gemini-3.6-flash-high"}`)
	responseJSON := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"visible"},{"text":"","thoughtSignature":"` + sig1 + `"},{"text":"","thoughtSignature":"` + sig2 + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},"modelVersion":"gemini-3.6-flash","responseId":"resp-consecutive-carriers"}}`)

	nonStream := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, responseJSON, nil)
	content := gjson.GetBytes(nonStream, "content").Array()
	wantCarrier1 := encodeGeminiClaudeCarrierSignature(sig1, geminiClaudeCarrierPrevious, geminiClaudeCarrierText)
	wantCarrier2 := encodeGeminiClaudeCarrierSignature(sig2, geminiClaudeCarrierPrevious, geminiClaudeCarrierText)
	if len(content) != 3 || content[1].Get("signature").String() != wantCarrier1 || content[2].Get("signature").String() != wantCarrier2 {
		t.Fatalf("non-stream carriers were merged: %s", nonStream)
	}
	replayRequest := []byte(`{"model":"gemini-3.6-flash-high","messages":[{"role":"assistant","content":[]},{"role":"user","content":[{"type":"text","text":"continue"}]}]}`)
	replayRequest, _ = sjson.SetRawBytes(replayRequest, "messages.0.content", []byte(gjson.GetBytes(nonStream, "content").Raw))
	replayRequest = StripInvalidGeminiSignatureThinkingBlocks(replayRequest)
	translated := ConvertClaudeRequestToAntigravity("gemini-3.6-flash-high", replayRequest, false)
	parts := gjson.GetBytes(translated, "request.contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("thoughtSignature").String() != sig1 || parts[1].Get("thoughtSignature").String() != sig2 {
		t.Fatalf("consecutive carriers did not round-trip in order: %s", translated)
	}
	if parts[0].Get("text").String() != "visible" || !parts[1].Get("text").Exists() || parts[1].Get("text").String() != "" {
		t.Fatalf("consecutive carrier targets malformed: %s", translated)
	}

	var param any
	stream := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, responseJSON, &param), nil)
	streamText := string(stream)
	if got := strings.Count(streamText, `"content_block":{"type":"thinking"`); got != 2 {
		t.Fatalf("stream thinking carrier count = %d, want 2; output=%s", got, stream)
	}
	if got := strings.Count(streamText, `"type":"signature_delta"`); got != 2 {
		t.Fatalf("stream signature count = %d, want 2; output=%s", got, stream)
	}
}

func TestConvertAntigravityResponseToClaudeNonStream_ThoughtBeforeSignedToolRoundTrips(t *testing.T) {
	validSignature := testGeminiEPrefixSignature(t)
	requestJSON := []byte(`{"model":"gemini-3.6-flash-high"}`)
	responseJSON := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hidden analysis","thought":true},{"thoughtSignature":"` + validSignature + `","functionCall":{"id":"native-id","name":"run_command","args":{"command":"true"}}}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.6-flash","responseId":"resp-thought-tool"}}`)

	output := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, responseJSON, nil)
	if got := gjson.GetBytes(output, "content.#").Int(); got != 2 {
		t.Fatalf("content blocks = %d, want thinking + tool_use; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "content.0.thinking").String(); got != "hidden analysis" {
		t.Fatalf("thinking text = %q; output=%s", got, output)
	}
	wantCarrier := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierNext, geminiClaudeCarrierFunction)
	if got := gjson.GetBytes(output, "content.0.signature").String(); got != wantCarrier {
		t.Fatalf("thinking carrier signature = %q, want %q; output=%s", got, wantCarrier, output)
	}

	replayRequest := []byte(`{"model":"gemini-3.6-flash-high","messages":[{"role":"assistant","content":[]},{"role":"user","content":[{"type":"text","text":"continue"}]}]}`)
	replayRequest, _ = sjson.SetRawBytes(replayRequest, "messages.0.content", []byte(gjson.GetBytes(output, "content").Raw))
	replayRequest = StripInvalidGeminiSignatureThinkingBlocks(replayRequest)
	translated := ConvertClaudeRequestToAntigravity("gemini-3.6-flash-high", replayRequest, false)
	if got := gjson.GetBytes(translated, "request.contents.0.parts.0.text").String(); got != "hidden analysis" {
		t.Fatalf("replayed thought text = %q; translated=%s", got, translated)
	}
	if gjson.GetBytes(translated, "request.contents.0.parts.0.thoughtSignature").Exists() {
		t.Fatalf("thought part must remain unsigned; translated=%s", translated)
	}
	if got := gjson.GetBytes(translated, "request.contents.0.parts.1.thoughtSignature").String(); got != validSignature {
		t.Fatalf("tool signature = %q, want %q; translated=%s", got, validSignature, translated)
	}
}

func TestConvertAntigravityResponseToClaudeNonStream_DetachedGeminiSignatureAfterVisibleText(t *testing.T) {
	validSignature := testGeminiEPrefixSignature(t)
	requestJSON := []byte(`{"model":"gemini-3.6-flash-high"}`)
	responseJSON := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"visible answer"},{"text":"","thoughtSignature":"` + validSignature + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15},"modelVersion":"gemini-3.6-flash","responseId":"resp-detached"}}`)
	output := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, responseJSON, nil)
	if got := gjson.GetBytes(output, "content.#").Int(); got != 2 {
		t.Fatalf("content blocks = %d, want text + detached thinking; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "content.0.text").String(); got != "visible answer" {
		t.Fatalf("visible text = %q; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "content.1.type").String(); got != "thinking" {
		t.Fatalf("detached block type = %q; output=%s", got, output)
	}
	wantCarrier := encodeGeminiClaudeCarrierSignature(validSignature, geminiClaudeCarrierPrevious, geminiClaudeCarrierText)
	if got := gjson.GetBytes(output, "content.1.signature").String(); got != wantCarrier {
		t.Fatalf("detached signature = %q, want %q; output=%s", got, wantCarrier, output)
	}
}

func TestConvertAntigravityResponseToClaude_DirectionalTextCarriersRoundTrip(t *testing.T) {
	signature := testGeminiEPrefixSignature(t)
	requestJSON := []byte(`{"model":"gemini-3.6-flash-high"}`)
	testCases := []struct {
		name                string
		parts               string
		wantFirstSignature  string
		wantSecondSignature string
		wantDirection       string
		carrierIndex        int
	}{
		{name: "signed first part", parts: `[{"text":"A","thoughtSignature":"` + signature + `"},{"text":"B"}]`, wantFirstSignature: signature, wantDirection: geminiClaudeCarrierNext},
		{name: "trailing carrier before next part", parts: `[{"text":"A"},{"text":"","thoughtSignature":"` + signature + `"},{"text":"B"}]`, wantFirstSignature: signature, wantDirection: geminiClaudeCarrierPrevious, carrierIndex: 1},
		{name: "signed second part", parts: `[{"text":"A"},{"text":"B","thoughtSignature":"` + signature + `"}]`, wantSecondSignature: signature, wantDirection: geminiClaudeCarrierNext, carrierIndex: 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			responseJSON := []byte(`{"response":{"candidates":[{"content":{"parts":` + testCase.parts + `},"finishReason":"STOP"}],"modelVersion":"gemini-3.6-flash","responseId":"directional-text"}}`)
			nonStream := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, responseJSON, nil)
			content := gjson.GetBytes(nonStream, "content").Array()
			if len(content) != 3 {
				t.Fatalf("Claude content count = %d, want carrier + two text blocks; output=%s", len(content), nonStream)
			}
			carrierSignature := content[testCase.carrierIndex].Get("signature").String()
			_, direction, targetKind, marked, okCarrier := decodeGeminiClaudeCarrierSignature(carrierSignature)
			if !marked || !okCarrier || direction != testCase.wantDirection || targetKind != geminiClaudeCarrierText {
				t.Fatalf("directional carrier malformed: %q", carrierSignature)
			}

			replayRequest := []byte(`{"model":"gemini-3.6-flash-high","messages":[{"role":"assistant","content":[]},{"role":"user","content":[{"type":"text","text":"continue"}]}]}`)
			replayRequest, _ = sjson.SetRawBytes(replayRequest, "messages.0.content", []byte(gjson.GetBytes(nonStream, "content").Raw))
			replayRequest = StripInvalidGeminiSignatureThinkingBlocks(replayRequest)
			translated := ConvertClaudeRequestToAntigravity("gemini-3.6-flash-high", replayRequest, false)
			parts := gjson.GetBytes(translated, "request.contents.0.parts").Array()
			if len(parts) != 2 || parts[0].Get("text").String() != "A" || parts[1].Get("text").String() != "B" {
				t.Fatalf("text boundaries changed: %s", translated)
			}
			if got := parts[0].Get("thoughtSignature").String(); got != testCase.wantFirstSignature {
				t.Fatalf("first signature = %q, want %q; translated=%s", got, testCase.wantFirstSignature, translated)
			}
			if got := parts[1].Get("thoughtSignature").String(); got != testCase.wantSecondSignature {
				t.Fatalf("second signature = %q, want %q; translated=%s", got, testCase.wantSecondSignature, translated)
			}
			if strings.Contains(string(translated), geminiClaudeCarrierPrefix) {
				t.Fatalf("carrier envelope leaked to Gemini wire: %s", translated)
			}

			var param any
			stream := bytes.Join(ConvertAntigravityResponseToClaude(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, responseJSON, &param), nil)
			if got := strings.Count(string(stream), `"content_block":{"type":"text"`); got != 2 {
				t.Fatalf("stream text block count = %d, want 2; output=%s", got, stream)
			}
			if !strings.Contains(string(stream), geminiClaudeCarrierPrefix+testCase.wantDirection+":"+geminiClaudeCarrierText+":") {
				t.Fatalf("stream carrier direction missing: %s", stream)
			}
		})
	}
}

func TestConvertAntigravityResponseToClaude_LeadingCarrierTargetsFollowingThought(t *testing.T) {
	signature := testGeminiEPrefixSignature(t)
	requestJSON := []byte(`{"model":"gemini-3.6-flash-high"}`)
	responseJSON := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + signature + `"},{"text":"reason","thought":true}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.6-flash","responseId":"leading-thought"}}`)
	nonStream := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "gemini-3.6-flash-high", requestJSON, requestJSON, responseJSON, nil)
	content := gjson.GetBytes(nonStream, "content").Array()
	if len(content) != 2 || content[0].Get("thinking").String() != "" || content[1].Get("thinking").String() != "reason" {
		t.Fatalf("leading thought carrier response malformed: %s", nonStream)
	}
	replayRequest := []byte(`{"model":"gemini-3.6-flash-high","messages":[{"role":"assistant","content":[]}]}`)
	replayRequest, _ = sjson.SetRawBytes(replayRequest, "messages.0.content", []byte(gjson.GetBytes(nonStream, "content").Raw))
	replayRequest = StripInvalidGeminiSignatureThinkingBlocks(replayRequest)
	if got := gjson.GetBytes(replayRequest, "messages.0.content.#").Int(); got != 2 {
		t.Fatalf("prevalidation dropped unsigned target thought: %s", replayRequest)
	}
	translated := ConvertClaudeRequestToAntigravity("gemini-3.6-flash-high", replayRequest, false)
	part := gjson.GetBytes(translated, "request.contents.0.parts.0")
	if part.Get("text").String() != "reason" || !part.Get("thought").Bool() || part.Get("thoughtSignature").String() != signature {
		t.Fatalf("leading thought carrier did not round-trip: %s", translated)
	}
}

func TestConvertAntigravityResponseToClaudeNonStream_SignatureOnlyPartWithoutThoughtFlag(t *testing.T) {
	previousCache := cache.SignatureCacheEnabled()
	cache.SetSignatureCacheEnabled(false)
	defer cache.SetSignatureCacheEnabled(previousCache)

	requestJSON := []byte(`{"model":"claude-sonnet-4-5-thinking"}`)
	validSignature := "EtestSig1234567890123456789012345678901234567890123456789"
	responseJSON := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [
						{"text": "Full thinking text.", "thought": true},
						{"text": "", "thoughtSignature": "` + validSignature + `"}
					]
				},
				"finishReason": "STOP"
			}],
			"usageMetadata": {
				"promptTokenCount": 10,
				"thoughtsTokenCount": 2,
				"totalTokenCount": 12
			},
			"modelVersion": "claude-sonnet-4-5-thinking",
			"responseId": "resp-test"
		}
	}`)

	output := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "claude-sonnet-4-5-thinking", requestJSON, requestJSON, responseJSON, nil)

	if got := gjson.GetBytes(output, "content.#").Int(); got != 1 {
		t.Fatalf("expected exactly one content block, got %d: %s", got, output)
	}
	if got := gjson.GetBytes(output, "content.0.type").String(); got != "thinking" {
		t.Fatalf("expected thinking content block, got %q: %s", got, output)
	}
	if got := gjson.GetBytes(output, "content.0.thinking").String(); got != "Full thinking text." {
		t.Fatalf("unexpected thinking text %q: %s", got, output)
	}
	if got := gjson.GetBytes(output, "content.0.signature").String(); got != validSignature {
		t.Fatalf("expected signature %q, got %q: %s", validSignature, got, output)
	}
}

func TestConvertAntigravityResponseToClaudeNonStream_TextWithThoughtSignatureStaysText(t *testing.T) {
	previousCache := cache.SignatureCacheEnabled()
	cache.SetSignatureCacheEnabled(false)
	defer cache.SetSignatureCacheEnabled(previousCache)

	requestJSON := []byte(`{"model":"gemini-3.1-pro-low"}`)
	translatedRequestJSON := []byte(`{"model":"gemini-3.1-pro-low"}`)
	responseJSON := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [
						{"text": "I need to multiply 17 by 24.", "thought": true},
						{"text": "408", "thoughtSignature": "sig-final-answer"}
					]
				},
				"finishReason": "STOP"
			}],
			"usageMetadata": {
				"promptTokenCount": 16,
				"candidatesTokenCount": 3,
				"thoughtsTokenCount": 42,
				"totalTokenCount": 61
			},
			"modelVersion": "gemini-3.1-pro-low",
			"responseId": "resp-text-sig"
		}
	}`)

	output := ConvertAntigravityResponseToClaudeNonStream(context.Background(), "gemini-3.1-pro-low", requestJSON, translatedRequestJSON, responseJSON, nil)
	if got := gjson.GetBytes(output, "content.#").Int(); got != 2 {
		t.Fatalf("content block count = %d, want 2. Output: %s", got, output)
	}
	if got := gjson.GetBytes(output, "content.0.type").String(); got != "thinking" {
		t.Fatalf("content.0.type = %q, want thinking. Output: %s", got, output)
	}
	if got := gjson.GetBytes(output, "content.0.thinking").String(); got != "I need to multiply 17 by 24." {
		t.Fatalf("thinking = %q, want thought text. Output: %s", got, output)
	}
	wantCarrier := encodeGeminiClaudeCarrierSignature("sig-final-answer", geminiClaudeCarrierNext, geminiClaudeCarrierText)
	if got := gjson.GetBytes(output, "content.0.signature").String(); got != wantCarrier {
		t.Fatalf("signature = %q, want %q. Output: %s", got, wantCarrier, output)
	}
	if got := gjson.GetBytes(output, "content.1.type").String(); got != "text" {
		t.Fatalf("content.1.type = %q, want text. Output: %s", got, output)
	}
	if got := gjson.GetBytes(output, "content.1.text").String(); got != "408" {
		t.Fatalf("text = %q, want final answer. Output: %s", got, output)
	}
}

func TestConvertAntigravityResponseToClaudeStream_TextWithThoughtSignatureStaysText(t *testing.T) {
	previousCache := cache.SignatureCacheEnabled()
	cache.SetSignatureCacheEnabled(false)
	defer cache.SetSignatureCacheEnabled(previousCache)

	requestJSON := []byte(`{"model":"gemini-3.1-pro-low"}`)
	translatedRequestJSON := []byte(`{"model":"gemini-3.1-pro-low"}`)
	thoughtChunk := []byte(`{
		"response": {
			"candidates": [{"content": {"parts": [{"text": "I need to multiply 17 by 24.", "thought": true}]}}],
			"modelVersion": "gemini-3.1-pro-low",
			"responseId": "resp-text-sig"
		}
	}`)
	textChunk := []byte(`{
		"response": {
			"candidates": [{"content": {"parts": [{"text": "408", "thoughtSignature": "sig-final-answer"}]}}],
			"modelVersion": "gemini-3.1-pro-low",
			"responseId": "resp-text-sig"
		}
	}`)
	finishChunk := []byte(`{
		"response": {
			"candidates": [{"finishReason": "STOP"}],
			"usageMetadata": {"promptTokenCount": 16, "candidatesTokenCount": 3, "thoughtsTokenCount": 42, "totalTokenCount": 61},
			"modelVersion": "gemini-3.1-pro-low",
			"responseId": "resp-text-sig"
		}
	}`)

	var param any
	ctx := context.Background()
	output := bytes.Join(ConvertAntigravityResponseToClaude(ctx, "gemini-3.1-pro-low", requestJSON, translatedRequestJSON, thoughtChunk, &param), nil)
	output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(ctx, "gemini-3.1-pro-low", requestJSON, translatedRequestJSON, textChunk, &param), nil)...)
	output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(ctx, "gemini-3.1-pro-low", requestJSON, translatedRequestJSON, finishChunk, &param), nil)...)
	output = append(output, bytes.Join(ConvertAntigravityResponseToClaude(ctx, "gemini-3.1-pro-low", requestJSON, translatedRequestJSON, []byte("[DONE]"), &param), nil)...)
	outputText := string(output)

	wantCarrier := encodeGeminiClaudeCarrierSignature("sig-final-answer", geminiClaudeCarrierNext, geminiClaudeCarrierText)
	if !strings.Contains(outputText, `"delta":{"type":"signature_delta","signature":"`+wantCarrier+`"}`) {
		t.Fatalf("expected signature delta for thinking block: %s", outputText)
	}
	if !strings.Contains(outputText, `"content_block":{"type":"text","text":""}`) {
		t.Fatalf("expected text content block after thinking: %s", outputText)
	}
	if !strings.Contains(outputText, `"delta":{"type":"text_delta","text":"408"}`) {
		t.Fatalf("expected final answer as text delta: %s", outputText)
	}
	if strings.Contains(outputText, `"delta":{"type":"thinking_delta","thinking":"408"}`) {
		t.Fatalf("final answer must not be emitted as thinking delta: %s", outputText)
	}
}
