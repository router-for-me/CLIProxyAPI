package dto

import (
	"encoding/json"
	"testing"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeneralOpenAIRequestPreserveExplicitZeroValues(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-4.1",
		"stream":false,
		"max_tokens":0,
		"max_completion_tokens":0,
		"top_p":0,
		"top_k":0,
		"n":0,
		"frequency_penalty":0,
		"presence_penalty":0,
		"seed":0,
		"logprobs":false,
		"top_logprobs":0,
		"dimensions":0,
		"return_images":false,
		"return_related_questions":false
	}`)

	var req GeneralOpenAIRequest
	err := kitutil.Unmarshal(raw, &req)
	require.NoError(t, err)

	encoded, err := kitutil.Marshal(req)
	require.NoError(t, err)

	require.True(t, gjson.GetBytes(encoded, "stream").Exists())
	require.True(t, gjson.GetBytes(encoded, "max_tokens").Exists())
	require.True(t, gjson.GetBytes(encoded, "max_completion_tokens").Exists())
	require.True(t, gjson.GetBytes(encoded, "top_p").Exists())
	require.True(t, gjson.GetBytes(encoded, "top_k").Exists())
	require.True(t, gjson.GetBytes(encoded, "n").Exists())
	require.True(t, gjson.GetBytes(encoded, "frequency_penalty").Exists())
	require.True(t, gjson.GetBytes(encoded, "presence_penalty").Exists())
	require.True(t, gjson.GetBytes(encoded, "seed").Exists())
	require.True(t, gjson.GetBytes(encoded, "logprobs").Exists())
	require.True(t, gjson.GetBytes(encoded, "top_logprobs").Exists())
	require.True(t, gjson.GetBytes(encoded, "dimensions").Exists())
	require.True(t, gjson.GetBytes(encoded, "return_images").Exists())
	require.True(t, gjson.GetBytes(encoded, "return_related_questions").Exists())
}

func TestGeneralOpenAIRequestPreserveQwenThinkingBudget(t *testing.T) {
	raw := []byte(`{
		"model":"qwen-plus",
		"thinking_budget":0
	}`)

	var req GeneralOpenAIRequest
	err := kitutil.Unmarshal(raw, &req)
	require.NoError(t, err)

	encoded, err := kitutil.Marshal(req)
	require.NoError(t, err)

	value := gjson.GetBytes(encoded, "thinking_budget")
	assert.True(t, value.Exists())
	assert.Equal(t, int64(0), value.Int())
}

func TestGeneralOpenAIRequestPreserveQwQThinkingBudget(t *testing.T) {
	req := GeneralOpenAIRequest{
		Model:          "QwQ-32B",
		ThinkingBudget: json.RawMessage(`128`),
	}

	encoded, err := kitutil.Marshal(req)
	require.NoError(t, err)

	value := gjson.GetBytes(encoded, "thinking_budget")
	assert.True(t, value.Exists())
	assert.Equal(t, int64(128), value.Int())
}

func TestGeneralOpenAIRequestDropsThinkingBudgetForNonQwenModel(t *testing.T) {
	req := GeneralOpenAIRequest{
		Model:          "gpt-4.1",
		ThinkingBudget: json.RawMessage(`128`),
	}

	encoded, err := kitutil.Marshal(req)
	require.NoError(t, err)

	assert.False(t, gjson.GetBytes(encoded, "thinking_budget").Exists())
}

func TestIsQwenThinkingBudgetModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "qwen-plus", want: true},
		{model: "Qwen/Qwen3-235B-A22B-Thinking-2507", want: true},
		{model: "qwq-32b", want: true},
		{model: "provider/qwen-plus", want: true},
		{model: "provider/qwq-32b", want: true},
		{model: "gpt-4.1", want: false},
		{model: "deepseek-r1", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, IsQwenThinkingBudgetModel(tt.model))
		})
	}
}

func TestOpenAIResponsesRequestPreserveExplicitZeroValues(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-4.1",
		"max_output_tokens":0,
		"max_tool_calls":0,
		"stream":false,
		"top_p":0,
		"frequency_penalty":0,
		"presence_penalty":0
	}`)

	var req OpenAIResponsesRequest
	err := kitutil.Unmarshal(raw, &req)
	require.NoError(t, err)

	encoded, err := kitutil.Marshal(req)
	require.NoError(t, err)

	require.True(t, gjson.GetBytes(encoded, "max_output_tokens").Exists())
	require.True(t, gjson.GetBytes(encoded, "max_tool_calls").Exists())
	require.True(t, gjson.GetBytes(encoded, "stream").Exists())
	require.True(t, gjson.GetBytes(encoded, "top_p").Exists())
	require.True(t, gjson.GetBytes(encoded, "frequency_penalty").Exists())
	require.True(t, gjson.GetBytes(encoded, "presence_penalty").Exists())
}

