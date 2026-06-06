package cliproxy

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestHasNativeOpenAICompatExecutorConfig(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{Name: "native-provider", BaseURL: "https://native.example.com/v1"},
			},
		},
	}

	tests := []struct {
		name        string
		auth        *coreauth.Auth
		providerKey string
		want        bool
	}{
		{
			name:        "config provider",
			auth:        &coreauth.Auth{Provider: "native-provider"},
			providerKey: "native-provider",
			want:        true,
		},
		{
			name:        "inline base url",
			auth:        &coreauth.Auth{Provider: "plugin-provider", Attributes: map[string]string{"base_url": "https://compat.example.com/v1"}},
			providerKey: "plugin-provider",
			want:        true,
		},
		{
			name:        "compat metadata",
			auth:        &coreauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{"compat_name": "compat"}},
			providerKey: "compat",
			want:        true,
		},
		{
			name:        "plain plugin auth",
			auth:        &coreauth.Auth{Provider: "plugin-provider", Attributes: map[string]string{"api_key": "test"}},
			providerKey: "plugin-provider",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := service.hasNativeOpenAICompatExecutorConfig(tt.auth, tt.providerKey)
			if got != tt.want {
				t.Fatalf("hasNativeOpenAICompatExecutorConfig() = %v, want %v", got, tt.want)
			}
		})
	}
}
