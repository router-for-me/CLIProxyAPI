package test

import (
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAIToCodex_PreservesBuiltinTools(t *testing.T) {
	in := []byte(`{
		"model":"gpt-5",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"web_search","search_context_size":"high"}],
		"tool_choice":{"type":"web_search"}
	}`)

	out := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAI, sdktranslator.FormatCodex, "gpt-5", in, false)

	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("expected 1 tool, got %d: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "web_search" {
		t.Fatalf("expected tools[0].type=web_search, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.search_context_size").String(); got != "high" {
		t.Fatalf("expected tools[0].search_context_size=high, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "web_search" {
		t.Fatalf("expected tool_choice.type=web_search, got %q: %s", got, string(out))
	}
}

func TestOpenAIToCodex_IgnoresMalformedFunctionToolsAndToolChoice(t *testing.T) {
	in := []byte(`{
		"model":"gpt-5",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[
			{"type":"function","function":{"name":"valid_tool","parameters":{"type":"object"}}},
			{"type":"web_search","search_context_size":"high"},
			{"type":"function","function":{"name":""}},
			{"type":"function","function":{"name":"   "}},
			{"type":"function","function":{}}
		],
		"tool_choice":{"type":"function","function":{"name":"   "}}
	}`)

	out := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAI, sdktranslator.FormatCodex, "gpt-5", in, false)

	if got := gjson.GetBytes(out, "tools.#").Int(); got != 2 {
		t.Fatalf("expected 2 tools, got %d: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("expected first tool to be function, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "valid_tool" {
		t.Fatalf("expected valid function tool name, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.1.type").String(); got != "web_search" {
		t.Fatalf("expected second tool to remain web_search, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice"); got.Exists() {
		t.Fatalf("expected malformed function tool_choice to be omitted, got %s", got.Raw)
	}
}

func TestOpenAIResponsesToOpenAI_IgnoresMalformedFunctionToolsAndToolChoice(t *testing.T) {
	in := []byte(`{
		"model":"gpt-5",
		"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"tools":[
			{"type":"function","name":"valid_tool","parameters":{"type":"object"}},
			{"type":"function","name":""},
			{"type":"function","name":"   "},
			{"type":"function","description":"missing name"}
		],
		"tool_choice":{"type":"function","name":"   "}
	}`)

	out := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, "gpt-5", in, false)

	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("expected 1 valid function tool, got %d: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "valid_tool" {
		t.Fatalf("expected valid tool name, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice"); got.Exists() {
		t.Fatalf("expected malformed function tool_choice to be omitted, got %s", got.Raw)
	}
}

func TestClaudeToOpenAI_IgnoresBlankToolsAndToolChoice(t *testing.T) {
	in := []byte(`{
		"model":"claude-sonnet-4-5",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[
			{"name":"valid_tool","description":"ok","input_schema":{"type":"object"}},
			{"name":""},
			{"name":"   "}
		],
		"tool_choice":{"type":"tool","name":"   "}
	}`)

	out := sdktranslator.TranslateRequest(sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, "gpt-5", in, false)

	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("expected 1 valid tool, got %d: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "valid_tool" {
		t.Fatalf("expected valid tool name, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice"); got.Exists() {
		t.Fatalf("expected blank tool_choice to be omitted, got %s", got.Raw)
	}
}
