package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestSanitizeAPIKeyMetadata(t *testing.T) {
	originalKeys := []string{" key-a ", "key-b", "key-c", "key-b"}
	cfg := &Config{
		SDKConfig: SDKConfig{
			APIKeys: append([]string(nil), originalKeys...),
			APIKeyMetadata: map[string]ClientAPIKeyMetadata{
				" key-a ": {ID: " shared-id ", Alias: " Team A "},
				"key-b":   {ID: "shared-id", Alias: " Team B ", Disabled: true},
				"key-c":   {Alias: " Team C "},
				"orphan":  {ID: "orphan-id", Alias: " Orphan "},
			},
		},
	}

	cfg.SanitizeAPIKeyMetadata()

	if !reflect.DeepEqual(cfg.APIKeys, originalKeys) {
		t.Fatalf("APIKeys = %#v, want unchanged %#v", cfg.APIKeys, originalKeys)
	}
	if len(cfg.APIKeyMetadata) != 3 {
		t.Fatalf("APIKeyMetadata length = %d, want 3", len(cfg.APIKeyMetadata))
	}
	if _, exists := cfg.APIKeyMetadata["orphan"]; exists {
		t.Fatal("orphan metadata was not removed")
	}
	if got := cfg.APIKeyMetadata["key-a"]; got.ID != "shared-id" || got.Alias != "Team A" || got.Disabled {
		t.Fatalf("key-a metadata = %+v, want normalized explicit metadata", got)
	}
	if got := cfg.APIKeyMetadata["key-b"]; got.ID != sdkaccess.FallbackClientKeyID("key-b") || got.Alias != "Team B" || !got.Disabled {
		t.Fatalf("key-b metadata = %+v, want duplicate ID fallback and disabled", got)
	}
	if got := cfg.APIKeyMetadata["key-c"]; got.ID != sdkaccess.FallbackClientKeyID("key-c") || got.Alias != "Team C" {
		t.Fatalf("key-c metadata = %+v, want generated ID and normalized alias", got)
	}
}

func TestSanitizeAPIKeyMetadataAvoidsExplicitFallbackCollision(t *testing.T) {
	cfg := &Config{SDKConfig: SDKConfig{
		APIKeys: []string{"key-a", "key-b"},
		APIKeyMetadata: map[string]ClientAPIKeyMetadata{
			"key-a": {},
			"key-b": {ID: sdkaccess.FallbackClientKeyID("key-a")},
		},
	}}

	cfg.SanitizeAPIKeyMetadata()

	if got, want := cfg.APIKeyMetadata["key-a"].ID, sdkaccess.FallbackClientKeyID("key-a"); got != want {
		t.Fatalf("key-a ID = %q, want %q", got, want)
	}
	if got, want := cfg.APIKeyMetadata["key-b"].ID, sdkaccess.FallbackClientKeyID("key-b"); got != want {
		t.Fatalf("key-b ID = %q, want %q", got, want)
	}
}

func TestSanitizeAPIKeyMetadataResolvesCollisionWithImplicitLegacyKey(t *testing.T) {
	keyA := "key-a"
	keyB := "key-b"
	fallbackB := sdkaccess.FallbackClientKeyID(keyB)
	tests := []struct {
		name string
		keys []string
	}{
		{name: "explicit first", keys: []string{keyA, keyB}},
		{name: "implicit first", keys: []string{keyB, keyA}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := &Config{SDKConfig: SDKConfig{
				APIKeys: test.keys,
				APIKeyMetadata: map[string]ClientAPIKeyMetadata{
					keyA: {ID: fallbackB},
				},
			}}

			cfg.SanitizeAPIKeyMetadata()

			idA := cfg.APIKeyMetadata[keyA].ID
			idB := sdkaccess.FallbackClientKeyID(keyB)
			if metadataB, exists := cfg.APIKeyMetadata[keyB]; exists && metadataB.ID != "" {
				idB = metadataB.ID
			}
			if idA == idB {
				t.Fatalf("resolved IDs collide: key-a=%q key-b=%q metadata=%#v", idA, idB, cfg.APIKeyMetadata)
			}
		})
	}
}

