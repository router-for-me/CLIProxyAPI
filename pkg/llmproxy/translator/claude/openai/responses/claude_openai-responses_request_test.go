package responses

import (
<<<<<<< HEAD:pkg/llmproxy/translator/claude/openai/responses/claude_openai-responses_request_test.go
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToClaude(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4o",
		"instructions": "Be helpful.",
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "input_text", "text": "hello"}
				]
			}
		],
		"max_output_tokens": 100
	}`)

	got := ConvertOpenAIResponsesRequestToClaude("claude-3-5-sonnet", input, true)
	res := gjson.ParseBytes(got)

	if res.Get("model").String() != "claude-3-5-sonnet" {
		t.Errorf("expected model claude-3-5-sonnet, got %s", res.Get("model").String())
	}

	if res.Get("max_tokens").Int() != 100 {
		t.Errorf("expected max_tokens 100, got %d", res.Get("max_tokens").Int())
	}

	messages := res.Get("messages").Array()
	if len(messages) < 1 {
		t.Errorf("expected at least 1 message, got %d", len(messages))
	}
}

func TestConvertOpenAIResponsesRequestToClaudeToolChoice(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-5-sonnet",
		"input": [{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],
		"tool_choice": "required",
		"tools": [{
			"type": "function",
			"name": "weather",
			"description": "Get weather",
			"parameters": {"type":"object","properties":{"city":{"type":"string"}}}
		}]
	}`)

	got := ConvertOpenAIResponsesRequestToClaude("claude-3-5-sonnet", input, false)
	res := gjson.ParseBytes(got)

	if res.Get("tool_choice.type").String() != "any" {
		t.Fatalf("tool_choice.type = %s, want any", res.Get("tool_choice.type").String())
	}

	if res.Get("max_tokens").Int() != 32000 {
		t.Fatalf("expected default max_tokens to remain, got %d", res.Get("max_tokens").Int())
	}
}

