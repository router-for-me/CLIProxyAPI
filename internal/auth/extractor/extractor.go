// Package extractor discovers local credentials for coding agents and writes them
// as CLIProxyAPI auth JSON files under the configured auth directory.
package extractor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ExtractedAuth holds the result of a credential extraction.
type ExtractedAuth struct {
	Provider string
	Filename string
	Data     map[string]any
}

// ExtractAll scans known local credential sources and writes auth files to
// the provided auth directory. It returns the list of providers for which
// credentials were found.
func ExtractAll(authDir string) ([]string, error) {
	if authDir == "" {
		return nil, fmt.Errorf("auth directory not set")
	}
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return nil, err
	}

	extractors := []func() *ExtractedAuth{
		extractWindsurf,
		extractCodex,
		extractClaude,
		extractCursor,
		extractGemini,
		extractOpenAI,
		extractAnthropic,
		extractDeepSeek,
		extractMistral,
		extractGroq,
		extractCohere,
		extractPerplexity,
		extractTogether,
		extractFireworks,
	}

	var found []string
	for _, extractor := range extractors {
		if auth := extractor(); auth != nil {
			path := filepath.Join(authDir, auth.Filename)
			if err := writeAuthFile(path, auth.Data); err != nil {
				return found, fmt.Errorf("write %s: %w", path, err)
			}
			found = append(found, auth.Provider)
		}
	}

	return found, nil
}

