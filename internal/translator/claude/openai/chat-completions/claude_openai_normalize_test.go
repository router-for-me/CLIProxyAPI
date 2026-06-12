package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

// Cursor agent mode sends Anthropic-native tool_use / tool_result blocks and
// bare tool definitions to the OpenAI Chat Completions endpoint. These tests
// verify normalizeAnthropicRequestBlocks rewrites them into standard OpenAI
// shapes so the existing translation produces a valid Claude request.

func TestConvertOpenAIRequestToClaude_CursorToolResultBlock(t *testing.T) {
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"messages": [
			{"role": "user", "content": "list files"},
			{
				"role": "assistant",
				"content": [
					{"type": "text", "text": "Let me check."},
					{"type": "tool_use", "id": "toolu_1", "name": "list_dir", "input": {"path": "."}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_1", "content": "a.txt\nb.txt"}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	// assistant message must carry a tool_use block
	asst := messages[1]
	if got := asst.Get("role").String(); got != "assistant" {
		t.Fatalf("Expected messages[1].role assistant, got %q", got)
	}
	toolUse := asst.Get("content.#(type==tool_use)")
	if !toolUse.Exists() {
		t.Fatalf("Expected a tool_use block in assistant message, got: %s", asst.Raw)
	}
	if got := toolUse.Get("id").String(); got != "toolu_1" {
		t.Fatalf("Expected tool_use id toolu_1, got %q", got)
	}
	if got := toolUse.Get("name").String(); got != "list_dir" {
		t.Fatalf("Expected tool_use name list_dir, got %q", got)
	}
	if got := toolUse.Get("input.path").String(); got != "." {
		t.Fatalf("Expected tool_use input.path '.', got %q", got)
	}

	// the tool_result must become a user message with a tool_result block
	toolResultMsg := messages[2]
	if got := toolResultMsg.Get("role").String(); got != "user" {
		t.Fatalf("Expected messages[2].role user, got %q", got)
	}
	tr := toolResultMsg.Get("content.0")
	if got := tr.Get("type").String(); got != "tool_result" {
		t.Fatalf("Expected tool_result block, got %q (%s)", got, toolResultMsg.Raw)
	}
	if got := tr.Get("tool_use_id").String(); got != "toolu_1" {
		t.Fatalf("Expected tool_use_id toolu_1, got %q", got)
	}
	if got := tr.Get("content").String(); got != "a.txt\nb.txt" {
		t.Fatalf("Expected tool_result content, got %q", got)
	}
}

func TestConvertOpenAIRequestToClaude_CursorToolResultWithText(t *testing.T) {
	// A user turn that mixes a tool_result with a follow-up text instruction.
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_9", "content": "done"},
					{"type": "text", "text": "now summarize"}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages (tool_result + user text), got %d: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	if got := messages[0].Get("content.0.type").String(); got != "tool_result" {
		t.Fatalf("Expected first message tool_result, got %q", got)
	}
	if got := messages[1].Get("content.0.text").String(); got != "now summarize" {
		t.Fatalf("Expected trailing user text 'now summarize', got %q", got)
	}
}

func TestConvertOpenAIRequestToClaude_CursorParallelToolResults(t *testing.T) {
	// Cursor agent mode runs parallel tool calls: one assistant turn with
	// multiple tool_use blocks, answered by a single user turn carrying multiple
	// tool_result blocks. Claude requires all tool_result blocks for the prior
	// assistant tool_use turn to be grouped in ONE user message; splitting them
	// into separate user turns breaks the tool_use/tool_result pairing.
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"messages": [
			{"role": "user", "content": "inspect both files"},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "toolu_a", "name": "read_file", "input": {"path": "a.txt"}},
					{"type": "tool_use", "id": "toolu_b", "name": "read_file", "input": {"path": "b.txt"}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_a", "content": "alpha"},
					{"type": "tool_result", "tool_use_id": "toolu_b", "content": "beta"}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	// user, assistant(2 tool_use), user(2 tool_result grouped) -> 3 messages
	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages (parallel results grouped), got %d: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	asst := messages[1]
	if got := len(asst.Get("content").Array()); got != 2 {
		t.Fatalf("Expected 2 tool_use blocks in assistant turn, got %d: %s", got, asst.Raw)
	}

	toolTurn := messages[2]
	if got := toolTurn.Get("role").String(); got != "user" {
		t.Fatalf("Expected grouped tool_result turn role user, got %q", got)
	}
	blocks := toolTurn.Get("content").Array()
	if len(blocks) != 2 {
		t.Fatalf("Expected 2 tool_result blocks grouped in one user turn, got %d: %s", len(blocks), toolTurn.Raw)
	}
	if got := blocks[0].Get("tool_use_id").String(); got != "toolu_a" {
		t.Fatalf("Expected first tool_result tool_use_id toolu_a, got %q", got)
	}
	if got := blocks[0].Get("content").String(); got != "alpha" {
		t.Fatalf("Expected first tool_result content alpha, got %q", got)
	}
	if got := blocks[1].Get("tool_use_id").String(); got != "toolu_b" {
		t.Fatalf("Expected second tool_result tool_use_id toolu_b, got %q", got)
	}
	if got := blocks[1].Get("content").String(); got != "beta" {
		t.Fatalf("Expected second tool_result content beta, got %q", got)
	}
}

func TestConvertOpenAIRequestToClaude_CursorToolResultIsError(t *testing.T) {
	// Cursor forwards failed tool executions as Anthropic tool_result blocks with
	// is_error:true. That flag must survive the full conversion so Claude sees a
	// failed result instead of an apparent success.
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "toolu_err", "name": "run", "input": {"cmd": "boom"}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_err", "content": "command failed", "is_error": true}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	tr := messages[1].Get("content.0")
	if got := tr.Get("type").String(); got != "tool_result" {
		t.Fatalf("Expected tool_result block, got %q (%s)", got, messages[1].Raw)
	}
	if got := tr.Get("tool_use_id").String(); got != "toolu_err" {
		t.Fatalf("Expected tool_use_id toolu_err, got %q", got)
	}
	if !tr.Get("is_error").Exists() || !tr.Get("is_error").Bool() {
		t.Fatalf("Expected is_error:true preserved on tool_result, got: %s", tr.Raw)
	}
	if got := tr.Get("content").String(); got != "command failed" {
		t.Fatalf("Expected tool_result content preserved, got %q", got)
	}
}

func TestConvertOpenAIRequestToClaude_CursorToolResultNativeContentArray(t *testing.T) {
	// Cursor can forward a tool_result whose content is an array of Claude-native
	// blocks (e.g. an image with source, or a tool_reference). These must survive
	// verbatim instead of being dropped/stringified by the OpenAI part converter.
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "toolu_img", "name": "screenshot", "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_img",
						"content": [
							{"type": "text", "text": "here is the capture"},
							{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgo="}}
						]
					}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	tr := messages[1].Get("content.0")
	if got := tr.Get("type").String(); got != "tool_result" {
		t.Fatalf("Expected tool_result block, got %q (%s)", got, messages[1].Raw)
	}
	blocks := tr.Get("content").Array()
	if len(blocks) != 2 {
		t.Fatalf("Expected 2 native content blocks preserved, got %d: %s", len(blocks), tr.Raw)
	}
	if got := blocks[0].Get("text").String(); got != "here is the capture" {
		t.Fatalf("Expected text block preserved, got %q", got)
	}
	img := blocks[1]
	if got := img.Get("type").String(); got != "image" {
		t.Fatalf("Expected native image block preserved, got %q (%s)", got, img.Raw)
	}
	if got := img.Get("source.media_type").String(); got != "image/png" {
		t.Fatalf("Expected image source media_type preserved, got %q (%s)", got, img.Raw)
	}
	if got := img.Get("source.data").String(); got != "iVBORw0KGgo=" {
		t.Fatalf("Expected image source data preserved, got %q", got)
	}
	// The internal marker must not leak into the final Claude payload.
	if messages[1].Get("_anthropic_native_content").Exists() {
		t.Fatalf("Internal marker _anthropic_native_content leaked into output: %s", messages[1].Raw)
	}
}

func TestConvertOpenAIRequestToClaude_CursorThinkingBlocksPreserved(t *testing.T) {
	// Extended-thinking models send thinking / redacted_thinking blocks before
	// the tool_use in the assistant turn. Anthropic requires these to be returned
	// unchanged on subsequent tool-result requests, so they must survive and lead
	// the Claude assistant content.
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"messages": [
			{"role": "user", "content": "compute"},
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "let me reason", "signature": "sig-abc"},
					{"type": "redacted_thinking", "data": "encrypted-xyz"},
					{"type": "text", "text": "Calling tool"},
					{"type": "tool_use", "id": "toolu_t", "name": "calc", "input": {"x": 1}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_t", "content": "2"}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	asst := messages[1]
	blocks := asst.Get("content").Array()
	if len(blocks) < 3 {
		t.Fatalf("Expected thinking + text + tool_use in assistant content, got %d: %s", len(blocks), asst.Raw)
	}
	// thinking blocks must lead, unchanged.
	if got := blocks[0].Get("type").String(); got != "thinking" {
		t.Fatalf("Expected first block thinking, got %q (%s)", got, asst.Raw)
	}
	if got := blocks[0].Get("signature").String(); got != "sig-abc" {
		t.Fatalf("Expected thinking signature preserved, got %q", got)
	}
	if got := blocks[0].Get("thinking").String(); got != "let me reason" {
		t.Fatalf("Expected thinking text preserved, got %q", got)
	}
	if got := blocks[1].Get("type").String(); got != "redacted_thinking" {
		t.Fatalf("Expected second block redacted_thinking, got %q (%s)", got, asst.Raw)
	}
	if got := blocks[1].Get("data").String(); got != "encrypted-xyz" {
		t.Fatalf("Expected redacted_thinking data preserved, got %q", got)
	}
	// tool_use must still be present.
	if !asst.Get("content.#(type==tool_use)").Exists() {
		t.Fatalf("Expected tool_use preserved in assistant content: %s", asst.Raw)
	}
	// Internal marker must not leak.
	if asst.Get("_anthropic_thinking").Exists() {
		t.Fatalf("Internal marker _anthropic_thinking leaked into output: %s", asst.Raw)
	}
}

func TestConvertOpenAIRequestToClaude_BareAnthropicTools(t *testing.T) {
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [
			{
				"name": "read_file",
				"description": "Read a file",
				"input_schema": {"type": "object", "properties": {"path": {"type": "string"}}}
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	tools := resultJSON.Get("tools").Array()

	if len(tools) != 1 {
		t.Fatalf("Expected 1 tool, got %d: %s", len(tools), resultJSON.Get("tools").Raw)
	}
	tool := tools[0]
	if got := tool.Get("name").String(); got != "read_file" {
		t.Fatalf("Expected tool name read_file, got %q", got)
	}
	if got := tool.Get("description").String(); got != "Read a file" {
		t.Fatalf("Expected tool description, got %q", got)
	}
	if got := tool.Get("input_schema.properties.path.type").String(); got != "string" {
		t.Fatalf("Expected input_schema preserved, got: %s", tool.Raw)
	}
}

func TestNormalizeAnthropicRequestBlocks_TypedToolNotWrapped(t *testing.T) {
	// Typed Anthropic server tools (type present, not "function") must NOT be
	// rewritten into OpenAI function tools by the normalizer; only bare tools
	// (no "type") are wrapped. This guards against corrupting server tools like
	// web_search_20250305 into a wrong custom tool with no schema.
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"messages": [{"role": "user", "content": "search"}],
		"tools": [
			{"type": "web_search_20250305", "name": "web_search", "max_uses": 5},
			{"name": "read_file", "description": "Read a file", "input_schema": {"type": "object"}}
		]
	}`

	normalized := normalizeAnthropicRequestBlocks([]byte(inputJSON))
	normJSON := gjson.ParseBytes(normalized)
	tools := normJSON.Get("tools").Array()

	if len(tools) != 2 {
		t.Fatalf("Expected 2 tools after normalize, got %d: %s", len(tools), normJSON.Get("tools").Raw)
	}

	// Typed server tool kept verbatim (still its original type, not "function").
	typed := normJSON.Get(`tools.#(name=="web_search")`)
	if !typed.Exists() {
		t.Fatalf("Expected web_search tool preserved, got: %s", normJSON.Get("tools").Raw)
	}
	if got := typed.Get("type").String(); got != "web_search_20250305" {
		t.Fatalf("Expected typed tool type preserved as web_search_20250305, got %q (%s)", got, typed.Raw)
	}
	if got := typed.Get("max_uses").Int(); got != 5 {
		t.Fatalf("Expected typed tool fields preserved (max_uses=5), got %d", got)
	}

	// Bare tool wrapped into OpenAI function shape.
	bare := normJSON.Get(`tools.#(function.name=="read_file")`)
	if !bare.Exists() {
		t.Fatalf("Expected read_file wrapped into function tool, got: %s", normJSON.Get("tools").Raw)
	}
	if got := bare.Get("type").String(); got != "function" {
		t.Fatalf("Expected wrapped tool type function, got %q", got)
	}
	if !bare.Get("function.parameters").Exists() {
		t.Fatalf("Expected wrapped tool parameters, got: %s", bare.Raw)
	}
}

func TestConvertOpenAIRequestToClaude_TypedServerToolPreserved(t *testing.T) {
	// End-to-end guard: a typed Anthropic server tool sent alongside a bare
	// custom tool must survive the FULL conversion, not just the normalizer.
	// The downstream tool mapper previously emitted only type=="function"
	// tools, silently dropping typed server tools like web_search_20250305.
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"messages": [{"role": "user", "content": "search the web"}],
		"tools": [
			{"type": "web_search_20250305", "name": "web_search", "max_uses": 5},
			{"name": "read_file", "description": "Read a file", "input_schema": {"type": "object"}}
		]
	}`

	out := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	outJSON := gjson.ParseBytes(out)
	tools := outJSON.Get("tools").Array()

	if len(tools) != 2 {
		t.Fatalf("Expected 2 tools after full conversion, got %d: %s", len(tools), outJSON.Get("tools").Raw)
	}

	typed := outJSON.Get(`tools.#(name=="web_search")`)
	if !typed.Exists() {
		t.Fatalf("Expected typed server tool web_search to survive conversion, got: %s", outJSON.Get("tools").Raw)
	}
	if got := typed.Get("type").String(); got != "web_search_20250305" {
		t.Fatalf("Expected typed tool type preserved as web_search_20250305, got %q (%s)", got, typed.Raw)
	}
	if got := typed.Get("max_uses").Int(); got != 5 {
		t.Fatalf("Expected typed tool fields preserved (max_uses=5), got %d", got)
	}

	bare := outJSON.Get(`tools.#(name=="read_file")`)
	if !bare.Exists() {
		t.Fatalf("Expected bare custom tool read_file mapped to Claude tool, got: %s", outJSON.Get("tools").Raw)
	}
	if !bare.Get("input_schema").Exists() {
		t.Fatalf("Expected read_file mapped with input_schema, got: %s", bare.Raw)
	}
}

func TestConvertOpenAIRequestToClaude_UnversionedBuiltinToolDropped(t *testing.T) {
	// An unversioned OpenAI built-in tool type (e.g. {"type":"web_search"}) is NOT
	// a Claude-native server tool. Forwarding it verbatim would make Claude reject
	// the request with a 400, so it must be dropped (as before this change), while
	// a sibling function tool is still mapped normally.
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"messages": [{"role": "user", "content": "search"}],
		"tools": [
			{"type": "web_search"},
			{"type": "function", "function": {"name": "read_file", "description": "d", "parameters": {"type": "object"}}}
		]
	}`

	out := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	outJSON := gjson.ParseBytes(out)
	tools := outJSON.Get("tools").Array()

	if len(tools) != 1 {
		t.Fatalf("Expected only the function tool to survive (unversioned built-in dropped), got %d: %s", len(tools), outJSON.Get("tools").Raw)
	}
	if got := tools[0].Get("name").String(); got != "read_file" {
		t.Fatalf("Expected surviving tool read_file, got %q (%s)", got, tools[0].Raw)
	}
	// The unversioned built-in must not be present.
	if outJSON.Get(`tools.#(type=="web_search")`).Exists() {
		t.Fatalf("Unversioned web_search built-in should have been dropped: %s", outJSON.Get("tools").Raw)
	}
}

func TestConvertOpenAIRequestToClaude_StandardOpenAIUnchanged(t *testing.T) {
	// A normal OpenAI payload must pass through normalization untouched.
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"messages": [
			{
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "do_work", "arguments": "{\"a\":1}"}}
				]
			},
			{"role": "tool", "tool_call_id": "call_1", "content": "ok"}
		],
		"tools": [
			{"type": "function", "function": {"name": "do_work", "description": "d", "parameters": {"type": "object"}}}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d: %s", len(messages), resultJSON.Get("messages").Raw)
	}
	if got := messages[0].Get("content.0.type").String(); got != "tool_use" {
		t.Fatalf("Expected assistant tool_use, got %q", got)
	}
	if got := messages[0].Get("content.0.id").String(); got != "call_1" {
		t.Fatalf("Expected tool_use id call_1, got %q", got)
	}
	if got := messages[1].Get("content.0.type").String(); got != "tool_result" {
		t.Fatalf("Expected tool_result, got %q", got)
	}
	if got := resultJSON.Get("tools.0.name").String(); got != "do_work" {
		t.Fatalf("Expected tool do_work, got %q", got)
	}
}
