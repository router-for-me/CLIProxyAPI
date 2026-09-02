package helps

import (
	"fmt"
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
	if matched, _ := regexp.MatchString(`^mcp__[a-z]+_[a-z]+__[a-z]+_search_web$`, first); !matched {
		t.Fatalf("generated alias %q does not contain word-based IDs plus semantics", first)
	}
	assertClaudeMCPAliasWords(t, first)
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
		name       string
		original   string
		wantSuffix string
	}{
		{name: "invalid separators", original: "browser.open URL", wantSuffix: "_browser_open_URL"},
		{name: "unicode mixed", original: "search.网页/tool with spaces", wantSuffix: "_search_tool_with_spaces"},
		{name: "unicode only", original: "搜索网页", wantSuffix: "_tool"},
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
			if !strings.HasSuffix(alias, tt.wantSuffix) {
				t.Fatalf("generated alias %q does not end in %q", alias, tt.wantSuffix)
			}
		})
	}

	const original = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	alias := ClaudeMCPToolAlias("credential-secret", original, 0)
	underscore := strings.LastIndex(alias, "_")
	if underscore < 0 {
		t.Fatalf("generated alias %q has no semantic separator", alias)
	}
	prefixLen := underscore + 1
	wantSemanticLen := 64 - prefixLen
	if wantSemanticLen < 1 {
		wantSemanticLen = 1
	}
	if got := alias[prefixLen:]; got != strings.Repeat("a", wantSemanticLen) {
		t.Fatalf("semantic suffix = %q, want %d a's", got, wantSemanticLen)
	}
	if len(alias) != 64 {
		t.Fatalf("generated alias length = %d, want 64: %q", len(alias), alias)
	}
}

func TestClaudeMCPToolAlias_Strict64CharLimitUnderAllWordCombinations(t *testing.T) {
	for i := 0; i < ClaudeMCPAliasWordCount(); i++ {
		secret := fmt.Sprintf("test-secret-%d", i)
		original := strings.Repeat(fmt.Sprintf("tool_%d_long_name_", i), 50)
		alias := ClaudeMCPToolAlias(secret, original, uint32(i))
		if len(alias) > 64 {
			t.Fatalf("alias length %d exceeds Anthropic 64-char limit: %q", len(alias), alias)
		}
		if !IsClaudeMCPToolName(alias) {
			t.Fatalf("alias %q is not a valid MCP tool name", alias)
		}
		assertClaudeMCPAliasWords(t, alias)
	}
}

func TestAllocateClaudeMCPToolAlias_StopsWhenAttemptsExhausted(t *testing.T) {
	const secret = "exhaust-space"
	const original = "tool.name"
	reserved := make(map[string]bool, ClaudeMCPAliasWordCount())
	for attempt := 0; attempt < ClaudeMCPAliasWordCount(); attempt++ {
		reserved[ClaudeMCPToolAlias(secret, original, uint32(attempt))] = true
	}
	if _, ok := AllocateClaudeMCPToolAlias(secret, original, reserved); ok {
		t.Fatal("allocate succeeded after every attempt alias was reserved")
	}
	if alias, ok := AllocateClaudeMCPToolAlias(secret, original, nil); !ok || alias == "" {
		t.Fatal("allocate failed with an empty reserved set")
	}
}

func TestClaudeMCPToolAlias_ProbesAllWordsWithoutDuplicates(t *testing.T) {
	const secret = "test-secret"
	const original = "tool.name"
	totalWords := ClaudeMCPAliasWordCount()
	seen := make(map[string]bool, totalWords)

	for attempt := 0; attempt < totalWords; attempt++ {
		alias := ClaudeMCPToolAlias(secret, original, uint32(attempt))
		parts := strings.Split(alias, "__")
		toolID, _, _ := strings.Cut(parts[2], "_")
		if seen[toolID] {
			t.Fatalf("attempt %d generated duplicate toolID %q", attempt, toolID)
		}
		seen[toolID] = true
	}
	if len(seen) != totalWords {
		t.Fatalf("covered %d words in %d attempts, want 100%% (%d words)", len(seen), totalWords, totalWords)
	}
}

