package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
)

func registerTestWebSearchModel(t *testing.T, clientID, provider, modelID string, supportsSearch bool) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{
		{
			ID:                modelID,
			SupportsWebSearch: supportsSearch,
		},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})
}

func TestConvertOpenAIResponsesRequestToGemini_WebSearchCapabilityGate(t *testing.T) {
	capableModel := "gemini-search-capable"
	incapableModel := "gemini-search-incapable"

	registerTestWebSearchModel(t, "client-capable", "antigravity", capableModel, true)
	registerTestWebSearchModel(t, "client-incapable", "antigravity", incapableModel, false)

	req := []byte(`{
		"model": "dummy",
		"input": "test query",
		"tools": [{"type": "web_search"}]
	}`)

	// Capable model should receive googleSearch tool
	capableOut := ConvertOpenAIResponsesRequestToGemini(capableModel, req, false)
	toolsCapable := gjson.GetBytes(capableOut, "tools").Array()
	foundGoogleSearch := false
	for _, tool := range toolsCapable {
		if tool.Get("googleSearch").Exists() {
			foundGoogleSearch = true
			break
		}
	}
	if !foundGoogleSearch {
		t.Fatalf("expected googleSearch tool for capable model, got: %s", capableOut)
	}

	// Incapable model must NOT receive googleSearch tool
	incapableOut := ConvertOpenAIResponsesRequestToGemini(incapableModel, req, false)
	toolsIncapable := gjson.GetBytes(incapableOut, "tools")
	if toolsIncapable.Exists() {
		for _, tool := range toolsIncapable.Array() {
			if tool.Get("googleSearch").Exists() {
				t.Fatalf("incapable model should not receive googleSearch tool, got: %s", incapableOut)
			}
		}
	}
}

