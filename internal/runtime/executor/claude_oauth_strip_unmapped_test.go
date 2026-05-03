package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Tests for remapOAuthToolNamesEx(body, stripUnmapped=true).
//
// stripUnmapped=true is the path taken for ALL OAuth requests. These tests pin
// the contract that:
//   - whitelisted (Claude Code catalog) tools survive and get TitleCased,
//   - Anthropic built-in tools (with a non-empty `type`) survive verbatim,
//   - everything else (client-specific tools like lsp_*, context7_*, etc.) is
//     dropped from tools[],
//   - tool_choice is reconciled with the new tools[] (kept when valid, dropped
//     when it points at a stripped tool, kept when it points at a built-in).

func TestRemapOAuthToolNamesEx_StripUnmapped_RemovesThirdPartyTools(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"name":"bash","description":"x","input_schema":{}},
			{"name":"lsp_definition","description":"x","input_schema":{}},
			{"name":"context7_get_docs","description":"x","input_schema":{}},
			{"name":"session_create","description":"x","input_schema":{}},
			{"name":"read","description":"x","input_schema":{}}
		],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)

	out, renamed := remapOAuthToolNamesEx(body, true)
	if !renamed {
		t.Fatalf("renamed = false, want true (lowercase bash/read should be retitled)")
	}

	got := gjson.GetBytes(out, "tools.#").Int()
	if got != 2 {
		t.Fatalf("tools.# = %d, want 2 (only bash and read survive)", got)
	}

	names := []string{
		gjson.GetBytes(out, "tools.0.name").String(),
		gjson.GetBytes(out, "tools.1.name").String(),
	}
	want := map[string]bool{"Bash": true, "Read": true}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected surviving tool name %q; want one of %v", n, want)
		}
	}
}

func TestRemapOAuthToolNamesEx_StripUnmapped_PreservesAnthropicBuiltins(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"type":"web_search_20250305","name":"web_search"},
			{"name":"bash","description":"x","input_schema":{}},
			{"name":"nodes","description":"x","input_schema":{}}
		],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)

	out, _ := remapOAuthToolNamesEx(body, true)

	got := gjson.GetBytes(out, "tools.#").Int()
	if got != 2 {
		t.Fatalf("tools.# = %d, want 2 (web_search builtin + Bash); nodes should be stripped", got)
	}

	if t0Type := gjson.GetBytes(out, "tools.0.type").String(); t0Type != "web_search_20250305" {
		t.Errorf("tools.0.type = %q, want %q (built-in must survive verbatim)", t0Type, "web_search_20250305")
	}
	if t1Name := gjson.GetBytes(out, "tools.1.name").String(); t1Name != "Bash" {
		t.Errorf("tools.1.name = %q, want %q", t1Name, "Bash")
	}
	for i := 0; i < int(got); i++ {
		path := "tools." + strings.TrimSpace(string('0'+rune(i))) + ".name"
		if gjson.GetBytes(out, path).String() == "nodes" {
			t.Errorf("'nodes' should have been stripped, found at %s", path)
		}
	}
}

func TestRemapOAuthToolNamesEx_StripUnmapped_DropsToolChoiceForStrippedTool(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"name":"bash","description":"x","input_schema":{}},
			{"name":"lsp_definition","description":"x","input_schema":{}}
		],
		"tool_choice":{"type":"tool","name":"lsp_definition"},
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)

	out, _ := remapOAuthToolNamesEx(body, true)

	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("tools.# = %d, want 1 (only Bash survives)", got)
	}
	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Errorf("tool_choice should be deleted when it pointed at a stripped tool, got %q",
			gjson.GetBytes(out, "tool_choice").Raw)
	}
}

func TestRemapOAuthToolNamesEx_StripUnmapped_PreservesToolChoiceForBuiltin(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"type":"web_search_20250305","name":"web_search"},
			{"name":"bash","description":"x","input_schema":{}}
		],
		"tool_choice":{"type":"tool","name":"web_search"},
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)

	out, _ := remapOAuthToolNamesEx(body, true)

	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "web_search" {
		t.Errorf("tool_choice.name = %q, want %q (built-in tool_choice must survive)", got, "web_search")
	}
	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "tool" {
		t.Errorf("tool_choice.type = %q, want %q", got, "tool")
	}
}

func TestRemapOAuthToolNamesEx_StripUnmapped_RetitlesToolChoice(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"name":"bash","description":"x","input_schema":{}}
		],
		"tool_choice":{"type":"tool","name":"bash"},
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)

	out, _ := remapOAuthToolNamesEx(body, true)

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "Bash" {
		t.Fatalf("tools.0.name = %q, want %q", got, "Bash")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "Bash" {
		t.Errorf("tool_choice.name = %q, want %q (must be retitled together with tools[])", got, "Bash")
	}
}
