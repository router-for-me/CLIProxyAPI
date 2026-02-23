package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgresStoreResolveDeletePathRejectsEscapeInputs(t *testing.T) {
	t.Parallel()

	store := &PostgresStore{authDir: t.TempDir()}
	absolute := filepath.Join(t.TempDir(), "outside.json")
	cases := []string{
		"../outside.json",
		absolute,
	}
	for _, id := range cases {
		if _, err := store.resolveDeletePath(id); err == nil {
			t.Fatalf("expected id %q to be rejected", id)
		}
	}
}

func TestPostgresStoreUpsertAuthRecordUsesManagedPathFromRelID(t *testing.T) {
	t.Parallel()

	authDir := filepath.Join(t.TempDir(), "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	store := &PostgresStore{
		authDir: authDir,
	}

	untrustedPath := filepath.Join(t.TempDir(), "untrusted.json")
	if err := os.WriteFile(untrustedPath, []byte(`{"source":"untrusted"}`), 0o600); err != nil {
		t.Fatalf("write untrusted auth file: %v", err)
	}

	err := store.upsertAuthRecord(context.Background(), "safe.json", untrustedPath)
	if err == nil {
		t.Fatal("expected upsert to fail because managed relID path does not exist")
	}
	if !strings.Contains(err.Error(), "read auth file") {
		t.Fatalf("expected read auth file error, got %v", err)
	}
}
