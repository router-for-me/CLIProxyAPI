//go:build !windows

package config

import "os"

func openConfigFileForRemoval(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR, 0)
}
