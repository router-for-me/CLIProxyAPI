package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexintegration"
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

func TestSelectCodexCommand(t *testing.T) {
	action, selected, err := selectCodexCommand(false, false, true, false)
	if err != nil || !selected || action != codexintegration.CommandDoctor {
		t.Fatalf("selectCodexCommand() = %q, %t, %v", action, selected, err)
	}
	if _, _, err = selectCodexCommand(true, true, false, false); err == nil {
		t.Fatal("selectCodexCommand() accepted multiple commands")
	}
	action, selected, err = selectCodexCommand(false, false, false, false)
	if err != nil || selected || action != "" {
		t.Fatalf("empty selectCodexCommand() = %q, %t, %v", action, selected, err)
	}
}

func TestValidateCodexCommandFlags(t *testing.T) {
	if err := validateCodexCommandFlags(true, true, codexintegration.CommandOptions{Action: codexintegration.CommandSetup}); err == nil {
		t.Fatal("validateCodexCommandFlags() accepted another one-shot command")
	}
	if err := validateCodexCommandFlags(false, false, codexintegration.CommandOptions{JSON: true}); err == nil {
		t.Fatal("validateCodexCommandFlags() accepted -json without a command")
	}
	if err := validateCodexCommandFlags(true, false, codexintegration.CommandOptions{Action: codexintegration.CommandSetup}); err != nil {
		t.Fatalf("validateCodexCommandFlags() error = %v", err)
	}
}

func TestIsCodexJSONInvocation(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{args: []string{"-codex-doctor", "-json"}, want: true},
		{args: []string{"--json=true", "--codex-setup=true"}, want: true},
		{args: []string{"-codex-doctor", "-json=false"}, want: false},
		{args: []string{"-json"}, want: false},
		{args: []string{"-codex-doctor"}, want: false},
	}
	for _, test := range tests {
		if got := isCodexJSONInvocation(test.args); got != test.want {
			t.Fatalf("isCodexJSONInvocation(%v) = %t, want %t", test.args, got, test.want)
		}
	}
}