func TestConfigParsingSanitizesAPIKeyMetadata(t *testing.T) {
	const payload = `api-keys:
  - " key-a "
  - "key-b"
api-key-metadata:
  " key-a ":
    id: " tenant "
    alias: " Team A "
    created-at: "2026-08-12T03:04:05Z"
  key-b:
    id: "tenant"
    disabled: true
  orphan:
    id: "orphan"
`

	tests := []struct {
		name  string
		parse func(*testing.T) *Config
	}{
		{
			name: "bytes",
			parse: func(t *testing.T) *Config {
				cfg, err := ParseConfigBytes([]byte(payload))
				if err != nil {
					t.Fatalf("ParseConfigBytes() error = %v", err)
				}
				return cfg
			},
		},
		{
			name: "file",
			parse: func(t *testing.T) *Config {
				path := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
					t.Fatalf("write config: %v", err)
				}
				cfg, err := LoadConfigOptional(path, false)
				if err != nil {
					t.Fatalf("LoadConfigOptional() error = %v", err)
				}
				return cfg
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := test.parse(t)
			if got := cfg.APIKeyMetadata["key-a"]; got.ID != "tenant" || got.Alias != "Team A" || got.CreatedAt != "2026-08-12T03:04:05Z" {
				t.Fatalf("key-a metadata = %+v, want normalized values", got)
			}
			if got := cfg.APIKeyMetadata["key-b"]; got.ID != sdkaccess.FallbackClientKeyID("key-b") || !got.Disabled {
				t.Fatalf("key-b metadata = %+v, want duplicate ID fallback and disabled", got)
			}
			if _, exists := cfg.APIKeyMetadata["orphan"]; exists {
				t.Fatal("orphan metadata was not removed")
			}
			if got := cfg.APIKeys[0]; got != " key-a " {
				t.Fatalf("APIKeys[0] = %q, want legacy value unchanged", got)
			}
		})
	}
}

func TestSaveConfigPreserveCommentsPrunesRemovedAPIKeyMetadataFields(t *testing.T) {
	const payload = `# keep this comment
api-keys:
  - key-one
  - key-two
api-key-metadata:
  key-one:
    id: tenant-one
    alias: Old alias
    disabled: true
  key-two:
    id: tenant-two
    alias: Removed profile
    disabled: true
`

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte(payload), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	cfg, errLoad := LoadConfigOptional(configPath, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	cfg.APIKeyMetadata["key-one"] = ClientAPIKeyMetadata{Alias: "New alias"}
	delete(cfg.APIKeyMetadata, "key-two")

	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}
	saved, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("read saved config: %v", errRead)
	}
	savedText := string(saved)
	for _, staleValue := range []string{"tenant-one", "Old alias", "disabled: true", "tenant-two", "Removed profile"} {
		if strings.Contains(savedText, staleValue) {
			t.Fatalf("saved config retained stale API key metadata %q:\n%s", staleValue, savedText)
		}
	}
	if !strings.Contains(savedText, "# keep this comment") || !strings.Contains(savedText, "alias: New alias") {
		t.Fatalf("saved config lost the comment or current alias:\n%s", savedText)
	}

	reloaded, errReload := LoadConfigOptional(configPath, false)
	if errReload != nil {
		t.Fatalf("reload config: %v", errReload)
	}
	keyOne := reloaded.APIKeyMetadata["key-one"]
	if keyOne.Disabled || keyOne.Alias != "New alias" || keyOne.ID == "tenant-one" {
		t.Fatalf("key-one metadata after reload = %#v, want enabled current metadata", keyOne)
	}
	if _, exists := reloaded.APIKeyMetadata["key-two"]; exists {
		t.Fatal("deleted key-two metadata reappeared after reload")
	}
}

func TestSanitizeAPIKeyMetadataRemovesCredentialMaterialAndControls(t *testing.T) {
	rawKey := "sk-sensitive-value"
	otherRawKey := "sk-other-sensitive-value"
	cfg := &Config{SDKConfig: SDKConfig{
		APIKeys: []string{rawKey, otherRawKey, "key-two"},
		APIKeyMetadata: map[string]ClientAPIKeyMetadata{
			rawKey:    {ID: otherRawKey, Alias: "Production " + otherRawKey},
			"key-two": {ID: "valid-id", Alias: "Team\x1b[31m\nTwo" + strings.Repeat("界", 140)},
		},
	}}

	cfg.SanitizeAPIKeyMetadata()

	if got := cfg.APIKeyMetadata[rawKey]; got.ID != sdkaccess.FallbackClientKeyID(rawKey) || got.Alias != "" {
		t.Fatalf("sensitive metadata = %#v, want fallback ID and empty alias", got)
	}
	second := cfg.APIKeyMetadata["key-two"]
	if strings.ContainsAny(second.Alias, "\x1b\r\n") {
		t.Fatalf("sanitized alias retained controls: %q", second.Alias)
	}
	if got := len([]rune(second.Alias)); got != maxConfiguredClientAPIKeyAliasLength {
		t.Fatalf("sanitized alias rune length = %d, want %d", got, maxConfiguredClientAPIKeyAliasLength)
	}
}
