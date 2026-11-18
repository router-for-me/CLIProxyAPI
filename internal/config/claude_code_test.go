package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseConfigBytesClaudeCodeModelListCloaking(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{
			name: "defaults to enabled cloaking",
			yaml: "port: 8317\n",
			want: false,
		},
		{
			name: "disables model list cloaking",
			yaml: "claude-code:\n  disable-cloaking-model-list: true\n",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes([]byte(tt.yaml))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v", errParse)
			}
			if got := cfg.ClaudeCode.DisableCloakingModelList; got != tt.want {
				t.Fatalf("DisableCloakingModelList = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestParseConfigBytesClaudeModelLevelCooling(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{
			name: "defaults to false",
			yaml: "port: 8317\n",
			want: false,
		},
		{
			name: "enables model-level cooling",
			yaml: "claude:\n  model-level-cooling: true\n",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes([]byte(tt.yaml))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v", errParse)
			}
			if got := cfg.Claude.ModelLevelCooling; got != tt.want {
				t.Fatalf("cfg.Claude.ModelLevelCooling = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestParseConfigBytesClaudeSessionDatePinDays(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want int
	}{
		{name: "defaults to 7 days", yaml: "port: 8317\n", want: 7},
		{name: "legacy layout", yaml: "claude:\n  session-date-pin-days: 14\n", want: 14},
		{name: "v8 layout", yaml: "config-version: 8\nupstream:\n  claude:\n    session-date-pin-days: 7\n", want: 7},
		{name: "v8 explicit zero", yaml: "config-version: 8\nupstream:\n  claude:\n    session-date-pin-days: 0\n", want: 0},
		{name: "legacy explicit zero", yaml: "claude:\n  session-date-pin-days: 0\n", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes([]byte(tt.yaml))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v", errParse)
			}
			if got := cfg.Claude.SessionDatePinDays; got == nil || *got != tt.want {
				t.Fatalf("cfg.Claude.SessionDatePinDays = %v, want %d", got, tt.want)
			}
		})
	}
}

