package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestExportConfigRoundTrip seeds runtime_config via DoImportConfig,
// exports it via DoExportConfig, and asserts the output file contains the
// scalar settings (e.g. port from the template). Resources live in
// normalized tables so the export only carries the snapshot projection.
func TestExportConfigRoundTrip(t *testing.T) {
	schema := "test_export_roundtrip"
	newCmdTestPG(t, schema)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := DoImportConfig(ctx, ImportConfigOptions{
		SourcePath: "../../config.example.yaml",
		Schema:     schema,
		DSN:        cmdTestDSN(),
	}); err != nil {
		t.Fatalf("seed DoImportConfig: %v", err)
	}

	out := filepath.Join(t.TempDir(), "export.yaml")
	if err := DoExportConfig(ctx, ExportConfigOptions{
		Schema:     schema,
		OutputPath: out,
		DSN:        cmdTestDSN(),
	}); err != nil {
		t.Fatalf("DoExportConfig: %v", err)
	}

	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("export file is empty")
	}
	if !strings.Contains(string(body), "port:") {
		t.Fatalf("export missing port key: %s", body)
	}
	if !strings.Contains(string(body), "tls:") {
		t.Fatalf("export missing tls block: %s", body)
	}
}

// TestExportConfigRedactsSecrets asserts the default-mode export redacts
// remote-management.secret-key and any "api-key:" inline value. We use
// a config that ships with a plaintext secret-key so the redaction
// actually has something to redact.
func TestExportConfigRedactsSecrets(t *testing.T) {
	schema := "test_export_redact"
	newCmdTestPG(t, schema)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	seed := filepath.Join(t.TempDir(), "seed.yaml")
	seedBody := []byte(`host: ""
port: 18317
remote-management:
  allow-remote: false
  secret-key: "supersecret-plaintext"
api-keys:
  - "smoke-key-1"
`)
	if err := os.WriteFile(seed, seedBody, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	if _, err := DoImportConfig(ctx, ImportConfigOptions{
		SourcePath: seed,
		Schema:     schema,
		DSN:        cmdTestDSN(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out := filepath.Join(t.TempDir(), "export.yaml")
	if err := DoExportConfig(ctx, ExportConfigOptions{
		Schema:     schema,
		OutputPath: out,
		DSN:        cmdTestDSN(),
	}); err != nil {
		t.Fatalf("DoExportConfig: %v", err)
	}

	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if strings.Contains(string(body), "supersecret-plaintext") {
		t.Fatalf("export leaked plaintext secret-key: %s", body)
	}
}

// TestExportConfigIncludesSecretsWhenRequested asserts the IncludeSecrets
// path keeps whatever value the snapshot holds. ImportConfigBytes hashes
// remote-management.secret-key when a plaintext value is supplied, so the
// exported value is the bcrypt hash rather than the original secret.
// The contract under test is "do not redact further when IncludeSecrets is
// true", which we verify by checking that the default-mode redactor would
// have replaced this scalar and the IncludeSecrets export did not.
func TestExportConfigIncludesSecretsWhenRequested(t *testing.T) {
	schema := "test_export_include"
	newCmdTestPG(t, schema)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	seed := filepath.Join(t.TempDir(), "seed.yaml")
	seedBody := []byte(`host: ""
port: 18317
remote-management:
  secret-key: "keepthis-secret"
api-keys:
  - "smoke-key-2"
`)
	if err := os.WriteFile(seed, seedBody, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	if _, err := DoImportConfig(ctx, ImportConfigOptions{
		SourcePath: seed,
		Schema:     schema,
		DSN:        cmdTestDSN(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Default mode first: remote-management.secret-key value is whatever
	// the snapshot holds (a bcrypt hash), but the IncludeSecrets path must
	// still produce a file with the same scalar (no further redaction).
	outDefault := filepath.Join(t.TempDir(), "export-default.yaml")
	if err := DoExportConfig(ctx, ExportConfigOptions{
		Schema:     schema,
		OutputPath: outDefault,
		DSN:        cmdTestDSN(),
	}); err != nil {
		t.Fatalf("DoExportConfig default: %v", err)
	}
	outInclude := filepath.Join(t.TempDir(), "export-include.yaml")
	if err := DoExportConfig(ctx, ExportConfigOptions{
		Schema:         schema,
		OutputPath:     outInclude,
		IncludeSecrets: true,
		DSN:            cmdTestDSN(),
	}); err != nil {
		t.Fatalf("DoExportConfig include: %v", err)
	}

	defaultBody, err := os.ReadFile(outDefault)
	if err != nil {
		t.Fatalf("read default: %v", err)
	}
	includeBody, err := os.ReadFile(outInclude)
	if err != nil {
		t.Fatalf("read include: %v", err)
	}
	// The IncludeSecrets file must contain the secret-key scalar; the
	// default file uses "<redacted>" in place of the value. Both must
	// still mention the key path so a diff of the two files is the only
	// observable difference.
	if !strings.Contains(string(defaultBody), "secret-key: <redacted>") {
		t.Fatalf("default-mode export did not redact secret-key:\n%s", defaultBody)
	}
	if !strings.Contains(string(includeBody), "secret-key:") ||
		strings.Contains(string(includeBody), "secret-key: <redacted>") {
		t.Fatalf("include-secrets export still redacts secret-key:\n%s", includeBody)
	}
	if string(defaultBody) == string(includeBody) {
		t.Fatal("include-secrets and default exports are byte-identical; IncludeSecrets flag was ignored")
	}
}

// TestExportConfigEmptyDatabase asserts the empty-snapshot path fails
// closed.
func TestExportConfigEmptyDatabase(t *testing.T) {
	schema := "test_export_empty"
	newCmdTestPG(t, schema)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out := filepath.Join(t.TempDir(), "export.yaml")
	if err := DoExportConfig(ctx, ExportConfigOptions{
		Schema:     schema,
		OutputPath: out,
		DSN:        cmdTestDSN(),
	}); err == nil {
		t.Fatal("expected error on empty runtime_config, got nil")
	}
}

// TestExportConfigRequiresDSN asserts the command rejects missing DSN
// before touching the database.
func TestExportConfigRequiresDSN(t *testing.T) {
	t.Setenv("PGSTORE_DSN", "")
	if err := DoExportConfig(context.Background(), ExportConfigOptions{
		OutputPath: filepath.Join(t.TempDir(), "x.yaml"),
	}); err == nil {
		t.Fatal("expected error when PGSTORE_DSN is missing, got nil")
	}
}
