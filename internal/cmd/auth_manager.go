package cmd

import (
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
)

// newAuthManager creates a new authentication manager instance with all supported
// authenticators and a file-based token store. It initializes authenticators for
// Gemini, Codex, Claude, Antigravity, Kimi, xAI, Kiro, GitHub Copilot, Kilo,
// GitLab, CodeBuddy, and Cursor providers.
//
// Returns:
//   - *sdkAuth.Manager: A configured authentication manager instance
func newAuthManager() *sdkAuth.Manager {
	store := sdkAuth.GetTokenStore()
	manager := sdkAuth.NewManager(store,
		sdkAuth.NewCodexAuthenticator(),
		sdkAuth.NewClaudeAuthenticator(),
		sdkAuth.NewAntigravityAuthenticator(),
		sdkAuth.NewKimiAuthenticator(),
		sdkAuth.NewXAIAuthenticator(),
		sdkAuth.NewKiroAuthenticator(),
		sdkAuth.NewGitHubCopilotAuthenticator(),
		sdkAuth.NewKiloAuthenticator(),
		sdkAuth.NewGitLabAuthenticator(),
		sdkAuth.NewCodeBuddyAuthenticator(),
		sdkAuth.NewCursorAuthenticator(),
		sdkAuth.NewQoderAuthenticator(),
	)
	return manager
}
