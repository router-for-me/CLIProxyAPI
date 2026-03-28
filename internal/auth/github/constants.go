// Package github provides OAuth2 device flow authentication for GitHub Copilot.
// It handles token acquisition via GitHub's device authorization grant and
// exchanges GitHub user tokens for Copilot API tokens.
package github

import "time"

// OAuth client credentials and device flow configuration.
const (
	// ClientID is the GitHub OAuth app client ID used for device flow.
	// This is the public client ID from the GitHub CLI app, widely accepted by GitHub's OAuth endpoints.
	ClientID = "01ab8ac9400c4e429b23"

	// Scope is the OAuth scope required for GitHub Copilot access.
	Scope = "read:user"

	// DeviceCodeURL is the endpoint for initiating the device authorization flow.
	DeviceCodeURL = "https://github.com/login/device/code"

	// AccessTokenURL is the endpoint for polling/exchanging device codes for access tokens.
	AccessTokenURL = "https://github.com/login/oauth/access_token"

	// UserInfoURL is the GitHub API endpoint for fetching authenticated user info.
	UserInfoURL = "https://api.github.com/user"

	// CopilotTokenURL is the endpoint for exchanging a GitHub user token for a Copilot API token.
	CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

	// CopilotAPIBaseURL is the base URL for GitHub Copilot chat completions.
	CopilotAPIBaseURL = "https://api.githubcopilot.com"
)

// Polling configuration for device flow.
const (
	defaultPollInterval = 5 * time.Second
	maxPollDuration     = 15 * time.Minute
)
