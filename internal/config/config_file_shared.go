//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows

package config

import "sync"

var configFileGate sync.RWMutex

func lockLocalConfigFile(write bool) func() {
	if write {
		configFileGate.Lock()
		return configFileGate.Unlock
	}
	configFileGate.RLock()
	return configFileGate.RUnlock
}
