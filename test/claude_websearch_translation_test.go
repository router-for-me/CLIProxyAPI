package test

import (
	"context"
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

// --- Request translation tests ---

func TestOpenAIToClaude_PreservesBuiltinWebSearchTool(t *testing.T) {
	in := []byte(`{
		"model":"claude-haiku",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[
			{"type":"web_search_20250305","name":"web_search","max_uses":1},
			{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{}}}}
		]
	}`)

	out := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "claude-haiku", in, false)

	toolCount := gjson.GetBytes(out, "tools.#").Int()
	if toolCount != 2 {
		t.Fatalf("expected 2 tools, got %d: %s", toolCount, string(out))
	}

	// First tool should be the built-in web_search passed through as-is
	tool0Type := gjson.GetBytes(out, "tools.0.type").String()
	if tool0Type != "web_search_20250305" {
		t.Fatalf("expected tools[0].type=web_search_20250305, got %q", tool0Type)
	}
	tool0Name := gjson.GetBytes(out, "tools.0.name").String()
	if tool0Name != "web_search" {
		t.Fatalf("expected tools[0].name=web_search, got %q", tool0Name)
	}
	tool0MaxUses := gjson.GetBytes(out, "tools.0.max_uses").Int()
	if tool0MaxUses != 1 {
		t.Fatalf("expected tools[0].max_uses=1, got %d", tool0MaxUses)
	}

	// Second tool should be converted to Claude function format
	tool1Name := gjson.GetBytes(out, "tools.1.name").String()
	if tool1Name != "get_weather" {
		t.Fatalf("expected tools[1].name=get_weather, got %q", tool1Name)
	}
}

func TestOpenAIResponsesToClaude_PreservesBuiltinWebSearchTool(t *testing.T) {
	in := []byte(`{
		"model":"claude-haiku",
		"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"tools":[
			{"type":"web_search_20250305","name":"web_search","max_uses":2},
			{"type":"function","name":"calc","description":"Calculate","parameters":{"type":"object","properties":{}}}
		]
	}`)

	out := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-haiku", in, false)

	toolCount := gjson.GetBytes(out, "tools.#").Int()
	if toolCount != 2 {
		t.Fatalf("expected 2 tools, got %d: %s", toolCount, string(out))
	}

	tool0Type := gjson.GetBytes(out, "tools.0.type").String()
	if tool0Type != "web_search_20250305" {
		t.Fatalf("expected tools[0].type=web_search_20250305, got %q", tool0Type)
	}
	tool0MaxUses := gjson.GetBytes(out, "tools.0.max_uses").Int()
	if tool0MaxUses != 2 {
		t.Fatalf("expected tools[0].max_uses=2, got %d", tool0MaxUses)
	}
}

// --- Response translation tests (streaming) ---

