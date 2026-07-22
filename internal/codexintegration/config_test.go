package codexintegration

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestSetupPreviewDoesNotWrite(t *testing.T) {
	lifecycle := testLifecycle(t)
	models, providers := catalogTestModels()
	result, err := lifecycle.Setup(models, providers, false, false)
	if err != nil {
		t.Fatalf("Setup(preview) error = %v", err)
	}
	if !result.Changed || result.Applied {
		t.Fatalf("Setup(preview) result = %#v", result)
	}
	if _, err = os.Stat(lifecycle.Paths.ConfigFile); !os.IsNotExist(err) {
		t.Fatalf("preview created config: %v", err)
	}
	if _, err = os.Stat(lifecycle.Paths.CatalogFile); !os.IsNotExist(err) {
		t.Fatalf("preview created catalog: %v", err)
	}
}

func TestSetupPreservesConfigShapeAndIsIdempotent(t *testing.T) {
	fixtures := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "comments and table", body: "# user comment\n\n[features]\nmulti_agent = true\n"},
		{name: "CRLF", body: "# user comment\r\n\r\n[features]\r\nmulti_agent = true\r\n"},
		{name: "multiple profiles", body: "[profiles.work]\nmodel = \"gpt-5.6-sol\"\n\n[features]\nweb_search = true\n"},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			lifecycle := testLifecycle(t)
			if err := os.MkdirAll(lifecycle.Paths.Home, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(lifecycle.Paths.ConfigFile, []byte(fixture.body), 0o640); err != nil {
				t.Fatal(err)
			}
			models, providers := catalogTestModels()
			result, err := lifecycle.Setup(models, providers, true, false)
			if err != nil {
				t.Fatalf("Setup(apply) error = %v", err)
			}
			if !result.Applied || !result.RestartRequired {
				t.Fatalf("Setup(apply) result = %#v", result)
			}
			configured, err := os.ReadFile(lifecycle.Paths.ConfigFile)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(configured, []byte(ManagedConfigBegin)) || !bytes.Contains(configured, []byte(`openai_base_url = "http://127.0.0.1:8317/v1"`)) {
				t.Fatalf("managed config missing: %s", configured)
			}
			if table := bytes.Index(configured, []byte("[features]")); table >= 0 && bytes.Index(configured, []byte(ManagedConfigBegin)) > table {
				t.Fatalf("managed root keys inserted after table: %s", configured)
			}
			if strings.Contains(fixture.body, "\r\n") && bytes.Contains(configured, []byte("\n")) && !bytes.Contains(configured, []byte("\r\n")) {
				t.Fatal("CRLF style was not preserved")
			}
			info, err := os.Stat(lifecycle.Paths.ConfigFile)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o640 {
				t.Fatalf("config mode = %o, want 640", info.Mode().Perm())
			}
			catalogData, err := os.ReadFile(lifecycle.Paths.CatalogFile)
			if err != nil {
				t.Fatal(err)
			}
			if err = registry.ValidateCodexClientModelsJSON(catalogData); err != nil {
				t.Fatalf("written catalog invalid: %v", err)
			}

			second, err := lifecycle.Setup(models, providers, true, false)
			if err != nil {
				t.Fatalf("second Setup() error = %v", err)
			}
			if second.Changed || second.Applied {
				t.Fatalf("second Setup() not idempotent: %#v", second)
			}
		})
	}
}

