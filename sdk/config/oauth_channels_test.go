package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestConfigRejectsAmbiguousOAuthChannels(t *testing.T) {
	for _, field := range []string{"model-alias", "excluded-models", "settings", "request-scoped-errors"} {
		for _, layout := range []string{"legacy", "v8", "merged v8"} {
			t.Run(field+"/"+layout, func(t *testing.T) {
				// Even empty entries must not hide a second spelling of a channel.
				raw := oauthChannelDocument(layout, field, `{"codex": [], " CODEX ": [], "Claude": [], "claude": []}`)
				// Rejection must precede the load's secret hashing and layout rewrites.
				raw += "remote-management: {secret-key: channel-validation-fixture}\n"
				want := fmt.Sprintf("oauth-%s: channel keys %q and %q normalize to %q", field, "Claude", "claude", "claude")
				for _, loader := range []string{"bytes", "file", "optional file"} {
					t.Run(loader, func(t *testing.T) {
						path := filepath.Join(t.TempDir(), "config.yaml")
						if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
							t.Fatal(err)
						}
						for range 4 {
							var cfg *config.Config
							var err error
							if loader == "bytes" {
								cfg, err = config.ParseConfigBytes([]byte(raw))
							} else if loader == "file" {
								cfg, err = config.LoadConfig(path)
							} else {
								cfg, err = config.LoadConfigOptional(path, true)
							}
							if err == nil || err.Error() != want || cfg != nil {
								t.Fatalf("load returned config=%t, error=%v; want no config and %q", cfg != nil, err, want)
							}
						}
						after, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						if string(after) != raw {
							t.Fatal("rejected load changed config bytes")
						}
					})
				}
			})
		}
	}
}

func oauthChannelDocument(layout, field, entries string) string {
	if layout == "legacy" {
		return "oauth-" + field + ": " + entries + "\n"
	}
	if layout == "merged v8" {
		return "defaults: &channels " + entries + "\nconfig-version: 8\noauth:\n  " + field + ": {<<: *channels}\n"
	}
	return "config-version: 8\noauth:\n  " + field + ": " + strings.ReplaceAll(entries, "\n", "\n  ") + "\n"
}

func TestConfigAcceptsUnambiguousOAuthChannels(t *testing.T) {
	fields := `model-alias:
  "": []
  " ": null
  " CODEX ": [{name: " upstream ", alias: " public "}]
  " Plugin-Provider ": [{name: plugin-model, alias: plugin-public}]
excluded-models:
  " CodeX ": [" PRIVATE-* ", "private-*"]
settings:
  " codex ": [{name: upstream, max-context-length: 8192}]
request-scoped-errors:
  "CODEX": [{status: 400, match: [" invalid input "], action: " cooldown "}]
`
	for _, layout := range []string{"legacy", "v8", "merged v8", "v8 precedence"} {
		t.Run(layout, func(t *testing.T) {
			var raw string
			if layout == "legacy" {
				for _, line := range strings.Split(fields, "\n") {
					if line != "" && line[0] != ' ' {
						line = "oauth-" + line
					}
					raw += line + "\n"
				}
			} else {
				raw = "config-version: 8\noauth:\n  " + strings.ReplaceAll(fields, "\n", "\n  ") + "\n"
				if layout == "merged v8" {
					raw = "defaults: &defaults\n  " + strings.ReplaceAll(fields, "\n", "\n  ") + "\noauth: {<<: *defaults}\n"
				} else if layout == "v8 precedence" {
					raw += "oauth-model-alias: {codex: [{name: ignored, alias: old}]}\n"
				}
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			parsed, err := config.ParseConfigBytes([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, cfg := range []*config.Config{parsed, loaded} {
				wantAliases := map[string][]config.OAuthModelAlias{
					"codex":           {{Name: "upstream", Alias: "public"}},
					"plugin-provider": {{Name: "plugin-model", Alias: "plugin-public"}},
				}
				if !reflect.DeepEqual(cfg.OAuthModelAlias, wantAliases) {
					t.Fatal("single-spelling channels did not retain their normalized aliases")
				}
				if !reflect.DeepEqual(cfg.OAuthExcludedModels, map[string][]string{"codex": {"private-*"}}) {
					t.Fatal("model exclusions were not normalized")
				}
				if !reflect.DeepEqual(cfg.OAuthSettings, map[string][]config.OAuthModelSetting{"codex": {{Name: "upstream", MaxContextLength: 8192}}}) {
					t.Fatal("model settings were not preserved")
				}
				rules := cfg.OAuthRequestScopedErrors["codex"]
				if len(cfg.OAuthRequestScopedErrors) != 1 || len(rules) != 1 || rules[0].Status != 400 || rules[0].Action != "cooldown" || !reflect.DeepEqual(rules[0].Match, []string{"invalid input"}) {
					t.Fatal("request-scoped error rules were not normalized")
				}
			}
		})
	}
}