func TestAllocateClaudeMCPToolAlias_AllocatesEveryDistinctWord(t *testing.T) {
	const secret = "allocate-full-space"
	const original = "tool.name"
	totalWords := ClaudeMCPAliasWordCount()
	reserved := make(map[string]bool, totalWords)

	for i := 0; i < totalWords; i++ {
		alias, ok := AllocateClaudeMCPToolAlias(secret, original, reserved)
		if !ok {
			t.Fatalf("failed to allocate at step %d with %d words reserved", i, len(reserved))
		}
		if reserved[alias] {
			t.Fatalf("allocated duplicate alias %q at step %d", alias, i)
		}
		reserved[alias] = true
	}
	if len(reserved) != totalWords {
		t.Fatalf("reserved count = %d, want %d", len(reserved), totalWords)
	}
	if _, ok := AllocateClaudeMCPToolAlias(secret, original, reserved); ok {
		t.Fatal("allocate succeeded when all 2048 words are reserved")
	}
}

func BenchmarkAllocateClaudeMCPToolAlias_Collision(b *testing.B) {
	const secret = "test-secret"
	const original = "tool.name"
	reserved := make(map[string]bool)
	for attempt := 0; attempt < 100; attempt++ {
		reserved[ClaudeMCPToolAlias(secret, original, uint32(attempt))] = true
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = AllocateClaudeMCPToolAlias(secret, original, reserved)
	}
}

func assertClaudeMCPAliasWords(t *testing.T, alias string) {
	t.Helper()
	parts := strings.Split(alias, "__")
	if len(parts) != 3 {
		t.Fatalf("alias %q does not have mcp/server/tool parts", alias)
	}
	serverWords := strings.Split(parts[1], "_")
	if len(serverWords) != 2 {
		t.Fatalf("alias %q server %q is not two BIP-39 words", alias, parts[1])
	}
	toolID, _, ok := strings.Cut(parts[2], "_")
	if !ok {
		t.Fatalf("alias %q tool component %q has no semantic suffix", alias, parts[2])
	}
	allowed := make(map[string]struct{}, len(claudeMCPAliasEnglishWords))
	for _, word := range claudeMCPAliasEnglishWords {
		allowed[word] = struct{}{}
	}
	for _, word := range append(append([]string{}, serverWords...), toolID) {
		if _, exists := allowed[word]; !exists {
			t.Fatalf("alias %q uses non-BIP39 word %q", alias, word)
		}
	}
}

func TestClaudeMCPAliasWordlistIntegrity(t *testing.T) {
	// The wordlist is embedded, so a truncated or reordered file would silently
	// disable aliasing (AllocateClaudeMCPToolAlias returns false for every tool)
	// instead of failing loudly. Pin the exact BIP-39 English dictionary.
	if got := ClaudeMCPAliasWordCount(); got != 2048 {
		t.Fatalf("wordlist size = %d, want the 2048-word BIP-39 English dictionary", got)
	}
	if got := claudeMCPAliasEnglishWords[0]; got != "abandon" {
		t.Fatalf("first word = %q, want %q", got, "abandon")
	}
	if got := claudeMCPAliasEnglishWords[2047]; got != "zoo" {
		t.Fatalf("last word = %q, want %q", got, "zoo")
	}
	seen := make(map[string]struct{}, len(claudeMCPAliasEnglishWords))
	for _, word := range claudeMCPAliasEnglishWords {
		if _, duplicate := seen[word]; duplicate {
			t.Fatalf("duplicate word %q would shrink the usable alias space", word)
		}
		seen[word] = struct{}{}
		if word == "" || len(word) > 8 {
			t.Fatalf("word %q is outside the 1..8 character budget assumed by the 64-char alias limit", word)
		}
		for _, char := range word {
			if char < 'a' || char > 'z' {
				t.Fatalf("word %q contains a non-lowercase-ASCII rune %q", word, char)
			}
		}
	}
}

func TestAllocateClaudeMCPToolAliasMatchesSingleShotConstruction(t *testing.T) {
	// Both entry points must build identical names; the exhaustion tests above
	// use ClaudeMCPToolAlias to seed the reserved set, so any drift between the
	// two would make them silently stop testing the production path.
	const secret = "shared-construction"
	for _, original := range []string{"Bash", "read_file", strings.Repeat("long_tool_name_", 9)} {
		reserved := make(map[string]bool, ClaudeMCPAliasWordCount())
		for attempt := 0; attempt < ClaudeMCPAliasWordCount(); attempt++ {
			allocated, ok := AllocateClaudeMCPToolAlias(secret, original, reserved)
			if !ok {
				t.Fatalf("original %q: allocation exhausted at attempt %d", original, attempt)
			}
			if want := ClaudeMCPToolAlias(secret, original, uint32(attempt)); allocated != want {
				t.Fatalf("original %q attempt %d: allocated %q, single-shot %q", original, attempt, allocated, want)
			}
			reserved[allocated] = true
		}
	}
}
