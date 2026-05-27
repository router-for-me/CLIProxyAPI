package common

import (
	"strings"
	"testing"
)

func TestToolNameShortener_ShortenName(t *testing.T) {
	shortener := DefaultToolNameShortener()

	t.Run("short name unchanged", func(t *testing.T) {
		got := shortener.ShortenName("Bash")
		if got != "Bash" {
			t.Errorf("expected %q, got %q", "Bash", got)
		}
	})

	t.Run("exact limit unchanged", func(t *testing.T) {
		name := strings.Repeat("a", 64)
		got := shortener.ShortenName(name)
		if got != name {
			t.Errorf("expected name at limit to be unchanged")
		}
	})

	t.Run("over limit truncated", func(t *testing.T) {
		name := strings.Repeat("a", 100)
		got := shortener.ShortenName(name)
		if len(got) != 64 {
			t.Errorf("expected length 64, got %d", len(got))
		}
	})

	t.Run("mcp prefix preserved", func(t *testing.T) {
		name := "mcp__very_long_server_name__very_long_tool_name_that_exceeds_limit"
		got := shortener.ShortenName(name)
		if !strings.HasPrefix(got, "mcp__") {
			t.Errorf("expected mcp__ prefix, got %q", got)
		}
		if len(got) > 64 {
			t.Errorf("expected max 64 chars, got %d", len(got))
		}
	})

	t.Run("mcp shortens to last segment", func(t *testing.T) {
		// Name must be > 64 chars to trigger shortening
		name := "mcp__server__very_long_tool_name_that_definitely_exceeds_sixty_four_chars"
		got := shortener.ShortenName(name)
		if !strings.HasPrefix(got, "mcp__") {
			t.Errorf("expected mcp__ prefix, got %q", got)
		}
		if len(got) > 64 {
			t.Errorf("expected max 64 chars, got %d", len(got))
		}
	})

	t.Run("custom mapping overrides", func(t *testing.T) {
		longName := strings.Repeat("x", 100)
		s := &ToolNameShortener{
			MaxLen: 64,
			CustomMappings: map[string]string{
				longName: "short",
			},
			PreservePrefixes: []string{"mcp__"},
		}
		got := s.ShortenName(longName)
		if got != "short" {
			t.Errorf("expected %q, got %q", "short", got)
		}
	})
}

func TestToolNameShortener_ShortenNames(t *testing.T) {
	shortener := DefaultToolNameShortener()

	t.Run("no conflicts", func(t *testing.T) {
		names := []string{"Bash", "Write", "Read"}
		result := shortener.ShortenNames(names)
		for _, name := range names {
			if result[name] != name {
				t.Errorf("expected %q, got %q", name, result[name])
			}
		}
	})

	t.Run("duplicate names made unique", func(t *testing.T) {
		// Use names that are different but would collide after truncation
		// All start with same prefix but have different suffixes
		base := strings.Repeat("a", 60)
		name1 := base + "xxxx"
		name2 := base + "yyyy"
		name3 := base + "zzzz"
		names := []string{name1, name2, name3}
		result := shortener.ShortenNames(names)

		// All shortened names should be unique
		seen := make(map[string]bool)
		for _, name := range names {
			short := result[name]
			if seen[short] {
				t.Errorf("duplicate shortened name: %q", short)
			}
			seen[short] = true
			if len(short) > 64 {
				t.Errorf("expected max 64 chars, got %d", len(short))
			}
		}
	})

	t.Run("long names shortened uniquely", func(t *testing.T) {
		long1 := strings.Repeat("a", 100)
		long2 := strings.Repeat("b", 100)
		names := []string{long1, long2}
		result := shortener.ShortenNames(names)

		if len(result[long1]) > 64 {
			t.Errorf("expected max 64 chars, got %d", len(result[long1]))
		}
		if result[long1] == result[long2] {
			t.Errorf("expected unique names, both got %q", result[long1])
		}
	})
}

func TestShortenToolName(t *testing.T) {
	t.Run("short name", func(t *testing.T) {
		if got := ShortenToolName("Bash"); got != "Bash" {
			t.Errorf("expected %q, got %q", "Bash", got)
		}
	})

	t.Run("long name", func(t *testing.T) {
		name := strings.Repeat("x", 100)
		got := ShortenToolName(name)
		if len(got) != 64 {
			t.Errorf("expected 64, got %d", len(got))
		}
	})
}

func TestShortenToolNames(t *testing.T) {
	names := []string{"Bash", "Write", "Read"}
	result := ShortenToolNames(names)
	if len(result) != 3 {
		t.Errorf("expected 3 entries, got %d", len(result))
	}
}
