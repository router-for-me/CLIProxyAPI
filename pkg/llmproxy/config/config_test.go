package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "config*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	content := `
port: 8080
auth-dir: ./auth
debug: true
`
	if _, err := tmpFile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Port)
	}

	if cfg.AuthDir != "./auth" {
		t.Errorf("expected auth-dir ./auth, got %s", cfg.AuthDir)
	}

	if !cfg.Debug {
		t.Errorf("expected debug true, got false")
	}
}

func TestConfig_Validate(t *testing.T) {
	cfg := &Config{
		Port: 8080,
	}
	if cfg.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Port)
	}
}

func TestLoadConfigOptional_DirectoryPath(t *testing.T) {
	tmpDir := t.TempDir()
	dirPath := filepath.Join(tmpDir, "config-dir")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}

	_, err := LoadConfigOptional(dirPath, false)
	if err == nil {
		t.Fatal("expected error for directory config path when optional=false")
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("expected directory error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "pass a YAML file path") {
		t.Fatalf("expected remediation hint in error, got: %v", err)
	}

	cfg, err := LoadConfigOptional(dirPath, true)
	if err != nil {
		t.Fatalf("expected nil error for optional directory config path, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config for optional directory config path")
	}
}

func TestConfigSanitizePayloadRules_ValidNestedPathsPreserved(t *testing.T) {
	cfg := &Config{
		Payload: PayloadConfig{
			Default: []PayloadRule{
				{
					Params: map[string]any{
						"response_format.json_schema.schema.properties.output.type": "string",
					},
				},
			},
			Override: []PayloadRule{
				{
					Params: map[string]any{
						"metadata.flags.enable_nested_mapping": true,
					},
				},
			},
			Filter: []PayloadFilterRule{
				{
					Params: []string{"metadata.debug.internal"},
				},
			},
			DefaultRaw: []PayloadRule{
				{
					Params: map[string]any{
						"tool_choice": `{"type":"function","name":"route_to_primary"}`,
					},
				},
			},
		},
	}

	cfg.SanitizePayloadRules()

	if len(cfg.Payload.Default) != 1 {
		t.Fatalf("expected default rules preserved, got %d", len(cfg.Payload.Default))
	}
	if len(cfg.Payload.Override) != 1 {
		t.Fatalf("expected override rules preserved, got %d", len(cfg.Payload.Override))
	}
	if len(cfg.Payload.Filter) != 1 {
		t.Fatalf("expected filter rules preserved, got %d", len(cfg.Payload.Filter))
	}
	if len(cfg.Payload.DefaultRaw) != 1 {
		t.Fatalf("expected default-raw rules preserved, got %d", len(cfg.Payload.DefaultRaw))
	}
}

func TestConfigSanitizePayloadRules_InvalidPathDropped(t *testing.T) {
	cfg := &Config{
		Payload: PayloadConfig{
			Default: []PayloadRule{
				{
					Params: map[string]any{
						".invalid.path": "x",
					},
				},
			},
			Override: []PayloadRule{
				{
					Params: map[string]any{
						"metadata..invalid": true,
					},
				},
			},
			Filter: []PayloadFilterRule{
				{
					Params: []string{"metadata.invalid."},
				},
			},
			DefaultRaw: []PayloadRule{
				{
					Params: map[string]any{
						".raw.invalid": `{"ok":true}`,
					},
				},
			},
		},
	}

	cfg.SanitizePayloadRules()

	if len(cfg.Payload.Default) != 0 {
		t.Fatalf("expected invalid default rule dropped, got %d", len(cfg.Payload.Default))
	}
	if len(cfg.Payload.Override) != 0 {
		t.Fatalf("expected invalid override rule dropped, got %d", len(cfg.Payload.Override))
	}
	if len(cfg.Payload.Filter) != 0 {
		t.Fatalf("expected invalid filter rule dropped, got %d", len(cfg.Payload.Filter))
	}
	if len(cfg.Payload.DefaultRaw) != 0 {
		t.Fatalf("expected invalid default-raw rule dropped, got %d", len(cfg.Payload.DefaultRaw))
	}
}

func TestConfigSanitizePayloadRules_InvalidRawJSONDropped(t *testing.T) {
	cfg := &Config{
		Payload: PayloadConfig{
			DefaultRaw: []PayloadRule{
				{
					Params: map[string]any{
						"tool_choice": `{"type":`,
					},
				},
			},
			OverrideRaw: []PayloadRule{
				{
					Params: map[string]any{
						"metadata.labels": []byte(`{"env":"prod"`),
					},
				},
			},
		},
	}

	cfg.SanitizePayloadRules()

	if len(cfg.Payload.DefaultRaw) != 0 {
		t.Fatalf("expected invalid default-raw JSON rule dropped, got %d", len(cfg.Payload.DefaultRaw))
	}
	if len(cfg.Payload.OverrideRaw) != 0 {
		t.Fatalf("expected invalid override-raw JSON rule dropped, got %d", len(cfg.Payload.OverrideRaw))
	}
}

func TestCheckedPathLengthPlusOne(t *testing.T) {
	if got := checkedPathLengthPlusOne(4); got != 5 {
		t.Fatalf("expected 5, got %d", got)
	}

	maxInt := int(^uint(0) >> 1)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for overflow path length")
		}
	}()
	_ = checkedPathLengthPlusOne(maxInt)
}
