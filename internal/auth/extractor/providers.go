// Package extractor discovers local credentials for coding agents and AI subscriptions.
package extractor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// providerSource defines a credential source to try.
type providerSource struct {
	Provider   string
	EnvKey     string
	Files      []string
	JSONKeys   []string
	Type       string
	BaseURL    string
	CompatName string
}

var sources = []providerSource{
	{
		Provider: "windsurf",
		Files:    []string{".local/share/devin/credentials.toml", ".codex/devin-credentials.toml"},
		JSONKeys: []string{"windsurf_api_key", "api_key"},
		Type:     "windsurf",
		BaseURL:  "https://server.codeium.com",
	},
	{
		Provider: "codex",
		Files:    []string{".codex/auth.json", ".codex/token.json", ".codex/credentials.json"},
		JSONKeys: []string{"access_token", "token", "api_key"},
		Type:     "codex",
	},
	{
		Provider: "claude-code",
		Files:    []string{".claude/auth.json", ".claude/token.json", ".claude/credentials.json", ".claude-cli/auth.json"},
		JSONKeys: []string{"api_key", "access_token", "token"},
		Type:     "claude",
		BaseURL:  "https://api.anthropic.com",
	},
	{
		Provider: "cursor",
		Files:    []string{".cursor/auth.json", ".cursor/token.json", ".cursor/credentials.json"},
		JSONKeys: []string{"api_key", "access_token", "token"},
		Type:     "cursor",
	},
	{
		Provider: "opencode",
		Files:    []string{".opencode/auth.json", ".opencode/token.json", ".opencode/config.json", ".opencode/credentials.json"},
		JSONKeys: []string{"api_key", "access_token", "token"},
		Type:     "openai-compatibility",
		BaseURL:  "https://api.opencode.ai",
		CompatName: "opencode",
	},
	{
		Provider: "openai",
		EnvKey:   "OPENAI_API_KEY",
		Files:    []string{".openai/auth.json", ".openai/token.json", ".openai/config.json"},
		JSONKeys: []string{"api_key", "access_token", "token"},
		Type:     "openai-compatibility",
		BaseURL:  "https://api.openai.com",
		CompatName: "openai",
	},
	{
		Provider: "anthropic",
		EnvKey:   "ANTHROPIC_API_KEY",
		Files:    []string{".anthropic/auth.json", ".anthropic/token.json", ".anthropic/config.json"},
		JSONKeys: []string{"api_key", "access_token", "token"},
		Type:     "claude",
		BaseURL:  "https://api.anthropic.com",
	},
	{
		Provider: "gemini",
		EnvKey:   "GEMINI_API_KEY",
		Files:    []string{".local/share/gemini/api_key", ".gemini/auth.json", ".gemini/api_key"},
		JSONKeys: []string{"api_key"},
		Type:     "gemini",
	},
	{
		Provider: "deepseek",
		EnvKey:   "DEEPSEEK_API_KEY",
		Files:    []string{".deepseek/auth.json", ".deepseek/config.json", ".deepseek/api_key"},
		JSONKeys: []string{"api_key", "token", "access_token"},
		Type:     "openai-compatibility",
		BaseURL:  "https://api.deepseek.com",
		CompatName: "deepseek",
	},
	{
		Provider: "mistral",
		EnvKey:   "MISTRAL_API_KEY",
		Files:    []string{".mistral/auth.json", ".mistral/config.json"},
		JSONKeys: []string{"api_key", "token"},
		Type:     "openai-compatibility",
		BaseURL:  "https://api.mistral.ai",
		CompatName: "mistral",
	},
	{
		Provider: "groq",
		EnvKey:   "GROQ_API_KEY",
		Files:    []string{".groq/auth.json", ".groq/config.json"},
		JSONKeys: []string{"api_key", "token"},
		Type:     "openai-compatibility",
		BaseURL:  "https://api.groq.com/openai/v1",
		CompatName: "groq",
	},
	{
		Provider: "xai",
		EnvKey:   "XAI_API_KEY",
		Files:    []string{".xai/auth.json", ".xai/config.json"},
		JSONKeys: []string{"api_key", "token"},
		Type:     "openai-compatibility",
		BaseURL:  "https://api.x.ai/v1",
		CompatName: "xai",
	},
	{
		Provider: "cohere",
		EnvKey:   "COHERE_API_KEY",
		Files:    []string{".cohere/auth.json", ".cohere/config.json"},
		JSONKeys: []string{"api_key", "token"},
		Type:     "openai-compatibility",
		BaseURL:  "https://api.cohere.com/v1",
		CompatName: "cohere",
	},
	{
		Provider: "perplexity",
		EnvKey:   "PERPLEXITY_API_KEY",
		Files:    []string{".perplexity/auth.json", ".perplexity/config.json"},
		JSONKeys: []string{"api_key", "token"},
		Type:     "openai-compatibility",
		BaseURL:  "https://api.perplexity.ai",
		CompatName: "perplexity",
	},
	{
		Provider: "together",
		EnvKey:   "TOGETHER_API_KEY",
		Files:    []string{".together/auth.json", ".together/config.json"},
		JSONKeys: []string{"api_key", "token"},
		Type:     "openai-compatibility",
		BaseURL:  "https://api.together.xyz/v1",
		CompatName: "together",
	},
	{
		Provider: "fireworks",
		EnvKey:   "FIREWORKS_API_KEY",
		Files:    []string{".fireworks/auth.json", ".fireworks/config.json"},
		JSONKeys: []string{"api_key", "token"},
		Type:     "openai-compatibility",
		BaseURL:  "https://api.fireworks.ai/inference/v1",
		CompatName: "fireworks",
	},
	{
		Provider: "openrouter",
		EnvKey:   "OPENROUTER_API_KEY",
		Files:    []string{".openrouter/auth.json", ".openrouter/config.json"},
		JSONKeys: []string{"api_key", "token"},
		Type:     "openai-compatibility",
		BaseURL:  "https://openrouter.ai/api/v1",
		CompatName: "openrouter",
	},
	{
		Provider: "azure-openai",
		EnvKey:   "AZURE_OPENAI_API_KEY",
		Files:    []string{".azure/openai.json", ".azure/config.json"},
		JSONKeys: []string{"api_key", "token"},
		Type:     "openai-compatibility",
		BaseURL:  "https://api.openai.com",
		CompatName: "azure-openai",
	},
	{
		Provider: "aider",
		EnvKey:   "OPENAI_API_KEY",
		Files:    []string{".aider.conf.yml", ".aider.conf.yaml", ".aider/config.yml", ".aider/config.yaml"},
		JSONKeys: []string{"api_key"},
		Type:     "openai-compatibility",
		BaseURL:  "https://api.openai.com",
		CompatName: "aider",
	},
	{
		Provider: "cline",
		Files:    []string{".cline/auth.json", ".cline/config.json", ".config/Cline/auth.json"},
		JSONKeys: []string{"api_key", "token", "access_token"},
		Type:     "openai-compatibility",
		BaseURL:  "https://api.openai.com",
		CompatName: "cline",
	},
	{
		Provider: "continue",
		Files:    []string{".continue/config.json", ".continue/config.yaml", ".continue/auth.json"},
		JSONKeys: []string{"api_key", "token"},
		Type:     "openai-compatibility",
		BaseURL:  "https://api.openai.com",
		CompatName: "continue",
	},
	{
		Provider: "vscode-copilot",
		Files:    []string{".vscode-copilot/auth.json", ".config/Code/User/globalStorage/github.copilot/config.json"},
		JSONKeys: []string{"token", "access_token"},
		Type:     "openai-compatibility",
		BaseURL:  "https://api.githubcopilot.com",
		CompatName: "copilot",
	},
}

