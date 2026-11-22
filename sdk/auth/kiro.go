package auth

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// KiroAuthenticator implements OAuth and local credential import for Kiro CLI.
type KiroAuthenticator struct {
	AuthService *kiroauth.KiroAuthService
}

// NewKiroAuthenticator constructs a new Kiro authenticator instance.
func NewKiroAuthenticator() *KiroAuthenticator {
	return &KiroAuthenticator{}
}

// Provider returns the unique provider identifier for Kiro.
func (a *KiroAuthenticator) Provider() string {
	return "kiro"
}

// RefreshLead specifies that Kiro tokens should be refreshed 15 minutes before expiration.
func (a *KiroAuthenticator) RefreshLead() *time.Duration {
	d := 15 * time.Minute
	return &d
}

// Login executes local token detection or launches the login flow.
func (a *KiroAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := a.AuthService
	if authSvc == nil {
		authSvc = kiroauth.NewKiroAuthServiceWithProxyURL(cfg, opts.ProxyURL)
	}

	// 1. Try detecting existing local Kiro CLI session
	bundle, errDetect := kiroauth.DetectLocalKiroToken()
	if errDetect == nil && bundle != nil && bundle.RefreshToken != "" {
		log.Infof("found existing local Kiro session for %s", bundle.Email)
		// Perform an initial refresh to ensure active access token
		freshBundle, errRefresh := authSvc.RefreshTokens(ctx, bundle.RefreshToken)
		if errRefresh == nil && freshBundle != nil {
			if bundle.Email != "" && freshBundle.Email == "" {
				freshBundle.Email = bundle.Email
			}
			if bundle.Provider != "" && freshBundle.Provider == "" {
				freshBundle.Provider = bundle.Provider
			}
			bundle = freshBundle
		} else {
			log.Warnf("initial refresh of detected Kiro token failed: %v; proceeding with cached token", errRefresh)
		}
		return kiroauth.CreateAuthRecord(bundle)
	}

	// 2. If not detected, check if kiro-cli binary is available to run interactive login
	if !opts.NoBrowser {
		if path, err := exec.LookPath("kiro-cli"); err == nil && path != "" {
			fmt.Println("No existing Kiro token found. Launching 'kiro-cli login'...")
			cmd := exec.CommandContext(ctx, "kiro-cli", "login")
			cmd.Stdin = nil
			cmd.Stdout = nil
			cmd.Stderr = nil
			_ = cmd.Run()

			// Check again after login attempt
			if bundleAfter, errAfter := kiroauth.DetectLocalKiroToken(); errAfter == nil && bundleAfter != nil {
				return kiroauth.CreateAuthRecord(bundleAfter)
			}
		}
	}

	// 3. Fallback prompt if available
	if opts.Prompt != nil {
		refreshToken, errPrompt := opts.Prompt("Enter Kiro refresh token (or run 'kiro-cli login' first): ")
		if errPrompt != nil {
			return nil, errPrompt
		}
		refreshToken = strings.TrimSpace(refreshToken)
		if refreshToken != "" {
			freshBundle, errRefresh := authSvc.RefreshTokens(ctx, refreshToken)
			if errRefresh != nil {
				return nil, fmt.Errorf("failed to authenticate with provided refresh token: %w", errRefresh)
			}
			return kiroauth.CreateAuthRecord(freshBundle)
		}
	}

	return nil, fmt.Errorf("no Kiro token found. Please log in using 'kiro-cli login' first")
}
