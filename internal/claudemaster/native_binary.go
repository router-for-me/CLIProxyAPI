package claudemaster

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// resolveNativeBinary selects the user's installed Claude executable without
// changing or downloading it. Resolve the launcher symlink before starting so
// an updater changing that symlink cannot swap the binary during this run.
func resolveNativeBinary(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", errors.New("native Claude selection requires a context")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	candidate, err := exec.LookPath("claude")
	if err != nil {
		return "", errors.New("native Claude Code is not installed or not on PATH")
	}
	return installedNativeBinary(ctx, candidate)
}

func installedNativeBinary(ctx context.Context, candidate string) (string, error) {
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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return resolved, nil
}