// findStringInJSON recursively searches for any of the given keys in a JSON value.
func findStringInJSON(v any, keys []string) string {
	switch val := v.(type) {
	case map[string]any:
		for _, key := range keys {
			if s, ok := val[key].(string); ok && s != "" {
				return s
			}
		}
		for _, v := range val {
			if s := findStringInJSON(v, keys); s != "" {
				return s
			}
		}
	case []any:
		for _, item := range val {
			if s := findStringInJSON(item, keys); s != "" {
				return s
			}
		}
	}
	return ""
}

// extractKeyFromJSON extracts a string key from JSON data recursively.
func extractKeyFromJSON(data []byte, keys ...string) string {
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ""
	}
	return findStringInJSON(parsed, keys)
}

// defaultString returns a if non-empty, otherwise b.
func defaultString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// userHome returns the user's home directory or an empty string.
func userHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// readFirstFile tries to read the first existing file path relative to home.
func readFirstFile(home string, paths []string) ([]byte, string) {
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(home, p)
		}
		data, err := os.ReadFile(p)
		if err == nil {
			return data, p
		}
	}
	return nil, ""
}

// findEnvKey searches for KEY=value in common dotenv/config files.
func findEnvKey(home, key string, dirs []string) string {
	searchDirs := []string{""}
	for _, d := range dirs {
		if d == "" || d == "." {
			continue
		}
		searchDirs = append(searchDirs, filepath.Join(home, d))
	}

	filenames := []string{".env", ".env.local", "config", "credentials", "settings.json"}
	for _, dir := range searchDirs {
		for _, name := range filenames {
			path := filepath.Join(dir, name)
			if dir == "" {
				path = filepath.Join(home, name)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "#") {
					continue
				}
				parts := strings.SplitN(line, "=", 2)
				if len(parts) != 2 {
					continue
				}
				if strings.TrimSpace(parts[0]) == key {
					return strings.Trim(strings.TrimSpace(parts[1]), `"'`)
				}
			}
		}
	}
	return ""
}
