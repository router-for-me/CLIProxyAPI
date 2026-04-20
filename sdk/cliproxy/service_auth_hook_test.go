package cliproxy

import (
	"context"
	"os"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestServiceAuthHookSyncsModelRegistryOnAuthUpdate(t *testing.T) {
	t.Parallel()

	svc := &Service{
		cfg: &config.Config{
			AuthDir: t.TempDir(),
		},
	}
	if err := svc.ensureDefaults(); err != nil {
		t.Fatalf("ensureDefaults() error = %v", err)
	}

	auth := &coreauth.Auth{
		ID:       "codex-test-plus.json",
		Provider: "codex",
		Label:    "codex-test",
		Attributes: map[string]string{
			"path":      "codex-test-plus.json",
			"plan_type": "plus",
		},
		Metadata: map[string]any{
			"type": "codex",
		},
		Status: coreauth.StatusActive,
	}

	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	if _, err := svc.coreManager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if got := reg.GetModelsForClient(auth.ID); len(got) == 0 {
		t.Fatalf("expected registered models for enabled auth, got none")
	}

	auth.Disabled = true
	auth.Status = coreauth.StatusDisabled
	if _, err := svc.coreManager.Update(context.Background(), auth); err != nil {
		t.Fatalf("Update(disable) error = %v", err)
	}
	if got := reg.GetModelsForClient(auth.ID); len(got) != 0 {
		t.Fatalf("expected no registered models for disabled auth, got %d", len(got))
	}

	auth.Disabled = false
	auth.Status = coreauth.StatusActive
	if _, err := svc.coreManager.Update(context.Background(), auth); err != nil {
		t.Fatalf("Update(enable) error = %v", err)
	}
	if got := reg.GetModelsForClient(auth.ID); len(got) == 0 {
		t.Fatalf("expected registered models after re-enable, got none")
	}
}

func TestServiceSyncLoadedAuthModelsRegistersPersistedAuths(t *testing.T) {
	t.Parallel()

	authDir := t.TempDir()
	raw := []byte(`{"type":"codex","disabled":false,"id_token":"header.payload.sig"}`)
	if err := os.WriteFile(authDir+"/codex-startup-plus.json", raw, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tokenStore := sdkAuth.GetTokenStore()
	dirSetter, ok := tokenStore.(interface{ SetBaseDir(string) })
	if !ok {
		t.Fatal("token store does not support SetBaseDir")
	}
	dirSetter.SetBaseDir(authDir)

	svc := &Service{
		cfg: &config.Config{
			AuthDir: authDir,
		},
	}
	if err := svc.ensureDefaults(); err != nil {
		t.Fatalf("ensureDefaults() error = %v", err)
	}
	if err := svc.coreManager.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	auth, ok := svc.coreManager.GetByID("codex-startup-plus.json")
	if !ok || auth == nil {
		t.Fatal("expected persisted auth to be loaded")
	}
	auth.Attributes["plan_type"] = "plus"

	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	svc.syncLoadedAuthModels()

	if got := reg.GetModelsForClient(auth.ID); len(got) == 0 {
		t.Fatalf("expected registered models for loaded auth, got none")
	}
}

func TestRegisterModelsForAuth_UsesProviderNativeTypeForConfiguredModels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		auth        *coreauth.Auth
		cfg         *config.Config
		wantModelID string
		wantType    string
		wantOwnedBy string
	}{
		{
			name: "gemini",
			auth: &coreauth.Auth{
				ID:       "gemini-config-auth",
				Provider: "gemini",
				Attributes: map[string]string{
					"api_key":  "g-test",
					"base_url": "https://gemini.example.com",
				},
				Status: coreauth.StatusActive,
			},
			cfg: &config.Config{
				GeminiKey: []config.GeminiKey{
					{
						APIKey:  "g-test",
						BaseURL: "https://gemini.example.com",
						Models: []internalconfig.GeminiModel{
							{Name: "gemini-2.5-pro", Alias: "gemini-latest"},
						},
					},
				},
			},
			wantModelID: "gemini-latest",
			wantType:    "gemini",
			wantOwnedBy: "google",
		},
		{
			name: "claude",
			auth: &coreauth.Auth{
				ID:       "claude-config-auth",
				Provider: "claude",
				Attributes: map[string]string{
					"api_key":  "c-test",
					"base_url": "https://claude.example.com",
				},
				Status: coreauth.StatusActive,
			},
			cfg: &config.Config{
				ClaudeKey: []config.ClaudeKey{
					{
						APIKey:  "c-test",
						BaseURL: "https://claude.example.com",
						Models: []internalconfig.ClaudeModel{
							{Name: "claude-sonnet-4-5", Alias: "claude-latest"},
						},
					},
				},
			},
			wantModelID: "claude-latest",
			wantType:    "claude",
			wantOwnedBy: "anthropic",
		},
		{
			name: "codex",
			auth: &coreauth.Auth{
				ID:       "codex-config-auth",
				Provider: "codex",
				Attributes: map[string]string{
					"api_key":  "x-test",
					"base_url": "https://codex.example.com",
				},
				Status: coreauth.StatusActive,
			},
			cfg: &config.Config{
				CodexKey: []config.CodexKey{
					{
						APIKey:  "x-test",
						BaseURL: "https://codex.example.com",
						Models: []internalconfig.CodexModel{
							{Name: "gpt-5-codex", Alias: "codex-latest"},
						},
					},
				},
			},
			wantModelID: "codex-latest",
			wantType:    "codex",
			wantOwnedBy: "openai",
		},
		{
			name: "openai-compatibility",
			auth: &coreauth.Auth{
				ID:       "compat-config-auth",
				Provider: "demo",
				Label:    "demo",
				Attributes: map[string]string{
					"api_key":      "o-test",
					"base_url":     "https://compat.example.com",
					"compat_name":  "demo",
					"provider_key": "demo",
				},
				Status: coreauth.StatusActive,
			},
			cfg: &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{
					{
						Name:    "demo",
						BaseURL: "https://compat.example.com",
						APIKeyEntries: []config.OpenAICompatibilityAPIKey{
							{APIKey: "o-test"},
						},
						Models: []config.OpenAICompatibilityModel{
							{Name: "gpt-4.1", Alias: "demo-latest"},
						},
					},
				},
			},
			wantModelID: "demo-latest",
			wantType:    "openai-compatibility",
			wantOwnedBy: "demo",
		},
	}

	reg := registry.GetGlobalRegistry()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := &Service{
				cfg: tt.cfg,
			}
			svc.cfg.AuthDir = t.TempDir()
			if err := svc.ensureDefaults(); err != nil {
				t.Fatalf("ensureDefaults() error = %v", err)
			}

			reg.UnregisterClient(tt.auth.ID)
			t.Cleanup(func() {
				reg.UnregisterClient(tt.auth.ID)
			})

			svc.registerModelsForAuth(tt.auth)

			got := reg.GetModelsForClient(tt.auth.ID)
			if len(got) != 1 {
				t.Fatalf("expected 1 registered model, got %d", len(got))
			}
			if got[0].ID != tt.wantModelID {
				t.Fatalf("expected model id %q, got %q", tt.wantModelID, got[0].ID)
			}
			if got[0].Type != tt.wantType {
				t.Fatalf("expected model type %q, got %q", tt.wantType, got[0].Type)
			}
			if got[0].OwnedBy != tt.wantOwnedBy {
				t.Fatalf("expected owned_by %q, got %q", tt.wantOwnedBy, got[0].OwnedBy)
			}
		})
	}
}