func writeAuthFile(path string, data map[string]any) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func extractWindsurf() *ExtractedAuth {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(home, ".local", "share", "devin", "credentials.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg struct {
		WindsurfAPIKey string `toml:"windsurf_api_key"`
		APIServerURL   string `toml:"api_server_url"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	if cfg.WindsurfAPIKey == "" {
		return nil
	}
	return &ExtractedAuth{
		Provider: "windsurf",
		Filename: "windsurf.json",
		Data: map[string]any{
			"type":     "windsurf",
			"api_key":  cfg.WindsurfAPIKey,
			"base_url": defaultString(cfg.APIServerURL, "https://server.codeium.com"),
		},
	}
}

func extractCodex() *ExtractedAuth {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(home, ".codex", "auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		path = filepath.Join(home, ".codex", "token.json")
		data, err = os.ReadFile(path)
		if err != nil {
			return nil
		}
	}
	apiKey := extractKeyFromJSON(data, "api_key", "token", "access_token")
	if apiKey == "" {
		return nil
	}
	return &ExtractedAuth{
		Provider: "codex",
		Filename: "codex.json",
		Data: map[string]any{
			"type":    "codex",
			"api_key": apiKey,
		},
	}
}

func extractClaude() *ExtractedAuth {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(home, ".claude", "auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	apiKey := extractKeyFromJSON(data, "api_key", "token", "access_token")
	if apiKey == "" {
		return nil
	}
	return &ExtractedAuth{
		Provider: "claude",
		Filename: "claude.json",
		Data: map[string]any{
			"type":    "claude",
			"api_key": apiKey,
		},
	}
}

func extractCursor() *ExtractedAuth {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(home, ".cursor", "auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	apiKey := extractKeyFromJSON(data, "api_key", "token", "access_token")
	if apiKey == "" {
		return nil
	}
	return &ExtractedAuth{
		Provider: "cursor",
		Filename: "cursor.json",
		Data: map[string]any{
			"type":    "cursor",
			"api_key": apiKey,
		},
	}
}

func extractGemini() *ExtractedAuth {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		path := filepath.Join(home, ".local", "share", "gemini", "api_key")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		apiKey = strings.TrimSpace(string(data))
	}
	if apiKey == "" {
		return nil
	}
	return &ExtractedAuth{
		Provider: "gemini",
		Filename: "gemini.json",
		Data: map[string]any{
			"type":    "gemini",
			"api_key": apiKey,
		},
	}
}

func extractOpenAI() *ExtractedAuth {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		path := filepath.Join(home, ".openai", "auth.json")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		apiKey = extractKeyFromJSON(data, "api_key", "token", "access_token")
	}
	if apiKey == "" {
		return nil
	}
	return &ExtractedAuth{
		Provider: "openai-compatibility",
		Filename: "openai.json",
		Data: map[string]any{
			"type":        "openai-compatibility",
			"api_key":     apiKey,
			"base_url":    "https://api.openai.com",
			"compat_name": "openai",
		},
	}
}

func extractAnthropic() *ExtractedAuth {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		apiKey = keychainFind("ANTHROPIC_API_KEY", "anthropic")
	}
	if apiKey == "" {
		apiKey = findEnvInFile("ANTHROPIC_API_KEY", ".anthropic", ".env")
	}
	if apiKey == "" {
		return nil
	}
	return &ExtractedAuth{
		Provider: "anthropic",
		Filename: "anthropic.json",
		Data: map[string]any{
			"type":    "claude",
			"api_key": apiKey,
			"base_url": "https://api.anthropic.com",
		},
	}
}

func extractDeepSeek() *ExtractedAuth {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		apiKey = keychainFind("DEEPSEEK_API_KEY", "deepseek")
	}
	if apiKey == "" {
		apiKey = findEnvInFile("DEEPSEEK_API_KEY", ".deepseek", ".env")
	}
	if apiKey == "" {
		return nil
	}
	return &ExtractedAuth{
		Provider: "deepseek",
		Filename: "deepseek.json",
		Data: map[string]any{
			"type":        "openai-compatibility",
			"api_key":     apiKey,
			"base_url":    "https://api.deepseek.com",
			"compat_name": "deepseek",
		},
	}
}

func extractMistral() *ExtractedAuth {
	apiKey := os.Getenv("MISTRAL_API_KEY")
	if apiKey == "" {
		apiKey = keychainFind("MISTRAL_API_KEY", "mistral")
	}
	if apiKey == "" {
		apiKey = findEnvInFile("MISTRAL_API_KEY", ".mistral", ".env")
	}
	if apiKey == "" {
		return nil
	}
	return &ExtractedAuth{
		Provider: "mistral",
		Filename: "mistral.json",
		Data: map[string]any{
			"type":        "openai-compatibility",
			"api_key":     apiKey,
			"base_url":    "https://api.mistral.ai",
			"compat_name": "mistral",
		},
	}
}

func extractGroq() *ExtractedAuth {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		apiKey = keychainFind("GROQ_API_KEY", "groq")
	}
	if apiKey == "" {
		apiKey = findEnvInFile("GROQ_API_KEY", ".groq", ".env")
	}
	if apiKey == "" {
		return nil
	}
	return &ExtractedAuth{
		Provider: "groq",
		Filename: "groq.json",
		Data: map[string]any{
			"type":        "openai-compatibility",
			"api_key":     apiKey,
			"base_url":    "https://api.groq.com/openai/v1",
			"compat_name": "groq",
		},
	}
}

func extractCohere() *ExtractedAuth {
	apiKey := os.Getenv("COHERE_API_KEY")
	if apiKey == "" {
		apiKey = keychainFind("COHERE_API_KEY", "cohere")
	}
	if apiKey == "" {
		apiKey = findEnvInFile("COHERE_API_KEY", ".cohere", ".env")
	}
	if apiKey == "" {
		return nil
	}
	return &ExtractedAuth{
		Provider: "cohere",
		Filename: "cohere.json",
		Data: map[string]any{
			"type":        "openai-compatibility",
			"api_key":     apiKey,
			"base_url":    "https://api.cohere.com/v1",
			"compat_name": "cohere",
		},
	}
}

func extractPerplexity() *ExtractedAuth {
	apiKey := os.Getenv("PERPLEXITY_API_KEY")
	if apiKey == "" {
		apiKey = keychainFind("PERPLEXITY_API_KEY", "perplexity")
	}
	if apiKey == "" {
		apiKey = findEnvInFile("PERPLEXITY_API_KEY", ".perplexity", ".env")
	}
	if apiKey == "" {
		return nil
	}
	return &ExtractedAuth{
		Provider: "perplexity",
		Filename: "perplexity.json",
		Data: map[string]any{
			"type":        "openai-compatibility",
			"api_key":     apiKey,
			"base_url":    "https://api.perplexity.ai",
			"compat_name": "perplexity",
		},
	}
}

func extractTogether() *ExtractedAuth {
	apiKey := os.Getenv("TOGETHER_API_KEY")
	if apiKey == "" {
		apiKey = keychainFind("TOGETHER_API_KEY", "together")
	}
	if apiKey == "" {
		apiKey = findEnvInFile("TOGETHER_API_KEY", ".together", ".env")
	}
	if apiKey == "" {
		return nil
	}
	return &ExtractedAuth{
		Provider: "together",
		Filename: "together.json",
		Data: map[string]any{
			"type":        "openai-compatibility",
			"api_key":     apiKey,
			"base_url":    "https://api.together.xyz/v1",
			"compat_name": "together",
		},
	}
}

func extractFireworks() *ExtractedAuth {
	apiKey := os.Getenv("FIREWORKS_API_KEY")
	if apiKey == "" {
		apiKey = keychainFind("FIREWORKS_API_KEY", "fireworks")
	}
	if apiKey == "" {
		apiKey = findEnvInFile("FIREWORKS_API_KEY", ".fireworks", ".env")
	}
	if apiKey == "" {
		return nil
	}
	return &ExtractedAuth{
		Provider: "fireworks",
		Filename: "fireworks.json",
		Data: map[string]any{
			"type":        "openai-compatibility",
			"api_key":     apiKey,
			"base_url":    "https://api.fireworks.ai/inference/v1",
			"compat_name": "fireworks",
		},
	}
}

func extractKeyFromJSON(data []byte, keys ...string) string {
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ""
	}
	for _, key := range keys {
		if s, ok := parsed[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func defaultString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// keychainFind tries to read an API key from the macOS keychain.
func keychainFind(envVar, serviceHint string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("security", "find-generic-password", "-s", serviceHint, "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// findEnvInFile searches for KEY=value patterns in common dotenv/config files.
func findEnvInFile(key string, dirs ...string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	searchDirs := []string{""}
	for _, d := range dirs {
		if d == "" || d == "." {
			continue
		}
		searchDirs = append(searchDirs, filepath.Join(home, d))
	}

	filenames := []string{".env", ".env.local", "config", "credentials"}
	for _, dir := range searchDirs {
		for _, name := range filenames {
			path := filepath.Join(dir, name)
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
