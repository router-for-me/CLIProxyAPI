package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMetaReasoningReplayDefaultsOff(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Meta.ReasoningReplay.Enabled || len(cfg.Meta.ReasoningReplay.Models) != 0 {
		t.Fatalf("meta reasoning replay must default to off, got %+v", cfg.Meta.ReasoningReplay)
	}
	if cfg.Meta.ReasoningReplay.AppliesToModel("muse-spark-1.3") {
		t.Fatal("a default config applied reasoning replay to a model")
	}
}

func TestMetaReasoningReplayConfigSurvivesLayoutMigration(t *testing.T) {
	legacy := []byte("meta: {reasoning-replay: {enabled: true, models: ['muse-spark-1.3*', 'canary/*']}}\n")
	canonical := []byte("upstream:\n  meta:\n    reasoning-replay:\n      enabled: true\n      models: ['muse-spark-1.3*', 'canary/*']\n")
	want := MetaReasoningReplayConfig{Enabled: true, Models: []string{"muse-spark-1.3*", "canary/*"}}
	for name, raw := range map[string][]byte{"legacy": legacy, "upstream": canonical} {
		t.Run(name, func(t *testing.T) {
			cfg, err := ParseConfigBytes(raw)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.Meta.ReasoningReplay, want) {
				t.Fatalf("parsed %+v, want %+v", cfg.Meta.ReasoningReplay, want)
			}
			if got := cfg.CloneForRuntime().Meta.ReasoningReplay; !reflect.DeepEqual(got, want) {
				t.Fatalf("clone changed the setting: %+v", got)
			}
			migrated, _, err := NormalizeConfigLayout(raw, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateV8Config(migrated); err != nil {
				t.Fatal(err)
			}
			restored, err := ParseConfigBytes(migrated)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(restored.Meta.ReasoningReplay, want) {
				t.Fatalf("migration changed the setting: %+v", restored.Meta.ReasoningReplay)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := SaveConfigPreserveComments(path, cfg, true); err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			reloaded, err := ParseConfigBytes(saved)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reloaded.Meta.ReasoningReplay, want) {
				t.Fatalf("save/reload changed the setting: %+v\n%s", reloaded.Meta.ReasoningReplay, saved)
			}
		})
	}
}

func TestMetaReasoningReplayAppliesToModel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cfg    MetaReasoningReplayConfig
		models []string
		want   bool
	}{
		{"disabled ignores patterns", MetaReasoningReplayConfig{Models: []string{"*"}}, []string{"muse-spark-1.3"}, false},
		{"enabled with no patterns covers every model", MetaReasoningReplayConfig{Enabled: true}, []string{"muse-spark-1.3"}, true},
		{"blank patterns are no patterns", MetaReasoningReplayConfig{Enabled: true, Models: []string{" ", ""}}, []string{"muse-spark-1.3"}, true},
		{"exact match is case-insensitive", MetaReasoningReplayConfig{Enabled: true, Models: []string{"Muse-Spark-1.3"}}, []string{"muse-spark-1.3"}, true},
		{"exact pattern does not match a longer name", MetaReasoningReplayConfig{Enabled: true, Models: []string{"muse-spark-1.3"}}, []string{"muse-spark-1.3-contributor"}, false},
		{"trailing wildcard", MetaReasoningReplayConfig{Enabled: true, Models: []string{"muse-spark-1.3*"}}, []string{"muse-spark-1.3-contributor"}, true},
		{"leading wildcard", MetaReasoningReplayConfig{Enabled: true, Models: []string{"*-contributor"}}, []string{"muse-spark-1.2-contributor"}, true},
		{"inner wildcard", MetaReasoningReplayConfig{Enabled: true, Models: []string{"muse-*-contributor"}}, []string{"muse-spark-1.3-contributor"}, true},
		{"wildcard crosses a prefix separator", MetaReasoningReplayConfig{Enabled: true, Models: []string{"canary/*"}}, []string{"canary/muse-spark-1.3"}, true},
		{"non-matching pattern", MetaReasoningReplayConfig{Enabled: true, Models: []string{"muse-spark-1.2*"}}, []string{"muse-spark-1.3"}, false},
		{"alias matches even when the upstream name does not", MetaReasoningReplayConfig{Enabled: true, Models: []string{"muse-canary"}}, []string{"muse-canary", "muse-spark-1.3"}, true},
		{"upstream name matches even when the alias does not", MetaReasoningReplayConfig{Enabled: true, Models: []string{"muse-spark-1.3"}}, []string{"agent-alias", "muse-spark-1.3"}, true},
		{"no model names given", MetaReasoningReplayConfig{Enabled: true, Models: []string{"muse-*"}}, nil, false},
		{"wildcard must be anchored at both ends", MetaReasoningReplayConfig{Enabled: true, Models: []string{"spark"}}, []string{"muse-spark-1.3"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.AppliesToModel(tc.models...); got != tc.want {
				t.Fatalf("AppliesToModel(%v) = %v, want %v", tc.models, got, tc.want)
			}
		})
	}
}
