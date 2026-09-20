package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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

func collectResponsesStreamEvents(events [][]byte) (eventTypes []string, byType map[string][]gjson.Result) {
	byType = make(map[string][]gjson.Result)
	for _, ev := range events {
		lines := strings.Split(string(ev), "\n")
		var currentType string
		for _, line := range lines {
			if strings.HasPrefix(line, "event: ") {
				currentType = strings.TrimPrefix(line, "event: ")
				eventTypes = append(eventTypes, currentType)
			}
			if strings.HasPrefix(line, "data: ") && currentType != "" {
				byType[currentType] = append(byType[currentType], gjson.Parse(strings.TrimPrefix(line, "data: ")))
			}
		}
	}
	return eventTypes, byType
}

func findWebSearchCallDone(payloads []gjson.Result) gjson.Result {
	for _, p := range payloads {
		if p.Get("item.type").String() == "web_search_call" {
			return p
		}
	}
	return gjson.Result{}
}

func findMessageOutputItemDone(payloads []gjson.Result) gjson.Result {
	for _, p := range payloads {
		if p.Get("item.type").String() == "message" {
			return p
		}
	}
	return gjson.Result{}
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

	supportsOnly := gjson.Parse(`{"groundingSupports": [{"groundingChunkIndices": [0]}]}`)
	if HasValidWebGrounding(supportsOnly) {
		t.Fatal("metadata with only groundingSupports should not be valid web grounding")
	}

	ragRetrieval := gjson.Parse(`{
		"groundingChunks": [{"retrievedContext": {"uri": "rag-doc-1", "title": "Internal Doc"}}],
		"groundingSupports": [{"groundingChunkIndices": [0], "segment": {"startIndex": 0, "endIndex": 10}}]
	}`)
	if HasValidWebGrounding(ragRetrieval) {
		t.Fatal("metadata with only retrievedContext chunks and supports should not be valid web grounding")
	}

	emptyWebURI := gjson.Parse(`{
		"groundingChunks": [{"web": {"uri": ""}}],
		"groundingSupports": [{"groundingChunkIndices": [0]}]
	}`)
	if HasValidWebGrounding(emptyWebURI) {
		t.Fatal("metadata with empty web uri should not be valid web grounding")
	}

	withQuery := gjson.Parse(`{"webSearchQueries": ["query"]}`)
	if !HasValidWebGrounding(withQuery) {
		t.Fatal("metadata with webSearchQueries should be valid web grounding")
	}

	withChunk := gjson.Parse(`{"groundingChunks": [{"web": {"uri": "https://example.com"}}]}`)
	if !HasValidWebGrounding(withChunk) {
		t.Fatal("metadata with web chunk should be valid web grounding")
	}

	ragWithWebQuery := gjson.Parse(`{
		"groundingChunks": [{"retrievedContext": {"uri": "rag-doc-1"}}],
		"webSearchQueries": ["actual query"]
	}`)
	if !HasValidWebGrounding(ragWithWebQuery) {
		t.Fatal("metadata with retrievedContext and valid webSearchQueries should be valid web grounding")
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

	// 3. allowed_tools containing the versioned preview alias should permit search
	withPreview := gjson.Parse(`{
		"tool_choice": {
			"type": "allowed_tools",
			"mode": "auto",
			"tools": [{"type": "web_search_preview_2025_03_11"}]
		}
	}`)
	if !AllowsResponsesWebSearchToolChoice(withPreview) {
		t.Fatalf("expected allowed_tools containing web_search_preview_2025_03_11 to allow search")
	}
}

func TestResponsesWebSearchToolTypeAliases(t *testing.T) {
	aliases := []string{"web_search", "web_search_2025_08_26", "web_search_preview", "web_search_preview_2025_03_11"}
	for _, toolType := range aliases {
		decl := gjson.Parse(`{"tools":[{"type":"` + toolType + `"}]}`)
		if !HasResponsesWebSearchTool(decl) {
			t.Fatalf("HasResponsesWebSearchTool(%q) = false, want true", toolType)
		}
		choice := gjson.Parse(`{"tool_choice":{"type":"` + toolType + `"}}`)
		if !AllowsResponsesWebSearchToolChoice(choice) {
			t.Fatalf("AllowsResponsesWebSearchToolChoice(%q) = false, want true", toolType)
		}
	}
}

func TestConvertOpenAIResponsesRequestToGemini_WebSearchPreview20250311(t *testing.T) {
	modelID := "gemini-search-preview-20250311"
	registerTestWebSearchModel(t, "client-preview-20250311", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-search-preview-20250311",
		"input": "search query",
		"tools": [{"type": "web_search_preview_2025_03_11"}],
		"tool_choice": {"type": "web_search_preview_2025_03_11"}
	}`)

	out := ConvertOpenAIResponsesRequestToGemini(modelID, req, false)
	if !gjson.GetBytes(out, "tools.0.googleSearch").Exists() {
		t.Fatalf("expected googleSearch tool for web_search_preview_2025_03_11 declaration and tool_choice, got: %s", out)
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

func TestConvertGeminiResponseToOpenAIResponsesStream_InterleavedTextAndFunctionCallPreservesTrailingText(t *testing.T) {
	modelID := "gemini-search-interleaved"
	registerTestWebSearchModel(t, "client-interleaved", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-search-interleaved",
		"input": "search and calc",
		"tools": [{"type": "web_search"}, {"type": "function", "function": {"name": "calculate"}}]
	}`)

	// Upstream returns text A -> functionCall -> text B -> STOP without grounding metadata
	chunk1 := []byte(`data: {
		"responseId": "stream_interleaved_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Text A"}]
			}
		}]
	}`)

	chunk2 := []byte(`data: {
		"responseId": "stream_interleaved_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"functionCall": {"name": "calculate", "args": {"expr": "1+1"}}}]
			}
		}]
	}`)

	chunk3 := []byte(`data: {
		"responseId": "stream_interleaved_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Text B"}]
			}
		}]
	}`)

	chunk4 := []byte(`data: {
		"responseId": "stream_interleaved_1",
		"candidates": [{
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
	}`)

	var param any
	e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
	e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
	e4 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk4, &param)

	allEvents := append(append(append(e1, e2...), e3...), e4...)

	var textBDeltas []string
	var completedJSON gjson.Result
	for _, ev := range allEvents {
		lines := strings.Split(string(ev), "\n")
		var currentType string
		for _, line := range lines {
			if strings.HasPrefix(line, "event: ") {
				currentType = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				dataStr := strings.TrimPrefix(line, "data: ")
				if currentType == "response.output_text.delta" {
					delta := gjson.Get(dataStr, "delta").String()
					if strings.Contains(delta, "Text B") {
						textBDeltas = append(textBDeltas, delta)
					}
				}
				if currentType == "response.completed" {
					completedJSON = gjson.Parse(dataStr)
				}
			}
		}
	}

	if len(textBDeltas) == 0 {
		t.Fatalf("expected output_text.delta event containing 'Text B', but got none")
	}

	outputs := completedJSON.Get("response.output").Array()
	if len(outputs) != 3 {
		t.Fatalf("expected 3 output items (message, function_call, message), got %d: %s", len(outputs), completedJSON.Raw)
	}

	if outputs[0].Get("type").String() != "message" || outputs[0].Get("content.0.text").String() != "Text A" {
		t.Fatalf("unexpected outputs[0]: %s", outputs[0].Raw)
	}
	if outputs[1].Get("type").String() != "function_call" || outputs[1].Get("name").String() != "calculate" {
		t.Fatalf("unexpected outputs[1]: %s", outputs[1].Raw)
	}
	if outputs[2].Get("type").String() != "message" || outputs[2].Get("content.0.text").String() != "Text B" {
		t.Fatalf("unexpected outputs[2]: %s", outputs[2].Raw)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_OutputIndexMatchesCompletedOrderWithLateGrounding(t *testing.T) {
	modelID := "gemini-search-late-order"
	registerTestWebSearchModel(t, "client-late-order", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-search-late-order",
		"input": "test query",
		"tools": [{"type": "web_search"}, {"type": "function", "function": {"name": "query_db"}}]
	}`)

	// Text A is flushed early because function call arrives
	chunk1 := []byte(`data: {
		"responseId": "stream_late_order_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Searching database..."}]
			}
		}]
	}`)

	chunk2 := []byte(`data: {
		"responseId": "stream_late_order_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"functionCall": {"name": "query_db", "args": {}}}]
			}
		}]
	}`)

	chunk3 := []byte(`data: {
		"responseId": "stream_late_order_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Results found."}]
			}
		}]
	}`)

	// Late grounding metadata arrives with web_search queries
	chunk4 := []byte(`data: {
		"responseId": "stream_late_order_1",
		"candidates": [{
			"groundingMetadata": {
				"webSearchQueries": ["test query"],
				"groundingChunks": [{"web": {"uri": "https://example.com/db", "title": "DB"}}]
			}
		}]
	}`)

	chunk5 := []byte(`data: {
		"responseId": "stream_late_order_1",
		"candidates": [{
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
	}`)

	var param any
	e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
	e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
	e4 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk4, &param)
	e5 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk5, &param)

	allEvents := append(append(append(append(e1, e2...), e3...), e4...), e5...)

	type itemAddedInfo struct {
		outputIndex int
		itemType    string
		itemID      string
	}
	var addedItems []itemAddedInfo
	var completedJSON gjson.Result

	for _, ev := range allEvents {
		lines := strings.Split(string(ev), "\n")
		var currentType string
		for _, line := range lines {
			if strings.HasPrefix(line, "event: ") {
				currentType = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				dataStr := strings.TrimPrefix(line, "data: ")
				if currentType == "response.output_item.added" {
					idx := int(gjson.Get(dataStr, "output_index").Int())
					typ := gjson.Get(dataStr, "item.type").String()
					id := gjson.Get(dataStr, "item.id").String()
					addedItems = append(addedItems, itemAddedInfo{outputIndex: idx, itemType: typ, itemID: id})
				}
				if currentType == "response.completed" {
					completedJSON = gjson.Parse(dataStr)
				}
			}
		}
	}

	outputs := completedJSON.Get("response.output").Array()
	if len(outputs) != len(addedItems) {
		t.Fatalf("mismatch: emitted %d added items, but response.completed has %d outputs", len(addedItems), len(outputs))
	}

	for i, added := range addedItems {
		outItem := outputs[i]
		outType := outItem.Get("type").String()
		outID := outItem.Get("id").String()
		if outType != added.itemType {
			t.Fatalf("output[%d] type mismatch: emitted %q at index %d, got %q in completed", i, added.itemType, added.outputIndex, outType)
		}
		if outID != added.itemID {
			t.Fatalf("output[%d] ID mismatch: emitted %q at index %d, got %q in completed", i, added.itemID, added.outputIndex, outID)
		}
	}
}

func TestConvertGeminiResponseToOpenAIResponses_CitationsRespectPartIndexAndMessage(t *testing.T) {
	t.Run("multi-part within single message", func(t *testing.T) {
		geminiResp := []byte(`{
			"responseId": "resp_multipart_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [
						{"text": "Intro "},
						{"text": "Fact"}
					]
				},
				"groundingMetadata": {
					"webSearchQueries": ["fact query"],
					"groundingChunks": [{"web": {"uri": "https://example.com/fact", "title": "Fact Source"}}],
					"groundingSupports": [{
						"groundingChunkIndices": [0],
						"segment": {
							"partIndex": 1,
							"startIndex": 0,
							"endIndex": 4,
							"text": "Fact"
						}
					}]
				}
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 5, "totalTokenCount": 10}
		}`)

		out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-test", nil, nil, geminiResp, nil)
		parsed := gjson.ParseBytes(out)

		msg := parsed.Get("response.output.#(type=message)")
		if !msg.Exists() {
			msg = parsed.Get("output.#(type=message)")
		}
		if !msg.Exists() {
			t.Fatalf("expected message output item, got: %s", out)
		}

		citations := msg.Get("content.0.annotations").Array()
		if len(citations) != 1 {
			t.Fatalf("expected 1 citation, got %d: %s", len(citations), msg.Raw)
		}

		cite := citations[0]
		// In "Intro Fact": "Intro " is 6 runes [0, 6). "Fact" is 4 runes [6, 10).
		startIdx := cite.Get("start_index").Int()
		endIdx := cite.Get("end_index").Int()
		if startIdx != 6 || endIdx != 10 {
			t.Fatalf("expected start_index=6, end_index=10 referencing 'Fact', got start=%d end=%d", startIdx, endIdx)
		}
	})

	t.Run("multi-message separated by function calls", func(t *testing.T) {
		geminiResp := []byte(`{
			"responseId": "resp_multimsg_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [
						{"text": "First intro "},
						{"functionCall": {"name": "search", "args": {}}},
						{"text": "Second fact"}
					]
				},
				"groundingMetadata": {
					"webSearchQueries": ["second query"],
					"groundingChunks": [{"web": {"uri": "https://example.com/second", "title": "Second Source"}}],
					"groundingSupports": [{
						"groundingChunkIndices": [0],
						"segment": {
							"partIndex": 2,
							"startIndex": 7,
							"endIndex": 11,
							"text": "fact"
						}
					}]
				}
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
		}`)

		out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-test", nil, nil, geminiResp, nil)
		parsed := gjson.ParseBytes(out)

		outputs := parsed.Get("output").Array()
		// outputs: [web_search_call, message_0, function_call, message_1]
		var firstMsg, secondMsg gjson.Result
		for _, item := range outputs {
			if item.Get("type").String() == "message" {
				if !firstMsg.Exists() {
					firstMsg = item
				} else {
					secondMsg = item
				}
			}
		}

		if !firstMsg.Exists() || !secondMsg.Exists() {
			t.Fatalf("expected two messages, got: %s", out)
		}

		// First message must NOT have citations meant for second message
		if len(firstMsg.Get("content.0.annotations").Array()) != 0 {
			t.Fatalf("first message should have 0 annotations, got: %s", firstMsg.Get("content.0.annotations").Raw)
		}

		// Second message must have the citation for "fact" [7, 11)
		secondCitations := secondMsg.Get("content.0.annotations").Array()
		if len(secondCitations) != 1 {
			t.Fatalf("second message should have 1 annotation, got %d: %s", len(secondCitations), secondMsg.Raw)
		}
		startIdx := secondCitations[0].Get("start_index").Int()
		endIdx := secondCitations[0].Get("end_index").Int()
		if startIdx != 7 || endIdx != 11 {
			t.Fatalf("expected second message start=7 end=11, got start=%d end=%d", startIdx, endIdx)
		}
	})

	t.Run("non-ASCII CJK text multi-part", func(t *testing.T) {
		geminiResp := []byte(`{
			"responseId": "resp_cjk_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [
						{"text": "Go语言的最新版本是"},
						{"text": "Go 1.27。"}
					]
				},
				"groundingMetadata": {
					"webSearchQueries": ["Go最新版本"],
					"groundingChunks": [{"web": {"uri": "https://go.dev", "title": "Go"}}],
					"groundingSupports": [{
						"groundingChunkIndices": [0],
						"segment": {
							"partIndex": 1,
							"startIndex": 0,
							"endIndex": 7,
							"text": "Go 1.27"
						}
					}]
				}
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
		}`)

		out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-test", nil, nil, geminiResp, nil)
		parsed := gjson.ParseBytes(out)

		msg := parsed.Get("output.#(type=message)")
		citations := msg.Get("content.0.annotations").Array()
		if len(citations) != 1 {
			t.Fatalf("expected 1 citation, got %d: %s", len(citations), msg.Raw)
		}
		// "Go语言的最新版本是" has 10 runes.
		// "Go 1.27" has 7 runes.
		// So in merged message "Go语言的最新版本是Go 1.27。", start=10, end=17
		startIdx := citations[0].Get("start_index").Int()
		endIdx := citations[0].Get("end_index").Int()
		if startIdx != 10 || endIdx != 17 {
			t.Fatalf("expected start=10 end=17, got start=%d end=%d", startIdx, endIdx)
		}
	})

	t.Run("citation span across multiple messages", func(t *testing.T) {
		geminiResp := []byte(`{
			"responseId": "resp_multimsg_span_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [
						{"text": "Hello ", "partIndex": 0},
						{"functionCall": {"name": "search", "args": {}}, "partIndex": 1},
						{"text": "world", "partIndex": 0}
					]
				},
				"groundingMetadata": {
					"webSearchQueries": ["span query"],
					"groundingChunks": [{"web": {"uri": "https://example.com/span", "title": "Span Source"}}],
					"groundingSupports": [{
						"groundingChunkIndices": [0],
						"segment": {
							"partIndex": 0,
							"startIndex": 0,
							"endIndex": 11,
							"text": "Hello world"
						}
					}]
				}
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
		}`)

		out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-test", nil, nil, geminiResp, nil)
		parsed := gjson.ParseBytes(out)

		outputs := parsed.Get("output").Array()
		var firstMsg, secondMsg gjson.Result
		for _, item := range outputs {
			if item.Get("type").String() == "message" {
				if !firstMsg.Exists() {
					firstMsg = item
				} else {
					secondMsg = item
				}
			}
		}

		if !firstMsg.Exists() || !secondMsg.Exists() {
			t.Fatalf("expected two messages, got: %s", out)
		}

		firstCitations := firstMsg.Get("content.0.annotations").Array()
		if len(firstCitations) != 1 {
			t.Fatalf("expected first message to have 1 citation, got %d: %s", len(firstCitations), firstMsg.Raw)
		}
		if firstCitations[0].Get("start_index").Int() != 0 || firstCitations[0].Get("end_index").Int() != 6 {
			t.Fatalf("expected first message citation [0, 6), got [%d, %d)", firstCitations[0].Get("start_index").Int(), firstCitations[0].Get("end_index").Int())
		}
		if firstCitations[0].Get("url").String() != "https://example.com/span" {
			t.Fatalf("expected url https://example.com/span, got %q", firstCitations[0].Get("url").String())
		}

		secondCitations := secondMsg.Get("content.0.annotations").Array()
		if len(secondCitations) != 1 {
			t.Fatalf("expected second message to have 1 citation, got %d: %s", len(secondCitations), secondMsg.Raw)
		}
		if secondCitations[0].Get("start_index").Int() != 0 || secondCitations[0].Get("end_index").Int() != 5 {
			t.Fatalf("expected second message citation [0, 5), got [%d, %d)", secondCitations[0].Get("start_index").Int(), secondCitations[0].Get("end_index").Int())
		}
		if secondCitations[0].Get("url").String() != "https://example.com/span" {
			t.Fatalf("expected url https://example.com/span, got %q", secondCitations[0].Get("url").String())
		}
	})
}

func TestBuildResponsesURLCitationsForMessages_MultiMessageSpan(t *testing.T) {
	t.Run("citation span covering two messages", func(t *testing.T) {
		gm := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/span", "title": "Span Title"}}
			],
			"groundingSupports": [
				{
					"segment": {"partIndex": 0, "startIndex": 0, "endIndex": 11},
					"groundingChunkIndices": [0]
				}
			]
		}`)

		mappings := []GeminiPartMapping{
			{
				PartIndex:      0,
				MessageIndex:   0,
				StartRuneInMsg: 0,
				PartText:       "Hello ",
			},
			{
				PartIndex:      0,
				MessageIndex:   1,
				StartRuneInMsg: 0,
				PartText:       "world",
			},
		}

		res := BuildResponsesURLCitationsForMessages(gm, mappings, []string{"Hello ", "world"})
		if len(res) != 2 {
			t.Fatalf("expected citations in 2 messages, got %d", len(res))
		}

		c0 := res[0]
		if len(c0) != 1 {
			t.Fatalf("expected 1 citation in message 0, got %d", len(c0))
		}
		if gjson.GetBytes(c0[0], "start_index").Int() != 0 || gjson.GetBytes(c0[0], "end_index").Int() != 6 {
			t.Fatalf("expected [0, 6) in message 0, got: %s", string(c0[0]))
		}

		c1 := res[1]
		if len(c1) != 1 {
			t.Fatalf("expected 1 citation in message 1, got %d", len(c1))
		}
		if gjson.GetBytes(c1[0], "start_index").Int() != 0 || gjson.GetBytes(c1[0], "end_index").Int() != 5 {
			t.Fatalf("expected [0, 5) in message 1, got: %s", string(c1[0]))
		}
	})

	t.Run("citation partial overlap across two messages", func(t *testing.T) {
		gm := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/span", "title": "Span Title"}}
			],
			"groundingSupports": [
				{
					"segment": {"partIndex": 0, "startIndex": 2, "endIndex": 9},
					"groundingChunkIndices": [0]
				}
			]
		}`)

		mappings := []GeminiPartMapping{
			{
				PartIndex:      0,
				MessageIndex:   0,
				StartRuneInMsg: 0,
				PartText:       "Hello ",
			},
			{
				PartIndex:      0,
				MessageIndex:   1,
				StartRuneInMsg: 0,
				PartText:       "world",
			},
		}

		res := BuildResponsesURLCitationsForMessages(gm, mappings, []string{"Hello ", "world"})
		if len(res) != 2 {
			t.Fatalf("expected citations in 2 messages, got %d", len(res))
		}

		c0 := res[0]
		if len(c0) != 1 {
			t.Fatalf("expected 1 citation in message 0, got %d", len(c0))
		}
		if gjson.GetBytes(c0[0], "start_index").Int() != 2 || gjson.GetBytes(c0[0], "end_index").Int() != 6 {
			t.Fatalf("expected [2, 6) in message 0, got: %s", string(c0[0]))
		}

		c1 := res[1]
		if len(c1) != 1 {
			t.Fatalf("expected 1 citation in message 1, got %d", len(c1))
		}
		if gjson.GetBytes(c1[0], "start_index").Int() != 0 || gjson.GetBytes(c1[0], "end_index").Int() != 3 {
			t.Fatalf("expected [0, 3) in message 1, got: %s", string(c1[0]))
		}
	})

	t.Run("citation span with non-ASCII CJK runes across two messages", func(t *testing.T) {
		gm := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/cjk", "title": "CJK Title"}}
			],
			"groundingSupports": [
				{
					"segment": {"partIndex": 0, "startIndex": 0, "endIndex": 13},
					"groundingChunkIndices": [0]
				}
			]
		}`)

		mappings := []GeminiPartMapping{
			{
				PartIndex:      0,
				MessageIndex:   0,
				StartRuneInMsg: 0,
				PartText:       "你好 ",
			},
			{
				PartIndex:      0,
				MessageIndex:   1,
				StartRuneInMsg: 0,
				PartText:       "世界",
			},
		}

		res := BuildResponsesURLCitationsForMessages(gm, mappings, []string{"你好 ", "世界"})
		if len(res) != 2 {
			t.Fatalf("expected citations in 2 messages, got %d", len(res))
		}

		c0 := res[0]
		if len(c0) != 1 {
			t.Fatalf("expected 1 citation in message 0, got %d", len(c0))
		}
		if gjson.GetBytes(c0[0], "start_index").Int() != 0 || gjson.GetBytes(c0[0], "end_index").Int() != 3 {
			t.Fatalf("expected [0, 3) in message 0, got: %s", string(c0[0]))
		}

		c1 := res[1]
		if len(c1) != 1 {
			t.Fatalf("expected 1 citation in message 1, got %d", len(c1))
		}
		if gjson.GetBytes(c1[0], "start_index").Int() != 0 || gjson.GetBytes(c1[0], "end_index").Int() != 2 {
			t.Fatalf("expected [0, 2) in message 1, got: %s", string(c1[0]))
		}
	})
}

