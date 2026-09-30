package executor

import (
	"context"
	"sync"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// isClaudeSetupToken reads Auth.Metadata directly. Every other accessor on that
// map takes claudeDevicePoolMu, so a bare index here races any concurrent
// StoreMetadataValue on the same shared credential. The race only shows up under
// load: the detector needs the two goroutines to actually interleave.
//
// The fixture deliberately omits account_uuid. With it preset,
// PrepareRequestAuth returns before reaching the setup-token check, which is why
// the sibling test in claude_executor_auth_race_test.go did not catch this.
func TestIsClaudeSetupTokenConcurrentMetadataAccess(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       "shared-setup-token-credential",
		Metadata: map[string]any{},
	}
	apiKey := "sk-ant-oat01-shared"

	const iterations = 200
	var wg sync.WaitGroup

	start := make(chan struct{})

	// Readers: the unlocked path under test, plus the locked accessor for
	// comparison.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				_ = isClaudeSetupToken(auth, apiKey)
			}
		}()
	}

	// Writers: every write goes through the locked store helpers.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				claudeauth.StoreMetadataString(&auth.Metadata, "is_setup_token", "true")
				claudeauth.StoreMetadataValue(&auth.Metadata, "last_refresh", "2026-10-01T00:00:00Z")
				claudeauth.EnsureMetadataMap(&auth.Metadata)
			}
		}(i)
	}

	close(start)
	wg.Wait()
}

// TestPrepareRequestAuthReachesSetupTokenCheckUnderConcurrency exercises the
// same path through the exported entry point, so a future early return cannot
// silently stop covering the unlocked read.
func TestPrepareRequestAuthReachesSetupTokenCheckUnderConcurrency(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		profile := &claudeauth.OAuthProfile{}
		profile.Account.UUID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		return profile, nil
	}

	// No account_uuid preset: this is what lets the call reach
	// isClaudeSetupToken instead of returning early.
	auth := &cliproxyauth.Auth{
		ID:       "shared-setup-token-entrypoint",
		Metadata: map[string]any{},
	}
	ctx := context.Background()

	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			for j := 0; j < 50; j++ {
				switch i % 3 {
				case 0:
					if _, err := executor.PrepareRequestAuth(ctx, auth); err != nil {
						t.Errorf("PrepareRequestAuth() error = %v", err)
					}
				case 1:
					claudeauth.StoreMetadataString(&auth.Metadata, "is_setup_token", "true")
				default:
					_ = claudeauth.ReadMetadataBool(&auth.Metadata, "is_setup_token")
				}
			}
		}(i)
	}

	close(start)
	wg.Wait()
}
