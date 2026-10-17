package diff

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestBuildConfigChangeDetailsDetectsCompatibilityV1CompactionToggle(t *testing.T) {
	for _, tc := range []struct {
		name string
		old  bool
		new  bool
	}{
		{name: "enable", old: false, new: true},
		{name: "disable", old: true, new: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buildConfig := func(enabled bool) *config.Config {
				return &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
					Name:          "custom-provider",
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "secret-test-key"}},
					Models: []config.OpenAICompatibilityModel{{
						Name: "upstream-model", Alias: "public-model", UseV1Compaction: enabled,
					}},
				}}}
			}
			oldCfg, newCfg := buildConfig(tc.old), buildConfig(tc.new)
			if changes := BuildConfigChangeDetails(oldCfg, oldCfg); len(changes) != 0 {
				t.Fatalf("unchanged config produced diff: %v", changes)
			}
			changes := BuildConfigChangeDetails(oldCfg, newCfg)
			expectContains(t, changes, "openai-compatibility:")
			expectContains(t, changes, "  provider updated: custom-provider (use-v1-compaction settings updated)")
			for _, change := range changes {
				if strings.Contains(change, "secret-test-key") {
					t.Fatalf("config diff leaked API key material: %q", change)
				}
			}
		})
	}
}