func TestConvertGeminiResponseToOpenAIResponsesStream_DisablesBufferingWhenSearchDisabled(t *testing.T) {
	modelID := "gemini-search-nobuffer"
	registerTestWebSearchModel(t, "client-nobuffer", "antigravity", modelID, true)

	t.Run("tool_choice none disables buffering", func(t *testing.T) {
		req := []byte(`{
			"model": "gemini-search-nobuffer",
			"input": "test query",
			"tools": [{"type": "web_search"}],
			"tool_choice": "none"
		}`)

		chunk1 := []byte(`data: {
			"responseId": "stream_nobuffer_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Immediate stream text"}]
				}
			}]
		}`)

		var param any
		events := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)

		foundDelta := false
		for _, ev := range events {
			lines := strings.Split(string(ev), "\n")
			for _, line := range lines {
				if line == "event: response.output_text.delta" {
					foundDelta = true
					break
				}
			}
		}
		if !foundDelta {
			t.Fatalf("expected immediate output_text.delta event on chunk 1 when tool_choice is none, got events: %v", events)
		}
	})

	t.Run("upstream request without googleSearch disables buffering", func(t *testing.T) {
		originalReq := []byte(`{
			"model": "gemini-search-nobuffer",
			"input": "test query",
			"tools": [{"type": "web_search"}, {"type": "function", "function": {"name": "func1"}}]
		}`)

		// Effective upstream request had search stripped (e.g. Antigravity mixed tool fallback)
		upstreamReq := []byte(`{
			"model": "gemini-search-nobuffer",
			"contents": [{"role": "user", "parts": [{"text": "test query"}]}],
			"tools": [{"functionDeclarations": [{"name": "func1"}]}]
		}`)

		chunk1 := []byte(`data: {
			"responseId": "stream_nobuffer_2",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Immediate stream text"}]
				}
			}]
		}`)

		var param any
		events := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, originalReq, upstreamReq, chunk1, &param)

		foundDelta := false
		for _, ev := range events {
			lines := strings.Split(string(ev), "\n")
			for _, line := range lines {
				if line == "event: response.output_text.delta" {
					foundDelta = true
					break
				}
			}
		}
		if !foundDelta {
			t.Fatalf("expected immediate output_text.delta event on chunk 1 when upstream lacks googleSearch, got events: %v", events)
		}
	})
}

func TestConvertGeminiResponseToOpenAIResponsesStream_MultiChunkCitationsAndExplicitPartIndex(t *testing.T) {
	modelID := "gemini-search-multichunk-citations"
	registerTestWebSearchModel(t, "client-mc-citations", "antigravity", modelID, true)

	t.Run("citation spanning across multiple streamed chunks", func(t *testing.T) {
		req := []byte(`{
			"model": "gemini-search-multichunk-citations",
			"input": "test query",
			"tools": [{"type": "web_search"}]
		}`)

		chunk1 := []byte(`data: {
			"responseId": "stream_mc_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Hello "}]
				}
			}]
		}`)

		chunk2 := []byte(`data: {
			"responseId": "stream_mc_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "world"}]
				}
			}]
		}`)

		// Grounding metadata referencing segment [0, 11) for full "Hello world"
		chunk3 := []byte(`data: {
			"responseId": "stream_mc_1",
			"candidates": [{
				"groundingMetadata": {
					"webSearchQueries": ["hello query"],
					"groundingChunks": [{"web": {"uri": "https://example.com/hello", "title": "Hello"}}],
					"groundingSupports": [{
						"groundingChunkIndices": [0],
						"segment": {
							"partIndex": 0,
							"startIndex": 0,
							"endIndex": 11,
							"text": "Hello world"
						}
					}]
				}
			}]
		}`)

		chunk4 := []byte(`data: {
			"responseId": "stream_mc_1",
			"candidates": [{
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
		}`)

		var param any
		e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
		e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
		e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
		e4 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk4, &param)

		allEvents := append(append(append(e1, e2...), e3...), e4...)

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

		msg := completedJSON.Get("response.output.#(type=message)")
		if !msg.Exists() {
			t.Fatalf("expected message output item, got: %s", completedJSON.Raw)
		}
		if msg.Get("content.0.text").String() != "Hello world" {
			t.Fatalf("expected full text 'Hello world', got %q", msg.Get("content.0.text").String())
		}
		citations := msg.Get("content.0.annotations").Array()
		if len(citations) != 1 {
			t.Fatalf("expected 1 citation, got %d: %s", len(citations), msg.Raw)
		}
		startIdx := citations[0].Get("start_index").Int()
		endIdx := citations[0].Get("end_index").Int()
		if startIdx != 0 || endIdx != 11 {
			t.Fatalf("expected start_index=0, end_index=11 spanning across chunks, got start=%d end=%d", startIdx, endIdx)
		}
	})

	t.Run("citation spanning across multiple streamed chunks without partIndex", func(t *testing.T) {
		req := []byte(`{
			"model": "gemini-search-multichunk-citations",
			"input": "test query",
			"tools": [{"type": "web_search"}]
		}`)

		chunk1 := []byte(`data: {
			"responseId": "stream_mc_1b",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Hello "}]
				}
			}]
		}`)

		chunk2 := []byte(`data: {
			"responseId": "stream_mc_1b",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "world"}]
				}
			}]
		}`)

		// Grounding metadata referencing segment [0, 11) for full "Hello world" without partIndex
		chunk3 := []byte(`data: {
			"responseId": "stream_mc_1b",
			"candidates": [{
				"groundingMetadata": {
					"webSearchQueries": ["hello query"],
					"groundingChunks": [{"web": {"uri": "https://example.com/hello", "title": "Hello"}}],
					"groundingSupports": [{
						"groundingChunkIndices": [0],
						"segment": {
							"startIndex": 0,
							"endIndex": 11,
							"text": "Hello world"
						}
					}]
				}
			}]
		}`)

		chunk4 := []byte(`data: {
			"responseId": "stream_mc_1b",
			"candidates": [{
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
		}`)

		var param any
		e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
		e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
		e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
		e4 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk4, &param)

		allEvents := append(append(append(e1, e2...), e3...), e4...)

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

		msg := completedJSON.Get("response.output.#(type=message)")
		if !msg.Exists() {
			t.Fatalf("expected message output item, got: %s", completedJSON.Raw)
		}
		citations := msg.Get("content.0.annotations").Array()
		if len(citations) != 1 {
			t.Fatalf("expected 1 citation, got %d: %s", len(citations), msg.Raw)
		}
		startIdx := citations[0].Get("start_index").Int()
		endIdx := citations[0].Get("end_index").Int()
		if startIdx != 0 || endIdx != 11 {
			t.Fatalf("expected start_index=0, end_index=11 spanning across chunks without partIndex, got start=%d end=%d", startIdx, endIdx)
		}
	})

	t.Run("citation referencing text in subsequent chunk", func(t *testing.T) {
		req := []byte(`{
			"model": "gemini-search-multichunk-citations",
			"input": "test query",
			"tools": [{"type": "web_search"}]
		}`)

		chunk1 := []byte(`data: {
			"responseId": "stream_mc_2",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Hello "}]
				}
			}]
		}`)

		chunk2 := []byte(`data: {
			"responseId": "stream_mc_2",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "world"}]
				}
			}]
		}`)

		// Grounding metadata referencing segment [6, 11) for "world" arriving in chunk 2
		chunk3 := []byte(`data: {
			"responseId": "stream_mc_2",
			"candidates": [{
				"groundingMetadata": {
					"webSearchQueries": ["world query"],
					"groundingChunks": [{"web": {"uri": "https://example.com/world", "title": "World"}}],
					"groundingSupports": [{
						"groundingChunkIndices": [0],
						"segment": {
							"partIndex": 0,
							"startIndex": 6,
							"endIndex": 11,
							"text": "world"
						}
					}]
				}
			}]
		}`)

		chunk4 := []byte(`data: {
			"responseId": "stream_mc_2",
			"candidates": [{
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
		}`)

		var param any
		e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
		e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
		e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
		e4 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk4, &param)

		allEvents := append(append(append(e1, e2...), e3...), e4...)

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

		msg := completedJSON.Get("response.output.#(type=message)")
		citations := msg.Get("content.0.annotations").Array()
		if len(citations) != 1 {
			t.Fatalf("expected 1 citation, got %d: %s", len(citations), msg.Raw)
		}
		startIdx := citations[0].Get("start_index").Int()
		endIdx := citations[0].Get("end_index").Int()
		if startIdx != 6 || endIdx != 11 {
			t.Fatalf("expected start_index=6, end_index=11 for 'world', got start=%d end=%d", startIdx, endIdx)
		}
	})

	t.Run("streaming with explicit partIndex across parts", func(t *testing.T) {
		req := []byte(`{
			"model": "gemini-search-multichunk-citations",
			"input": "test query",
			"tools": [{"type": "web_search"}]
		}`)

		chunk1 := []byte(`data: {
			"responseId": "stream_mc_3",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"partIndex": 0, "text": "Intro "}]
				}
			}]
		}`)

		chunk2 := []byte(`data: {
			"responseId": "stream_mc_3",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"partIndex": 1, "text": "Fact"}]
				}
			}]
		}`)

		chunk3 := []byte(`data: {
			"responseId": "stream_mc_3",
			"candidates": [{
				"groundingMetadata": {
					"webSearchQueries": ["fact query"],
					"groundingChunks": [{"web": {"uri": "https://example.com/fact", "title": "Fact"}}],
					"groundingSupports": [{
						"groundingChunkIndices": [0],
						"segment": {
							"partIndex": 1,
							"startIndex": 0,
							"endIndex": 4,
							"text": "Fact"
						}
					}]
				}
			}]
		}`)

		chunk4 := []byte(`data: {
			"responseId": "stream_mc_3",
			"candidates": [{
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
		}`)

		var param any
		e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
		e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
		e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
		e4 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk4, &param)

		allEvents := append(append(append(e1, e2...), e3...), e4...)

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

		msg := completedJSON.Get("response.output.#(type=message)")
		citations := msg.Get("content.0.annotations").Array()
		if len(citations) != 1 {
			t.Fatalf("expected 1 citation, got %d: %s", len(citations), msg.Raw)
		}
		// "Intro " is 6 runes [0, 6). "Fact" starts at rune 6, end at 10.
		startIdx := citations[0].Get("start_index").Int()
		endIdx := citations[0].Get("end_index").Int()
		if startIdx != 6 || endIdx != 10 {
			t.Fatalf("expected start_index=6, end_index=10 for 'Fact', got start=%d end=%d", startIdx, endIdx)
		}
	})

	t.Run("streaming with explicit partIndex across multiple chunks of same part", func(t *testing.T) {
		req := []byte(`{
			"model": "gemini-search-multichunk-citations",
			"input": "test query",
			"tools": [{"type": "web_search"}]
		}`)

		chunk1 := []byte(`data: {
			"responseId": "stream_mc_4",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"partIndex": 0, "text": "Intro "}]
				}
			}]
		}`)

		chunk2 := []byte(`data: {
			"responseId": "stream_mc_4",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"partIndex": 0, "text": "continuation "}]
				}
			}]
		}`)

		chunk3 := []byte(`data: {
			"responseId": "stream_mc_4",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"partIndex": 1, "text": "Fact"}]
				}
			}]
		}`)

		chunk4 := []byte(`data: {
			"responseId": "stream_mc_4",
			"candidates": [{
				"groundingMetadata": {
					"webSearchQueries": ["all queries"],
					"groundingChunks": [
						{"web": {"uri": "https://example.com/intro", "title": "Intro"}},
						{"web": {"uri": "https://example.com/fact", "title": "Fact"}}
					],
					"groundingSupports": [
						{
							"groundingChunkIndices": [0],
							"segment": {
								"partIndex": 0,
								"startIndex": 0,
								"endIndex": 19,
								"text": "Intro continuation "
							}
						},
						{
							"groundingChunkIndices": [1],
							"segment": {
								"partIndex": 1,
								"startIndex": 0,
								"endIndex": 4,
								"text": "Fact"
							}
						}
					]
				}
			}]
		}`)

		chunk5 := []byte(`data: {
			"responseId": "stream_mc_4",
			"candidates": [{
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
		}`)

		var param any
		e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
		e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
		e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
		e4 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk4, &param)
		e5 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk5, &param)

		allEvents := append(append(append(append(e1, e2...), e3...), e4...), e5...)

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

		msg := completedJSON.Get("response.output.#(type=message)")
		citations := msg.Get("content.0.annotations").Array()
		if len(citations) != 2 {
			t.Fatalf("expected 2 citations, got %d: %s", len(citations), msg.Raw)
		}
		// First citation: "Intro continuation " [0, 19)
		if citations[0].Get("start_index").Int() != 0 || citations[0].Get("end_index").Int() != 19 {
			t.Fatalf("expected citation 0 at [0, 19), got [%d, %d)", citations[0].Get("start_index").Int(), citations[0].Get("end_index").Int())
		}
		// Second citation: "Fact" [19, 23)
		if citations[1].Get("start_index").Int() != 19 || citations[1].Get("end_index").Int() != 23 {
			t.Fatalf("expected citation 1 at [19, 23), got [%d, %d)", citations[1].Get("start_index").Int(), citations[1].Get("end_index").Int())
		}
	})
}

