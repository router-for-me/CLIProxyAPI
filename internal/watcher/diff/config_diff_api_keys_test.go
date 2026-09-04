package diff

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestBuildConfigChangeDetailsAPIKeyNameOnlyChange(t *testing.T) {
	tests := []struct {
		name       string
		oldEntries []config.APIKeyEntry
		newEntries []config.APIKeyEntry
		wantChange bool
	}{
		{
			name:       "name added",
			oldEntries: []config.APIKeyEntry{{Key: "secret-key"}},
			newEntries: []config.APIKeyEntry{{Key: "secret-key", Name: "alice"}},
			wantChange: true,
		},
		{
			name:       "name changed",
			oldEntries: []config.APIKeyEntry{{Key: "secret-key", Name: "alice"}},
			newEntries: []config.APIKeyEntry{{Key: "secret-key", Name: "bob"}},
			wantChange: true,
		},
		{
			name:       "no change",
			oldEntries: []config.APIKeyEntry{{Key: "secret-key", Name: "alice"}},
			newEntries: []config.APIKeyEntry{{Key: " secret-key ", Name: " alice "}},
			wantChange: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldCfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{APIKeys: tt.oldEntries}}
			newCfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{APIKeys: tt.newEntries}}

			details := BuildConfigChangeDetails(oldCfg, newCfg)
			joined := strings.Join(details, "\n")
			hasChange := strings.Contains(joined, "api-keys: values updated (count unchanged, redacted)")
			if hasChange != tt.wantChange {
				t.Fatalf("api-keys change = %v, want %v; details=%v", hasChange, tt.wantChange, details)
			}
			if strings.Contains(joined, "secret-key") || strings.Contains(joined, "alice") || strings.Contains(joined, "bob") {
				t.Fatalf("change message leaked key or name: %v", details)
			}
		})
	}
}
