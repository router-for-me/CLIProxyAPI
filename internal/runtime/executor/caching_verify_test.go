package executor

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

func TestEnsureCacheControl(t *testing.T) {
	// Test case 1: System prompt as string
	t.Run("String System Prompt", func(t *testing.T) {
		input := []byte(`{"model": "claude-3-5-sonnet", "system": "This is a long system prompt", "messages": []}`)
		output := ensureCacheControl(input)

		res := gjson.GetBytes(output, "system.0.cache_control.type")
		if res.String() != "ephemeral" {
			t.Errorf("cache_control not found in system string. Output: %s", string(output))
		}
	})

	// Test case 2: System prompt as array
	t.Run("Array System Prompt", func(t *testing.T) {
		input := []byte(`{"model": "claude-3-5-sonnet", "system": [{"type": "text", "text": "Part 1"}, {"type": "text", "text": "Part 2"}], "messages": []}`)
		output := ensureCacheControl(input)

		// cache_control should only be on the LAST element
		res0 := gjson.GetBytes(output, "system.0.cache_control")
		res1 := gjson.GetBytes(output, "system.1.cache_control.type")

		if res0.Exists() {
			t.Errorf("cache_control should NOT be on the first element")
		}
		if res1.String() != "ephemeral" {
			t.Errorf("cache_control not found on last system element. Output: %s", string(output))
		}
	})

	// Test case 3: Native Claude Code does not auto-stamp tools; system still caches.
	t.Run("Tools Not Auto Cached", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-3-5-sonnet",
			"tools": [
				{"name": "tool1", "description": "First tool", "input_schema": {"type": "object"}},
				{"name": "tool2", "description": "Second tool", "input_schema": {"type": "object"}}
			],
			"system": "System prompt",
			"messages": []
		}`)
		output := ensureCacheControl(input)

		if gjson.GetBytes(output, "tools.0.cache_control").Exists() || gjson.GetBytes(output, "tools.1.cache_control").Exists() {
			t.Errorf("default ensureCacheControl must not stamp tools[*].cache_control: %s", string(output))
		}

		systemCache := gjson.GetBytes(output, "system.0.cache_control")
		if systemCache.Get("type").String() != "ephemeral" {
			t.Errorf("cache_control not found in system. Output: %s", string(output))
		}
		// The native constructor spreads ttl in only when a ttl is selected, so the
		// default breakpoint carries none. upgradeClaudeCacheControlTTL adds it later
		// for the credentials native uses the 1h pool on.
		if systemCache.Get("ttl").Exists() {
			t.Errorf("default system cache_control must not carry ttl. Output: %s", string(output))
		}
	})

	// Test case 4: Tools and system are INDEPENDENT breakpoints
	// Per Anthropic docs: Up to 4 breakpoints allowed, tools and system are cached separately
	t.Run("Independent Cache Breakpoints", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-3-5-sonnet",
			"tools": [
				{"name": "tool1", "description": "First tool", "input_schema": {"type": "object"}, "cache_control": {"type": "ephemeral"}}
			],
			"system": [{"type": "text", "text": "System"}],
			"messages": []
		}`)
		output := ensureCacheControl(input)

		// Tool already has cache_control - should not be changed
		tool0Cache := gjson.GetBytes(output, "tools.0.cache_control.type")
		if tool0Cache.String() != "ephemeral" {
			t.Errorf("existing cache_control was incorrectly removed")
		}

		// System SHOULD get cache_control because it is an INDEPENDENT breakpoint
		// Tools and system are separate cache levels in the hierarchy
		systemCache := gjson.GetBytes(output, "system.0.cache_control.type")
		if systemCache.String() != "ephemeral" {
			t.Errorf("system should have its own cache_control breakpoint (independent of tools)")
		}
	})

	// Test case 5: tools without any system prompt. Native always sends a system
	// prompt, so this shape only reaches CPA from OpenAI/Gemini translation where the
	// caller supplied no system message. Without a tools breakpoint the sole marker
	// would sit on the volatile final message and a stateless caller would rewrite
	// the whole tools prefix on every request.
	t.Run("Only Tools No System Falls Back To Tools Breakpoint", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-3-5-sonnet",
			"tools": [
				{"name": "tool1", "description": "Tool", "input_schema": {"type": "object"}},
				{"name": "tool2", "description": "Tool", "input_schema": {"type": "object"}}
			],
			"messages": [{"role": "user", "content": "Hi"}]
		}`)
		output := ensureCacheControl(input)

		if gjson.GetBytes(output, "tools.0.cache_control").Exists() {
			t.Errorf("only the last tool may host the fallback breakpoint: %s", string(output))
		}
		if got := gjson.GetBytes(output, "tools.1.cache_control.type").String(); got != "ephemeral" {
			t.Errorf("missing tools fallback breakpoint when system is absent: %s", string(output))
		}
		if gjson.GetBytes(output, "tools.1.cache_control.ttl").Exists() {
			t.Errorf("tools fallback breakpoint must not carry a default ttl: %s", string(output))
		}
		if got := gjson.GetBytes(output, "messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
			t.Errorf("rolling message breakpoint should still be present: %s", string(output))
		}
	})

	t.Run("Empty System Still Falls Back To Tools", func(t *testing.T) {
		for name, system := range map[string]string{
			"empty array":  `"system": [],`,
			"empty string": `"system": "",`,
			"blank string": `"system": "   ",`,
		} {
			t.Run(name, func(t *testing.T) {
				input := []byte(`{
					"model": "claude-3-5-sonnet",
					` + system + `
					"tools": [{"name": "tool1", "description": "Tool", "input_schema": {"type": "object"}}],
					"messages": [{"role": "user", "content": "Hi"}]
				}`)
				output := ensureCacheControl(input)

				if got := gjson.GetBytes(output, "tools.0.cache_control.type").String(); got != "ephemeral" {
					t.Errorf("an unusable system prompt must still yield a tools breakpoint: %s", string(output))
				}
				// Empty/blank string system must not be rewritten into a marked text
				// block; that would double-stamp tools + a whitespace system host.
				if bytes.Contains(output, []byte(`"text":""`)) || bytes.Contains(output, []byte(`"text":"   "`)) {
					t.Errorf("unusable string system must stay unconverted: %s", string(output))
				}
				if gjson.GetBytes(output, "system.0.cache_control").Exists() {
					t.Errorf("unusable system must not receive its own breakpoint: %s", string(output))
				}
				if countCacheControls(output) != 2 {
					t.Errorf("want tools+message breakpoints only, got %d in %s", countCacheControls(output), string(output))
				}
			})
		}
	})

	// Test case 6: Many tools (Claude Code scenario) — default skips tools.
	t.Run("Many Tools (Claude Code Scenario)", func(t *testing.T) {
		// Simulate Claude Code with many tools
		toolsJSON := `[`
		for i := 0; i < 50; i++ {
			if i > 0 {
				toolsJSON += ","
			}
			toolsJSON += fmt.Sprintf(`{"name": "tool%d", "description": "Tool %d", "input_schema": {"type": "object"}}`, i, i)
		}
		toolsJSON += `]`

		input := []byte(fmt.Sprintf(`{
			"model": "claude-3-5-sonnet",
			"tools": %s,
			"system": [{"type": "text", "text": "You are Claude Code"}],
			"messages": [{"role": "user", "content": "Hello"}]
		}`, toolsJSON))

		output := ensureCacheControl(input)

		for i := 0; i < 50; i++ {
			path := fmt.Sprintf("tools.%d.cache_control", i)
			if gjson.GetBytes(output, path).Exists() {
				t.Errorf("tool %d should NOT have cache_control under default ensure", i)
			}
		}

		helperOut := injectToolsCacheControl(input)
		if got := gjson.GetBytes(helperOut, "tools.49.cache_control.type").String(); got != "ephemeral" {
			t.Errorf("injectToolsCacheControl should still mark last tool")
		}

		if got := gjson.GetBytes(output, "system.0.cache_control.type").String(); got != "ephemeral" {
			t.Errorf("system should have cache_control, got %q", got)
		}
		if got := gjson.GetBytes(output, "messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
			t.Errorf("latest user should have cache_control, got %q", got)
		}
	})

	// Test case 7: Empty tools array
	t.Run("Empty Tools Array", func(t *testing.T) {
		input := []byte(`{"model": "claude-3-5-sonnet", "tools": [], "system": "Test", "messages": []}`)
		output := ensureCacheControl(input)

		// System should still get cache_control
		systemCache := gjson.GetBytes(output, "system.0.cache_control.type")
		if systemCache.String() != "ephemeral" {
			t.Errorf("system should have cache_control even with empty tools array")
		}
	})

	// Test case 8: Messages caching follows native Claude Code (latest user turn).
	t.Run("Messages Caching Latest User", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-3-5-sonnet",
			"messages": [
				{"role": "user", "content": "First user"},
				{"role": "assistant", "content": "Assistant reply"},
				{"role": "user", "content": "Second user"},
				{"role": "assistant", "content": "Assistant reply 2"},
				{"role": "user", "content": "Third user"}
			]
		}`)
		output := ensureCacheControl(input)

		if got := gjson.GetBytes(output, "messages.4.content.0.cache_control.type").String(); got != "ephemeral" {
			t.Errorf("cache_control.type on latest user = %q, want ephemeral. Output: %s", got, string(output))
		}
		if gjson.GetBytes(output, "messages.4.content.0.cache_control.ttl").Exists() {
			t.Errorf("default rolling marker must not carry ttl. Output: %s", string(output))
		}
		if gjson.GetBytes(output, "messages.2.content.0.cache_control").Exists() {
			t.Errorf("second-to-last user turn should NOT have cache_control; native Claude Code rolls onto the latest user")
		}
	})

	// The native final-system special case is narrow: it requires non-empty STRING
	// content and replaces it with a single freshly marked text block.
	t.Run("Messages Caching Trailing System String", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-3-5-sonnet",
			"messages": [
				{"role": "user", "content": "User"},
				{"role": "assistant", "content": "Assistant"},
				{"role": "system", "content": "Internal system"}
			]
		}`)
		output := ensureCacheControl(input)

		systemContent := gjson.GetBytes(output, "messages.2.content")
		if !systemContent.IsArray() || len(systemContent.Array()) != 1 {
			t.Fatalf("trailing string system was not replaced by a single text block: %s", output)
		}
		if got := systemContent.Get("0.text").String(); got != "Internal system" {
			t.Fatalf("trailing system text = %q, want the original string: %s", got, output)
		}
		if got := systemContent.Get("0.cache_control.type").String(); got != "ephemeral" {
			t.Fatalf("trailing string system did not take the native special case: %s", output)
		}
		if gjson.GetBytes(output, "messages.1.content.0.cache_control").Exists() {
			t.Fatalf("preceding assistant must not also receive the rolling marker: %s", output)
		}
	})

	// An array-content trailing system turn is NOT the native special case: native
	// requires string content there, so the marker falls back to the last eligible
	// user/assistant turn instead.
	t.Run("Messages Caching Trailing System Array Falls Back", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-3-5-sonnet",
			"messages": [
				{"role": "user", "content": "User"},
				{"role": "assistant", "content": "Assistant"},
				{"role": "system", "content": [{"type": "text", "text": "Internal 1"}, {"type": "text", "text": "Internal 2"}]}
			]
		}`)
		output := ensureCacheControl(input)

		if gjson.GetBytes(output, "messages.2.content.1.cache_control").Exists() {
			t.Fatalf("array-content trailing system must not be marked; native requires string content: %s", output)
		}
		if got := gjson.GetBytes(output, "messages.1.content.0.cache_control.type").String(); got != "ephemeral" {
			t.Fatalf("marker should fall back to the last eligible assistant turn: %s", output)
		}
	})

	t.Run("Messages Caching Trailing Assistant Text", func(t *testing.T) {
		input := []byte(`{
			"messages": [
				{"role": "user", "content": "User"},
				{"role": "assistant", "content": "Assistant prefill"}
			]
		}`)
		output := ensureCacheControl(input)

		if got := gjson.GetBytes(output, "messages.1.content.0.cache_control.type").String(); got != "ephemeral" {
			t.Fatalf("trailing assistant cache_control.type = %q, want ephemeral. Output: %s", got, output)
		}
		wantAssistant := []byte(`[{"type":"text","text":"Assistant prefill","cache_control":{"type":"ephemeral"}}]`)
		if !bytes.Contains(output, wantAssistant) {
			t.Fatalf("assistant string promotion does not match native order: %s", output)
		}
		if gjson.GetBytes(output, "messages.0.content.0.cache_control").Exists() {
			t.Fatalf("preceding user must not receive an assistant rolling marker: %s", output)
		}
	})

	t.Run("Messages Skip Trailing Assistant Thinking", func(t *testing.T) {
		input := []byte(`{
			"messages": [
				{"role": "user", "content": "User"},
				{"role": "system", "content": "Internal system"},
				{"role": "assistant", "content": [
					{"type": "text", "text": "Assistant"},
					{"type": "thinking", "thinking": "Internal"}
				]}
			]
		}`)
		output := ensureCacheControl(input)

		if got := gjson.GetBytes(output, "messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
			t.Fatalf("preceding user cache_control.type = %q, want fallback ephemeral. Output: %s", got, output)
		}
		if got := gjson.GetBytes(output, "messages.1.content"); got.Type != gjson.String {
			t.Fatalf("internal system message was rewritten instead of skipped: %s", output)
		}
		if gjson.GetBytes(output, "messages.2.content.1.cache_control").Exists() {
			t.Fatalf("assistant thinking block must not receive cache_control: %s", output)
		}
	})

	// Test case 9: Cloaking first-user marker must not suppress latest-user rolling write.
	t.Run("Messages Inject Despite Cloaking First User Marker", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-3-5-sonnet",
			"tools": [{"name": "Read", "description": "read", "input_schema": {"type": "object"}}],
			"system": "You are helpful.",
			"messages": [
				{"role": "user", "content": [{"type": "text", "text": "currentDate"}, {"type": "text", "text": "First user", "cache_control": {"type": "ephemeral"}}]},
				{"role": "assistant", "content": [{"type": "text", "text": "Assistant reply"}]},
				{"role": "user", "content": [{"type": "text", "text": "Second user"}]},
				{"role": "assistant", "content": [{"type": "text", "text": "Assistant reply 2"}]},
				{"role": "user", "content": [{"type": "text", "text": "Third user"}]}
			]
		}`)
		output := ensureCacheControl(input)

		if got := gjson.GetBytes(output, "messages.0.content.1.cache_control.type").String(); got != "ephemeral" {
			t.Errorf("cloaking first-user marker lost: %s", string(output))
		}
		if got := gjson.GetBytes(output, "messages.4.content.0.cache_control.type").String(); got != "ephemeral" {
			t.Errorf("latest user missing rolling cache_control after cloaking marker. Output: %s", string(output))
		}
		if gjson.GetBytes(output, "tools.0.cache_control").Exists() {
			t.Errorf("a payload with a system prompt must not stamp tools[*].cache_control: %s", string(output))
		}
		if got := gjson.GetBytes(output, "system.0.cache_control.type").String(); got != "ephemeral" {
			t.Errorf("system should still receive independent cache_control. Output: %s", string(output))
		}
	})

	// Test case 10: Existing marker on the latest user turn is preserved / not duplicated.
	t.Run("Messages Skip When Latest User Already Has Cache Control", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-3-5-sonnet",
			"messages": [
				{"role": "user", "content": [{"type": "text", "text": "First user"}]},
				{"role": "assistant", "content": [{"type": "text", "text": "Assistant reply"}]},
				{"role": "user", "content": [{"type": "text", "text": "Second user", "cache_control": {"type": "ephemeral", "ttl": "1h"}}]}
			]
		}`)
		output := ensureCacheControl(input)

		if got := gjson.GetBytes(output, "messages.2.content.0.cache_control.ttl").String(); got != "1h" {
			t.Errorf("existing latest-user cache_control.ttl = %q, want 1h. Output: %s", got, string(output))
		}
		if gjson.GetBytes(output, "messages.0.content.0.cache_control").Exists() {
			t.Errorf("should not invent an extra message breakpoint on the first user when latest already has one")
		}
	})

	// Test case 11: Generated cache controls preserve native JSON property order.
	t.Run("Native Cache Control Wire Order", func(t *testing.T) {
		input := []byte(`{
			"system": [{"type": "text", "text": "System"}],
			"messages": [{"role": "user", "content": [{"type": "text", "text": "User"}]}]
		}`)
		output := ensureCacheControl(input)
		want := []byte(`"cache_control":{"type":"ephemeral"}`)
		if got := bytes.Count(output, want); got != 2 {
			t.Fatalf("native cache_control wire shape count = %d, want 2. Output: %s", got, output)
		}

		upgraded := upgradeClaudeCacheControlTTL(output, claudeCacheControlTTL1h)
		wantUpgraded := []byte(`"cache_control":{"type":"ephemeral","ttl":"1h"}`)
		if got := bytes.Count(upgraded, wantUpgraded); got != 2 {
			t.Fatalf("upgraded cache_control wire shape count = %d, want 2. Output: %s", got, upgraded)
		}
		if bytes.Contains(upgraded, []byte(`"cache_control":{"ttl":"1h","type":"ephemeral"}`)) {
			t.Fatalf("cache_control keys emitted in non-native order: %s", upgraded)
		}
	})

	t.Run("String Promotion Native Parent Order", func(t *testing.T) {
		input := []byte(`{"system":"System <tag> &","messages":[{"role":"user","content":"User <tag> &"}]}`)
		output := ensureCacheControl(input)
		wantSystem := []byte(`"system":[{"type":"text","text":"System <tag> &","cache_control":{"type":"ephemeral"}}]`)
		wantMessage := []byte(`"content":[{"type":"text","text":"User <tag> &","cache_control":{"type":"ephemeral"}}]`)
		if !bytes.Contains(output, wantSystem) || !bytes.Contains(output, wantMessage) {
			t.Fatalf("string promotion does not match native parent/key escaping order: %s", output)
		}
		if bytes.Contains(output, []byte(`\u003c`)) || bytes.Contains(output, []byte(`\u003e`)) || bytes.Contains(output, []byte(`\u0026`)) {
			t.Fatalf("string promotion introduced HTML escaping: %s", output)
		}
	})

	t.Run("Existing Global Scope Preserved", func(t *testing.T) {
		input := []byte(`{"system":[{"type":"text","text":"Global","cache_control":{"type":"ephemeral","ttl":"1h","scope":"global"}}],"messages":[{"role":"user","content":"User"}]}`)
		output := ensureCacheControl(input)
		want := []byte(`"cache_control":{"type":"ephemeral","ttl":"1h","scope":"global"}`)
		if !bytes.Contains(output, want) {
			t.Fatalf("existing native global scope marker changed: %s", output)
		}
	})
}