func TestConvertGeminiResponseToOpenAIResponsesStream_InterleavedTextAndThought(t *testing.T) {
	modelID := "gemini-search-interleaved-thought"
	registerTestWebSearchModel(t, "client-interleaved-th", "antigravity", modelID, true)

	t.Run("without grounding metadata preserves text -> thought -> text order", func(t *testing.T) {
		req := []byte(`{
			"model": "gemini-search-interleaved-thought",
			"input": "test query",
			"tools": [{"type": "web_search"}]
		}`)

		// Text A is sent first
		chunk1 := []byte(`data: {
			"responseId": "stream_it_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Text A"}]
				}
			}]
		}`)

		// Thought R arrives
		chunk2 := []byte(`data: {
			"responseId": "stream_it_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"thought": true, "text": "Reasoning R"}]
				}
			}]
		}`)

		// Text B arrives
		chunk3 := []byte(`data: {
			"responseId": "stream_it_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Text B"}]
				}
			}]
		}`)

		chunk4 := []byte(`data: {
			"responseId": "stream_it_1",
			"candidates": [{
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
		}`)

		var param any
		e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
		e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
		e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
		e4 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk4, &param)

		allEvents := append(append(append(e1, e2...), e3...), e4...)

		type itemAddedInfo struct {
			outputIndex int
			itemType    string
		}
		var addedItems []itemAddedInfo
		var completedJSON gjson.Result

		for _, ev := range allEvents {
			lines := strings.Split(string(ev), "\n")
			var currentType string
			for _, line := range lines {
				if strings.HasPrefix(line, "event: ") {
					currentType = strings.TrimPrefix(line, "event: ")
				}
				if strings.HasPrefix(line, "data: ") {
					dataStr := strings.TrimPrefix(line, "data: ")
					if currentType == "response.output_item.added" {
						idx := int(gjson.Get(dataStr, "output_index").Int())
						typ := gjson.Get(dataStr, "item.type").String()
						addedItems = append(addedItems, itemAddedInfo{outputIndex: idx, itemType: typ})
					}
					if currentType == "response.completed" {
						completedJSON = gjson.Parse(dataStr)
					}
				}
			}
		}

		if len(addedItems) != 3 {
			t.Fatalf("expected 3 added items (message, reasoning, message), got %d: %+v", len(addedItems), addedItems)
		}
		if addedItems[0].itemType != "message" || addedItems[0].outputIndex != 0 {
			t.Fatalf("expected added[0] to be message at index 0, got %+v", addedItems[0])
		}
		if addedItems[1].itemType != "reasoning" || addedItems[1].outputIndex != 1 {
			t.Fatalf("expected added[1] to be reasoning at index 1, got %+v", addedItems[1])
		}
		if addedItems[2].itemType != "message" || addedItems[2].outputIndex != 2 {
			t.Fatalf("expected added[2] to be message at index 2, got %+v", addedItems[2])
		}

		outputs := completedJSON.Get("response.output").Array()
		if len(outputs) != 3 {
			t.Fatalf("expected 3 completed outputs, got %d: %s", len(outputs), completedJSON.Raw)
		}
		if outputs[0].Get("type").String() != "message" || outputs[0].Get("content.0.text").String() != "Text A" {
			t.Fatalf("unexpected outputs[0]: %s", outputs[0].Raw)
		}
		if outputs[1].Get("type").String() != "reasoning" {
			t.Fatalf("unexpected outputs[1]: %s", outputs[1].Raw)
		}
		if outputs[2].Get("type").String() != "message" || outputs[2].Get("content.0.text").String() != "Text B" {
			t.Fatalf("unexpected outputs[2]: %s", outputs[2].Raw)
		}
	})

	t.Run("with grounding metadata preserves text -> thought -> text order and attaches citations", func(t *testing.T) {
		req := []byte(`{
			"model": "gemini-search-interleaved-thought",
			"input": "test query",
			"tools": [{"type": "web_search"}]
		}`)

		chunk1 := []byte(`data: {
			"responseId": "stream_it_2",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Text A"}]
				}
			}]
		}`)

		chunk2 := []byte(`data: {
			"responseId": "stream_it_2",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"thought": true, "text": "Reasoning R"}]
				}
			}]
		}`)

		// Grounding metadata arrives with Text B
		chunk3 := []byte(`data: {
			"responseId": "stream_it_2",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Text B"}]
				},
				"groundingMetadata": {
					"webSearchQueries": ["search B"],
					"groundingChunks": [
						{"web": {"uri": "https://example.com/a", "title": "A Source"}},
						{"web": {"uri": "https://example.com/b", "title": "B Source"}}
					],
					"groundingSupports": [
						{
							"groundingChunkIndices": [0],
							"segment": {
								"partIndex": 0,
								"startIndex": 0,
								"endIndex": 6,
								"text": "Text A"
							}
						},
						{
							"groundingChunkIndices": [1],
							"segment": {
								"partIndex": 2,
								"startIndex": 0,
								"endIndex": 6,
								"text": "Text B"
							}
						}
					]
				}
			}]
		}`)

		chunk4 := []byte(`data: {
			"responseId": "stream_it_2",
			"candidates": [{
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
		}`)

		var param any
		e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
		e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
		e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
		e4 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk4, &param)

		allEvents := append(append(append(e1, e2...), e3...), e4...)

		type itemAddedInfo struct {
			outputIndex int
			itemType    string
		}
		var addedItems []itemAddedInfo
		var completedJSON gjson.Result

		for _, ev := range allEvents {
			lines := strings.Split(string(ev), "\n")
			var currentType string
			for _, line := range lines {
				if strings.HasPrefix(line, "event: ") {
					currentType = strings.TrimPrefix(line, "event: ")
				}
				if strings.HasPrefix(line, "data: ") {
					dataStr := strings.TrimPrefix(line, "data: ")
					if currentType == "response.output_item.added" {
						idx := int(gjson.Get(dataStr, "output_index").Int())
						typ := gjson.Get(dataStr, "item.type").String()
						addedItems = append(addedItems, itemAddedInfo{outputIndex: idx, itemType: typ})
					}
					if currentType == "response.completed" {
						completedJSON = gjson.Parse(dataStr)
					}
				}
			}
		}

		// Items: Message A (0), Reasoning R (1), web_search_call (2), Message B (3)
		if len(addedItems) != 4 {
			t.Fatalf("expected 4 added items (message A, reasoning, web_search_call, message B), got %d: %+v", len(addedItems), addedItems)
		}
		if addedItems[0].itemType != "message" || addedItems[0].outputIndex != 0 {
			t.Fatalf("expected added[0] to be message at index 0, got %+v", addedItems[0])
		}
		if addedItems[1].itemType != "reasoning" || addedItems[1].outputIndex != 1 {
			t.Fatalf("expected added[1] to be reasoning at index 1, got %+v", addedItems[1])
		}
		if addedItems[2].itemType != "web_search_call" || addedItems[2].outputIndex != 2 {
			t.Fatalf("expected added[2] to be web_search_call at index 2, got %+v", addedItems[2])
		}
		if addedItems[3].itemType != "message" || addedItems[3].outputIndex != 3 {
			t.Fatalf("expected added[3] to be message at index 3, got %+v", addedItems[3])
		}

		outputs := completedJSON.Get("response.output").Array()
		if len(outputs) != 4 {
			t.Fatalf("expected 4 completed outputs, got %d: %s", len(outputs), completedJSON.Raw)
		}
		if outputs[0].Get("content.0.text").String() != "Text A" {
			t.Fatalf("unexpected outputs[0]: %s", outputs[0].Raw)
		}
		if outputs[3].Get("content.0.text").String() != "Text B" {
			t.Fatalf("unexpected outputs[3]: %s", outputs[3].Raw)
		}

		// Verify Message A has citation for "Text A"
		citationsA := outputs[0].Get("content.0.annotations").Array()
		if len(citationsA) != 1 {
			t.Fatalf("expected 1 citation on Message A, got %d: %s", len(citationsA), outputs[0].Raw)
		}
		if citationsA[0].Get("url").String() != "https://example.com/a" {
			t.Fatalf("expected citation url 'https://example.com/a', got %q", citationsA[0].Get("url").String())
		}

		// Verify Message B has citation for "Text B"
		citationsB := outputs[3].Get("content.0.annotations").Array()
		if len(citationsB) != 1 {
			t.Fatalf("expected 1 citation on Message B, got %d: %s", len(citationsB), outputs[3].Raw)
		}
		if citationsB[0].Get("url").String() != "https://example.com/b" {
			t.Fatalf("expected citation url 'https://example.com/b', got %q", citationsB[0].Get("url").String())
		}
	})
}

func TestConvertGeminiResponseToOpenAIResponsesStream_ConsecutiveBufferedTextChunksAfterClosure(t *testing.T) {
	modelID := "gemini-search-consecutive-buffered"
	registerTestWebSearchModel(t, "client-consec-buf", "antigravity", modelID, true)

	t.Run("function call followed by multiple consecutive buffered text chunks and late-arriving grounding metadata", func(t *testing.T) {
		req := []byte(`{
			"model": "gemini-search-consecutive-buffered",
			"input": "test query",
			"tools": [{"type": "web_search"}, {"type": "function", "name": "test_tool"}]
		}`)

		chunk1 := []byte(`data: {
			"responseId": "stream_cbc_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "text A"}]
				}
			}]
		}`)

		chunk2 := []byte(`data: {
			"responseId": "stream_cbc_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"functionCall": {"name": "test_tool", "args": {}}}]
				}
			}]
		}`)

		chunk3 := []byte(`data: {
			"responseId": "stream_cbc_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Hello "}]
				}
			}]
		}`)

		chunk4 := []byte(`data: {
			"responseId": "stream_cbc_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "world"}]
				}
			}]
		}`)

		chunk5 := []byte(`data: {
			"responseId": "stream_cbc_1",
			"candidates": [{
				"groundingMetadata": {
					"webSearchQueries": ["hello query"],
					"groundingChunks": [
						{"web": {"uri": "https://example.com/hello", "title": "Hello"}},
						{"web": {"uri": "https://example.com/world", "title": "World"}}
					],
					"groundingSupports": [
						{
							"groundingChunkIndices": [0],
							"segment": {
								"partIndex": 2,
								"startIndex": 0,
								"endIndex": 11,
								"text": "Hello world"
							}
						},
						{
							"groundingChunkIndices": [1],
							"segment": {
								"partIndex": 2,
								"startIndex": 6,
								"endIndex": 11,
								"text": "world"
							}
						}
					]
				}
			}]
		}`)

		chunk6 := []byte(`data: {
			"responseId": "stream_cbc_1",
			"candidates": [{
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
		}`)

		var param any
		e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
		e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
		e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
		e4 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk4, &param)
		e5 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk5, &param)
		e6 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk6, &param)

		allEvents := append(append(append(append(append(e1, e2...), e3...), e4...), e5...), e6...)

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
		// Expect: message 0 (text A), function_call 1, web_search_call 2, message 3 (Hello world)
		if len(outputs) < 4 {
			t.Fatalf("expected at least 4 output items, got %d: %s", len(outputs), completedJSON.Raw)
		}
		lastMsg := outputs[len(outputs)-1]
		if lastMsg.Get("type").String() != "message" {
			t.Fatalf("expected last output item to be message, got: %s", lastMsg.Raw)
		}
		if gotText := lastMsg.Get("content.0.text").String(); gotText != "Hello world" {
			t.Fatalf("expected last message text 'Hello world', got %q", gotText)
		}

		citations := lastMsg.Get("content.0.annotations").Array()
		if len(citations) != 2 {
			t.Fatalf("expected 2 citations on last message, got %d: %s", len(citations), lastMsg.Raw)
		}
		if citations[0].Get("start_index").Int() != 0 || citations[0].Get("end_index").Int() != 11 {
			t.Fatalf("expected citation 0 span [0, 11), got [%d, %d)", citations[0].Get("start_index").Int(), citations[0].Get("end_index").Int())
		}
		if citations[1].Get("start_index").Int() != 6 || citations[1].Get("end_index").Int() != 11 {
			t.Fatalf("expected citation 1 span [6, 11), got [%d, %d)", citations[1].Get("start_index").Int(), citations[1].Get("end_index").Int())
		}
	})

	t.Run("thought followed by multiple consecutive buffered text chunks and late-arriving grounding metadata", func(t *testing.T) {
		req := []byte(`{
			"model": "gemini-search-consecutive-buffered",
			"input": "test query",
			"tools": [{"type": "web_search"}]
		}`)

		chunk1 := []byte(`data: {
			"responseId": "stream_cbc_2",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "text A"}]
				}
			}]
		}`)

		chunk2 := []byte(`data: {
			"responseId": "stream_cbc_2",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"thought": true, "text": "Reasoning R"}]
				}
			}]
		}`)

		chunk3 := []byte(`data: {
			"responseId": "stream_cbc_2",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Hello "}]
				}
			}]
		}`)

		chunk4 := []byte(`data: {
			"responseId": "stream_cbc_2",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "world"}]
				}
			}]
		}`)

		chunk5 := []byte(`data: {
			"responseId": "stream_cbc_2",
			"candidates": [{
				"groundingMetadata": {
					"webSearchQueries": ["hello query"],
					"groundingChunks": [
						{"web": {"uri": "https://example.com/hello", "title": "Hello"}},
						{"web": {"uri": "https://example.com/world", "title": "World"}}
					],
					"groundingSupports": [
						{
							"groundingChunkIndices": [0],
							"segment": {
								"partIndex": 2,
								"startIndex": 0,
								"endIndex": 11,
								"text": "Hello world"
							}
						},
						{
							"groundingChunkIndices": [1],
							"segment": {
								"partIndex": 2,
								"startIndex": 6,
								"endIndex": 11,
								"text": "world"
							}
						}
					]
				}
			}]
		}`)

		chunk6 := []byte(`data: {
			"responseId": "stream_cbc_2",
			"candidates": [{
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
		}`)

		var param any
		e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
		e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
		e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
		e4 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk4, &param)
		e5 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk5, &param)
		e6 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk6, &param)

		allEvents := append(append(append(append(append(e1, e2...), e3...), e4...), e5...), e6...)

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
		if len(outputs) < 4 {
			t.Fatalf("expected at least 4 output items, got %d: %s", len(outputs), completedJSON.Raw)
		}
		lastMsg := outputs[len(outputs)-1]
		if lastMsg.Get("type").String() != "message" {
			t.Fatalf("expected last output item to be message, got: %s", lastMsg.Raw)
		}
		if gotText := lastMsg.Get("content.0.text").String(); gotText != "Hello world" {
			t.Fatalf("expected last message text 'Hello world', got %q", gotText)
		}

		citations := lastMsg.Get("content.0.annotations").Array()
		if len(citations) != 2 {
			t.Fatalf("expected 2 citations on last message, got %d: %s", len(citations), lastMsg.Raw)
		}
		if citations[0].Get("start_index").Int() != 0 || citations[0].Get("end_index").Int() != 11 {
			t.Fatalf("expected citation 0 span [0, 11), got [%d, %d)", citations[0].Get("start_index").Int(), citations[0].Get("end_index").Int())
		}
		if citations[1].Get("start_index").Int() != 6 || citations[1].Get("end_index").Int() != 11 {
			t.Fatalf("expected citation 1 span [6, 11), got [%d, %d)", citations[1].Get("start_index").Int(), citations[1].Get("end_index").Int())
		}
	})
}

