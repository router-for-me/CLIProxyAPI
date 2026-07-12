// Package extractor discovers local credentials for coding agents and writes them
// as CLIProxyAPI auth JSON files under the configured auth directory.
package extractor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