// Simulates the Claude SSE events for a web_search response and verifies the
// OpenAI Chat Completions streaming output contains web_search_results and citations.
func TestClaudeToOpenAI_StreamWebSearchResultsAndCitations(t *testing.T) {
	ctx := context.Background()
	model := "claude-haiku-4-5-20251001"
	reqJSON := []byte(`{}`)
	var param any

	// 1. message_start
	sse1 := []byte(`data: {"type":"message_start","message":{"id":"msg_test123","model":"claude-haiku-4-5-20251001","type":"message","role":"assistant","content":[],"usage":{"input_tokens":100,"output_tokens":10}}}`)
	results := sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, sse1, &param)
	if len(results) == 0 {
		t.Fatal("expected output for message_start")
	}

	// 2. content_block_start with server_tool_use (should be silently skipped)
	sse2 := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_test","name":"web_search","input":{}}}`)
	results = sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, sse2, &param)
	if len(results) != 0 {
		t.Fatalf("expected empty output for server_tool_use, got %d results", len(results))
	}

	// 3. content_block_stop for server_tool_use
	sse3 := []byte(`data: {"type":"content_block_stop","index":0}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, sse3, &param)

	// 4. content_block_start with web_search_tool_result (should accumulate results)
	sse4 := []byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_test","content":[{"type":"web_search_result","title":"Test Result","url":"https://example.com","encrypted_content":"abc123"}]}}`)
	results = sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, sse4, &param)
	if len(results) != 0 {
		t.Fatalf("expected empty output for web_search_tool_result, got %d results", len(results))
	}

	// 5. content_block_stop for web_search_tool_result
	sse5 := []byte(`data: {"type":"content_block_stop","index":1}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, sse5, &param)

	// 6. content_block_start with text block
	sse6 := []byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, sse6, &param)

	// 7. citations_delta
	sse7 := []byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","cited_text":"test cited text","url":"https://example.com","title":"Test Result","encrypted_index":"enc123"}}}`)
	results = sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, sse7, &param)
	if len(results) == 0 {
		t.Fatal("expected output for citations_delta")
	}
	citationChunk := results[0]
	if !gjson.Get(citationChunk, "choices.0.delta.citations.0.url").Exists() {
		t.Fatalf("expected citation in delta chunk, got: %s", citationChunk)
	}

	// 8. text_delta
	sse8 := []byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"The answer is here."}}`)
	results = sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, sse8, &param)
	if len(results) == 0 {
		t.Fatal("expected output for text_delta")
	}

	// 9. message_delta (final) - should contain accumulated web_search_results and citations
	sse9 := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":50}}`)
	results = sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, sse9, &param)
	if len(results) == 0 {
		t.Fatal("expected output for message_delta")
	}
	finalChunk := results[0]

	// Verify web_search_results on choice
	wsrCount := gjson.Get(finalChunk, "choices.0.web_search_results.#").Int()
	if wsrCount != 1 {
		t.Fatalf("expected 1 web_search_result on choices.0, got %d: %s", wsrCount, finalChunk)
	}
	wsrTitle := gjson.Get(finalChunk, "choices.0.web_search_results.0.title").String()
	if wsrTitle != "Test Result" {
		t.Fatalf("expected web_search_results[0].title=Test Result, got %q", wsrTitle)
	}

	// Verify citations on choice
	citCount := gjson.Get(finalChunk, "choices.0.citations.#").Int()
	if citCount != 1 {
		t.Fatalf("expected 1 citation on choices.0, got %d: %s", citCount, finalChunk)
	}
	citURL := gjson.Get(finalChunk, "choices.0.citations.0.url").String()
	if citURL != "https://example.com" {
		t.Fatalf("expected citations[0].url=https://example.com, got %q", citURL)
	}
}

// --- Response translation tests (non-streaming) ---

