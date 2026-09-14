package translator

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// These tests prove the integral-float → integer coercion for integer-typed tool-call
// parameters is applied at the registry choke point (so every upstream provider benefits),
// keyed off the ORIGINAL client request's tool schema, for both client formats and both
// streaming modes — and that it is inert without tools / for other client formats.

const toolArgsChatRequest = `{"model":"m","tools":[{"type":"function","function":{"name":"wait","parameters":{"type":"object","properties":{"yield_time_ms":{"type":"integer"}}}}}]}`
const toolArgsResponsesRequest = `{"model":"m","tools":[{"type":"function","name":"wait","parameters":{"type":"object","properties":{"yield_time_ms":{"type":"integer"}}}}]}`

func fakeFloatEmittingProvider() ResponseTransform {
	return ResponseTransform{
		Stream: func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
			// A provider translator that already produced client-format events from an
			// upstream that wrote the integer as 14380.0.
			return [][]byte{rawJSON}
		},
		NonStream: func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
			return rawJSON
		},
	}
}

func TestRegistryCoercesToolArgsChatNonStream(t *testing.T) {
	r := NewRegistry()
	r.Register(Format("fake"), FormatOpenAI, nil, fakeFloatEmittingProvider())
	body := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"wait","arguments":"{\"yield_time_ms\":14380.0}"}}]},"finish_reason":"tool_calls"}]}`)
	out := r.TranslateNonStream(context.Background(), Format("fake"), FormatOpenAI, "m", []byte(toolArgsChatRequest), nil, body, nil)
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.arguments").String(); got != `{"yield_time_ms":14380}` {
		t.Fatalf("arguments = %s", got)
	}
	if gjson.GetBytes(out, "choices.0.finish_reason").String() != "tool_calls" || gjson.GetBytes(out, "choices.0.message.tool_calls.0.id").String() != "call_1" {
		t.Fatalf("finish_reason / call id must be untouched: %s", out)
	}
	// Without tools in the client request the body is returned byte-identical.
	if got := r.TranslateNonStream(context.Background(), Format("fake"), FormatOpenAI, "m", []byte(`{"model":"m"}`), nil, body, nil); string(got) != string(body) {
		t.Fatalf("no-tools request must be a no-op")
	}
}

func TestRegistryCoercesToolArgsChatStreamWholeValueChunk(t *testing.T) {
	r := NewRegistry()
	r.Register(Format("fake"), FormatOpenAI, nil, fakeFloatEmittingProvider())
	var param any
	chunk := []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"wait","arguments":"{\"yield_time_ms\":14380.0}"}}]},"finish_reason":null}]}`)
	outs := r.TranslateStream(context.Background(), Format("fake"), FormatOpenAI, "m", []byte(toolArgsChatRequest), nil, chunk, &param)
	if len(outs) != 1 {
		t.Fatalf("outputs = %d", len(outs))
	}
	if got := gjson.GetBytes(outs[0], "choices.0.delta.tool_calls.0.function.arguments").String(); got != `{"yield_time_ms":14380}` {
		t.Fatalf("stream arguments = %s", got)
	}
	// A fragment chunk (no name, partial JSON) is passed through verbatim.
	frag := []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"yield_time_ms\":143"}}]}}]}`)
	outs = r.TranslateStream(context.Background(), Format("fake"), FormatOpenAI, "m", []byte(toolArgsChatRequest), nil, frag, &param)
	if string(outs[0]) != string(frag) {
		t.Fatalf("fragment must pass through: %s", outs[0])
	}
}

func TestRegistryCoercesToolArgsResponsesStreamAndNonStream(t *testing.T) {
	r := NewRegistry()
	r.Register(Format("fake"), FormatOpenAIResponse, nil, fakeFloatEmittingProvider())
	ctx := context.Background()
	var param any
	send := func(ev string) []byte {
		outs := r.TranslateStream(ctx, Format("fake"), FormatOpenAIResponse, "m", []byte(toolArgsResponsesRequest), nil, []byte(ev), &param)
		if len(outs) != 1 {
			t.Fatalf("outputs = %d", len(outs))
		}
		return outs[0]
	}
	send("event: response.output_item.added\ndata: " + `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"in_progress","arguments":"","call_id":"call_1","name":"wait"}}` + "\n\n")
	done := send("event: response.function_call_arguments.done\ndata: " + `{"type":"response.function_call_arguments.done","sequence_number":2,"item_id":"fc_1","output_index":0,"arguments":"{\"yield_time_ms\":14380.0}"}` + "\n\n")
	if !strings.Contains(string(done), `"arguments":"{\"yield_time_ms\":14380}"`) || !strings.HasPrefix(string(done), "event: response.function_call_arguments.done\ndata: ") {
		t.Fatalf("args.done not coerced / framing lost: %q", done)
	}
	itemDone := send("event: response.output_item.done\ndata: " + `{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"yield_time_ms\":14380.0}","call_id":"call_1","name":"wait"}}` + "\n\n")
	if !strings.Contains(string(itemDone), `"arguments":"{\"yield_time_ms\":14380}"`) || !strings.Contains(string(itemDone), `"sequence_number":3`) || !strings.Contains(string(itemDone), `"call_id":"call_1"`) {
		t.Fatalf("output_item.done not coerced or metadata altered: %q", itemDone)
	}
	send("event: response.completed\ndata: " + `{"type":"response.completed","sequence_number":4,"response":{"id":"r"}}` + "\n\n")

	body := []byte(`{"id":"r","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"wait","arguments":"{\"yield_time_ms\":14380.0}","status":"completed"}]}`)
	out := r.TranslateNonStream(ctx, Format("fake"), FormatOpenAIResponse, "m", []byte(toolArgsResponsesRequest), nil, body, nil)
	if got := gjson.GetBytes(out, "output.0.arguments").String(); got != `{"yield_time_ms":14380}` {
		t.Fatalf("responses non-stream arguments = %s", got)
	}
}

func TestRegistryToolArgsCoercionRunsBeforePluginNormalizeAfterAndSkipsOtherFormats(t *testing.T) {
	r := NewRegistry()
	r.Register(Format("fake"), FormatOpenAI, nil, fakeFloatEmittingProvider())
	var seenByPlugin string
	r.SetPluginHooks(&fakePluginHooks{normalizeAfter: func(b []byte) []byte { seenByPlugin = string(b); return b }})
	body := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"wait","arguments":"{\"yield_time_ms\":14380.0}"}}]}}]}`)
	r.TranslateNonStream(context.Background(), Format("fake"), FormatOpenAI, "m", []byte(toolArgsChatRequest), nil, body, nil)
	if !strings.Contains(seenByPlugin, `14380}`) {
		t.Fatalf("plugin NormalizeResponseAfter must observe the already-coerced body, got %s", seenByPlugin)
	}
	// A non-OpenAI client format (e.g. a Claude-format client) is never touched.
	r2 := NewRegistry()
	r2.Register(Format("fake"), Format("claude"), nil, fakeFloatEmittingProvider())
	claudeBody := []byte(`{"content":[{"type":"tool_use","name":"wait","input":{"yield_time_ms":14380.0}}]}`)
	if got := r2.TranslateNonStream(context.Background(), Format("fake"), Format("claude"), "m", []byte(toolArgsChatRequest), nil, claudeBody, nil); string(got) != string(claudeBody) {
		t.Fatalf("non-OpenAI client format must be untouched: %s", got)
	}
}
