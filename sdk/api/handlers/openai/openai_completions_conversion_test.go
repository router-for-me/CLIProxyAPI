package openai

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestConvertCompletionsRequestToChatCompletions_ArrayPrompt(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "single-element string array",
			body: `{"model":"gpt-4o-mini","prompt":["def fib(n):"]}`,
			want: "def fib(n):",
		},
		{
			name: "multi-element string array",
			body: `{"model":"gpt-4o-mini","prompt":["def fib(n):","    return n"]}`,
			want: "def fib(n):\n    return n",
		},
		{
			name: "array with empty entries ignored",
			body: `{"model":"gpt-4o-mini","prompt":["","hello",""]}`,
			want: "hello",
		},
		{
			name: "empty array falls back to default prompt",
			body: `{"model":"gpt-4o-mini","prompt":[]}`,
			want: "Complete this:",
		},
		{
			name: "null prompt falls back to default prompt",
			body: `{"model":"gpt-4o-mini","prompt":null}`,
			want: "Complete this:",
		},
		{
			name: "plain string prompt",
			body: `{"model":"gpt-4o-mini","prompt":"hello world"}`,
			want: "hello world",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			converted := convertCompletionsRequestToChatCompletions([]byte(tc.body))
			got := ojson.GetBytes(converted, "messages.0.content")
			if got.Type != ojson.String || got.String() != tc.want {
				t.Fatalf("messages.0.content = %q (type=%v), want %q; full=%s", got.String(), got.Type, tc.want, string(converted))
			}
		})
	}
}

func TestConvertCompletionsRequestToChatCompletions_NullOptionalParamsOmitted(t *testing.T) {
	body := `{
		"model": "gpt-4o-mini",
		"prompt": "hi",
		"max_tokens": null,
		"temperature": null,
		"top_p": null,
		"frequency_penalty": null,
		"presence_penalty": null,
		"stop": null,
		"stream": null,
		"logprobs": null,
		"top_logprobs": null,
		"echo": null
	}`

	converted := convertCompletionsRequestToChatCompletions([]byte(body))
	fields := []string{
		"max_tokens",
		"temperature",
		"top_p",
		"frequency_penalty",
		"presence_penalty",
		"stop",
		"stream",
		"logprobs",
		"top_logprobs",
		"echo",
	}
	for _, field := range fields {
		res := ojson.GetBytes(converted, field)
		if res.Exists() {
			t.Errorf("expected null optional field %q to be omitted, got raw=%s (type=%v); full=%s", field, res.Raw, res.Type, string(converted))
		}
	}
}

func TestConvertCompletionsRequestToChatCompletions_ValidOptionalParamsPreserved(t *testing.T) {
	body := `{
		"model": "gpt-4o-mini",
		"prompt": "hi",
		"max_tokens": 128,
		"temperature": 0,
		"top_p": 0.9,
		"frequency_penalty": 0.2,
		"presence_penalty": -0.1,
		"stop": ["\n", "END"],
		"stream": true,
		"logprobs": true,
		"top_logprobs": 3,
		"echo": false
	}`

	converted := convertCompletionsRequestToChatCompletions([]byte(body))
	if got := ojson.GetBytes(converted, "max_tokens"); got.Type != ojson.Number || got.Int() != 128 {
		t.Errorf("max_tokens = %v (%s), want 128", got.Type, got.Raw)
	}
	if got := ojson.GetBytes(converted, "temperature"); got.Type != ojson.Number || got.Float() != 0 {
		t.Errorf("temperature = %v (%s), want 0", got.Type, got.Raw)
	}
	if got := ojson.GetBytes(converted, "top_p"); got.Type != ojson.Number || got.Float() != 0.9 {
		t.Errorf("top_p = %v (%s), want 0.9", got.Type, got.Raw)
	}
	if got := ojson.GetBytes(converted, "frequency_penalty"); got.Type != ojson.Number || got.Float() != 0.2 {
		t.Errorf("frequency_penalty = %v (%s), want 0.2", got.Type, got.Raw)
	}
	if got := ojson.GetBytes(converted, "presence_penalty"); got.Type != ojson.Number || got.Float() != -0.1 {
		t.Errorf("presence_penalty = %v (%s), want -0.1", got.Type, got.Raw)
	}
	if got := ojson.GetBytes(converted, "stop"); !got.IsArray() || len(got.Array()) != 2 {
		t.Errorf("stop = %v (%s), want 2-element array", got.Type, got.Raw)
	}
	if got := ojson.GetBytes(converted, "stream"); got.Type != ojson.True {
		t.Errorf("stream = %v (%s), want true", got.Type, got.Raw)
	}
	if got := ojson.GetBytes(converted, "logprobs"); got.Type != ojson.True {
		t.Errorf("logprobs = %v (%s), want true", got.Type, got.Raw)
	}
	if got := ojson.GetBytes(converted, "top_logprobs"); got.Type != ojson.Number || got.Int() != 3 {
		t.Errorf("top_logprobs = %v (%s), want 3", got.Type, got.Raw)
	}
	if got := ojson.GetBytes(converted, "echo"); got.Type != ojson.False {
		t.Errorf("echo = %v (%s), want false", got.Type, got.Raw)
	}
}

