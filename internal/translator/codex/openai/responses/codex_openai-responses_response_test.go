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
