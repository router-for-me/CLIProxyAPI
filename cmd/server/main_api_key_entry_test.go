package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/safemode"
)

// A named entry must still be checked against the template key list, so a name
// cannot hide an example API key from safe mode.
func TestExampleAPIKeyDetectionWithNamedEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []config.APIKeyEntry
		want    bool
	}{
		{
			name:    "named template key detected",
			entries: []config.APIKeyEntry{{Key: "your-api-key-1", Name: "alice"}},
			want:    true,
		},
		{
			name:    "mixed entries detect template key",
			entries: []config.APIKeyEntry{{Key: "real-key"}, {Key: " your-api-key-2 ", Name: "bob"}},
			want:    true,
		},
		{
			name:    "named real keys are safe",
			entries: []config.APIKeyEntry{{Key: "real-key", Name: "alice"}, {Key: "another-key"}},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: tt.entries}}
			if got := safemode.HasExampleAPIKeys(cfg.APIKeyValues()); got != tt.want {
				t.Fatalf("HasExampleAPIKeys() = %v, want %v", got, tt.want)
			}
			if got := shouldEnableExampleAPIKeySafeMode(cfg, false, false, false, false, false); got != tt.want {
				t.Fatalf("shouldEnableExampleAPIKeySafeMode() = %v, want %v", got, tt.want)
			}
		})
	}
}
