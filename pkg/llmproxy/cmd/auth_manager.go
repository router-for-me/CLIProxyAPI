package cmd

import (
<<<<<<< HEAD:pkg/llmproxy/cmd/auth_manager.go
	sdkAuth "github.com/kooshapari/CLIProxyAPI/v7/sdk/auth"
)

// newAuthManager creates a new authentication manager instance with all supported
// authenticators and a file-based token store.
=======
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
)

// newAuthManager creates a new authentication manager instance with all supported
// authenticators and a file-based token store. It initializes authenticators for
// Codex, Claude, Antigravity, Kimi, and xAI providers.
>>>>>>> upstream/main:internal/cmd/auth_manager.go
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
<<<<<<< HEAD:pkg/llmproxy/cmd/auth_manager.go
		sdkAuth.NewKiroAuthenticator(),
		sdkAuth.NewGitHubCopilotAuthenticator(),
		sdkAuth.NewKiloAuthenticator(),
		sdkAuth.NewGitLabAuthenticator(),
		sdkAuth.NewCodeBuddyAuthenticator(),
		sdkAuth.NewCursorAuthenticator(),
=======
		sdkAuth.NewXAIAuthenticator(),
>>>>>>> upstream/main:internal/cmd/auth_manager.go
	)
	return manager
}
