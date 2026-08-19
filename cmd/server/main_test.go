package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestShouldEnableExampleAPIKeySafeMode(t *testing.T) {
	cfgWithExampleKey := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"real-key", " your-api-key-1 "},
		},
	}
	cfgWithRealKey := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"real-key"},
		},
	}

	tests := []struct {
		name               string
		cfg                *config.Config
		commandMode        bool
		tuiMode            bool
		standalone         bool
		cloudConfigMissing bool
		homeMode           bool
		want               bool
	}{
		{
			name: "normal server with example key",
			cfg:  cfgWithExampleKey,
			want: true,
		},
		{
			name:       "standalone tui with example key",
			cfg:        cfgWithExampleKey,
			tuiMode:    true,
			standalone: true,
			want:       true,
		},
		{
			name:        "pure tui client is not blocked",
			cfg:         cfgWithExampleKey,
			tuiMode:     true,
			standalone:  false,
			commandMode: false,
			want:        false,
		},
		{
			name:        "one-shot command is not blocked",
			cfg:         cfgWithExampleKey,
			commandMode: true,
			want:        false,
		},
		{
			name:     "home mode is not blocked",
			cfg:      cfgWithExampleKey,
			homeMode: true,
			want:     false,
		},
		{
			name:               "cloud standby without config is not blocked",
			cfg:                cfgWithExampleKey,
			cloudConfigMissing: true,
			want:               false,
		},
		{
			name: "normal server with real key",
			cfg:  cfgWithRealKey,
			want: false,
		},
		{
			name: "nil config",
			cfg:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldEnableExampleAPIKeySafeMode(tt.cfg, tt.commandMode, tt.tuiMode, tt.standalone, tt.cloudConfigMissing, tt.homeMode)
			if got != tt.want {
				t.Fatalf("shouldEnableExampleAPIKeySafeMode() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestIsOneShotCommandMode(t *testing.T) {
	tests := []struct {
		name string
		opts commandModeOptions
		want bool
	}{
		{name: "none", want: false},
		{name: "vertex import", opts: commandModeOptions{vertexImport: "/tmp/key.json"}, want: true},
		{name: "plugin command line", opts: commandModeOptions{pluginCommandLine: true}, want: true},
		{name: "gemini login", opts: commandModeOptions{login: true}, want: true},
		{name: "antigravity login", opts: commandModeOptions{antigravityLogin: true}, want: true},
		{name: "github copilot login", opts: commandModeOptions{githubCopilotLogin: true}, want: true},
		{name: "codebuddy login", opts: commandModeOptions{codeBuddyLogin: true}, want: true},
		{name: "codex oauth login", opts: commandModeOptions{codexLogin: true}, want: true},
		{name: "codex device login", opts: commandModeOptions{codexDeviceLogin: true}, want: true},
		{name: "claude login", opts: commandModeOptions{claudeLogin: true}, want: true},
		{name: "kilo login", opts: commandModeOptions{kiloLogin: true}, want: true},
		{name: "iflow oauth login", opts: commandModeOptions{iflowLogin: true}, want: true},
		{name: "iflow cookie login", opts: commandModeOptions{iflowCookie: true}, want: true},
		{name: "gitlab oauth login", opts: commandModeOptions{gitlabLogin: true}, want: true},
		{name: "gitlab token login", opts: commandModeOptions{gitlabTokenLogin: true}, want: true},
		{name: "kimi login", opts: commandModeOptions{kimiLogin: true}, want: true},
		{name: "cursor login", opts: commandModeOptions{cursorLogin: true}, want: true},
		{name: "kiro google login", opts: commandModeOptions{kiroLogin: true}, want: true},
		{name: "kiro google alias login", opts: commandModeOptions{kiroGoogleLogin: true}, want: true},
		{name: "kiro aws device login", opts: commandModeOptions{kiroAWSLogin: true}, want: true},
		{name: "kiro aws authcode login", opts: commandModeOptions{kiroAWSAuthCode: true}, want: true},
		{name: "kiro import", opts: commandModeOptions{kiroImport: true}, want: true},
		{name: "kiro idc login", opts: commandModeOptions{kiroIDCLogin: true}, want: true},
		{name: "xai login", opts: commandModeOptions{xaiLogin: true}, want: true},
		{name: "qoder login", opts: commandModeOptions{qoderLogin: true}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isOneShotCommandMode(tt.opts)
			if got != tt.want {
				t.Fatalf("isOneShotCommandMode() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestModelCatalogUpdaterPlan(t *testing.T) {
	tests := []struct {
		name            string
		localModel      bool
		homeEnabled     bool
		wantModels      bool
		wantCodexClient bool
	}{
		{
			name:            "normal CPA refreshes both catalogs",
			localModel:      false,
			homeEnabled:     false,
			wantModels:      true,
			wantCodexClient: true,
		},
		{
			name:            "home mode keeps models.json local and refreshes codex templates",
			localModel:      false,
			homeEnabled:     true,
			wantModels:      false,
			wantCodexClient: true,
		},
		{
			name:            "local-model disables both remote catalogs",
			localModel:      true,
			homeEnabled:     false,
			wantModels:      false,
			wantCodexClient: false,
		},
		{
			name:            "local-model disables both remote catalogs even under home",
			localModel:      true,
			homeEnabled:     true,
			wantModels:      false,
			wantCodexClient: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModels, gotCodex := modelCatalogUpdaterPlan(tt.localModel, tt.homeEnabled)
			if gotModels != tt.wantModels || gotCodex != tt.wantCodexClient {
				t.Fatalf("modelCatalogUpdaterPlan(%v, %v) = (%v, %v), want (%v, %v)",
					tt.localModel, tt.homeEnabled, gotModels, gotCodex, tt.wantModels, tt.wantCodexClient)
			}
		})
	}
}
