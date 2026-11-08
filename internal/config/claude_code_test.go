package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
