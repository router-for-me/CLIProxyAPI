package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestClientCodexHistoricalProgrammaticFields(t *testing.T) {
	for _, cfg := range []*Config{
		{Codex: CodexConfig{OptimizeMultiAgentV2: true}},
		{SDKConfig: SDKConfig{CodexOptimizeMultiAgentV2: true}},
		{SDKConfig: SDKConfig{Client: ClientConfig{Codex: CodexClientConfig{OptimizeMultiAgentV2: true}}}},
	} {
		if !cfg.CodexMultiAgentV2Enabled() || !cfg.ForAPIKey().CodexMultiAgentV2Enabled() {
			t.Fatal("historical programmatic client setting was ignored")
		}
		for _, encoding := range []struct {
			marshal   func(any) ([]byte, error)
			unmarshal func([]byte, any) error
		}{{json.Marshal, json.Unmarshal}, {yaml.Marshal, yaml.Unmarshal}} {
			data, err := encoding.marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var restored Config
			if err := encoding.unmarshal(data, &restored); err != nil || !restored.Client.Codex.OptimizeMultiAgentV2 {
				t.Fatalf("canonical snapshot lost the effective setting: %v", err)
			}
		}
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("config-version: 8\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := SaveConfigPreserveComments(path, cfg, true); err != nil {
			t.Fatal(err)
		}
		restored, err := LoadConfig(path)
		if err != nil || !restored.Client.Codex.OptimizeMultiAgentV2 {
			t.Fatalf("save lost the effective setting: %v", err)
		}
	}
	if (*SDKConfig)(nil).CodexMultiAgentV2Enabled() || (*Config)(nil).CodexMultiAgentV2Enabled() {
		t.Fatal("nil configuration enabled optimization")
	}
}
