package test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/backup"
)

// TestCollectorConfigAuthsRoundTrip is a hermetic integration that exercises
// the real Collector + Restorer against a temp filesystem, confirming
// config.yaml and auths/ files survive a collect→restore cycle without any
// S3/DB involvement. The PG-export path is covered separately by the store's
// backup round-trip tests (they need a live Postgres); the S3 upload/download
// path is covered by internal/backup's in-memory-stub tests. Together those
// three cover the full lifecycle without external infrastructure.
func TestCollectorConfigAuthsRoundTrip(t *testing.T) {
	dir := t.TempDir()

	cfgPath := filepath.Join(dir, "config.yaml")
	authDir := filepath.Join(dir, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgContent := "api_key: sk-live-test\nmodel: claude-sonnet\n"
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "openai.json"), []byte(`{"api_key":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Collect full state (no PG exporter → config + auths only).
	src := backup.NewPathSource(cfgPath, authDir)
	collector := backup.NewCollector(nil, src)
	bundle, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if bundle.Version != 2 || bundle.Mode != "full" {
		t.Errorf("bundle version=%d mode=%q; want 2/full", bundle.Version, bundle.Mode)
	}
	if bundle.ConfigYAML != cfgContent {
		t.Errorf("ConfigYAML = %q; want %q", bundle.ConfigYAML, cfgContent)
	}
	if len(bundle.AuthFiles) != 1 || bundle.AuthFiles[0].Path != "openai.json" {
		t.Errorf("AuthFiles = %+v; want one openai.json", bundle.AuthFiles)
	}

	// Restore into a fresh directory (merge — file is absent → written).
	outDir := t.TempDir()
	outCfg := filepath.Join(outDir, "config.yaml")
	outAuths := filepath.Join(outDir, "auths")
	restorer := backup.NewRestorer(nil, nil, outCfg, outAuths)
	res, err := restorer.Restore(context.Background(), bundle, backup.RestoreModeMerge)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Files != 2 { // config.yaml + openai.json
		t.Errorf("Files = %d; want 2", res.Files)
	}

	gotCfg, err := os.ReadFile(outCfg)
	if err != nil {
		t.Fatalf("read restored config: %v", err)
	}
	if string(gotCfg) != cfgContent {
		t.Errorf("restored config = %q; want %q", gotCfg, cfgContent)
	}
	gotAuth, err := os.ReadFile(filepath.Join(outAuths, "openai.json"))
	if err != nil {
		t.Fatalf("read restored auth file: %v", err)
	}
	if string(gotAuth) != `{"api_key":"x"}` {
		t.Errorf("restored auth = %q; want openai content", gotAuth)
	}
}
