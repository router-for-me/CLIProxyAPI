package test

import (
	"context"
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

// --- Request translation tests ---

func TestResponsesToOpenAI_PassesBuiltinWebSearchTool(t *testing.T) {
	in := []byte(`{
		"model":"gpt-4o",
		"input":[{"role":"user","content":[{"type":"input_text","text":"search the web"}]}],
		"tools":[
			{"type":"web_search_preview"},
			{"type":"function","name":"calc","description":"Calculate","parameters":{"type":"object","properties":{}}}
		]
	}`)

	out := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, "gpt-4o", in, false)

	toolCount := gjson.GetBytes(out, "tools.#").Int()
	if toolCount != 2 {
		t.Fatalf("expected 2 tools, got %d: %s", toolCount, string(out))
	}

	// First tool should be passed through as-is
	tool0Type := gjson.GetBytes(out, "tools.0.type").String()
	if tool0Type != "web_search_preview" {
		t.Fatalf("expected tools[0].type=web_search_preview, got %q", tool0Type)
	}

	// Second should be converted to function format
	tool1Type := gjson.GetBytes(out, "tools.1.type").String()
	if tool1Type != "function" {
		t.Fatalf("expected tools[1].type=function, got %q", tool1Type)
	}
}

// --- OpenAI→Claude response tests ---

func TestOpenAIToClaude_StreamAnnotationsAsCitations(t *testing.T) {
	ctx := context.Background()
	model := "gpt-4o"
	reqJSON := []byte(`{"stream":true}`)
	var param any

	// First chunk with role
	sse1 := []byte(`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, model, reqJSON, reqJSON, sse1, &param)

	// Content chunk
	sse2 := []byte(`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"The answer is here."},"finish_reason":null}]}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, model, reqJSON, reqJSON, sse2, &param)

	// First annotation chunk
	sse3 := []byte(`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"annotations":[{"type":"url_citation","url":"https://example.com/1","title":"First","start_index":0,"end_index":10}]},"finish_reason":null}]}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, model, reqJSON, reqJSON, sse3, &param)

	// Second annotation chunk (tests multi-chunk accumulation)
	sse3b := []byte(`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"annotations":[{"type":"url_citation","url":"https://example.com/2","title":"Second","start_index":11,"end_index":19}]},"finish_reason":null}]}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, model, reqJSON, reqJSON, sse3b, &param)

	// Finish + usage chunk
	sse4 := []byte(`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":20,"total_tokens":70}}`)
	results := sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, model, reqJSON, reqJSON, sse4, &param)

	var messageDelta string
	for _, r := range results {
		if gjson.Get(r, "type").String() == "message_delta" {
			messageDelta = r
			break
		}
	}
	if messageDelta == "" {
		t.Fatalf("expected message_delta event, got: %v", results)
	}

	citCount := gjson.Get(messageDelta, "citations.#").Int()
	if citCount != 2 {
		t.Fatalf("expected 2 citations on message_delta, got %d: %s", citCount, messageDelta)
	}
	if url := gjson.Get(messageDelta, "citations.0.url").String(); url != "https://example.com/1" {
		t.Fatalf("expected citations[0].url=https://example.com/1, got %q", url)
	}
	if url := gjson.Get(messageDelta, "citations.1.url").String(); url != "https://example.com/2" {
		t.Fatalf("expected citations[1].url=https://example.com/2, got %q", url)
	}
}