func TestSetupRejectsUserOwnedRootKeys(t *testing.T) {
	lifecycle := testLifecycle(t)
	if err := os.MkdirAll(lifecycle.Paths.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lifecycle.Paths.ConfigFile, []byte("openai_base_url = \"https://gateway.example/v1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	models, providers := catalogTestModels()
	if _, err := lifecycle.Setup(models, providers, true, false); err == nil || !strings.Contains(err.Error(), "openai_base_url") {
		t.Fatalf("Setup() error = %v, want ownership conflict", err)
	}
	if _, err := os.Stat(lifecycle.Paths.JournalFile); !os.IsNotExist(err) {
		t.Fatalf("conflict wrote journal: %v", err)
	}
}

func TestSetupMigratesOnlyKnownOpenCodexMarker(t *testing.T) {
	lifecycle := testLifecycle(t)
	if err := os.MkdirAll(lifecycle.Paths.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	original := "# Auto-injected by opencodex\nopenai_base_url = \"http://127.0.0.1:10100/v1\"\n# Auto-injected by opencodex\nmodel_catalog_json = \"/tmp/opencodex-catalog.json\"\n\n[features]\nweb_search = true\n"
	if err := os.WriteFile(lifecycle.Paths.ConfigFile, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	models, providers := catalogTestModels()
	if _, err := lifecycle.Setup(models, providers, false, false); err == nil {
		t.Fatal("normal Setup() accepted OpenCodex-owned root keys")
	}
	if _, err := lifecycle.Setup(models, providers, true, true); err != nil {
		t.Fatalf("migration Setup() error = %v", err)
	}
	configured, err := os.ReadFile(lifecycle.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(configured, []byte("Auto-injected by opencodex")) || !bytes.Contains(configured, []byte(ManagedConfigBegin)) {
		t.Fatalf("migration result = %s", configured)
	}
}

func TestRestorePreservesPostSetupUserChanges(t *testing.T) {
	lifecycle := testLifecycle(t)
	models, providers := catalogTestModels()
	if _, err := lifecycle.Setup(models, providers, true, false); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	configured, err := os.ReadFile(lifecycle.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	configured = append(configured, []byte("\n[features]\nweb_search = true\n")...)
	if err = os.WriteFile(lifecycle.Paths.ConfigFile, configured, 0o600); err != nil {
		t.Fatal(err)
	}

	preview, err := lifecycle.Restore(false)
	if err != nil {
		t.Fatalf("Restore(preview) error = %v", err)
	}
	if !preview.Changed || preview.Applied {
		t.Fatalf("Restore(preview) = %#v", preview)
	}
	if _, err = lifecycle.Restore(true); err != nil {
		t.Fatalf("Restore(apply) error = %v", err)
	}
	restored, err := os.ReadFile(lifecycle.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(restored, []byte(ManagedConfigBegin)) || !bytes.Contains(restored, []byte("web_search = true")) {
		t.Fatalf("Restore() result = %s", restored)
	}
	if _, err = os.Stat(lifecycle.Paths.CatalogFile); !os.IsNotExist(err) {
		t.Fatalf("Restore() left catalog: %v", err)
	}
	second, err := lifecycle.Restore(true)
	if err != nil || second.Changed || second.Applied {
		t.Fatalf("second Restore() = %#v, %v", second, err)
	}
}

func TestRestoreReinstatesPreexistingCatalogAndIsIdempotent(t *testing.T) {
	lifecycle := testLifecycle(t)
	if err := os.MkdirAll(lifecycle.Paths.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	originalConfig := []byte("# original\n[features]\nweb_search = true\n")
	originalCatalog := []byte("user-owned catalog bytes\n")
	if err := os.WriteFile(lifecycle.Paths.ConfigFile, originalConfig, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lifecycle.Paths.CatalogFile, originalCatalog, 0o600); err != nil {
		t.Fatal(err)
	}
	models, providers := catalogTestModels()
	if _, err := lifecycle.Setup(models, providers, true, false); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if _, err := lifecycle.Restore(true); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	restoredConfig, err := os.ReadFile(lifecycle.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	restoredCatalog, err := os.ReadFile(lifecycle.Paths.CatalogFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restoredConfig, originalConfig) || !bytes.Equal(restoredCatalog, originalCatalog) {
		t.Fatalf("restore mismatch: config=%q catalog=%q", restoredConfig, restoredCatalog)
	}
	second, err := lifecycle.Restore(true)
	if err != nil || second.Changed || second.Applied {
		t.Fatalf("second Restore() = %#v, %v", second, err)
	}
}

func TestResolvePathsPrecedence(t *testing.T) {
	integration := config.DefaultCodexIntegrationConfig()
	integration.CodexHome = filepath.Join(t.TempDir(), "configured")
	environmentHome := filepath.Join(t.TempDir(), "environment")
	explicitHome := filepath.Join(t.TempDir(), "explicit")
	t.Setenv("CODEX_HOME", environmentHome)

	paths, err := ResolvePaths(explicitHome, integration)
	if err != nil {
		t.Fatal(err)
	}
	if paths.Home != explicitHome {
		t.Fatalf("explicit home = %q, want %q", paths.Home, explicitHome)
	}
	paths, err = ResolvePaths("", integration)
	if err != nil {
		t.Fatal(err)
	}
	if paths.Home != integration.CodexHome {
		t.Fatalf("configured home = %q, want %q", paths.Home, integration.CodexHome)
	}
	integration.CodexHome = ""
	paths, err = ResolvePaths("", integration)
	if err != nil {
		t.Fatal(err)
	}
	if paths.Home != environmentHome {
		t.Fatalf("environment home = %q, want %q", paths.Home, environmentHome)
	}
}

func TestSetupRejectsSymlinkAndHeldLock(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		lifecycle := testLifecycle(t)
		if err := os.MkdirAll(lifecycle.Paths.Home, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "outside.toml")
		if err := os.WriteFile(target, []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, lifecycle.Paths.ConfigFile); err != nil {
			t.Fatal(err)
		}
		models, providers := catalogTestModels()
		if _, err := lifecycle.Setup(models, providers, true, false); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("Setup() error = %v, want symlink rejection", err)
		}
	})

	t.Run("held lock", func(t *testing.T) {
		lifecycle := testLifecycle(t)
		if err := os.MkdirAll(lifecycle.Paths.Home, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lifecycle.Paths.LockFile, []byte("held"), 0o600); err != nil {
			t.Fatal(err)
		}
		models, providers := catalogTestModels()
		if _, err := lifecycle.Setup(models, providers, true, false); err == nil || !strings.Contains(err.Error(), "lock") {
			t.Fatalf("Setup() error = %v, want lock rejection", err)
		}
	})
}

func TestSetupCompileFailureLeavesHomeUntouched(t *testing.T) {
	lifecycle := testLifecycle(t)
	models, providers := catalogTestModels()
	filtered := models[:0]
	for _, model := range models {
		if catalogString(model, "id") != "gemini-pro-agent" {
			filtered = append(filtered, model)
		}
	}
	if _, err := lifecycle.Setup(filtered, providers, true, false); err == nil {
		t.Fatal("Setup() error = nil, want compile failure")
	}
	if _, err := os.Stat(lifecycle.Paths.Home); !os.IsNotExist(err) {
		t.Fatalf("compile failure touched home: %v", err)
	}
}

func testLifecycle(t *testing.T) *Lifecycle {
	t.Helper()
	integration := config.DefaultCodexIntegrationConfig()
	integration.Enabled = true
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{CodexIntegration: integration},
		Host:      "127.0.0.1",
		Port:      8317,
	}
	lifecycle, err := NewLifecycle(cfg, filepath.Join(t.TempDir(), "codex-home"))
	if err != nil {
		t.Fatalf("NewLifecycle() error = %v", err)
	}
	return lifecycle
}
