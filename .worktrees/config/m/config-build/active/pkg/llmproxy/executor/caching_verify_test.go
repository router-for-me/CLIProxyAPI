package executor

import (
	"fmt"
	"testing"

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

	// Test case 3: Tools are cached
	t.Run("Tools Caching", func(t *testing.T) {
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

		// cache_control should only be on the LAST tool
		tool0Cache := gjson.GetBytes(output, "tools.0.cache_control")
		tool1Cache := gjson.GetBytes(output, "tools.1.cache_control.type")

		if tool0Cache.Exists() {
			t.Errorf("cache_control should NOT be on the first tool")
		}
		if tool1Cache.String() != "ephemeral" {
			t.Errorf("cache_control not found on last tool. Output: %s", string(output))
		}

		// System should also have cache_control
		systemCache := gjson.GetBytes(output, "system.0.cache_control.type")
		if systemCache.String() != "ephemeral" {
			t.Errorf("cache_control not found in system. Output: %s", string(output))
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

	// Test case 5: Only tools, no system
	t.Run("Only Tools No System", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-3-5-sonnet",
			"tools": [
				{"name": "tool1", "description": "Tool", "input_schema": {"type": "object"}}
			],
			"messages": [{"role": "user", "content": "Hi"}]
		}`)
		output := ensureCacheControl(input)

		toolCache := gjson.GetBytes(output, "tools.0.cache_control.type")
		if toolCache.String() != "ephemeral" {
			t.Errorf("cache_control not found on tool. Output: %s", string(output))
		}
	})

	// Test case 6: Many tools (Claude Code scenario)
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

		// Only the last tool (index 49) should have cache_control
		for i := 0; i < 49; i++ {
			path := fmt.Sprintf("tools.%d.cache_control", i)
			if gjson.GetBytes(output, path).Exists() {
				t.Errorf("tool %d should NOT have cache_control", i)
			}
		}

		lastToolCache := gjson.GetBytes(output, "tools.49.cache_control.type")
		if lastToolCache.String() != "ephemeral" {
			t.Errorf("last tool (49) should have cache_control")
		}

		// System should also have cache_control
		systemCache := gjson.GetBytes(output, "system.0.cache_control.type")
		if systemCache.String() != "ephemeral" {
			t.Errorf("system should have cache_control")
		}

		t.Log("test passed: 50 tools - cache_control only on last tool")
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

	// Test case 8: Messages caching for multi-turn (second-to-last user)
	t.Run("Messages Caching Second-To-Last User", func(t *testing.T) {
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

		cacheType := gjson.GetBytes(output, "messages.2.content.0.cache_control.type")
		if cacheType.String() != "ephemeral" {
			t.Errorf("cache_control not found on second-to-last user turn. Output: %s", string(output))
		}

		lastUserCache := gjson.GetBytes(output, "messages.4.content.0.cache_control")
		if lastUserCache.Exists() {
			t.Errorf("last user turn should NOT have cache_control")
		}
	})

	// Test case 9: Existing message cache_control should skip injection
	t.Run("Messages Skip When Cache Control Exists", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-3-5-sonnet",
			"messages": [
				{"role": "user", "content": [{"type": "text", "text": "First user"}]},
				{"role": "assistant", "content": [{"type": "text", "text": "Assistant reply", "cache_control": {"type": "ephemeral"}}]},
				{"role": "user", "content": [{"type": "text", "text": "Second user"}]}
			]
		}`)
		output := ensureCacheControl(input)

		userCache := gjson.GetBytes(output, "messages.0.content.0.cache_control")
		if userCache.Exists() {
			t.Errorf("cache_control should NOT be injected when a message already has cache_control")
		}

		existingCache := gjson.GetBytes(output, "messages.1.content.0.cache_control.type")
		if existingCache.String() != "ephemeral" {
			t.Errorf("existing cache_control should be preserved. Output: %s", string(output))
		}
	})
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

	// 1. Last tool has cache_control
	if gjson.GetBytes(output, "tools.1.cache_control.type").String() != "ephemeral" {
		t.Error("last tool should have cache_control")
	}

	// 2. First tool has NO cache_control
	if gjson.GetBytes(output, "tools.0.cache_control").Exists() {
		t.Error("first tool should NOT have cache_control")
	}

	// 3. Last system element has cache_control
	if gjson.GetBytes(output, "system.1.cache_control.type").String() != "ephemeral" {
		t.Error("last system element should have cache_control")
	}

	// 4. First system element has NO cache_control
	if gjson.GetBytes(output, "system.0.cache_control").Exists() {
		t.Error("first system element should NOT have cache_control")
	}

	t.Log("cache order correct: tools -> system")
}