func TestOpenAIToClaude_NonStreamAnnotationsAsCitations(t *testing.T) {
	ctx := context.Background()
	model := "gpt-4o"
	reqJSON := []byte(`{}`)

	// Non-streaming response with annotations
	rawJSON := []byte(`{
		"id":"chatcmpl-ns","object":"chat.completion","created":1700000000,"model":"gpt-4o",
		"choices":[{
			"index":0,"message":{
				"role":"assistant","content":"The answer is here.",
				"annotations":[
					{"type":"url_citation","url":"https://example.com/1","title":"First","start_index":0,"end_index":10},
					{"type":"url_citation","url":"https://example.com/2","title":"Second","start_index":11,"end_index":19}
				]
			},"finish_reason":"stop"
		}],
		"usage":{"prompt_tokens":50,"completion_tokens":20,"total_tokens":70}
	}`)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, model, reqJSON, reqJSON, rawJSON, &param)

	// Verify citations on response
	citCount := gjson.Get(out, "citations.#").Int()
	if citCount != 2 {
		t.Fatalf("expected 2 citations, got %d: %s", citCount, out)
	}
	if url := gjson.Get(out, "citations.0.url").String(); url != "https://example.com/1" {
		t.Fatalf("expected citations[0].url=https://example.com/1, got %q", url)
	}
	if url := gjson.Get(out, "citations.1.url").String(); url != "https://example.com/2" {
		t.Fatalf("expected citations[1].url=https://example.com/2, got %q", url)
	}

	// Verify text content still present
	textContent := gjson.Get(out, "content.0.text").String()
	if textContent != "The answer is here." {
		t.Fatalf("expected text content, got %q", textContent)
	}
}

// --- OpenAI→Gemini response tests ---

func TestOpenAIToGemini_StreamAnnotationsAsGroundingMetadata(t *testing.T) {
	ctx := context.Background()
	model := "gpt-4o"
	reqJSON := []byte(`{}`)
	var param any

	// First chunk with role
	sse1 := []byte(`data: {"id":"chatcmpl-gem","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatGemini, model, reqJSON, reqJSON, sse1, &param)

	// Content chunk
	sse2 := []byte(`data: {"id":"chatcmpl-gem","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Gemini answer."},"finish_reason":null}]}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatGemini, model, reqJSON, reqJSON, sse2, &param)

	// Annotation chunk
	sse3 := []byte(`data: {"id":"chatcmpl-gem","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"annotations":[{"type":"url_citation","url":"https://gemini.test","title":"Gemini Source","start_index":0,"end_index":14}]},"finish_reason":null}]}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatGemini, model, reqJSON, reqJSON, sse3, &param)

	// Finish reason chunk
	sse4 := []byte(`data: {"id":"chatcmpl-gem","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	results := sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatGemini, model, reqJSON, reqJSON, sse4, &param)

	// Find chunk with groundingMetadata
	var finalChunk string
	for _, r := range results {
		if gjson.Get(r, "candidates.0.groundingMetadata").Exists() {
			finalChunk = r
			break
		}
	}
	if finalChunk == "" {
		t.Fatalf("expected groundingMetadata on finish chunk, got: %v", results)
	}

	citCount := gjson.Get(finalChunk, "candidates.0.groundingMetadata.citations.#").Int()
	if citCount != 1 {
		t.Fatalf("expected 1 citation in groundingMetadata, got %d: %s", citCount, finalChunk)
	}
	citURL := gjson.Get(finalChunk, "candidates.0.groundingMetadata.citations.0.url").String()
	if citURL != "https://gemini.test" {
		t.Fatalf("expected citations[0].url=https://gemini.test, got %q", citURL)
	}
}

func TestOpenAIToGemini_NonStreamAnnotationsAsGroundingMetadata(t *testing.T) {
	ctx := context.Background()
	model := "gpt-4o"
	reqJSON := []byte(`{}`)

	rawJSON := []byte(`{
		"id":"chatcmpl-gns","object":"chat.completion","created":1700000000,"model":"gpt-4o",
		"choices":[{
			"index":0,"message":{
				"role":"assistant","content":"Gemini non-stream.",
				"annotations":[
					{"type":"url_citation","url":"https://gemini-ns.test","title":"GNS Source","start_index":0,"end_index":18}
				]
			},"finish_reason":"stop"
		}],
		"usage":{"prompt_tokens":40,"completion_tokens":15,"total_tokens":55}
	}`)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatGemini, model, reqJSON, reqJSON, rawJSON, &param)

	if !gjson.Get(out, "candidates.0.groundingMetadata").Exists() {
		t.Fatalf("expected groundingMetadata, got: %s", out)
	}

	citCount := gjson.Get(out, "candidates.0.groundingMetadata.citations.#").Int()
	if citCount != 1 {
		t.Fatalf("expected 1 citation, got %d: %s", citCount, out)
	}
	citURL := gjson.Get(out, "candidates.0.groundingMetadata.citations.0.url").String()
	if citURL != "https://gemini-ns.test" {
		t.Fatalf("expected url=https://gemini-ns.test, got %q", citURL)
	}
}