func TestConvertCompletionsRequestToChatCompletions_IntegerLogprobs(t *testing.T) {
	t.Run("positive integer logprobs maps to logprobs true and top_logprobs", func(t *testing.T) {
		body := `{"model":"gpt-4o-mini","prompt":"hi","logprobs":3}`
		converted := convertCompletionsRequestToChatCompletions([]byte(body))
		if got := ojson.GetBytes(converted, "logprobs"); got.Type != ojson.True {
			t.Errorf("logprobs = %v (%s), want true; full=%s", got.Type, got.Raw, string(converted))
		}
		if got := ojson.GetBytes(converted, "top_logprobs"); got.Type != ojson.Number || got.Int() != 3 {
			t.Errorf("top_logprobs = %v (%s), want 3; full=%s", got.Type, got.Raw, string(converted))
		}
	})

	t.Run("explicit top_logprobs takes precedence over integer logprobs", func(t *testing.T) {
		body := `{"model":"gpt-4o-mini","prompt":"hi","logprobs":3,"top_logprobs":5}`
		converted := convertCompletionsRequestToChatCompletions([]byte(body))
		if got := ojson.GetBytes(converted, "logprobs"); got.Type != ojson.True {
			t.Errorf("logprobs = %v (%s), want true; full=%s", got.Type, got.Raw, string(converted))
		}
		if got := ojson.GetBytes(converted, "top_logprobs"); got.Type != ojson.Number || got.Int() != 5 {
			t.Errorf("top_logprobs = %v (%s), want 5; full=%s", got.Type, got.Raw, string(converted))
		}
	})

	t.Run("zero integer logprobs maps to logprobs false without top_logprobs", func(t *testing.T) {
		body := `{"model":"gpt-4o-mini","prompt":"hi","logprobs":0}`
		converted := convertCompletionsRequestToChatCompletions([]byte(body))
		if got := ojson.GetBytes(converted, "logprobs"); got.Type != ojson.False {
			t.Errorf("logprobs = %v (%s), want false; full=%s", got.Type, got.Raw, string(converted))
		}
		if got := ojson.GetBytes(converted, "top_logprobs"); got.Exists() {
			t.Errorf("expected top_logprobs to be omitted, got %s; full=%s", got.Raw, string(converted))
		}
	})
}

