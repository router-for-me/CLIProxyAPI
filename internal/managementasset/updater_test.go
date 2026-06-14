package managementasset

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestAutoUpdateSkipReason(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		wantReason string
		wantSkip   bool
	}{
		{
			name:       "nil config",
			cfg:        nil,
			wantReason: "config not yet available",
			wantSkip:   true,
		},
		{
			name: "cluster mode",
			cfg: &config.Config{
				Home: config.HomeConfig{Enabled: true},
			},
			wantReason: "cluster mode enabled",
			wantSkip:   true,
		},
		{
			name: "control panel disabled",
			cfg: &config.Config{
				RemoteManagement: config.RemoteManagement{DisableControlPanel: true},
			},
			wantReason: "control panel disabled",
			wantSkip:   true,
		},
		{
			name: "auto update disabled",
			cfg: &config.Config{
				RemoteManagement: config.RemoteManagement{DisableAutoUpdatePanel: true},
			},
			wantReason: "disable-auto-update-panel is enabled",
			wantSkip:   true,
		},
		{
			name:       "enabled",
			cfg:        &config.Config{},
			wantReason: "",
			wantSkip:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotReason, gotSkip := autoUpdateSkipReason(tt.cfg)
			if gotReason != tt.wantReason || gotSkip != tt.wantSkip {
				t.Fatalf("autoUpdateSkipReason() = (%q, %t), want (%q, %t)", gotReason, gotSkip, tt.wantReason, tt.wantSkip)
			}
		})
	}
}

func TestNewHTTPClient_HasExpectedTimeout(t *testing.T) {
	assetHTTPClientMu.Lock()
	assetHTTPClient = nil
	assetHTTPClientProxyURL = ""
	assetHTTPClientMu.Unlock()

	client := newHTTPClient("")
	if client == nil {
		t.Fatal("newHTTPClient returned nil")
	}

	want := 15 * time.Second
	if client.Timeout != want {
		t.Errorf("client.Timeout = %v, want %v", client.Timeout, want)
	}
}

func TestNewHTTPClient_SameProxyDoesNotReplace(t *testing.T) {
	assetHTTPClientMu.Lock()
	assetHTTPClient = nil
	assetHTTPClientProxyURL = ""
	assetHTTPClientMu.Unlock()

	first := newHTTPClient("")
	if first == nil {
		t.Fatal("newHTTPClient returned nil on first call")
	}

	assetHTTPClientMu.Lock()
	assetHTTPClientProxyURL = ""
	assetHTTPClientMu.Unlock()

	second := newHTTPClient("")
	if second == nil {
		t.Fatal("newHTTPClient returned nil on second call")
	}
	if first != second {
		t.Errorf("newHTTPClient returned different pointers: first=%p second=%p; expected reusable client", first, second)
	}
}

func TestNewHTTPClient_DifferentProxyCreatesNew(t *testing.T) {
	assetHTTPClientMu.Lock()
	assetHTTPClient = nil
	assetHTTPClientProxyURL = ""
	assetHTTPClientMu.Unlock()

	first := newHTTPClient("")
	if first == nil {
		t.Fatal("newHTTPClient returned nil on first call")
	}

	second := newHTTPClient("http://proxy:3128")
	if second == nil {
		t.Fatal("newHTTPClient returned nil on second call")
	}
	if first == second {
		t.Errorf("newHTTPClient returned same pointer for different proxy URLs: %p", first)
	}
}
