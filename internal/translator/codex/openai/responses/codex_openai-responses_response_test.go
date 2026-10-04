package responses

import (
	"context"
	"testing"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToOpenAIResponses_CreatedIncludesOriginalRequestModel(t *testing.T) {
	request := []byte(`{"model":"original-codex-model"}`)
	translatedRequest := []byte(`{"model":"translated-codex-model"}`)
	for eventName, raw := range map[string][]byte{
		"response.created":     []byte(`data: {"type":"response.created","response":{"id":"resp_1"}}`),
		"response.in_progress": []byte(`data: {"type":"response.in_progress","response":{"id":"resp_1"}}`),
	} {
		outputs := ConvertCodexResponseToOpenAIResponses(context.Background(), "fallback-model", request, translatedRequest, raw, nil)
		if len(outputs) != 1 {
			t.Fatalf("%s outputs = %d, want 1", eventName, len(outputs))
		}
		if got := gjson.GetBytes(outputs[0], "response.model").String(); got != "original-codex-model" {
			t.Fatalf("%s models = %q, want original-codex-model; payload=%s", eventName, got, outputs[0])
		}
	}
}

func TestConvertCodexResponseToOpenAIResponsesNonStreamIncomplete(t *testing.T) {
	raw := []byte(`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)

	out := ConvertCodexResponseToOpenAIResponsesNonStream(context.Background(), "gpt-5.5", nil, nil, raw, nil)

	if got := gjson.GetBytes(out, "status").String(); got != "incomplete" {
		t.Fatalf("status = %q, want incomplete; payload=%s", got, out)
	}
	if got := gjson.GetBytes(out, "incomplete_details.reason").String(); got != "max_output_tokens" {
		t.Fatalf("incomplete reason = %q, want max_output_tokens; payload=%s", got, out)
	}
}

func TestConvertCodexResponseToOpenAIResponses_PreservesWebSearchSources(t *testing.T) {
	const searchItem = `{"id":"ws_fixture_0123456789abcdef0123456789abcdef","type":"web_search_call","status":"completed","action":{"type":"search","queries":["python asyncio documentation"],"query":"python asyncio documentation","sources":[{"type":"url","url":"https://docs.python.org/3.13/library/asyncio.html"},{"type":"url","url":"https://docs.python.org/3/library/asyncio-task.html?highlight=n"}]}}`
	const messageItem = `{"id":"msg_fixture_0123456789abcdef0123456789abcdef","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Python docs","annotations":[{"type":"url_citation","start_index":0,"end_index":11,"title":"asyncio","url":"https://docs.python.org/3/library/asyncio.html?trk=article-ssr-frontend-pulse_little-text-block&utm_source=openai"}]}]}`

	done := []byte(`data: {"type":"response.output_item.done","output_index":0,"item":` + searchItem + "}")
	outputs := ConvertCodexResponseToOpenAIResponses(context.Background(), "gpt-6-luna", nil, nil, done, nil)
	if len(outputs) != 1 {
		t.Fatalf("output_item.done outputs = %d, want 1", len(outputs))
	}
	if got := gjson.GetBytes(outputs[0], "item.id").String(); got != "ws_fixture_0123456789abcdef0123456789abcdef" {
		t.Fatalf("item.id = %q, want ws_fixture_0123456789abcdef0123456789abcdef", got)
	}
	sources := gjson.GetBytes(outputs[0], "item.action.sources").Array()
	if len(sources) != 2 {
		t.Fatalf("item.action.sources = %s, want 2 entries", gjson.GetBytes(outputs[0], "item.action.sources").Raw)
	}
	if got := sources[0].Get("url").String(); got != "https://docs.python.org/3.13/library/asyncio.html" {
		t.Fatalf("sources[0].url = %q", got)
	}
	if got := sources[1].Get("url").String(); got != "https://docs.python.org/3/library/asyncio-task.html?highlight=n" {
		t.Fatalf("sources[1].url = %q", got)
	}

	completed := []byte(`data: {"type":"response.completed","response":{"id":"resp_04f9","status":"completed","output":[` + searchItem + "," + messageItem + "]}}")
	outputs = ConvertCodexResponseToOpenAIResponses(context.Background(), "gpt-6-luna", nil, nil, completed, nil)
	if len(outputs) != 1 {
		t.Fatalf("response.completed outputs = %d, want 1", len(outputs))
	}
	if got := gjson.GetBytes(outputs[0], "response.output.0.action.sources.1.url").String(); got != "https://docs.python.org/3/library/asyncio-task.html?highlight=n" {
		t.Fatalf("response.output.0.action.sources.1.url = %q", got)
	}
	if got := gjson.GetBytes(outputs[0], "response.output.1.id").String(); got != "msg_fixture_0123456789abcdef0123456789abcdef" {
		t.Fatalf("response.output.1.id = %q, want msg_fixture_0123456789abcdef0123456789abcdef", got)
	}
	if got := gjson.GetBytes(outputs[0], "response.output.1.content.0.annotations.0.url").String(); got != "https://docs.python.org/3/library/asyncio.html?trk=article-ssr-frontend-pulse_little-text-block&utm_source=openai" {
		t.Fatalf("citation url = %q", got)
	}
}

func TestConvertCodexResponseToOpenAIResponsesNonStream_PreservesWebSearchSources(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_04f9","status":"completed","output":[{"id":"ws_fixture_0123456789abcdef0123456789abcdef","type":"web_search_call","status":"completed","action":{"type":"search","query":"python asyncio documentation","sources":[{"type":"url","url":"https://docs.python.org/3.13/library/asyncio.html"},{"type":"url","url":"https://docs.python.org/3/library/asyncio-task.html?highlight=n"}]}},{"id":"msg_fixture_0123456789abcdef0123456789abcdef","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Python docs","annotations":[{"type":"url_citation","start_index":0,"end_index":11,"title":"asyncio","url":"https://docs.python.org/3/library/asyncio.html?trk=article-ssr-frontend-pulse_little-text-block&utm_source=openai"}]}]}]}}`)

	out := ConvertCodexResponseToOpenAIResponsesNonStream(context.Background(), "gpt-6-luna", nil, nil, raw, nil)

	if got := gjson.GetBytes(out, "output.0.id").String(); got != "ws_fixture_0123456789abcdef0123456789abcdef" {
		t.Fatalf("output.0.id = %q, want ws_fixture_0123456789abcdef0123456789abcdef", got)
	}
	sources := gjson.GetBytes(out, "output.0.action.sources").Array()
	if len(sources) != 2 {
		t.Fatalf("output.0.action.sources = %s, want 2 entries", gjson.GetBytes(out, "output.0.action.sources").Raw)
	}
	if got := sources[0].Get("url").String(); got != "https://docs.python.org/3.13/library/asyncio.html" {
		t.Fatalf("sources[0].url = %q", got)
	}
	if got := sources[1].Get("url").String(); got != "https://docs.python.org/3/library/asyncio-task.html?highlight=n" {
		t.Fatalf("sources[1].url = %q", got)
	}
	if got := gjson.GetBytes(out, "output.1.content.0.annotations.0.type").String(); got != "url_citation" {
		t.Fatalf("annotation type = %q, want url_citation", got)
	}
	if got := gjson.GetBytes(out, "output.1.content.0.annotations.0.url").String(); got != "https://docs.python.org/3/library/asyncio.html?trk=article-ssr-frontend-pulse_little-text-block&utm_source=openai" {
		t.Fatalf("citation url = %q", got)
	}
}

func TestConvertCodexResponseToOpenAIResponses_MissingSourcesStayAbsentWithCitation(t *testing.T) {
	const searchItem = `{"id":"ws_missing","type":"web_search_call","status":"completed","action":{"type":"search","query":"python asyncio documentation"}}`
	const messageItem = `{"id":"msg_cited","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Python docs","annotations":[{"type":"url_citation","start_index":0,"end_index":11,"title":"asyncio","url":"https://docs.python.org/3/library/asyncio.html?trk=article-ssr-frontend-pulse_little-text-block&utm_source=openai"}]}]}`

	done := []byte(`data: {"type":"response.output_item.done","output_index":0,"item":` + searchItem + "}")
	outputs := ConvertCodexResponseToOpenAIResponses(context.Background(), "gpt-6-luna", nil, nil, done, nil)
	if len(outputs) != 1 {
		t.Fatalf("output_item.done outputs = %d, want 1", len(outputs))
	}
	if gjson.GetBytes(outputs[0], "item.action.sources").Exists() {
		t.Fatalf("sources should not be fabricated on output_item.done: %s", outputs[0])
	}

	completed := []byte(`data: {"type":"response.completed","response":{"id":"resp_missing","status":"completed","output":[` + searchItem + "," + messageItem + "]}}")
	outputs = ConvertCodexResponseToOpenAIResponses(context.Background(), "gpt-6-luna", nil, nil, completed, nil)
	if len(outputs) != 1 {
		t.Fatalf("response.completed outputs = %d, want 1", len(outputs))
	}
	if gjson.GetBytes(outputs[0], "response.output.0.action.sources").Exists() {
		t.Fatalf("sources should not be fabricated on response.completed: %s", outputs[0])
	}
	if got := gjson.GetBytes(outputs[0], "response.output.1.content.0.annotations.0.type").String(); got != "url_citation" {
		t.Fatalf("annotation type = %q, want url_citation; payload=%s", got, outputs[0])
	}

	out := ConvertCodexResponseToOpenAIResponsesNonStream(context.Background(), "gpt-6-luna", nil, nil, []byte(`{"type":"response.completed","response":{"id":"resp_missing","status":"completed","output":[`+searchItem+","+messageItem+"]}}"), nil)
	if gjson.GetBytes(out, "output.0.action.sources").Exists() {
		t.Fatalf("sources should not be fabricated on non-stream response: %s", out)
	}
	if got := gjson.GetBytes(out, "output.1.content.0.annotations.0.url").String(); got != "https://docs.python.org/3/library/asyncio.html?trk=article-ssr-frontend-pulse_little-text-block&utm_source=openai" {
		t.Fatalf("citation url = %q", got)
	}
}

func TestApplyPatchResponsesActualRequestGatesNativeCodex(t *testing.T) {
	original := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`)
	bridged, errNormalize := translatorcommon.NormalizeApplyPatchResponsesRequest(original)
	if errNormalize != nil {
		t.Fatal(errNormalize)
	}
	raw := []byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"a","call_id":"c","name":"apply_patch","arguments":"{\"input\":\"p\"}"}}`)
	var native any
	out := ConvertCodexResponseToOpenAIResponses(t.Context(), "m", original, original, raw, &native)
	if len(out) != 1 || string(out[0]) != string(raw) {
		t.Fatalf("native Codex changed: %s", out)
	}
	var configuredNative any
	out = ConvertCodexResponseToOpenAIResponses(t.Context(), "m", original, bridged, raw, &configuredNative)
	if len(out) != 1 || string(out[0]) != string(raw) {
		t.Fatalf("configuration enabled native bridging: %s", out)
	}
	var xai any = translatorcommon.NewApplyPatchResponsesBridge(original)
	out = ConvertCodexResponseToOpenAIResponses(t.Context(), "m", original, bridged, raw, &xai)
	if len(out) != 3 || gjson.GetBytes(out[2][6:], "item.input").String() != "p" {
		t.Fatalf("same Codex wire format bypassed bridge: %s", out)
	}
	var failed any = translatorcommon.NewApplyPatchResponsesBridge(original)
	out = ConvertCodexResponseToOpenAIResponses(t.Context(), "m", original, bridged, []byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","name":"apply_patch","arguments":"{}"}}`), &failed)
	if len(out) != 1 || gjson.GetBytes(out[0][6:], "type").String() != "response.failed" || failed.(interface{ ToolInputError() error }).ToolInputError() == nil {
		t.Fatalf("failure: %s", out)
	}
}
