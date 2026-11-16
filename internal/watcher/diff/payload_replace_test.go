package diff

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestBuildConfigChangeDetailsPayloadReplace(t *testing.T) {
	oldCfg := &config.Config{}
	newCfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{
		Models: []config.PayloadModelRule{{Name: "*"}}, Target: config.PayloadReplaceTarget{Role: "user", Positions: []int{1, 2}}, Replacements: []config.PayloadReplacement{{"private-source": "private-destination"}},
	}}}}
	for _, tt := range []struct {
		old, new *config.Config
		want     string
	}{
		{oldCfg, newCfg, "payload.replace: updated (0 -> 1 rules)"},
		{newCfg, oldCfg, "payload.replace: updated (1 -> 0 rules)"},
	} {
		changes := BuildConfigChangeDetails(tt.old, tt.new)
		if len(changes) != 1 || changes[0] != tt.want {
			t.Fatalf("changes = %v, want %q", changes, tt.want)
		}
	}
	updated := newCfg.CloneForRuntime()
	updated.Payload.Replace[0].Replacements[0]["private-source"] = "another-private-destination"
	changes := BuildConfigChangeDetails(newCfg, updated)
	if len(changes) != 1 || changes[0] != "payload.replace: updated (1 -> 1 rules)" {
		t.Fatalf("missed replacement edit: %v", changes)
	}
	if strings.Contains(strings.Join(changes, " "), "private") {
		t.Fatal("diff leaked replacement text")
	}
	if changes := BuildConfigChangeDetails(updated, updated); len(changes) != 0 {
		t.Fatalf("unchanged rules produced diff: %v", changes)
	}
}
