package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoKiroLogin triggers the Kiro authentication flow with Google OAuth.
// This is the default login method (same as --kiro-google-login).
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including Prompt field
func DoKiroLogin(cfg *config.Config, options *LoginOptions) {
	// Use Google login as default
	DoKiroGoogleLogin(cfg, options)
}

// DoKiroGoogleLogin triggers Kiro authentication with Google OAuth.
// This uses a custom protocol handler (kiro://) to receive the callback.
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including prompts
func DoKiroGoogleLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	// Note: Kiro defaults to incognito mode for multi-account support.
	// Users can override with --no-incognito if they want to use existing browser sessions.

	manager := newAuthManager()

	// Use KiroAuthenticator with Google login
	authenticator := sdkAuth.NewKiroAuthenticator()
	record, err := authenticator.LoginWithGoogle(context.Background(), cfg, &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
		Prompt:    options.Prompt,
	})
	if err != nil {
		log.Errorf("Kiro Google authentication failed: %v", err)
		fmt.Println("\nTroubleshooting:")
		fmt.Println("1. Make sure the protocol handler is installed")
		fmt.Println("2. Complete the Google login in the browser")
		fmt.Println("3. If callback fails, try: --kiro-import (after logging in via Kiro IDE)")
		return
	}

	// Save the auth record
	savedPath, err := manager.SaveAuth(record, cfg)
	if err != nil {
		log.Errorf("Failed to save auth: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Kiro Google authentication successful!")
}

// DoKiroAWSLogin triggers Kiro authentication with AWS Builder ID.
// This uses the device code flow for AWS SSO OIDC authentication.
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including prompts
func DoKiroAWSLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	// Note: Kiro defaults to incognito mode for multi-account support.
	// Users can override with --no-incognito if they want to use existing browser sessions.

	manager := newAuthManager()

	// Use KiroAuthenticator with AWS Builder ID login (device code flow)
	authenticator := sdkAuth.NewKiroAuthenticator()
	record, err := authenticator.Login(context.Background(), cfg, &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
		Prompt:    options.Prompt,
	})
	if err != nil {
		log.Errorf("Kiro AWS authentication failed: %v", err)
		fmt.Println("\nTroubleshooting:")
		fmt.Println("1. Make sure you have an AWS Builder ID")
		fmt.Println("2. Complete the authorization in the browser")
		fmt.Println("3. If callback fails, try: --kiro-import (after logging in via Kiro IDE)")
		return
	}

	// Save the auth record
	savedPath, err := manager.SaveAuth(record, cfg)
	if err != nil {
		log.Errorf("Failed to save auth: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Kiro AWS authentication successful!")
}

// DoKiroAWSAuthCodeLogin triggers Kiro authentication with AWS Builder ID using authorization code flow.
// This provides a better UX than device code flow as it uses automatic browser callback.
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including prompts
func DoKiroAWSAuthCodeLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	// Note: Kiro defaults to incognito mode for multi-account support.
	// Users can override with --no-incognito if they want to use existing browser sessions.

	manager := newAuthManager()

	// Use KiroAuthenticator with AWS Builder ID login (authorization code flow)
	authenticator := sdkAuth.NewKiroAuthenticator()
	record, err := authenticator.LoginWithAuthCode(context.Background(), cfg, &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
		Prompt:    options.Prompt,
	})
	if err != nil {
		log.Errorf("Kiro AWS authentication (auth code) failed: %v", err)
		fmt.Println("\nTroubleshooting:")
		fmt.Println("1. Make sure you have an AWS Builder ID")
		fmt.Println("2. Complete the authorization in the browser")
		fmt.Println("3. If callback fails, try: --kiro-aws-login (device code flow)")
		return
	}

	// Save the auth record
	savedPath, err := manager.SaveAuth(record, cfg)
	if err != nil {
		log.Errorf("Failed to save auth: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Kiro AWS authentication successful!")
}

// DoKiroImport imports Kiro token from Kiro IDE's token file.
// This is useful for users who have already logged in via Kiro IDE
// and want to use the same credentials in CLI Proxy API.
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options (currently unused for import)
func DoKiroImport(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()

	// Use ImportFromKiroIDE instead of Login
	authenticator := sdkAuth.NewKiroAuthenticator()
	record, err := authenticator.ImportFromKiroIDE(context.Background(), cfg)
	if err != nil {
		log.Errorf("Kiro token import failed: %v", err)
		fmt.Println("\nMake sure you have logged in to Kiro IDE first:")
		fmt.Println("1. Open Kiro IDE")
		fmt.Println("2. Click 'Sign in with Google' (or GitHub)")
		fmt.Println("3. Complete the login process")
		fmt.Println("4. Run this command again")
		return
	}

	// Save the imported auth record
	savedPath, err := manager.SaveAuth(record, cfg)
	if err != nil {
		log.Errorf("Failed to save auth: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Imported as %s\n", record.Label)
	}
	fmt.Println("Kiro token import successful!")
}
