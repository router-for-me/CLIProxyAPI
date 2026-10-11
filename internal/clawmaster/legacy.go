package clawmaster

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Names from before the project was renamed claw-master (2026-10). Each is still read, answered or
// stripped, so servers, client boxes and their configuration can move to the new names in any order.
const (
	legacyEnvPrefix     = "CLAUDE_MASTER_"
	legacyProjectHeader = "X-Claude-Master-Project"
	legacyProbePath     = "/.claude-master/probe"
	legacyProbeHeader   = "X-Claude-Master"
	legacyDataDir       = "claude-master"
)

// LegacyBackupAPIKeyEnvironment is BackupAPIKeyEnvironment's name before the rename: read when
// that one is unset, and always kept out of the launched Claude's environment.
const LegacyBackupAPIKeyEnvironment = "CLAUDE_MASTER_BACKUP_API_KEY"

// Getenv is the environment variable CLAW_MASTER_<name>, or CLAUDE_MASTER_<name> (its name before
// the rename) when that is unset or empty.
func Getenv(name string) string {
	if value := os.Getenv("CLAW_MASTER_" + name); value != "" {
		return value
	}
	return os.Getenv(legacyEnvPrefix + name)
}

// dataDir is this user's claw-master data directory under share: claw-master, or claude-master
// (its name before the rename) while only that one exists, so existing profiles and logins keep
// working until it is moved.
func dataDir(share string) string {
	current := filepath.Join(share, "claw-master")
	if _, err := os.Lstat(current); !errors.Is(err, fs.ErrNotExist) {
		return current
	}
	legacy := filepath.Join(share, legacyDataDir)
	if info, err := os.Lstat(legacy); err == nil && info.IsDir() {
		return legacy
	}
	return current
}
