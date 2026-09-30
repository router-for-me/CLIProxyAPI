package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCollaborationPlaintextConfigScopeAndPersistence(t *testing.T) {
	for _, setting := range []string{
		"requests: {codex-collaboration-plaintext: true}",
		"codex-collaboration-plaintext: true",
	} {
		raw := []byte(setting + "\noauth: {providers: {codex: {optimize-multi-agent-v2: true}}}\n")
		cfg, err := ParseConfigBytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := ParseConfigBytes(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		for _, view := range []*Config{cfg, cfg.CloneForRuntime(), decoded} {
			if !view.CodexCollaborationPlaintext || !view.ForAPIKey().CodexCollaborationPlaintext {
				t.Fatal("shared plaintext setting lost")
			}
			if view.ForAPIKey().CodexOptimizeMultiAgentV2 || view.ForAPIKey().Codex.OptimizeMultiAgentV2 {
				t.Fatal("OAuth-only optimizer leaked into API-key view")
			}
			if !view.Codex.OptimizeMultiAgentV2 || !view.OAuthOnlyFields["codex.optimize-multi-agent-v2"] {
				t.Fatal("shared OAuth configuration mutated")
			}
		}
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := SaveConfigPreserveComments(path, cfg); err != nil {
			t.Fatal(err)
		}
		reloaded, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reloaded.ForAPIKey().CodexCollaborationPlaintext || reloaded.ForAPIKey().Codex.OptimizeMultiAgentV2 {
			t.Fatal("save/reload changed request/OAuth scope")
		}
	}
	cfg, err := ParseConfigBytes([]byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CodexCollaborationPlaintext {
		t.Fatal("plaintext must be opt-in")
	}
}
