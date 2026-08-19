package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToAntigravity_PreservesWebSearchTool(t *testing.T) {
	input := []byte(`{
		"model":"gemini-3.1-flash-lite",
		"input":"latest Go release",
		"tools":[
			{"type":"web_search"},
			{"type":"function","name":"save_release","parameters":{"type":"object"}}
		]
	}`)
	output := ConvertOpenAIResponsesRequestToAntigravity("gemini-3.1-flash-lite", input, false)

	tools := gjson.GetBytes(output, "request.tools").Array()
	if len(tools) != 2 {
		t.Fatalf("request.tools length = %d, want 2; output=%s", len(tools), output)
	}
	if got := tools[0].Get("functionDeclarations.0.name").String(); got != "save_release" {
		t.Fatalf("function declaration name = %q, want save_release; output=%s", got, output)
	}
	if !tools[1].Get("googleSearch").Exists() {
		t.Fatalf("Antigravity request dropped googleSearch: %s", output)
	}
}

func TestConvertAntigravityResponseToOpenAIResponsesNonStream_WebSearchGrounding(t *testing.T) {
	originalRequest := []byte(`{"model":"gemini-3.1-flash-lite","input":"latest Go release","tools":[{"type":"web_search"}]}`)
	translatedRequest := []byte(`{"model":"gemini-3.1-flash-lite","request":{"tools":[{"googleSearch":{}}]}}`)
	rawResponse := []byte(`{
		"response":{
			"responseId":"antigravity-search",
			"candidates":[{
				"content":{"parts":[{"text":"Go 1.26 is current."}]},
				"groundingMetadata":{
					"webSearchQueries":["latest Go release"],
					"groundingChunks":[{"web":{"uri":"https://go.dev/doc/go1.26","title":"Go 1.26 Release Notes"}}],
					"groundingSupports":[{"segment":{"startIndex":0,"endIndex":7,"text":"Go 1.26"},"groundingChunkIndices":[0]}]
				},
				"finishReason":"STOP"
			}]
		}
	}`)

	output := ConvertAntigravityResponseToOpenAIResponsesNonStream(
		context.Background(),
		"gemini-3.1-flash-lite",
		originalRequest,
		translatedRequest,
		rawResponse,
		nil,
	)
	if got := gjson.GetBytes(output, "output.0.type").String(); got != "web_search_call" {
		t.Fatalf("output.0.type = %q, want web_search_call; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "output.0.action.sources.0.url").String(); got != "https://go.dev/doc/go1.26" {
		t.Fatalf("web search source URL = %q; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "output.1.content.0.annotations.0.type").String(); got != "url_citation" {
		t.Fatalf("citation type = %q, want url_citation; output=%s", got, output)
	}
}
