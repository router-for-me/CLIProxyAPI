package util

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
)

const DefaultAuthDir = config.DefaultAuthDir

func ResolveAuthDirOrDefault(authDir string) (string, error) {
	authDir = strings.TrimSpace(authDir)
	if authDir == "" {
		authDir = DefaultAuthDir
	}
	if strings.HasPrefix(authDir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if home != "" {
			return filepath.Join(home, strings.TrimPrefix(authDir, "~/")), nil
		}
	}
	return authDir, nil
}
