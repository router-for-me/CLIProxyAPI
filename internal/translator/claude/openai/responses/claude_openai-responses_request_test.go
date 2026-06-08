package responses

import (
	"encoding/base64"
	"testing"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestConvertOpenAIResponsesRequestToClaude_ReasoningItemToThinkingBlock(t *testing.T) {
	rawSignature, expectedSignature := testClaudeResponsesThinkingSignature(t)
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + rawSignature + `",
				"summary":[{"type":"summary_text","text":"internal reasoning"}]
			},
			{
				"type":"message",
				"role":"assistant",
				"content":[{"type":"output_text","text":"visible answer"}]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	assistant := root.Get("messages.0")
	if got := assistant.Get("role").String(); got != "assistant" {
		t.Fatalf("first message role = %q, want assistant. Output: %s", got, string(out))
	}
	if got := assistant.Get("content.0.type").String(); got != "thinking" {
		t.Fatalf("first content type = %q, want thinking. Output: %s", got, string(out))
	}
	if got := assistant.Get("content.0.signature").String(); got != expectedSignature {
		t.Fatalf("thinking signature = %q, want %q", got, expectedSignature)
	}
	if got := assistant.Get("content.0.thinking").String(); got != "internal reasoning" {
		t.Fatalf("thinking text = %q, want internal reasoning", got)
	}
	if got := assistant.Get("content.1.type").String(); got != "text" {
		t.Fatalf("second content type = %q, want text. Output: %s", got, string(out))
	}
	if got := assistant.Get("content.1.text").String(); got != "visible answer" {
		t.Fatalf("assistant text = %q, want visible answer", got)
	}
	if got := root.Get("messages.1.role").String(); got != "user" {
		t.Fatalf("second message role = %q, want user. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SignatureOnlyReasoningFlushesBeforeUser(t *testing.T) {
	rawSignature, expectedSignature := testClaudeResponsesThinkingSignature(t)
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + rawSignature + `",
				"summary":[]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	thinking := root.Get("messages.0.content.0")
	if got := thinking.Get("type").String(); got != "thinking" {
		t.Fatalf("first content type = %q, want thinking. Output: %s", got, string(out))
	}
	if got := thinking.Get("signature").String(); got != expectedSignature {
		t.Fatalf("thinking signature = %q, want %q", got, expectedSignature)
	}
	if got := thinking.Get("thinking").String(); got != "" {
		t.Fatalf("thinking text = %q, want empty", got)
	}
	if got := root.Get("messages.1.role").String(); got != "user" {
		t.Fatalf("second message role = %q, want user. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_DropsIncompatibleReasoningSignature(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + testGPTResponsesReasoningSignature() + `",
				"summary":[{"type":"summary_text","text":"must not become Claude thinking"}]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)

	if gjson.GetBytes(out, "messages.0.content.0.type").String() == "thinking" {
		t.Fatalf("GPT encrypted_content should not become Claude thinking. Output: %s", string(out))
	}
	if gjson.GetBytes(out, "messages.0.content.0.signature").Exists() {
		t.Fatalf("incompatible signature should not be forwarded. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "user" {
		t.Fatalf("first message role = %q, want user. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ToolSearchGetsQuerySchema(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"search tools"}]}],
		"tools":[{"type":"tool_search"}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	tool := gjson.GetBytes(out, "tools.0")

	if got := tool.Get("name").String(); got != "ToolSearch" {
		t.Fatalf("tool_search name = %q, want ToolSearch. Output: %s", got, string(out))
	}
	if tool.Get("type").Exists() {
		t.Fatalf("tool_search should not leak raw Responses type. Output: %s", string(out))
	}
	if got := tool.Get("input_schema.type").String(); got != "object" {
		t.Fatalf("input_schema.type = %q, want object. Output: %s", got, string(out))
	}
	if got := tool.Get("input_schema.properties.query.type").String(); got != "string" {
		t.Fatalf("query.type = %q, want string. Output: %s", got, string(out))
	}
	if got := tool.Get("input_schema.required.0").String(); got != "query" {
		t.Fatalf("required.0 = %q, want query. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_NamedToolSearchPreservesNameAndSchema(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"search tools"}]}],
		"tools":[{"type":"tool_search","name":"CustomToolSearch"}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	tool := gjson.GetBytes(out, "tools.0")

	if got := tool.Get("name").String(); got != "CustomToolSearch" {
		t.Fatalf("tool_search name = %q, want CustomToolSearch. Output: %s", got, string(out))
	}
	if tool.Get("type").Exists() {
		t.Fatalf("tool_search should not leak raw Responses type. Output: %s", string(out))
	}
	if got := tool.Get("input_schema.properties.query.type").String(); got != "string" {
		t.Fatalf("query.type = %q, want string. Output: %s", got, string(out))
	}
	if got := tool.Get("input_schema.required.0").String(); got != "query" {
		t.Fatalf("required.0 = %q, want query. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ToolSearchPreservesDescription(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"search tools"}]}],
		"tools":[{"type":"tool_search","description":"Find deferred tools."}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)

	if got := gjson.GetBytes(out, "tools.0.description").String(); got != "Find deferred tools." {
		t.Fatalf("description = %q, want custom description. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ToolSearchToolChoice(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"search tools"}]}],
		"tools":[{"type":"tool_search"}],
		"tool_choice":{"type":"function","name":"ToolSearch"}
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)

	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "tool" {
		t.Fatalf("tool_choice.type = %q, want tool. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "ToolSearch" {
		t.Fatalf("tool_choice.name = %q, want ToolSearch. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ToolSearchReplayLoadsDiscoveredTaskTools(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"create a task"}]},
			{"id":"tsc_1","type":"tool_search_call","call_id":"call_ts_1","arguments":{"query":"task"}},
			{
				"id":"tso_1",
				"type":"tool_search_output",
				"call_id":"call_ts_1",
				"execution":"server",
				"status":"completed",
				"tools":[
					{"type":"function","name":"TaskCreate","description":"Create a task.","parameters":{"type":"object","properties":{"prompt":{"type":"string"}},"required":["prompt"]}},
					{"type":"function","name":"TaskGet","description":"Get a task.","parameters":{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"]}},
					{"type":"function","name":"TaskList","description":"List tasks.","parameters":{"type":"object","properties":{"status":{"type":"string"}}}},
					{"type":"function","name":"TaskOutput","description":"Read task output.","parameters":{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"]}},
					{"type":"function","name":"TaskStop","description":"Stop a task.","parameters":{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"]}},
					{"type":"function","name":"TaskUpdate","description":"Update a task.","parameters":{"type":"object","properties":{"task_id":{"type":"string"},"status":{"type":"string"}},"required":["task_id","status"]}}
				]
			}
		],
		"tools":[{"type":"tool_search"}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	toolUse := root.Get("messages.1.content.0")
	if got := toolUse.Get("type").String(); got != "tool_use" {
		t.Fatalf("tool_search_call content type = %q, want tool_use. Output: %s", got, string(out))
	}
	if got := toolUse.Get("name").String(); got != "ToolSearch" {
		t.Fatalf("tool_search_call tool name = %q, want ToolSearch. Output: %s", got, string(out))
	}
	if got := toolUse.Get("id").String(); got != "call_ts_1" {
		t.Fatalf("tool_search_call tool id = %q, want call_ts_1. Output: %s", got, string(out))
	}
	if got := toolUse.Get("input.query").String(); got != "task" {
		t.Fatalf("tool_search_call input.query = %q, want task. Output: %s", got, string(out))
	}

	toolResult := root.Get("messages.2.content.0")
	if got := toolResult.Get("type").String(); got != "tool_result" {
		t.Fatalf("tool_search_output content type = %q, want tool_result. Output: %s", got, string(out))
	}
	if got := toolResult.Get("tool_use_id").String(); got != "call_ts_1" {
		t.Fatalf("tool_search_output tool_use_id = %q, want call_ts_1. Output: %s", got, string(out))
	}

	requiredTaskTools := map[string]bool{
		"TaskCreate": false,
		"TaskGet":    false,
		"TaskList":   false,
		"TaskOutput": false,
		"TaskStop":   false,
		"TaskUpdate": false,
	}
	root.Get("tools").ForEach(func(_, tool gjson.Result) bool {
		if _, ok := requiredTaskTools[tool.Get("name").String()]; ok {
			requiredTaskTools[tool.Get("name").String()] = true
			if got := tool.Get("input_schema.type").String(); got != "object" {
				t.Fatalf("%s input_schema.type = %q, want object. Output: %s", tool.Get("name").String(), got, string(out))
			}
		}
		return true
	})
	for name, found := range requiredTaskTools {
		if !found {
			t.Fatalf("discovered tool %s was not loaded into Claude tools. Output: %s", name, string(out))
		}
	}
	if got := root.Get(`tools.#(name=="TaskCreate").input_schema.required.0`).String(); got != "prompt" {
		t.Fatalf("TaskCreate required.0 = %q, want prompt. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ToolSearchOutputResultsLoadsDiscoveredTools(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"tool_search_call","call_id":"call_ts_2","arguments":"task"},
			{"type":"tool_search_output","call_id":"call_ts_2","results":[{"tool":{"type":"function","name":"TaskOutput","parameters":{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"]}}}]}
		],
		"tools":[{"type":"tool_search","name":"CustomToolSearch"}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	if got := root.Get("messages.0.content.0.name").String(); got != "CustomToolSearch" {
		t.Fatalf("tool_search_call tool name = %q, want CustomToolSearch. Output: %s", got, string(out))
	}
	if got := root.Get("messages.0.content.0.input.query").String(); got != "task" {
		t.Fatalf("string arguments should become query, got %q. Output: %s", got, string(out))
	}
	if got := root.Get(`tools.#(name=="TaskOutput").input_schema.required.0`).String(); got != "task_id" {
		t.Fatalf("TaskOutput schema not loaded from results. required.0 = %q. Output: %s", got, string(out))
	}
}

func testClaudeResponsesThinkingSignature(t *testing.T) (string, string) {
	t.Helper()
	channelBlock := []byte{}
	channelBlock = protowire.AppendTag(channelBlock, 1, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 12)
	channelBlock = protowire.AppendTag(channelBlock, 2, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 2)
	channelBlock = protowire.AppendTag(channelBlock, 6, protowire.BytesType)
	channelBlock = protowire.AppendString(channelBlock, "claude-sonnet-4-6")

	container := []byte{}
	container = protowire.AppendTag(container, 1, protowire.BytesType)
	container = protowire.AppendBytes(container, channelBlock)

	payload := []byte{}
	payload = protowire.AppendTag(payload, 2, protowire.BytesType)
	payload = protowire.AppendBytes(payload, container)
	payload = protowire.AppendTag(payload, 3, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)

	rawSignature := base64.StdEncoding.EncodeToString(payload)
	normalized, ok := sigcompat.CompatibleSignatureForProvider(sigcompat.SignatureProviderClaude, rawSignature)
	if !ok {
		t.Fatal("test Claude signature should be compatible")
	}
	return rawSignature, normalized
}

func testGPTResponsesReasoningSignature() string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	payload[8] = 1
	for i := 9; i < len(payload); i++ {
		payload[i] = byte(i)
	}
	return base64.URLEncoding.EncodeToString(payload)
}