// Feeds all Claude SSE events at once to the non-stream translator and verifies
// web_search_results and citations are placed on choices.0.
func TestClaudeToOpenAI_NonStreamWebSearchResultsAndCitations(t *testing.T) {
	ctx := context.Background()
	model := "claude-haiku-4-5-20251001"
	reqJSON := []byte(`{}`)

	// All SSE events concatenated (the non-stream translator parses all at once)
	allSSE := []byte(
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test456\",\"model\":\"claude-haiku-4-5-20251001\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":100,\"output_tokens\":10}}}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"server_tool_use\",\"id\":\"srvtoolu_test\",\"name\":\"web_search\",\"input\":{}}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"web_search_tool_result\",\"tool_use_id\":\"srvtoolu_test\",\"content\":[{\"type\":\"web_search_result\",\"title\":\"ESPN Result\",\"url\":\"https://espn.com/test\",\"encrypted_content\":\"xyz\"},{\"type\":\"web_search_result\",\"title\":\"CBS Result\",\"url\":\"https://cbs.com/test\",\"encrypted_content\":\"abc\"}]}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"citations_delta\",\"citation\":{\"type\":\"web_search_result_location\",\"cited_text\":\"Seahawks won\",\"url\":\"https://espn.com/test\",\"title\":\"ESPN Result\",\"encrypted_index\":\"enc1\"}}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"text_delta\",\"text\":\"The Seahawks won the Super Bowl.\"}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"citations_delta\",\"citation\":{\"type\":\"web_search_result_location\",\"cited_text\":\"29-13 victory\",\"url\":\"https://cbs.com/test\",\"title\":\"CBS Result\",\"encrypted_index\":\"enc2\"}}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":2}\n\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":50}}\n\n" +
			"data: {\"type\":\"message_stop\"}\n\n")

	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, allSSE, &param)

	// Verify web_search_results at choices.0 level
	wsrCount := gjson.Get(out, "choices.0.web_search_results.#").Int()
	if wsrCount != 2 {
		t.Fatalf("expected 2 web_search_results on choices.0, got %d: %s", wsrCount, out)
	}
	if title := gjson.Get(out, "choices.0.web_search_results.0.title").String(); title != "ESPN Result" {
		t.Fatalf("expected first result title=ESPN Result, got %q", title)
	}
	if title := gjson.Get(out, "choices.0.web_search_results.1.title").String(); title != "CBS Result" {
		t.Fatalf("expected second result title=CBS Result, got %q", title)
	}

	// Verify citations at choices.0 level
	citCount := gjson.Get(out, "choices.0.citations.#").Int()
	if citCount != 2 {
		t.Fatalf("expected 2 citations on choices.0, got %d: %s", citCount, out)
	}

	// Verify NOT at root level (consistency check)
	if gjson.Get(out, "web_search_results").Exists() {
		t.Fatal("web_search_results should NOT be at root level, should be on choices.0")
	}
	if gjson.Get(out, "citations").Exists() {
		t.Fatal("citations should NOT be at root level, should be on choices.0")
	}

	// Verify text content is present
	content := gjson.Get(out, "choices.0.message.content").String()
	if content != "The Seahawks won the Super Bowl." {
		t.Fatalf("expected text content, got %q", content)
	}
}

// --- Gemini output format test ---