func TestConvertChatCompletionsStreamChunkToCompletions_NullFinishReasonAndUsage(t *testing.T) {
	t.Run("intermediate chunk with null finish_reason and null usage emits null finish_reason and omits usage", func(t *testing.T) {
		chunk := `{"id":"chatcmpl-1","created":1700000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}],"usage":null}`
		converted := convertChatCompletionsStreamChunkToCompletions([]byte(chunk))
		if converted == nil {
			t.Fatalf("expected non-nil converted chunk")
		}
		if got := ojson.GetBytes(converted, "choices.0.text"); got.Type != ojson.String || got.String() != "hello" {
			t.Errorf("choices.0.text = %q (type=%v), want %q", got.String(), got.Type, "hello")
		}
		fr := ojson.GetBytes(converted, "choices.0.finish_reason")
		if !fr.Exists() || fr.Type != ojson.Null || fr.Raw != "null" {
			t.Errorf("choices.0.finish_reason = raw=%q type=%v, want JSON null; full=%s", fr.Raw, fr.Type, string(converted))
		}
		if usage := ojson.GetBytes(converted, "usage"); usage.Exists() {
			t.Errorf("expected null usage to be omitted, got raw=%s; full=%s", usage.Raw, string(converted))
		}
	})

	t.Run("empty delta chunk with null usage and null finish_reason is filtered out", func(t *testing.T) {
		chunk := `{"id":"chatcmpl-1","created":1700000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}],"usage":null}`
		converted := convertChatCompletionsStreamChunkToCompletions([]byte(chunk))
		if converted != nil {
			t.Fatalf("expected empty delta chunk with usage:null to be filtered out (nil), got %s", string(converted))
		}
	})

	t.Run("final chunk with string finish_reason preserves finish_reason and omits null usage", func(t *testing.T) {
		chunk := `{"id":"chatcmpl-1","created":1700000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":null}`
		converted := convertChatCompletionsStreamChunkToCompletions([]byte(chunk))
		if converted == nil {
			t.Fatalf("expected non-nil final chunk")
		}
		fr := ojson.GetBytes(converted, "choices.0.finish_reason")
		if fr.Type != ojson.String || fr.String() != "stop" {
			t.Errorf("choices.0.finish_reason = raw=%q type=%v, want \"stop\"", fr.Raw, fr.Type)
		}
		if usage := ojson.GetBytes(converted, "usage"); usage.Exists() {
			t.Errorf("expected null usage to be omitted, got raw=%s", usage.Raw)
		}
	})

	t.Run("usage-only chunk with object usage is kept", func(t *testing.T) {
		chunk := `{"id":"chatcmpl-1","created":1700000000,"model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
		converted := convertChatCompletionsStreamChunkToCompletions([]byte(chunk))
		if converted == nil {
			t.Fatalf("expected non-nil usage chunk")
		}
		usage := ojson.GetBytes(converted, "usage")
		if !usage.IsObject() || usage.Get("total_tokens").Int() != 15 {
			t.Errorf("usage = raw=%q type=%v, want object with total_tokens=15", usage.Raw, usage.Type)
		}
	})
}

func TestConvertChatCompletionsResponseToCompletions_NullFinishReasonAndUsage(t *testing.T) {
	t.Run("null finish_reason emits JSON null and omits null usage", func(t *testing.T) {
		resp := `{"id":"chatcmpl-1","created":1700000000,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":null}],"usage":null}`
		converted := convertChatCompletionsResponseToCompletions([]byte(resp))
		fr := ojson.GetBytes(converted, "choices.0.finish_reason")
		if !fr.Exists() || fr.Type != ojson.Null || fr.Raw != "null" {
			t.Errorf("choices.0.finish_reason = raw=%q type=%v, want JSON null; full=%s", fr.Raw, fr.Type, string(converted))
		}
		if usage := ojson.GetBytes(converted, "usage"); usage.Exists() {
			t.Errorf("expected null usage to be omitted, got raw=%s; full=%s", usage.Raw, string(converted))
		}
	})

	t.Run("string finish_reason and object usage are preserved", func(t *testing.T) {
		resp := `{"id":"chatcmpl-1","created":1700000000,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
		converted := convertChatCompletionsResponseToCompletions([]byte(resp))
		fr := ojson.GetBytes(converted, "choices.0.finish_reason")
		if fr.Type != ojson.String || fr.String() != "stop" {
			t.Errorf("choices.0.finish_reason = raw=%q type=%v, want \"stop\"", fr.Raw, fr.Type)
		}
		usage := ojson.GetBytes(converted, "usage")
		if !usage.IsObject() || usage.Get("total_tokens").Int() != 4 {
			t.Errorf("usage = raw=%q type=%v, want object with total_tokens=4", usage.Raw, usage.Type)
		}
	})
}
