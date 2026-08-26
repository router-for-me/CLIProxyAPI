package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfigBytesAndPersistMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	input := []byte("# keep\nport: 8317\nremote-management:\n  secret-key: plaintext\n")
	if err := os.WriteFile(path, input, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, persisted, err := ParseConfigBytesAndPersistMigrations(path, input)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.Port != 8317 || !looksLikeBcrypt(cfg.RemoteManagement.SecretKey) {
		t.Fatalf("unexpected parsed config: port=%d secret=%q", cfg.Port, cfg.RemoteManagement.SecretKey)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	if !bytes.Equal(disk, persisted) {
		t.Fatal("returned persisted bytes do not match disk")
	}
	if bytes.Contains(persisted, []byte("plaintext")) || !bytes.Contains(persisted, []byte("# keep")) {
		t.Fatalf("unexpected migrated bytes: %s", persisted)
	}
}

type failingMigrationFile struct {
	*os.File
	failWriteOnce bool
	failTruncate  bool
}

func (f *failingMigrationFile) Write(p []byte) (int, error) {
	if f.failWriteOnce {
		f.failWriteOnce = false
		n := len(p) / 2
		written, err := f.File.Write(p[:n])
		if err != nil {
			return written, err
		}
		return written, errors.New("injected partial write failure")
	}
	return f.File.Write(p)
}

func (f *failingMigrationFile) Truncate(size int64) error {
	if f.failTruncate {
		f.failTruncate = false
		return errors.New("injected truncate failure")
	}
	return f.File.Truncate(size)
}

func testMigrationPersistenceFailureIsNonFatal(t *testing.T, wrap func(*os.File) migrationFile) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	input := []byte("port: 8317\nremote-management:\n  secret-key: plaintext\n")
	if err := os.WriteFile(path, input, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	originalOpen := openMigrationFile
	openMigrationFile = func(configFile string) (migrationFile, error) {
		f, err := os.OpenFile(configFile, os.O_RDWR, 0)
		if err != nil {
			return nil, err
		}
		return wrap(f), nil
	}
	t.Cleanup(func() { openMigrationFile = originalOpen })
	cfg, persisted, err := ParseConfigBytesAndPersistMigrations(path, input)
	if err != nil {
		t.Fatalf("migration persistence failure rejected valid config: %v", err)
	}
	if cfg.Port != 8317 || !looksLikeBcrypt(cfg.RemoteManagement.SecretKey) {
		t.Fatalf("unexpected parsed config: port=%d secret=%q", cfg.Port, cfg.RemoteManagement.SecretKey)
	}
	if !bytes.Equal(persisted, input) {
		t.Fatal("reported migration bytes after persistence failure")
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !bytes.Equal(disk, input) {
		t.Fatalf("persistence failure changed config: %s", disk)
	}
}

func TestParseConfigBytesAndPersistMigrationsPartialWriteFailureIsNonFatal(t *testing.T) {
	testMigrationPersistenceFailureIsNonFatal(t, func(f *os.File) migrationFile {
		return &failingMigrationFile{File: f, failWriteOnce: true}
	})
}

func TestParseConfigBytesAndPersistMigrationsTruncateFailureIsNonFatal(t *testing.T) {
	testMigrationPersistenceFailureIsNonFatal(t, func(f *os.File) migrationFile {
		return &failingMigrationFile{File: f, failTruncate: true}
	})
}

func TestParseConfigBytesAndPersistMigrationsOpenFailureIsNonFatal(t *testing.T) {
	input := []byte("port: 8317\nremote-management:\n  secret-key: plaintext\n")
	cfg, persisted, err := ParseConfigBytesAndPersistMigrations(t.TempDir(), input)
	if err != nil {
		t.Fatalf("migration open failure rejected valid config: %v", err)
	}
	if cfg.Port != 8317 || !looksLikeBcrypt(cfg.RemoteManagement.SecretKey) {
		t.Fatalf("unexpected parsed config: port=%d secret=%q", cfg.Port, cfg.RemoteManagement.SecretKey)
	}
	if !bytes.Equal(persisted, input) {
		t.Fatal("reported migration bytes after open failure")
	}
}

func TestParseConfigBytesAndPersistMigrationsLeavesNewerGenerationUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	old := []byte("remote-management:\n  secret-key: plaintext\n")
	newer := []byte("port: 9999\n")
	if err := os.WriteFile(path, newer, 0o644); err != nil {
		t.Fatalf("write newer config: %v", err)
	}
	_, persisted, err := ParseConfigBytesAndPersistMigrations(path, old)
	if err != nil {
		t.Fatalf("parse old generation: %v", err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !bytes.Equal(disk, newer) {
		t.Fatalf("old migration overwrote newer generation: %s", disk)
	}
	if !bytes.Equal(persisted, old) {
		t.Fatal("reported migration that was not persisted")
	}
}

func TestParseConfigBytesAndPersistMigrationsLeavesHashedConfigUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	input := []byte("remote-management:\n  secret-key: $2a$10$already-hashed\n")
	if err := os.WriteFile(path, input, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, persisted, err := ParseConfigBytesAndPersistMigrations(path, input)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !bytes.Equal(persisted, input) || !bytes.Equal(disk, input) {
		t.Fatal("hashed config was unexpectedly rewritten")
	}
}

func TestSaveConfigPreserveCommentsUpdateNestedScalar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	input := []byte("# keep\nremote-management:\n  secret-key: old\n")
	if err := os.WriteFile(path, input, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := SaveConfigPreserveCommentsUpdateNestedScalar(path, []string{"remote-management", "secret-key"}, "new"); err != nil {
		t.Fatalf("update scalar: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !bytes.Contains(data, []byte("# keep")) || !bytes.Contains(data, []byte("secret-key: new")) {
		t.Fatalf("unexpected updated config: %s", data)
	}
}

func TestParseConfigBytesAndPersistMigrationsValidatesCodexLiveMediaRelay(t *testing.T) {
	_, _, err := ParseConfigBytesAndPersistMigrations(filepath.Join(t.TempDir(), "config.yaml"), []byte("codex:\n  live-media-relay:\n    enabled: true\n    ice-servers:\n      - urls: []\n"))
	if err == nil {
		t.Fatal("expected invalid live-media relay config to be rejected")
	}
}