func TestClaudeToGemini_StreamWebSearchAsGroundingMetadata(t *testing.T) {
	ctx := context.Background()
	model := "claude-haiku-4-5-20251001"
	reqJSON := []byte(`{}`)
	var param any

	// message_start
	sse1 := []byte(`data: {"type":"message_start","message":{"id":"msg_gem1","model":"claude-haiku-4-5-20251001","type":"message","role":"assistant","content":[],"usage":{"input_tokens":50,"output_tokens":5}}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatGemini, model, reqJSON, reqJSON, sse1, &param)

	// web_search_tool_result
	sse2 := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[{"type":"web_search_result","title":"Gemini Test","url":"https://gemini.test","encrypted_content":"gem123"}]}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatGemini, model, reqJSON, reqJSON, sse2, &param)

	// content_block_stop
	sse3 := []byte(`data: {"type":"content_block_stop","index":0}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatGemini, model, reqJSON, reqJSON, sse3, &param)

	// text block start
	sse4 := []byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatGemini, model, reqJSON, reqJSON, sse4, &param)

	// citations_delta
	sse5 := []byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","cited_text":"gemini cited","url":"https://gemini.test","title":"Gemini Test","encrypted_index":"genc1"}}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatGemini, model, reqJSON, reqJSON, sse5, &param)

	// text_delta
	sse6 := []byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello from Gemini."}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatGemini, model, reqJSON, reqJSON, sse6, &param)

	// message_delta - should have groundingMetadata
	sse7 := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`)
	results := sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatGemini, model, reqJSON, reqJSON, sse7, &param)
	if len(results) == 0 {
		t.Fatal("expected output for message_delta")
	}
	finalChunk := results[0]

	// Verify groundingMetadata exists on candidates.0
	if !gjson.Get(finalChunk, "candidates.0.groundingMetadata").Exists() {
		t.Fatalf("expected groundingMetadata on candidates.0, got: %s", finalChunk)
	}

	// Verify webSearchResults inside groundingMetadata
	wsrCount := gjson.Get(finalChunk, "candidates.0.groundingMetadata.webSearchResults.#").Int()
	if wsrCount != 1 {
		t.Fatalf("expected 1 webSearchResult in groundingMetadata, got %d: %s", wsrCount, finalChunk)
	}
	wsrTitle := gjson.Get(finalChunk, "candidates.0.groundingMetadata.webSearchResults.0.title").String()
	if wsrTitle != "Gemini Test" {
		t.Fatalf("expected webSearchResults[0].title=Gemini Test, got %q", wsrTitle)
	}

	// Verify citations inside groundingMetadata
	citCount := gjson.Get(finalChunk, "candidates.0.groundingMetadata.citations.#").Int()
	if citCount != 1 {
		t.Fatalf("expected 1 citation in groundingMetadata, got %d: %s", citCount, finalChunk)
	}
}

// --- OpenAI Responses format tests ---

// Streaming: Verifies web_search_results and citations appear on response.completed event
// inside the response object (response.web_search_results, response.citations).
func TestClaudeToOpenAIResponses_StreamWebSearchResultsAndCitations(t *testing.T) {
	ctx := context.Background()
	model := "claude-haiku-4-5-20251001"
	reqJSON := []byte(`{}`)
	var param any

	// message_start
	sse1 := []byte(`data: {"type":"message_start","message":{"id":"msg_resp1","model":"claude-haiku-4-5-20251001","type":"message","role":"assistant","content":[],"usage":{"input_tokens":80,"output_tokens":5}}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, sse1, &param)

	// server_tool_use (should be silently skipped)
	sse2 := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_resp","name":"web_search","input":{}}}`)
	results := sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, sse2, &param)
	if len(results) != 0 {
		t.Fatalf("expected empty output for server_tool_use in responses format, got %d results", len(results))
	}

	// content_block_stop for server_tool_use
	sse3 := []byte(`data: {"type":"content_block_stop","index":0}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, sse3, &param)

	// web_search_tool_result
	sse4 := []byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_resp","content":[{"type":"web_search_result","title":"Responses Test","url":"https://responses.test","encrypted_content":"resp123"}]}}`)
	results = sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, sse4, &param)
	if len(results) != 0 {
		t.Fatalf("expected empty output for web_search_tool_result in responses format, got %d results", len(results))
	}

	// content_block_stop
	sse5 := []byte(`data: {"type":"content_block_stop","index":1}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, sse5, &param)

	// text block
	sse6 := []byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, sse6, &param)

	// citations_delta
	sse7 := []byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","cited_text":"responses cited","url":"https://responses.test","title":"Responses Test","encrypted_index":"renc1"}}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, sse7, &param)

	// text_delta
	sse8 := []byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Responses answer."}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, sse8, &param)

	// content_block_stop
	sse9 := []byte(`data: {"type":"content_block_stop","index":2}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, sse9, &param)

	// message_delta (usage update)
	sse10 := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":30}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, sse10, &param)

	// message_stop (triggers response.completed in Responses format)
	sse11 := []byte(`data: {"type":"message_stop"}`)
	results = sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, sse11, &param)

	// Find the response.completed event
	var completedEvent string
	for _, r := range results {
		if gjson.Get(r, "type").String() == "response.completed" {
			completedEvent = r
			break
		}
	}
	if completedEvent == "" {
		t.Fatalf("expected response.completed event, got events: %v", results)
	}

	// Verify web_search_results on response object
	wsrCount := gjson.Get(completedEvent, "response.web_search_results.#").Int()
	if wsrCount != 1 {
		t.Fatalf("expected 1 web_search_result on response, got %d: %s", wsrCount, completedEvent)
	}
	wsrTitle := gjson.Get(completedEvent, "response.web_search_results.0.title").String()
	if wsrTitle != "Responses Test" {
		t.Fatalf("expected web_search_results[0].title=Responses Test, got %q", wsrTitle)
	}

	// Verify citations on response object
	citCount := gjson.Get(completedEvent, "response.citations.#").Int()
	if citCount != 1 {
		t.Fatalf("expected 1 citation on response, got %d: %s", citCount, completedEvent)
	}
	citURL := gjson.Get(completedEvent, "response.citations.0.url").String()
	if citURL != "https://responses.test" {
		t.Fatalf("expected citations[0].url=https://responses.test, got %q", citURL)
	}
}

