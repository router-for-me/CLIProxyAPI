package config

import (
	"os"
	"sync"
)

// configFileMu orders config file writes against reads within this process, so
// a reader such as the config watcher never sees a truncated or half-written file.
var configFileMu sync.RWMutex

// ReadConfigFile reads a config file. It never overlaps a WriteConfigFile call,
// so it returns either the previous or the new content of a concurrent write.
func ReadConfigFile(path string) ([]byte, error) {
	configFileMu.RLock()
	defer configFileMu.RUnlock()
	return os.ReadFile(path)
}

// WriteConfigFile rewrites a config file and syncs it to disk. The file is
// written in place to keep the inode of a mounted configuration file.
func WriteConfigFile(path string, data []byte, perm os.FileMode) error {
	configFileMu.Lock()
	defer configFileMu.Unlock()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if errClose := f.Close(); err == nil {
		err = errClose
	}
	return err
}
