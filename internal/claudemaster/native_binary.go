package claudemaster

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// resolveNativeBinary selects an already installed, reviewed executable without
// changing the user's global Claude launcher or downloading another version.
// Resolve the launcher's symlink before checking its version, so a background
// updater changing that launcher cannot select a different binary at execution.
func resolveNativeBinary(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", errors.New("native Claude selection requires a context")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if candidate, err := exec.LookPath("claude"); err == nil {
		if resolved, err := reviewedNativeBinary(ctx, candidate); err == nil {
			return resolved, nil
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir != "" {
		if !filepath.IsAbs(dataDir) {
			return "", errors.New("XDG_DATA_HOME must be absolute when selecting the reviewed native Claude executable")
		}
	} else {
		homeDir, err := os.UserHomeDir()
		if err != nil || homeDir == "" || !filepath.IsAbs(homeDir) {
			return "", errors.New("cannot locate the current user's installed native Claude versions")
		}
		dataDir = filepath.Join(homeDir, ".local", "share")
	}
	installed := filepath.Join(dataDir, "claude", "versions", NativeClaudeVersion)
	if resolved, err := reviewedNativeBinary(ctx, installed); err == nil {
		return resolved, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", errors.New("reviewed native Claude Code " + NativeClaudeVersion + " is not installed; no unreviewed version was selected")
}

func reviewedNativeBinary(ctx context.Context, candidate string) (string, error) {
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", errors.New("cannot resolve installed native Claude executable")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", errors.New("cannot resolve installed native Claude executable")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("installed native Claude is not an executable file")
	}
	if err := verifyNativeVersion(ctx, resolved); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return resolved, nil
}
