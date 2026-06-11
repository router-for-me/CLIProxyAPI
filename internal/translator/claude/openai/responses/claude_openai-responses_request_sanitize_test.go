package responses

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeAnthropicToolName_BasicCases(t *testing.T) {
	cases := map[string]string{
		"":                              "",
		"exec_command":                  "exec_command",
		"mcp__computer_use__list_apps":  "mcp__computer_use__list_apps",
		"multi_agent_v1__spawn_agent":   "multi_agent_v1__spawn_agent",
		"computer-use:computer-use":     "computer-use__computer-use",
		"plugin.skill.name":             "plugin__skill__name",
		"   spaced name   ":             "spaced__name",
		"!!!":                           "tool",
		"a:b:c:d":                       "a__b__c__d",
		"a..b..c":                       "a__b__c",
	}
	for in, want := range cases {
		got := sanitizeAnthropicToolName(in)
		if got != want {
			t.Errorf("sanitizeAnthropicToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeAnthropicToolName_LengthCap(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := sanitizeAnthropicToolName(long)
	if len(got) != 64 {
		t.Errorf("expected 64-char cap, got %d", len(got))
	}
}

func TestSanitizeAnthropicToolName_PreservesValidNames(t *testing.T) {
	good := []string{"exec_command", "view_image", "apply_patch", "mcp__Serena__find_symbol", "multi_agent_v1__wait_agent"}
	for _, n := range good {
		if got := sanitizeAnthropicToolName(n); got != n {
			t.Errorf("expected passthrough for %q, got %q", n, got)
		}
	}
}

func TestConvertOpenAIResponses_ToolUseNameSanitized(t *testing.T) {
	body := []byte(`{
		"model": "kiro-api/claude-opus-4-7-thinking",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"function_call","call_id":"tooluse_xxx","name":"computer-use:computer-use","arguments":"{\"app\":\"/Applications/Tor Browser.app\"}"},
			{"type":"function_call_output","call_id":"tooluse_xxx","output":"unsupported call: computer-use:computer-use"}
		],
		"tools": []
	}`)
	out := ConvertOpenAIResponsesRequestToClaude("kiro-api/claude-opus-4-7-thinking", body, false)
	gj := gjson.ParseBytes(out)
	msgs := gj.Get("messages")
	if !msgs.IsArray() {
		t.Fatalf("no messages produced: %s", string(out))
	}
	found := false
	msgs.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "assistant" {
			return true
		}
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, blk gjson.Result) bool {
			if blk.Get("type").String() == "tool_use" {
				name := blk.Get("name").String()
				if strings.ContainsAny(name, ":.") {
					t.Errorf("tool_use.name still contains illegal char: %q", name)
				}
				if name == "computer-use__computer-use" {
					found = true
				}
			}
			return true
		})
		return true
	})
	if !found {
		t.Errorf("did not find sanitized tool_use name; full out=%s", string(out))
	}
}
