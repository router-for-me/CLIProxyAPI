package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// stubExporter returns a fixed bundle from ExportData.
type stubExporter struct {
	bundle store.BackupBundle
	err    error
}

func (s stubExporter) ExportData(ctx context.Context, opts store.BackupExportOpts) (store.BackupBundle, error) {
	return s.bundle, s.err
}

// stubSource points the Collector at a fixed config path and auth dir.
type stubSource struct {
	cfgPath string
	authDir string
}

func (s stubSource) ConfigPath() string { return s.cfgPath }
func (s stubSource) AuthDir() string    { return s.authDir }

func TestCollectorBuildsV2Bundle(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgBody := "port: 8317\nauth-dir: auths\n"
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	authDir := filepath.Join(dir, "auths")
	if err := os.MkdirAll(filepath.Join(authDir, "nested"), 0o750); err != nil {
		t.Fatalf("mkdir auths: %v", err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "openai.json"), []byte(`{"key":"a"}`), 0o600); err != nil {
		t.Fatalf("write openai.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "nested", "other.json"), []byte(`{"key":"b"}`), 0o600); err != nil {
		t.Fatalf("write nested/other.json: %v", err)
	}

	rows := []json.RawMessage{json.RawMessage(`{"id":1}`)}
	exported := store.BackupBundle{
		Resources: map[string]store.BackupResourceData{
			"api_keys": {Tables: map[string][]json.RawMessage{"api_keys": rows}},
		},
	}
	c := NewCollector(stubExporter{bundle: exported}, stubSource{cfgPath: cfgPath, authDir: authDir})

	bundle, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if bundle.Version != 2 {
		t.Errorf("Version = %d, want 2", bundle.Version)
	}
	if bundle.Mode != "full" {
		t.Errorf("Mode = %q, want %q", bundle.Mode, "full")
	}
	if bundle.ConfigYAML != cfgBody {
		t.Errorf("ConfigYAML = %q, want %q", bundle.ConfigYAML, cfgBody)
	}
	if len(bundle.AuthFiles) != 2 {
		t.Fatalf("AuthFiles len = %d, want 2", len(bundle.AuthFiles))
	}
	got := make(map[string]string)
	for _, f := range bundle.AuthFiles {
		got[f.Path] = f.Content
	}
	if got["openai.json"] != `{"key":"a"}` {
		t.Errorf("AuthFiles[openai.json] = %q", got["openai.json"])
	}
	if got["nested/other.json"] != `{"key":"b"}` {
		t.Errorf("AuthFiles[nested/other.json] = %q", got["nested/other.json"])
	}
	if bundle.Summary["config"] != 1 {
		t.Errorf("Summary[config] = %d, want 1", bundle.Summary["config"])
	}
	if bundle.Summary["auth_files"] != 2 {
		t.Errorf("Summary[auth_files] = %d, want 2", bundle.Summary["auth_files"])
	}
	res, ok := bundle.Resources["api_keys"]
	if !ok {
		t.Fatalf("Resources missing api_keys key")
	}
	if len(res.Tables["api_keys"]) != 1 {
		t.Errorf("Resources[api_keys].Tables[api_keys] len = %d, want 1", len(res.Tables["api_keys"]))
	}
}

func TestCollectorNilSources(t *testing.T) {
	c := NewCollector(nil, stubSource{cfgPath: filepath.Join(t.TempDir(), "missing.yaml"), authDir: ""})

	bundle, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if bundle.Version != 2 {
		t.Errorf("Version = %d, want 2", bundle.Version)
	}
	if bundle.Mode != "full" {
		t.Errorf("Mode = %q, want %q", bundle.Mode, "full")
	}
	if len(bundle.Resources) != 0 {
		t.Errorf("Resources len = %d, want 0", len(bundle.Resources))
	}
	if bundle.ConfigYAML != "" {
		t.Errorf("ConfigYAML = %q, want empty", bundle.ConfigYAML)
	}
	if len(bundle.AuthFiles) != 0 {
		t.Errorf("AuthFiles len = %d, want 0", len(bundle.AuthFiles))
	}
	if bundle.Summary["auth_files"] != 0 {
		t.Errorf("Summary[auth_files] = %d, want 0", bundle.Summary["auth_files"])
	}
}