func TestConvertGeminiResponseToOpenAIResponsesStream_ConsecutiveFunctionCallsAdvancePartIndex(t *testing.T) {
	modelID := "gemini-consecutive-fc-part-index"
	registerTestWebSearchModel(t, "client-consecutive-fc", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-consecutive-fc-part-index",
		"input": "test query",
		"tools": [
			{"type": "web_search"},
			{"type": "function", "function": {"name": "func_a", "parameters": {"type": "object"}}},
			{"type": "function", "function": {"name": "func_b", "parameters": {"type": "object"}}}
		]
	}`)

	chunk1 := []byte(`data: {
		"responseId": "stream_fc_adv_1",
		"candidates": [{
			"content": {
				"parts": [{"text": "Initial text."}],
				"role": "model"
			}
		}]
	}`)

	chunk2 := []byte(`data: {
		"responseId": "stream_fc_adv_1",
		"candidates": [{
			"content": {
				"parts": [{"functionCall": {"name": "func_a", "args": {}}}],
				"role": "model"
			}
		}]
	}`)

	chunk3 := []byte(`data: {
		"responseId": "stream_fc_adv_1",
		"candidates": [{
			"content": {
				"parts": [{"functionCall": {"name": "func_b", "args": {}}}],
				"role": "model"
			}
		}]
	}`)

	chunk4 := []byte(`data: {
		"responseId": "stream_fc_adv_1",
		"candidates": [{
			"content": {
				"parts": [{"text": "Trailing citation text."}],
				"role": "model"
			}
		}]
	}`)

	chunk5 := []byte(`data: {
		"responseId": "stream_fc_adv_1",
		"candidates": [{
			"groundingMetadata": {
				"webSearchQueries": ["test query"],
				"groundingChunks": [
					{"web": {"uri": "https://example.com/trailing", "title": "Trailing Source"}}
				],
				"groundingSupports": [
					{
						"segment": {
							"startIndex": 0,
							"endIndex": 8,
							"text": "Trailing",
							"partIndex": 3
						},
						"groundingChunkIndices": [0]
					}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 20, "totalTokenCount": 25}
	}`)

	var param any
	e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
	e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
	e4 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk4, &param)
	e5 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk5, &param)

	allEvents := append(append(append(append(e1, e2...), e3...), e4...), e5...)

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
	if len(outputs) == 0 {
		t.Fatalf("expected outputs in completed response, got none: %s", completedJSON.Raw)
	}

	var trailingMsg gjson.Result
	foundTrailing := false
	for _, out := range outputs {
		if out.Get("type").String() == "message" && out.Get("content.0.text").String() == "Trailing citation text." {
			trailingMsg = out
			foundTrailing = true
			break
		}
	}
	if !foundTrailing {
		t.Fatalf("trailing message not found in outputs: %s", completedJSON.Raw)
	}

	citations := trailingMsg.Get("content.0.annotations").Array()
	if len(citations) != 1 {
		t.Fatalf("expected 1 citation on trailing message with partIndex=3, got %d: %s", len(citations), trailingMsg.Raw)
	}
	if citations[0].Get("url").String() != "https://example.com/trailing" {
		t.Fatalf("expected citation url 'https://example.com/trailing', got %q", citations[0].Get("url").String())
	}
	if citations[0].Get("start_index").Int() != 0 || citations[0].Get("end_index").Int() != 8 {
		t.Fatalf("expected citation span [0, 8), got [%d, %d)", citations[0].Get("start_index").Int(), citations[0].Get("end_index").Int())
	}
}

func TestMergeGroundingMetadata(t *testing.T) {
	t.Run("merging disjoint chunks and supports with index remapping", func(t *testing.T) {
		gm1 := gjson.Parse(`{
			"webSearchQueries": ["query1"],
			"groundingChunks": [
				{"web": {"uri": "https://example.com/1", "title": "Title 1"}}
			],
			"groundingSupports": [
				{
					"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
					"groundingChunkIndices": [0]
				}
			]
		}`)
		gm2 := gjson.Parse(`{
			"webSearchQueries": ["query2"],
			"groundingChunks": [
				{"web": {"uri": "https://example.com/2", "title": "Title 2"}}
			],
			"groundingSupports": [
				{
					"segment": {"startIndex": 6, "endIndex": 10, "partIndex": 0},
					"groundingChunkIndices": [1]
				}
			]
		}`)

		merged := MergeGroundingMetadata(gm1, gm2)

		queries := merged.Get("webSearchQueries").Array()
		if len(queries) != 2 || queries[0].String() != "query1" || queries[1].String() != "query2" {
			t.Fatalf("expected 2 queries [query1, query2], got: %s", merged.Get("webSearchQueries").Raw)
		}

		chunks := merged.Get("groundingChunks").Array()
		if len(chunks) != 2 {
			t.Fatalf("expected 2 chunks, got %d: %s", len(chunks), merged.Raw)
		}
		if chunks[0].Get("web.uri").String() != "https://example.com/1" || chunks[1].Get("web.uri").String() != "https://example.com/2" {
			t.Fatalf("unexpected chunks: %s", merged.Get("groundingChunks").Raw)
		}

		supports := merged.Get("groundingSupports").Array()
		if len(supports) != 2 {
			t.Fatalf("expected 2 supports, got %d: %s", len(supports), merged.Raw)
		}
		if idx := supports[0].Get("groundingChunkIndices.0").Int(); idx != 0 {
			t.Fatalf("expected support 0 index 0, got %d", idx)
		}
		if idx := supports[1].Get("groundingChunkIndices.0").Int(); idx != 1 {
			t.Fatalf("expected support 1 remapped index 1, got %d", idx)
		}
	})

	t.Run("incremental supports without chunks referencing previously seen chunk", func(t *testing.T) {
		gm1 := gjson.Parse(`{
			"webSearchQueries": ["query1"],
			"groundingChunks": [
				{"web": {"uri": "https://example.com/1", "title": "Title 1"}}
			]
		}`)
		gm2 := gjson.Parse(`{
			"groundingSupports": [
				{
					"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
					"groundingChunkIndices": [0]
				}
			]
		}`)

		merged := MergeGroundingMetadata(gm1, gm2)
		chunks := merged.Get("groundingChunks").Array()
		if len(chunks) != 1 || chunks[0].Get("web.uri").String() != "https://example.com/1" {
			t.Fatalf("expected 1 chunk retained, got: %s", merged.Get("groundingChunks").Raw)
		}

		supports := merged.Get("groundingSupports").Array()
		if len(supports) != 1 || supports[0].Get("groundingChunkIndices.0").Int() != 0 {
			t.Fatalf("expected 1 support pointing to chunk 0, got: %s", merged.Get("groundingSupports").Raw)
		}
	})

	t.Run("overlapping chunks deduplication and remapping", func(t *testing.T) {
		gm1 := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/1", "title": ""}}
			]
		}`)
		gm2 := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/1", "title": "Title 1"}},
				{"web": {"uri": "https://example.com/2", "title": "Title 2"}}
			],
			"groundingSupports": [
				{
					"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
					"groundingChunkIndices": [0, 2]
				}
			]
		}`)

		merged := MergeGroundingMetadata(gm1, gm2)
		chunks := merged.Get("groundingChunks").Array()
		if len(chunks) != 2 {
			t.Fatalf("expected 2 chunks, got %d: %s", len(chunks), merged.Raw)
		}
		if chunks[0].Get("web.title").String() != "Title 1" {
			t.Fatalf("expected upgraded title 'Title 1', got %q", chunks[0].Get("web.title").String())
		}
		supports := merged.Get("groundingSupports").Array()
		if len(supports) != 1 {
			t.Fatalf("expected 1 support, got: %s", merged.Raw)
		}
		indices := supports[0].Get("groundingChunkIndices").Array()
		if len(indices) != 2 || indices[0].Int() != 0 || indices[1].Int() != 1 {
			t.Fatalf("expected indices [0, 1], got: %s", supports[0].Get("groundingChunkIndices").Raw)
		}
	})

	t.Run("deduplication of identical supports", func(t *testing.T) {
		gm1 := gjson.Parse(`{
			"groundingChunks": [{"web": {"uri": "https://example.com/1"}}],
			"groundingSupports": [
				{
					"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
					"groundingChunkIndices": [0]
				}
			]
		}`)
		gm2 := gjson.Parse(`{
			"groundingSupports": [
				{
					"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
					"groundingChunkIndices": [0]
				},
				{
					"segment": {"startIndex": 6, "endIndex": 10, "partIndex": 0},
					"groundingChunkIndices": [0]
				}
			]
		}`)

		merged := MergeGroundingMetadata(gm1, gm2)
		supports := merged.Get("groundingSupports").Array()
		if len(supports) != 2 {
			t.Fatalf("expected 2 unique supports, got %d: %s", len(supports), merged.Raw)
		}
	})

	t.Run("incremental grounding queries then supports then chunks preserves indices", func(t *testing.T) {
		gm1 := gjson.Parse(`{
			"webSearchQueries": ["query1"]
		}`)
		gm2 := gjson.Parse(`{
			"groundingSupports": [
				{
					"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
					"groundingChunkIndices": [0]
				}
			]
		}`)
		gm3 := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/1", "title": "Title 1"}}
			]
		}`)

		merged1 := MergeGroundingMetadata(gm1, gm2)
		supports1 := merged1.Get("groundingSupports").Array()
		if len(supports1) != 1 || supports1[0].Get("groundingChunkIndices.0").Int() != 0 {
			t.Fatalf("expected 1 support with index 0 preserved, got: %s", merged1.Get("groundingSupports").Raw)
		}

		merged2 := MergeGroundingMetadata(merged1, gm3)
		chunks2 := merged2.Get("groundingChunks").Array()
		if len(chunks2) != 1 || chunks2[0].Get("web.uri").String() != "https://example.com/1" {
			t.Fatalf("expected 1 chunk, got: %s", merged2.Get("groundingChunks").Raw)
		}
		supports2 := merged2.Get("groundingSupports").Array()
		if len(supports2) != 1 || supports2[0].Get("groundingChunkIndices.0").Int() != 0 {
			t.Fatalf("expected 1 support with index 0, got: %s", merged2.Get("groundingSupports").Raw)
		}

		citations := BuildResponsesURLCitations(merged2, "Hello world")
		if len(citations) != 1 {
			t.Fatalf("expected 1 citation, got %d", len(citations))
		}
		if gjson.GetBytes(citations[0], "url").String() != "https://example.com/1" {
			t.Fatalf("expected url https://example.com/1, got: %s", string(citations[0]))
		}
	})

	t.Run("supports arrive first with index 1 then identical duplicate URL chunks arrive and deduplicate", func(t *testing.T) {
		gm1 := gjson.Parse(`{
			"groundingSupports": [
				{
					"segment": {"startIndex": 0, "endIndex": 11, "partIndex": 0},
					"groundingChunkIndices": [1]
				}
			]
		}`)
		gm2 := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/dup", "title": "Dup 1"}},
				{"web": {"uri": "https://example.com/dup", "title": "Dup 2"}}
			]
		}`)

		merged := MergeGroundingMetadata(gm1, gm2)
		chunks := merged.Get("groundingChunks").Array()
		if len(chunks) != 1 {
			t.Fatalf("expected 1 deduplicated chunk, got %d: %s", len(chunks), merged.Get("groundingChunks").Raw)
		}
		if chunks[0].Get("web.uri").String() != "https://example.com/dup" {
			t.Fatalf("expected uri https://example.com/dup, got %q", chunks[0].Get("web.uri").String())
		}

		supports := merged.Get("groundingSupports").Array()
		if len(supports) != 1 {
			t.Fatalf("expected 1 support, got %d: %s", len(supports), merged.Get("groundingSupports").Raw)
		}
		indices := supports[0].Get("groundingChunkIndices").Array()
		if len(indices) != 1 || indices[0].Int() != 0 {
			t.Fatalf("expected remapped index [0], got: %s", supports[0].Get("groundingChunkIndices").Raw)
		}

		citations := BuildResponsesURLCitations(merged, "Hello world")
		if len(citations) != 1 {
			t.Fatalf("expected 1 citation, got %d", len(citations))
		}
		if gjson.GetBytes(citations[0], "url").String() != "https://example.com/dup" {
			t.Fatalf("expected citation url https://example.com/dup, got %s", citations[0])
		}
	})

	t.Run("queries in frame 1 then duplicate chunks [A, A, B] in frame 2 then supports [2] in frame 3", func(t *testing.T) {
		// Frame 1: queries only
		gm1 := gjson.Parse(`{
			"webSearchQueries": ["test query"]
		}`)

		// Frame 2: duplicate chunks [A, A, B] (indices 0, 1, 2)
		gm2 := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/A", "title": "Title A1"}},
				{"web": {"uri": "https://example.com/A", "title": "Title A2"}},
				{"web": {"uri": "https://example.com/B", "title": "Title B"}}
			]
		}`)

		merged1 := MergeGroundingMetadata(gm1, gm2)
		chunks1 := merged1.Get("groundingChunks").Array()
		if len(chunks1) != 2 {
			t.Fatalf("expected 2 deduplicated chunks [A, B], got %d: %s", len(chunks1), merged1.Get("groundingChunks").Raw)
		}
		if chunks1[0].Get("web.uri").String() != "https://example.com/A" || chunks1[1].Get("web.uri").String() != "https://example.com/B" {
			t.Fatalf("unexpected chunks order: %s", merged1.Get("groundingChunks").Raw)
		}

		// Frame 3: supports only with index [2], pointing to B in Gemini stream
		gm3 := gjson.Parse(`{
			"groundingSupports": [
				{
					"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
					"groundingChunkIndices": [2]
				},
				{
					"segment": {"startIndex": 6, "endIndex": 11, "partIndex": 0},
					"groundingChunkIndices": [1]
				}
			]
		}`)

		merged2 := MergeGroundingMetadata(merged1, gm3)
		supports := merged2.Get("groundingSupports").Array()
		if len(supports) != 2 {
			t.Fatalf("expected 2 supports, got %d: %s", len(supports), merged2.Get("groundingSupports").Raw)
		}

		// Support 0 had index [2], should be remapped to index 1 (chunk B)
		indices0 := supports[0].Get("groundingChunkIndices").Array()
		if len(indices0) != 1 || indices0[0].Int() != 1 {
			t.Fatalf("expected support 0 remapped to chunk index 1 (B), got: %s", supports[0].Get("groundingChunkIndices").Raw)
		}

		// Support 1 had index [1], should be remapped to index 0 (chunk A)
		indices1 := supports[1].Get("groundingChunkIndices").Array()
		if len(indices1) != 1 || indices1[0].Int() != 0 {
			t.Fatalf("expected support 1 remapped to chunk index 0 (A), got: %s", supports[1].Get("groundingChunkIndices").Raw)
		}

		citations := BuildResponsesURLCitations(merged2, "Alpha Beta.")
		if len(citations) != 2 {
			t.Fatalf("expected 2 citations, got %d", len(citations))
		}
		if gjson.GetBytes(citations[0], "url").String() != "https://example.com/B" {
			t.Fatalf("expected citation 0 url https://example.com/B, got %s", citations[0])
		}
		if gjson.GetBytes(citations[1], "url").String() != "https://example.com/A" {
			t.Fatalf("expected citation 1 url https://example.com/A, got %s", citations[1])
		}
	})

	t.Run("existing pending support resolves to cumulative raw index rather than subsequent frame local chunk index", func(t *testing.T) {
		// Frame 1: groundingChunks = [A], support with groundingChunkIndices = [1]
		gm1 := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/A", "title": "Chunk A"}}
			],
			"groundingSupports": [
				{
					"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
					"groundingChunkIndices": [1]
				}
			]
		}`)

		// Frame 2: groundingChunks = [B, C]
		gm2 := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/B", "title": "Chunk B"}},
				{"web": {"uri": "https://example.com/C", "title": "Chunk C"}}
			]
		}`)

		merged1 := MergeGroundingMetadata(gjson.Result{}, gm1)
		merged2 := MergeGroundingMetadata(merged1, gm2)

		chunks := merged2.Get("groundingChunks").Array()
		if len(chunks) != 3 {
			t.Fatalf("expected 3 chunks [A, B, C], got %d: %s", len(chunks), merged2.Get("groundingChunks").Raw)
		}
		if chunks[0].Get("web.uri").String() != "https://example.com/A" ||
			chunks[1].Get("web.uri").String() != "https://example.com/B" ||
			chunks[2].Get("web.uri").String() != "https://example.com/C" {
			t.Fatalf("unexpected chunks: %s", merged2.Get("groundingChunks").Raw)
		}

		supports := merged2.Get("groundingSupports").Array()
		if len(supports) != 1 {
			t.Fatalf("expected 1 support, got %d: %s", len(supports), merged2.Get("groundingSupports").Raw)
		}

		// Frame 1's existing support had cumulative index [1], must resolve to Chunk B (merged index 1), not Chunk C (merged index 2)
		indices0 := supports[0].Get("groundingChunkIndices").Array()
		if len(indices0) != 1 || indices0[0].Int() != 1 {
			t.Fatalf("expected existing support 0 to resolve to chunk index 1 (B), got %s", supports[0].Get("groundingChunkIndices").Raw)
		}

		citations := BuildResponsesURLCitations(merged2, "Alpha.")
		if len(citations) != 1 {
			t.Fatalf("expected 1 citation, got %d", len(citations))
		}
		if gjson.GetBytes(citations[0], "url").String() != "https://example.com/B" {
			t.Fatalf("expected citation url https://example.com/B, got %s", citations[0])
		}
	})

	t.Run("existing pending support and new frame support with same numeric index resolve correctly", func(t *testing.T) {
		// Frame 1: groundingChunks = [A], support with groundingChunkIndices = [1]
		gm1 := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/A", "title": "Chunk A"}}
			],
			"groundingSupports": [
				{
					"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
					"groundingChunkIndices": [1]
				}
			]
		}`)

		// Frame 2: groundingChunks = [B, C], and new support with cumulative index [1] (pointing to B)
		gm2 := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/B", "title": "Chunk B"}},
				{"web": {"uri": "https://example.com/C", "title": "Chunk C"}}
			],
			"groundingSupports": [
				{
					"segment": {"startIndex": 6, "endIndex": 11, "partIndex": 0},
					"groundingChunkIndices": [1]
				}
			]
		}`)

		merged1 := MergeGroundingMetadata(gjson.Result{}, gm1)
		merged2 := MergeGroundingMetadata(merged1, gm2)

		supports := merged2.Get("groundingSupports").Array()
		if len(supports) != 2 {
			t.Fatalf("expected 2 supports, got %d: %s", len(supports), merged2.Get("groundingSupports").Raw)
		}

		// Support 0 (existing from frame 1): cumulative raw index 1 -> Chunk B (index 1)
		indices0 := supports[0].Get("groundingChunkIndices").Array()
		if len(indices0) != 1 || indices0[0].Int() != 1 {
			t.Fatalf("expected existing support 0 to resolve to chunk index 1 (B), got %s", supports[0].Get("groundingChunkIndices").Raw)
		}

		// Support 1 (new from frame 2): cumulative index 1 -> Chunk B (index 1)
		indices1 := supports[1].Get("groundingChunkIndices").Array()
		if len(indices1) != 1 || indices1[0].Int() != 1 {
			t.Fatalf("expected new support 1 to resolve to chunk index 1 (B), got %s", supports[1].Get("groundingChunkIndices").Raw)
		}

		citations := BuildResponsesURLCitations(merged2, "Alpha Beta.")
		if len(citations) != 2 {
			t.Fatalf("expected 2 citations, got %d", len(citations))
		}
		if gjson.GetBytes(citations[0], "url").String() != "https://example.com/B" {
			t.Fatalf("expected citation 0 url https://example.com/B, got %s", citations[0])
		}
		if gjson.GetBytes(citations[1], "url").String() != "https://example.com/B" {
			t.Fatalf("expected citation 1 url https://example.com/B, got %s", citations[1])
		}
	})

	t.Run("deduplicated frame 1 chunks resolve frame 2 cumulative support index correctly", func(t *testing.T) {
		// Frame 1: groundingChunks = [A, A] (deduplicated to [A])
		gm1 := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/A", "title": "Chunk A"}},
				{"web": {"uri": "https://example.com/A", "title": "Chunk A"}}
			]
		}`)

		// Frame 2: groundingChunks = [B], with new support referencing groundingChunkIndices = [1] (pointing to A)
		gm2 := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/B", "title": "Chunk B"}}
			],
			"groundingSupports": [
				{
					"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
					"groundingChunkIndices": [1]
				}
			]
		}`)

		merged1 := MergeGroundingMetadata(gjson.Result{}, gm1)
		merged2 := MergeGroundingMetadata(merged1, gm2)

		chunks := merged2.Get("groundingChunks").Array()
		if len(chunks) != 2 {
			t.Fatalf("expected 2 chunks [A, B], got %d: %s", len(chunks), merged2.Get("groundingChunks").Raw)
		}
		if chunks[0].Get("web.uri").String() != "https://example.com/A" ||
			chunks[1].Get("web.uri").String() != "https://example.com/B" {
			t.Fatalf("unexpected chunks: %s", merged2.Get("groundingChunks").Raw)
		}

		supports := merged2.Get("groundingSupports").Array()
		if len(supports) != 1 {
			t.Fatalf("expected 1 support, got %d: %s", len(supports), merged2.Get("groundingSupports").Raw)
		}

		// Support referenced index [1], which in candidate stream was raw chunk 1 (Chunk A).
		// Must resolve to merged index 0 (Chunk A), NOT merged index 1 (Chunk B).
		indices0 := supports[0].Get("groundingChunkIndices").Array()
		if len(indices0) != 1 || indices0[0].Int() != 0 {
			t.Fatalf("expected support to resolve to chunk index 0 (A), got %s", supports[0].Get("groundingChunkIndices").Raw)
		}

		citations := BuildResponsesURLCitations(merged2, "Alpha.")
		if len(citations) != 1 {
			t.Fatalf("expected 1 citation, got %d", len(citations))
		}
		if gjson.GetBytes(citations[0], "url").String() != "https://example.com/A" {
			t.Fatalf("expected citation url https://example.com/A, got %s", citations[0])
		}
	})

	t.Run("deduplicated frame 1 chunks [A, A] resolve frame 2 chunks [B, C] cumulative support [1] to A not C", func(t *testing.T) {
		// Frame 1: groundingChunks = [A, A] (deduplicated to [A], prevRawCount = 2, cumulativeRemap has {0: 0, 1: 0})
		gm1 := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/A", "title": "Chunk A"}},
				{"web": {"uri": "https://example.com/A", "title": "Chunk A"}}
			]
		}`)

		// Frame 2: groundingChunks = [B, C], and support with groundingChunkIndices = [1] (referencing cumulative raw chunk 1 = A)
		gm2 := gjson.Parse(`{
			"groundingChunks": [
				{"web": {"uri": "https://example.com/B", "title": "Chunk B"}},
				{"web": {"uri": "https://example.com/C", "title": "Chunk C"}}
			],
			"groundingSupports": [
				{
					"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
					"groundingChunkIndices": [1]
				}
			]
		}`)

		merged1 := MergeGroundingMetadata(gjson.Result{}, gm1)
		merged2 := MergeGroundingMetadata(merged1, gm2)

		chunks := merged2.Get("groundingChunks").Array()
		if len(chunks) != 3 {
			t.Fatalf("expected 3 chunks [A, B, C], got %d: %s", len(chunks), merged2.Get("groundingChunks").Raw)
		}
		if chunks[0].Get("web.uri").String() != "https://example.com/A" ||
			chunks[1].Get("web.uri").String() != "https://example.com/B" ||
			chunks[2].Get("web.uri").String() != "https://example.com/C" {
			t.Fatalf("unexpected chunks: %s", merged2.Get("groundingChunks").Raw)
		}

		supports := merged2.Get("groundingSupports").Array()
		if len(supports) != 1 {
			t.Fatalf("expected 1 support, got %d: %s", len(supports), merged2.Get("groundingSupports").Raw)
		}

		// Support referenced cumulative index [1], which in candidate stream was raw chunk 1 (Chunk A).
		// Must resolve to merged index 0 (Chunk A), NOT merged index 2 (Chunk C).
		indices0 := supports[0].Get("groundingChunkIndices").Array()
		if len(indices0) != 1 || indices0[0].Int() != 0 {
			t.Fatalf("expected support to resolve to chunk index 0 (A), got %s", supports[0].Get("groundingChunkIndices").Raw)
		}

		citations := BuildResponsesURLCitations(merged2, "Alpha.")
		if len(citations) != 1 {
			t.Fatalf("expected 1 citation, got %d", len(citations))
		}
		if gjson.GetBytes(citations[0], "url").String() != "https://example.com/A" {
			t.Fatalf("expected citation url https://example.com/A, got %s", citations[0])
		}
	})
}

func TestMergeCitationAnnotations(t *testing.T) {
	c1 := []byte(`{"type":"url_citation","url":"https://example.com/1","title":"","start_index":0,"end_index":5}`)
	c2 := []byte(`{"type":"url_citation","url":"https://example.com/1","title":"Title 1","start_index":0,"end_index":5}`)
	c3 := []byte(`{"type":"url_citation","url":"https://example.com/2","title":"Title 2","start_index":6,"end_index":10}`)

	merged := MergeCitationAnnotations(nil, [][]byte{c1})
	if len(merged) != 1 {
		t.Fatalf("expected 1 citation, got %d", len(merged))
	}

	merged = MergeCitationAnnotations([][]byte{c1}, [][]byte{c2, c3})
	if len(merged) != 2 {
		t.Fatalf("expected 2 citations, got %d", len(merged))
	}
	if title := gjson.GetBytes(merged[0], "title").String(); title != "Title 1" {
		t.Fatalf("expected upgraded title 'Title 1', got %q", title)
	}
	if url := gjson.GetBytes(merged[1], "url").String(); url != "https://example.com/2" {
		t.Fatalf("expected url 'https://example.com/2', got %q", url)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_MultipleGroundingUpdates(t *testing.T) {
	modelID := "gemini-multi-grounding-stream"
	registerTestWebSearchModel(t, "client-multi-gm", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-multi-grounding-stream",
		"input": "test search",
		"tools": [{"type": "web_search"}]
	}`)

	chunk1 := []byte(`data: {
		"responseId": "stream_multi_gm_1",
		"candidates": [{
			"content": {
				"parts": [{"text": "Alpha "}],
				"role": "model"
			},
			"groundingMetadata": {
				"webSearchQueries": ["query alpha"],
				"groundingChunks": [
					{"web": {"uri": "https://example.com/alpha", "title": "Alpha"}}
				],
				"groundingSupports": [
					{
						"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
						"groundingChunkIndices": [0]
					}
				]
			}
		}]
	}`)

	chunk2 := []byte(`data: {
		"responseId": "stream_multi_gm_1",
		"candidates": [{
			"content": {
				"parts": [{"text": "Beta."}],
				"role": "model"
			},
			"groundingMetadata": {
				"webSearchQueries": ["query beta"],
				"groundingChunks": [
					{"web": {"uri": "https://example.com/beta", "title": "Beta"}}
				],
				"groundingSupports": [
					{
						"segment": {"startIndex": 6, "endIndex": 10, "partIndex": 0},
						"groundingChunkIndices": [1]
					}
				]
			}
		}]
	}`)

	chunk3 := []byte(`data: {
		"responseId": "stream_multi_gm_1",
		"candidates": [{
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
	}`)

	var param any
	e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
	e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)

	_, e1ByType := collectResponsesStreamEvents(e1)
	if len(e1ByType["response.web_search_call.completed"]) > 0 {
		t.Fatalf("did not expect web_search_call.completed on first incremental grounding frame, events=%v", e1ByType["response.web_search_call.completed"])
	}
	if done := findWebSearchCallDone(e1ByType["response.output_item.done"]); done.Exists() {
		t.Fatalf("did not expect web_search_call output_item.done on first incremental grounding frame, got: %s", done.Raw)
	}

	allEvents := append(append(e1, e2...), e3...)
	_, byType := collectResponsesStreamEvents(allEvents)
	if len(byType["response.completed"]) == 0 {
		t.Fatal("expected response.completed event")
	}
	completedJSON := byType["response.completed"][0]

	outputs := completedJSON.Get("response.output").Array()
	var msgItem gjson.Result
	var wsItem gjson.Result
	for _, out := range outputs {
		if out.Get("type").String() == "message" {
			msgItem = out
		} else if out.Get("type").String() == "web_search_call" {
			wsItem = out
		}
	}

	if !msgItem.Exists() {
		t.Fatalf("expected message output item, got: %s", completedJSON.Raw)
	}
	if gotText := msgItem.Get("content.0.text").String(); gotText != "Alpha Beta." {
		t.Fatalf("expected message text 'Alpha Beta.', got %q", gotText)
	}

	citations := msgItem.Get("content.0.annotations").Array()
	if len(citations) != 2 {
		t.Fatalf("expected 2 citations from merged grounding metadata, got %d: %s", len(citations), msgItem.Raw)
	}
	if citations[0].Get("url").String() != "https://example.com/alpha" || citations[0].Get("start_index").Int() != 0 || citations[0].Get("end_index").Int() != 5 {
		t.Fatalf("unexpected citation 0: %s", citations[0].Raw)
	}
	if citations[1].Get("url").String() != "https://example.com/beta" || citations[1].Get("start_index").Int() != 6 || citations[1].Get("end_index").Int() != 10 {
		t.Fatalf("unexpected citation 1: %s", citations[1].Raw)
	}

	if !wsItem.Exists() {
		t.Fatalf("expected web_search_call item, got: %s", completedJSON.Raw)
	}
	sources := wsItem.Get("action.sources").Array()
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources in web_search_call, got %d: %s", len(sources), wsItem.Raw)
	}

	wsDone := findWebSearchCallDone(byType["response.output_item.done"])
	if !wsDone.Exists() {
		t.Fatal("expected web_search_call output_item.done")
	}
	doneSources := wsDone.Get("item.action.sources").Array()
	if len(doneSources) != len(sources) {
		t.Fatalf("output_item.done sources=%d, response.completed sources=%d; want matching full sources", len(doneSources), len(sources))
	}
	for i := range sources {
		if doneSources[i].Get("url").String() != sources[i].Get("url").String() {
			t.Fatalf("output_item.done source[%d]=%q, completed source[%d]=%q", i, doneSources[i].Get("url").String(), i, sources[i].Get("url").String())
		}
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_IncrementalSupportsWithoutChunks(t *testing.T) {
	modelID := "gemini-incr-supports-stream"
	registerTestWebSearchModel(t, "client-incr-sup", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-incr-supports-stream",
		"input": "test search",
		"tools": [{"type": "web_search"}]
	}`)

	chunk1 := []byte(`data: {
		"responseId": "stream_incr_sup_1",
		"candidates": [{
			"content": {
				"parts": [{"text": "Hello world."}],
				"role": "model"
			},
			"groundingMetadata": {
				"webSearchQueries": ["query hw"],
				"groundingChunks": [
					{"web": {"uri": "https://example.com/hw", "title": "Hello World"}}
				]
			}
		}]
	}`)

	chunk2 := []byte(`data: {
		"responseId": "stream_incr_sup_1",
		"candidates": [{
			"groundingMetadata": {
				"groundingSupports": [
					{
						"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
						"groundingChunkIndices": [0]
					}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
	}`)

	var param any
	e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)

	allEvents := append(e1, e2...)

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
	var msgItem gjson.Result
	for _, out := range outputs {
		if out.Get("type").String() == "message" {
			msgItem = out
			break
		}
	}
	if !msgItem.Exists() {
		t.Fatalf("expected message output item, got: %s", completedJSON.Raw)
	}

	citations := msgItem.Get("content.0.annotations").Array()
	if len(citations) != 1 {
		t.Fatalf("expected 1 citation attached from incremental supports, got %d: %s", len(citations), msgItem.Raw)
	}
	if citations[0].Get("url").String() != "https://example.com/hw" || citations[0].Get("start_index").Int() != 0 || citations[0].Get("end_index").Int() != 5 {
		t.Fatalf("unexpected citation: %s", citations[0].Raw)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_IncrementalGroundingQueriesSupportsChunks(t *testing.T) {
	modelID := "gemini-incr-q-s-c-stream"
	registerTestWebSearchModel(t, "client-incr-qsc", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-incr-q-s-c-stream",
		"input": "test search",
		"tools": [{"type": "web_search"}]
	}`)

	// Frame 1: queries only
	chunk1 := []byte(`data: {
		"responseId": "stream_incr_qsc_1",
		"candidates": [{
			"content": {
				"parts": [{"text": "Hello world."}],
				"role": "model"
			},
			"groundingMetadata": {
				"webSearchQueries": ["query hw"]
			}
		}]
	}`)

	// Frame 2: supports only (chunks not yet present)
	chunk2 := []byte(`data: {
		"responseId": "stream_incr_qsc_1",
		"candidates": [{
			"groundingMetadata": {
				"groundingSupports": [
					{
						"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
						"groundingChunkIndices": [0]
					}
				]
			}
		}]
	}`)

	// Frame 3: chunks arrive
	chunk3 := []byte(`data: {
		"responseId": "stream_incr_qsc_1",
		"candidates": [{
			"groundingMetadata": {
				"groundingChunks": [
					{"web": {"uri": "https://example.com/hw", "title": "Hello World"}}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
	}`)

	var param any
	e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
	e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)

	_, e1ByType := collectResponsesStreamEvents(e1)
	if len(e1ByType["response.output_item.added"]) == 0 {
		t.Fatal("expected web_search_call output_item.added when queries first arrive")
	}
	if e1ByType["response.output_item.added"][0].Get("item.status").String() != "in_progress" {
		t.Fatalf("expected in_progress web_search_call on queries-only frame, got: %s", e1ByType["response.output_item.added"][0].Raw)
	}
	if len(e1ByType["response.web_search_call.completed"]) > 0 {
		t.Fatal("did not expect web_search_call.completed on queries-only frame")
	}
	if done := findWebSearchCallDone(e1ByType["response.output_item.done"]); done.Exists() {
		t.Fatalf("did not expect web_search_call output_item.done on queries-only frame, got: %s", done.Raw)
	}

	allEvents := append(append(e1, e2...), e3...)
	_, byType := collectResponsesStreamEvents(allEvents)
	if len(byType["response.completed"]) == 0 {
		t.Fatal("expected response.completed event")
	}
	completedJSON := byType["response.completed"][0]

	outputs := completedJSON.Get("response.output").Array()
	var msgItem gjson.Result
	var wsItem gjson.Result
	for _, out := range outputs {
		if out.Get("type").String() == "message" {
			msgItem = out
		} else if out.Get("type").String() == "web_search_call" {
			wsItem = out
		}
	}
	if !msgItem.Exists() {
		t.Fatalf("expected message output item, got: %s", completedJSON.Raw)
	}

	citations := msgItem.Get("content.0.annotations").Array()
	if len(citations) != 1 {
		t.Fatalf("expected 1 citation from queries -> supports -> chunks sequence, got %d: %s", len(citations), msgItem.Raw)
	}
	if citations[0].Get("url").String() != "https://example.com/hw" || citations[0].Get("start_index").Int() != 0 || citations[0].Get("end_index").Int() != 5 {
		t.Fatalf("unexpected citation: %s", citations[0].Raw)
	}

	if !wsItem.Exists() {
		t.Fatalf("expected web_search_call item, got: %s", completedJSON.Raw)
	}
	sources := wsItem.Get("action.sources").Array()
	if len(sources) != 1 || sources[0].Get("url").String() != "https://example.com/hw" {
		t.Fatalf("expected source in web_search_call, got: %s", wsItem.Raw)
	}

	wsDone := findWebSearchCallDone(byType["response.output_item.done"])
	if !wsDone.Exists() {
		t.Fatal("expected web_search_call output_item.done")
	}
	doneSources := wsDone.Get("item.action.sources").Array()
	if len(doneSources) != len(sources) {
		t.Fatalf("output_item.done sources=%d, response.completed sources=%d; want matching full sources", len(doneSources), len(sources))
	}
	for i := range sources {
		if doneSources[i].Get("url").String() != sources[i].Get("url").String() {
			t.Fatalf("output_item.done source[%d]=%q, completed source[%d]=%q", i, doneSources[i].Get("url").String(), i, sources[i].Get("url").String())
		}
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_LateSourcesAfterInterleavedFinalization(t *testing.T) {
	modelID := "gemini-late-sources-after-fc"
	registerTestWebSearchModel(t, "client-late-src-fc", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-late-sources-after-fc",
		"input": "test search",
		"tools": [{"type": "web_search"}, {"type": "function", "name": "lookup"}]
	}`)

	// Frame 1: queries and an initial source arrive with text.
	chunk1 := []byte(`data: {
		"responseId": "stream_late_src_fc_1",
		"candidates": [{
			"content": {
				"parts": [{"text": "Alpha "}],
				"role": "model"
			},
			"groundingMetadata": {
				"webSearchQueries": ["query alpha"],
				"groundingChunks": [
					{"web": {"uri": "https://example.com/alpha", "title": "Alpha"}}
				]
			}
		}]
	}`)

	// Frame 2: a function call forces early web_search_call finalization.
	chunk2 := []byte(`data: {
		"responseId": "stream_late_src_fc_1",
		"candidates": [{
			"content": {
				"parts": [{"functionCall": {"name": "lookup", "args": {}}}],
				"role": "model"
			}
		}]
	}`)

	// Frame 3: additional sources and queries arrive after the search item was emitted.
	chunk3 := []byte(`data: {
		"responseId": "stream_late_src_fc_1",
		"candidates": [{
			"groundingMetadata": {
				"webSearchQueries": ["query beta"],
				"groundingChunks": [
					{"web": {"uri": "https://example.com/beta", "title": "Beta"}}
				]
			}
		}]
	}`)

	chunk4 := []byte(`data: {
		"responseId": "stream_late_src_fc_1",
		"candidates": [{
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
	}`)

	var param any
	e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
	e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
	e4 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk4, &param)

	_, e1ByType := collectResponsesStreamEvents(e1)
	if len(e1ByType["response.web_search_call.completed"]) > 0 {
		t.Fatalf("did not expect web_search_call.completed on queries/sources frame, events=%v", e1ByType["response.web_search_call.completed"])
	}

	_, e2ByType := collectResponsesStreamEvents(e2)
	if len(e2ByType["response.web_search_call.completed"]) == 0 {
		t.Fatal("expected function call to finalize web_search_call")
	}
	earlyDone := findWebSearchCallDone(e2ByType["response.output_item.done"])
	if !earlyDone.Exists() {
		t.Fatal("expected web_search_call output_item.done when function call arrives")
	}
	earlySources := earlyDone.Get("item.action.sources").Array()
	if len(earlySources) != 1 || earlySources[0].Get("url").String() != "https://example.com/alpha" {
		t.Fatalf("expected early-finalized search to contain only the first source, got: %s", earlyDone.Raw)
	}

	allEvents := append(append(append(e1, e2...), e3...), e4...)
	_, byType := collectResponsesStreamEvents(allEvents)
	if len(byType["response.completed"]) == 0 {
		t.Fatal("expected response.completed event")
	}
	completedJSON := byType["response.completed"][0]

	var wsItem gjson.Result
	for _, out := range completedJSON.Get("response.output").Array() {
		if out.Get("type").String() == "web_search_call" {
			wsItem = out
			break
		}
	}
	if !wsItem.Exists() {
		t.Fatalf("expected web_search_call item, got: %s", completedJSON.Raw)
	}

	sources := wsItem.Get("action.sources").Array()
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources in completed web_search_call after late grounding, got %d: %s", len(sources), wsItem.Raw)
	}
	gotURLs := map[string]bool{}
	for _, src := range sources {
		gotURLs[src.Get("url").String()] = true
	}
	if !gotURLs["https://example.com/alpha"] || !gotURLs["https://example.com/beta"] {
		t.Fatalf("expected both alpha and beta sources in completed output, got: %s", wsItem.Raw)
	}

	queries := wsItem.Get("action.queries").Array()
	gotQueries := map[string]bool{}
	for _, q := range queries {
		gotQueries[q.String()] = true
	}
	if !gotQueries["query alpha"] || !gotQueries["query beta"] {
		t.Fatalf("expected both alpha and beta queries in completed output, got: %s", wsItem.Raw)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_CitationsSpanAcrossMessages(t *testing.T) {
	modelID := "gemini-stream-citation-span"
	registerTestWebSearchModel(t, "client-citation-span", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-stream-citation-span",
		"input": "test query",
		"tools": [{"type": "web_search"}]
	}`)

	// Chunk 1: message 0 part
	chunk1 := []byte(`data: {
		"responseId": "stream_span_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Hello ", "partIndex": 0}]
			}
		}]
	}`)

	// Chunk 2: intervening function call forces message 0 to close
	chunk2 := []byte(`data: {
		"responseId": "stream_span_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"functionCall": {"name": "search", "args": {}}, "partIndex": 1}]
			}
		}]
	}`)

	// Chunk 3: message 1 continues part 0
	chunk3 := []byte(`data: {
		"responseId": "stream_span_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "world", "partIndex": 0}]
			}
		}]
	}`)

	// Chunk 4: grounding metadata covering partIndex 0 [0, 11)
	chunk4 := []byte(`data: {
		"responseId": "stream_span_1",
		"candidates": [{
			"groundingMetadata": {
				"webSearchQueries": ["span query"],
				"groundingChunks": [{"web": {"uri": "https://example.com/span", "title": "Span"}}],
				"groundingSupports": [{
					"groundingChunkIndices": [0],
					"segment": {
						"partIndex": 0,
						"startIndex": 0,
						"endIndex": 11,
						"text": "Hello world"
					}
				}]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
	}`)

	var param any
	e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
	e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
	e4 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk4, &param)

	allEvents := append(append(append(e1, e2...), e3...), e4...)

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
	var messages []gjson.Result
	for _, out := range outputs {
		if out.Get("type").String() == "message" {
			messages = append(messages, out)
		}
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d: %s", len(messages), completedJSON.Raw)
	}

	c0 := messages[0].Get("content.0.annotations").Array()
	if len(c0) != 1 {
		t.Fatalf("expected 1 citation in message 0, got %d: %s", len(c0), messages[0].Raw)
	}
	if c0[0].Get("start_index").Int() != 0 || c0[0].Get("end_index").Int() != 6 {
		t.Fatalf("expected [0, 6) in message 0, got [%d, %d)", c0[0].Get("start_index").Int(), c0[0].Get("end_index").Int())
	}
	if c0[0].Get("url").String() != "https://example.com/span" {
		t.Fatalf("expected url https://example.com/span, got %q", c0[0].Get("url").String())
	}

	c1 := messages[1].Get("content.0.annotations").Array()
	if len(c1) != 1 {
		t.Fatalf("expected 1 citation in message 1, got %d: %s", len(c1), messages[1].Raw)
	}
	if c1[0].Get("start_index").Int() != 0 || c1[0].Get("end_index").Int() != 5 {
		t.Fatalf("expected [0, 5) in message 1, got [%d, %d)", c1[0].Get("start_index").Int(), c1[0].Get("end_index").Int())
	}
	if c1[0].Get("url").String() != "https://example.com/span" {
		t.Fatalf("expected url https://example.com/span, got %q", c1[0].Get("url").String())
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_SupportsArriveFirstThenDuplicateChunksDeduplicate(t *testing.T) {
	modelID := "gemini-stream-dup-chunks-dedup"
	registerTestWebSearchModel(t, "client-dup-chunks", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-stream-dup-chunks-dedup",
		"input": "test dup search",
		"tools": [{"type": "web_search"}]
	}`)

	// Frame 1: text content
	chunk1 := []byte(`data: {
		"responseId": "stream_dup_dedup_1",
		"candidates": [{
			"content": {
				"parts": [{"text": "Hello world."}],
				"role": "model"
			}
		}]
	}`)

	// Frame 2: supports arrive referencing index [1] before chunks
	chunk2 := []byte(`data: {
		"responseId": "stream_dup_dedup_1",
		"candidates": [{
			"groundingMetadata": {
				"groundingSupports": [
					{
						"segment": {"startIndex": 0, "endIndex": 11, "partIndex": 0},
						"groundingChunkIndices": [1]
					}
				]
			}
		}]
	}`)

	// Frame 3: duplicate URL chunks arrive and get deduplicated to index 0
	chunk3 := []byte(`data: {
		"responseId": "stream_dup_dedup_1",
		"candidates": [{
			"groundingMetadata": {
				"groundingChunks": [
					{"web": {"uri": "https://example.com/dup", "title": "Dup 1"}},
					{"web": {"uri": "https://example.com/dup", "title": "Dup 2"}}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
	}`)

	var param any
	e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
	e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)

	allEvents := append(append(e1, e2...), e3...)

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
	var msgItem gjson.Result
	for _, out := range outputs {
		if out.Get("type").String() == "message" {
			msgItem = out
			break
		}
	}
	if !msgItem.Exists() {
		t.Fatalf("expected message output item, got: %s", completedJSON.Raw)
	}

	citations := msgItem.Get("content.0.annotations").Array()
	if len(citations) != 1 {
		t.Fatalf("expected 1 citation from remapped support, got %d: %s", len(citations), msgItem.Raw)
	}
	if citations[0].Get("url").String() != "https://example.com/dup" || citations[0].Get("start_index").Int() != 0 || citations[0].Get("end_index").Int() != 11 {
		t.Fatalf("unexpected citation: %s", citations[0].Raw)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_SignatureBoundaryContinuationTextChunks(t *testing.T) {
	modelID := "gemini-sig-boundary-continuation"
	registerTestWebSearchModel(t, "client-sig-boundary", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-sig-boundary-continuation",
		"input": "test query"
	}`)

	// Frame 1: signed text chunk A (partIndex=0)
	chunk1 := []byte(`data: {
		"responseId": "stream_sbc_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Hello ", "thoughtSignature": "` + testResponsesGeminiThoughtSignature + `", "partIndex": 0}]
			}
		}]
	}`)

	// Frame 2: unsigned text chunk B (partIndex=1). Pending signature logic finalizes message 0.
	chunk2 := []byte(`data: {
		"responseId": "stream_sbc_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "beautiful ", "partIndex": 1}]
			}
		}]
	}`)

	// Frame 3: continuation text chunk C (without explicit partIndex)
	chunk3 := []byte(`data: {
		"responseId": "stream_sbc_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "world"}]
			}
		}]
	}`)

	// Frame 4: grounding metadata referencing partIndex 1 across both B and C
	chunk4 := []byte(`data: {
		"responseId": "stream_sbc_1",
		"candidates": [{
			"groundingMetadata": {
				"webSearchQueries": ["world query"],
				"groundingChunks": [
					{"web": {"uri": "https://example.com/world", "title": "World"}}
				],
				"groundingSupports": [
					{
						"groundingChunkIndices": [0],
						"segment": {
							"partIndex": 1,
							"startIndex": 0,
							"endIndex": 15,
							"text": "beautiful world"
						}
					}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
	}`)

	var param any
	e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
	e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
	e4 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk4, &param)

	allEvents := append(append(append(e1, e2...), e3...), e4...)

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
	var messages []gjson.Result
	for _, out := range outputs {
		if out.Get("type").String() == "message" {
			messages = append(messages, out)
		}
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d: %s", len(messages), completedJSON.Raw)
	}

	if got := messages[0].Get("content.0.text").String(); got != "Hello " {
		t.Fatalf("expected message 0 text 'Hello ', got %q", got)
	}
	if got := messages[1].Get("content.0.text").String(); got != "beautiful world" {
		t.Fatalf("expected message 1 text 'beautiful world', got %q", got)
	}

	citations := messages[1].Get("content.0.annotations").Array()
	if len(citations) != 1 {
		t.Fatalf("expected 1 citation in message 1, got %d: %s", len(citations), messages[1].Raw)
	}
	if citations[0].Get("url").String() != "https://example.com/world" {
		t.Fatalf("expected url https://example.com/world, got %q", citations[0].Get("url").String())
	}
	if citations[0].Get("start_index").Int() != 0 || citations[0].Get("end_index").Int() != 15 {
		t.Fatalf("expected span [0, 15), got [%d, %d)", citations[0].Get("start_index").Int(), citations[0].Get("end_index").Int())
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_Frame2DuplicateChunksThenFrame3Supports(t *testing.T) {
	modelID := "gemini-stream-frame2-dup-chunks-frame3-sup"
	registerTestWebSearchModel(t, "client-f2-f3", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-stream-frame2-dup-chunks-frame3-sup",
		"input": "test dup search separate frames",
		"tools": [{"type": "web_search"}]
	}`)

	// Frame 1: text content + webSearchQueries
	chunk1 := []byte(`data: {
		"responseId": "stream_f2_f3_1",
		"candidates": [{
			"content": {
				"parts": [{"text": "Alpha Beta."}],
				"role": "model"
			},
			"groundingMetadata": {
				"webSearchQueries": ["test query"]
			}
		}]
	}`)

	// Frame 2: duplicate chunks [A, A, B] arrive without supports
	chunk2 := []byte(`data: {
		"responseId": "stream_f2_f3_1",
		"candidates": [{
			"groundingMetadata": {
				"groundingChunks": [
					{"web": {"uri": "https://example.com/A", "title": "Title A1"}},
					{"web": {"uri": "https://example.com/A", "title": "Title A2"}},
					{"web": {"uri": "https://example.com/B", "title": "Title B"}}
				]
			}
		}]
	}`)

	// Frame 3: supports arrive with index [2] (referencing B) without chunks
	chunk3 := []byte(`data: {
		"responseId": "stream_f2_f3_1",
		"candidates": [{
			"groundingMetadata": {
				"groundingSupports": [
					{
						"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
						"groundingChunkIndices": [2]
					}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
	}`)

	var param any
	e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
	e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)

	allEvents := append(append(e1, e2...), e3...)

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
	var msgItem gjson.Result
	for _, out := range outputs {
		if out.Get("type").String() == "message" {
			msgItem = out
			break
		}
	}
	if !msgItem.Exists() {
		t.Fatalf("expected message output item, got: %s", completedJSON.Raw)
	}

	citations := msgItem.Get("content.0.annotations").Array()
	if len(citations) != 1 {
		t.Fatalf("expected 1 citation from remapped support, got %d: %s", len(citations), msgItem.Raw)
	}
	if citations[0].Get("url").String() != "https://example.com/B" {
		t.Fatalf("expected citation url https://example.com/B, got %q", citations[0].Get("url").String())
	}
	if citations[0].Get("start_index").Int() != 0 || citations[0].Get("end_index").Int() != 5 {
		t.Fatalf("unexpected citation range: %s", citations[0].Raw)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_PendingSupportResolvesToCumulativeRawChunk(t *testing.T) {
	modelID := "gemini-stream-pending-support-raw-chunk"
	registerTestWebSearchModel(t, "client-pending-sup", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-stream-pending-support-raw-chunk",
		"input": "test pending support cumulative resolution",
		"tools": [{"type": "web_search"}]
	}`)

	// Frame 1: text + groundingChunks=[A] + support pointing to upcoming chunk [1]
	chunk1 := []byte(`data: {
		"responseId": "stream_pend_1",
		"candidates": [{
			"content": {
				"parts": [{"text": "Alpha Beta."}],
				"role": "model"
			},
			"groundingMetadata": {
				"webSearchQueries": ["test query"],
				"groundingChunks": [
					{"web": {"uri": "https://example.com/A", "title": "Chunk A"}}
				],
				"groundingSupports": [
					{
						"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
						"groundingChunkIndices": [1]
					}
				]
			}
		}]
	}`)

	// Frame 2: subsequent frame brings chunks [B, C]
	chunk2 := []byte(`data: {
		"responseId": "stream_pend_1",
		"candidates": [{
			"groundingMetadata": {
				"groundingChunks": [
					{"web": {"uri": "https://example.com/B", "title": "Chunk B"}},
					{"web": {"uri": "https://example.com/C", "title": "Chunk C"}}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
	}`)

	var param any
	e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)

	allEvents := append(e1, e2...)

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
	var msgItem gjson.Result
	for _, out := range outputs {
		if out.Get("type").String() == "message" {
			msgItem = out
			break
		}
	}
	if !msgItem.Exists() {
		t.Fatalf("expected message output item, got: %s", completedJSON.Raw)
	}

	citations := msgItem.Get("content.0.annotations").Array()
	if len(citations) != 1 {
		t.Fatalf("expected 1 citation from remapped support, got %d: %s", len(citations), msgItem.Raw)
	}
	// Must resolve to Chunk B (raw chunk 1 in cumulative stream), NOT Chunk C (local chunk 1 in frame 2)
	if citations[0].Get("url").String() != "https://example.com/B" {
		t.Fatalf("expected citation url https://example.com/B, got %q", citations[0].Get("url").String())
	}
	if citations[0].Get("start_index").Int() != 0 || citations[0].Get("end_index").Int() != 5 {
		t.Fatalf("unexpected citation range: %s", citations[0].Raw)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_DeduplicatedChunksResolveFrame2CumulativeSupport(t *testing.T) {
	modelID := "gemini-stream-dedup-frame2-cumulative-support"
	registerTestWebSearchModel(t, "client-dedup-f2-sup", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-stream-dedup-frame2-cumulative-support",
		"input": "test dedup cumulative resolution",
		"tools": [{"type": "web_search"}]
	}`)

	// Frame 1: text + groundingChunks=[A, A] (deduplicated to [A])
	chunk1 := []byte(`data: {
		"responseId": "stream_dedup_f2_1",
		"candidates": [{
			"content": {
				"parts": [{"text": "Alpha."}],
				"role": "model"
			},
			"groundingMetadata": {
				"webSearchQueries": ["test query"],
				"groundingChunks": [
					{"web": {"uri": "https://example.com/A", "title": "Chunk A"}},
					{"web": {"uri": "https://example.com/A", "title": "Chunk A"}}
				]
			}
		}]
	}`)

	// Frame 2: subsequent frame brings chunks [B] and new support referencing raw chunk [1]
	chunk2 := []byte(`data: {
		"responseId": "stream_dedup_f2_1",
		"candidates": [{
			"groundingMetadata": {
				"groundingChunks": [
					{"web": {"uri": "https://example.com/B", "title": "Chunk B"}}
				],
				"groundingSupports": [
					{
						"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
						"groundingChunkIndices": [1]
					}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
	}`)

	var param any
	e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)

	allEvents := append(e1, e2...)

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
	var msgItem gjson.Result
	for _, out := range outputs {
		if out.Get("type").String() == "message" {
			msgItem = out
			break
		}
	}
	if !msgItem.Exists() {
		t.Fatalf("expected message output item, got: %s", completedJSON.Raw)
	}

	citations := msgItem.Get("content.0.annotations").Array()
	if len(citations) != 1 {
		t.Fatalf("expected 1 citation, got %d: %s", len(citations), msgItem.Raw)
	}
	// Must resolve to Chunk A (raw chunk 1 in cumulative stream), NOT Chunk B (local chunk 0 in frame 2)
	if citations[0].Get("url").String() != "https://example.com/A" {
		t.Fatalf("expected citation url https://example.com/A, got %q", citations[0].Get("url").String())
	}
	if citations[0].Get("start_index").Int() != 0 || citations[0].Get("end_index").Int() != 5 {
		t.Fatalf("unexpected citation range: %s", citations[0].Raw)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_DeduplicatedFrame1ChunksFrame2ChunksAndSupportResolvesToFrame1(t *testing.T) {
	modelID := "gemini-stream-dedup-f1-f2-support-resolves-f1"
	registerTestWebSearchModel(t, "client-dedup-f1-f2-sup", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-stream-dedup-f1-f2-support-resolves-f1",
		"input": "test dedup cumulative resolution with multiple frame 2 chunks",
		"tools": [{"type": "web_search"}]
	}`)

	// Frame 1: text + groundingChunks=[A, A] (deduplicated to [A], raw count 2, cumulativeRemap {0: 0, 1: 0})
	chunk1 := []byte(`data: {
		"responseId": "stream_dedup_f1_f2_1",
		"candidates": [{
			"content": {
				"parts": [{"text": "Alpha."}],
				"role": "model"
			},
			"groundingMetadata": {
				"webSearchQueries": ["test query"],
				"groundingChunks": [
					{"web": {"uri": "https://example.com/A", "title": "Chunk A"}},
					{"web": {"uri": "https://example.com/A", "title": "Chunk A"}}
				]
			}
		}]
	}`)

	// Frame 2: subsequent frame brings chunks [B, C] and support referencing cumulative raw chunk [1]
	chunk2 := []byte(`data: {
		"responseId": "stream_dedup_f1_f2_1",
		"candidates": [{
			"groundingMetadata": {
				"groundingChunks": [
					{"web": {"uri": "https://example.com/B", "title": "Chunk B"}},
					{"web": {"uri": "https://example.com/C", "title": "Chunk C"}}
				],
				"groundingSupports": [
					{
						"segment": {"startIndex": 0, "endIndex": 5, "partIndex": 0},
						"groundingChunkIndices": [1]
					}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
	}`)

	var param any
	e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)

	allEvents := append(e1, e2...)

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
	var msgItem gjson.Result
	for _, out := range outputs {
		if out.Get("type").String() == "message" {
			msgItem = out
			break
		}
	}
	if !msgItem.Exists() {
		t.Fatalf("expected message output item, got: %s", completedJSON.Raw)
	}

	citations := msgItem.Get("content.0.annotations").Array()
	if len(citations) != 1 {
		t.Fatalf("expected 1 citation, got %d: %s", len(citations), msgItem.Raw)
	}
	// Must resolve to Chunk A (raw chunk 1 in cumulative stream), NOT Chunk C (which would happen if 1 hit Frame 2's local index)
	if citations[0].Get("url").String() != "https://example.com/A" {
		t.Fatalf("expected citation url https://example.com/A, got %q", citations[0].Get("url").String())
	}
	if citations[0].Get("start_index").Int() != 0 || citations[0].Get("end_index").Int() != 5 {
		t.Fatalf("unexpected citation range: %s", citations[0].Raw)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_NonWebGrounding_NoWebSearchCallOrUsage(t *testing.T) {
	modelID := "gemini-search-rag-test"
	registerTestWebSearchModel(t, "client-rag-test", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-search-rag-test",
		"input": "Summarize company policy",
		"tools": [{"type": "web_search"}]
	}`)

	t.Run("stream", func(t *testing.T) {
		chunk1 := []byte(`data: {
			"responseId": "stream_rag_test_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "According to the internal policy document, vacation is 20 days."}]
				}
			}]
		}`)

		chunk2 := []byte(`data: {
			"responseId": "stream_rag_test_1",
			"candidates": [{
				"groundingMetadata": {
					"groundingChunks": [
						{
							"retrievedContext": {
								"uri": "policy-doc-42",
								"title": "Employee Handbook"
							}
						}
					],
					"groundingSupports": [
						{
							"groundingChunkIndices": [0],
							"segment": {
								"partIndex": 0,
								"startIndex": 0,
								"endIndex": 40,
								"text": "According to the internal policy document"
							}
						}
					]
				}
			}]
		}`)

		chunk3 := []byte(`data: {
			"responseId": "stream_rag_test_1",
			"candidates": [{
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 8, "candidatesTokenCount": 16, "totalTokenCount": 24}
		}`)

		var param any
		e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
		e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
		e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)

		allEvents := append(append(e1, e2...), e3...)

		var completedJSON gjson.Result
		for _, ev := range allEvents {
			evStr := string(ev)
			if strings.Contains(evStr, "web_search_call") {
				t.Fatalf("did not expect web_search_call for non-web grounding, got event: %s", evStr)
			}
			lines := strings.Split(evStr, "\n")
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

		if !completedJSON.Exists() {
			t.Fatal("expected response.completed event")
		}

		if completedJSON.Get("response.tool_usage.web_search").Exists() {
			t.Fatalf("did not expect tool_usage.web_search for non-web grounding: %s", completedJSON.Raw)
		}

		outputs := completedJSON.Get("response.output").Array()
		for _, out := range outputs {
			if out.Get("type").String() == "web_search_call" {
				t.Fatalf("expected no web_search_call in response.output for non-web grounding: %s", completedJSON.Raw)
			}
		}
	})

	t.Run("non_stream", func(t *testing.T) {
		respJSON := []byte(`{
			"responseId": "nonstream_rag_test_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "According to the internal policy document, vacation is 20 days."}]
				},
				"groundingMetadata": {
					"groundingChunks": [
						{
							"retrievedContext": {
								"uri": "policy-doc-42",
								"title": "Employee Handbook"
							}
						}
					],
					"groundingSupports": [
						{
							"groundingChunkIndices": [0],
							"segment": {
								"partIndex": 0,
								"startIndex": 0,
								"endIndex": 40,
								"text": "According to the internal policy document"
							}
						}
					]
				},
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 8, "candidatesTokenCount": 16, "totalTokenCount": 24}
		}`)

		var param any
		outBytes := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), modelID, req, req, respJSON, &param)
		outParsed := gjson.ParseBytes(outBytes)

		if outParsed.Get("tool_usage.web_search").Exists() {
			t.Fatalf("did not expect tool_usage.web_search in non-stream response for non-web grounding: %s", string(outBytes))
		}

		outputs := outParsed.Get("output").Array()
		for _, out := range outputs {
			if out.Get("type").String() == "web_search_call" {
				t.Fatalf("expected no web_search_call in non-stream output for non-web grounding: %s", string(outBytes))
			}
		}
	})
}

func TestConvertGeminiResponseToOpenAIResponsesStream_LateCitations_AnnotationAdded(t *testing.T) {
	modelID := "gemini-search-late-cite-test"
	registerTestWebSearchModel(t, "client-late-cite-test", "antigravity", modelID, true)

	t.Run("message closed early by function call followed by late grounding metadata", func(t *testing.T) {
		req := []byte(`{
			"model": "gemini-search-late-cite-test",
			"input": "What is the weather in Paris?",
			"tools": [{"type": "web_search"}, {"type": "function", "function": {"name": "get_weather"}}]
		}`)

		chunk1 := []byte(`data: {
			"responseId": "stream_lc_fc_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Paris is sunny today."}]
				}
			}]
		}`)

		chunk2 := []byte(`data: {
			"responseId": "stream_lc_fc_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"functionCall": {"name": "get_weather", "args": {"city": "Paris"}}}]
				}
			}]
		}`)

		chunk3 := []byte(`data: {
			"responseId": "stream_lc_fc_1",
			"candidates": [{
				"groundingMetadata": {
					"webSearchQueries": ["Paris weather today"],
					"groundingChunks": [
						{"web": {"uri": "https://weather.example.com/paris", "title": "Paris Weather"}}
					],
					"groundingSupports": [
						{
							"groundingChunkIndices": [0],
							"segment": {
								"partIndex": 0,
								"startIndex": 0,
								"endIndex": 5,
								"text": "Paris"
							}
						}
					]
				},
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 15, "totalTokenCount": 25}
		}`)

		var param any
		e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
		e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)

		// Verify chunk 2 closed message 0 without citations
		foundMsgDoneWithoutCitations := false
		for _, ev := range e2 {
			lines := strings.Split(string(ev), "\n")
			var currentType string
			for _, line := range lines {
				if strings.HasPrefix(line, "event: ") {
					currentType = strings.TrimPrefix(line, "event: ")
				}
				if strings.HasPrefix(line, "data: ") && currentType == "response.content_part.done" {
					partDone := gjson.Parse(strings.TrimPrefix(line, "data: "))
					if len(partDone.Get("part.annotations").Array()) == 0 {
						foundMsgDoneWithoutCitations = true
					}
				}
			}
		}
		if !foundMsgDoneWithoutCitations {
			t.Fatal("expected message 0 to be closed without citations on chunk 2")
		}

		e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
		allEvents := append(append(e1, e2...), e3...)

		var annotationAddedEvents []gjson.Result
		var completedJSON gjson.Result
		var eventSequence []string

		for _, ev := range allEvents {
			lines := strings.Split(string(ev), "\n")
			var currentType string
			for _, line := range lines {
				if strings.HasPrefix(line, "event: ") {
					currentType = strings.TrimPrefix(line, "event: ")
					eventSequence = append(eventSequence, currentType)
				}
				if strings.HasPrefix(line, "data: ") {
					dataStr := strings.TrimPrefix(line, "data: ")
					if currentType == "response.output_text.annotation.added" {
						annotationAddedEvents = append(annotationAddedEvents, gjson.Parse(dataStr))
					}
					if currentType == "response.completed" {
						completedJSON = gjson.Parse(dataStr)
					}
				}
			}
		}

		if len(annotationAddedEvents) != 1 {
			t.Fatalf("expected 1 response.output_text.annotation.added event, got %d. All events: %v", len(annotationAddedEvents), eventSequence)
		}

		annEv := annotationAddedEvents[0]
		if annEv.Get("type").String() != "response.output_text.annotation.added" {
			t.Fatalf("unexpected event type: %q", annEv.Get("type").String())
		}
		if annEv.Get("output_index").Int() != 0 {
			t.Fatalf("expected output_index 0, got %d", annEv.Get("output_index").Int())
		}
		if annEv.Get("content_index").Int() != 0 {
			t.Fatalf("expected content_index 0, got %d", annEv.Get("content_index").Int())
		}
		if annEv.Get("annotation_index").Int() != 0 {
			t.Fatalf("expected annotation_index 0, got %d", annEv.Get("annotation_index").Int())
		}
		if annEv.Get("response_id").String() == "" {
			t.Fatal("expected non-empty response_id")
		}

		ann := annEv.Get("annotation")
		if ann.Get("type").String() != "url_citation" {
			t.Fatalf("expected annotation type url_citation, got %q", ann.Get("type").String())
		}
		if ann.Get("url").String() != "https://weather.example.com/paris" {
			t.Fatalf("expected url https://weather.example.com/paris, got %q", ann.Get("url").String())
		}
		if ann.Get("title").String() != "Paris Weather" {
			t.Fatalf("expected title Paris Weather, got %q", ann.Get("title").String())
		}
		if ann.Get("start_index").Int() != 0 || ann.Get("end_index").Int() != 5 {
			t.Fatalf("expected span [0, 5), got [%d, %d)", ann.Get("start_index").Int(), ann.Get("end_index").Int())
		}

		// Verify event sequence: response.output_text.annotation.added must be emitted before response.completed
		annIdx := -1
		completedIdx := -1
		for i, evType := range eventSequence {
			if evType == "response.output_text.annotation.added" && annIdx == -1 {
				annIdx = i
			}
			if evType == "response.completed" && completedIdx == -1 {
				completedIdx = i
			}
		}
		if annIdx == -1 || completedIdx == -1 || annIdx >= completedIdx {
			t.Fatalf("expected response.output_text.annotation.added (idx=%d) before response.completed (idx=%d), events=%v", annIdx, completedIdx, eventSequence)
		}

		// Verify that response.completed.response.output[0] annotations match the emitted annotation
		msgOutput := completedJSON.Get("response.output.0")
		if msgOutput.Get("type").String() != "message" {
			t.Fatalf("expected output 0 to be message, got %q: %s", msgOutput.Get("type").String(), completedJSON.Raw)
		}
		if annEv.Get("item_id").String() == "" {
			t.Fatal("expected non-empty item_id in response.output_text.annotation.added")
		}
		if annEv.Get("item_id").String() != msgOutput.Get("id").String() {
			t.Fatalf("expected item_id %q, got %q", msgOutput.Get("id").String(), annEv.Get("item_id").String())
		}
		msgCitations := msgOutput.Get("content.0.annotations").Array()
		if len(msgCitations) != 1 {
			t.Fatalf("expected 1 citation in completed message 0, got %d: %s", len(msgCitations), msgOutput.Raw)
		}
		if msgCitations[0].Get("url").String() != ann.Get("url").String() {
			t.Fatalf("citation url mismatch: completed=%q, emitted=%q", msgCitations[0].Get("url").String(), ann.Get("url").String())
		}
		if msgCitations[0].Get("start_index").Int() != ann.Get("start_index").Int() || msgCitations[0].Get("end_index").Int() != ann.Get("end_index").Int() {
			t.Fatalf("citation range mismatch")
		}
	})

	t.Run("message closed early by thought boundary followed by multiple late citations", func(t *testing.T) {
		req := []byte(`{
			"model": "gemini-search-late-cite-test",
			"input": "Provide details on Alpha and Beta",
			"tools": [{"type": "web_search"}]
		}`)

		chunk1 := []byte(`data: {
			"responseId": "stream_lc_tb_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Alpha is active. Beta is ready."}]
				}
			}]
		}`)

		chunk2 := []byte(`data: {
			"responseId": "stream_lc_tb_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"thought": true, "text": "Analyzing downstream steps..."}]
				}
			}]
		}`)

		chunk3 := []byte(`data: {
			"responseId": "stream_lc_tb_1",
			"candidates": [{
				"groundingMetadata": {
					"webSearchQueries": ["Alpha and Beta status"],
					"groundingChunks": [
						{"web": {"uri": "https://example.com/alpha", "title": "Alpha Doc"}},
						{"web": {"uri": "https://example.com/beta", "title": "Beta Doc"}}
					],
					"groundingSupports": [
						{
							"groundingChunkIndices": [0],
							"segment": {
								"partIndex": 0,
								"startIndex": 0,
								"endIndex": 5,
								"text": "Alpha"
							}
						},
						{
							"groundingChunkIndices": [1],
							"segment": {
								"partIndex": 0,
								"startIndex": 17,
								"endIndex": 21,
								"text": "Beta"
							}
						}
					]
				},
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 20, "totalTokenCount": 30}
		}`)

		var param any
		e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
		e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
		e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)

		allEvents := append(append(e1, e2...), e3...)

		var annotationAddedEvents []gjson.Result
		var completedJSON gjson.Result
		var eventSequence []string

		for _, ev := range allEvents {
			lines := strings.Split(string(ev), "\n")
			var currentType string
			for _, line := range lines {
				if strings.HasPrefix(line, "event: ") {
					currentType = strings.TrimPrefix(line, "event: ")
					eventSequence = append(eventSequence, currentType)
				}
				if strings.HasPrefix(line, "data: ") {
					dataStr := strings.TrimPrefix(line, "data: ")
					if currentType == "response.output_text.annotation.added" {
						annotationAddedEvents = append(annotationAddedEvents, gjson.Parse(dataStr))
					}
					if currentType == "response.completed" {
						completedJSON = gjson.Parse(dataStr)
					}
				}
			}
		}

		if len(annotationAddedEvents) != 2 {
			t.Fatalf("expected 2 response.output_text.annotation.added events, got %d. All events: %v", len(annotationAddedEvents), eventSequence)
		}

		// Verify event 0
		ev0 := annotationAddedEvents[0]
		if ev0.Get("annotation_index").Int() != 0 {
			t.Fatalf("expected annotation_index 0 for first event, got %d", ev0.Get("annotation_index").Int())
		}
		if ev0.Get("output_index").Int() != 0 {
			t.Fatalf("expected output_index 0, got %d", ev0.Get("output_index").Int())
		}
		if ev0.Get("annotation.url").String() != "https://example.com/alpha" {
			t.Fatalf("expected url https://example.com/alpha, got %q", ev0.Get("annotation.url").String())
		}

		// Verify event 1
		ev1 := annotationAddedEvents[1]
		if ev1.Get("annotation_index").Int() != 1 {
			t.Fatalf("expected annotation_index 1 for second event, got %d", ev1.Get("annotation_index").Int())
		}
		if ev1.Get("output_index").Int() != 0 {
			t.Fatalf("expected output_index 0, got %d", ev1.Get("output_index").Int())
		}
		if ev1.Get("annotation.url").String() != "https://example.com/beta" {
			t.Fatalf("expected url https://example.com/beta, got %q", ev1.Get("annotation.url").String())
		}

		// Verify output in completed JSON matches the two annotations
		outputs := completedJSON.Get("response.output").Array()
		var msgOutput gjson.Result
		for _, out := range outputs {
			if out.Get("type").String() == "message" {
				msgOutput = out
				break
			}
		}
		if !msgOutput.Exists() {
			t.Fatalf("expected message output in completed: %s", completedJSON.Raw)
		}
		if ev0.Get("item_id").String() == "" || ev0.Get("item_id").String() != msgOutput.Get("id").String() {
			t.Fatalf("expected item_id %q for first event, got %q", msgOutput.Get("id").String(), ev0.Get("item_id").String())
		}
		if ev1.Get("item_id").String() == "" || ev1.Get("item_id").String() != msgOutput.Get("id").String() {
			t.Fatalf("expected item_id %q for second event, got %q", msgOutput.Get("id").String(), ev1.Get("item_id").String())
		}
		citations := msgOutput.Get("content.0.annotations").Array()
		if len(citations) != 2 {
			t.Fatalf("expected 2 citations in completed message, got %d: %s", len(citations), msgOutput.Raw)
		}
		if citations[0].Get("url").String() != "https://example.com/alpha" || citations[1].Get("url").String() != "https://example.com/beta" {
			t.Fatalf("citations array does not match emitted events: %s", msgOutput.Raw)
		}
	})
}

func TestConvertGeminiResponseToOpenAIResponsesStream_WebSearchBufferingSignatureBoundary(t *testing.T) {
	modelID := "gemini-search-sig-boundary-buffering"
	registerTestWebSearchModel(t, "client-search-sig-boundary-buf", "antigravity", modelID, true)

	req := []byte(`{
		"model": "gemini-search-sig-boundary-buffering",
		"input": "test search query",
		"tools": [{"type": "web_search"}]
	}`)

	// Frame 1: signed text chunk A
	chunk1 := []byte(`data: {
		"responseId": "stream_sb_buf_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "signed chunk A", "thoughtSignature": "` + testResponsesGeminiThoughtSignature + `"}]
			}
		}]
	}`)

	// Frame 2: unsigned text chunk B
	chunk2 := []byte(`data: {
		"responseId": "stream_sb_buf_1",
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "unsigned chunk B"}]
			}
		}]
	}`)

	// Frame 3: grounding metadata
	chunk3 := []byte(`data: {
		"responseId": "stream_sb_buf_1",
		"candidates": [{
			"groundingMetadata": {
				"webSearchQueries": ["test search query"],
				"groundingChunks": [
					{"web": {"uri": "https://example.com/info", "title": "Example Info"}}
				],
				"groundingSupports": [
					{
						"groundingChunkIndices": [0],
						"segment": {
							"startIndex": 0,
							"endIndex": 16,
							"text": "unsigned chunk B"
						}
					}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 20, "totalTokenCount": 30}
	}`)

	var param any
	e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
	e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
	e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)

	allEvents := append(append(e1, e2...), e3...)

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
	var messages []gjson.Result
	for _, out := range outputs {
		if out.Get("type").String() == "message" {
			messages = append(messages, out)
		}
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d: %s", len(messages), completedJSON.Raw)
	}

	if got := messages[0].Get("content.0.text").String(); got != "signed chunk A" {
		t.Fatalf("expected message 0 text 'signed chunk A', got %q", got)
	}
	if got := messages[1].Get("content.0.text").String(); got != "unsigned chunk B" {
		t.Fatalf("expected message 1 text 'unsigned chunk B', got %q", got)
	}

	// Verify that chunk A message has the thought signature, and chunk B message does not.
	// Translate the completed response outputs back to Gemini request to verify signature binding.
	replayReq := []byte(`{"model":"gemini-search-sig-boundary-buffering","input":[]}`)
	replayReq, _ = sjson.SetRawBytes(replayReq, "input", []byte(completedJSON.Get("response.output").Raw))
	translated := ConvertOpenAIResponsesRequestToGemini("gemini-search-sig-boundary-buffering", replayReq, false)

	var visibleParts []gjson.Result
	for _, part := range gjson.GetBytes(translated, "contents.0.parts").Array() {
		if !part.Get("thought").Bool() && part.Get("text").String() != "" {
			visibleParts = append(visibleParts, part)
		}
	}

	if len(visibleParts) != 2 {
		t.Fatalf("expected 2 visible parts on replay, got %d: %s", len(visibleParts), translated)
	}
	if visibleParts[0].Get("text").String() != "signed chunk A" || visibleParts[0].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature {
		t.Fatalf("expected chunk A to have thought signature %q, got text=%q sig=%q", testResponsesGeminiThoughtSignature, visibleParts[0].Get("text").String(), visibleParts[0].Get("thoughtSignature").String())
	}
	if visibleParts[1].Get("text").String() != "unsigned chunk B" || visibleParts[1].Get("thoughtSignature").String() != "" {
		t.Fatalf("expected chunk B to have no thought signature, got text=%q sig=%q", visibleParts[1].Get("text").String(), visibleParts[1].Get("thoughtSignature").String())
	}
}

func TestConvertGeminiResponseToOpenAIResponsesStream_CitationAnnotationAddedTimings(t *testing.T) {
	modelID := "gemini-search-ann-added-timings"
	registerTestWebSearchModel(t, "client-ann-added-timings", "antigravity", modelID, true)

	assertAnnotationMatches := func(t *testing.T, eventTypes []string, byType map[string][]gjson.Result, wantURL, wantTitle string, wantStart, wantEnd int64, late bool) {
		t.Helper()
		annEvents := byType["response.output_text.annotation.added"]
		if len(annEvents) != 1 {
			t.Fatalf("expected 1 response.output_text.annotation.added, got %d. events=%v", len(annEvents), eventTypes)
		}
		annEv := annEvents[0]
		if annEv.Get("type").String() != "response.output_text.annotation.added" {
			t.Fatalf("unexpected event type %q", annEv.Get("type").String())
		}
		if annEv.Get("response_id").String() == "" {
			t.Fatal("expected non-empty response_id")
		}
		if annEv.Get("item_id").String() == "" {
			t.Fatal("expected non-empty item_id")
		}
		if annEv.Get("content_index").Int() != 0 {
			t.Fatalf("expected content_index 0, got %d", annEv.Get("content_index").Int())
		}
		if annEv.Get("annotation_index").Int() != 0 {
			t.Fatalf("expected annotation_index 0, got %d", annEv.Get("annotation_index").Int())
		}
		ann := annEv.Get("annotation")
		if ann.Get("type").String() != "url_citation" {
			t.Fatalf("expected annotation type url_citation, got %q", ann.Get("type").String())
		}
		if ann.Get("url").String() != wantURL {
			t.Fatalf("expected url %q, got %q", wantURL, ann.Get("url").String())
		}
		if ann.Get("title").String() != wantTitle {
			t.Fatalf("expected title %q, got %q", wantTitle, ann.Get("title").String())
		}
		if ann.Get("start_index").Int() != wantStart || ann.Get("end_index").Int() != wantEnd {
			t.Fatalf("expected span [%d, %d), got [%d, %d)", wantStart, wantEnd, ann.Get("start_index").Int(), ann.Get("end_index").Int())
		}

		msgDone := findMessageOutputItemDone(byType["response.output_item.done"])
		if !msgDone.Exists() {
			t.Fatal("expected message output_item.done")
		}
		if annEv.Get("item_id").String() != msgDone.Get("item.id").String() {
			t.Fatalf("annotation item_id %q != message id %q", annEv.Get("item_id").String(), msgDone.Get("item.id").String())
		}
		if annEv.Get("output_index").Int() != msgDone.Get("output_index").Int() {
			t.Fatalf("annotation output_index %d != message output_index %d", annEv.Get("output_index").Int(), msgDone.Get("output_index").Int())
		}
		if !late {
			msgCites := msgDone.Get("item.content.0.annotations").Array()
			if len(msgCites) != 1 || msgCites[0].Get("url").String() != wantURL {
				t.Fatalf("message output_item.done annotations mismatch: %s", msgDone.Raw)
			}
		}

		if len(byType["response.completed"]) == 0 {
			t.Fatal("expected response.completed")
		}
		completedJSON := byType["response.completed"][0]
		var completedMsg gjson.Result
		for _, out := range completedJSON.Get("response.output").Array() {
			if out.Get("type").String() == "message" {
				completedMsg = out
				break
			}
		}
		if !completedMsg.Exists() {
			t.Fatalf("expected message in response.completed: %s", completedJSON.Raw)
		}
		completedCites := completedMsg.Get("content.0.annotations").Array()
		if len(completedCites) != 1 || completedCites[0].Get("url").String() != wantURL {
			t.Fatalf("completed message annotations mismatch: %s", completedMsg.Raw)
		}

		annIdx := -1
		completedIdx := -1
		msgDoneEventIdx := -1
		doneSeen := 0
		for i, evType := range eventTypes {
			switch evType {
			case "response.output_text.annotation.added":
				if annIdx == -1 {
					annIdx = i
				}
			case "response.output_item.done":
				payload := byType["response.output_item.done"][doneSeen]
				doneSeen++
				if payload.Get("item.type").String() == "message" && msgDoneEventIdx == -1 {
					msgDoneEventIdx = i
				}
			case "response.completed":
				if completedIdx == -1 {
					completedIdx = i
				}
			}
		}
		if annIdx == -1 || completedIdx == -1 || annIdx >= completedIdx {
			t.Fatalf("expected annotation.added (idx=%d) before response.completed (idx=%d), events=%v", annIdx, completedIdx, eventTypes)
		}
		if late {
			if msgDoneEventIdx == -1 || annIdx <= msgDoneEventIdx {
				t.Fatalf("expected late annotation.added (idx=%d) after message output_item.done (idx=%d), events=%v", annIdx, msgDoneEventIdx, eventTypes)
			}
		} else if msgDoneEventIdx == -1 || annIdx >= msgDoneEventIdx {
			t.Fatalf("expected annotation.added (idx=%d) before message output_item.done (idx=%d), events=%v", annIdx, msgDoneEventIdx, eventTypes)
		}
	}

	t.Run("early metadata before message closes", func(t *testing.T) {
		req := []byte(`{
			"model": "gemini-search-ann-added-timings",
			"input": "search query",
			"tools": [{"type": "web_search"}]
		}`)

		chunk1 := []byte(`data: {
			"responseId": "stream_ann_early_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Hello world."}]
				},
				"groundingMetadata": {
					"webSearchQueries": ["hello query"],
					"groundingChunks": [{"web": {"uri": "https://example.com/hello", "title": "Hello"}}],
					"groundingSupports": [{
						"groundingChunkIndices": [0],
						"segment": {"partIndex": 0, "startIndex": 0, "endIndex": 5, "text": "Hello"}
					}]
				}
			}]
		}`)

		chunk2 := []byte(`data: {
			"responseId": "stream_ann_early_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": " More."}]
				}
			}]
		}`)

		chunk3 := []byte(`data: {
			"responseId": "stream_ann_early_1",
			"candidates": [{"finishReason": "STOP"}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
		}`)

		var param any
		e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
		e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
		e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
		eventTypes, byType := collectResponsesStreamEvents(append(append(e1, e2...), e3...))
		assertAnnotationMatches(t, eventTypes, byType, "https://example.com/hello", "Hello", 0, 5, false)
	})

	t.Run("same-frame metadata with message close", func(t *testing.T) {
		req := []byte(`{
			"model": "gemini-search-ann-added-timings",
			"input": "search query",
			"tools": [{"type": "web_search"}]
		}`)

		chunk1 := []byte(`data: {
			"responseId": "stream_ann_same_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Hello world."}]
				},
				"groundingMetadata": {
					"webSearchQueries": ["hello query"],
					"groundingChunks": [{"web": {"uri": "https://example.com/hello", "title": "Hello"}}],
					"groundingSupports": [{
						"groundingChunkIndices": [0],
						"segment": {"partIndex": 0, "startIndex": 0, "endIndex": 5, "text": "Hello"}
					}]
				},
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 8, "totalTokenCount": 13}
		}`)

		var param any
		events := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
		eventTypes, byType := collectResponsesStreamEvents(events)
		assertAnnotationMatches(t, eventTypes, byType, "https://example.com/hello", "Hello", 0, 5, false)
	})

	t.Run("late metadata after message closes", func(t *testing.T) {
		req := []byte(`{
			"model": "gemini-search-ann-added-timings",
			"input": "What is the weather in Paris?",
			"tools": [{"type": "web_search"}, {"type": "function", "function": {"name": "get_weather"}}]
		}`)

		chunk1 := []byte(`data: {
			"responseId": "stream_ann_late_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Paris is sunny today."}]
				}
			}]
		}`)

		chunk2 := []byte(`data: {
			"responseId": "stream_ann_late_1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"functionCall": {"name": "get_weather", "args": {"city": "Paris"}}}]
				}
			}]
		}`)

		chunk3 := []byte(`data: {
			"responseId": "stream_ann_late_1",
			"candidates": [{
				"groundingMetadata": {
					"webSearchQueries": ["Paris weather today"],
					"groundingChunks": [{"web": {"uri": "https://weather.example.com/paris", "title": "Paris Weather"}}],
					"groundingSupports": [{
						"groundingChunkIndices": [0],
						"segment": {"partIndex": 0, "startIndex": 0, "endIndex": 5, "text": "Paris"}
					}]
				},
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 15, "totalTokenCount": 25}
		}`)

		var param any
		e1 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk1, &param)
		e2 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk2, &param)
		e3 := ConvertGeminiResponseToOpenAIResponses(context.Background(), modelID, req, req, chunk3, &param)
		eventTypes, byType := collectResponsesStreamEvents(append(append(e1, e2...), e3...))
		assertAnnotationMatches(t, eventTypes, byType, "https://weather.example.com/paris", "Paris Weather", 0, 5, true)
	})
}
