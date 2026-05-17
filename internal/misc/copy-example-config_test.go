package misc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGenerateClientAPIKey(t *testing.T) {
	key, err := generateClientAPIKey()
	if err != nil {
		t.Fatalf("generateClientAPIKey() error = %v", err)
	}
	if !strings.HasPrefix(key, "cpak-") {
		t.Fatalf("generateClientAPIKey() = %q, want cpak- prefix", key)
	}
	if strings.ContainsAny(key, " \t\r\n") {
		t.Fatalf("generateClientAPIKey() contains whitespace: %q", key)
	}
}

func TestRenderConfigTemplateWithGeneratedAPIKey(t *testing.T) {
	template := []byte(`host: ""
port: 8317

# API keys for authentication
# Sample keys are rejected; generate private keys before exposing port 8317 publicly.
api-keys:
  # - "your-api-key-1"
  # - "your-api-key-2"
  # - "your-api-key-3"

debug: false
`)

	rendered, key, generated, err := renderConfigTemplateWithGeneratedAPIKey(template)
	if err != nil {
		t.Fatalf("renderConfigTemplateWithGeneratedAPIKey() error = %v", err)
	}
	if !generated {
		t.Fatal("renderConfigTemplateWithGeneratedAPIKey() generated = false, want true")
	}
	if key == "" || strings.Contains(key, "your-api-key") {
		t.Fatalf("generated key = %q, want private non-sample key", key)
	}

	var cfg struct {
		APIKeys []string `yaml:"api-keys"`
	}
	if err = yaml.Unmarshal(rendered, &cfg); err != nil {
		t.Fatalf("unmarshal rendered config: %v\n%s", err, rendered)
	}
	if len(cfg.APIKeys) != 1 {
		t.Fatalf("api-keys length = %d, want 1\n%s", len(cfg.APIKeys), rendered)
	}
	if cfg.APIKeys[0] != key {
		t.Fatalf("api-keys[0] = %q, want %q", cfg.APIKeys[0], key)
	}
	if strings.Contains(string(rendered), "\n  - \"your-api-key-1\"") {
		t.Fatalf("rendered config still contains active sample key:\n%s", rendered)
	}
	if strings.Contains(string(rendered), "your-api-key-1") {
		t.Fatalf("rendered generated config should not keep sample key comments:\n%s", rendered)
	}
}

func TestRenderConfigTemplatePreservesOperatorAPIKeys(t *testing.T) {
	template := []byte(`host: ""
api-keys:
  - "operator-key"
  - "your-api-key-1"
debug: false
`)

	rendered, key, generated, err := renderConfigTemplateWithGeneratedAPIKey(template)
	if err != nil {
		t.Fatalf("renderConfigTemplateWithGeneratedAPIKey() error = %v", err)
	}
	if generated {
		t.Fatalf("renderConfigTemplateWithGeneratedAPIKey() generated = true, want false")
	}
	if key != "" {
		t.Fatalf("generated key = %q, want empty", key)
	}
	if string(rendered) != string(template) {
		t.Fatalf("rendered config changed operator keys:\n%s", rendered)
	}
}

func TestRenderConfigTemplateReplacesOnlySampleAPIKeys(t *testing.T) {
	template := []byte(`host: ""
api-keys:
  - "your-api-key-1"
  - "your-api-key-2"
`)

	rendered, key, generated, err := renderConfigTemplateWithGeneratedAPIKey(template)
	if err != nil {
		t.Fatalf("renderConfigTemplateWithGeneratedAPIKey() error = %v", err)
	}
	if !generated {
		t.Fatal("renderConfigTemplateWithGeneratedAPIKey() generated = false, want true")
	}

	var cfg struct {
		APIKeys []string `yaml:"api-keys"`
	}
	if err = yaml.Unmarshal(rendered, &cfg); err != nil {
		t.Fatalf("unmarshal rendered config: %v\n%s", err, rendered)
	}
	if len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != key || strings.Contains(cfg.APIKeys[0], "your-api-key") {
		t.Fatalf("api-keys = %#v, generated key = %q", cfg.APIKeys, key)
	}
}

func TestCopyConfigTemplateGeneratesClientAPIKey(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "config.example.yaml")
	dst := filepath.Join(dir, "nested", "config.yaml")
	if err := os.WriteFile(src, []byte("api-keys:\n  # - \"your-api-key-1\"\n"), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}

	if err := CopyConfigTemplate(src, dst); err != nil {
		t.Fatalf("CopyConfigTemplate() error = %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	var cfg struct {
		APIKeys []string `yaml:"api-keys"`
	}
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal destination: %v\n%s", err, data)
	}
	if len(cfg.APIKeys) != 1 || !strings.HasPrefix(cfg.APIKeys[0], "cpak-") {
		t.Fatalf("api-keys = %#v, want one generated cpak key", cfg.APIKeys)
	}
}
