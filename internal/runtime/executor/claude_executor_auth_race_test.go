package executor

import (
	"context"
	"sync"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// A single Auth is shared by every in-flight request that selects the credential,
// so any request path reaching into Auth.Metadata directly races the others. An
// earlier fix locked only the device pool helpers and left the account-profile
// path unguarded, which these tests would have caught: they drive the exported
// entry points rather than the helper that was known to be broken.

func newSharedClaudeOAuthAuth(id string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:         id,
		Attributes: map[string]string{"api_key": "sk-ant-oat-race-probe"},
		Metadata:   map[string]any{},
	}
}

func TestClaudeExecutorPrepareRequestAuthIsRaceFreeOnSharedCredential(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		profile := &claudeauth.OAuthProfile{}
		profile.Account.UUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		profile.Account.Email = "user@example.com"
		profile.Organization.UUID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		profile.Organization.Name = "Example Org"
		return profile, nil
	}

	auth := newSharedClaudeOAuthAuth("claude-race-prepare")
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// ShouldPrepareRequestAuth reads the same map the writers below mutate.
			if executor.ShouldPrepareRequestAuth(auth) {
				if _, err := executor.PrepareRequestAuth(ctx, auth); err != nil {
					t.Errorf("PrepareRequestAuth() error = %v", err)
				}
				return
			}
			_ = executor.ShouldPrepareRequestAuth(auth)
		}()
	}
	wg.Wait()

	if got := claudeauth.ReadMetadataString(&auth.Metadata, "account_uuid"); got != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("account_uuid = %q, want the fetched profile account", got)
	}
	if !claudeauth.HasCanonicalDeviceIDPool(claudeauth.ReadDeviceIDPool(&auth.Metadata)) {
		t.Fatal("device ID pool was not established under concurrency")
	}
}

// TestClaudeExecutorSharedCredentialMetadataMixedAccess drives the request-path
// readers against the profile writer at the same time, which is the shape that
// produced the reported data races.
func TestClaudeExecutorSharedCredentialMetadataReadersUseOneLock(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "claude-race-all-readers", Metadata: map[string]any{
		"access_token":          "sk-ant-oat-race-probe",
		"cloak_mode":            "always",
		"cloak_sensitive_words": "secret",
	}}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%3 == 0 {
				claudeauth.StoreMetadataValue(&auth.Metadata, "access_token", "sk-ant-oat-race-probe")
				claudeauth.StoreMetadataValue(&auth.Metadata, "cloak_mode", "always")
				return
			}
			if i%3 == 1 {
				_, _ = claudeCreds(auth)
				return
			}
			_, _, _, _ = getCloakConfigFromAuth(auth)
		}(i)
	}
	close(start)
	wg.Wait()
}

func TestClaudeExecutorSharedCredentialMetadataMixedAccess(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		profile := &claudeauth.OAuthProfile{}
		profile.Account.UUID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		return profile, nil
	}

	auth := newSharedClaudeOAuthAuth("claude-race-mixed")
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 4 {
			case 0:
				if _, err := executor.PrepareRequestAuth(ctx, auth); err != nil {
					t.Errorf("PrepareRequestAuth() error = %v", err)
				}
			case 1:
				_ = executor.ShouldPrepareRequestAuth(auth)
			case 2:
				_ = claudeauth.ReadMetadataString(&auth.Metadata, "account_uuid")
			default:
				_ = claudeauth.ReadDeviceIDPool(&auth.Metadata)
			}
		}(i)
	}
	wg.Wait()
}

// isClaudeSetupToken runs on every PrepareRequestAuth that still lacks an account
// UUID, while another request on the same credential may be storing the account
// profile. Its flag reads must go through the metadata lock; under -race an
// unlocked read is reported on every run, since nothing orders it after the
// locked write.
func TestIsClaudeSetupTokenReadsMetadataUnderLock(t *testing.T) {
	auth := newSharedClaudeOAuthAuth("claude-race-setup-token")
	// A setup-token flag makes it return before its locked scope read, which would
	// otherwise order an unlocked flag read before the write when it runs first.
	auth.Metadata["setup_token"] = true
	apiKey := auth.Attributes["api_key"]

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		claudeauth.StoreMetadataString(&auth.Metadata, "account_uuid", "dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	}()
	go func() {
		defer wg.Done()
		if !isClaudeSetupToken(auth, apiKey) {
			t.Error("isClaudeSetupToken() = false, want true for setup_token")
		}
	}()
	wg.Wait()
}

func TestIsClaudeSetupTokenFlags(t *testing.T) {
	const apiKey = "sk-ant-oat-flag-probe"
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		want     bool
	}{
		{"no flags", map[string]any{}, false},
		{"nil metadata", nil, false},
		{"skip_account_profile", map[string]any{"skip_account_profile": true}, true},
		{"is_setup_token", map[string]any{"is_setup_token": true}, true},
		{"setup_token", map[string]any{"setup_token": true}, true},
		{"flag false", map[string]any{"setup_token": false}, false},
		{"flag not a bool", map[string]any{"setup_token": "true"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": apiKey}, Metadata: tc.metadata}
			if got := isClaudeSetupToken(auth, apiKey); got != tc.want {
				t.Fatalf("isClaudeSetupToken() = %v, want %v", got, tc.want)
			}
		})
	}
}
