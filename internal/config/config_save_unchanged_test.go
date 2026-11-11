package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// saveAfterServerLoad loads configYAML the way the server does, applies change, saves,
// and returns the saved file.
func saveAfterServerLoad(t *testing.T, configYAML string, change func(*Config)) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte(configYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}
	cfg, errLoad := LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	// cmd/server/main.go replaces the default auth-dir with its resolved path before management saves.
	if cfg.AuthDir == "" || cfg.AuthDir == DefaultAuthDir {
		home, errHome := os.UserHomeDir()
		if errHome != nil {
			t.Fatalf("os.UserHomeDir() error = %v", errHome)
		}
		cfg.AuthDir = filepath.Join(home, ".cli-proxy-api")
	}
	change(cfg)
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}
	saved, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	return string(saved)
}

func TestSaveConfigPreserveComments_TogglingOneSettingKeepsTheRest(t *testing.T) {
	const original = `port: 8317
auth-dir: "~/.cli-proxy-api"
api-keys:
  - sk-test
payload:
  override:
# Pin the service tier for gpt-5.
    - models:
        - name: gpt-5
          protocol: openai
      params:
        service_tier: default
`
	saved := saveAfterServerLoad(t, original, func(cfg *Config) { cfg.LoggingToFile = true })

	want := original + "logging-to-file: true\n"
	if saved != want {
		t.Fatalf("saved config:\n%s\nwant:\n%s", saved, want)
	}
}

func TestSaveConfigPreserveComments_WritesChangedDefaults(t *testing.T) {
	saved := saveAfterServerLoad(t, "port: 8317\n", func(cfg *Config) { cfg.RedisUsageQueueRetentionSeconds = 120 })

	want := "port: 8317\nredis-usage-queue-retention-seconds: 120\n"
	if saved != want {
		t.Fatalf("saved config:\n%s\nwant:\n%s", saved, want)
	}
}

func TestSaveConfigPreserveComments_RemovedPayloadParamStaysRemoved(t *testing.T) {
	const original = `payload:
  override:
    - models:
        - name: gpt-5
          protocol: openai
      params:
        service_tier: priority
        temperature: 0.7
`
	saved := saveAfterServerLoad(t, original, func(cfg *Config) {
		delete(cfg.Payload.Override[0].Params, "service_tier")
	})

	if strings.Contains(saved, "service_tier") {
		t.Fatalf("saved config still sets the removed param:\n%s", saved)
	}
}

func TestSaveConfigPreserveComments_WritesAuthDirReset(t *testing.T) {
	saved := saveAfterServerLoad(t, "auth-dir: .\n", func(cfg *Config) { cfg.AuthDir = "" })

	want := "auth-dir: \"\"\n"
	if saved != want {
		t.Fatalf("saved config:\n%s\nwant:\n%s", saved, want)
	}
}

func TestSaveConfigPreserveComments_RenamedPayloadKeyReplacesOldKey(t *testing.T) {
	for name, tc := range map[string]struct{ oldKey, newKey string }{
		"surrounding spaces": {oldKey: "owner", newKey: " owner "},
		"number to string":   {oldKey: "1.0", newKey: "1.0"},
	} {
		t.Run(name, func(t *testing.T) {
			original := `payload:
  override:
    - models:
        - name: gpt-5
          protocol: openai
      params:
        metadata:
          ` + tc.oldKey + `: alice
`
			saved := saveAfterServerLoad(t, original, func(cfg *Config) {
				cfg.Payload.Override[0].Params["metadata"] = map[string]any{tc.newKey: "alice"}
			})

			reloaded, errParse := ParseConfigBytes([]byte(saved))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v", errParse)
			}
			metadata, ok := reloaded.Payload.Override[0].Params["metadata"].(map[string]any)
			if !ok || len(metadata) != 1 || metadata[tc.newKey] != "alice" {
				t.Fatalf("saved metadata = %#v, want only %q: alice; saved config:\n%s",
					reloaded.Payload.Override[0].Params["metadata"], tc.newKey, saved)
			}
		})
	}
}