func TestShouldEnsureCacheControl(t *testing.T) {
	markerless := []byte(`{"messages":[{"role":"user","content":"x"}]}`)
	withMarker := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}]}]}`)
	tests := []struct {
		name                string
		payload             []byte
		cloaked             bool
		confirmedClaudeCode bool
		want                bool
	}{
		{name: "confirmed native markerless", payload: markerless, confirmedClaudeCode: true, want: false},
		{name: "confirmed native with marker", payload: withMarker, confirmedClaudeCode: true, want: false},
		{name: "cloaked with marker", payload: withMarker, cloaked: true, want: true},
		{name: "unconfirmed markerless", payload: markerless, want: true},
		{name: "unconfirmed with marker", payload: withMarker, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldEnsureCacheControl(tt.payload, tt.cloaked, tt.confirmedClaudeCode); got != tt.want {
				t.Fatalf("shouldEnsureCacheControl() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestInjectToolsCacheControlSkipsDeferredTools(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantCacheIndex int
		wantCacheTTL   string
	}{
		{
			name: "trailing deferred tool",
			input: `{"tools":[
				{"name":"resident","defer_loading":false},
				{"name":"deferred","defer_loading":true}
			]}`,
			wantCacheIndex: 0,
		},
		{
			name: "multiple trailing deferred tools",
			input: `{"tools":[
				{"name":"resident"},
				{"name":"deferred_1","defer_loading":true},
				{"name":"deferred_2","defer_loading":true}
			]}`,
			wantCacheIndex: 0,
		},
		{
			name: "middle deferred tool",
			input: `{"tools":[
				{"name":"resident_1"},
				{"name":"deferred","defer_loading":true},
				{"name":"resident_2"}
			]}`,
			wantCacheIndex: 2,
		},
		{
			name: "all tools deferred",
			input: `{"tools":[
				{"name":"deferred_1","defer_loading":true},
				{"name":"deferred_2","defer_loading":true}
			]}`,
			wantCacheIndex: -1,
		},
		{
			name: "existing cache control",
			input: `{"tools":[
				{"name":"resident_1","cache_control":{"type":"ephemeral","ttl":"1h"}},
				{"name":"resident_2"}
			]}`,
			wantCacheIndex: 0,
			wantCacheTTL:   "1h",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := injectToolsCacheControl([]byte(tt.input))
			tools := gjson.GetBytes(output, "tools").Array()
			cacheCount := 0
			for index, tool := range tools {
				cacheControl := tool.Get("cache_control")
				if cacheControl.Exists() {
					cacheCount++
					if index != tt.wantCacheIndex {
						t.Errorf("cache_control added to tool %d, want tool %d: %s", index, tt.wantCacheIndex, string(output))
					}
				}
				if tool.Get("defer_loading").Bool() && cacheControl.Exists() {
					t.Errorf("deferred tool %d must not have cache_control: %s", index, string(output))
				}
			}

			wantCacheCount := 1
			if tt.wantCacheIndex < 0 {
				wantCacheCount = 0
			}
			if cacheCount != wantCacheCount {
				t.Errorf("cache_control count = %d, want %d: %s", cacheCount, wantCacheCount, string(output))
			}
			if tt.wantCacheTTL != "" {
				path := fmt.Sprintf("tools.%d.cache_control.ttl", tt.wantCacheIndex)
				if got := gjson.GetBytes(output, path).String(); got != tt.wantCacheTTL {
					t.Errorf("cache_control TTL = %q, want %q: %s", got, tt.wantCacheTTL, string(output))
				}
			}
		})
	}
}

// TestCacheControlOrder verifies the correct order: tools -> system -> messages
func TestCacheControlOrder(t *testing.T) {
	input := []byte(`{
		"model": "claude-sonnet-4",
		"tools": [
			{"name": "Read", "description": "Read file", "input_schema": {"type": "object", "properties": {"path": {"type": "string"}}}},
			{"name": "Write", "description": "Write file", "input_schema": {"type": "object", "properties": {"path": {"type": "string"}, "content": {"type": "string"}}}}
		],
		"system": [
			{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
			{"type": "text", "text": "Additional instructions here..."}
		],
		"messages": [
			{"role": "user", "content": "Hello"}
		]
	}`)

	output := ensureCacheControl(input)

	// Native default path does not stamp tools.
	if gjson.GetBytes(output, "tools.0.cache_control").Exists() || gjson.GetBytes(output, "tools.1.cache_control").Exists() {
		t.Error("default ensureCacheControl must not stamp tools[*].cache_control")
	}

	// Last system element has the default cache_control, which carries no ttl.
	if gjson.GetBytes(output, "system.1.cache_control.type").String() != "ephemeral" {
		t.Error("last system element should have cache_control")
	}
	if gjson.GetBytes(output, "system.1.cache_control.ttl").Exists() {
		t.Error("default last system element must not carry a ttl")
	}
	if got := gjson.GetBytes(upgradeClaudeCacheControlTTL(output, claudeCacheControlTTL1h), "system.1.cache_control.ttl").String(); got != "1h" {
		t.Errorf("upgraded last system element ttl = %q, want 1h", got)
	}

	// First system element has NO cache_control
	if gjson.GetBytes(output, "system.0.cache_control").Exists() {
		t.Error("first system element should NOT have cache_control")
	}
}

// The native ttl helper only touches blocks that already carry a cache_control and
// have no ttl yet. It must never create a breakpoint, because placement is decided
// by ensureCacheControl before this step runs.
func TestUpgradeClaudeCacheControlTTL(t *testing.T) {
	t.Run("Upgrades Only Existing Markers", func(t *testing.T) {
		input := []byte(`{` +
			`"tools":[{"name":"t","cache_control":{"type":"ephemeral"}},{"name":"u"}],` +
			`"system":[{"type":"text","text":"s0"},{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}],` +
			`"messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b","cache_control":{"type":"ephemeral"}}]}]}`)
		output := upgradeClaudeCacheControlTTL(input, claudeCacheControlTTL1h)

		for _, path := range []string{"tools.0", "system.1", "messages.0.content.1"} {
			if got := gjson.GetBytes(output, path+".cache_control.ttl").String(); got != "1h" {
				t.Errorf("%s.cache_control.ttl = %q, want 1h. Output: %s", path, got, output)
			}
		}
		for _, path := range []string{"tools.1", "system.0", "messages.0.content.0"} {
			if gjson.GetBytes(output, path+".cache_control").Exists() {
				t.Errorf("%s must not gain a cache_control: %s", path, output)
			}
		}
		if got := countCacheControls(output); got != 3 {
			t.Errorf("breakpoint count = %d, want the original 3", got)
		}
	})

	t.Run("Preserves Caller TTL And Is Idempotent", func(t *testing.T) {
		input := []byte(`{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"5m"}}]}`)
		output := upgradeClaudeCacheControlTTL(input, claudeCacheControlTTL1h)
		if got := gjson.GetBytes(output, "system.0.cache_control.ttl").String(); got != "5m" {
			t.Errorf("existing ttl = %q, want the caller's 5m to survive", got)
		}

		once := upgradeClaudeCacheControlTTL([]byte(`{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}]}`), claudeCacheControlTTL1h)
		twice := upgradeClaudeCacheControlTTL(once, claudeCacheControlTTL1h)
		if !bytes.Equal(once, twice) {
			t.Errorf("upgrade is not idempotent: %s vs %s", once, twice)
		}
	})

	t.Run("Keeps Native Key Order With Scope", func(t *testing.T) {
		input := []byte(`{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","scope":"global"}}]}`)
		output := upgradeClaudeCacheControlTTL(input, claudeCacheControlTTL1h)
		want := []byte(`"cache_control":{"type":"ephemeral","ttl":"1h","scope":"global"}`)
		if !bytes.Contains(output, want) {
			t.Errorf("scope-bearing marker lost native {type, ttl, scope} order: %s", output)
		}
	})

	t.Run("No TTL Is A No-op", func(t *testing.T) {
		input := []byte(`{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}]}`)
		if output := upgradeClaudeCacheControlTTL(input, ""); !bytes.Equal(output, input) {
			t.Errorf("empty ttl must be a no-op: %s", output)
		}
		if output := upgradeClaudeCacheControlTTL([]byte(`not json`), claudeCacheControlTTL1h); string(output) != "not json" {
			t.Errorf("invalid payload must be returned untouched: %s", output)
		}
	})
}

