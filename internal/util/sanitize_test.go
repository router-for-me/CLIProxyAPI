package util

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeFunctionName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"Normal", "valid_name", "valid_name"},
		{"With Dots", "name.with.dots", "name.with.dots"},
		{"With Colons", "name:with:colons", "name:with:colons"},
		{"With Dashes", "name-with-dashes", "name-with-dashes"},
		{"Mixed Allowed", "name.with_dots:colons-dashes", "name.with_dots:colons-dashes"},
		{"Invalid Characters", "name!with@invalid#chars", "name_with_invalid_chars"},
		{"Spaces", "name with spaces", "name_with_spaces"},
		{"Non-ASCII", "name_with_你好_chars", "name_with____chars"},
		{"Starts with digit", "123name", "_123name"},
		{"Starts with dot", ".name", "_.name"},
		{"Starts with colon", ":name", "_:name"},
		{"Starts with dash", "-name", "_-name"},
		{"Starts with invalid char", "!name", "_name"},
		{"Exactly 64 chars", "this_is_a_very_long_name_that_exactly_reaches_sixty_four_charact", "this_is_a_very_long_name_that_exactly_reaches_sixty_four_charact"},
		{"Too long (65 chars)", "this_is_a_very_long_name_that_exactly_reaches_sixty_four_charactX", "this_is_a_very_long_name_that_exactly_reaches_sixty_four_charact"},
		{"Very long", "this_is_a_very_long_name_that_exceeds_the_sixty_four_character_limit_for_function_names", "this_is_a_very_long_name_that_exceeds_the_sixty_four_character_l"},
		{"Starts with digit (64 chars total)", "1234567890123456789012345678901234567890123456789012345678901234", "_123456789012345678901234567890123456789012345678901234567890123"},
		{"Starts with invalid char (64 chars total)", "!234567890123456789012345678901234567890123456789012345678901234", "_234567890123456789012345678901234567890123456789012345678901234"},
		{"Empty", "", ""},
		{"Single character invalid", "@", "_"},
		{"Single character valid", "a", "a"},
		{"Single character digit", "1", "_1"},
		{"Single character underscore", "_", "_"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeFunctionName(tt.input)
			if got != tt.expected {
				t.Errorf("SanitizeFunctionName(%q) = %v, want %v", tt.input, got, tt.expected)
			}
			// Verify Gemini compliance
			if len(got) > 64 {
				t.Errorf("SanitizeFunctionName(%q) result too long: %d", tt.input, len(got))
			}
			if len(got) > 0 {
				first := got[0]
				if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || first == '_') {
					t.Errorf("SanitizeFunctionName(%q) result starts with invalid char: %c", tt.input, first)
				}
			}
		})
	}
}

func TestSanitizedToolNameMap(t *testing.T) {
	t.Run("returns map for tools needing sanitization", func(t *testing.T) {
		raw := []byte(`{"tools":[
			{"name":"valid_tool","input_schema":{}},
			{"name":"mcp/server/read","input_schema":{}},
			{"name":"tool@v2","input_schema":{}}
		]}`)
		m := SanitizedToolNameMap(raw)
		if m == nil {
			t.Fatal("expected non-nil map")
		}
		if m["mcp_server_read"] != "mcp/server/read" {
			t.Errorf("expected mcp_server_read → mcp/server/read, got %q", m["mcp_server_read"])
		}
		if m["tool_v2"] != "tool@v2" {
			t.Errorf("expected tool_v2 → tool@v2, got %q", m["tool_v2"])
		}
		if _, exists := m["valid_tool"]; exists {
			t.Error("valid_tool should not be in the map (no sanitization needed)")
		}
	})

	t.Run("returns nil when no tools need sanitization", func(t *testing.T) {
		raw := []byte(`{"tools":[{"name":"Read","input_schema":{}},{"name":"Write","input_schema":{}}]}`)
		m := SanitizedToolNameMap(raw)
		if m != nil {
			t.Errorf("expected nil, got %v", m)
		}
	})

	t.Run("returns nil for empty/missing tools", func(t *testing.T) {
		if m := SanitizedToolNameMap([]byte(`{}`)); m != nil {
			t.Error("expected nil for no tools")
		}
		if m := SanitizedToolNameMap(nil); m != nil {
			t.Error("expected nil for nil input")
		}
	})

	t.Run("legacy map ignores nested OpenAI tools", func(t *testing.T) {
		raw := []byte(`{"tools":[
			{"type":"function","function":{"name":"web/search"}},
			{"type":"web_search","name":"web_search"}
		]}`)
		if m := SanitizedToolNameMap(raw); m != nil {
			t.Fatalf("legacy map = %v, want nil", m)
		}
	})

	t.Run("collision keeps first legacy mapping", func(t *testing.T) {
		raw := []byte(`{"tools":[
			{"name":"read/file","input_schema":{}},
			{"name":"read@file","input_schema":{}}
		]}`)
		m := SanitizedToolNameMap(raw)
		if m == nil {
			t.Fatal("expected non-nil map")
		}
		if got := m["read_file"]; got != "read/file" {
			t.Errorf("legacy collision mapping = %q, want read/file", got)
		}
	})
}

