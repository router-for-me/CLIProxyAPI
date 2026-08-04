package helps

import (
	"regexp"
	"strings"
	"testing"
)

func TestIsClaudeMCPToolName(t *testing.T) {
	for _, name := range []string{
		"mcp__context7__query-docs",
		"mcp__amber_cedar__quiet_harbor",
		"mcp__server__tool__variant",
	} {
		if !IsClaudeMCPToolName(name) {
			t.Fatalf("IsClaudeMCPToolName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"context7__query-docs",
		"mcp____query-docs",
		"mcp__context7__",
		"mcp__context7__query.docs",
		"mcp__context7__" + strings.Repeat("x", 64),
	} {
		if IsClaudeMCPToolName(name) {
			t.Fatalf("IsClaudeMCPToolName(%q) = true, want false", name)
		}
	}
}

func TestClaudeMCPToolAlias(t *testing.T) {
	first := ClaudeMCPToolAlias("credential-secret", "search_web", 0)
	if second := ClaudeMCPToolAlias("credential-secret", "search_web", 0); second != first {
		t.Fatalf("alias is not deterministic: %q != %q", first, second)
	}
	caseDistinct := ClaudeMCPToolAlias("credential-secret", "Search_Web", 0)
	if first == caseDistinct {
		t.Fatalf("case-distinct names produced the same initial alias: %q", first)
	}
	retry := ClaudeMCPToolAlias("credential-secret", "search_web", 1)
	if first == retry {
		t.Fatalf("collision retry did not change alias: %q", first)
	}
	if !IsClaudeMCPToolName(first) {
		t.Fatalf("generated alias %q is not a valid MCP tool name", first)
	}
	if !strings.HasSuffix(first, "_search_web") {
		t.Fatalf("generated alias %q does not preserve the semantic suffix", first)
	}
	if matched, _ := regexp.MatchString(`^mcp__[a-z2-7]{12}__[a-z2-7]{12}_search_web$`, first); !matched {
		t.Fatalf("generated alias %q does not contain keyed IDs plus semantics", first)
	}
	server := strings.Split(first, "__")[1]
	if got := strings.Split(caseDistinct, "__")[1]; got != server {
		t.Fatalf("case-distinct tool server = %q, want shared caller server %q", got, server)
	}
	if got := strings.Split(retry, "__")[1]; got != server {
		t.Fatalf("retry server = %q, want shared caller server %q", got, server)
	}
	if got := strings.Split(ClaudeMCPToolAlias("other-caller", "search_web", 0), "__")[1]; got == server {
		t.Fatalf("different caller unexpectedly shared server %q", server)
	}
}

func TestClaudeMCPToolAlias_SemanticSuffixIsSafeAndBounded(t *testing.T) {
	tests := []struct {
		name         string
		original     string
		wantSuffix   string
		wantAliasLen int
	}{
		{name: "invalid separators", original: "browser.open URL", wantSuffix: "_browser_open_URL"},
		{name: "unicode mixed", original: "search.网页/tool with spaces", wantSuffix: "_search_tool_with_spaces"},
		{name: "unicode only", original: "搜索网页", wantSuffix: "_tool"},
		{name: "maximum length", original: strings.Repeat("a", 100), wantSuffix: "_" + strings.Repeat("a", 32), wantAliasLen: 64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alias := ClaudeMCPToolAlias("credential-secret", tt.original, 0)
			if !IsClaudeMCPToolName(alias) {
				t.Fatalf("generated alias %q is not a valid MCP tool name", alias)
			}
			if len(alias) > 64 {
				t.Fatalf("generated alias length = %d, want <= 64: %q", len(alias), alias)
			}
			if tt.wantAliasLen > 0 && len(alias) != tt.wantAliasLen {
				t.Fatalf("generated alias length = %d, want %d", len(alias), tt.wantAliasLen)
			}
			if !strings.HasSuffix(alias, tt.wantSuffix) {
				t.Fatalf("generated alias %q does not end in %q", alias, tt.wantSuffix)
			}
		})
	}
}
