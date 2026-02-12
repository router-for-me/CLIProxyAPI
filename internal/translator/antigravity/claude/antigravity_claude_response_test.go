package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	"github.com/tidwall/gjson"
)

// ============================================================================
// Signature Caching Tests
// ============================================================================

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

// ============================================================================
// Web Search / Grounding Metadata Tests
// ============================================================================

func TestConvertAntigravityResponseToClaude_StreamingGroundingMetadata(t *testing.T) {
	requestJSON := []byte(`{
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Search test"}]}]
	}`)

	// Response with text + groundingMetadata
	responseJSON := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "Here are the results."}]
				},
				"groundingMetadata": {
					"webSearchQueries": ["Go tutorials 2024"],
					"groundingChunks": [
						{"web": {"uri": "https://go.dev/tour", "title": "A Tour of Go"}},
						{"web": {"uri": "https://gobyexample.com", "title": "Go by Example"}}
					]
				}
			}]
		}
	}`)

	var param any
	ctx := context.Background()
	results := ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5", requestJSON, requestJSON, responseJSON, &param)

	if len(results) == 0 {
		t.Fatal("Expected response output")
	}

	output := results[0]

	// Should contain server_tool_use
	if !strings.Contains(output, "server_tool_use") {
		t.Error("Output should contain server_tool_use block")
	}

	// Should contain web_search_tool_result
	if !strings.Contains(output, "web_search_tool_result") {
		t.Error("Output should contain web_search_tool_result block")
	}

	// Should contain the search query
	if !strings.Contains(output, "Go tutorials 2024") {
		t.Error("Output should contain the search query")
	}

	// Should contain grounding chunk URLs
	if !strings.Contains(output, "https://go.dev/tour") {
		t.Error("Output should contain grounding chunk URL")
	}
	if !strings.Contains(output, "A Tour of Go") {
		t.Error("Output should contain grounding chunk title")
	}

	// Check params state
	params := param.(*Params)
	if !params.HasWebSearch {
		t.Error("HasWebSearch should be true")
	}
	if !params.HasToolUse {
		t.Error("HasToolUse should be true (triggers tool_use stop_reason)")
	}
}

func TestConvertAntigravityResponseToClaude_StreamingGroundingMetadata_StopReason(t *testing.T) {
	requestJSON := []byte(`{
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Search test"}]}]
	}`)

	// Response with groundingMetadata + finishReason
	responseJSON := []byte(`{
		"response": {
			"candidates": [{
				"content": {
					"parts": [{"text": "Results."}]
				},
				"finishReason": "STOP",
				"groundingMetadata": {
					"webSearchQueries": ["test query"],
					"groundingChunks": [
						{"web": {"uri": "https://example.com", "title": "Example"}}
					]
				}
			}],
			"usageMetadata": {
				"promptTokenCount": 10,
				"candidatesTokenCount": 20,
				"totalTokenCount": 30
			}
		}
	}`)

	var param any
	ctx := context.Background()
	results := ConvertAntigravityResponseToClaude(ctx, "claude-sonnet-4-5", requestJSON, requestJSON, responseJSON, &param)

	output := results[0]

	// Should have tool_use as stop_reason (not end_turn) because HasToolUse=true
	if !strings.Contains(output, `"stop_reason":"tool_use"`) {
		t.Errorf("Stop reason should be 'tool_use' when grounding metadata present, got output: %s", output)
	}
}

func TestConvertAntigravityResponseToClaudeNonStream_GroundingMetadata(t *testing.T) {
	requestJSON := []byte(`{
		"model": "claude-sonnet-4-5",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Search test"}]}]
	}`)

	responseJSON := []byte(`{
		"response": {
			"responseId": "resp_123",
			"modelVersion": "claude-sonnet-4-5",
			"candidates": [{
				"content": {
					"parts": [{"text": "Here are results from the web."}]
				},
				"finishReason": "STOP",
				"groundingMetadata": {
					"webSearchQueries": ["web search query"],
					"groundingChunks": [
						{"web": {"uri": "https://example.com/page1", "title": "Page 1"}},
						{"web": {"uri": "https://example.com/page2", "title": "Page 2"}}
					]
				}
			}],
			"usageMetadata": {
				"promptTokenCount": 100,
				"candidatesTokenCount": 50,
				"totalTokenCount": 150
			}
		}
	}`)

	var param any
	ctx := context.Background()
	output := ConvertAntigravityResponseToClaudeNonStream(ctx, "claude-sonnet-4-5", requestJSON, requestJSON, responseJSON, &param)

	// Should have text content
	textBlock := gjson.Get(output, "content.0")
	if textBlock.Get("type").String() != "text" {
		t.Errorf("First content block should be text, got: %s", textBlock.Get("type").String())
	}
	if textBlock.Get("text").String() != "Here are results from the web." {
		t.Errorf("Text mismatch, got: %s", textBlock.Get("text").String())
	}

	// Should have server_tool_use block
	srvToolBlock := gjson.Get(output, "content.1")
	if srvToolBlock.Get("type").String() != "server_tool_use" {
		t.Errorf("Second content block should be server_tool_use, got: %s", srvToolBlock.Get("type").String())
	}
	if srvToolBlock.Get("name").String() != "web_search" {
		t.Errorf("server_tool_use name should be 'web_search', got: %s", srvToolBlock.Get("name").String())
	}
	if srvToolBlock.Get("input.query").String() != "web search query" {
		t.Errorf("server_tool_use query should be 'web search query', got: %s", srvToolBlock.Get("input.query").String())
	}

	// Should have web_search_tool_result block
	resultBlock := gjson.Get(output, "content.2")
	if resultBlock.Get("type").String() != "web_search_tool_result" {
		t.Errorf("Third content block should be web_search_tool_result, got: %s", resultBlock.Get("type").String())
	}

	// Check grounding chunks in result
	results := resultBlock.Get("content")
	if !results.IsArray() || len(results.Array()) != 2 {
		t.Fatalf("Expected 2 web_search_result entries, got: %s", results.Raw)
	}

	firstResult := results.Array()[0]
	if firstResult.Get("type").String() != "web_search_result" {
		t.Error("Content entry should be web_search_result type")
	}
	if firstResult.Get("url").String() != "https://example.com/page1" {
		t.Errorf("Expected URL 'https://example.com/page1', got: %s", firstResult.Get("url").String())
	}
	if firstResult.Get("title").String() != "Page 1" {
		t.Errorf("Expected title 'Page 1', got: %s", firstResult.Get("title").String())
	}

	// Stop reason should be tool_use
	if gjson.Get(output, "stop_reason").String() != "tool_use" {
		t.Errorf("Stop reason should be 'tool_use', got: %s", gjson.Get(output, "stop_reason").String())
	}

	// tool_use_id should match between server_tool_use and web_search_tool_result
	srvToolID := srvToolBlock.Get("id").String()
	resultToolID := resultBlock.Get("tool_use_id").String()
	if srvToolID == "" || srvToolID != resultToolID {
		t.Errorf("tool_use_id mismatch: server_tool_use.id=%s, web_search_tool_result.tool_use_id=%s", srvToolID, resultToolID)
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