// Non-streaming: Verifies web_search_results and citations on root (which IS the response object).
func TestClaudeToOpenAIResponses_NonStreamWebSearchResultsAndCitations(t *testing.T) {
	ctx := context.Background()
	model := "claude-haiku-4-5-20251001"
	reqJSON := []byte(`{}`)

	allSSE := []byte(
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_respns\",\"model\":\"claude-haiku-4-5-20251001\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":80,\"output_tokens\":5}}}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"server_tool_use\",\"id\":\"srvtoolu_ns\",\"name\":\"web_search\",\"input\":{}}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"web_search_tool_result\",\"tool_use_id\":\"srvtoolu_ns\",\"content\":[{\"type\":\"web_search_result\",\"title\":\"NonStream Resp\",\"url\":\"https://nonstream.test\",\"encrypted_content\":\"ns1\"}]}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"citations_delta\",\"citation\":{\"type\":\"web_search_result_location\",\"cited_text\":\"ns cited\",\"url\":\"https://nonstream.test\",\"title\":\"NonStream Resp\",\"encrypted_index\":\"nsenc\"}}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"text_delta\",\"text\":\"NonStream response.\"}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":2}\n\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":20}}\n\n" +
			"data: {\"type\":\"message_stop\"}\n\n")

	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, model, reqJSON, reqJSON, allSSE, &param)

	// Non-streaming Responses: out IS the response object, so web_search_results at root = response level
	wsrCount := gjson.Get(out, "web_search_results.#").Int()
	if wsrCount != 1 {
		t.Fatalf("expected 1 web_search_result, got %d: %s", wsrCount, out)
	}
	wsrTitle := gjson.Get(out, "web_search_results.0.title").String()
	if wsrTitle != "NonStream Resp" {
		t.Fatalf("expected web_search_results[0].title=NonStream Resp, got %q", wsrTitle)
	}

	// Verify citations
	citCount := gjson.Get(out, "citations.#").Int()
	if citCount != 1 {
		t.Fatalf("expected 1 citation, got %d: %s", citCount, out)
	}
	citURL := gjson.Get(out, "citations.0.url").String()
	if citURL != "https://nonstream.test" {
		t.Fatalf("expected citations[0].url=https://nonstream.test, got %q", citURL)
	}

	// Verify text content is present
	outputText := gjson.Get(out, "output.#(type==\"message\").content.0.text").String()
	if outputText != "NonStream response." {
		t.Fatalf("expected text content 'NonStream response.', got %q", outputText)
	}
}

// --- Gemini non-streaming test ---