func TestOpenAIResponsesRequestPreserveQwenThinkingBudget(t *testing.T) {
	req := OpenAIResponsesRequest{
		Model:          "qwen-plus",
		ThinkingBudget: json.RawMessage(`0`),
	}

	encoded, err := kitutil.Marshal(req)
	require.NoError(t, err)

	value := gjson.GetBytes(encoded, "thinking_budget")
	assert.True(t, value.Exists())
	assert.Equal(t, int64(0), value.Int())
}

func TestOpenAIResponsesRequestPreserveQwQThinkingBudget(t *testing.T) {
	req := OpenAIResponsesRequest{
		Model:          "provider/QwQ-32B",
		ThinkingBudget: json.RawMessage(`128`),
	}

	encoded, err := kitutil.Marshal(req)
	require.NoError(t, err)

	value := gjson.GetBytes(encoded, "thinking_budget")
	assert.True(t, value.Exists())
	assert.Equal(t, int64(128), value.Int())
}

func TestOpenAIResponsesRequestDropsThinkingBudgetForNonQwenModel(t *testing.T) {
	req := OpenAIResponsesRequest{
		Model:          "gpt-4.1",
		ThinkingBudget: json.RawMessage(`128`),
	}

	encoded, err := kitutil.Marshal(req)
	require.NoError(t, err)

	assert.False(t, gjson.GetBytes(encoded, "thinking_budget").Exists())
}

func TestGeneralOpenAIRequestGetSystemRoleName(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{name: "o1 uses developer", model: "o1", want: "developer"},
		{name: "o3 family uses developer", model: "o3-mini-high", want: "developer"},
		{name: "o4 family uses developer", model: "o4-mini", want: "developer"},
		{name: "o1 mini stays system", model: "o1-mini", want: "system"},
		{name: "o1 preview stays system", model: "o1-preview", want: "system"},
		{name: "gpt 5 uses developer", model: "gpt-5", want: "developer"},
		{name: "gpt 5.6 uses developer", model: "gpt-5.6-luna", want: "developer"},
		{name: "gpt 6 uses developer", model: "gpt-6-astra", want: "developer"},
		{name: "gpt 6 snapshot uses developer", model: "gpt-6-astra-2026-09-03", want: "developer"},
		{name: "unknown gpt 6 variant stays system", model: "gpt-6-astra-pro", want: "system"},
		{name: "invalid gpt 6 snapshot stays system", model: "gpt-6-astra-2026-99-03", want: "system"},
		{name: "unknown generation stays system", model: "gpt-7", want: "system"},
		{name: "gpt 4.1 stays system", model: "gpt-4.1-nano", want: "system"},
		{name: "omni is not o series", model: "omni-moderation-latest", want: "system"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := GeneralOpenAIRequest{Model: tt.model}

			assert.Equal(t, tt.want, req.GetSystemRoleName())
		})
	}
}

func TestIsOpenAIGPT5Model(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "gpt-5", want: true},
		{model: "gpt-5-mini", want: true},
		{model: "gpt-5-chat-latest", want: true},
		{model: "gpt-5.6-luna", want: true},
		{model: "gpt-5.4-nano", want: true},
		{model: "gpt-5.2-2025-12-11", want: true},
		{model: "gpt-6-astra", want: false},
		{model: "gpt-50", want: false},
		{model: "gpt-5custom", want: false},
		{model: " GPT-5 ", want: false},
		{model: "gpt-4.1", want: false},
		{model: "gpt-4.1-nano", want: false},
		{model: "gpt-4o", want: false},
		{model: "gpt-4.5-preview", want: false},
		{model: "gpt-oss-120b", want: false},
		{model: "gpt-image-2", want: false},
		{model: "gpt-realtime-2.1", want: false},
		{model: "chatgpt-4o-latest", want: false},
		{model: "o3-mini", want: false},
		{model: "gpt-", want: false},
		{model: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, IsOpenAIGPT5Model(tt.model))
		})
	}
}

