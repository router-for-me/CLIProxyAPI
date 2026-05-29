package helps

import (
	"testing"

	responsesconverter "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestIsDeepSeekReasoningModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model string
		want  bool
	}{
		{model: "deepseek-v4-pro", want: true},
		{model: "deepseek-v4-flash", want: true},
		{model: "deepseek-v4-pro(8192)", want: true},
		{model: "deepseek-v4-pro(high)", want: true},
		{model: "deepseek-v4-pro(max)", want: true},
		{model: "deepseek-v3.1", want: false},
		{model: "gpt-5.4", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			t.Parallel()
			if got := IsDeepSeekReasoningModel(tt.model); got != tt.want {
				t.Fatalf("IsDeepSeekReasoningModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestPreserveDeepSeekReasoningContent_AttachesReasoningToToolCallAssistant(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run a tool"}]},
			{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"I need to inspect the file first."}]},
			{"type":"function_call","call_id":"call_read","name":"read","arguments":"{\"filePath\":\"README.md\"}"},
			{"type":"function_call_output","call_id":"call_read","output":"ok"}
		]
	}`)

	translated := responsesconverter.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro", raw, true)
	out := PreserveDeepSeekReasoningContent("deepseek-v4-pro", translated, raw)

	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "assistant" {
		t.Fatalf("messages.1.role = %q, want assistant; payload=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.1.reasoning_content").String(); got != "I need to inspect the file first." {
		t.Fatalf("messages.1.reasoning_content = %q", got)
	}
	if got := gjson.GetBytes(out, "messages.1.tool_calls.0.id").String(); got != "call_read" {
		t.Fatalf("messages.1.tool_calls.0.id = %q, want call_read", got)
	}
}

func TestPreserveDeepSeekReasoningContent_UsesAssistantOrdinal(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]},
			{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"The next step needs a tool."}]},
			{"type":"function_call","call_id":"call_shell","name":"shell","arguments":"{\"cmd\":\"pwd\"}"}
		]
	}`)

	translated := responsesconverter.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)
	out := PreserveDeepSeekReasoningContent("deepseek-v4-flash", translated, raw)

	if gjson.GetBytes(out, "messages.1.reasoning_content").Exists() {
		t.Fatalf("reasoning_content attached to the wrong assistant message: %s", out)
	}
	if got := gjson.GetBytes(out, "messages.2.reasoning_content").String(); got != "The next step needs a tool." {
		t.Fatalf("messages.2.reasoning_content = %q", got)
	}
}

func TestPreserveDeepSeekReasoningContent_PrefersFullContentOverSummary(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"input": [
			{"type":"reasoning","summary":[{"type":"summary_text","text":"short summary"}],"content":[{"type":"reasoning_text","text":"full reasoning trace"}]},
			{"type":"function_call","call_id":"call_1","name":"tool","arguments":"{}"}
		]
	}`)
	translated := responsesconverter.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro", raw, true)
	out := PreserveDeepSeekReasoningContent("deepseek-v4-pro", translated, raw)

	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "full reasoning trace" {
		t.Fatalf("messages.0.reasoning_content = %q, want full reasoning trace", got)
	}
}

func TestPreserveDeepSeekReasoningContent_SkipsOtherModels(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"input": [
			{"type":"reasoning","summary":[{"type":"summary_text","text":"hidden"}]},
			{"type":"function_call","call_id":"call_1","name":"tool","arguments":"{}"}
		]
	}`)
	translated := responsesconverter.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v3.1", raw, true)
	out := PreserveDeepSeekReasoningContent("deepseek-v3.1", translated, raw)

	if gjson.GetBytes(out, "messages.0.reasoning_content").Exists() {
		t.Fatalf("unexpected reasoning_content for non-v4 model: %s", out)
	}
}

func TestPreserveDeepSeekReasoningContent_DoesNotOverwriteExistingReasoning(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"input": [
			{"type":"reasoning","summary":[{"type":"summary_text","text":"new reasoning"}]},
			{"type":"function_call","call_id":"call_1","name":"tool","arguments":"{}"}
		]
	}`)
	translated := responsesconverter.ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro", raw, true)
	translated, _ = sjson.SetBytes(translated, "messages.0.reasoning_content", "existing reasoning")
	out := PreserveDeepSeekReasoningContent("deepseek-v4-pro", translated, raw)

	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "existing reasoning" {
		t.Fatalf("messages.0.reasoning_content = %q, want existing reasoning", got)
	}
}

func TestDeepSeekReasoningCache_RestoresStreamingToolCallReasoning(t *testing.T) {
	t.Parallel()

	recorder := NewDeepSeekReasoningRecorder("deepseek-v4-flash")
	recorder.RecordChatCompletionStreamLine([]byte(`data: {"choices":[{"delta":{"reasoning_content":"Need a tool."},"finish_reason":null}]}`))
	recorder.RecordChatCompletionStreamLine([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_cached","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":"tool_calls"}]}`))

	payload := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"assistant","tool_calls":[{"id":"call_cached","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_cached","content":"ok"}]}`)
	out := RestoreCachedDeepSeekReasoningContent("deepseek-v4-flash", payload)

	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "Need a tool." {
		t.Fatalf("messages.0.reasoning_content = %q, want cached reasoning; payload=%s", got, out)
	}
}

func TestDeepSeekReasoningCache_DoesNotOverwriteExistingReasoning(t *testing.T) {
	t.Parallel()

	recorder := NewDeepSeekReasoningRecorder("deepseek-v4-pro")
	recorder.RecordChatCompletionResponse([]byte(`{"choices":[{"message":{"role":"assistant","reasoning_content":"cached","tool_calls":[{"id":"call_existing","type":"function","function":{"name":"read","arguments":"{}"}}]}}]}`))

	payload := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"assistant","reasoning_content":"client reasoning","tool_calls":[{"id":"call_existing","type":"function","function":{"name":"read","arguments":"{}"}}]}]}`)
	out := RestoreCachedDeepSeekReasoningContent("deepseek-v4-pro", payload)

	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "client reasoning" {
		t.Fatalf("messages.0.reasoning_content = %q, want client reasoning", got)
	}
}
