package responses

import (
	"encoding/base64"
	"fmt"
	"strings"
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

func TestConvertOpenAIResponsesRequestToClaude_FableMaxTokens(t *testing.T) {
	t.Run("defaults to 64k", func(t *testing.T) {
		out := ConvertOpenAIResponsesRequestToClaude(
			"claude-fable-5-1",
			[]byte(`{"model":"claude-fable-5-1","input":"hello"}`),
			true,
		)
		if got := gjson.GetBytes(out, "max_tokens").Int(); got != 64000 {
			t.Fatalf("max_tokens = %d, want %d; output=%s", got, 64000, out)
		}
	})

	t.Run("preserves explicit 128k limit", func(t *testing.T) {
		out := ConvertOpenAIResponsesRequestToClaude(
			"claude-fable-5-1",
			[]byte(`{"model":"claude-fable-5-1","max_output_tokens":128000,"input":"hello"}`),
			true,
		)
		if got := gjson.GetBytes(out, "max_tokens").Int(); got != 128000 {
			t.Fatalf("max_tokens = %d, want 128000; output=%s", got, out)
		}
	})

	t.Run("does not exceed registered model maximum", func(t *testing.T) {
		out := ConvertOpenAIResponsesRequestToClaude(
			"claude-3-5-haiku-20241022",
			[]byte(`{"model":"claude-3-5-haiku-20241022","input":"hello"}`),
			true,
		)
		if got := gjson.GetBytes(out, "max_tokens").Int(); got != 8192 {
			t.Fatalf("max_tokens = %d, want 8192; output=%s", got, out)
		}
	})

	t.Run("clamps explicit limit exceeding registered model maximum", func(t *testing.T) {
		out := ConvertOpenAIResponsesRequestToClaude(
			"claude-3-5-haiku-20241022",
			[]byte(`{"model":"claude-3-5-haiku-20241022","max_output_tokens":128000,"input":"hello"}`),
			true,
		)
		if got := gjson.GetBytes(out, "max_tokens").Int(); got != 8192 {
			t.Fatalf("max_tokens = %d, want 8192; output=%s", got, out)
		}
	})

	t.Run("null max_output_tokens retains default 64k", func(t *testing.T) {
		out := ConvertOpenAIResponsesRequestToClaude(
			"claude-fable-5-1",
			[]byte(`{"model":"claude-fable-5-1","max_output_tokens":null,"input":"hello"}`),
			true,
		)
		if got := gjson.GetBytes(out, "max_tokens").Int(); got != 64000 {
			t.Fatalf("max_tokens = %d, want 64000; output=%s", got, out)
		}
	})
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

func TestConvertOpenAIResponsesRequestToClaude_RedactedReasoningItemRestoresRedactedThinking(t *testing.T) {
	const data = "EroBCkYIBRgCKkA"
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + ClaudeResponsesRedactedThinkingPrefix + data + `",
				"summary":[]
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

	block := root.Get("messages.0.content.0")
	if got := block.Get("type").String(); got != "redacted_thinking" {
		t.Fatalf("first content type = %q, want redacted_thinking. Output: %s", got, string(out))
	}
	if got := block.Get("data").String(); got != data {
		t.Fatalf("redacted_thinking data = %q, want %q", got, data)
	}
	if block.Get("signature").Exists() {
		t.Fatalf("redacted_thinking must not carry a signature. Output: %s", string(out))
	}
	if got := root.Get("messages.0.content.1.text").String(); got != "visible answer" {
		t.Fatalf("assistant text = %q, want visible answer. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_EmptyRedactedReasoningItemIsDropped(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + ClaudeResponsesRedactedThinkingPrefix + `",
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

	if got := root.Get("messages.#").Int(); got != 1 {
		t.Fatalf("message count = %d, want only the user turn. Output: %s", got, string(out))
	}
	if got := root.Get("messages.0.role").String(); got != "user" {
		t.Fatalf("first message role = %q, want user. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ReasoningContentTextRebuildsThinking(t *testing.T) {
	rawSignature, expectedSignature := testClaudeResponsesThinkingSignature(t)
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + rawSignature + `",
				"summary":[],
				"content":[{"type":"reasoning_text","text":"restored from content"}]
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
	if got := thinking.Get("thinking").String(); got != "restored from content" {
		t.Fatalf("thinking text = %q, want restored from content. Output: %s", got, string(out))
	}
	if got := thinking.Get("signature").String(); got != expectedSignature {
		t.Fatalf("thinking signature = %q, want %q", got, expectedSignature)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SummaryWinsOverDuplicatedReasoningContent(t *testing.T) {
	rawSignature, _ := testClaudeResponsesThinkingSignature(t)
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + rawSignature + `",
				"summary":[{"type":"summary_text","text":"chain of thought"}],
				"content":[{"type":"reasoning_text","text":"chain of thought"}]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	if got := gjson.ParseBytes(out).Get("messages.0.content.0.thinking").String(); got != "chain of thought" {
		t.Fatalf("thinking text = %q, want the summary text exactly once. Output: %s", got, string(out))
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

func TestConvertOpenAIResponsesRequestToClaude_GroupsAssistantAndToolResultTurns(t *testing.T) {
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
				"type":"function_call",
				"call_id":"call_first",
				"name":"read_file",
				"arguments":"{\"path\":\"first\"}"
			},
			{
				"type":"function_call",
				"call_id":"call_second",
				"name":"read_file",
				"arguments":"{\"path\":\"second\"}"
			},
			{
				"type":"function_call_output",
				"call_id":"call_first",
				"output":"first result"
			},
			{
				"type":"function_call_output",
				"call_id":"call_second",
				"output":"second result"
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)
	if got := root.Get("messages.#").Int(); got != 2 {
		t.Fatalf("message count = %d, want 2. Output: %s", got, string(out))
	}

	assistant := root.Get("messages.0")
	if got := assistant.Get("role").String(); got != "assistant" {
		t.Fatalf("first message role = %q, want assistant. Output: %s", got, string(out))
	}
	wantAssistantTypes := []string{"thinking", "text", "tool_use", "tool_use"}
	assistantContent := assistant.Get("content").Array()
	if len(assistantContent) != len(wantAssistantTypes) {
		t.Fatalf("assistant content count = %d, want %d. Output: %s", len(assistantContent), len(wantAssistantTypes), string(out))
	}
	for i, wantType := range wantAssistantTypes {
		if got := assistantContent[i].Get("type").String(); got != wantType {
			t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", i, got, wantType, string(out))
		}
	}
	if got := assistantContent[0].Get("signature").String(); got != expectedSignature {
		t.Fatalf("thinking signature = %q, want %q", got, expectedSignature)
	}
	if got := assistantContent[2].Get("id").String(); got != "call_first" {
		t.Fatalf("first tool_use id = %q, want call_first", got)
	}
	if got := assistantContent[3].Get("id").String(); got != "call_second" {
		t.Fatalf("second tool_use id = %q, want call_second", got)
	}

	user := root.Get("messages.1")
	if got := user.Get("role").String(); got != "user" {
		t.Fatalf("second message role = %q, want user. Output: %s", got, string(out))
	}
	userContent := user.Get("content").Array()
	if len(userContent) != 2 {
		t.Fatalf("user content count = %d, want 2. Output: %s", len(userContent), string(out))
	}
	for i, wantID := range []string{"call_first", "call_second"} {
		if got := userContent[i].Get("type").String(); got != "tool_result" {
			t.Fatalf("user content[%d].type = %q, want tool_result. Output: %s", i, got, string(out))
		}
		if got := userContent[i].Get("tool_use_id").String(); got != wantID {
			t.Fatalf("user content[%d].tool_use_id = %q, want %q", i, got, wantID)
		}
	}
}

func TestConvertOpenAIResponsesRequestToClaude_MergesConsecutiveUserMessagesAndPreservesCacheControl(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"message",
				"role":"user",
				"cache_control":{"type":"ephemeral"},
				"content":[{"type":"input_text","text":"first"}]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"second"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)
	if got := root.Get("messages.#").Int(); got != 1 {
		t.Fatalf("message count = %d, want 1. Output: %s", got, string(out))
	}
	content := root.Get("messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("content count = %d, want 2. Output: %s", len(content), string(out))
	}
	if got := content[0].Get("text").String(); got != "first" {
		t.Fatalf("content[0].text = %q, want first", got)
	}
	if got := content[0].Get("cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("content[0].cache_control.type = %q, want ephemeral", got)
	}
	if got := content[1].Get("text").String(); got != "second" {
		t.Fatalf("content[1].text = %q, want second", got)
	}
	if content[1].Get("cache_control").Exists() {
		t.Fatalf("content[1] should not have cache_control. Output: %s", string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_DoesNotMergeAcrossRoleChanges(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first assistant"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"user reply"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second assistant"}]}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)
	messages := root.Get("messages").Array()
	if len(messages) != 3 {
		t.Fatalf("message count = %d, want 3. Output: %s", len(messages), string(out))
	}
	for i, wantRole := range []string{"assistant", "user", "assistant"} {
		if got := messages[i].Get("role").String(); got != wantRole {
			t.Fatalf("messages[%d].role = %q, want %q", i, got, wantRole)
		}
	}
}

func TestConvertOpenAIResponsesRequestToClaude_EmptyStringContentDoesNotBreakAssistantTurn(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"message","role":"assistant","content":"first assistant"},
			{"type":"message","role":"user","content":""},
			{"type":"message","role":"assistant","content":"second assistant"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)
	messages := root.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1. Output: %s", len(messages), string(out))
	}
	if got := messages[0].Get("role").String(); got != "assistant" {
		t.Fatalf("message role = %q, want assistant. Output: %s", got, string(out))
	}
	content := messages[0].Get("content").Array()
	if len(content) != 2 {
		t.Fatalf("content count = %d, want 2. Output: %s", len(content), string(out))
	}
	for i, wantText := range []string{"first assistant", "second assistant"} {
		if got := content[i].Get("type").String(); got != "text" {
			t.Fatalf("content[%d].type = %q, want text. Output: %s", i, got, string(out))
		}
		if got := content[i].Get("text").String(); got != wantText {
			t.Fatalf("content[%d].text = %q, want %q. Output: %s", i, got, wantText, string(out))
		}
	}
}

func TestConvertOpenAIResponsesRequestToClaude_FunctionCallOutputPreservesInputImage(t *testing.T) {
	const imageB64 = "iVBORw0KGgo="
	dataURL := "data:image/png;base64," + imageB64
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"function_call",
				"call_id":"call_view_image_1",
				"name":"view_image",
				"arguments":"{}"
			},
			{
				"type":"function_call_output",
				"call_id":"call_view_image_1",
				"output":[
					{
						"type":"input_image",
						"image_url":"` + dataURL + `",
						"detail":"high"
					}
				]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	toolResult := root.Get("messages.1.content.0")
	if got := toolResult.Get("type").String(); got != "tool_result" {
		t.Fatalf("tool_result type = %q, want tool_result. Output: %s", got, string(out))
	}
	if got := toolResult.Get("content.0.type").String(); got != "image" {
		t.Fatalf("tool_result content block type = %q, want image. Output: %s", got, string(out))
	}
	if got := toolResult.Get("content.0.source.media_type").String(); got != "image/png" {
		t.Fatalf("image media_type = %q, want image/png. Output: %s", got, string(out))
	}
	if got := toolResult.Get("content.0.source.data").String(); got != imageB64 {
		t.Fatalf("image data = %q, want raw base64 without data URL prefix", got)
	}
	if strings.Contains(toolResult.Get("content").Raw, "data:image") {
		t.Fatalf("tool_result content must not embed data URL as text. Output: %s", string(out))
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

	if got := root.Get("messages.#").Int(); got != 2 {
		t.Fatalf("message count = %d, want 2. Output: %s", got, string(out))
	}
	if got := root.Get("messages.0.role").String(); got != "assistant" {
		t.Fatalf("first message role = %q, want assistant. Output: %s", got, string(out))
	}
	if got := root.Get("messages.0.content.0.text").String(); got != "I'll check your Obsidian vault for articles." {
		t.Fatalf("first assistant block text = %q. Output: %s", got, string(out))
	}
	if got := root.Get("messages.0.content.1.type").String(); got != "tool_use" {
		t.Fatalf("second assistant block type = %q, want tool_use. Output: %s", got, string(out))
	}
	if got := root.Get("messages.0.content.1.id").String(); got != "call_00_awGuheXs4aRbtedNK8LE3743" {
		t.Fatalf("tool_use id = %q, want call_00_awGuheXs4aRbtedNK8LE3743. Output: %s", got, string(out))
	}
	if got := root.Get("messages.1.content.0.type").String(); got != "tool_result" {
		t.Fatalf("user block type = %q, want tool_result. Output: %s", got, string(out))
	}
	if got := root.Get("messages.1.content.0.tool_use_id").String(); got != "call_00_awGuheXs4aRbtedNK8LE3743" {
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

func TestConvertOpenAIResponsesRequestToClaude_NormalizesRootToolSchemaUnion(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"tools":[{
			"type":"function",
			"name":"lookup",
			"parameters":{
				"type":"object",
				"properties":{"query":{"type":"string"},"id":{"type":"string"}},
				"oneOf":[{"required":["query"]},{"required":["id"]}]
			}
		}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	schema := gjson.GetBytes(out, "tools.0.input_schema")

	if got := schema.Get("type").String(); got != "object" {
		t.Fatalf("input_schema.type = %q, want object. Output: %s", got, string(out))
	}
	if schema.Get("oneOf").Exists() {
		t.Fatalf("input_schema should not contain root oneOf. Output: %s", string(out))
	}
	if !schema.Get("properties.query").Exists() || !schema.Get("properties.id").Exists() {
		t.Fatalf("input_schema should preserve query and id properties. Output: %s", string(out))
	}
	if schema.Get("required").Exists() {
		t.Fatalf("input_schema should not merge alternative required fields. Output: %s", string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_MergesAdditionalToolsAndPrefersTopLevel(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"tools":[
			{
				"type":"function",
				"name":"exec",
				"description":"top-level exec",
				"parameters":{"type":"object","properties":{"command":{"type":"string"}}}
			},
			{
				"type":"namespace",
				"name":"collaboration",
				"tools":[{"type":"function","name":"spawn","description":"top-level spawn","parameters":{"type":"object","properties":{}}}]
			}
		],
		"input":[
			{
				"type":"additional_tools",
				"role":"developer",
				"tools":[
					{"type":"custom","name":"exec","description":"additional exec"},
					{"type":"function","name":"wait","parameters":{"type":"object","properties":{}}},
					{"type":"namespace","name":"collaboration","tools":[
						{"type":"function","name":"spawn","parameters":{"type":"object","properties":{}}},
						{"type":"custom","name":"send","description":"send a message"}
					]}
				]
			},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
		]
	}`)

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	if got := root.Get("tools.#").Int(); got != 4 {
		t.Fatalf("tools count = %d, want 4; output=%s", got, root.Raw)
	}
	if got := root.Get(`tools.#(name=="exec").description`).String(); got != "top-level exec" {
		t.Fatalf("exec description = %q, want top-level exec", got)
	}
	if got := root.Get(`tools.#(name=="wait").name`).String(); got != "wait" {
		t.Fatalf("additional function name = %q, want wait", got)
	}
	if got := root.Get(`tools.#(name=="collaboration__spawn").name`).String(); got != "collaboration__spawn" {
		t.Fatalf("namespace function name = %q, want collaboration__spawn", got)
	}
	custom := root.Get(`tools.#(name=="collaboration__send")`)
	if !custom.Exists() {
		t.Fatal("missing namespace custom tool")
	}
	if got := custom.Get("input_schema.properties.input.type").String(); got != "string" {
		t.Fatalf("custom input schema type = %q, want string", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_DeduplicatesExpandedToolNames(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"tools":[{"type":"function","name":"collaboration__send","description":"top-level send","parameters":{"type":"object","properties":{}}}],
		"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"collaboration","tools":[
			{"type":"function","name":"send","description":"additional send","parameters":{"type":"object","properties":{}}},
			{"type":"function","name":"other","parameters":{"type":"object","properties":{}}}
		]}]}]
	}`)

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	if got := root.Get("tools.#").Int(); got != 2 {
		t.Fatalf("tools count = %d, want 2; output=%s", got, root.Raw)
	}
	if got := root.Get(`tools.#(name=="collaboration__send").description`).String(); got != "top-level send" {
		t.Fatalf("duplicate final name description = %q, want top-level send", got)
	}
	if !root.Get(`tools.#(name=="collaboration__other")`).Exists() {
		t.Fatal("unique namespace child was dropped")
	}
	customNames := responsesCustomToolNames(raw)
	if _, ok := customNames["collaboration__send"]; ok {
		t.Fatal("final-name collision should keep the top-level function type")
	}
	name, namespace := splitResponsesQualifiedFunctionCallFromRequest(raw, "collaboration__send")
	if name != "collaboration__send" || namespace != "" {
		t.Fatalf("final-name collision namespace = (%q, %q), want (collaboration__send, empty)", name, namespace)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_DirectToolWinsOverEarlierNamespaceCollision(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"tools":[
			{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","parameters":{"type":"object","properties":{}}}]},
			{"type":"custom","name":"n__x"}
		],
		"tool_choice":{"type":"custom","name":"n__x"}
	}`)

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	if got := root.Get("tools.#").Int(); got != 1 {
		t.Fatalf("tools count = %d, want 1; output=%s", got, root.Raw)
	}
	if got := root.Get("tools.0.name").String(); got != "n__x" {
		t.Fatalf("winning tool name = %q, want n__x", got)
	}
	if got := root.Get("tools.0.input_schema.properties.input.type").String(); got != "string" {
		t.Fatalf("winning tool schema type = %q, want string for custom tool", got)
	}
	if got := root.Get("tool_choice.name").String(); got != "n__x" {
		t.Fatalf("tool_choice.name = %q, want n__x; output=%s", got, root.Raw)
	}
	if _, ok := responsesCustomToolNames(raw)["n__x"]; !ok {
		t.Fatal("winning direct custom tool was not classified as custom")
	}
}

func TestConvertOpenAIResponsesRequestToClaude_PrefersDirectToolAcrossAdditionalSources(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"additional_tools","tools":[{"type":"namespace","name":"n","tools":[{"type":"function","name":"x","description":"namespace x","parameters":{"type":"object","properties":{}}}]}]},
			{"type":"additional_tools","tools":[{"type":"custom","name":"n__x","description":"direct x"}]}
		]
	}`)

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	if got := root.Get("tools.#").Int(); got != 1 {
		t.Fatalf("tools count = %d, want 1; output=%s", got, root.Raw)
	}
	tool := root.Get("tools.0")
	if got := tool.Get("name").String(); got != "n__x" {
		t.Fatalf("winning tool name = %q, want n__x", got)
	}
	if got := tool.Get("description").String(); got != "direct x" {
		t.Fatalf("winning tool description = %q, want direct x", got)
	}
	if got := tool.Get("input_schema.properties.input.type").String(); got != "string" {
		t.Fatalf("winning tool schema type = %q, want string for custom tool", got)
	}
	if _, ok := responsesCustomToolNames(raw)["n__x"]; !ok {
		t.Fatal("direct custom tool should win classification across additional sources")
	}
}

func TestConvertOpenAIResponsesRequestToClaude_PreservesToolDeclarationOrder(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"tools":[
			{"type":"function","name":"first","parameters":{"type":"object","properties":{}}},
			{"type":"namespace","name":"n","tools":[{"type":"function","name":"middle","parameters":{"type":"object","properties":{}}}]},
			{"type":"function","name":"last","parameters":{"type":"object","properties":{}}}
		]
	}`)

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	want := []string{"first", "n__middle", "last"}
	got := root.Get("tools.#.name").Array()
	if len(got) != len(want) {
		t.Fatalf("tools count = %d, want %d; output=%s", len(got), len(want), root.Raw)
	}
	for i, wantName := range want {
		if got[i].String() != wantName {
			t.Errorf("tools[%d].name = %q, want %q", i, got[i].String(), wantName)
		}
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ReplaysCustomToolCallHistory(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"custom_tool_call","call_id":"call.custom:1","name":"exec","input":"pwd"},
			{"type":"custom_tool_call_output","call_id":"call.custom:1","output":"/workspace"}
		]
	}`)

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	toolUse := root.Get("messages.0.content.0")
	if got := toolUse.Get("type").String(); got != "tool_use" {
		t.Fatalf("tool use type = %q, want tool_use; output=%s", got, root.Raw)
	}
	if got := toolUse.Get("id").String(); got != "call_custom_1" {
		t.Fatalf("tool use id = %q, want call_custom_1", got)
	}
	if got := toolUse.Get("input.input").String(); got != "pwd" {
		t.Fatalf("custom tool input = %q, want pwd", got)
	}
	toolResult := root.Get("messages.1.content.0")
	if got := toolResult.Get("type").String(); got != "tool_result" {
		t.Fatalf("tool result type = %q, want tool_result", got)
	}
	if got := toolResult.Get("tool_use_id").String(); got != "call_custom_1" {
		t.Fatalf("tool result id = %q, want call_custom_1", got)
	}
	if got := toolResult.Get("content").String(); got != "/workspace" {
		t.Fatalf("tool result content = %q, want /workspace", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ReplaysNamespacedFunctionCallHistory(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"additional_tools","tools":[{"type":"namespace","name":"mcp__node_repl","tools":[{"type":"function","name":"js","parameters":{"type":"object","properties":{}}}]}]},
			{"type":"function_call","call_id":"call.namespace","name":"js","namespace":"mcp__node_repl","arguments":"{\"code\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call.namespace","output":"ok"}
		]
	}`)

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	if !root.Get(`tools.#(name=="mcp__node_repl__js")`).Exists() {
		t.Fatal("missing qualified namespace tool declaration")
	}
	toolUse := root.Get("messages.0.content.0")
	if got := toolUse.Get("name").String(); got != "mcp__node_repl__js" {
		t.Fatalf("historical tool_use name = %q, want mcp__node_repl__js", got)
	}
	if got := root.Get("messages.1.content.0.tool_use_id").String(); got != "call_namespace" {
		t.Fatalf("historical tool_result id = %q, want call_namespace", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_MapsCustomAndNamespacedToolChoice(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantToolName string
	}{
		{
			name: "custom",
			raw: `{
				"model":"claude-test",
				"tools":[{"type":"custom","name":"exec"}],
				"tool_choice":{"type":"custom","name":"exec"}
			}`,
			wantToolName: "exec",
		},
		{
			name: "namespace",
			raw: `{
				"model":"claude-test",
				"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"mcp__node_repl","tools":[{"type":"function","name":"js"}]}]}],
				"tool_choice":{"type":"function","name":"js","namespace":"mcp__node_repl"}
			}`,
			wantToolName: "mcp__node_repl__js",
		},
		{
			name: "top-level-short-name-wins",
			raw: `{
				"model":"claude-test",
				"tools":[{"type":"function","name":"foo"}],
				"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"mcp__tools","tools":[{"type":"function","name":"foo"}]}]}],
				"tool_choice":{"type":"function","name":"foo"}
			}`,
			wantToolName: "foo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", []byte(tt.raw), false))
			if got := root.Get("tool_choice.type").String(); got != "tool" {
				t.Fatalf("tool_choice.type = %q, want tool; output=%s", got, root.Raw)
			}
			if got := root.Get("tool_choice.name").String(); got != tt.wantToolName {
				t.Fatalf("tool_choice.name = %q, want %q", got, tt.wantToolName)
			}
		})
	}
}