func TestClaudeToGemini_NonStreamWebSearchAsGroundingMetadata(t *testing.T) {
	ctx := context.Background()
	model := "claude-haiku-4-5-20251001"
	reqJSON := []byte(`{}`)

	allSSE := []byte(
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_gns\",\"model\":\"claude-haiku-4-5-20251001\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":60,\"output_tokens\":5}}}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"web_search_tool_result\",\"tool_use_id\":\"srv_gns\",\"content\":[{\"type\":\"web_search_result\",\"title\":\"Gemini NS\",\"url\":\"https://gemini-ns.test\",\"encrypted_content\":\"gns1\"}]}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"citations_delta\",\"citation\":{\"type\":\"web_search_result_location\",\"cited_text\":\"gemini ns cited\",\"url\":\"https://gemini-ns.test\",\"title\":\"Gemini NS\",\"encrypted_index\":\"gnsenc\"}}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Gemini non-stream.\"}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":15}}\n\n" +
			"data: {\"type\":\"message_stop\"}\n\n")

	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatGemini, model, reqJSON, reqJSON, allSSE, &param)

	// Verify groundingMetadata on candidates.0
	if !gjson.Get(out, "candidates.0.groundingMetadata").Exists() {
		t.Fatalf("expected groundingMetadata on candidates.0, got: %s", out)
	}

	// Verify webSearchResults
	wsrCount := gjson.Get(out, "candidates.0.groundingMetadata.webSearchResults.#").Int()
	if wsrCount != 1 {
		t.Fatalf("expected 1 webSearchResult in groundingMetadata, got %d: %s", wsrCount, out)
	}
	wsrTitle := gjson.Get(out, "candidates.0.groundingMetadata.webSearchResults.0.title").String()
	if wsrTitle != "Gemini NS" {
		t.Fatalf("expected webSearchResults[0].title=Gemini NS, got %q", wsrTitle)
	}

	// Verify citations
	citCount := gjson.Get(out, "candidates.0.groundingMetadata.citations.#").Int()
	if citCount != 1 {
		t.Fatalf("expected 1 citation in groundingMetadata, got %d: %s", citCount, out)
	}
	citURL := gjson.Get(out, "candidates.0.groundingMetadata.citations.0.url").String()
	if citURL != "https://gemini-ns.test" {
		t.Fatalf("expected citations[0].url=https://gemini-ns.test, got %q", citURL)
	}

	// Verify text content
	textPart := gjson.Get(out, "candidates.0.content.parts.0.text").String()
	if textPart != "Gemini non-stream." {
		t.Fatalf("expected text='Gemini non-stream.', got %q", textPart)
	}
}

// --- Accumulator reset test ---

func TestClaudeToOpenAI_StreamAccumulatorResetOnNewMessage(t *testing.T) {
	ctx := context.Background()
	model := "claude-haiku-4-5-20251001"
	reqJSON := []byte(`{}`)
	var param any

	// First message with web search data
	sse1 := []byte(`data: {"type":"message_start","message":{"id":"msg_first","model":"claude-haiku-4-5-20251001","type":"message","role":"assistant","content":[],"usage":{"input_tokens":50,"output_tokens":5}}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, sse1, &param)

	// Add web search result
	sse2 := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[{"type":"web_search_result","title":"First Message Result","url":"https://first.com","encrypted_content":"first"}]}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, sse2, &param)

	// Now simulate a second message_start (should reset accumulators)
	sse3 := []byte(`data: {"type":"message_start","message":{"id":"msg_second","model":"claude-haiku-4-5-20251001","type":"message","role":"assistant","content":[],"usage":{"input_tokens":50,"output_tokens":5}}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, sse3, &param)

	// Text block without any web search
	sse4 := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, sse4, &param)

	sse5 := []byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"No search here."}}`)
	sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, sse5, &param)

	// message_delta for second message - should NOT have web_search_results from first message
	sse6 := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}`)
	results := sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, model, reqJSON, reqJSON, sse6, &param)
	if len(results) == 0 {
		t.Fatal("expected output for message_delta")
	}
	finalChunk := results[0]

	// Verify NO web_search_results leaked from first message
	if gjson.Get(finalChunk, "choices.0.web_search_results").Exists() {
		t.Fatalf("web_search_results from first message leaked to second message: %s", finalChunk)
	}
	if gjson.Get(finalChunk, "choices.0.citations").Exists() {
		t.Fatalf("citations from first message leaked to second message: %s", finalChunk)
	}
}
