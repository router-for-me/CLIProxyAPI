 package copilot
 
 import (
 	"testing"
 	"time"
 
 	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
 )
 
 func TestResolveAccountType(t *testing.T) {
 	tests := []struct {
 		name string
 		auth *coreauth.Auth
 		want AccountType
 	}{
 		{
 			name: "nil auth",
 			auth: nil,
 			want: AccountTypeIndividual,
 		},
 		{
 			name: "empty auth",
 			auth: &coreauth.Auth{},
 			want: AccountTypeIndividual,
 		},
 		{
 			name: "from attributes - individual",
 			auth: &coreauth.Auth{
 				Attributes: map[string]string{"account_type": "individual"},
 			},
 			want: AccountTypeIndividual,
 		},
 		{
 			name: "from attributes - business",
 			auth: &coreauth.Auth{
 				Attributes: map[string]string{"account_type": "business"},
 			},
 			want: AccountTypeBusiness,
 		},
 		{
 			name: "from attributes - enterprise",
 			auth: &coreauth.Auth{
 				Attributes: map[string]string{"account_type": "enterprise"},
 			},
 			want: AccountTypeEnterprise,
 		},
 		{
 			name: "from storage when attributes empty",
 			auth: &coreauth.Auth{
 				Storage: &CopilotTokenStorage{AccountType: "business"},
 			},
 			want: AccountTypeBusiness,
 		},
 		{
 			name: "attributes take precedence over storage",
 			auth: &coreauth.Auth{
 				Attributes: map[string]string{"account_type": "enterprise"},
 				Storage:    &CopilotTokenStorage{AccountType: "business"},
 			},
 			want: AccountTypeEnterprise,
 		},
 	}
 
 	for _, tt := range tests {
 		t.Run(tt.name, func(t *testing.T) {
 			got := ResolveAccountType(tt.auth)
 			if got != tt.want {
 				t.Errorf("ResolveAccountType() = %v, want %v", got, tt.want)
 			}
 		})
 	}
 }
 
 func TestResolveGitHubToken(t *testing.T) {
 	tests := []struct {
 		name string
 		auth *coreauth.Auth
 		want string
 	}{
 		{
 			name: "nil auth",
 			auth: nil,
 			want: "",
 		},
 		{
 			name: "empty auth",
 			auth: &coreauth.Auth{},
 			want: "",
 		},
 		{
 			name: "from metadata",
 			auth: &coreauth.Auth{
 				Metadata: map[string]any{"github_token": "ghp_test123"},
 			},
 			want: "ghp_test123",
 		},
 		{
 			name: "from storage when metadata empty",
 			auth: &coreauth.Auth{
 				Storage: &CopilotTokenStorage{GitHubToken: "ghp_storage456"},
 			},
 			want: "ghp_storage456",
 		},
 		{
 			name: "metadata takes precedence over storage",
 			auth: &coreauth.Auth{
 				Metadata: map[string]any{"github_token": "ghp_metadata"},
 				Storage:  &CopilotTokenStorage{GitHubToken: "ghp_storage"},
 			},
 			want: "ghp_metadata",
 		},
 	}
 
 	for _, tt := range tests {
 		t.Run(tt.name, func(t *testing.T) {
 			got := ResolveGitHubToken(tt.auth)
 			if got != tt.want {
 				t.Errorf("ResolveGitHubToken() = %q, want %q", got, tt.want)
 			}
 		})
 	}
 }
 
 func TestResolveCopilotToken(t *testing.T) {
 	now := time.Now()
 	expiryStr := now.Add(time.Hour).Format(time.RFC3339)
 
 	tests := []struct {
 		name      string
 		auth      *coreauth.Auth
 		wantToken string
 		wantOK    bool
 	}{
 		{
 			name:      "nil auth",
 			auth:      nil,
 			wantToken: "",
 			wantOK:    false,
 		},
 		{
 			name:      "nil metadata",
 			auth:      &coreauth.Auth{},
 			wantToken: "",
 			wantOK:    false,
 		},
 		{
 			name: "valid token and expiry",
 			auth: &coreauth.Auth{
 				Metadata: map[string]any{
 					"copilot_token":        "test_token",
 					"copilot_token_expiry": expiryStr,
 				},
 			},
 			wantToken: "test_token",
 			wantOK:    true,
 		},
 		{
 			name: "missing token",
 			auth: &coreauth.Auth{
 				Metadata: map[string]any{
 					"copilot_token_expiry": expiryStr,
 				},
 			},
 			wantToken: "",
 			wantOK:    false,
 		},
 		{
 			name: "missing expiry",
 			auth: &coreauth.Auth{
 				Metadata: map[string]any{
 					"copilot_token": "test_token",
 				},
 			},
 			wantToken: "",
 			wantOK:    false,
 		},
 		{
 			name: "invalid expiry format",
 			auth: &coreauth.Auth{
 				Metadata: map[string]any{
 					"copilot_token":        "test_token",
 					"copilot_token_expiry": "invalid",
 				},
 			},
 			wantToken: "",
 			wantOK:    false,
 		},
 	}
 
 	for _, tt := range tests {
 		t.Run(tt.name, func(t *testing.T) {
 			token, _, ok := ResolveCopilotToken(tt.auth)
 			if token != tt.wantToken {
 				t.Errorf("ResolveCopilotToken() token = %q, want %q", token, tt.wantToken)
 			}
 			if ok != tt.wantOK {
 				t.Errorf("ResolveCopilotToken() ok = %v, want %v", ok, tt.wantOK)
 			}
 		})
 	}
 }
 
 func TestEnsureMetadataHydrated(t *testing.T) {
 	t.Run("nil auth", func(t *testing.T) {
 		// Should not panic
 		EnsureMetadataHydrated(nil)
 	})
 
 	t.Run("creates metadata if nil", func(t *testing.T) {
 		auth := &coreauth.Auth{
 			Storage: &CopilotTokenStorage{GitHubToken: "ghp_test"},
 		}
 		EnsureMetadataHydrated(auth)
 		if auth.Metadata == nil {
 			t.Error("EnsureMetadataHydrated() did not create metadata map")
 		}
 		if auth.Metadata["github_token"] != "ghp_test" {
 			t.Errorf("EnsureMetadataHydrated() github_token = %v, want ghp_test", auth.Metadata["github_token"])
 		}
 	})
 
 	t.Run("does not overwrite existing token", func(t *testing.T) {
 		auth := &coreauth.Auth{
 			Metadata: map[string]any{"github_token": "existing"},
 			Storage:  &CopilotTokenStorage{GitHubToken: "from_storage"},
 		}
 		EnsureMetadataHydrated(auth)
 		if auth.Metadata["github_token"] != "existing" {
 			t.Errorf("EnsureMetadataHydrated() overwrote existing token")
 		}
 	})
 }
 
 func TestApplyTokenRefresh(t *testing.T) {
 	now := time.Now()
 	tokenResp := &CopilotTokenResponse{
 		Token:     "new_token",
 		ExpiresAt: now.Add(time.Hour).Unix(),
 		RefreshIn: 3600,
 	}
 
 	t.Run("nil auth", func(t *testing.T) {
 		// Should not panic
 		ApplyTokenRefresh(nil, tokenResp, now)
 	})
 
 	t.Run("nil token response", func(t *testing.T) {
 		auth := &coreauth.Auth{}
 		// Should not panic
 		ApplyTokenRefresh(auth, nil, now)
 	})
 
 	t.Run("updates metadata and storage", func(t *testing.T) {
 		storage := &CopilotTokenStorage{}
 		auth := &coreauth.Auth{
 			Storage: storage,
 		}
 
 		ApplyTokenRefresh(auth, tokenResp, now)
 
 		if auth.Metadata["copilot_token"] != "new_token" {
 			t.Errorf("ApplyTokenRefresh() metadata copilot_token = %v, want new_token", auth.Metadata["copilot_token"])
 		}
 		if storage.CopilotToken != "new_token" {
 			t.Errorf("ApplyTokenRefresh() storage CopilotToken = %v, want new_token", storage.CopilotToken)
 		}
 		if auth.LastRefreshedAt != now {
 			t.Errorf("ApplyTokenRefresh() LastRefreshedAt = %v, want %v", auth.LastRefreshedAt, now)
 		}
 	})
 }
