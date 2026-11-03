package claude

import (
	"strings"
	"testing"
)

const gatewayPrefix = "mcp__gateway__mcp_github-12345678-9abc-def0-1234-56789abcdef0-12345678_"

func TestBuildShortNameMapKeepsGatewayToolNamesDistinct(t *testing.T) {
	names := []string{
		gatewayPrefix + "add-comment-to-pending-review",
		gatewayPrefix + "create-branch",
		gatewayPrefix + "get_file_contents",
		"mcp__gateway__mcp_playwright-browser-67892a0b_browser_click",
		"Bash",
	}
	m := buildShortNameMap(names)
	want := map[string]string{
		names[0]: "mcp__github_add-comment-to-pending-review",
		names[1]: "mcp__github_create-branch",
		names[2]: "mcp__github_get_file_contents",
		names[3]: "mcp__gateway__mcp_playwright-browser-67892a0b_browser_click",
		names[4]: "Bash",
	}
	for orig, short := range want {
		if got := m[orig]; got != short {
			t.Errorf("short(%q) = %q, want %q", orig, got, short)
		}
		if len(m[orig]) > 64 {
			t.Errorf("short(%q) is %d chars", orig, len(m[orig]))
		}
	}
}

func TestBuildShortNameMapDisambiguatesSameToolOnTwoServers(t *testing.T) {
	a := gatewayPrefix + "create-branch"
	b := "mcp__gateway__mcp_github-0b9e7c1d-1111-2222-3333-444455556666-89abcdef_create-branch"
	m := buildShortNameMap([]string{a, b})
	if m[a] == m[b] {
		t.Fatalf("both servers mapped to %q", m[a])
	}
	if !strings.HasPrefix(m[b], "mcp__github_create-branch") {
		t.Fatalf("second server lost its readable name: %q", m[b])
	}
}

func TestReverseMapRestoresReadableGatewayNames(t *testing.T) {
	orig := gatewayPrefix + "add-issue-comment"
	req := []byte(`{"tools":[{"name":"` + orig + `","input_schema":{"type":"object"}}]}`)
	rev := buildReverseMapFromClaudeOriginalShortToOriginal(req)
	if got := rev["mcp__github_add-issue-comment"]; got != orig {
		t.Fatalf("reverse map = %q, want %q", got, orig)
	}
	if got := shortenNameIfNeeded(orig); got != "mcp__github_add-issue-comment" {
		t.Fatalf("single-name shortening = %q", got)
	}
}

func TestShortenNameIfNeededFallsBackForUnknownLongNames(t *testing.T) {
	long := "mcp__srv__" + strings.Repeat("x", 80)
	if got := shortenNameIfNeeded(long); len(got) != 64 || !strings.HasPrefix(got, "mcp__xxx") {
		t.Fatalf("fallback = %q", got)
	}
}
