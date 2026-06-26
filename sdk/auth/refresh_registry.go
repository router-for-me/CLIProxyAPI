package auth

import (
	"time"

<<<<<<< HEAD
	cliproxyauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
=======
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
>>>>>>> upstream/main
)

func init() {
	registerRefreshLead("codex", func() Authenticator { return NewCodexAuthenticator() })
	registerRefreshLead("claude", func() Authenticator { return NewClaudeAuthenticator() })
	registerRefreshLead("antigravity", func() Authenticator { return NewAntigravityAuthenticator() })
	registerRefreshLead("kimi", func() Authenticator { return NewKimiAuthenticator() })
<<<<<<< HEAD
	registerRefreshLead("kiro", func() Authenticator { return NewKiroAuthenticator() })
	registerRefreshLead("github-copilot", func() Authenticator { return NewGitHubCopilotAuthenticator() })
	registerRefreshLead("gitlab", func() Authenticator { return NewGitLabAuthenticator() })
	registerRefreshLead("codebuddy", func() Authenticator { return NewCodeBuddyAuthenticator() })
	registerRefreshLead("cursor", func() Authenticator { return NewCursorAuthenticator() })
=======
	registerRefreshLead("xai", func() Authenticator { return NewXAIAuthenticator() })
>>>>>>> upstream/main
}

func registerRefreshLead(provider string, factory func() Authenticator) {
	cliproxyauth.RegisterRefreshLeadProvider(provider, func() *time.Duration {
		if factory == nil {
			return nil
		}
		auth := factory()
		if auth == nil {
			return nil
		}
		return auth.RefreshLead()
	})
}
