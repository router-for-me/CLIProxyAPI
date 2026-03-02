package config

// defaultKiroAliases returns default oauth-model-alias entries for Kiro.
// These aliases expose standard Claude IDs for Kiro-prefixed upstream models.
func defaultKiroAliases() []OAuthModelAlias {
	return []OAuthModelAlias{
		// Sonnet 4.6
		{Name: "kiro-claude-sonnet-4-6", Alias: "claude-sonnet-4-6", Fork: true},
		// Sonnet 4.5
		{Name: "kiro-claude-sonnet-4-5", Alias: "claude-sonnet-4-5-20250929", Fork: true},
		{Name: "kiro-claude-sonnet-4-5", Alias: "claude-sonnet-4-5", Fork: true},
		// Sonnet 4
		{Name: "kiro-claude-sonnet-4", Alias: "claude-sonnet-4-20250514", Fork: true},
		{Name: "kiro-claude-sonnet-4", Alias: "claude-sonnet-4", Fork: true},
		// Opus 4.6
		{Name: "kiro-claude-opus-4-6", Alias: "claude-opus-4-6", Fork: true},
		// Opus 4.5
		{Name: "kiro-claude-opus-4-5", Alias: "claude-opus-4-5-20251101", Fork: true},
		{Name: "kiro-claude-opus-4-5", Alias: "claude-opus-4-5", Fork: true},
		// Haiku 4.5
		{Name: "kiro-claude-haiku-4-5", Alias: "claude-haiku-4-5-20251001", Fork: true},
		{Name: "kiro-claude-haiku-4-5", Alias: "claude-haiku-4-5", Fork: true},
	}
}

// defaultGitHubCopilotAliases returns default oauth-model-alias entries for
// GitHub Copilot Claude models. It exposes hyphen-style IDs used by clients.
func defaultGitHubCopilotAliases() []OAuthModelAlias {
	return []OAuthModelAlias{
		{Name: "claude-haiku-4.5", Alias: "claude-haiku-4-5", Fork: true},
		{Name: "claude-opus-4.1", Alias: "claude-opus-4-1", Fork: true},
		{Name: "claude-opus-4.5", Alias: "claude-opus-4-5", Fork: true},
		{Name: "claude-opus-4.6", Alias: "claude-opus-4-6", Fork: true},
		{Name: "claude-sonnet-4.5", Alias: "claude-sonnet-4-5", Fork: true},
		{Name: "claude-sonnet-4.6", Alias: "claude-sonnet-4-6", Fork: true},
	}
}
