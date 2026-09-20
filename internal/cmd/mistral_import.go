// Package cmd contains CLI helpers. This file implements importing a Mistral
// API key into the auth store as an OpenAI-compatibility credential.
package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// MistralImportOptions controls the mistral-import flow.
type MistralImportOptions struct {
	// APIKey, when non-empty, overrides discovery.
	APIKey string
	// VibeEnvPath overrides the default ~/.vibe/.env lookup path.
	VibeEnvPath string
	// Label customizes the saved file name; defaults to "default".
	Label string
}

// DoMistralImport reads a Mistral API key and persists it as a "mistral"
// provider credential. The file synthesizer routes the saved record through the
// existing OpenAI-compatibility executor, so no dedicated executor, OAuth flow
// or refresh registry entry is required: the key is permanent and the chat
// completions API is OpenAI-compatible.
//
// Discovery order: APIKey option > MISTRAL_API_KEY env var > the Mistral Vibe
// CLI env file (~/.vibe/.env, or $VIBE_HOME/.env). The first non-empty value
// wins.
func DoMistralImport(cfg *config.Config, opts *MistralImportOptions) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if resolved, errResolve := util.ResolveAuthDir(cfg.AuthDir); errResolve == nil {
		cfg.AuthDir = resolved
	}
	if opts == nil {
		opts = &MistralImportOptions{}
	}

	apiKey, source, errDiscover := discoverMistralAPIKey(opts)
	if errDiscover != nil {
		log.Errorf("mistral-import: %v", errDiscover)
		return
	}

	authDir := strings.TrimSpace(cfg.AuthDir)
	if authDir == "" {
		log.Error("mistral-import: auth directory is empty; cannot save credential")
		return
	}

	// Default to a stable filename so re-running the importer overwrites the
	// existing entry instead of polluting the auth pool with duplicate keys.
	// Pass -mistral-label to keep multiple Mistral identities side by side.
	label := sanitizeFilePart(opts.Label)
	if label == "" {
		label = "default"
	}
	fileName := fmt.Sprintf("mistral-%s.json", label)

	// compat_name is the only routing knob stored: the file synthesizer derives
	// the internal provider key from it, the same way a config-declared
	// openai-compatibility entry is resolved.
	metadata := map[string]any{
		"type":        util.MistralProvider,
		"api_key":     apiKey,
		"base_url":    util.MistralDefaultBaseURL,
		"compat_name": util.MistralProvider,
		"label":       label,
	}
	record := &coreauth.Auth{
		ID:       fileName,
		Provider: util.MistralProvider,
		FileName: fileName,
		Metadata: metadata,
	}

	store := sdkAuth.GetTokenStore()
	if setter, ok := store.(interface{ SetBaseDir(string) }); ok {
		setter.SetBaseDir(authDir)
	}
	path, errSave := store.Save(context.Background(), record)
	if errSave != nil {
		log.Errorf("mistral-import: save credential failed: %v", errSave)
		return
	}

	fmt.Printf("Mistral API key imported from %s\n", source)
	fmt.Printf("Credential saved to %s\n", path)
	fmt.Println("Models served through the OpenAI-compatibility executor:")
	for _, model := range registry.GetMistralModels() {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			continue
		}
		fmt.Printf("  - %s\n", model.ID)
	}
	fmt.Printf("To serve a different model list, declare an openai-compatibility entry named %q\n", util.MistralProvider)
	fmt.Println("in config.yaml; it takes precedence over these defaults.")
}

// discoverMistralAPIKey resolves the API key and reports where it came from.
func discoverMistralAPIKey(opts *MistralImportOptions) (apiKey string, source string, err error) {
	if key := strings.TrimSpace(opts.APIKey); key != "" {
		return key, "-mistral-api-key flag", nil
	}
	if key := strings.TrimSpace(os.Getenv("MISTRAL_API_KEY")); key != "" {
		return key, "MISTRAL_API_KEY env var", nil
	}
	envPath, errPath := resolveVibeEnvPath(opts.VibeEnvPath)
	if errPath != nil {
		return "", "", errPath
	}
	envMap, errRead := godotenv.Read(envPath)
	if errRead != nil {
		return "", "", fmt.Errorf("read %s failed: %w; %s", envPath, errRead, mistralKeyHint)
	}
	if key := strings.TrimSpace(envMap["MISTRAL_API_KEY"]); key != "" {
		return key, envPath, nil
	}
	return "", "", fmt.Errorf("no API key found in %s; %s", envPath, mistralKeyHint)
}

// mistralKeyHint tells the user how to supply a key when discovery fails.
const mistralKeyHint = "sign in with the Mistral Vibe CLI, set MISTRAL_API_KEY, or pass -mistral-api-key"

// resolveVibeEnvPath returns the Mistral Vibe CLI env file to read. A leading
// "~" in the override is expanded, matching how auth-dir is resolved.
func resolveVibeEnvPath(override string) (string, error) {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return expandHomeDir(trimmed)
	}
	if vibeHome := strings.TrimSpace(os.Getenv("VIBE_HOME")); vibeHome != "" {
		return filepath.Join(vibeHome, ".env"), nil
	}
	home, errHome := os.UserHomeDir()
	if errHome != nil {
		return "", fmt.Errorf("resolve home dir: %w", errHome)
	}
	return filepath.Join(home, ".vibe", ".env"), nil
}

// expandHomeDir expands a leading "~" or "~/" to the user's home directory.
// "~user" forms are left untouched.
func expandHomeDir(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, "~\\") {
		return path, nil
	}
	home, errHome := os.UserHomeDir()
	if errHome != nil {
		return "", fmt.Errorf("resolve home dir: %w", errHome)
	}
	return filepath.Join(home, path[1:]), nil
}
