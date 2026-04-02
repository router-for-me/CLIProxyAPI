package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestResolveAntigravityWebUICallbackPort(t *testing.T) {
	tests := []struct {
		name    string
		apiPort int
		cfg     *config.Config
		want    int
	}{
		{
			name:    "defaults standard instance",
			apiPort: 8317,
			cfg:     &config.Config{},
			want:    defaultAntigravityStandardPort,
		},
		{
			name:    "defaults premium instance",
			apiPort: 8318,
			cfg:     &config.Config{},
			want:    defaultAntigravityPremiumPort,
		},
		{
			name:    "custom standard port from config",
			apiPort: 8317,
			cfg: &config.Config{RemoteManagement: config.RemoteManagement{
				AntigravityWebUICallbackPort: 60121,
				AntigravityWebUICallbackPortPremium: 60122,
			}},
			want: 60121,
		},
		{
			name:    "custom premium port from config",
			apiPort: 8318,
			cfg: &config.Config{RemoteManagement: config.RemoteManagement{
				AntigravityWebUICallbackPort: 60121,
				AntigravityWebUICallbackPortPremium: 60122,
			}},
			want: 60122,
		},
		{
			name:    "fallback to standard default when config is nil",
			apiPort: 8317,
			cfg:     nil,
			want:    defaultAntigravityStandardPort,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{cfg: tt.cfg}
			got := h.resolveAntigravityWebUICallbackPort(tt.apiPort)
			if got != tt.want {
				t.Fatalf("resolveAntigravityWebUICallbackPort(%d) = %d, want %d", tt.apiPort, got, tt.want)
			}
		})
	}
}
