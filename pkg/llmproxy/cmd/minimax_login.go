package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

const minimaxAuthFileName = "minimax-api-key.json"

// DoMinimaxLogin prompts for MiniMax API key and stores it in auth-dir (same primitives as OAuth providers).
// Writes a JSON file to auth-dir and adds a minimax: block with token-file pointing to it.
func DoMinimaxLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	var apiKey string
	if options.Prompt != nil {
		var err error
		apiKey, err = options.Prompt("Enter MiniMax API key (from platform.minimax.io): ")
		if err != nil {
			log.Errorf("MiniMax prompt failed: %v", err)
			return
		}
	} else {
		fmt.Print("Enter MiniMax API key (from platform.minimax.io): ")
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			log.Error("MiniMax: failed to read API key")
			return
		}
		apiKey = strings.TrimSpace(scanner.Text())
	}

	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		log.Error("MiniMax: API key cannot be empty")
		return
	}

	authDir, err := ensureAuthDir(strings.TrimSpace(cfg.AuthDir), "MiniMax")
	if err != nil {
		log.Errorf("MiniMax: %v", err)
		return
	}

	tokenPath := filepath.Join(authDir, minimaxAuthFileName)
	tokenData := map[string]string{"api_key": apiKey}
	raw, err := json.MarshalIndent(tokenData, "", "  ")
	if err != nil {
		log.Errorf("MiniMax: failed to marshal token: %v", err)
		return
	}
	if err := os.WriteFile(tokenPath, raw, 0o600); err != nil {
		log.Errorf("MiniMax: failed to write token file %s: %v", tokenPath, err)
		return
	}

	// Use token-file (same primitive as OAuth providers); do not store raw key in config.
	// Prefer portable ~ path when under default auth-dir for consistency with config.example.
	tokenFileRef := authDirTokenFileRef(authDir, minimaxAuthFileName)

	entry := config.MiniMaxKey{
		TokenFile: tokenFileRef,
		BaseURL:   "https://api.minimax.chat/v1",
	}
	if len(cfg.MiniMaxKey) == 0 {
		cfg.MiniMaxKey = []config.MiniMaxKey{entry}
	} else {
		cfg.MiniMaxKey[0] = entry
	}

	configPath := options.ConfigPath
	if configPath == "" {
		log.Error("MiniMax: config path not set; cannot save")
		return
	}

	if err := config.SaveConfigPreserveComments(configPath, cfg); err != nil {
		log.Errorf("MiniMax: failed to save config: %v", err)
		return
	}

	fmt.Printf("MiniMax API key saved to %s (auth-dir). Config updated with token-file. Restart the proxy to apply.\n", tokenPath)
}
