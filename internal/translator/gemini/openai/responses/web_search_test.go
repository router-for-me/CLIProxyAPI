package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToGemini_WebSearchTools(t *testing.T) {
	for _, toolType := range []string{"web_search", "web_search_preview", "web_search_preview_2025_03_11"} {
		t.Run(toolType, func(t *testing.T) {
			input := []byte(`{"model":"gemini-test","input":"latest Go release","tools":[{"type":"` + toolType + `"}]}`)
			output := ConvertOpenAIResponsesRequestToGemini("gemini-test", input, false)

			tools := gjson.GetBytes(output, "tools").Array()
			if len(tools) != 1 {
				t.Fatalf("tools length = %d, want 1; output=%s", len(tools), output)
			}
			if !tools[0].Get("googleSearch").Exists() {
				t.Fatalf("googleSearch tool missing: %s", output)
			}
			if tools[0].Get("functionDeclarations").Exists() {
				t.Fatalf("web search unexpectedly became a function declaration: %s", output)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToGemini_WebSearchAndFunctionToolsRemainSeparate(t *testing.T) {
	input := []byte(`{
		"model":"gemini-test",
		"input":"find the release and save it",
		"tools":[
			{"type":"web_search"},
			{"type":"function","name":"save_release","description":"Save a release","parameters":{"type":"object","properties":{"version":{"type":"string"}}}}
		]
	}`)
	output := ConvertOpenAIResponsesRequestToGemini("gemini-test", input, false)

	tools := gjson.GetBytes(output, "tools").Array()
	if len(tools) != 2 {
		t.Fatalf("tools length = %d, want 2; output=%s", len(tools), output)
	}
	if got := tools[0].Get("functionDeclarations.0.name").String(); got != "save_release" {
		t.Fatalf("function declaration name = %q, want save_release; output=%s", got, output)
	}
	if !tools[1].Get("googleSearch").Exists() {
		t.Fatalf("googleSearch must be a separate Gemini tool: %s", output)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_WebSearchGrounding(t *testing.T) {
	request := []byte(`{"model":"gemini-test","input":"latest Go release","tools":[{"type":"web_search"}]}`)
	response := []byte(`{
		"responseId":"search-nonstream",
		"candidates":[{
			"content":{"parts":[{"text":"Go 1.26 is current."}]},
			"groundingMetadata":{
				"webSearchQueries":["latest Go release"],
				"groundingChunks":[{"web":{"uri":"https://go.dev/doc/go1.26","title":"Go 1.26 Release Notes"}}],
				"groundingSupports":[{"segment":{"startIndex":0,"endIndex":7,"text":"Go 1.26"},"groundingChunkIndices":[0]}]
			},
			"finishReason":"STOP"
		}]
	}`)

	output := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-test", request, nil, response, nil)
	if got := gjson.GetBytes(output, "output.0.type").String(); got != "web_search_call" {
		t.Fatalf("output.0.type = %q, want web_search_call; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "output.0.status").String(); got != "completed" {
		t.Fatalf("web search status = %q, want completed; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "output.0.action.query").String(); got != "latest Go release" {
		t.Fatalf("web search query = %q; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "output.0.action.sources.0.url").String(); got != "https://go.dev/doc/go1.26" {
		t.Fatalf("web search source URL = %q; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "output.1.type").String(); got != "message" {
		t.Fatalf("output.1.type = %q, want message; output=%s", got, output)
	}
	annotation := gjson.GetBytes(output, "output.1.content.0.annotations.0")
	if annotation.Get("type").String() != "url_citation" || annotation.Get("url").String() != "https://go.dev/doc/go1.26" {
		t.Fatalf("unexpected citation annotation: %s; output=%s", annotation.Raw, output)
	}
	if annotation.Get("start_index").Int() != 0 || annotation.Get("end_index").Int() != 7 {
		t.Fatalf("citation offsets = %d..%d, want 0..7; output=%s", annotation.Get("start_index").Int(), annotation.Get("end_index").Int(), output)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_StreamBuffersWebSearchBeforeMessage(t *testing.T) {
	request := []byte(`{"model":"gemini-test","input":"latest Go release","tools":[{"type":"web_search"}]}`)
	var param any

	first := []byte(`data: {"responseId":"search-stream","candidates":[{"content":{"parts":[{"text":"Go 1.26 "}]}}]}`)
	if output := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-test", request, nil, first, &param); len(output) != 0 {
		t.Fatalf("web-search stream emitted text before grounding metadata: %q", output)
	}

	final := []byte(`data: {
		"responseId":"search-stream",
		"candidates":[{
			"content":{"parts":[{"text":"is current."}]},
			"groundingMetadata":{
				"webSearchQueries":["latest Go release"],
				"groundingChunks":[{"web":{"uri":"https://go.dev/doc/go1.26","title":"Go 1.26 Release Notes"}}],
				"groundingSupports":[{"segment":{"startIndex":0,"endIndex":7,"text":"Go 1.26"},"groundingChunkIndices":[0]}]
			},
			"finishReason":"STOP"
		}],
		"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":4,"totalTokenCount":9}
	}`)
	output := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-test", request, nil, final, &param)
	if len(output) == 0 {
		t.Fatal("final web-search stream emitted no events")
	}

	webSearchDoneIndex := -1
	messageAddedIndex := -1
	searchingSeen := false
	completedSeen := false
	annotationSeen := false
	var completedResponse gjson.Result
	for i, chunk := range output {
		event, data := parseSSEEvent(t, chunk)
		switch event {
		case "response.output_item.done":
			if data.Get("item.type").String() == "web_search_call" {
				webSearchDoneIndex = i
			}
		case "response.output_item.added":
			if data.Get("item.type").String() == "message" && messageAddedIndex < 0 {
				messageAddedIndex = i
			}
		case "response.web_search_call.searching":
			searchingSeen = true
		case "response.web_search_call.completed":
			completedSeen = true
		case "response.output_text.annotation.added":
			annotationSeen = data.Get("annotation.type").String() == "url_citation"
		case "response.completed":
			completedResponse = data.Get("response")
		}
	}

	if webSearchDoneIndex < 0 || messageAddedIndex < 0 || webSearchDoneIndex >= messageAddedIndex {
		t.Fatalf("web_search_call must complete before the message starts; web=%d message=%d", webSearchDoneIndex, messageAddedIndex)
	}
	if !searchingSeen || !completedSeen {
		t.Fatalf("missing web search lifecycle events: searching=%v completed=%v", searchingSeen, completedSeen)
	}
	if !annotationSeen {
		t.Fatal("missing response.output_text.annotation.added event")
	}
	if got := completedResponse.Get("output.0.type").String(); got != "web_search_call" {
		t.Fatalf("completed response output.0.type = %q, want web_search_call; response=%s", got, completedResponse.Raw)
	}
	if got := completedResponse.Get("output.1.content.0.text").String(); got != "Go 1.26 is current." {
		t.Fatalf("buffered message text = %q; response=%s", got, completedResponse.Raw)
	}
	if got := completedResponse.Get("output.1.content.0.annotations.0.url").String(); got != "https://go.dev/doc/go1.26" {
		t.Fatalf("completed response citation URL = %q; response=%s", got, completedResponse.Raw)
	}
}

func TestBuildOpenAIResponsesWebSearchCallItem_AlwaysIncludesQuery(t *testing.T) {
	groundingMetadata := gjson.Parse(`{"groundingChunks":[{"web":{"uri":"https://example.com"}}]}`)
	item := buildOpenAIResponsesWebSearchCallItem("resp_query-default", groundingMetadata)
	query := gjson.GetBytes(item, "action.query")
	if !query.Exists() || query.Type != gjson.String || query.String() != "" {
		t.Fatalf("action.query must be an empty string when upstream omits queries: %s", item)
	}
}
