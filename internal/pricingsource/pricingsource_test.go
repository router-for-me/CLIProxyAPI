package pricingsource

import (
	"testing"
)

func TestMatchAllExact(t *testing.T) {
	got := MatchAll([]string{"gpt-4o", "claude-3-5-sonnet"})
	if len(got["gpt-4o"]) == 0 {
		t.Fatal("expected pricing match for gpt-4o")
	}
	if len(got["claude-3-5-sonnet"]) == 0 {
		t.Fatal("expected pricing match for claude-3-5-sonnet")
	}
}

func TestMatchAllCaseInsensitive(t *testing.T) {
	got := MatchAll([]string{"GPT-4O"})
	if len(got["GPT-4O"]) == 0 {
		t.Fatal("expected case-insensitive match for GPT-4O")
	}
}

func TestMatchAllDateSuffixStripped(t *testing.T) {
	got := MatchAll([]string{"claude-3-5-sonnet-20241022"})
	if len(got["claude-3-5-sonnet-20241022"]) == 0 {
		t.Fatal("expected match by stripping date suffix")
	}
}

func TestMatchAllUnknownModelOmitted(t *testing.T) {
	got := MatchAll([]string{"not-a-real-model"})
	if _, ok := got["not-a-real-model"]; ok {
		t.Fatal("expected unknown models to be omitted from result map")
	}
}

func TestSourcesReturnsNonEmpty(t *testing.T) {
	sources := Sources()
	if len(sources) == 0 {
		t.Fatal("expected at least one source in bundled catalog")
	}
	foundAnthropic := false
	for _, s := range sources {
		if s == "anthropic-official" {
			foundAnthropic = true
			break
		}
	}
	if !foundAnthropic {
		t.Errorf("sources = %v; want anthropic-official present", sources)
	}
}

func TestStripDateSuffix(t *testing.T) {
	cases := map[string]string{
		"claude-3-5-sonnet-20241022": "claude-3-5-sonnet",
		"gpt-4o-2024-07-18":         "gpt-4o-2024-07-18", // not -YYYYMMDD format, kept as-is
		"short":                     "short",
		"gpt-4o":                    "gpt-4o",
		"":                          "",
	}
	for in, want := range cases {
		if got := stripDateSuffix(in); got != want {
			t.Errorf("stripDateSuffix(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestMatchAllMultipleSources(t *testing.T) {
	// gpt-4o has entries from both openai-official and openrouter —
	// verify both are returned.
	got := MatchAll([]string{"gpt-4o"})
	entries := got["gpt-4o"]
	if len(entries) < 2 {
		t.Fatalf("expected ≥2 sources for gpt-4o; got %d", len(entries))
	}
	sources := map[string]bool{}
	for _, e := range entries {
		sources[e.Source] = true
	}
	if !sources["openai-official"] || !sources["openrouter"] {
		t.Errorf("expected openai-official + openrouter sources; got %v", sources)
	}
}
