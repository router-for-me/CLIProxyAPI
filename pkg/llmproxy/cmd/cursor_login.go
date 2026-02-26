package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kooshapari/cliproxyapi-plusplus/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

const (
	defaultCursorAPIURL        = "http://127.0.0.1:3000"
	defaultCursorTokenFilePath = "~/.cursor/session-token.txt"
)

// DoCursorLogin configures Cursor credentials in the local config file.
func DoCursorLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}
	if cfg == nil {
		cfg = &config.Config{}
	}

	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = defaultProjectPrompt()
	}

	mode, err := promptFn("Cursor auth mode [1] token-file, [2] zero-action from Cursor IDE: ")
	if err != nil {
		log.Errorf("Cursor login canceled: %v", err)
		return
	}

	apiURL, err := promptCursorURL(promptFn)
	if err != nil {
		log.Errorf("Cursor login canceled: %v", err)
		return
	}

	modeTokenFile := isCursorTokenFileMode(mode)
	entry := config.CursorKey{CursorAPIURL: apiURL}

	if modeTokenFile {
		if err := applyCursorTokenFileMode(promptFn, &entry); err != nil {
			log.Errorf("Cursor token-file login failed: %v", err)
			return
		}
	} else {
		if err := applyCursorZeroActionMode(promptFn, &entry); err != nil {
			log.Errorf("Cursor zero-action login failed: %v", err)
			return
		}
	}

	if len(cfg.CursorKey) == 0 {
		cfg.CursorKey = []config.CursorKey{entry}
	} else {
		cfg.CursorKey[0] = entry
	}

	configPath := strings.TrimSpace(options.ConfigPath)
	if configPath == "" {
		log.Errorf("Cursor login requires config path; pass --config=<path> before running login")
		return
	}

	if err := config.SaveConfigPreserveComments(configPath, cfg); err != nil {
		log.Errorf("Failed to save cursor config: %v", err)
		return
	}

	fmt.Printf("Cursor config saved to %s. Restart the proxy to apply it.\n", configPath)
}

func isCursorTokenFileMode(raw string) bool {
	choice := strings.ToLower(strings.TrimSpace(raw))
	return choice != "2" && choice != "zero" && choice != "zero-action"
}

func promptCursorURL(promptFn func(string) (string, error)) (string, error) {
	candidateURL, err := promptFn(fmt.Sprintf("Cursor API URL [%s]: ", defaultCursorAPIURL))
	if err != nil {
		return "", err
	}
	candidateURL = strings.TrimSpace(candidateURL)
	if candidateURL == "" {
		return defaultCursorAPIURL, nil
	}
	return candidateURL, nil
}

func applyCursorZeroActionMode(promptFn func(string) (string, error), entry *config.CursorKey) error {
	entry.TokenFile = ""

	candidateToken, err := promptFn("Cursor auth-token (required for zero-action): ")
	if err != nil {
		return err
	}
	candidateToken = strings.TrimSpace(candidateToken)
	if candidateToken == "" {
		return fmt.Errorf("auth-token cannot be empty")
	}

	entry.AuthToken = candidateToken
	return nil
}

func applyCursorTokenFileMode(promptFn func(string) (string, error), entry *config.CursorKey) error {
	token, err := promptFn("Cursor token (from cursor-api /build-key): ")
	if err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("token cannot be empty")
	}

	tokenFile, err := promptFn(fmt.Sprintf("Token-file path [%s]: ", defaultCursorTokenFilePath))
	if err != nil {
		return err
	}
	tokenFile = strings.TrimSpace(tokenFile)
	if tokenFile == "" {
		tokenFile = defaultCursorTokenFilePath
	}

	tokenPath, err := resolveAndWriteCursorTokenFile(tokenFile, token)
	if err != nil {
		return err
	}

	entry.TokenFile = tokenPath
	entry.AuthToken = ""
	return nil
}

func resolveAndWriteCursorTokenFile(rawPath, token string) (string, error) {
	resolved, err := resolveCursorPathForWrite(rawPath)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(resolved), 0o700); err != nil {
		return "", fmt.Errorf("create token directory: %w", err)
	}

	if err := os.WriteFile(resolved, []byte(strings.TrimSpace(token)+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write token file: %w", err)
	}

	return cursorTokenPathForConfig(resolved), nil
}

func resolveCursorPathForWrite(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("path cannot be empty")
	}
	if strings.HasPrefix(trimmed, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		remainder := strings.TrimPrefix(trimmed, "~")
		remainder = strings.ReplaceAll(remainder, "\\", "/")
		remainder = strings.TrimLeft(remainder, "/")
		if remainder == "" {
			return filepath.Clean(home), nil
		}
		return filepath.Clean(filepath.Join(home, filepath.FromSlash(remainder))), nil
	}

	return filepath.Clean(trimmed), nil
}

func cursorTokenPathForConfig(resolved string) string {
	if home, err := os.UserHomeDir(); err == nil {
		rel, relErr := filepath.Rel(home, resolved)
		if relErr == nil {
			cleanRel := filepath.Clean(rel)
			if cleanRel != "." && cleanRel != ".." && !strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
				return "~/" + filepath.ToSlash(cleanRel)
			}
		}
	}

	return filepath.Clean(resolved)
}