func TestConvertOpenAIResponsesRequestToGemini_WebSearchAllowedDomains(t *testing.T) {
	modelID := "gemini-search-domains"
	registerTestWebSearchModel(t, "client-domains", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-search-domains",
		"input": "search query",
		"tools": [{
			"type": "web_search",
			"filters": {
				"allowed_domains": ["go.dev", "github.com"]
			}
		}]
	}`)

	out := ConvertOpenAIResponsesRequestToGemini(modelID, req, false)
	domains := gjson.GetBytes(out, "tools.0.googleSearch.includedDomains").Array()
	if len(domains) != 2 {
		t.Fatalf("expected 2 includedDomains, got %d: %s", len(domains), out)
	}
	if domains[0].String() != "go.dev" || domains[1].String() != "github.com" {
		t.Fatalf("unexpected domains: %v", domains)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_WebSearchToolChoiceNoneSuppresses(t *testing.T) {
	modelID := "gemini-search-suppress"
	registerTestWebSearchModel(t, "client-suppress", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-search-suppress",
		"input": "search query",
		"tools": [{"type": "web_search"}],
		"tool_choice": "none"
	}`)

	out := ConvertOpenAIResponsesRequestToGemini(modelID, req, false)
	if tools := gjson.GetBytes(out, "tools"); tools.Exists() {
		for _, tool := range tools.Array() {
			if tool.Get("googleSearch").Exists() {
				t.Fatalf("tool_choice: none should suppress googleSearch, got: %s", out)
			}
		}
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_GroundingMetadata(t *testing.T) {
	geminiResp := []byte(`{
		"responseId": "resp_test123",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{
					"text": "Go 1.27 is the latest release."
				}]
			},
			"groundingMetadata": {
				"webSearchQueries": ["latest Go release"],
				"groundingChunks": [
					{"web": {"uri": "https://go.dev/dl/", "title": "Download Go"}}
				],
				"groundingSupports": [{
					"groundingChunkIndices": [0],
					"segment": {
						"startIndex": 0,
						"endIndex": 7,
						"text": "Go 1.27"
					}
				}]
			}
		}],
		"usageMetadata": {
			"promptTokenCount": 15,
			"candidatesTokenCount": 8,
			"totalTokenCount": 23
		}
	}`)

	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-test", nil, nil, geminiResp, nil)
	parsed := gjson.ParseBytes(out)

	// Check output array has 2 items: web_search_call and message
	outputs := parsed.Get("output").Array()
	if len(outputs) != 2 {
		t.Fatalf("expected 2 output items, got %d: %s", len(outputs), out)
	}

	// First item: web_search_call
	wsCall := outputs[0]
	if wsCall.Get("type").String() != "web_search_call" {
		t.Fatalf("expected first output item type web_search_call, got %q", wsCall.Get("type").String())
	}
	if wsCall.Get("action.type").String() != "search" {
		t.Fatalf("expected action.type search, got %q", wsCall.Get("action.type").String())
	}
	if wsCall.Get("action.query").String() != "latest Go release" {
		t.Fatalf("expected action.query 'latest Go release', got %q", wsCall.Get("action.query").String())
	}
	sources := wsCall.Get("action.sources").Array()
	if len(sources) != 1 || sources[0].Get("url").String() != "https://go.dev/dl/" {
		t.Fatalf("unexpected sources: %s", wsCall.Get("action.sources").Raw)
	}

	// Second item: message with url_citation annotation
	msg := outputs[1]
	if msg.Get("type").String() != "message" {
		t.Fatalf("expected second output item type message, got %q", msg.Get("type").String())
	}
	citations := msg.Get("content.0.annotations").Array()
	if len(citations) != 1 {
		t.Fatalf("expected 1 citation, got %d: %s", len(citations), msg.Raw)
	}
	citation := citations[0]
	if citation.Get("type").String() != "url_citation" {
		t.Fatalf("expected citation type url_citation, got %q", citation.Get("type").String())
	}
	if citation.Get("url").String() != "https://go.dev/dl/" {
		t.Fatalf("expected citation url 'https://go.dev/dl/', got %q", citation.Get("url").String())
	}
	if citation.Get("title").String() != "Download Go" {
		t.Fatalf("expected citation title 'Download Go', got %q", citation.Get("title").String())
	}
	if citation.Get("start_index").Int() != 0 || citation.Get("end_index").Int() != 7 {
		t.Fatalf("expected start_index=0, end_index=7, got start=%d end=%d", citation.Get("start_index").Int(), citation.Get("end_index").Int())
	}

	// Tool usage check
	if parsed.Get("tool_usage.web_search.num_requests").Int() != 1 {
		t.Fatalf("expected tool_usage.web_search.num_requests = 1, got %d", parsed.Get("tool_usage.web_search.num_requests").Int())
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_WebSearch(t *testing.T) {
	modelID := "gemini-search-stream"
	registerTestWebSearchModel(t, "client-stream", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-search-stream",
		"input": "search query",
		"tools": [{"type": "web_search"}]
	}`)

	chunk1 := []byte(`data: {
		"responseId": "stream_resp_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Found information."}]
			},
			"groundingMetadata": {
				"webSearchQueries": ["search query"],
				"groundingChunks": [{"web": {"uri": "https://example.com", "title": "Example"}}],
				"groundingSupports": [{
					"groundingChunkIndices": [0],
					"segment": {"startIndex": 0, "endIndex": 5, "text": "Found"}
				}]
			}
		}]
	}`)

	chunk2 := []byte(`data: {
		"candidates": [{"finishReason": "STOP"}],
		"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15}
	}`)

	var param any
	events1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	events2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)

	allEvents := append(events1, events2...)
	var eventTypes []string
	var fullSSE strings.Builder
	for _, ev := range allEvents {
		fullSSE.Write(ev)
		for _, line := range strings.Split(string(ev), "\n") {
			if strings.HasPrefix(line, "event: ") {
				eventTypes = append(eventTypes, strings.TrimPrefix(line, "event: "))
			}
		}
	}

	// Verify event progression
	expectedEvents := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added", // web_search_call
		"response.web_search_call.searching",
		"response.web_search_call.completed",
		"response.output_item.done",  // web_search_call done
		"response.output_item.added", // message
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done", // message done
		"response.completed",
	}

	for _, expected := range expectedEvents {
		found := false
		for _, actual := range eventTypes {
			if actual == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing expected event %q in event sequence: %v\nFull SSE:\n%s", expected, eventTypes, fullSSE.String())
		}
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_NoGroundingDoesNotEmitWebSearchCall(t *testing.T) {
	modelID := "gemini-search-noground"
	registerTestWebSearchModel(t, "client-noground", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-search-noground",
		"input": "Calculate 2+2",
		"tools": [{"type": "web_search"}]
	}`)

	chunk1 := []byte(`data: {
		"responseId": "stream_noground_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "2+2=4"}]
			}
		}]
	}`)

	chunk2 := []byte(`data: {
		"candidates": [{"finishReason": "STOP"}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 5, "totalTokenCount": 10}
	}`)

	var param any
	events1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	events2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)

	allEvents := append(events1, events2...)
	for _, ev := range allEvents {
		evStr := string(ev)
		if strings.Contains(evStr, "web_search_call") {
			t.Fatalf("did not expect web_search_call when no grounding occurred, got event: %s", evStr)
		}
		if strings.Contains(evStr, `"tool_usage"`) {
			t.Fatalf("did not expect tool_usage when no grounding occurred, got event: %s", evStr)
		}
	}
}

