// Package copilot provides authentication and token management for GitHub Copilot API.
// It handles the OAuth2 device code flow, token exchange, and automatic token refresh.
package copilot

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	copilotshared "github.com/router-for-me/CLIProxyAPI/v6/internal/copilot"
)

const (
	GitHubBaseURL    = "https://github.com"
	GitHubAPIBaseURL = "https://api.github.com"
	// GitHubClientID is the PUBLIC OAuth client ID for GitHub Copilot's VS Code extension.
	// This is NOT a secret - it's the same client ID used by the official Copilot CLI and
	// VS Code extension, publicly visible in their source code and network requests.
	GitHubClientID   = "Iv1.b507a08c87ecfe98"
	GitHubAppScopes  = "read:user"
	DeviceCodePath   = "/login/device/code"
	AccessTokenPath  = "/login/oauth/access_token"
	CopilotTokenPath = "/copilot_internal/v2/token"
	CopilotUserPath  = "/copilot_internal/user"
	UserInfoPath     = "/user"

	CopilotVersion       = "0.0.363"
	EditorPluginVersion  = "copilot/" + CopilotVersion
	CopilotUserAgent     = "copilot/" + CopilotVersion + " (linux v22.15.0)"
	CopilotAPIVersion    = "2025-05-01"
	CopilotIntegrationID = "copilot-developer-cli"
	DefaultVSCodeVersion = "1.95.0"
)

type AccountType = copilotshared.AccountType

const (
	AccountTypeIndividual AccountType = copilotshared.AccountTypeIndividual
	AccountTypeBusiness   AccountType = copilotshared.AccountTypeBusiness
	AccountTypeEnterprise AccountType = copilotshared.AccountTypeEnterprise
)

var ValidAccountTypes = copilotshared.ValidAccountTypes

const DefaultAccountType = copilotshared.DefaultAccountType

func CopilotBaseURL(accountType AccountType) string {
	switch accountType {
	case AccountTypeBusiness:
		return "https://api.business.githubcopilot.com"
	case AccountTypeEnterprise:
		return "https://api.enterprise.githubcopilot.com"
	default:
		// Individual accounts use the individual Copilot endpoint.
		return "https://api.individual.githubcopilot.com"
	}
}

func StandardHeaders() map[string]string {
	return map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
	}
}

func GitHubHeaders(githubToken, vsCodeVersion string) map[string]string {
	if vsCodeVersion == "" {
		vsCodeVersion = DefaultVSCodeVersion
	}
	return map[string]string{
		"Content-Type":                        "application/json",
		"Accept":                              "application/json",
		"Authorization":                       fmt.Sprintf("token %s", githubToken),
		"Editor-Version":                      fmt.Sprintf("vscode/%s", vsCodeVersion),
		"Editor-Plugin-Version":               EditorPluginVersion,
		"User-Agent":                          CopilotUserAgent,
		"X-Github-Api-Version":                CopilotAPIVersion,
		"X-Vscode-User-Agent-Library-Version": "electron-fetch",
	}
}

func CopilotHeaders(copilotToken, vsCodeVersion string, enableVision bool) map[string]string {
	if vsCodeVersion == "" {
		vsCodeVersion = DefaultVSCodeVersion
	}
	headers := map[string]string{
		"Content-Type":                        "application/json",
		"Authorization":                       fmt.Sprintf("Bearer %s", copilotToken),
		"Copilot-Integration-Id":              CopilotIntegrationID,
		"Editor-Version":                      fmt.Sprintf("vscode/%s", vsCodeVersion),
		"Editor-Plugin-Version":               EditorPluginVersion,
		"User-Agent":                          CopilotUserAgent,
		"Openai-Intent":                       "conversation-agent",
		"X-Github-Api-Version":                CopilotAPIVersion,
		"X-Request-Id":                        generateRequestID(),
		"X-Interaction-Id":                    generateRequestID(),
		"X-Vscode-User-Agent-Library-Version": "electron-fetch",
	}
	if enableVision {
		headers["Copilot-Vision-Request"] = "true"
	}
	return headers
}

func generateRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("failed to generate random bytes for request ID: %v", err))
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]))
}

// MaskToken returns a masked version of a token for safe logging.
// Shows first 2 and last 2 characters with asterisks in between.
// Returns "<empty>" for empty tokens and "<short>" for tokens under 5 chars.
func MaskToken(token string) string {
	if token == "" {
		return "<empty>"
	}
	if len(token) < 5 {
		return "<short>"
	}
	return token[:2] + "****" + token[len(token)-2:]
}
