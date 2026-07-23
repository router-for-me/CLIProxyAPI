package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseConfigBytesCodexIntegrationDefaultsRemainDisabled(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("host: \"\"\nport: 8317\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.CodexIntegration.Enabled {
		t.Fatal("CodexIntegration.Enabled = true, want false")
	}
	if cfg.CodexIntegration.CatalogFile != DefaultCodexCatalogFile {
		t.Fatalf("CatalogFile = %q, want %q", cfg.CodexIntegration.CatalogFile, DefaultCodexCatalogFile)
	}
	if len(cfg.CodexIntegration.Models) != 4 {
		t.Fatalf("len(Models) = %d, want 4", len(cfg.CodexIntegration.Models))
	}
}

func TestParseConfigBytesCodexIntegrationStableDefaults(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("host: 127.0.0.1\ncodex-integration:\n  enabled: true\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}

	want := map[string]string{
		"xai/grok-4.5":                         "grok-4.5",
		"antigravity/gemini-3.6-flash":         "gemini-3.6-flash-high",
		"antigravity/gemini-3.1-pro":           "gemini-pro-agent",
		"antigravity/claude-opus-4-6-thinking": "claude-opus-4-6-thinking",
	}
	for _, model := range cfg.CodexIntegration.Models {
		upstream, ok := want[model.Slug]
		if !ok {
			t.Fatalf("unexpected stable model %q", model.Slug)
		}
		if model.UpstreamModel != upstream {
			t.Fatalf("model %q upstream = %q, want %q", model.Slug, model.UpstreamModel, upstream)
		}
		delete(want, model.Slug)
	}
	if len(want) != 0 {
		t.Fatalf("missing stable models: %#v", want)
	}
}

func TestParseConfigBytesCodexIntegrationAcceptsKimiProvider(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("host: 127.0.0.1\ncodex-integration:\n  enabled: true\n  models:\n    - slug: kimi/kimi-for-coding\n      provider: kimi\n      upstream-model: kimi-for-coding\n      visible: true\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if got := cfg.CodexIntegration.Models[0].Provider; got != "kimi" {
		t.Fatalf("Provider = %q, want kimi", got)
	}
}

func TestParseConfigBytesRejectsInvalidCodexIntegration(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "loopback access with wildcard host",
			yaml:    "host: 0.0.0.0\ncodex-integration:\n  enabled: true\n",
			wantErr: "host",
		},
		{
			name:    "duplicate slug",
			yaml:    "host: 127.0.0.1\ncodex-integration:\n  enabled: true\n  models:\n    - slug: xai/grok\n      provider: xai\n      upstream-model: grok\n      visible: true\n    - slug: xai/grok\n      provider: xai\n      upstream-model: grok-2\n      visible: true\n",
			wantErr: "models[1].slug",
		},
		{
			name:    "unknown provider",
			yaml:    "host: 127.0.0.1\ncodex-integration:\n  enabled: true\n  models:\n    - slug: other/model\n      provider: other\n      upstream-model: model\n      visible: true\n",
			wantErr: "models[0].provider",
		},
		{
			name:    "bare third party slug",
			yaml:    "host: 127.0.0.1\ncodex-integration:\n  enabled: true\n  models:\n    - slug: grok-4.5\n      provider: xai\n      upstream-model: grok-4.5\n      visible: true\n",
			wantErr: "models[0].slug",
		},
		{
			name:    "catalog traversal",
			yaml:    "host: 127.0.0.1\ncodex-integration:\n  enabled: true\n  catalog-file: ../catalog.json\n",
			wantErr: "catalog-file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseConfigBytes([]byte(tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ParseConfigBytes() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestCloneForRuntimeDeepCopiesCodexIntegrationModels(t *testing.T) {
	cfg := &Config{SDKConfig: SDKConfig{CodexIntegration: DefaultCodexIntegrationConfig()}}
	clone := cfg.CloneForRuntime()
	clone.CodexIntegration.Models[0].Slug = "xai/changed"
	if cfg.CodexIntegration.Models[0].Slug == clone.CodexIntegration.Models[0].Slug {
		t.Fatal("CloneForRuntime() shared CodexIntegration.Models backing storage")
	}
}

func TestConfigExampleCodexIntegrationParses(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig(config.example.yaml) error = %v", err)
	}
	if cfg.CodexIntegration.Enabled {
		t.Fatal("example config enables Codex integration")
	}
	if len(cfg.CodexIntegration.Models) != 4 {
		t.Fatalf("len(example Models) = %d, want 4", len(cfg.CodexIntegration.Models))
	}
}