func TestBuildResponsesURLCitations_RuneOffsetConversion(t *testing.T) {
	fullText := "Go语言的最新版本是Go 1.27。"
	// "Go语言的最新版本是" has 10 runes, 26 UTF-8 bytes.
	// "Go 1.27" has 7 runes, 7 UTF-8 bytes.
	// Start byte = 26 (rune 10). End byte = 26 + 7 = 33 (rune 17).
	rawJSON := `{
		"groundingChunks": [{"web": {"uri": "https://go.dev", "title": "Go"}}],
		"groundingSupports": [
			{
				"groundingChunkIndices": [0],
				"segment": {"startIndex": 26, "endIndex": 33}
			},
			{
				"groundingChunkIndices": [0],
				"segment": {"startIndex": 33, "endIndex": 20}
			}
		]
	}`
	gm := gjson.Parse(rawJSON)

	citations := BuildResponsesURLCitations(gm, fullText)
	if len(citations) != 1 {
		t.Fatalf("expected exactly 1 citation (inverted one dropped), got %d", len(citations))
	}

	cite := gjson.ParseBytes(citations[0])
	startRune := cite.Get("start_index").Int()
	endRune := cite.Get("end_index").Int()

	if startRune != 10 {
		t.Fatalf("start_index = %d, want 10 runes", startRune)
	}
	if endRune != 17 {
		t.Fatalf("end_index = %d, want 17 runes", endRune)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_WebSearchOnlyToolChoiceRequiredNoFunctions(t *testing.T) {
	modelID := "gemini-search-only-req"
	registerTestWebSearchModel(t, "client-req", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-search-only-req",
		"input": "search query",
		"tools": [{"type": "web_search"}],
		"tool_choice": "required"
	}`)

	out := ConvertOpenAIResponsesRequestToGemini(modelID, req, false)
	parsed := gjson.ParseBytes(out)

	// googleSearch should be present
	if !parsed.Get("tools.0.googleSearch").Exists() {
		t.Fatalf("expected googleSearch in tools, got: %s", out)
	}
	// functionCallingConfig should NOT be present since functionDeclarations are empty
	if parsed.Get("toolConfig.functionCallingConfig").Exists() {
		t.Fatalf("functionCallingConfig should not be set when no function declarations exist, got: %s", out)
	}
}

func TestHasValidWebGrounding(t *testing.T) {
	empty := gjson.Parse(`{}`)
	if HasValidWebGrounding(empty) {
		t.Fatal("empty metadata should not be valid web grounding")
	}

	noWeb := gjson.Parse(`{"groundingChunks": [{"other": "something"}]}`)
	if HasValidWebGrounding(noWeb) {
		t.Fatal("metadata without web uri should not be valid web grounding")
	}

	withQuery := gjson.Parse(`{"webSearchQueries": ["query"]}`)
	if !HasValidWebGrounding(withQuery) {
		t.Fatal("metadata with webSearchQueries should be valid web grounding")
	}

	withChunk := gjson.Parse(`{"groundingChunks": [{"web": {"uri": "https://example.com"}}]}`)
	if !HasValidWebGrounding(withChunk) {
		t.Fatal("metadata with web chunk should be valid web grounding")
	}
}

func TestModelSupportsWebSearch_StaticVetoTakesPrecedence(t *testing.T) {
	modelID := "gemini-veto-test-model"
	vetoFalse := false

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("client-veto", "antigravity", []*registry.ModelInfo{
		{
			ID:                 modelID,
			SupportsWebSearch:  true,                                                // dynamic probe said true
			NativeCapabilities: &registry.NativeCapabilities{WebSearch: &vetoFalse}, // static models.json vetoes
		},
	})
	defer reg.UnregisterClient("client-veto")

	if ModelSupportsWebSearch(modelID) {
		t.Fatalf("expected ModelSupportsWebSearch to be false due to explicit veto, got true")
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_LateGroundingMetadataAndCJKOffsets(t *testing.T) {
	modelID := "gemini-search-late-grounding"
	registerTestWebSearchModel(t, "client-late-grounding", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-search-late-grounding",
		"input": "Go最新版本是多少？",
		"tools": [{"type": "web_search"}]
	}`)

	// Chunk 1: Chinese text only, NO groundingMetadata
	chunk1 := []byte(`data: {
		"responseId": "stream_late_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Go语言的最新版本是"}]
			}
		}]
	}`)

	// Chunk 2: Remaining text + groundingMetadata + finishReason STOP
	// "Go语言的最新版本是" has 10 runes and 26 bytes.
	// "Go 1.27" has 7 runes (bytes: 26 to 33).
	// Total: "Go语言的最新版本是Go 1.27。" = 18 runes, 36 bytes.
	// Segment at byte [26, 33) points to "Go 1.27" -> runes [10, 17)
	chunk2 := []byte(`data: {
		"responseId": "stream_late_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Go 1.27。"}]
			},
			"groundingMetadata": {
				"webSearchQueries": ["Go release"],
				"groundingChunks": [
					{"web": {"uri": "https://go.dev/doc/devel/release", "title": "Go Releases"}}
				],
				"groundingSupports": [
					{
						"groundingChunkIndices": [0],
						"segment": {"startIndex": 26, "endIndex": 33}
					}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 20, "totalTokenCount": 30}
	}`)

	var param any
	events1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	events2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)

	allEvents := append(events1, events2...)

	var eventTypes []string
	var completedJSON gjson.Result
	var partDoneJSON gjson.Result
	var itemDoneJSON gjson.Result

	for _, ev := range allEvents {
		lines := strings.Split(string(ev), "\n")
		var currentType string
		for _, line := range lines {
			if strings.HasPrefix(line, "event: ") {
				currentType = strings.TrimPrefix(line, "event: ")
				eventTypes = append(eventTypes, currentType)
			}
			if strings.HasPrefix(line, "data: ") {
				evBody := strings.TrimPrefix(line, "data: ")
				parsed := gjson.Parse(evBody)
				if currentType == "response.completed" {
					completedJSON = parsed
				}
				if currentType == "response.content_part.done" && parsed.Get("part.annotations.0").Exists() {
					partDoneJSON = parsed
				}
				if currentType == "response.output_item.done" && parsed.Get("item.type").String() == "message" {
					itemDoneJSON = parsed
				}
			}
		}
	}

	// 1. Verify event types sequence
	// web_search_call events must precede message deltas and message done
	wsAddedIdx := -1
	wsDoneIdx := -1
	msgAddedIdx := -1
	msgDoneIdx := -1
	completedIdx := -1

	for i, typ := range eventTypes {
		switch typ {
		case "response.output_item.added":
			if wsAddedIdx == -1 {
				wsAddedIdx = i
			} else if msgAddedIdx == -1 {
				msgAddedIdx = i
			}
		case "response.output_item.done":
			if wsDoneIdx == -1 {
				wsDoneIdx = i
			} else if msgDoneIdx == -1 {
				msgDoneIdx = i
			}
		case "response.completed":
			completedIdx = i
		}
	}

	if wsAddedIdx == -1 || wsDoneIdx == -1 || msgAddedIdx == -1 || msgDoneIdx == -1 {
		t.Fatalf("expected both web_search_call and message output items, got events: %v", eventTypes)
	}

	// web_search_call should be added and completed BEFORE message is added
	if wsDoneIdx >= msgAddedIdx {
		t.Fatalf("expected web_search_call to complete (idx=%d) before message added (idx=%d), events=%v", wsDoneIdx, msgAddedIdx, eventTypes)
	}
	if msgDoneIdx >= completedIdx {
		t.Fatalf("expected message done (idx=%d) before completed (idx=%d)", msgDoneIdx, completedIdx)
	}

	// 2. Verify Unicode rune offset conversion on the citations
	if !partDoneJSON.Exists() {
		t.Fatalf("expected content_part.done with annotations, got none. Events: %v", eventTypes)
	}
	startRune := partDoneJSON.Get("part.annotations.0.start_index").Int()
	endRune := partDoneJSON.Get("part.annotations.0.end_index").Int()
	if startRune != 10 {
		t.Fatalf("annotation start_index = %d, want 10", startRune)
	}
	if endRune != 17 {
		t.Fatalf("annotation end_index = %d, want 17", endRune)
	}

	// 3. Verify message output_item.done also has the exact rune offsets
	if itemDoneJSON.Get("item.content.0.annotations.0.start_index").Int() != 10 {
		t.Fatalf("item.content annotations start_index != 10")
	}

	// 4. Verify response.completed.output has [web_search_call, message] in order
	outputs := completedJSON.Get("response.output").Array()
	if len(outputs) < 2 {
		t.Fatalf("expected at least 2 output items in completed, got %d", len(outputs))
	}
	if outputs[0].Get("type").String() != "web_search_call" {
		t.Fatalf("output[0].type = %q, want web_search_call", outputs[0].Get("type").String())
	}
	if outputs[1].Get("type").String() != "message" {
		t.Fatalf("output[1].type = %q, want message", outputs[1].Get("type").String())
	}

	// 5. Verify ID prefix stripping on web_search_call
	wsID := outputs[0].Get("id").String()
	if !strings.HasPrefix(wsID, "ws_") || strings.HasPrefix(wsID, "ws_resp_") {
		t.Fatalf("expected web_search_call id format ws_<id> without resp_ prefix, got %q", wsID)
	}

	// 6. Verify tool_usage
	if completedJSON.Get("response.tool_usage.web_search.num_requests").Int() != 1 {
		t.Fatalf("tool_usage num_requests = %d, want 1", completedJSON.Get("response.tool_usage.web_search.num_requests").Int())
	}
}

