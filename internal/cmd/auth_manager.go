package cmd

import (
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
)

// newAuthManager creates a new authentication manager instance with all supported
// authenticators and a file-based token store. It initializes authenticators for
// Gemini, Codex, Claude, Qwen, IFlow, Antigravity, and GitHub Copilot providers.
//
// Returns:
//   - *sdkAuth.Manager: A configured authentication manager instance
func newAuthManager() *sdkAuth.Manager {
	store := sdkAuth.GetTokenStore()
	manager := sdkAuth.NewManager(store,
		sdkAuth.NewGeminiAuthenticator(),
		sdkAuth.NewCodexAuthenticator(),
		sdkAuth.NewClaudeAuthenticator(),
		sdkAuth.NewQwenAuthenticator(),
		sdkAuth.NewIFlowAuthenticator(),
		sdkAuth.NewAntigravityAuthenticator(),
		sdkAuth.NewKimiAuthenticator(),
		sdkAuth.NewKiroAuthenticator(),
		sdkAuth.NewGitHubCopilotAuthenticator(),
	)
	return manager
}