func TestSanitizedFunctionNameMapDisambiguatesCollisions(t *testing.T) {
	first := "mcp__plugin_cloudflare_cloudflare-builds__workers_builds_get_build"
	second := "mcp__plugin_cloudflare_cloudflare-builds__workers_builds_get_build_logs"
	raw := []byte(`{"tools":[
		{"name":"` + first + `"},
		{"name":"` + first + `"},
		{"name":"` + second + `"}
	]}`)

	forward := SanitizedFunctionNameMap(raw)
	firstMapped := forward[first]
	secondMapped := forward[second]
	if firstMapped == "" || secondMapped == "" || secondMapped == firstMapped {
		t.Fatalf("mapped names = %q and %q, want distinct non-empty names", firstMapped, secondMapped)
	}
	if len(firstMapped) > 64 || len(secondMapped) > 64 {
		t.Fatalf("mapped name lengths = %d and %d, want <= 64", len(firstMapped), len(secondMapped))
	}

	reversed := []byte(`{"tools":[{"name":"` + second + `"},{"name":"` + first + `"}]}`)
	reversedForward := SanitizedFunctionNameMap(reversed)
	if reversedForward[first] != firstMapped || reversedForward[second] != secondMapped {
		t.Fatalf("mapping changed with declaration order: forward=%v reversed=%v", forward, reversedForward)
	}

	reverse := DisambiguatedToolNameMap(raw)
	if got := reverse[firstMapped]; got != first {
		t.Fatalf("reverse[%q] = %q, want %q", firstMapped, got, first)
	}
	if got := reverse[secondMapped]; got != second {
		t.Fatalf("reverse[%q] = %q, want %q", secondMapped, got, second)
	}
}

func TestSanitizedFunctionNameMapReadsSupportedToolShapes(t *testing.T) {
	raw := []byte(`{"tools":[
		{"type":"function","function":{"name":"nested/name"}},
		{
			"functionDeclarations":[{"name":"camel@name"}],
			"function_declarations":[{"name":"snake name"}]
		}
	]}`)
	forward := SanitizedFunctionNameMap(raw)
	for original, want := range map[string]string{
		"nested/name": "nested_name",
		"camel@name":  "camel_name",
		"snake name":  "snake_name",
	} {
		if got := forward[original]; got != want {
			t.Errorf("forward[%q] = %q, want %q", original, got, want)
		}
	}
}

func TestDeduplicateFunctionDeclarations(t *testing.T) {
	raw := []byte(`[
		{"name":"lookup","description":"first"},
		{"name":"other"},
		{"name":"lookup","description":"second"}
	]`)
	deduped := DeduplicateFunctionDeclarations(raw)
	declarations := gjson.ParseBytes(deduped).Array()
	if len(declarations) != 2 {
		t.Fatalf("declaration count = %d, want 2: %s", len(declarations), deduped)
	}
	if got := declarations[0].Get("description").String(); got != "first" {
		t.Fatalf("first duplicate description = %q, want first", got)
	}
	if got := declarations[1].Get("name").String(); got != "other" {
		t.Fatalf("second declaration name = %q, want other", got)
	}
}

func TestRestoreSanitizedToolName(t *testing.T) {
	m := map[string]string{
		"mcp_server_read": "mcp/server/read",
		"tool_v2":         "tool@v2",
	}

	if got := RestoreSanitizedToolName(m, "mcp_server_read"); got != "mcp/server/read" {
		t.Errorf("expected mcp/server/read, got %q", got)
	}
	if got := RestoreSanitizedToolName(m, "unknown"); got != "unknown" {
		t.Errorf("expected passthrough for unknown, got %q", got)
	}
	if got := RestoreSanitizedToolName(nil, "name"); got != "name" {
		t.Errorf("expected passthrough for nil map, got %q", got)
	}
	if got := RestoreSanitizedToolName(m, ""); got != "" {
		t.Errorf("expected empty for empty name, got %q", got)
	}
}
