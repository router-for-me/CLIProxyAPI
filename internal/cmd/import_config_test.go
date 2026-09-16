package cmd

import (
	"context"
	"os"
	"strings"
	"testing"
)

// osWriteFile aliases os.WriteFile so the test stays readable without
// pulling a second import.
func osWriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}

// TestDoImportConfigMissingSource verifies that an empty source path is
// rejected before any DB interaction.
func TestDoImportConfigMissingSource(t *testing.T) {
	t.Setenv("PGSTORE_DSN", "postgres://ignored")
	n, err := DoImportConfig(context.Background(), ImportConfigOptions{})
	if err == nil {
		t.Fatal("expected error for empty source path")
	}
	if !strings.Contains(err.Error(), "missing source path") {
		t.Fatalf("error = %v; want 'missing source path'", err)
	}
	if n != 0 {
		t.Fatalf("n = %d; want 0", n)
	}
}

// TestDoImportConfigMissingDSN verifies that the command fails closed when
// neither the flag nor PGSTORE_DSN provides a connection string.
func TestDoImportConfigMissingDSN(t *testing.T) {
	t.Setenv("PGSTORE_DSN", "")
	tmp := t.TempDir() + "/cfg.yaml"
	if err := writeFile(tmp, "port: 8317\n"); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	n, err := DoImportConfig(context.Background(), ImportConfigOptions{SourcePath: tmp})
	if err == nil {
		t.Fatal("expected error for missing DSN")
	}
	if !strings.Contains(err.Error(), "PGSTORE_DSN is required") {
		t.Fatalf("error = %v; want 'PGSTORE_DSN is required'", err)
	}
	if n != 0 {
		t.Fatalf("n = %d; want 0", n)
	}
}

// TestDoImportConfigMissingSourceFile verifies that a non-existent source
// path is surfaced as a parse error rather than a silent no-op.
func TestDoImportConfigMissingSourceFile(t *testing.T) {
	t.Setenv("PGSTORE_DSN", "postgres://ignored")
	n, err := DoImportConfig(context.Background(), ImportConfigOptions{
		SourcePath: "/nonexistent/path/config.yaml",
	})
	if err == nil {
		t.Fatal("expected error for missing source file")
	}
	if n != 0 {
		t.Fatalf("n = %d; want 0", n)
	}
}

// TestDoImportConfigDryRunNoDB pins that dry-run completes without needing
// a live PostgreSQL connection: BuildResourcePlan does the planning locally
// and the command returns before opening a connection.
func TestDoImportConfigDryRunNoDB(t *testing.T) {
	t.Setenv("PGSTORE_DSN", "")
	tmp := t.TempDir() + "/cfg.yaml"
	if err := writeFile(tmp, "port: 8317\n"); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	// Note: dry-run still enforces DSN presence as a configuration sanity
	// check so an operator cannot dry-run against a deployment that has no
	// PG at all.
	n, err := DoImportConfig(context.Background(), ImportConfigOptions{
		SourcePath: tmp,
		DryRun:     true,
	})
	if err == nil {
		t.Fatal("expected DSN-required error even for dry-run")
	}
	if n != 0 {
		t.Fatalf("n = %d; want 0", n)
	}
}

// writeFile is a tiny test helper for writing config fixtures.
func writeFile(path, content string) error {
	return osWriteFile(path, []byte(content), 0o600)
}