func TestQualifyResponsesNamespaceToolNameAvoidsPrefixCollision(t *testing.T) {
	tests := []struct {
		namespace string
		child     string
		want      string
	}{
		{namespace: "collab", child: "collaboration", want: "collab__collaboration"},
		{namespace: "collab", child: "collab__send", want: "collab__send"},
		{namespace: "collab__", child: "send", want: "collab__send"},
		{namespace: "mcp__node_repl", child: "mcp__node_repl__js", want: "mcp__node_repl__js"},
	}

	for _, tt := range tests {
		got := qualifyResponsesNamespaceToolName(tt.namespace, tt.child)
		if got != tt.want {
			t.Errorf("qualifyResponsesNamespaceToolName(%q, %q) = %q, want %q", tt.namespace, tt.child, got, tt.want)
		}
	}

	raw := []byte(`{
		"tools":[{"type":"namespace","name":"collab","tools":[{"type":"function","name":"collaboration"}]}]
	}`)
	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false))
	if got := root.Get("tools.0.name").String(); got != "collab__collaboration" {
		t.Fatalf("qualified tool declaration = %q, want collab__collaboration", got)
	}
}

func TestSplitResponsesQualifiedFunctionCallFromAdditionalTools(t *testing.T) {
	raw := []byte(`{
		"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"mcp__node_repl","tools":[{"type":"function","name":"js"}]}]}]
	}`)

	name, namespace := splitResponsesQualifiedFunctionCallFromRequest(raw, "mcp__node_repl__js")
	if name != "js" {
		t.Fatalf("name = %q, want js", name)
	}
	if namespace != "mcp__node_repl" {
		t.Fatalf("namespace = %q, want mcp__node_repl", namespace)
	}
}