// --- OpenAI CC→Responses tests ---

func TestOpenAIToResponses_StreamAnnotationsPopulated(t *testing.T) {
	ctx := context.Background()
	model := "gpt-4o"
	reqJSON := []byte(`{}`)
	var param any

	// First chunk
	sse1 := []byte(`data: {"id":"chatcmpl-resp","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, sse1, &param)

	// Content
	sse2 := []byte(`data: {"id":"chatcmpl-resp","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Responses answer."},"finish_reason":null}]}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, sse2, &param)

	// Annotation
	sse3 := []byte(`data: {"id":"chatcmpl-resp","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"annotations":[{"type":"url_citation","url":"https://resp.test","title":"Resp Source","start_index":0,"end_index":17}]},"finish_reason":null}]}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, sse3, &param)

	// Finish + usage
	sse4 := []byte(`data: {"id":"chatcmpl-resp","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":20,"total_tokens":70}}`)
	results := sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, sse4, &param)

	// Verify content_part.done has annotations
	var partDone string
	for _, r := range results {
		if gjson.Get(r, "type").String() == "response.content_part.done" {
			partDone = r
			break
		}
	}
	if partDone == "" {
		t.Fatalf("expected response.content_part.done, got: %v", results)
	}
	if cnt := gjson.Get(partDone, "part.annotations.#").Int(); cnt != 1 {
		t.Fatalf("expected 1 annotation on content_part.done, got %d: %s", cnt, partDone)
	}
	if url := gjson.Get(partDone, "part.annotations.0.url").String(); url != "https://resp.test" {
		t.Fatalf("expected part.annotations[0].url=https://resp.test, got %q", url)
	}

	// Verify output_item.done has annotations
	var itemDone string
	for _, r := range results {
		if gjson.Get(r, "type").String() == "response.output_item.done" {
			if gjson.Get(r, "item.type").String() == "message" {
				itemDone = r
				break
			}
		}
	}
	if itemDone == "" {
		t.Fatalf("expected response.output_item.done with message, got: %v", results)
	}
	annCount := gjson.Get(itemDone, "item.content.0.annotations.#").Int()
	if annCount != 1 {
		t.Fatalf("expected 1 annotation on output_item.done, got %d: %s", annCount, itemDone)
	}
	if url := gjson.Get(itemDone, "item.content.0.annotations.0.url").String(); url != "https://resp.test" {
		t.Fatalf("expected annotations[0].url=https://resp.test, got %q", url)
	}
}

func TestOpenAIToResponses_NonStreamAnnotationsPopulated(t *testing.T) {
	ctx := context.Background()
	model := "gpt-4o"
	reqJSON := []byte(`{}`)

	rawJSON := []byte(`{
		"id":"chatcmpl-rns","object":"chat.completion","created":1700000000,"model":"gpt-4o",
		"choices":[{
			"index":0,"message":{
				"role":"assistant","content":"Non-stream response.",
				"annotations":[
					{"type":"url_citation","url":"https://rns.test","title":"RNS Source","start_index":0,"end_index":20}
				]
			},"finish_reason":"stop"
		}],
		"usage":{"prompt_tokens":40,"completion_tokens":15,"total_tokens":55}
	}`)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, rawJSON, &param)

	// Find message output item
	annCount := gjson.Get(out, "output.#(type==\"message\").content.0.annotations.#").Int()
	if annCount != 1 {
		t.Fatalf("expected 1 annotation on message output, got %d: %s", annCount, out)
	}
	annURL := gjson.Get(out, "output.#(type==\"message\").content.0.annotations.0.url").String()
	if annURL != "https://rns.test" {
		t.Fatalf("expected annotations[0].url=https://rns.test, got %q", annURL)
	}
}
