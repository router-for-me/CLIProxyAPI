package executor

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestEnsureCacheControl(t *testing.T) {
	// Test Fall 1: System Prompt als String
	t.Run("String System Prompt", func(t *testing.T) {
		input := []byte(`{"model": "claude-3-5-sonnet", "system": "Dies ist ein langer System Prompt", "messages": []}`)
		output := ensureCacheControl(input)

		res := gjson.GetBytes(output, "system.0.cache_control.type")
		if res.String() != "ephemeral" {
			t.Errorf("cache_control nicht im System-String gefunden. Output: %s", string(output))
		}
	})

	// Test Fall 2: System Prompt als Array
	t.Run("Array System Prompt", func(t *testing.T) {
		input := []byte(`{"model": "claude-3-5-sonnet", "system": [{"type": "text", "text": "Teil 1"}, {"type": "text", "text": "Teil 2"}], "messages": []}`)
		output := ensureCacheControl(input)

		// cache_control sollte nur am LETZTEN Element sein
		res0 := gjson.GetBytes(output, "system.0.cache_control")
		res1 := gjson.GetBytes(output, "system.1.cache_control.type")

		if res0.Exists() {
			t.Errorf("cache_control sollte NICHT am ersten Element sein")
		}
		if res1.String() != "ephemeral" {
			t.Errorf("cache_control nicht am letzten System-Element gefunden. Output: %s", string(output))
		}
	})

	// Test Fall 3: Tools werden gecached
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

		// cache_control sollte nur am LETZTEN Tool sein
		tool0Cache := gjson.GetBytes(output, "tools.0.cache_control")
		tool1Cache := gjson.GetBytes(output, "tools.1.cache_control.type")

		if tool0Cache.Exists() {
			t.Errorf("cache_control sollte NICHT am ersten Tool sein")
		}
		if tool1Cache.String() != "ephemeral" {
			t.Errorf("cache_control nicht am letzten Tool gefunden. Output: %s", string(output))
		}

		// System sollte auch cache_control haben
		systemCache := gjson.GetBytes(output, "system.0.cache_control.type")
		if systemCache.String() != "ephemeral" {
			t.Errorf("cache_control nicht im System gefunden. Output: %s", string(output))
		}
	})

	// Test Fall 4: Tools und System sind UNABHÄNGIGE Breakpoints
	// Per Anthropic Docs: Bis zu 4 Breakpoints erlaubt, Tools und System werden separat gecached
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

		// Tool hat bereits cache_control - sollte nicht geändert werden
		tool0Cache := gjson.GetBytes(output, "tools.0.cache_control.type")
		if tool0Cache.String() != "ephemeral" {
			t.Errorf("Existierendes cache_control wurde fälschlicherweise entfernt")
		}

		// System SOLLTE cache_control bekommen, weil es ein UNABHÄNGIGER Breakpoint ist
		// Tools und System sind separate Cache-Ebenen in der Hierarchie
		systemCache := gjson.GetBytes(output, "system.0.cache_control.type")
		if systemCache.String() != "ephemeral" {
			t.Errorf("System sollte eigenen cache_control Breakpoint haben (unabhängig von Tools)")
		}
	})

	// Test Fall 5: Nur Tools, kein System
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
			t.Errorf("cache_control nicht am Tool gefunden. Output: %s", string(output))
		}
	})

	// Test Fall 6: Viele Tools (Claude Code Szenario)
	t.Run("Many Tools (Claude Code Scenario)", func(t *testing.T) {
		// Simuliere Claude Code mit vielen Tools
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

		// Nur das letzte Tool (index 49) sollte cache_control haben
		for i := 0; i < 49; i++ {
			path := fmt.Sprintf("tools.%d.cache_control", i)
			if gjson.GetBytes(output, path).Exists() {
				t.Errorf("Tool %d sollte KEIN cache_control haben", i)
			}
		}

		lastToolCache := gjson.GetBytes(output, "tools.49.cache_control.type")
		if lastToolCache.String() != "ephemeral" {
			t.Errorf("Letztes Tool (49) hat kein cache_control")
		}

		// System sollte auch cache_control haben
		systemCache := gjson.GetBytes(output, "system.0.cache_control.type")
		if systemCache.String() != "ephemeral" {
			t.Errorf("System hat kein cache_control")
		}

		fmt.Println("Test 6 (50 Tools) erfolgreich - cache_control nur am letzten Tool!")
	})

	// Test Fall 7: Leeres Tools-Array
	t.Run("Empty Tools Array", func(t *testing.T) {
		input := []byte(`{"model": "claude-3-5-sonnet", "tools": [], "system": "Test", "messages": []}`)
		output := ensureCacheControl(input)

		// System sollte trotzdem cache_control bekommen
		systemCache := gjson.GetBytes(output, "system.0.cache_control.type")
		if systemCache.String() != "ephemeral" {
			t.Errorf("System sollte cache_control haben auch bei leerem Tools-Array")
		}
	})
}

// TestCacheControlOrder prüft die korrekte Reihenfolge: tools -> system -> messages
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

	// Verifiziere die Cache-Breakpoints
	// 1. Letztes Tool hat cache_control
	if gjson.GetBytes(output, "tools.1.cache_control.type").String() != "ephemeral" {
		t.Error("Letztes Tool sollte cache_control haben")
	}

	// 2. Erstes Tool hat KEIN cache_control
	if gjson.GetBytes(output, "tools.0.cache_control").Exists() {
		t.Error("Erstes Tool sollte KEIN cache_control haben")
	}

	// 3. Letztes System-Element hat cache_control
	if gjson.GetBytes(output, "system.1.cache_control.type").String() != "ephemeral" {
		t.Error("Letztes System-Element sollte cache_control haben")
	}

	// 4. Erstes System-Element hat KEIN cache_control
	if gjson.GetBytes(output, "system.0.cache_control").Exists() {
		t.Error("Erstes System-Element sollte KEIN cache_control haben")
	}

	fmt.Println("Cache-Reihenfolge korrekt: tools -> system")
}