func TestConvertOpenAIResponsesRequestToClaudeFunctionCallOutput(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-5-sonnet",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"function_call","call_id":"call-1","name":"weather","arguments":"{\"city\":\"sf\"}"},
			{"type":"function_call_output","call_id":"call-1","output":"\"cloudy\""}
		]
	}`)

	got := ConvertOpenAIResponsesRequestToClaude("claude-3-5-sonnet", input, false)
	res := gjson.ParseBytes(got)

	messages := res.Get("messages").Array()
	if len(messages) < 3 {
		t.Fatalf("expected at least 3 messages, got %d", len(messages))
	}

	last := messages[len(messages)-1]
	if last.Get("role").String() != "user" {
		t.Fatalf("last message role = %s, want user", last.Get("role").String())
	}
	if last.Get("content.0.type").String() != "tool_result" {
		t.Fatalf("last content type = %s, want tool_result", last.Get("content.0.type").String())
	}
}

func TestConvertOpenAIResponsesRequestToClaudeStringInputBody(t *testing.T) {
	input := []byte(`{"model":"claude-3-5-sonnet","input":"hello"}`)
	got := ConvertOpenAIResponsesRequestToClaude("claude-3-5-sonnet", input, false)
	res := gjson.ParseBytes(got)

	messages := res.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}
	if messages[0].Get("role").String() != "user" {
		t.Fatalf("message role = %s, want user", messages[0].Get("role").String())
	}
	if messages[0].Get("content").String() != "hello" {
		t.Fatalf("message content = %q, want hello", messages[0].Get("content").String())
	}
}

func TestConvertOpenAIResponsesRequestToClaude_PreservesReasoningBeforeToolUse(t *testing.T) {
	input := []byte(`{
		"model": "claude-opus-4-6-thinking",
		"input": [
			{
				"type":"reasoning",
				"summary":[{"type":"summary_text","text":"I should call weather tool"}]
			},
			{
				"type":"function_call",
				"call_id":"call-1",
				"name":"weather",
				"arguments":"{\"city\":\"sf\"}"
			}
		]
	}`)

	got := ConvertOpenAIResponsesRequestToClaude("claude-opus-4-6-thinking", input, false)
	res := gjson.ParseBytes(got)

	messages := res.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}

	content := messages[0].Get("content").Array()
	if len(content) != 2 {
		t.Fatalf("assistant content len = %d, want 2", len(content))
	}
	if content[0].Get("type").String() != "redacted_thinking" {
		t.Fatalf("first content type = %s, want redacted_thinking", content[0].Get("type").String())
	}
	if content[0].Get("data").String() != "I should call weather tool" {
		t.Fatalf("redacted_thinking data = %q", content[0].Get("data").String())
	}
	if content[1].Get("type").String() != "tool_use" {
		t.Fatalf("second content type = %s, want tool_use", content[1].Get("type").String())
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SanitizesThinkingSignature(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-6",
		"input":[
			{
				"type":"message",
				"role":"assistant",
				"content":[
					{"type":"thinking","thinking":"prior provider reasoning","signature":"invalid-signature"},
					{"type":"output_text","text":"tool call next"}
				]
=======
	"encoding/base64"
	"testing"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestConvertOpenAIResponsesRequestToClaude_SanitizesToolCallIDsForClaude(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"input": [
			{
				"type": "function_call",
				"call_id": "call.with space:1",
				"name": "Read",
				"arguments": "{\"path\":\"README.md\"}"
			},
			{
				"type": "function_call_output",
				"call_id": "call.with space:1",
				"output": "ok"
			}
		]
	}`

	result := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	toolUseID := resultJSON.Get("messages.0.content.0.id").String()
	toolResultID := resultJSON.Get("messages.1.content.0.tool_use_id").String()

	if toolUseID != "call_with_space_1" {
		t.Fatalf("tool_use id = %q, want %q", toolUseID, "call_with_space_1")
	}
	if toolResultID != toolUseID {
		t.Fatalf("tool_result tool_use_id = %q, want same sanitized id %q", toolResultID, toolUseID)
	}
}

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
>>>>>>> upstream/main:internal/translator/claude/openai/responses/claude_openai-responses_request_test.go
			}
		]
	}`)

<<<<<<< HEAD:pkg/llmproxy/translator/claude/openai/responses/claude_openai-responses_request_test.go
	got := ConvertOpenAIResponsesRequestToClaude("claude-opus-4-6", input, false)
	res := gjson.ParseBytes(got)

	first := res.Get("messages.0.content.0")
	if first.Get("type").String() != "redacted_thinking" {
		t.Fatalf("first content type = %s, want redacted_thinking", first.Get("type").String())
	}
	if first.Get("data").String() != "prior provider reasoning" {
		t.Fatalf("redacted thinking data = %q", first.Get("data").String())
	}
	if first.Get("signature").Exists() {
		t.Fatal("redacted_thinking must not carry signature")
	}
}
=======
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

func TestConvertOpenAIResponsesRequestToClaude_KeepsToolUseAdjacentToToolResult(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"function_call",
				"call_id":"call_00_awGuheXs4aRbtedNK8LE3743",
				"name":"js",
				"arguments":"{\"code\":\"nodeRepl.write('ok')\",\"title\":\"List Obsidian vault contents\"}"
			},
			{
				"type":"message",
				"role":"assistant",
				"content":[{"type":"output_text","text":"I'll check your Obsidian vault for articles."}]
			},
			{
				"type":"function_call_output",
				"call_id":"call_00_awGuheXs4aRbtedNK8LE3743",
				"output":"Wall time: 0.1963 seconds\nOutput:\n[{\"type\":\"text\",\"text\":\"\"}]"
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	if got := root.Get("messages.0.role").String(); got != "assistant" {
		t.Fatalf("first message role = %q, want assistant. Output: %s", got, string(out))
	}
	if got := root.Get("messages.0.content").String(); got != "I'll check your Obsidian vault for articles." {
		t.Fatalf("first message content = %q, want assistant text. Output: %s", got, string(out))
	}
	if got := root.Get("messages.1.content.0.type").String(); got != "tool_use" {
		t.Fatalf("second message first content type = %q, want tool_use. Output: %s", got, string(out))
	}
	if got := root.Get("messages.1.content.0.id").String(); got != "call_00_awGuheXs4aRbtedNK8LE3743" {
		t.Fatalf("tool_use id = %q, want call_00_awGuheXs4aRbtedNK8LE3743. Output: %s", got, string(out))
	}
	if got := root.Get("messages.2.content.0.type").String(); got != "tool_result" {
		t.Fatalf("third message first content type = %q, want tool_result. Output: %s", got, string(out))
	}
	if got := root.Get("messages.2.content.0.tool_use_id").String(); got != "call_00_awGuheXs4aRbtedNK8LE3743" {
		t.Fatalf("tool_result id = %q, want call_00_awGuheXs4aRbtedNK8LE3743. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_DropsApplyPatchCustomTool(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"tools":[
			{
				"type":"custom",
				"name":"apply_patch",
				"description":"Use the apply_patch tool to edit files.",
				"format":{"type":"grammar","syntax":"lark","definition":"start: patch"}
			},
			{
				"type":"function",
				"name":"exec_command",
				"description":"Runs a command.",
				"parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	if got := root.Get("tools.#").Int(); got != 1 {
		t.Fatalf("tools count = %d, want 1. Output: %s", got, string(out))
	}
	if got := root.Get("tools.0.name").String(); got != "exec_command" {
		t.Fatalf("tools.0.name = %q, want exec_command. Output: %s", got, string(out))
	}
	if got := root.Get("tools.#(name==\"apply_patch\")").Raw; got != "" {
		t.Fatalf("apply_patch custom tool should be dropped. Output: %s", string(out))
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
>>>>>>> upstream/main:internal/translator/claude/openai/responses/claude_openai-responses_request_test.go