func TestAllowsResponsesWebSearchToolChoice_AllowedTools(t *testing.T) {
	// 1. allowed_tools containing web_search should permit search
	withSearch := gjson.Parse(`{
		"tool_choice": {
			"type": "allowed_tools",
			"mode": "auto",
			"tools": [{"type": "function", "name": "lookup"}, {"type": "web_search"}]
		}
	}`)
	if !AllowsResponsesWebSearchToolChoice(withSearch) {
		t.Fatalf("expected allowed_tools containing web_search to allow search")
	}

	// 2. allowed_tools without web_search should NOT permit search
	withoutSearch := gjson.Parse(`{
		"tool_choice": {
			"type": "allowed_tools",
			"mode": "auto",
			"tools": [{"type": "function", "name": "lookup"}]
		}
	}`)
	if AllowsResponsesWebSearchToolChoice(withoutSearch) {
		t.Fatalf("expected allowed_tools without web_search to disallow search")
	}
}

func TestExtractResponsesWebSearchQuery_MultipleTextParts(t *testing.T) {
	// 1. Array of input_text parts
	flatInput := gjson.Parse(`{
		"input": [
			{"type": "input_text", "text": "Who is"},
			{"type": "input_text", "text": "the current Go release lead?"}
		]
	}`)
	got := ExtractResponsesWebSearchQuery(flatInput)
	want := "Who is\nthe current Go release lead?"
	if got != want {
		t.Fatalf("flat input query = %q, want %q", got, want)
	}

	// 2. Message with multiple content parts
	nestedInput := gjson.Parse(`{
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "input_text", "text": "Part A"},
					{"type": "input_text", "text": "Part B"}
				]
			}
		]
	}`)
	gotNested := ExtractResponsesWebSearchQuery(nestedInput)
	wantNested := "Part A\nPart B"
	if gotNested != wantNested {
		t.Fatalf("nested content query = %q, want %q", gotNested, wantNested)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_ModelAliasUsesEffectiveRequest(t *testing.T) {
	// Upstream model is capable of web search
	resolvedModel := "gemini-3.7-flash-high"
	registerTestWebSearchModel(t, "client-alias-test", "antigravity", resolvedModel, true)

	// Original request uses a custom unregistered client alias
	clientAlias := "my-unregistered-alias"
	originalReq := []byte(`{
		"model": "` + clientAlias + `",
		"input": "Search latest news",
		"tools": [{"type": "web_search"}]
	}`)

	// Effective translated upstream request has googleSearch
	effectiveReq := []byte(`{
		"model": "` + resolvedModel + `",
		"contents": [{"role": "user", "parts": [{"text": "Search latest news"}]}],
		"tools": [{"googleSearch": {}}]
	}`)

	chunk1 := []byte(`data: {
		"responseId": "stream_alias_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Breaking news:"}]
			}
		}]
	}`)

	chunk2 := []byte(`data: {
		"responseId": "stream_alias_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": " Go 1.27 released."}]
			},
			"groundingMetadata": {
				"webSearchQueries": ["latest news"],
				"groundingChunks": [{"web": {"uri": "https://example.com", "title": "News"}}]
			},
			"finishReason": "STOP"
		}]
	}`)

	var param any
	events1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), resolvedModel, originalReq, effectiveReq, chunk1, &param)
	events2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), resolvedModel, originalReq, effectiveReq, chunk2, &param)

	allEvents := append(events1, events2...)
	var completedJSON gjson.Result
	for _, ev := range allEvents {
		lines := strings.Split(string(ev), "\n")
		var currentType string
		for _, line := range lines {
			if strings.HasPrefix(line, "event: ") {
				currentType = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") && currentType == "response.completed" {
				completedJSON = gjson.Parse(strings.TrimPrefix(line, "data: "))
			}
		}
	}

	outputs := completedJSON.Get("response.output").Array()
	if len(outputs) < 2 {
		t.Fatalf("expected 2 output items in completed, got %d", len(outputs))
	}
	// web_search_call MUST precede message even when an alias was used
	if outputs[0].Get("type").String() != "web_search_call" {
		t.Fatalf("output[0].type = %q, want web_search_call", outputs[0].Get("type").String())
	}
	if outputs[1].Get("type").String() != "message" {
		t.Fatalf("output[1].type = %q, want message", outputs[1].Get("type").String())
	}
}