func TestGeneralOpenAIRequestPreserveMessageLevelTools(t *testing.T) {
	raw := []byte(`{
		"model":"kimi-k3",
		"tool_choice":"required",
		"tools":[{"type":"function","function":{"name":"get_weather","description":"Get the weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}],
		"messages":[
			{"role":"system","content":"You are Kimi."},
			{"role":"user","content":"What time is it in Beijing?"},
			{"role":"system","tools":[{"type":"function","function":{"name":"get_current_time","description":"Get the current time of a city","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_current_time","arguments":"{\"city\":\"Beijing\"}"}}]},
			{"role":"system","content":"","tools":[{"type":"function","function":{"name":"lookup_order","parameters":{"type":"object"}}}]}
		]
	}`)

	var req GeneralOpenAIRequest
	require.NoError(t, kitutil.Unmarshal(raw, &req))
	require.Len(t, req.Messages, 5)

	encoded, err := kitutil.Marshal(req)
	require.NoError(t, err)

	messages := gjson.GetBytes(encoded, "messages").Array()
	require.Len(t, messages, 5)
	assert.Equal(t, "required", gjson.GetBytes(encoded, "tool_choice").String())
	assert.JSONEq(t, gjson.GetBytes(raw, "tools").Raw, gjson.GetBytes(encoded, "tools").Raw)

	// Regular messages keep their content untouched.
	assert.Equal(t, "You are Kimi.", messages[0].Get("content").String())
	assert.False(t, messages[0].Get("tools").Exists())
	assert.Equal(t, "What time is it in Beijing?", messages[1].Get("content").String())

	// Kimi K3 dynamic tool loading message: tools preserved byte-for-byte, no content key at all.
	assert.JSONEq(t, gjson.GetBytes(raw, "messages.2.tools").Raw, messages[2].Get("tools").Raw)
	assert.False(t, messages[2].Get("content").Exists())
	assert.Equal(t, "system", messages[2].Get("role").String())

	// Assistant tool call replay still emits an explicit "content": null.
	assistantContent := messages[3].Get("content")
	assert.True(t, assistantContent.Exists())
	assert.Equal(t, gjson.Null, assistantContent.Type)
	assert.JSONEq(t, gjson.GetBytes(raw, "messages.3.tool_calls").Raw, messages[3].Get("tool_calls").Raw)

	// Explicit empty content next to tools is forwarded as-is for the upstream to judge.
	emptyContent := messages[4].Get("content")
	assert.True(t, emptyContent.Exists())
	assert.Equal(t, gjson.String, emptyContent.Type)
	assert.Equal(t, "", emptyContent.String())
	assert.JSONEq(t, gjson.GetBytes(raw, "messages.4.tools").Raw, messages[4].Get("tools").Raw)

	// Token estimation sees message-level tools alongside the top-level ones.
	meta := req.GetTokenCountMeta()
	assert.Equal(t, 3, meta.ToolsCount)
	assert.Equal(t, 5, meta.MessagesCount)
	assert.Contains(t, meta.CombineText, "get_weather")
	assert.Contains(t, meta.CombineText, "get_current_time")
	assert.Contains(t, meta.CombineText, "Get the current time of a city")
	assert.Contains(t, meta.CombineText, "lookup_order")
}

func TestSGLangRequestExtensionsRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		request any
	}{
		{"chat", `{"model":"served-model","min_tokens":0,"top_k":-1,"min_p":0,"repetition_penalty":1,"separate_reasoning":false,"stream_reasoning":false,"regex":"[0-9]+","ebnf":"root ::= \"yes\"","stop_token_ids":[0],"stop_regex":["END$"],"no_stop_trim":false,"ignore_eos":false,"skip_special_tokens":false,"continue_final_message":false,"cache_salt":"tenant","chat_template_kwargs":{"enable_thinking":false}}`, &GeneralOpenAIRequest{}},
		{"responses", `{"model":"served-model","input":"hi","top_k":-1,"min_p":0,"repetition_penalty":1,"frequency_penalty":0,"presence_penalty":0,"stop":["END"],"cache_salt":"tenant","chat_template_kwargs":{"enable_thinking":false}}`, &OpenAIResponsesRequest{}},
		{"chat omitted", `{"model":"served-model"}`, &GeneralOpenAIRequest{}},
		{"responses omitted", `{"model":"served-model"}`, &OpenAIResponsesRequest{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, kitutil.Unmarshal([]byte(tc.raw), tc.request))
			encoded, err := kitutil.Marshal(tc.request)
			require.NoError(t, err)
			assert.JSONEq(t, tc.raw, string(encoded))
		})
	}
}