func TestSaveConfigPreserveComments_SessionDatePinDaysExplicitZeroWhenAbsent(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	zero := 0
	cfg := &Config{Claude: ClaudeConfig{SessionDatePinDays: &zero}}
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	if !strings.Contains(string(savedBytes), "session-date-pin-days: 0") {
		t.Fatalf("saved YAML missing explicit 'session-date-pin-days: 0':\n%s", savedBytes)
	}
	loaded, errLoad := LoadConfigOptional(configPath, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if loaded.Claude.SessionDatePinDays == nil || *loaded.Claude.SessionDatePinDays != 0 {
		t.Fatalf("loaded SessionDatePinDays = %v, want explicit 0", loaded.Claude.SessionDatePinDays)
	}
}

func TestSaveConfigPreserveComments_SessionDatePinDaysDefaultNotPersisted(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg, errLoad := LoadConfigOptional(configPath, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	if strings.Contains(string(savedBytes), "session-date-pin-days") {
		t.Fatalf("saved YAML must not contain the default session-date-pin-days:\n%s", savedBytes)
	}
}

func TestClaudeSessionDatePinRetentionClampsOverflow(t *testing.T) {
	days := 1 << 40
	cfg := ClaudeConfig{SessionDatePinDays: &days}
	if got := cfg.SessionDatePinRetention(); got <= 0 {
		t.Fatalf("SessionDatePinRetention() = %v, want positive clamped duration", got)
	}
	zero := 0
	if got := (ClaudeConfig{SessionDatePinDays: &zero}).SessionDatePinRetention(); got != 0 {
		t.Fatalf("explicit-zero retention = %v, want 0", got)
	}
}

func TestParseConfigBytesClaudeToolSearch(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{name: "defaults to false", yaml: "port: 8317\n", want: false},
		{name: "legacy layout", yaml: "claude:\n  tool-search: true\n", want: true},
		{name: "v8 layout", yaml: "config-version: 8\nupstream:\n  claude:\n    tool-search: true\n", want: true},
		{name: "v8 explicit false", yaml: "config-version: 8\nupstream:\n  claude:\n    tool-search: false\n", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes([]byte(tt.yaml))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v", errParse)
			}
			if got := cfg.Claude.ToolSearch; got != tt.want {
				t.Fatalf("cfg.Claude.ToolSearch = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestParseConfigBytesClaudeServerSideFallback(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{name: "defaults to false", yaml: "port: 8317\n", want: false},
		{name: "legacy layout", yaml: "claude:\n  server-side-fallback: true\n", want: true},
		{name: "v8 layout", yaml: "config-version: 8\nupstream:\n  claude:\n    server-side-fallback: true\n", want: true},
		{name: "v8 explicit false", yaml: "config-version: 8\nupstream:\n  claude:\n    server-side-fallback: false\n", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes([]byte(tt.yaml))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v", errParse)
			}
			if got := cfg.Claude.ServerSideFallback; got != tt.want {
				t.Fatalf("cfg.Claude.ServerSideFallback = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestParseAndSaveClaudeOAuthIncludeCreateAPIKey(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		wantEnabled bool
		wantNil     bool
	}{
		{name: "defaults to true when omitted", yaml: "port: 8317\n", wantEnabled: true, wantNil: true},
		{name: "v8 oauth providers explicit true", yaml: "config-version: 8\noauth:\n  providers:\n    claude:\n      oauth-include-create-api-key: true\n", wantEnabled: true},
		{name: "v8 oauth providers explicit false", yaml: "config-version: 8\noauth:\n  providers:\n    claude:\n      oauth-include-create-api-key: false\n", wantEnabled: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes([]byte(tt.yaml))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v", errParse)
			}
			if tt.wantNil && cfg.Claude.OAuthIncludeCreateAPIKey != nil {
				t.Fatalf("OAuthIncludeCreateAPIKey = %v, want nil", *cfg.Claude.OAuthIncludeCreateAPIKey)
			}
			if !tt.wantNil && cfg.Claude.OAuthIncludeCreateAPIKey == nil {
				t.Fatal("OAuthIncludeCreateAPIKey = nil, want non-nil")
			}
			if got := cfg.Claude.OAuthIncludeCreateAPIKeyEnabled(); got != tt.wantEnabled {
				t.Fatalf("OAuthIncludeCreateAPIKeyEnabled() = %t, want %t", got, tt.wantEnabled)
			}
			if cfg.ForAPIKey().Claude.OAuthIncludeCreateAPIKey != nil {
				t.Fatalf("ForAPIKey() inherited OAuthIncludeCreateAPIKey = %v", *cfg.ForAPIKey().Claude.OAuthIncludeCreateAPIKey)
			}
		})
	}

	raw := []byte("config-version: 8\noauth:\n  providers:\n    claude:\n      oauth-include-create-api-key: false\n")
	migrated, _, errMigrate := NormalizeConfigLayout(raw, true)
	if errMigrate != nil {
		t.Fatalf("NormalizeConfigLayout() error = %v", errMigrate)
	}
	if errValidate := ValidateV8Config(migrated); errValidate != nil {
		t.Fatalf("ValidateV8Config() error = %v", errValidate)
	}
	var doc yaml.Node
	if errUnmarshal := yaml.Unmarshal(migrated, &doc); errUnmarshal != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errUnmarshal)
	}
	if node := yamlPath(doc.Content[0], "oauth.providers.claude.oauth-include-create-api-key"); node == nil || node.Value != "false" {
		t.Fatalf("migrated YAML missing oauth.providers.claude.oauth-include-create-api-key=false:\n%s", migrated)
	}
	if yamlPath(doc.Content[0], "upstream.claude.oauth-include-create-api-key") != nil {
		t.Fatalf("oauth-include-create-api-key leaked into upstream.claude:\n%s", migrated)
	}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("config-version: 8\nserver:\n  port: 8317\n"), 0600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}
	cfgSave := &Config{
		Port: 8317,
		Claude: ClaudeConfig{
			OAuthIncludeCreateAPIKey: new(false),
		},
	}
	if errSave := SaveConfigPreserveComments(configPath, cfgSave, true); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}
	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	reloaded, errReload := LoadConfig(configPath)
	if errReload != nil {
		t.Fatalf("LoadConfig() error = %v", errReload)
	}
	if reloaded.Claude.OAuthIncludeCreateAPIKey == nil || *reloaded.Claude.OAuthIncludeCreateAPIKey {
		t.Fatalf("reloaded OAuthIncludeCreateAPIKey = %v, want false; saved:\n%s", reloaded.Claude.OAuthIncludeCreateAPIKey, savedBytes)
	}
	var savedDoc yaml.Node
	if errUnmarshal := yaml.Unmarshal(savedBytes, &savedDoc); errUnmarshal != nil {
		t.Fatalf("yaml.Unmarshal(savedBytes) error = %v", errUnmarshal)
	}
	if node := yamlPath(savedDoc.Content[0], "oauth.providers.claude.oauth-include-create-api-key"); node == nil || node.Value != "false" {
		t.Fatalf("saved YAML missing oauth.providers.claude.oauth-include-create-api-key=false:\n%s", savedBytes)
	}

	// Clearing OAuthIncludeCreateAPIKey back to nil must remove the persisted false override.
	reloaded.Claude.OAuthIncludeCreateAPIKey = nil
	if errSaveNil := SaveConfigPreserveComments(configPath, reloaded, true); errSaveNil != nil {
		t.Fatalf("SaveConfigPreserveComments(nil) error = %v", errSaveNil)
	}
	cleared, errClearReload := LoadConfig(configPath)
	if errClearReload != nil {
		t.Fatalf("LoadConfig() after clearing nil error = %v", errClearReload)
	}
	if cleared.Claude.OAuthIncludeCreateAPIKey != nil || !cleared.Claude.OAuthIncludeCreateAPIKeyEnabled() {
		t.Fatalf("cleared OAuthIncludeCreateAPIKey = %v, want nil (default true)", cleared.Claude.OAuthIncludeCreateAPIKey)
	}

	// Migrating legacy claude.oauth-include-create-api-key=false alongside an empty
	// oauth.providers.claude={} must preserve the setting under oauth.providers.claude.
	coexistRaw := []byte("claude:\n  oauth-include-create-api-key: false\noauth:\n  providers:\n    claude: {}\n")
	coexistMigrated, _, errCoexist := NormalizeConfigLayout(coexistRaw, true)
	if errCoexist != nil {
		t.Fatalf("NormalizeConfigLayout(coexist) error = %v", errCoexist)
	}
	if errValidate := ValidateV8Config(coexistMigrated); errValidate != nil {
		t.Fatalf("ValidateV8Config(coexist) error = %v\n%s", errValidate, coexistMigrated)
	}
	coexistCfg, errCoexistParse := ParseConfigBytes(coexistMigrated)
	if errCoexistParse != nil {
		t.Fatalf("ParseConfigBytes(coexist) error = %v", errCoexistParse)
	}
	if coexistCfg.Claude.OAuthIncludeCreateAPIKey == nil || *coexistCfg.Claude.OAuthIncludeCreateAPIKey {
		t.Fatalf("coexist OAuthIncludeCreateAPIKey = %v, want false\n%s", coexistCfg.Claude.OAuthIncludeCreateAPIKey, coexistMigrated)
	}
}
