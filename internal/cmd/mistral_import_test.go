package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

func writeVibeEnv(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write vibe env: %v", err)
	}
	return path
}

func TestDiscoverMistralAPIKey_FlagWins(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "from-env")
	envPath := writeVibeEnv(t, "MISTRAL_API_KEY=from-file\n")

	key, source, err := discoverMistralAPIKey(&MistralImportOptions{APIKey: "  from-flag  ", VibeEnvPath: envPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "from-flag" {
		t.Errorf("key = %q, want from-flag", key)
	}
	if source != "-mistral-api-key flag" {
		t.Errorf("source = %q, want the flag", source)
	}
}

func TestDiscoverMistralAPIKey_EnvBeatsVibeFile(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", " from-env ")
	envPath := writeVibeEnv(t, "MISTRAL_API_KEY=from-file\n")

	key, source, err := discoverMistralAPIKey(&MistralImportOptions{VibeEnvPath: envPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "from-env" {
		t.Errorf("key = %q, want from-env", key)
	}
	if source != "MISTRAL_API_KEY env var" {
		t.Errorf("source = %q, want the env var", source)
	}
}

func TestDiscoverMistralAPIKey_VibeFileFallback(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "")
	envPath := writeVibeEnv(t, "OTHER=1\nMISTRAL_API_KEY=from-file\n")

	key, source, err := discoverMistralAPIKey(&MistralImportOptions{VibeEnvPath: envPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "from-file" {
		t.Errorf("key = %q, want from-file", key)
	}
	if source != envPath {
		t.Errorf("source = %q, want %q", source, envPath)
	}
}

func TestDiscoverMistralAPIKey_MissingKeyReportsError(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "")
	envPath := writeVibeEnv(t, "OTHER=1\n")

	if _, _, err := discoverMistralAPIKey(&MistralImportOptions{VibeEnvPath: envPath}); err == nil {
		t.Fatal("expected an error when the env file carries no key")
	}
}

// Most users have no Mistral Vibe CLI, so the missing env file is the common
// failure; its error must say how to supply a key instead.
func TestDiscoverMistralAPIKey_MissingFileReportsActionableError(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "")

	_, _, err := discoverMistralAPIKey(&MistralImportOptions{VibeEnvPath: filepath.Join(t.TempDir(), "absent.env")})
	if err == nil {
		t.Fatal("expected an error when the env file is missing")
	}
	for _, hint := range []string{"-mistral-api-key", "MISTRAL_API_KEY"} {
		if !strings.Contains(err.Error(), hint) {
			t.Errorf("error %q does not mention %s", err, hint)
		}
	}
}

func TestResolveVibeEnvPath(t *testing.T) {
	if got, err := resolveVibeEnvPath("  /custom/.env  "); err != nil || got != "/custom/.env" {
		t.Fatalf("override: got %q, err %v", got, err)
	}
	if home, errHome := os.UserHomeDir(); errHome == nil {
		want := filepath.Join(home, ".vibe", ".env")
		if got, err := resolveVibeEnvPath("~/.vibe/.env"); err != nil || got != want {
			t.Fatalf("override with ~: got %q, err %v, want %q", got, err, want)
		}
	}
	if got, err := resolveVibeEnvPath("~other/.env"); err != nil || got != "~other/.env" {
		t.Fatalf("~user form should be left untouched: got %q, err %v", got, err)
	}

	t.Setenv("VIBE_HOME", "/vibe-home")
	got, err := resolveVibeEnvPath("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := filepath.Join("/vibe-home", ".env"); got != want {
		t.Fatalf("VIBE_HOME: got %q, want %q", got, want)
	}

	t.Setenv("VIBE_HOME", "")
	home, errHome := os.UserHomeDir()
	if errHome != nil {
		t.Skipf("home dir unavailable: %v", errHome)
	}
	got, err = resolveVibeEnvPath("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := filepath.Join(home, ".vibe", ".env"); got != want {
		t.Fatalf("default: got %q, want %q", got, want)
	}
}

func readImportedCredential(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credential: %v", err)
	}
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(raw, &metadata); errUnmarshal != nil {
		t.Fatalf("unmarshal credential: %v", errUnmarshal)
	}
	return metadata
}

func TestDoMistralImport_WritesCompatCredential(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "")
	authDir := t.TempDir()
	cfg := &config.Config{}
	cfg.AuthDir = authDir

	DoMistralImport(cfg, &MistralImportOptions{APIKey: "sk-mistral-test"})

	path := filepath.Join(authDir, "mistral-default.json")
	metadata := readImportedCredential(t, path)

	if metadata["type"] != util.MistralProvider {
		t.Errorf("type = %v, want %s", metadata["type"], util.MistralProvider)
	}
	if metadata["api_key"] != "sk-mistral-test" {
		t.Errorf("api_key = %v", metadata["api_key"])
	}
	if metadata["base_url"] != util.MistralDefaultBaseURL {
		t.Errorf("base_url = %v, want %s", metadata["base_url"], util.MistralDefaultBaseURL)
	}
	if metadata["compat_name"] != util.MistralProvider {
		t.Errorf("compat_name = %v, want %s", metadata["compat_name"], util.MistralProvider)
	}
}

func TestDoMistralImport_ReimportReusesSameFile(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "")
	authDir := t.TempDir()
	cfg := &config.Config{}
	cfg.AuthDir = authDir

	DoMistralImport(cfg, &MistralImportOptions{APIKey: "first-key"})
	DoMistralImport(cfg, &MistralImportOptions{APIKey: "second-key"})

	entries, err := os.ReadDir(authDir)
	if err != nil {
		t.Fatalf("read auth dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("expected a single credential file, got %v", names)
	}

	metadata := readImportedCredential(t, filepath.Join(authDir, entries[0].Name()))
	if metadata["api_key"] != "second-key" {
		t.Errorf("api_key = %v, want second-key", metadata["api_key"])
	}
}

func TestDoMistralImport_LabelCannotEscapeAuthDir(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "")
	authDir := t.TempDir()
	cfg := &config.Config{}
	cfg.AuthDir = authDir

	DoMistralImport(cfg, &MistralImportOptions{APIKey: "sk-mistral-test", Label: "../escaped"})

	entries, err := os.ReadDir(authDir)
	if err != nil {
		t.Fatalf("read auth dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected the credential to stay inside the auth dir, got %d entries", len(entries))
	}
	if filepath.Base(entries[0].Name()) != entries[0].Name() {
		t.Fatalf("unexpected nested path %q", entries[0].Name())
	}
}
