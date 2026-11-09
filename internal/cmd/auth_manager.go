package cmd

import (
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
)

// newAuthManager creates a new authentication manager instance with all supported
// authenticators and a file-based token store. It initializes authenticators for
// Codex, Claude, Antigravity, Kimi, ZCode, xAI, Devin, and Meta providers.
//
// Returns:
//   - *sdkAuth.Manager: A configured authentication manager instance
func newAuthManager() *sdkAuth.Manager {
	store := sdkAuth.GetTokenStore()
	manager := sdkAuth.NewManager(store,
		sdkAuth.NewCodexAuthenticator(),
		sdkAuth.NewClaudeAuthenticator(),
		sdkAuth.NewAntigravityAuthenticator(),
		sdkAuth.NewZCodeAuthenticator(),
		sdkAuth.NewKiroAuthenticator(),
		sdkAuth.NewKimiAuthenticator(),
		sdkAuth.NewKimiAIAuthenticator(),
		sdkAuth.NewKimiAIDotAuthenticator(),
		sdkAuth.NewQoderAuthenticator(),
		sdkAuth.NewQoderCNAuthenticator(),
		sdkAuth.NewXAIAuthenticator(),
		sdkAuth.NewDevinAuthenticator(),
		sdkAuth.NewMetaAuthenticator(),
	)
	return manager
}
