package auth

import (
	"sync"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

var (
	storeMu         sync.RWMutex
	registeredStore coreauth.Store
)

// RegisterTokenStore sets the global token store used by the authentication helpers.
func RegisterTokenStore(store coreauth.Store) {
	storeMu.Lock()
	registeredStore = store
	storeMu.Unlock()
}

// GetTokenStore returns the globally registered token store.
func GetTokenStore() coreauth.Store {
	storeMu.RLock()
	s := registeredStore
	storeMu.RUnlock()
	if s != nil {
		return s
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	if registeredStore == nil {
		registeredStore = NewFileTokenStore()
	}
	return registeredStore
}
