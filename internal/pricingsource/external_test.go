package pricingsource

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleLiteLLM = `{
  "sample_spec": {
    "input_cost_per_token": 0.0,
    "litellm_provider": "sample"
  },
  "glm-4": {
    "input_cost_per_token": 0.0000007,
    "output_cost_per_token": 0.0000021,
    "litellm_provider": "openai",
    "mode": "chat"
  },
  "glm-4.5": {
    "input_cost_per_token": 0.0000011,
    "output_cost_per_token": 0.0000033,
    "cache_read_input_token_cost": 0.0000001,
    "output_cost_per_reasoning_token": 0.0000022,
    "litellm_provider": "openai",
    "mode": "chat"
  },
  "no-pricing-entry": {
    "litellm_provider": "openai",
    "mode": "chat"
  }
}`

func TestParseLiteLLMCatalog(t *testing.T) {
	entries, err := parseLiteLLMCatalog([]byte(sampleLiteLLM), "litellm-test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 priced entries (sample_spec + no-pricing-entry skipped); got %d", len(entries))
	}
	byModel := map[string]SuggestedPrice{}
	for _, e := range entries {
		byModel[strings.ToLower(e.Model)] = e
	}
	glm4, ok := byModel["glm-4"]
	if !ok {
		t.Fatal("expected glm-4 entry present")
	}
	if glm4.Source != "litellm-test" {
		t.Errorf("glm-4 source = %q; want litellm-test", glm4.Source)
	}
	// 0.0000007 per-token → 0.7 per-1M
	if glm4.InputPer1M < 0.69 || glm4.InputPer1M > 0.71 {
		t.Errorf("glm-4 input_per_1m = %v; want ~0.7", glm4.InputPer1M)
	}
	glm45 := byModel["glm-4.5"]
	if glm45.CachedReadPer1M <= 0 || glm45.ReasoningPer1M <= 0 {
		t.Errorf("glm-4.5 missing cache_read/reasoning: %+v", glm45)
	}
}

func TestSetExternalSourcesAndMatchAll(t *testing.T) {
	// Reset registry at start/end of this test so it cannot leak.
	t.Cleanup(func() { SetExternalSources(nil) })

	SetExternalSources([]ExternalSource{{
		Name:       "litellm-test",
		SourceType: "url",
		URL:        "data:,fake",
		Format:     "litellm",
		Enabled:    true,
	}})
	// We can't hit the data: URL through the fetch path; pre-warm the
	// in-memory entry cache directly so MatchAll can find it.
	extRegistry.extMu.Lock()
	extRegistry.entries["litellm-test"] = []SuggestedPrice{{
		Model:       "glm-4",
		Source:      "litellm-test",
		InputPer1M:  0.7,
		OutputPer1M: 2.1,
	}}
	extRegistry.loadState["litellm-test"] = externalLoadState{entryCount: 1, fetchedAt: time.Now()}
	extRegistry.extMu.Unlock()

	got := MatchAll([]string{"glm-4"})
	entries := got["glm-4"]
	if len(entries) == 0 {
		t.Fatal("expected match for glm-4 (external source merged)")
	}
	found := false
	for _, e := range entries {
		if e.Source == "litellm-test" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected litellm-test source merged for glm-4; entries: %+v", entries)
	}

	// Sources() should also surface the external source name.
	srcs := Sources()
	foundSrc := false
	for _, s := range srcs {
		if s == "litellm-test" {
			foundSrc = true
		}
	}
	if !foundSrc {
		t.Errorf("Sources() = %v; expected litellm-test present", srcs)
	}
}

func TestRefreshSourceFile(t *testing.T) {
	t.Cleanup(func() { SetExternalSources(nil) })
	dir := t.TempDir()
	path := filepath.Join(dir, "litellm.json")
	if err := os.WriteFile(path, []byte(sampleLiteLLM), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	SetExternalSources([]ExternalSource{{
		Name:       "local-litellm",
		SourceType: "file",
		FilePath:   path,
		Format:     "litellm",
		Enabled:    true,
	}})
	count, err := RefreshSource(context.Background(), "local-litellm")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if count != 2 {
		t.Errorf("refresh count = %d; want 2", count)
	}
	// MatchAll should now find glm-4 from the external source.
	got := MatchAll([]string{"glm-4"})
	if len(got["glm-4"]) == 0 {
		t.Fatal("expected glm-4 match after file refresh")
	}
	// Snapshot reports the refresh outcome.
	snaps := ListExternalSources()
	if len(snaps) != 1 || snaps[0].Source.Name != "local-litellm" {
		t.Fatalf("snapshots = %+v", snaps)
	}
	if snaps[0].EntryCount != 2 || snaps[0].LastError != "" {
		t.Errorf("snapshot = %+v; want entry_count=2, no error", snaps[0])
	}
}

func TestRefreshSourceDisabledFails(t *testing.T) {
	t.Cleanup(func() { SetExternalSources(nil) })
	SetExternalSources([]ExternalSource{{
		Name:       "off",
		SourceType: "url",
		URL:        "https://example.invalid/x.json",
		Format:     "litellm",
		Enabled:    false,
	}})
	if _, err := RefreshSource(context.Background(), "off"); err == nil {
		t.Fatal("expected error refreshing disabled source")
	}
}
