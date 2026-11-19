package api_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestWriteConfigRejectsAmbiguousOAuthChannels(t *testing.T) {
	for _, field := range []struct{ name, value string }{
		{"model-alias", `[{name: upstream, alias: public}]`},
		{"excluded-models", `[private-*]`},
		{"settings", `[{name: upstream, max-context-length: 8192}]`},
		{"request-scoped-errors", `[{status: 400, match: [invalid input], action: cooldown}]`},
	} {
		for _, layout := range []struct{ name, template string }{
			{"legacy", "oauth-%s: %s\n"},
			{"v8", "config-version: 8\noauth:\n  %s: %s\n"},
		} {
			for _, existing := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/existing=%t", field.name, layout.name, existing), func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "config.yaml")
					const original = "port: 8317\n"
					var before os.FileInfo
					if existing {
						if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
							t.Fatal(err)
						}
						var err error
						before, err = os.Stat(path)
						if err != nil {
							t.Fatal(err)
						}
					}
					channels := fmt.Sprintf(`{codex: %s, ' CODEX ': %s}`, field.value, field.value)
					body := []byte(fmt.Sprintf(layout.template, field.name, channels))
					if _, err := config.ParseConfigBytes(body); err == nil {
						t.Fatal("fixture must be rejected by the shared parser")
					}
					if err := api.WriteConfig(path, body); err == nil || !strings.Contains(err.Error(), "normalize to") {
						t.Errorf("WriteConfig error = %v, want ambiguous channel rejection", err)
					}
					saved, err := os.ReadFile(path)
					if !existing {
						if !os.IsNotExist(err) {
							t.Fatal("rejected save created the destination")
						}
						return
					}
					if err != nil || string(saved) != original {
						t.Fatal("rejected save changed the destination")
					}
					after, err := os.Stat(path)
					if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.ModTime() != after.ModTime() {
						t.Fatal("rejected save changed destination metadata")
					}
				})
			}
		}
	}
}

func TestWriteConfigPreservesScalarContent(t *testing.T) {
	for _, tc := range []struct{ name, scalar, want string }{
		{"literal", "|-\n  first\n  # scalar data\n", "first\n# scalar data"},
		{"folded", ">-\n  first\n  # scalar data\n", "first # scalar data"},
		{"explicit indent", "|2-\n  # scalar data\n", "# scalar data"},
		{"single quoted", "'first\n  # scalar data'\n", "first # scalar data"},
		{"double quoted", "\"first\n  # scalar data\"\n", "first # scalar data"},
	} {
		for _, v8 := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/v8=%t", tc.name, v8), func(t *testing.T) {
				body := "# operator comment\nhost: " + tc.scalar + "port: 8317\n"
				if v8 {
					body = "config-version: 8\nserver:\n  " + strings.ReplaceAll(body, "\n", "\n  ")
				}
				path := filepath.Join(t.TempDir(), "config.yaml")
				if err := api.WriteConfig(path, []byte(body)); err != nil {
					t.Fatal(err)
				}
				loaded, err := config.LoadConfig(path)
				if err != nil {
					t.Fatal(err)
				}
				if loaded.Host != tc.want {
					t.Fatalf("saved scalar = %q, want %q", loaded.Host, tc.want)
				}
			})
		}
	}
}

func TestWriteConfigPreservesDistinctStringMapKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "antigravity:\n  sensitive-words-replace:\n    0x1: |-\n      first\n      # scalar data\n    1: stable\n"
	if err := api.WriteConfig(path, []byte(body)); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	values := cfg.Antigravity.SensitiveWordsReplace
	if values["0x1"] != "first\n# scalar data" || values["1"] != "stable" {
		t.Fatalf("saved replacements: %q", values)
	}
}
