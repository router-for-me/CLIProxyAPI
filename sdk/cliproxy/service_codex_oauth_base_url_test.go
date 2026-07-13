package cliproxy

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestValidateCodexOAuthBaseURLAuthConflicts(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		auth       *coreauth.Auth
		wantErr    bool
		wantDetail string
	}{
		{
			name: "unset preserves stock behavior",
			cfg:  &config.Config{},
			auth: &coreauth.Auth{Provider: "codex", Attributes: map[string]string{"base_url": "https://legacy.example.com"}},
		},
		{
			name:       "attribute base URL conflicts",
			cfg:        &config.Config{CodexOAuthBaseURL: "https://edge.example.com"},
			auth:       &coreauth.Auth{ID: "oauth-attr", Provider: "codex", Attributes: map[string]string{"base_url": "https://legacy.example.com"}},
			wantErr:    true,
			wantDetail: "oauth-attr",
		},
		{
			name:       "auth file metadata base URL conflicts",
			cfg:        &config.Config{CodexOAuthBaseURL: "https://edge.example.com"},
			auth:       &coreauth.Auth{ID: "oauth-file", FileName: "auths/codex.json", Provider: "codex", Metadata: map[string]any{"base_url": "https://legacy.example.com"}},
			wantErr:    true,
			wantDetail: "auths/codex.json",
		},
		{
			name: "matching codex API key entry is named",
			cfg: &config.Config{
				CodexOAuthBaseURL: "https://edge.example.com",
				CodexKey:          []config.CodexKey{{BaseURL: "https://legacy.example.com"}},
			},
			auth:       &coreauth.Auth{ID: "oauth-match", Provider: "codex", Metadata: map[string]any{"base_url": "https://legacy.example.com"}},
			wantErr:    true,
			wantDetail: "codex-api-key[0]",
		},
		{
			name: "API key auth keeps its own base URL",
			cfg:  &config.Config{CodexOAuthBaseURL: "https://edge.example.com"},
			auth: &coreauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "sk-test", "base_url": "https://api-key.example.com"}},
		},
		{
			name: "other provider is ignored",
			cfg:  &config.Config{CodexOAuthBaseURL: "https://edge.example.com"},
			auth: &coreauth.Auth{Provider: "claude", Attributes: map[string]string{"base_url": "https://legacy.example.com"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errConflict := validateCodexOAuthBaseURLAuthConflicts(tt.cfg, []*coreauth.Auth{tt.auth})
			if !tt.wantErr {
				if errConflict != nil {
					t.Fatalf("validation error = %v, want nil", errConflict)
				}
				return
			}
			if errConflict == nil {
				t.Fatal("validation error = nil, want conflict")
			}
			if !strings.Contains(errConflict.Error(), "codex-oauth-base-url") || !strings.Contains(errConflict.Error(), tt.wantDetail) {
				t.Fatalf("validation error = %q, want key and %q", errConflict, tt.wantDetail)
			}
		})
	}
}

func TestApplyConfigUpdateKeepsStartupCodexOAuthConfig(t *testing.T) {
	startupURL := "https://startup.example.com/codex"
	startupHeaders := map[string]string{"x-edgee-api-key": "startup-key"}
	service := &Service{
		cfg: &config.Config{
			CodexOAuthBaseURL: startupURL,
			CodexOAuthHeaders: map[string]string{"x-edgee-api-key": "startup-key"},
		},
		codexOAuthBaseURL: startupURL,
		codexOAuthHeaders: startupHeaders,
	}
	reloaded := &config.Config{
		CodexOAuthBaseURL: "https://reload.example.com/codex",
		CodexOAuthHeaders: map[string]string{"x-edgee-api-key": "reload-key"},
	}

	service.applyConfigUpdateWithAuthSynthesis(reloaded, false)

	if reloaded.CodexOAuthBaseURL != startupURL {
		t.Fatalf("reloaded CodexOAuthBaseURL = %q, want startup %q", reloaded.CodexOAuthBaseURL, startupURL)
	}
	if service.cfg.CodexOAuthBaseURL != startupURL {
		t.Fatalf("service CodexOAuthBaseURL = %q, want startup %q", service.cfg.CodexOAuthBaseURL, startupURL)
	}
	if got := reloaded.CodexOAuthHeaders["x-edgee-api-key"]; got != "startup-key" {
		t.Fatalf("reloaded CodexOAuthHeaders value = %q, want startup value", got)
	}
	if got := service.cfg.CodexOAuthHeaders["x-edgee-api-key"]; got != "startup-key" {
		t.Fatalf("service CodexOAuthHeaders value = %q, want startup value", got)
	}
	startupHeaders["x-edgee-api-key"] = "mutated-after-reload"
	if got := reloaded.CodexOAuthHeaders["x-edgee-api-key"]; got != "startup-key" {
		t.Fatalf("reloaded CodexOAuthHeaders aliases startup snapshot: got %q", got)
	}
}
