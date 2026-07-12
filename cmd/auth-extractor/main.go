package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

// authExtractor discovers local credentials for coding agents and writes them
// as CLIProxyAPI auth JSON files under ~/.cli-proxy-api.
//
// Supported providers:
//   - windsurf:   ~/.local/share/devin/credentials.toml (windsurf_api_key)
//   - codex:      ~/.codex/auth.json, ~/.codex/token.json
//   - claude:     ~/.claude/auth.json
//   - cursor:     ~/.cursor/auth.json
//   - gemini:     ~/.local/share/gemini/api_key or GEMINI_API_KEY env
//   - openai:     ~/.openai/auth.json or OPENAI_API_KEY env

func main() {
	authDir, err := util.ResolveAuthDir("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth-extractor: resolve auth dir: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "auth-extractor: mkdir: %v\n", err)
		os.Exit(1)
	}

	extractors := []func(string) *extractedAuth{
		extractWindsurf,
		extractCodex,
		extractClaude,
		extractCursor,
		extractGemini,
		extractOpenAI,
	}

	var written []string
	for _, extractor := range extractors {
		if auth := extractor(authDir); auth != nil {
			path := filepath.Join(authDir, auth.filename)
			if err := writeAuthFile(path, auth.data); err != nil {
				fmt.Fprintf(os.Stderr, "auth-extractor: write %s: %v\n", path, err)
				continue
			}
			written = append(written, auth.provider)
		}
	}

	if len(written) == 0 {
		fmt.Println("auth-extractor: no credentials found")
		return
	}
	fmt.Printf("auth-extractor: wrote credentials for %s to %s\n", strings.Join(written, ", "), authDir)
}

type extractedAuth struct {
	provider string
	filename string
	data     map[string]any
}

func writeAuthFile(path string, data map[string]any) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return nil
}

func extractWindsurf(authDir string) *extractedAuth {
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
	return &extractedAuth{
		provider: "windsurf",
		filename: "windsurf.json",
		data: map[string]any{
			"type":     "windsurf",
			"api_key":  cfg.WindsurfAPIKey,
			"base_url": defaultString(cfg.APIServerURL, "https://server.codeium.com"),
		},
	}
}

func extractCodex(authDir string) *extractedAuth {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(home, ".codex", "auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		// Fallback: try token.json
		path = filepath.Join(home, ".codex", "token.json")
		data, err = os.ReadFile(path)
		if err != nil {
			return nil
		}
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	apiKey, _ := parsed["api_key"].(string)
	if apiKey == "" {
		if t, ok := parsed["token"].(string); ok && t != "" {
			apiKey = t
		}
	}
	if apiKey == "" {
		if at, ok := parsed["access_token"].(string); ok && at != "" {
			apiKey = at
		}
	}
	if apiKey == "" {
		return nil
	}
	return &extractedAuth{
		provider: "codex",
		filename: "codex.json",
		data: map[string]any{
			"type":    "codex",
			"api_key": apiKey,
			"metadata": map[string]any{
				"source": "local-codex-cli",
			},
		},
	}
}

func extractClaude(authDir string) *extractedAuth {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(home, ".claude", "auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	apiKey, _ := parsed["api_key"].(string)
	if apiKey == "" {
		if t, ok := parsed["token"].(string); ok && t != "" {
			apiKey = t
		}
	}
	if apiKey == "" {
		if at, ok := parsed["access_token"].(string); ok && at != "" {
			apiKey = at
		}
	}
	if apiKey == "" {
		return nil
	}
	return &extractedAuth{
		provider: "claude",
		filename: "claude.json",
		data: map[string]any{
			"type":    "claude",
			"api_key": apiKey,
			"metadata": map[string]any{
				"source": "local-claude-cli",
			},
		},
	}
}

func extractCursor(authDir string) *extractedAuth {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(home, ".cursor", "auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	apiKey, _ := parsed["api_key"].(string)
	if apiKey == "" {
		if t, ok := parsed["token"].(string); ok && t != "" {
			apiKey = t
		}
	}
	if apiKey == "" {
		if at, ok := parsed["access_token"].(string); ok && at != "" {
			apiKey = at
		}
	}
	if apiKey == "" {
		return nil
	}
	return &extractedAuth{
		provider: "cursor",
		filename: "cursor.json",
		data: map[string]any{
			"type":    "cursor",
			"api_key": apiKey,
			"metadata": map[string]any{
				"source": "local-cursor",
			},
		},
	}
}

func extractGemini(authDir string) *extractedAuth {
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
	return &extractedAuth{
		provider: "gemini",
		filename: "gemini.json",
		data: map[string]any{
			"type":    "gemini",
			"api_key": apiKey,
		},
	}
}

func extractOpenAI(authDir string) *extractedAuth {
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
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil
		}
		apiKey, _ = parsed["api_key"].(string)
		if apiKey == "" {
			if t, ok := parsed["token"].(string); ok && t != "" {
				apiKey = t
			}
		}
		if apiKey == "" {
			if at, ok := parsed["access_token"].(string); ok && at != "" {
				apiKey = at
			}
		}
	}
	if apiKey == "" {
		return nil
	}
	return &extractedAuth{
		provider: "openai-compatibility",
		filename: "openai.json",
		data: map[string]any{
			"type":        "openai-compatibility",
			"api_key":     apiKey,
			"base_url":    "https://api.openai.com",
			"compat_name": "openai",
		},
	}
}

func defaultString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var _ = codex.ParseJWTToken
