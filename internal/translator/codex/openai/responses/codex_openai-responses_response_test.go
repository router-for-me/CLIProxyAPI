package responses

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToOpenAIResponses_NormalizesRgRelativePath(t *testing.T) {
	ctx := context.Background()
	original := []byte(`{
		"input":[
			{"type":"function_call_output","output":[{"type":"text","text":"<workspace_result workspace_path=\"c:\\Users\\me\\repo\"> No_matches_found </workspace_result>"}]}
		]
	}`)
	var param any

	out := ConvertCodexResponseToOpenAIResponses(ctx, "gpt-5.5", original, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_rg","name":"rg"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected rg announcement chunk, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAIResponses(ctx, "gpt-5.5", original, nil, []byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"path\":\"src/components/ChatPanelNew\",\"pattern\":\"EventSource\",\"glob\":\"**/*.{ts,tsx,js,jsx}\"}"}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected rg argument delta to be suppressed, got %d chunks: %s", len(out), string(out[0]))
	}

	out = ConvertCodexResponseToOpenAIResponses(ctx, "gpt-5.5", original, nil, []byte(`data: {"type":"response.function_call_arguments.done","arguments":"{\"path\":\"src/components/ChatPanelNew\",\"pattern\":\"EventSource\",\"glob\":\"**/*.{ts,tsx,js,jsx}\"}"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected normalized rg arguments done, got %d", len(out))
	}
	args := gjson.GetBytes(ssePayload(out[0]), "arguments").String()
	if gjson.Get(args, "path").String() != `c:\Users\me\repo\src\components\ChatPanelNew` {
		t.Fatalf("path = %q, args %s, chunk %s", gjson.Get(args, "path").String(), args, string(out[0]))
	}
}

func TestConvertCodexResponseToOpenAIResponses_NormalizesOutputItemDoneArguments(t *testing.T) {
	ctx := context.Background()
	original := []byte(`{
		"input":[
			{"type":"function_call_output","output":[{"type":"text","text":"<workspace_result workspace_path=\"c:\\Users\\me\\repo\"> No_matches_found </workspace_result>"}]}
		]
	}`)
	var param any

	out := ConvertCodexResponseToOpenAIResponses(ctx, "gpt-5.5", original, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_read","name":"ReadFile"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected ReadFile announcement chunk, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAIResponses(ctx, "gpt-5.5", original, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_read","name":"ReadFile","arguments":"{\"path\":\"src/components/ChatPanelNew.tsx\"}"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected normalized ReadFile output_item.done, got %d", len(out))
	}
	args := gjson.GetBytes(ssePayload(out[0]), "item.arguments").String()
	if gjson.Get(args, "path").String() != `c:\Users\me\repo\src\components\ChatPanelNew.tsx` {
		t.Fatalf("path = %q, args %s, chunk %s", gjson.Get(args, "path").String(), args, string(out[0]))
	}
}

func ssePayload(chunk []byte) []byte {
	return bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(chunk), []byte("data:")))
}
