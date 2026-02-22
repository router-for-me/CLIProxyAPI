package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDefaultConfigPath_DefaultFallback(t *testing.T) {
	t.Setenv("CONFIG", "")
	t.Setenv("CONFIG_PATH", "")
	t.Setenv("CLIPROXY_CONFIG", "")
	t.Setenv("CLIPROXY_CONFIG_PATH", "")

	wd := t.TempDir()
	got := resolveDefaultConfigPath(wd, false)
	want := filepath.Join(wd, "config.yaml")
	if got != want {
		t.Fatalf("resolveDefaultConfigPath() = %q, want %q", got, want)
	}
}

func TestResolveDefaultConfigPath_PrefersEnvFile(t *testing.T) {
	wd := t.TempDir()
	envPath := filepath.Join(t.TempDir(), "env-config.yaml")
	if err := os.WriteFile(envPath, []byte("port: 8317\n"), 0o644); err != nil {
		t.Fatalf("write env config: %v", err)
	}

	t.Setenv("CONFIG_PATH", envPath)
	t.Setenv("CONFIG", "")
	t.Setenv("CLIPROXY_CONFIG", "")
	t.Setenv("CLIPROXY_CONFIG_PATH", "")

	got := resolveDefaultConfigPath(wd, true)
	if got != envPath {
		t.Fatalf("resolveDefaultConfigPath() = %q, want env path %q", got, envPath)
	}
}

func TestResolveDefaultConfigPath_PrefersCLIPROXYConfigEnv(t *testing.T) {
	wd := t.TempDir()
	envPath := filepath.Join(t.TempDir(), "cliproxy-config.yaml")
	if err := os.WriteFile(envPath, []byte("port: 8317\n"), 0o644); err != nil {
		t.Fatalf("write env config: %v", err)
	}

	t.Setenv("CONFIG", "")
	t.Setenv("CONFIG_PATH", "")
	t.Setenv("CLIPROXY_CONFIG", envPath)
	t.Setenv("CLIPROXY_CONFIG_PATH", "")

	got := resolveDefaultConfigPath(wd, true)
	if got != envPath {
		t.Fatalf("resolveDefaultConfigPath() = %q, want CLIPROXY_CONFIG path %q", got, envPath)
	}
}

func TestResolveDefaultConfigPath_CloudFallbackToNestedConfig(t *testing.T) {
	t.Setenv("CONFIG", "")
	t.Setenv("CONFIG_PATH", "")
	t.Setenv("CLIPROXY_CONFIG", "")
	t.Setenv("CLIPROXY_CONFIG_PATH", "")

	wd := t.TempDir()
	configPathAsDir := filepath.Join(wd, "config.yaml")
	if err := os.MkdirAll(configPathAsDir, 0o755); err != nil {
		t.Fatalf("mkdir config.yaml dir: %v", err)
	}
	nested := filepath.Join(wd, "config", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatalf("mkdir nested parent: %v", err)
	}
	if err := os.WriteFile(nested, []byte("port: 8317\n"), 0o644); err != nil {
		t.Fatalf("write nested config: %v", err)
	}

	got := resolveDefaultConfigPath(wd, true)
	if got != nested {
		t.Fatalf("resolveDefaultConfigPath() = %q, want nested path %q", got, nested)
	}
}

func TestResolveDefaultConfigPath_NonCloudFallbackToNestedConfigWhenDefaultIsDir(t *testing.T) {
	t.Setenv("CONFIG", "")
	t.Setenv("CONFIG_PATH", "")
	t.Setenv("CLIPROXY_CONFIG", "")
	t.Setenv("CLIPROXY_CONFIG_PATH", "")

	wd := t.TempDir()
	configPathAsDir := filepath.Join(wd, "config.yaml")
	if err := os.MkdirAll(configPathAsDir, 0o755); err != nil {
		t.Fatalf("mkdir config.yaml dir: %v", err)
	}
	nested := filepath.Join(wd, "config", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatalf("mkdir nested parent: %v", err)
	}
	if err := os.WriteFile(nested, []byte("port: 8317\n"), 0o644); err != nil {
		t.Fatalf("write nested config: %v", err)
	}

	got := resolveDefaultConfigPath(wd, false)
	if got != nested {
		t.Fatalf("resolveDefaultConfigPath() = %q, want nested path %q", got, nested)
	}
}
