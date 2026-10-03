package diff

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestBuildConfigChangeDetailsDetectsV1CompactionToggle(t *testing.T) {
	oldCfg := &config.Config{CodexKey: []config.CodexKey{{
		APIKey: "secret-test-key",
		Models: []config.CodexModel{{Name: "upstream", Alias: "public"}},
	}}}
	newCfg := &config.Config{CodexKey: []config.CodexKey{{
		APIKey: "secret-test-key",
		Models: []config.CodexModel{{Name: "upstream", Alias: "public", UseV1Compaction: true}},
	}}}
	if changes := BuildConfigChangeDetails(oldCfg, oldCfg); len(changes) != 0 {
		t.Fatalf("unchanged config produced diff: %v", changes)
	}
	changes := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, changes, "codex[0].models: updated (1 -> 1 entries)")
	for _, change := range changes {
		if strings.Contains(change, "secret-test-key") {
			t.Fatalf("config diff leaked API key material: %q", change)
		}
	}
}
