package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertInteractionsRequestToOpenAIPreservesExpressibleFields(t *testing.T) {
	out := ConvertInteractionsRequestToOpenAI("gpt-test", []byte(`{"model":"gpt-test","tool_choice":{"type":"function","function":{"name":"lookup"}},"response_modalities":["text","image"],"service_tier":"priority","input":"hi"}`), false)
	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice.function.name").String(); got != "lookup" {
		t.Fatalf("tool_choice.function.name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "modalities.0").String(); got != "text" {
		t.Fatalf("modalities.0 = %q, want text. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "modalities.1").String(); got != "image" {
		t.Fatalf("modalities.1 = %q, want image. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want priority. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIRequestToInteractionsMapsMessagesToolsAndStream(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.1-flash-lite","stream":true,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"今天北京的天气怎么样？"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"weather","parameters":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}}],"tool_choice":"auto","max_completion_tokens":128}`)
	out := ConvertOpenAIRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	if got := gjson.GetBytes(out, "model").String(); got != "gemini-3.1-flash-lite" {
		t.Fatalf("model = %q, want gemini-3.1-flash-lite. Output: %s", got, string(out))
	}
	if !gjson.GetBytes(out, "stream").Bool() {
		t.Fatalf("stream should be true. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "system_instruction").String(); got != "be brief" {
		t.Fatalf("system_instruction = %q, want be brief. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "user_input" {
		t.Fatalf("input.0.type = %q, want user_input. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != "今天北京的天气怎么样？" {
		t.Fatalf("input text = %q. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools.0.type = %q, want function. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "get_weather" {
		t.Fatalf("tool name = %q, want get_weather. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.parameters.properties.location.type").String(); got != "string" {
		t.Fatalf("tool schema missing. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "generation_config.tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want auto. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "generation_config.max_output_tokens").Int(); got != 128 {
		t.Fatalf("max_output_tokens = %d, want 128. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIRequestToInteractionsMapsToolCallsAndResults(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.1-flash-lite","messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`)
	out := ConvertOpenAIRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "function_call" {
		t.Fatalf("input.0.type = %q, want function_call. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.call_id").String(); got != "call_1" {
		t.Fatalf("call_id = %q, want call_1. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.arguments.q").String(); got != "x" {
		t.Fatalf("arguments.q = %q, want x. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.type").String(); got != "function_result" {
		t.Fatalf("input.1.type = %q, want function_result. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.result").String(); got != "ok" {
		t.Fatalf("result = %q, want ok. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToOpenAIAcceptsImageContent(t *testing.T) {
	out := ConvertInteractionsRequestToOpenAI("gpt-test", []byte(`{"model":"gpt-test","input":[{"type":"user_input","content":[{"type":"image","mime_type":"image/png","data":"aGVsbG8="}]}]}`), false)
	if got := gjson.GetBytes(out, "messages.0.content.0.type").String(); got != "image_url" {
		t.Fatalf("content type = %q, want image_url. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.image_url.url").String(); got != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image url = %q, want data:image/png;base64,aGVsbG8=. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToOpenAIPreservesNonImageMediaContent(t *testing.T) {
	out := ConvertInteractionsRequestToOpenAI("gpt-test", []byte(`{"model":"gpt-test","input":[{"type":"user_input","content":[{"type":"audio","mime_type":"audio/wav","data":"UklGRg=="},{"type":"video","mime_type":"video/mp4","data":"AAAAIGZ0eXA="},{"type":"document","mime_type":"application/pdf","data":"JVBERi0="}]}]}`), false)

	if got := gjson.GetBytes(out, "messages.0.content.0.type").String(); got != "input_audio" {
		t.Fatalf("audio content type = %q, want input_audio. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.input_audio.format").String(); got != "wav" {
		t.Fatalf("audio format = %q, want wav. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.type").String(); got != "video_url" {
		t.Fatalf("video content type = %q, want video_url. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.2.type").String(); got != "file" {
		t.Fatalf("document content type = %q, want file. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToOpenAIWithToolMessagesDirect(t *testing.T) {
	out := ConvertInteractionsRequestToOpenAI("gpt-test", []byte(`{"model":"gpt-test","input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]},{"type":"function_call","name":"lookup","call_id":"call_1","arguments":{"q":"x"}},{"type":"function_result","name":"lookup","call_id":"call_1","result":{"ok":true}}]}`), false)
	if got := gjson.GetBytes(out, "messages.1.tool_calls.0.function.name").String(); got != "lookup" {
		t.Fatalf("tool call name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.1.tool_calls.0.function.arguments").String(); got != `{"q":"x"}` {
		t.Fatalf("tool call arguments = %q, want JSON object string. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.2.tool_call_id").String(); got != "call_1" {
		t.Fatalf("tool_call_id = %q, want call_1. Output: %s", got, string(out))
	}
}