func testClaudeResponsesThinkingSignature(t *testing.T) (string, string) {
	t.Helper()
	return testClaudeResponsesThinkingSignatureForModel(t, "claude-sonnet-4-6")
}

func testClaudeResponsesThinkingSignatureForModel(t *testing.T, model string) (string, string) {
	t.Helper()
	channelBlock := []byte{}
	channelBlock = protowire.AppendTag(channelBlock, 1, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 12)
	channelBlock = protowire.AppendTag(channelBlock, 2, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 2)
	channelBlock = protowire.AppendTag(channelBlock, 6, protowire.BytesType)
	channelBlock = protowire.AppendString(channelBlock, model)

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

func TestConvertOpenAIResponsesRequestToClaude_PreservesContentPartCacheControl(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [
					{"type": "input_text", "text": "cached prefix", "cache_control": {"type": "ephemeral"}},
					{"type": "input_text", "text": "fresh question"}
				]
			}
		]
	}`

	result := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	content := resultJSON.Get("messages.0.content")
	if !content.IsArray() {
		t.Fatalf("expected content array when cache_control is present, got %s", result)
	}
	if got := content.Get("0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("content.0.cache_control.type = %q, want ephemeral. Output: %s", got, result)
	}
	if content.Get("1.cache_control").Exists() {
		t.Fatalf("content.1 should not have cache_control. Output: %s", result)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SystemLevelInputsBecomeSeparateSystemBlocks(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"instructions": "I1",
		"input": [
			{"type": "message", "role": "system", "content": [{"type": "input_text", "text": "S1"}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "U1"}]},
			{"type": "message", "role": "developer", "content": "D1"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "A1"}]},
			{"type": "message", "role": "system", "content": [{"type": "input_text", "text": "S2"}]}
		]
	}`

	result := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	root := gjson.ParseBytes(result)

	system := root.Get("system").Array()
	if len(system) != 4 {
		t.Fatalf("system blocks = %d, want 4. system: %s", len(system), root.Get("system").Raw)
	}
	for idx, want := range []string{"I1", "S1", "D1", "S2"} {
		if got := system[idx].Get("type").String(); got != "text" {
			t.Fatalf("system[%d].type = %q, want text", idx, got)
		}
		if got := system[idx].Get("text").String(); got != want {
			t.Fatalf("system[%d].text = %q, want %q", idx, got, want)
		}
	}

	messages := root.Get("messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2. messages: %s", len(messages), root.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("messages[0].role = %q, want user", got)
	}
	if got := messages[1].Get("role").String(); got != "assistant" {
		t.Fatalf("messages[1].role = %q, want assistant", got)
	}
	if strings.Contains(root.Get("messages").Raw, "I1") ||
		strings.Contains(root.Get("messages").Raw, "S1") ||
		strings.Contains(root.Get("messages").Raw, "D1") {
		t.Fatalf("system-level text must not be downgraded into messages: %s", root.Get("messages").Raw)
	}
	if strings.Contains(root.Get("messages").Raw, `"role":"system"`) {
		t.Fatalf("translator must not emit role=system messages: %s", root.Get("messages").Raw)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SystemOnlyInputKeepsFallbackUserMessage(t *testing.T) {
	inputJSON := `{"model": "gpt-4.1", "instructions": "I1"}`

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-opus-5", []byte(inputJSON), false))
	if got := len(root.Get("system").Array()); got != 1 {
		t.Fatalf("system blocks = %d, want 1", got)
	}
	messages := root.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1. messages: %s", len(messages), root.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("messages[0].role = %q, want user", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SystemNonTextPartKeptAsTypedMarker(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"input": [
			{"type": "message", "role": "developer", "content": [
				{"type": "input_text", "text": "D1"},
				{"type": "input_image", "image_url": "data:image/png;base64,AAAA"}
			]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "U1"}]}
		]
	}`

	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-opus-5", []byte(inputJSON), false))
	system := root.Get("system").Array()
	if len(system) != 2 {
		t.Fatalf("system blocks = %d, want 2. system: %s", len(system), root.Get("system").Raw)
	}
	if got := system[0].Get("text").String(); got != "D1" {
		t.Fatalf("system[0].text = %q, want D1", got)
	}
	if got := system[1].Get("type").String(); got != "input_image" {
		t.Fatalf("system[1].type = %q, want input_image", got)
	}
	if system[1].Get("source").Exists() {
		t.Fatalf("unsupported marker must not copy the payload: %s", system[1].Raw)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SystemItemCacheControlAppliesToLastBlock(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"input": [
			{"type": "message", "role": "system", "cache_control": {"type": "ephemeral"}, "content": [
				{"type": "input_text", "text": "S1"},
				{"type": "input_text", "text": "S2"}
			]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "U1"}]}
		]
	}`

	system := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-opus-5", []byte(inputJSON), false)).Get("system").Array()
	if len(system) != 2 {
		t.Fatalf("system blocks = %d, want 2", len(system))
	}
	if system[0].Get("cache_control").Exists() {
		t.Fatalf("system[0] must not carry cache_control: %s", system[0].Raw)
	}
	if got := system[1].Get("cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("system[1].cache_control.type = %q, want ephemeral", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_DeduplicatesToolOutputs(t *testing.T) {
	// Tests that duplicate outputs are deduplicated to the final payload,
	// emitted at the first occurrence position (before subsequent assistant turns),
	// and that non-empty/empty IDs behave properly.
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"Use lookup."}]
			},
			{
				"type":"function_call",
				"call_id":"toolu_dup",
				"name":"lookup",
				"arguments":"{}"
			},
			{
				"type":"function_call_output",
				"call_id":"toolu_dup",
				"output":"first result"
			},
			{
				"type":"message",
				"role":"assistant",
				"content":[{"type":"output_text","text":"Intermediate step"}]
			},
			{
				"type":"function_call",
				"call_id":"toolu_parallel",
				"name":"other",
				"arguments":"{}"
			},
			{
				"type":"function_call_output",
				"call_id":"toolu_dup",
				"output":"final result"
			},
			{
				"type":"custom_tool_call_output",
				"call_id":"call.custom:dup",
				"output":"custom first"
			},
			{
				"type":"custom_tool_call_output",
				"call_id":"call.custom:dup",
				"output":"custom final"
			},
			{
				"type":"function_call_output",
				"call_id":"toolu_parallel",
				"output":"parallel result"
			},
			{
				"type":"function_call_output",
				"call_id":"",
				"output":"empty id output"
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	messages := root.Get("messages").Array()
	if len(messages) < 5 {
		t.Fatalf("expected at least 5 messages, got %d. Output: %s", len(messages), string(out))
	}

	// Message 0: user message
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("messages[0].role = %q, want user", got)
	}

	// Message 1: assistant tool_use toolu_dup
	if got := messages[1].Get("content.0.type").String(); got != "tool_use" {
		t.Fatalf("messages[1].content.0.type = %q, want tool_use", got)
	}
	if got := messages[1].Get("content.0.id").String(); got != "toolu_dup" {
		t.Fatalf("messages[1].content.0.id = %q, want toolu_dup", got)
	}

	// Message 2: user tool_result for toolu_dup with final payload, BEFORE assistant message 3
	if got := messages[2].Get("role").String(); got != "user" {
		t.Fatalf("messages[2].role = %q, want user", got)
	}
	if got := messages[2].Get("content.0.type").String(); got != "tool_result" {
		t.Fatalf("messages[2].content.0.type = %q, want tool_result", got)
	}
	if got := messages[2].Get("content.0.tool_use_id").String(); got != "toolu_dup" {
		t.Fatalf("messages[2].content.0.tool_use_id = %q, want toolu_dup", got)
	}
	if got := messages[2].Get("content.0.content").String(); got != "final result" {
		t.Fatalf("messages[2].content.0.content = %q, want 'final result'", got)
	}

	// Message 3: assistant intermediate text + tool_use for toolu_parallel
	if got := messages[3].Get("role").String(); got != "assistant" {
		t.Fatalf("messages[3].role = %q, want assistant", got)
	}
	if got := messages[3].Get("content.0.text").String(); got != "Intermediate step" {
		t.Fatalf("messages[3].content.0.text = %q, want 'Intermediate step'", got)
	}
	if got := messages[3].Get("content.1.id").String(); got != "toolu_parallel" {
		t.Fatalf("messages[3].content.1.id = %q, want toolu_parallel", got)
	}

	// Message 4: user tool_results: call_custom_dup (custom final), toolu_parallel (parallel result), and empty id output
	msg4Blocks := messages[4].Get("content").Array()
	if len(msg4Blocks) != 3 {
		t.Fatalf("expected 3 tool_result blocks in message 4, got %d. Output: %s", len(msg4Blocks), string(out))
	}
	if got := msg4Blocks[0].Get("tool_use_id").String(); got != "call_custom_dup" {
		t.Fatalf("msg4Blocks[0].tool_use_id = %q, want call_custom_dup", got)
	}
	if got := msg4Blocks[0].Get("content").String(); got != "custom final" {
		t.Fatalf("msg4Blocks[0].content = %q, want 'custom final'", got)
	}

	if got := msg4Blocks[1].Get("tool_use_id").String(); got != "toolu_parallel" {
		t.Fatalf("msg4Blocks[1].tool_use_id = %q, want toolu_parallel", got)
	}
	if got := msg4Blocks[1].Get("content").String(); got != "parallel result" {
		t.Fatalf("msg4Blocks[1].content = %q, want 'parallel result'", got)
	}

	if got := msg4Blocks[2].Get("content").String(); got != "empty id output" {
		t.Fatalf("msg4Blocks[2].content = %q, want 'empty id output'", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ServiceTierToSpeed(t *testing.T) {
	tests := []struct {
		name            string
		serviceTier     string
		hasServiceTier  bool
		reasoningEffort string
		wantSpeed       string
		wantSpeedExist  bool
	}{
		{
			name:           "absent service_tier omits speed",
			hasServiceTier: false,
			wantSpeedExist: false,
		},
		{
			name:           "default service_tier omits speed",
			serviceTier:    "default",
			hasServiceTier: true,
			wantSpeedExist: false,
		},
		{
			name:           "standard service_tier omits speed",
			serviceTier:    "standard",
			hasServiceTier: true,
			wantSpeedExist: false,
		},
		{
			name:           "unsupported service_tier omits speed",
			serviceTier:    "flex",
			hasServiceTier: true,
			wantSpeedExist: false,
		},
		{
			name:           "priority service_tier emits fast speed",
			serviceTier:    "priority",
			hasServiceTier: true,
			wantSpeed:      "fast",
			wantSpeedExist: true,
		},
		{
			name:            "priority with low reasoning effort",
			serviceTier:     "priority",
			hasServiceTier:  true,
			reasoningEffort: "low",
			wantSpeed:       "fast",
			wantSpeedExist:  true,
		},
		{
			name:            "priority with medium reasoning effort",
			serviceTier:     "priority",
			hasServiceTier:  true,
			reasoningEffort: "medium",
			wantSpeed:       "fast",
			wantSpeedExist:  true,
		},
		{
			name:            "priority with high reasoning effort",
			serviceTier:     "priority",
			hasServiceTier:  true,
			reasoningEffort: "high",
			wantSpeed:       "fast",
			wantSpeedExist:  true,
		},
		{
			name:            "priority with xhigh reasoning effort",
			serviceTier:     "priority",
			hasServiceTier:  true,
			reasoningEffort: "xhigh",
			wantSpeed:       "fast",
			wantSpeedExist:  true,
		},
		{
			name:            "priority with max reasoning effort",
			serviceTier:     "priority",
			hasServiceTier:  true,
			reasoningEffort: "max",
			wantSpeed:       "fast",
			wantSpeedExist:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"model":"claude-3-7-sonnet-20250219","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]`
			if tt.hasServiceTier {
				raw = fmt.Sprintf(`%s,"service_tier":%q`, raw, tt.serviceTier)
			}
			if tt.reasoningEffort != "" {
				raw = fmt.Sprintf(`%s,"reasoning":{"effort":%q}`, raw, tt.reasoningEffort)
			}
			raw += `}`

			out := ConvertOpenAIResponsesRequestToClaude("claude-3-7-sonnet-20250219", []byte(raw), false)
			root := gjson.ParseBytes(out)

			speedResult := root.Get("speed")
			if speedResult.Exists() != tt.wantSpeedExist {
				t.Fatalf("speed exists = %v, want %v. Output: %s", speedResult.Exists(), tt.wantSpeedExist, string(out))
			}
			if tt.wantSpeedExist && speedResult.String() != tt.wantSpeed {
				t.Fatalf("speed = %q, want %q. Output: %s", speedResult.String(), tt.wantSpeed, string(out))
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToClaude_PreservesCallerSuppliedMetadataUserID(t *testing.T) {
	testCases := []struct {
		name     string
		rawJSON  string
		expected string
	}{
		{
			name:     "plain string",
			rawJSON:  `{"model":"claude-test","metadata":{"user_id":"custom-resp-user-123"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
			expected: "custom-resp-user-123",
		},
		{
			name:     "special characters and json string",
			rawJSON:  `{"model":"claude-test","metadata":{"user_id":"foo\"bar\nbaz\\qux"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
			expected: "foo\"bar\nbaz\\qux",
		},
		{
			name:     "claude code json format",
			rawJSON:  `{"model":"claude-test","metadata":{"user_id":"{\"device_id\":\"0000000000000000000000000000000000000000000000000000000000000000\",\"session_id\":\"11111111-2222-4333-8444-555555555555\"}"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
			expected: `{"device_id":"0000000000000000000000000000000000000000000000000000000000000000","session_id":"11111111-2222-4333-8444-555555555555"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			out := ConvertOpenAIResponsesRequestToClaude("claude-test", []byte(tc.rawJSON), false)
			if !gjson.ValidBytes(out) {
				t.Fatalf("output is invalid json: %s", string(out))
			}
			got := gjson.GetBytes(out, "metadata.user_id").String()
			if got != tc.expected {
				t.Fatalf("metadata.user_id = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToClaude_PreservesUserField(t *testing.T) {
	raw := []byte(`{"model":"claude-test","user":"openai-resp-user-456","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	if !gjson.ValidBytes(out) {
		t.Fatalf("output is invalid json: %s", string(out))
	}
	got := gjson.GetBytes(out, "metadata.user_id").String()
	if got != "openai-resp-user-456" {
		t.Fatalf("metadata.user_id = %q, want %q", got, "openai-resp-user-456")
	}
}

func TestConvertOpenAIResponsesRequestToClaude_DifferentSessionsProduceDifferentUserIDs(t *testing.T) {
	a := []byte(`{"model":"claude-test","prompt_cache_key":"resp-session-a","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	b := []byte(`{"model":"claude-test","prompt_cache_key":"resp-session-b","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	outA := ConvertOpenAIResponsesRequestToClaude("claude-test", a, false)
	outB := ConvertOpenAIResponsesRequestToClaude("claude-test", b, false)
	idA := gjson.GetBytes(outA, "metadata.user_id").String()
	idB := gjson.GetBytes(outB, "metadata.user_id").String()
	if idA == idB {
		t.Fatalf("different prompt_cache_key produced identical metadata.user_id: %q", idA)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_DifferentUserContentWithSameSystemPrompt(t *testing.T) {
	rawA := []byte(`{
		"model": "claude-test",
		"instructions": "global instruction",
		"input": [
			{"type": "message", "role": "system", "content": "system context"},
			{"type": "message", "role": "user", "content": "user question A"}
		]
	}`)
	rawB := []byte(`{
		"model": "claude-test",
		"instructions": "global instruction",
		"input": [
			{"type": "message", "role": "system", "content": "system context"},
			{"type": "message", "role": "user", "content": "user question B"}
		]
	}`)
	outA := ConvertOpenAIResponsesRequestToClaude("claude-test", rawA, false)
	outB := ConvertOpenAIResponsesRequestToClaude("claude-test", rawB, false)
	idA := gjson.GetBytes(outA, "metadata.user_id").String()
	idB := gjson.GetBytes(outB, "metadata.user_id").String()
	if idA == "" || idB == "" || idA == "unknown" || idB == "unknown" {
		t.Fatalf("expected valid derived user_id, got idA=%q idB=%q", idA, idB)
	}
	if idA == idB {
		t.Fatalf("different user questions with same system prompt produced identical metadata.user_id: %q", idA)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_FableStripsTrailingAssistantPrefill(t *testing.T) {
	raw := []byte(`{
		"model": "claude-fable-5",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hello"}]},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "progress update"}]}
		]
	}`)
	out := ConvertOpenAIResponsesRequestToClaude("claude-fable-5", raw, false)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("expected 1 message after stripping trailing assistant prefill, got %d: %s", len(messages), string(out))
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("expected final message role = %q, got %q", "user", got)
	}
	if got := messages[0].Get("content").String(); got != "hello" {
		t.Fatalf("expected content = %q, got %q", "hello", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_FableOnlyAssistantMessageYieldsFallbackUser(t *testing.T) {
	raw := []byte(`{
		"model": "claude-fable-5",
		"input": [
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "orphan progress"}]}
		]
	}`)
	out := ConvertOpenAIResponsesRequestToClaude("claude-fable-5", raw, false)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("expected 1 fallback user message, got %d: %s", len(messages), string(out))
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("expected role = user, got %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_UnsupportedPrefillModelsStripTrailingAssistant(t *testing.T) {
	unsupportedModels := []string{
		"claude-fable-5",
		"claude-opus-5",
		"claude-sonnet-4-6",
	}
	for _, model := range unsupportedModels {
		t.Run(model, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{
				"model": %q,
				"input": [
					{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hello"}]},
					{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "progress update"}]}
				]
			}`, model))
			out := ConvertOpenAIResponsesRequestToClaude(model, raw, false)
			messages := gjson.GetBytes(out, "messages").Array()
			if len(messages) != 1 {
				t.Fatalf("expected 1 message after stripping trailing assistant prefill for model %s, got %d: %s", model, len(messages), string(out))
			}
			if got := messages[0].Get("role").String(); got != "user" {
				t.Fatalf("expected final message role = %q, got %q", "user", got)
			}
			if got := messages[0].Get("content").String(); got != "hello" {
				t.Fatalf("expected content = %q, got %q", "hello", got)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SupportedPrefillModelsPreserveAssistantPrefill(t *testing.T) {
	supportedModels := []string{
		"claude-sonnet-4-5",
		"claude-haiku-4-5",
	}
	for _, model := range supportedModels {
		t.Run(model, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{
				"model": %q,
				"input": [
					{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hello"}]},
					{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "prefill text"}]}
				]
			}`, model))
			out := ConvertOpenAIResponsesRequestToClaude(model, raw, false)
			messages := gjson.GetBytes(out, "messages").Array()
			if len(messages) != 2 {
				t.Fatalf("expected 2 messages preserving assistant prefill for model %s, got %d: %s", model, len(messages), string(out))
			}
			if got := messages[1].Get("role").String(); got != "assistant" {
				t.Fatalf("expected second message role = assistant, got %q", got)
			}
			if got := messages[1].Get("content").String(); got != "prefill text" {
				t.Fatalf("expected content = %q, got %q", "prefill text", got)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToClaudeWithCompat_FablePreservesAssistantPrefill(t *testing.T) {
	raw := []byte(`{
		"model": "claude-fable-5",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hello"}]},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "prefill text"}]}
		]
	}`)
	out := ConvertOpenAIResponsesRequestToClaudeWithCompat("claude-fable-5", raw, false)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages in compat mode, got %d: %s", len(messages), string(out))
	}
	if got := messages[1].Get("role").String(); got != "assistant" {
		t.Fatalf("expected second message role = assistant, got %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_StripsTrailingThinkingBlocksFromAssistant(t *testing.T) {
	rawSignature, _ := testClaudeResponsesThinkingSignature(t)

	t.Run("user_then_reasoning_drops_trailing_assistant_message", func(t *testing.T) {
		raw := []byte(fmt.Sprintf(`{
			"model": "claude-haiku-4-5-20251001",
			"input": [
				{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hello"}]},
				{"type": "reasoning", "encrypted_content": %q, "summary": [{"type": "summary_text", "text": "thought"}]}
			]
		}`, rawSignature))
		out := ConvertOpenAIResponsesRequestToClaude("claude-haiku-4-5-20251001", raw, false)
		messages := gjson.GetBytes(out, "messages").Array()
		if len(messages) != 1 {
			t.Fatalf("expected 1 message (user only) after stripping trailing thinking, got %d: %s", len(messages), string(out))
		}
		if got := messages[0].Get("role").String(); got != "user" {
			t.Fatalf("expected role = user, got %q", got)
		}
		if got := messages[0].Get("content").String(); got != "hello" {
			t.Fatalf("expected user content = hello, got %q", got)
		}
	})

	t.Run("user_assistant_text_then_reasoning_strips_trailing_thinking_keeps_assistant_text", func(t *testing.T) {
		raw := []byte(fmt.Sprintf(`{
			"model": "claude-haiku-4-5-20251001",
			"input": [
				{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hello"}]},
				{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "prefill text"}]},
				{"type": "reasoning", "encrypted_content": %q, "summary": [{"type": "summary_text", "text": "thought"}]}
			]
		}`, rawSignature))
		out := ConvertOpenAIResponsesRequestToClaude("claude-haiku-4-5-20251001", raw, false)
		messages := gjson.GetBytes(out, "messages").Array()
		if len(messages) != 2 {
			t.Fatalf("expected 2 messages (user + assistant text), got %d: %s", len(messages), string(out))
		}
		if got := messages[1].Get("role").String(); got != "assistant" {
			t.Fatalf("expected second message role = assistant, got %q", got)
		}
		if got := messages[1].Get("content").String(); got != "prefill text" {
			t.Fatalf("expected assistant content = prefill text, got %q (raw: %s)", got, messages[1].Get("content").Raw)
		}
	})

	t.Run("trailing_redacted_thinking_is_stripped", func(t *testing.T) {
		raw := []byte(fmt.Sprintf(`{
			"model": "claude-haiku-4-5-20251001",
			"input": [
				{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hello"}]},
				{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "prefill text"}]},
				{"type": "reasoning", "encrypted_content": %q, "summary": []},
				{"type": "reasoning", "encrypted_content": %q, "summary": []}
			]
		}`, rawSignature, ClaudeResponsesRedactedThinkingPrefix+"redacted-data"))
		out := ConvertOpenAIResponsesRequestToClaude("claude-haiku-4-5-20251001", raw, false)
		messages := gjson.GetBytes(out, "messages").Array()
		if len(messages) != 2 {
			t.Fatalf("expected 2 messages (user + assistant text), got %d: %s", len(messages), string(out))
		}
		if got := messages[1].Get("content").String(); got != "prefill text" {
			t.Fatalf("expected assistant content = prefill text, got %q (raw: %s)", got, messages[1].Get("content").Raw)
		}
	})

	t.Run("only_reasoning_item_yields_fallback_user_message", func(t *testing.T) {
		raw := []byte(fmt.Sprintf(`{
			"model": "claude-haiku-4-5-20251001",
			"input": [
				{"type": "reasoning", "encrypted_content": %q, "summary": [{"type": "summary_text", "text": "thought"}]}
			]
		}`, rawSignature))
		out := ConvertOpenAIResponsesRequestToClaude("claude-haiku-4-5-20251001", raw, false)
		messages := gjson.GetBytes(out, "messages").Array()
		if len(messages) != 1 {
			t.Fatalf("expected 1 message (fallback user), got %d: %s", len(messages), string(out))
		}
		if got := messages[0].Get("role").String(); got != "user" {
			t.Fatalf("expected fallback role = user, got %q", got)
		}
	})

	t.Run("compat_mode_preserves_trailing_thinking", func(t *testing.T) {
		raw := []byte(fmt.Sprintf(`{
			"model": "claude-haiku-4-5-20251001",
			"input": [
				{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hello"}]},
				{"type": "reasoning", "encrypted_content": %q, "summary": [{"type": "summary_text", "text": "thought"}]}
			]
		}`, rawSignature))
		out := ConvertOpenAIResponsesRequestToClaudeWithCompat("claude-haiku-4-5-20251001", raw, false)
		messages := gjson.GetBytes(out, "messages").Array()
		if len(messages) != 2 {
			t.Fatalf("expected 2 messages in compat mode, got %d: %s", len(messages), string(out))
		}
	})
}
