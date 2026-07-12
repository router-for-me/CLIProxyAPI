// Package extractor discovers local credentials for coding agents and AI subscriptions.
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

	var found []string
	seen := make(map[string]bool)
	for _, src := range sources {
		if auth := extractFromSource(src); auth != nil {
			if seen[auth.Provider] {
				continue
			}
			seen[auth.Provider] = true
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

func extractFromSource(src providerSource) *ExtractedAuth {
	home := userHome()

	// Check environment variable first.
	apiKey := ""
	if src.EnvKey != "" {
		apiKey = os.Getenv(src.EnvKey)
	}

	// Search dotenv files for the env key.
	if apiKey == "" && src.EnvKey != "" {
		apiKey = findEnvKey(home, src.EnvKey, src.Files)
	}

	// Try to read auth files.
	var data []byte
	var path string
	if apiKey == "" && len(src.Files) > 0 {
		data, path = readFirstFile(home, src.Files)
		if data != nil {
			apiKey = extractKeyFromJSON(data, src.JSONKeys...)
			if apiKey == "" {
				// Try TOML for windsurf-style credentials.
				apiKey = extractKeyFromTOML(data, src.JSONKeys...)
			}
		}
	}

	if apiKey == "" {
		// Try macOS keychain as a last resort.
		apiKey = keychainFind(src.Provider)
	}

	if apiKey == "" {
		return nil
	}

	// Preserve the original file content when possible for OAuth providers.
	dataToUse := data
	if dataToUse == nil {
		dataToUse = []byte(`{}`)
	}

	var parsed map[string]any
	if err := json.Unmarshal(dataToUse, &parsed); err != nil || parsed == nil {
		parsed = make(map[string]any)
	}
	if _, ok := parsed["type"]; !ok {
		parsed["type"] = src.Type
	}
	if _, ok := parsed["api_key"]; !ok {
		parsed["api_key"] = apiKey
	}
	if src.BaseURL != "" {
		if _, ok := parsed["base_url"]; !ok {
			parsed["base_url"] = src.BaseURL
		}
	}
	if src.CompatName != "" {
		if _, ok := parsed["compat_name"]; !ok {
			parsed["compat_name"] = src.CompatName
		}
	}
	if path != "" {
		if _, ok := parsed["source"]; !ok {
			parsed["source"] = path
		}
	}

	filename := src.Provider + ".json"
	if src.Provider == "claude-code" {
		filename = "claude.json"
	}
	if src.Provider == "vscode-copilot" {
		filename = "copilot.json"
	}

	return &ExtractedAuth{
		Provider: src.Provider,
		Filename: filename,
		Data:     parsed,
	}
}

func extractKeyFromTOML(data []byte, keys ...string) string {
	var cfg map[string]any
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	for _, key := range keys {
		if v, ok := cfg[key].(string); ok && v != "" {
			return v
		}
	}
	return findStringInJSON(cfg, keys)
}

// keychainFind tries to read a generic password from the macOS keychain by service name.
func keychainFind(service string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	if service == "" {
		return ""
	}
	candidates := []string{service, "com." + service, "ai." + service, "org." + service}
	for _, s := range candidates {
		out, err := execCmd("security", "find-generic-password", "-s", s, "-w")
		if err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out)
		}
	}
	return ""
}

func execCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	return string(out), err
}

