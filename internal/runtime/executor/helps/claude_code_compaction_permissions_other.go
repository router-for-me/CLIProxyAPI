//go:build !windows

package helps

import "os"

func secureClaudeCodeCompactionStateDirectory(path string) error {
	return os.Chmod(path, claudeCodeCompactionStateDirectoryMode)
}

func secureClaudeCodeCompactionStateFile(path string) error {
	return os.Chmod(path, claudeCodeCompactionStateFileMode)
}
