package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/util"
)

func resolveAuthDir(cfgAuthDir string) (string, error) {
	resolved, err := util.ResolveAuthDirOrDefault(cfgAuthDir)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func ensureAuthDir(cfgAuthDir string, provider string) (string, error) {
	authDir, err := resolveAuthDir(cfgAuthDir)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return "", err
	}

	info, err := os.Stat(authDir)
	if err != nil {
		return "", fmt.Errorf("%s auth-dir %q: %v", provider, authDir, err)
	}

	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		return "", fmt.Errorf("%s auth-dir %q mode %04o is too permissive; use chmod 700", provider, authDir, mode)
	}

	return authDir, nil
}

func authDirTokenFileRef(authDir string, fileName string) string {
	tokenPath := filepath.Join(authDir, fileName)
	authAbs, err := filepath.Abs(authDir)
	if err != nil {
		return tokenPath
	}
	tokenAbs := filepath.Join(authAbs, fileName)

	home, err := os.UserHomeDir()
	if err != nil {
		return tokenPath
	}

	rel, errRel := filepath.Rel(home, tokenAbs)
	if errRel != nil {
		return tokenPath
	}

	if rel == "." {
		return "~/" + filepath.ToSlash(fileName)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return tokenPath
	}

	return "~/" + filepath.ToSlash(rel)
}