// End-to-end guard for #4855. Cloaking stamps the first real user block, and the
// old global `countCacheControls(body) == 0` gate then skipped every remaining
// section, freezing the rolling breakpoint on messages[0] for the whole
// conversation. Section-independent ensure has to keep that breakpoint advancing
// as the history grows, so a reintroduced global short-circuit fails here.
func TestClaudeExecutorCloakedRollingCacheBreakpointAdvances(t *testing.T) {
	buildConversation := func(exchanges int) []byte {
		messages := make([]string, 0, exchanges*2)
		for i := 0; i < exchanges; i++ {
			messages = append(messages,
				fmt.Sprintf(`{"role":"user","content":"question number %d"}`, i),
				fmt.Sprintf(`{"role":"assistant","content":"answer number %d"}`, i),
			)
		}
		return []byte(`{"model":"claude-opus-5","max_tokens":100,` +
			`"system":"You are a helpful assistant.",` +
			`"messages":[` + strings.Join(messages, ",") + `]}`)
	}

	// lastMarkedMessage reports the highest message index carrying a breakpoint.
	lastMarkedMessage := func(body []byte) int {
		last := -1
		gjson.GetBytes(body, "messages").ForEach(func(msgIdx, message gjson.Result) bool {
			message.Get("content").ForEach(func(_, block gjson.Result) bool {
				if block.Get("cache_control").Exists() {
					last = int(msgIdx.Int())
				}
				return true
			})
			return true
		})
		return last
	}

	cfg := &config.Config{}
	shortBody := executeClaudeContextManagementRequest(t, cfg, buildConversation(2), false)
	longBody := executeClaudeContextManagementRequest(t, cfg, buildConversation(6), false)

	shortMarked := lastMarkedMessage(shortBody)
	longMarked := lastMarkedMessage(longBody)
	if shortMarked <= 0 {
		t.Fatalf("short conversation kept its only breakpoint at index %d: %s", shortMarked, shortBody)
	}
	if longMarked <= shortMarked {
		t.Fatalf("rolling breakpoint did not advance with history: short=%d long=%d\n%s", shortMarked, longMarked, longBody)
	}
	// The rolling marker must land on the final turn, not an early frozen prefix.
	if want := int(gjson.GetBytes(longBody, "messages.#").Int()) - 1; longMarked != want {
		t.Fatalf("rolling breakpoint at message %d, want final message %d: %s", longMarked, want, longBody)
	}
	// Cloaking's own first-user marker must still be present alongside it.
	if !gjson.GetBytes(longBody, "messages.0.content.1.cache_control").Exists() {
		t.Fatalf("cloak first-user breakpoint lost: %s", longBody)
	}
	if total := countCacheControls(longBody); total > 4 {
		t.Fatalf("cache_control count = %d, want at most 4: %s", total, longBody)
	}
}
