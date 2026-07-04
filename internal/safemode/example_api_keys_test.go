package safemode

import (
	"strings"
	"testing"
)

func TestExampleAPIKeysDetectsOnlyTemplateValues(t *testing.T) {
	keys := []string{
		" real-key ",
		" your-api-key-1 ",
		"your-api-key",
		"change-me",
		"your-api-key-2",
		"your-api-key-2",
		"your-api-key-3",
	}

	got := ExampleAPIKeys(keys)
	want := []string{"your-api-key-1", "your-api-key-2", "your-api-key-3"}
	if len(got) != len(want) {
		t.Fatalf("ExampleAPIKeys() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ExampleAPIKeys()[%d] = %q, want %q (all: %#v)", i, got[i], want[i], got)
		}
	}
}

func TestExampleAPIKeysIgnoresSimilarValues(t *testing.T) {
	keys := []string{"your-api-key", "change-me", "changeme", "your-api-key-4", "my-your-api-key-1"}
	if got := ExampleAPIKeys(keys); len(got) != 0 {
		t.Fatalf("ExampleAPIKeys() = %#v, want empty", got)
	}
	if HasExampleAPIKeys(keys) {
		t.Fatal("HasExampleAPIKeys() = true, want false")
	}
}

func TestExampleAPIKeyWarningPageIncludesManagementButton(t *testing.T) {
	body := ExampleAPIKeyWarningPageHTML([]string{"your-api-key-1"}, "/management.html?safe-mode=configure")
	for _, want := range []string{"Example API key detected", "your-api-key-1", "Open Management", `href="/management.html?safe-mode=configure"`, "Proxy API endpoints are disabled"} {
		if !strings.Contains(body, want) {
			t.Fatalf("warning page missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, `class="path"`) {
		t.Fatalf("warning page should not include a local config path: %s", body)
	}
}
